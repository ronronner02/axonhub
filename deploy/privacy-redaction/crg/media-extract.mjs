// 本地封装：在 CRG JSON.parse 之前把超大媒体字符串从 JSON 文本里抠走，
// 脱敏后再拼回。不修改 vendor worker.js。
//
// 只扫描 JSON 字符串字面量（O(n)），不 parse 整棵树，所以十几 MB 的图不会进 V8 JSON 解析器。

const DEFAULT_MIN_CHARS = 4096;
const MEDIA_KEYS = new Set(["image_url", "input_image", "input_audio", "file_data", "b64_json"]);

// Upstream control identifiers that must NOT be redacted. CRG's generic
// detectors (especially high-entropy) happily rewrite a UUID-shaped
// prompt_cache_key into a 99-char {{Redact:…}} placeholder: OpenAI rejects it
// (max 64), and redaction would also break prompt caching and Responses
// chaining even under the limit (the runtime salt changes every restart).
// These fields carry no free-form user content, so we lift them out before
// redaction and splice the originals back before forwarding — the same
// extract→restore trick media uses, with vendor worker.js left untouched.
export const CONTROL_KEYS = new Set([
  "prompt_cache_key",
  "previous_response_id",
  "safety_identifier",
  "encrypted_content",
]);

function controlPath(path) {
  const parts = Array.isArray(path) ? path : [path];
  const key = String(parts.at(-1) || "").toLowerCase();
  return CONTROL_KEYS.has(key);
}

export function isJsonContentType(ct) {
  return /(^|[+/])json(?:$|[; ])/i.test(ct || "") || /application\/.*\+json/i.test(ct || "");
}

export function randomMediaId() {
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

export function mediaToken(id, index) {
  return `__CRG_M_${id}_${index}__`;
}

function copySlice(s) {
  if (s.length <= 256) return s;
  return Buffer.from(s, "utf8").toString("utf8");
}

export function scanJsonString(s, start) {
  if (s.charCodeAt(start) !== 34) return null;
  let i = start + 1;
  const n = s.length;
  while (i < n) {
    const c = s.charCodeAt(i);
    if (c === 92) {
      const next = s.charCodeAt(i + 1);
      if (next === 117) i += 6;
      else i += 2;
      continue;
    }
    if (c === 34) return { end: i + 1, rawInner: s.slice(start + 1, i) };
    i++;
  }
  return null;
}

export function isDataUri(rawInner) {
  return /^data:[a-zA-Z0-9/+.=; -]*;base64,/i.test(rawInner || "");
}

export function looksLikePackedCharset(rawInner, minChars = DEFAULT_MIN_CHARS) {
  if (!rawInner) return false;
  const compact = rawInner.replace(/\\[nrt]/g, "").replace(/\s+/g, "");
  return compact.length >= minChars && /^[A-Za-z0-9+/=]+$/.test(compact);
}

export function isMediaPayload(rawInner) {
  return isDataUri(rawInner) || looksLikePackedCharset(rawInner);
}

export function precedingKey(s, stringStart) {
  let i = stringStart - 1;
  while (i >= 0 && (s.charCodeAt(i) === 32 || s.charCodeAt(i) === 9 || s.charCodeAt(i) === 10 || s.charCodeAt(i) === 13)) i--;
  if (i < 0 || s[i] !== ":") return "";
  i--;
  while (i >= 0 && (s.charCodeAt(i) === 32 || s.charCodeAt(i) === 9 || s.charCodeAt(i) === 10 || s.charCodeAt(i) === 13)) i--;
  if (i < 0 || s[i] !== '"') return "";
  const end = i;
  i--;
  while (i >= 0 && s[i] !== '"') i--;
  if (i < 0) return "";
  return s.slice(i + 1, end);
}

function mediaPath(path) {
  const parts = Array.isArray(path) ? path : [path];
  const key = String(parts.at(-1) || "").toLowerCase();
  if (MEDIA_KEYS.has(key)) return true;
  if (parts.length >= 2) {
    const parent = String(parts.at(-2) || "").toLowerCase();
    if (parent === "image_url" && key === "url") return true;
    if ((parent === "source" || parent === "image") && key === "data") return true;
  }
  return false;
}

export function shouldExtract(rawInner, path, minChars = DEFAULT_MIN_CHARS) {
  if (!rawInner || rawInner.length < minChars) return false;
  return mediaPath(path) && (isDataUri(rawInner) || looksLikePackedCharset(rawInner, minChars));
}

// Decide whether a parsed JSON string should be lifted out before redaction,
// and why. "media" = large media payload (size-based, keeps multi-MB blobs out
// of the vendor's JSON.parse + regex scan). "control" = an upstream control
// identifier that must survive verbatim (see CONTROL_KEYS). The two key sets
// are disjoint, so a string is at most one kind.
function spanKind(rawInner, path, minChars, opts) {
  if (opts.control && rawInner && controlPath(path)) return "control";
  if (opts.media && shouldExtract(rawInner, path, minChars)) return "media";
  return null;
}

function isWhitespace(code) {
  return code === 0x20 || code === 0x09 || code === 0x0a || code === 0x0d;
}

function decodeKey(rawInner) {
  try {
    // 仅解码当前对象键（不是整个请求体），用于路径匹配；原始 JSON 片段仍原样保留。
    return JSON.parse(`"${rawInner}"`);
  } catch {
    return null;
  }
}

function parseJsonForSpans(jsonText, minChars, opts) {
  const found = [];
  let i = 0;

  const skipWhitespace = () => {
    while (i < jsonText.length && isWhitespace(jsonText.charCodeAt(i))) i++;
  };

  const parseString = () => {
    const start = i;
    if (jsonText.charCodeAt(i) !== 34) throw new SyntaxError("expected string");
    i++;
    while (i < jsonText.length) {
      const c = jsonText.charCodeAt(i++);
      if (c === 34) return { start, end: i, rawInner: jsonText.slice(start + 1, i - 1) };
      if (c < 0x20) throw new SyntaxError("control character in string");
      if (c !== 92) continue;
      if (i >= jsonText.length) throw new SyntaxError("unterminated escape");
      const esc = jsonText.charCodeAt(i++);
      if (esc === 117) {
        if (i + 4 > jsonText.length || !/^[0-9a-f]{4}$/i.test(jsonText.slice(i, i + 4))) {
          throw new SyntaxError("invalid unicode escape");
        }
        i += 4;
      } else if (![34, 92, 47, 98, 102, 110, 114, 116].includes(esc)) {
        throw new SyntaxError("invalid escape");
      }
    }
    throw new SyntaxError("unterminated string");
  };

  const parseValue = (path) => {
    skipWhitespace();
    const c = jsonText.charCodeAt(i);
    if (c === 34) {
      const value = parseString();
      const kind = spanKind(value.rawInner, path, minChars, opts);
      if (kind) found.push({ ...value, path, kind });
      return;
    }
    if (c === 123) return parseObject(path);
    if (c === 91) return parseArray(path);
    for (const literal of ["true", "false", "null"]) {
      if (jsonText.startsWith(literal, i)) { i += literal.length; return; }
    }
    const number = jsonText.slice(i).match(/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?/);
    if (number) { i += number[0].length; return; }
    throw new SyntaxError("invalid value");
  };

  const parseObject = (path) => {
    i++;
    skipWhitespace();
    if (jsonText.charCodeAt(i) === 125) { i++; return; }
    while (true) {
      skipWhitespace();
      const key = parseString();
      const decoded = decodeKey(key.rawInner);
      if (decoded === null) throw new SyntaxError("invalid object key");
      skipWhitespace();
      if (jsonText.charCodeAt(i++) !== 58) throw new SyntaxError("expected colon");
      parseValue(path.concat(decoded));
      skipWhitespace();
      const delimiter = jsonText.charCodeAt(i++);
      if (delimiter === 125) return;
      if (delimiter !== 44) throw new SyntaxError("expected object delimiter");
    }
  };

  const parseArray = (path) => {
    i++;
    skipWhitespace();
    if (jsonText.charCodeAt(i) === 93) { i++; return; }
    while (true) {
      parseValue(path);
      skipWhitespace();
      const delimiter = jsonText.charCodeAt(i++);
      if (delimiter === 93) return;
      if (delimiter !== 44) throw new SyntaxError("expected array delimiter");
    }
  };

  try {
    skipWhitespace();
    parseValue([]);
    skipWhitespace();
    if (i !== jsonText.length) throw new SyntaxError("trailing JSON data");
    return found;
  } catch {
    return null;
  }
}

// Core: walk the JSON text once and lift out every span the requested kinds
// match, replacing each with a short, redaction-safe placeholder token. Returns
// the rewritten text plus the original quoted substrings (by index) for later
// restore. On any parse failure the input is returned untouched (spans empty),
// so a malformed body is never "repaired" into different JSON.
function extractSpans(jsonText, options = {}) {
  const minChars = Number.isFinite(options.minChars) && options.minChars > 0
    ? Math.floor(options.minChars)
    : DEFAULT_MIN_CHARS;
  const id = options.id || randomMediaId();
  const opts = { media: options.media !== false, control: options.control === true };
  const spans = [];
  if (typeof jsonText !== "string" || jsonText.length === 0) {
    return { text: jsonText, spans, id };
  }

  const found = parseJsonForSpans(jsonText, minChars, opts);
  if (!found) return { text: jsonText, spans, id };
  let cursor = 0;
  let out = "";
  for (const value of found) {
    out += jsonText.slice(cursor, value.start);
    const token = mediaToken(id, spans.length);
    spans.push(copySlice(jsonText.slice(value.start, value.end)));
    out += `"${token}"`;
    cursor = value.end;
  }
  out += jsonText.slice(cursor);
  return { text: out, spans, id };
}

// Back-compat media-only extraction. Existing callers/tests rely on the
// `{ text, media, id }` shape and on control fields NOT being touched here.
export function extractLargeJsonStrings(jsonText, options = {}) {
  const { text, spans, id } = extractSpans(jsonText, {
    minChars: options.minChars,
    id: options.id,
    media: true,
    control: false,
  });
  return { text, media: spans, id };
}

// Request-path extraction: size-based media stripping (toggleable) plus
// always-on control-field protection. Used by entry.mjs.
export function extractProtectedSpans(jsonText, options = {}) {
  return extractSpans(jsonText, {
    minChars: options.minChars,
    id: options.id,
    media: options.media !== false,
    control: options.control !== false,
  });
}

function restoreTokens(jsonText, list, id) {
  if (!list || list.length === 0) return jsonText;
  let out = jsonText;
  for (let i = 0; i < list.length; i++) {
    const token = `"${mediaToken(id, i)}"`;
    const idx = out.indexOf(token);
    if (idx < 0) continue;
    out = out.slice(0, idx) + list[i] + out.slice(idx + token.length);
  }
  return out;
}

export function restoreMedia(jsonText, extracted) {
  if (!extracted) return jsonText;
  return restoreTokens(jsonText, extracted.media, extracted.id);
}

export function restoreSpans(jsonText, extracted) {
  if (!extracted) return jsonText;
  return restoreTokens(jsonText, extracted.spans, extracted.id);
}

export function mediaExtractEnabled(env = {}) {
  const v = env.REDACT_MEDIA_EXTRACT;
  return v !== "0" && v !== "false" && v !== "off";
}

export function mediaMinChars(env = {}) {
  const n = Number(env.REDACT_MEDIA_MIN_CHARS);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : DEFAULT_MIN_CHARS;
}
