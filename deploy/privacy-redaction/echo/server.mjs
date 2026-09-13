// 受控回显上游（验证专用，绝不用于生产流量）
//
// 目的：把「上游实际收到了什么」完整打到 stdout，让 docker logs 成为判定依据。
// 判定必须看本容器的日志，不能看响应体 —— CRG 会在回程把占位符还原，
// 响应体证明不了上游看到的是占位符。
//
// 每个请求输出一行 JSON（便于 grep / jq），字段：
//   ts, method, url, headers, bodyLen, body
// body 为原始文本，未做任何解析或截断。

import http from "node:http";

const port = Number(process.env.PORT || 8080);
const host = process.env.HOST || "0.0.0.0";

// 固定的最小 Anthropic Messages 响应，让 axonhub 侧不至于因解析失败而掩盖真实现象。
// 内容本身不参与任何判定。
function fixedResponse() {
  return JSON.stringify({
    id: "msg_echo_fixed",
    type: "message",
    role: "assistant",
    model: "redact-verify-echo",
    content: [{ type: "text", text: "echo-ok" }],
    stop_reason: "end_turn",
    stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1 },
  });
}

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (c) => chunks.push(c));
  req.on("end", () => {
    const raw = Buffer.concat(chunks);
    const body = raw.toString("utf8");
    // 单行 JSON：换行会被转义，保证一个请求一行
    process.stdout.write(
      JSON.stringify({
        ts: new Date().toISOString(),
        method: req.method,
        url: req.url,
        headers: req.headers,
        bodyLen: raw.length,
        body,
      }) + "\n",
    );
    const payload = fixedResponse();
    res.writeHead(200, {
      "content-type": "application/json; charset=utf-8",
      "content-length": Buffer.byteLength(payload),
    });
    res.end(payload);
  });
  req.on("error", (e) => {
    process.stdout.write(
      JSON.stringify({ ts: new Date().toISOString(), error: String(e && e.message) }) + "\n",
    );
  });
});

server.listen(port, host, () => {
  process.stdout.write(
    JSON.stringify({ ts: new Date().toISOString(), event: "listening", host, port }) + "\n",
  );
});
