import http from "node:http";
import { Readable } from "node:stream";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { handleRequest } from "./worker.js";
import {
  extractProtectedSpans,
  restoreSpans,
  isJsonContentType,
  mediaExtractEnabled,
  mediaMinChars,
} from "./media-extract.mjs";

const port = Number(process.env.PORT || 8787);
const host = process.env.HOST || "127.0.0.1";

function bodyLimit(env) {
  const configured = Number(env.REDACT_MAX_BODY_BYTES);
  return Number.isFinite(configured) && configured > 0 ? Math.floor(configured) : 16 * 1024 * 1024;
}

function bodyTooLarge(limit) {
  return new Response(JSON.stringify({
    error: { message: `Request body exceeds ${limit} bytes`, type: "cosy_redact_gateway_error" },
  }), { status: 413, headers: { "content-type": "application/json; charset=utf-8" } });
}

async function readBodyWithinLimit(request, limit) {
  const declared = Number(request.headers.get("content-length"));
  if (Number.isFinite(declared) && declared > limit) return { tooLarge: true, bytes: null };
  if (!request.body) return { tooLarge: false, bytes: new Uint8Array() };

  const reader = request.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const chunk = value instanceof Uint8Array ? value : new Uint8Array(value);
      total += chunk.byteLength;
      if (total > limit) {
        await reader.cancel("request body exceeds limit").catch(() => {});
        return { tooLarge: true, bytes: null };
      }
      chunks.push(chunk);
    }
  } finally {
    reader.releaseLock();
  }

  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return { tooLarge: false, bytes };
}

export async function dispatch(request, env = process.env, options = {}) {
  const innerFetch = options.fetchImpl || fetch;
  const method = request.method;
  if (method === "GET" || method === "HEAD" || method === "OPTIONS") {
    return handleRequest(request, env, options);
  }

  const limit = bodyLimit(env);
  const read = await readBodyWithinLimit(request, limit);
  if (read.tooLarge) return bodyTooLarge(limit);
  const bytes = read.bytes;
  const headers = new Headers(request.headers);
  headers.delete("content-length");

  const rebuilt = (body) =>
    new Request(request.url, {
      method,
      headers,
      body,
      duplex: body ? "half" : undefined,
    });

  if (!bytes.byteLength) {
    return handleRequest(rebuilt(undefined), env, options);
  }

  const ct = request.headers.get("content-type") || "";
  if (!isJsonContentType(ct)) {
    return handleRequest(rebuilt(bytes), env, options);
  }

  // Lift out large media (size-based, toggleable via REDACT_MEDIA_EXTRACT) AND
  // upstream control identifiers (always on) before the vendor redactor runs,
  // then splice the originals back before forwarding. Control protection must
  // not depend on the media toggle: prompt_cache_key / previous_response_id /
  // safety_identifier are rewritten by CRG's generic detectors regardless of
  // media handling, which breaks upstream contracts (see media-extract.mjs).
  const text = new TextDecoder().decode(bytes);
  const extracted = extractProtectedSpans(text, {
    minChars: mediaMinChars(env),
    media: mediaExtractEnabled(env),
    control: true,
  });
  if (!extracted.spans.length) {
    return handleRequest(rebuilt(bytes), env, options);
  }

  return handleRequest(rebuilt(extracted.text), env, {
    ...options,
    fetchImpl: async (url, init = {}) => {
      const next = { ...init };
      if (typeof next.body === "string") next.body = restoreSpans(next.body, extracted);
      return innerFetch(url, next);
    },
  });
}

const server = http.createServer(async (req, res) => {
  try {
    const origin = `http://${req.headers.host || `${host}:${port}`}`;
    const body = req.method === "GET" || req.method === "HEAD" ? undefined : Readable.toWeb(req);
    const request = new Request(new URL(req.url, origin), {
      method: req.method,
      headers: req.headers,
      body,
      duplex: body ? "half" : undefined,
    });
    const response = await dispatch(request, process.env);
    res.writeHead(response.status, Object.fromEntries(response.headers));
    if (!response.body) return res.end();
    for await (const chunk of Readable.fromWeb(response.body)) res.write(chunk);
    res.end();
  } catch (e) {
    res.statusCode = 500;
    res.end(String(e?.stack || e));
  }
});

function isMain() {
  try {
    return fileURLToPath(import.meta.url) === resolve(process.argv[1]);
  } catch {
    return false;
  }
}

if (isMain()) {
  server.listen(port, host, () => console.log(`cosy-redact-gateway listening on http://${host}:${port}`));
}
