import { test } from "node:test";
import assert from "node:assert/strict";
import {
  scanJsonString,
  isMediaPayload,
  extractLargeJsonStrings,
  restoreMedia,
  mediaToken,
  extractProtectedSpans,
  restoreSpans,
} from "../crg/media-extract.mjs";
import { dispatch } from "../crg/entry.mjs";
import { isHighEntropyBlock } from "../crg/worker.js";
import { createHash } from "node:crypto";

function dataUri(bytes) {
  return "data:image/png;base64," + Buffer.alloc(bytes, 1).toString("base64");
}

test("scanJsonString 读到闭合引号并处理转义", () => {
  const s = '{"a":"he\\"llo"}';
  const start = s.indexOf('"h');
  const parsed = scanJsonString(s, start);
  assert.equal(parsed.rawInner, 'he\\"llo');
  assert.equal(s.slice(start, parsed.end), '"he\\"llo"');
});

test("小字符串不剥离", () => {
  const json = JSON.stringify({ input: [{ type: "input_text", text: "hello sk-test" }] });
  const { text, media } = extractLargeJsonStrings(json, { minChars: 4096, id: "deadbeef" });
  assert.equal(media.length, 0);
  assert.equal(text, json);
});

test("超大 data-URI 被剥离并可无损拼回", () => {
  const uri = dataUri(6000);
  const json = JSON.stringify({
    model: "gpt-5.6-sol",
    input: [
      { type: "custom_tool_call_output", output: [{ type: "input_image", detail: "high", image_url: uri }] },
      { type: "message", role: "user", content: "see screenshot" },
    ],
  });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "aabbccdd" });
  assert.equal(extracted.media.length, 1);
  assert.ok(extracted.text.length < json.length / 4);
  assert.ok(extracted.text.includes(`"${mediaToken("aabbccdd", 0)}"`));
  assert.equal(extracted.text.includes("iVBOR") || extracted.text.includes(uri.slice(0, 40)), false);
  const restored = restoreMedia(extracted.text, extracted);
  assert.equal(restored, json);
});

test("多张大图各自独立占位", () => {
  const a = dataUri(5000);
  const b = dataUri(7000);
  const json = JSON.stringify({ input: [{ image_url: a }, { image_url: b }] });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "11223344" });
  assert.equal(extracted.media.length, 2);
  assert.equal(restoreMedia(extracted.text, extracted), json);
});

test("image_url.url 对象形态可无损剥离", () => {
  const uri = dataUri(6000);
  const json = JSON.stringify({ input: [{ type: "input_image", image_url: { url: uri, detail: "high" } }] });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "55667788" });
  assert.equal(extracted.media.length, 1);
  assert.equal(restoreMedia(extracted.text, extracted), json);
});

test("正文里的长密钥不会被当成图", () => {
  const secret = "sk-" + "A".repeat(5000);
  const json = JSON.stringify({ messages: [{ role: "user", content: secret }] });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "99aa99aa" });
  assert.equal(extracted.media.length, 0);
  assert.equal(extracted.text, json);
});

test("b64_json 字段里的纯 base64 会被剥离", () => {
  const raw = "A".repeat(5000);
  const json = JSON.stringify({ data: [{ b64_json: raw }] });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "b64b64b6" });
  assert.equal(extracted.media.length, 1);
  assert.equal(restoreMedia(extracted.text, extracted), json);
});

test("isMediaPayload 识别 data URI 与纯 base64", () => {
  assert.equal(isMediaPayload("data:image/png;base64," + "A".repeat(80)), true);
  assert.equal(isMediaPayload("A".repeat(4096)), true);
  assert.equal(isMediaPayload("hello-world-" + "A".repeat(4096)), false);
});

test("Anthropic source.data、image.data 与转义键在嵌套数组中无损剥离", () => {
  const raw = "A".repeat(5000);
  const json = JSON.stringify({ messages: [{ content: [
    { source: { data: raw } }, { image: { data: raw } }, { data: raw },
  ] }] }).replace('"source"', '"sou\\u0072ce"');
  const extracted = extractLargeJsonStrings(json, { id: "cafebabe" });
  assert.equal(extracted.media.length, 2);
  assert.equal(restoreMedia(extracted.text, extracted), json);
});

test("普通正文里的长 data URI 不绕开脱敏，也不产生媒体占位符", async () => {
  const bytes = Buffer.from(Array.from({ length: 6000 }, (_, i) => (i * 73 + 19) % 256));
  const uri = "data:text/plain;base64," + bytes.toString("base64");
  const json = JSON.stringify({ model: "x", messages: [{ role: "user", content: uri }] });
  assert.equal(extractLargeJsonStrings(json).media.length, 0);
  let upstreamBody = "";
  const res = await dispatch(new Request("http://redact:8787/$http://echo.example/v1/messages", {
    method: "POST", headers: { "content-type": "application/json" }, body: json,
  }), { REDACT_ALLOWED_HOSTS: "echo.example" }, { fetchImpl: async (_url, init) => {
    upstreamBody = init.body;
    return new Response("{}", { headers: { "content-type": "application/json" } });
  } });
  assert.equal(res.status, 200);
  assert.equal(upstreamBody.includes("__CRG_M_"), false);
  assert.equal(upstreamBody.includes(uri), false);
  assert.match(upstreamBody, /\{\{Redact:[a-f0-9]{64}\}\}/);
});

test("通用 url 字段不按媒体路径剥离", () => {
  const uri = dataUri(6000);
  const json = JSON.stringify({
    model: "x",
    messages: [{ role: "user", content: [{ type: "tool_result", url: uri }] }],
  });
  const extracted = extractLargeJsonStrings(json);
  assert.equal(extracted.media.length, 0);
  assert.equal(extracted.text, json);
});

test("非法 JSON 不因媒体剥离被修成合法 JSON", () => {
  for (const json of [
    '{"image_url":"data:image/png;base64,' + "A".repeat(5000) + '\\q"}',
    '{"image_url":"' + dataUri(5000) + '",}',
  ]) {
    const extracted = extractLargeJsonStrings(json);
    assert.equal(extracted.media.length, 0);
    assert.equal(extracted.text, json);
  }
});

test("dispatch 让上游看到完整图，脱敏层只吃瘦 JSON", async () => {
  const uri = dataUri(6000);
  const secret = "sk-" + "B".repeat(64);
  const payload = {
    model: "x",
    messages: [{ role: "user", content: [{ type: "text", text: `key ${secret}` }, { type: "image_url", image_url: uri }] }],
  };
  let upstreamBody = "";
  const fetchImpl = async (_url, init) => {
    upstreamBody = init.body;
    return new Response(JSON.stringify({ ok: true, echo: "unused" }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
  const req = new Request("http://redact:8787/$http://echo.example/v1/messages", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(payload),
  });
  const res = await dispatch(req, { REDACT_ALLOWED_HOSTS: "echo.example" }, { fetchImpl });
  assert.equal(res.status, 200);
  assert.ok(upstreamBody.includes(uri), "上游必须拿到完整 data-URI");
  assert.equal(upstreamBody.includes(secret), false, "密钥不得出现在上游");
  assert.ok(/\{\{Redact:[a-f0-9]{64}\}\}/.test(upstreamBody), "密钥应变为占位符");
});

test("原始正文超限仍 413，不因剥离而放行", async () => {
  const uri = dataUri(8000);
  const payload = { messages: [{ role: "user", content: [{ type: "image_url", image_url: uri }] }] };
  let forwarded = false;
  const fetchImpl = async () => {
    forwarded = true;
    return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
  };
  const req = new Request("http://redact:8787/$http://echo.example/v1/messages", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(payload),
  });
  const res = await dispatch(
    req,
    { REDACT_ALLOWED_HOSTS: "echo.example", REDACT_MAX_BODY_BYTES: "64" },
    { fetchImpl },
  );
  assert.equal(res.status, 413);
  assert.equal(forwarded, false);
});

test("已知 Content-Length 超限时不读取正文", async () => {
  let getReaderCalls = 0;
  const body = {
    getReader() {
      getReaderCalls++;
      throw new Error("不应读取已知超限正文");
    },
  };
  const req = {
    method: "POST",
    url: "http://redact:8787/$http://echo.example/v1/messages",
    headers: new Headers({ "content-type": "application/json", "content-length": "128" }),
    body,
  };
  const res = await dispatch(req, { REDACT_ALLOWED_HOSTS: "echo.example", REDACT_MAX_BODY_BYTES: "64" });
  assert.equal(res.status, 413);
  assert.equal(getReaderCalls, 0);
  assert.deepEqual(await res.json(), {
    error: { message: "Request body exceeds 64 bytes", type: "cosy_redact_gateway_error" },
  });
});

test("未知长度正文首次超限后取消读取", async () => {
  let pulls = 0;
  let cancelled = false;
  const body = new ReadableStream({
    pull(controller) {
      pulls++;
      controller.enqueue(new Uint8Array(40));
    },
    cancel() {
      cancelled = true;
    },
  });
  const req = new Request("http://redact:8787/$http://echo.example/v1/messages", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body,
    duplex: "half",
  });
  const res = await dispatch(req, { REDACT_ALLOWED_HOSTS: "echo.example", REDACT_MAX_BODY_BYTES: "64" });
  assert.equal(res.status, 413);
  assert.equal(cancelled, true);
  assert.ok(pulls <= 3, `超限后仍读取过多分块: ${pulls}`);
});

// ── 控制字段保护（prompt_cache_key / previous_response_id / safety_identifier）──
// 根因：CRG 通用检测器（尤其 highEntropy）会把 UUID 形态的 prompt_cache_key 末段
// 12 位 hex 改写成 {{Redact:…}}，令整串 36→99 超过上游 64 上限被拒；即便不超限，
// CRG 每次重启换盐也会破坏 prompt 缓存与 Responses 链接。下列用例锁定 wrapper 在
// 脱敏前抠出这些字段、转发前原样拼回，且不改 vendor worker.js。

// 固定且必然高熵的 12 位 hex 末段：sha256 派生，跨运行确定，用作回归守卫。
const TRIP_TAIL = createHash("sha256").update("axonhub-control-key-regression").digest("hex").slice(0, 12);
const TRIP_UUID = `01a0be5a-fb48-7680-a940-${TRIP_TAIL}`;
const TRIP_RESP_ID = "resp_" + TRIP_TAIL + TRIP_TAIL;

test("控制字段测试常量确实会触发 CRG 熵门（守卫的前提）", () => {
  assert.equal(TRIP_UUID.length, 36);
  assert.ok(isHighEntropyBlock(TRIP_TAIL), "末段 hex 必须高熵，否则本组用例无法证明保护有效");
});

test("reasoning.encrypted_content 原样保留并可无损拼回", () => {
  const encrypted = "gAAAAAB" + "Aa9fK2mQ7xP4vN8cR1sT6uY3wZ0bC5dE".repeat(24);
  const json = JSON.stringify({
    model: "gpt-6-astra",
    input: [{ type: "reasoning", encrypted_content: encrypted, summary: [] }],
  });
  const extracted = extractProtectedSpans(json, { id: "ee11aa22" });
  assert.equal(extracted.spans.length, 1);
  assert.equal(extracted.text.includes(encrypted), false, "加密思维链不得进入脱敏器");
  assert.equal(restoreSpans(extracted.text, extracted), json);
});

test("dispatch：reasoning.encrypted_content 原样送达上游", async () => {
  const encrypted = "gAAAAAB" + "Aa9fK2mQ7xP4vN8cR1sT6uY3wZ0bC5dE".repeat(24);
  const json = JSON.stringify({
    model: "gpt-6-astra",
    input: [{ type: "reasoning", encrypted_content: encrypted, summary: [] }],
  });
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST", headers: { "content-type": "application/json" }, body: json,
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example" },
    { fetchImpl: async (_url, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  assert.equal(JSON.parse(upstreamBody).input[0].encrypted_content, encrypted);
  assert.equal(/\{\{Redact:[a-f0-9]{64}\}\}/.test(upstreamBody), false);
});
test("extractProtectedSpans 抠出控制字段并可无损拼回（与大小无关）", () => {
  const json = JSON.stringify({
    model: "gpt-6-astra",
    prompt_cache_key: TRIP_UUID,
    previous_response_id: TRIP_RESP_ID,
    safety_identifier: TRIP_UUID,
    input: [{ type: "message", role: "user", content: "hi" }],
  });
  const extracted = extractProtectedSpans(json, { id: "cccccccc" });
  assert.equal(extracted.spans.length, 3);
  assert.equal(extracted.text.includes(TRIP_UUID), false, "原值不得留在交给脱敏器的文本里");
  assert.equal(restoreSpans(extracted.text, extracted), json, "拼回必须逐字节还原");
});

test("控制字段占位符本身低熵，不会被 CRG 二次改写", () => {
  const json = JSON.stringify({ prompt_cache_key: TRIP_UUID });
  const extracted = extractProtectedSpans(json, { id: "deadc0de" });
  // 占位符形如 "__CRG_M_deadc0de_0__"：下划线切块后最长块 <=8，达不到熵门 length>8。
  const tokenBlocks = extracted.text.match(/[A-Za-z0-9]+/g) || [];
  for (const b of tokenBlocks) assert.equal(isHighEntropyBlock(b), false, `占位符块不应高熵: ${b}`);
});

test("dispatch：会触发熵门的 prompt_cache_key 原样送达上游（回归守卫）", async () => {
  const json = JSON.stringify({
    model: "gpt-6-astra",
    prompt_cache_key: TRIP_UUID,
    previous_response_id: TRIP_RESP_ID,
    input: [{ type: "message", role: "user", content: "see cache" }],
  });
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST", headers: { "content-type": "application/json" }, body: json,
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example" },
    { fetchImpl: async (_url, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  const sent = JSON.parse(upstreamBody);
  assert.equal(sent.prompt_cache_key, TRIP_UUID, "上游必须看到未改写的 prompt_cache_key");
  assert.equal(sent.prompt_cache_key.length, 36);
  assert.equal(sent.previous_response_id, TRIP_RESP_ID);
  // CRG 会把 REDACT_NOTICE 注入请求体，而该文案本身含字面量 "{{Redact:sha256}}"，
  // 故不能对整串 body 断言「不含 {{Redact:」——只断言不存在“真实”脱敏 token（64 位 hex）。
  // 干净正文（无真密钥）不应产生任何真实占位符；控制字段原值由上面的等值断言保证。
  assert.equal(/\{\{Redact:[a-f0-9]{64}\}\}/.test(upstreamBody), false, "干净请求体不应产生真实脱敏占位符");
});

test("dispatch：关闭媒体剥离时控制字段仍受保护", async () => {
  const json = JSON.stringify({ model: "gpt-6-astra", prompt_cache_key: TRIP_UUID, input: [{ type: "message", role: "user", content: "hi" }] });
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST", headers: { "content-type": "application/json" }, body: json,
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example", REDACT_MEDIA_EXTRACT: "off" },
    { fetchImpl: async (_url, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  assert.equal(JSON.parse(upstreamBody).prompt_cache_key, TRIP_UUID, "控制保护不得依赖媒体开关");
});

test("控制字段与真实密钥共存：密钥仍脱敏、控制字段保留", async () => {
  const secret = "sk-" + "B".repeat(64);
  const json = JSON.stringify({
    model: "gpt-6-astra",
    prompt_cache_key: TRIP_UUID,
    input: [{ type: "message", role: "user", content: `token ${secret}` }],
  });
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST", headers: { "content-type": "application/json" }, body: json,
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example" },
    { fetchImpl: async (_url, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  assert.equal(JSON.parse(upstreamBody).prompt_cache_key, TRIP_UUID, "控制字段必须保留");
  assert.equal(upstreamBody.includes(secret), false, "正文密钥仍必须被脱敏");
  assert.ok(/\{\{Redact:[a-f0-9]{64}\}\}/.test(upstreamBody), "正文密钥应变为占位符");
});

test("extractLargeJsonStrings 保持 media-only：不触碰控制字段（向后兼容）", () => {
  const json = JSON.stringify({ prompt_cache_key: TRIP_UUID, data: [{ b64_json: "A".repeat(5000) }] });
  const extracted = extractLargeJsonStrings(json, { minChars: 4096, id: "beefbeef" });
  assert.equal(extracted.media.length, 1, "仍应只剥离媒体");
  assert.ok(extracted.text.includes(TRIP_UUID), "media-only 路径不得动控制字段");
});

// ── 控制字段保护（prompt_cache_key 等，方案 B：不改 vendor worker.js）────────────

test("控制字段无条件抠出、占位符低熵、可无损拼回", () => {
  const pck = "01a0be5a-fb48-7680-a940-9a659d267f36";
  const prid = "resp_" + "a".repeat(48);
  const body = JSON.stringify({
    model: "gpt-6-astra",
    prompt_cache_key: pck,
    previous_response_id: prid,
    safety_identifier: "user-abc-123",
    input: [{ role: "user", content: "hi" }],
  });
  const extracted = extractProtectedSpans(body, { id: "deadbeef" });
  // 三个控制键都被抠出（按键名匹配，与大小/熵无关）。
  assert.equal(extracted.spans.length, 3);
  // 脱敏器看不到任何控制值原文。
  assert.equal(extracted.text.includes(pck), false);
  assert.equal(extracted.text.includes(prid), false);
  // 占位符本身低熵，不会被 CRG 二次脱敏。
  assert.match(extracted.text, /"__CRG_M_deadbeef_0__"/);
  assert.equal(/\{\{Redact:/.test(extracted.text), false);
  // 拼回无损。
  assert.equal(restoreSpans(extracted.text, extracted), body);
});

test("关闭 media 剥离时，控制字段仍受保护", () => {
  const body = JSON.stringify({ prompt_cache_key: "01a0be5a-fb48-7680-a940-9a659d267f36" });
  const extracted = extractProtectedSpans(body, { id: "cafe0000", media: false });
  assert.equal(extracted.spans.length, 1);
  assert.equal(restoreSpans(extracted.text, extracted), body);
});

test("dispatch：控制键豁免与值内容无关——键上的敏感值也原样保留（回归守卫）", async () => {
  // 这个值会被 CRG 的 secret 规则确定性命中（/\bsk-[A-Za-z0-9]{60,}\b/）。
  // 修复前它会被改写成 {{Redact:…}}；控制键豁免必须让它逐字节原样通过。
  const trap = "sk-" + "0123456789".repeat(6);
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model: "x", prompt_cache_key: trap, input: [] }),
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example" },
    { fetchImpl: async (_u, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  assert.ok(upstreamBody.includes(`"prompt_cache_key":"${trap}"`), "控制键值必须原样，即使它匹配密钥规则");
  assert.equal(/\{\{Redact:/.test(upstreamBody), false, "控制键值不应被脱敏");
  assert.equal(upstreamBody.includes("__CRG_M_"), false);
});

test("dispatch：UUID 形态 prompt_cache_key 送达上游长度≤64，同体内真密钥仍被脱敏", async () => {
  const pck = "01a0be5a-fb48-7680-a940-9a659d267f36"; // 36 字符，真实 Codex 形态
  const secret = "sk-" + "abcdefghij".repeat(6);
  let upstreamBody = "";
  const res = await dispatch(
    new Request("http://redact:8787/$http://echo.example/v1/responses", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        model: "gpt-6-astra",
        prompt_cache_key: pck,
        input: [{ role: "user", content: [{ type: "input_text", text: `k ${secret}` }] }],
      }),
    }),
    { REDACT_ALLOWED_HOSTS: "echo.example" },
    { fetchImpl: async (_u, init) => { upstreamBody = init.body; return new Response("{}", { headers: { "content-type": "application/json" } }); } },
  );
  assert.equal(res.status, 200);
  assert.ok(upstreamBody.includes(`"prompt_cache_key":"${pck}"`), "prompt_cache_key 必须原样送达");
  const sent = JSON.parse(upstreamBody).prompt_cache_key;
  assert.ok(sent.length <= 64, `prompt_cache_key 应≤64，实得 ${sent.length}`);
  assert.equal(upstreamBody.includes(secret), false, "正文里的真密钥不得泄漏到上游");
  assert.match(upstreamBody, /\{\{Redact:[a-f0-9]{64}\}\}/);
  assert.equal(upstreamBody.includes("__CRG_M_"), false);
});
