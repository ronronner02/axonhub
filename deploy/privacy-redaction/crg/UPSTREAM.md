# CRG Vendor 来源与完整性

本目录的 `worker.js`、`node-server.mjs`、`LICENSE` 是从上游仓库**原样复制**的第三方代码，不得修改。
需要调整 CRG 本身的行为时只能通过环境变量（见下方「可配置项」）。

本地另有两份**不属于 vendor** 的封装，不进 `SHA256SUMS`：

| 文件 | 角色 |
| --- | --- |
| `entry.mjs` | 容器入口。转发前先把「超大媒体字符串」和「上游控制字段」从 JSON 文本里抠成低熵占位符，交给未改动的 `handleRequest`，转发前拼回原值。 |
| `media-extract.mjs` | 抠出 / 拼回的纯函数，可单测。除媒体外还保护控制字段（见下方「控制字段保护」）。 |

`Dockerfile` 的 `CMD` 指向 `entry.mjs`，不再直接跑 `node-server.mjs`。`node-server.mjs` 仍原样保留，便于对照上游。

## 锁定来源

| 项 | 值 |
| --- | --- |
| 仓库 | https://github.com/CassiopeiaCode/CosyRedactGateway |
| 版本 | v0.3.0 |
| 锁定提交 | `dfce67a17c308faf8c84c10b852623506cf444ff` |
| 提交时间 | 2026-09-11T15:56:02Z |
| 许可 | MIT（见 `LICENSE`） |
| 取得方式 | `https://raw.githubusercontent.com/CassiopeiaCode/CosyRedactGateway/<提交>/<文件>` |
| 取得日期 | 2026-09-12 |

## 文件哈希

```text
da7ba72bffdf838c9210bdbee942f1b86dbb9361fb9abad47364567f9882017c  worker.js
45f54c99d1cac5573f987b303d92a1f3883a6c69aa853274a05aa2c7640c674f  node-server.mjs
7a6d026306e3c1c59af1de27bfb2509e5cc1a3181ee4da53f63359b013020f8a  LICENSE
```

校验（在本目录下执行）：

```bash
sha256sum -c SHA256SUMS
```

## 依赖面

`worker.js` 无任何 `import` / `require`，零运行时依赖。`node-server.mjs` 只依赖 Node 内置
`node:http`、`node:stream` 与 `worker.js`。本地封装 `entry.mjs` / `media-extract.mjs` 另用
`node:path`、`node:url`。因此镜像不需要 `npm install`，也不需要 vendor `package.json`。

## 可配置项（经源码核对，`worker.js` 实际读取的环境变量只有四个）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `REDACT_ALLOWED_HOSTS` | 空 = **允许一切** | 逗号分隔主机名。匹配精确主机或其子域（`a.example.com` 会被 `example.com` 匹配到）。**必须显式设置**，否则是开放代理。 |
| `REDACT_CORS_ORIGIN` | `*` | 响应的 `access-control-allow-origin` 取值。 |
| `REDACT_MAX_BODY_BYTES` | `16777216`（16 MiB） | 超出返回 413，不转发。 |
| `REDACT_MAX_REDACTIONS` | `16384` | 单请求唯一敏感值上限，超出返回 413。 |

本地封装（`entry.mjs`，**不是** vendor）另读：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `REDACT_MEDIA_EXTRACT` | 开启 | 设为 `0` / `false` / `off` 可关闭剥离，回退为整包 parse。 |
| `REDACT_MEDIA_MIN_CHARS` | `4096` | JSON 字符串字面量短于该长度不剥离。 |

`node-server.mjs` 另读 `HOST`（默认 `127.0.0.1`）与 `PORT`（默认 `8787`）。
**容器内必须设 `HOST=0.0.0.0`**，否则同网络的其它容器无法连接。

### 已知限制：注入文案不可配置

`REDACT_NOTICE` 是 `worker.js` 第 15 行的导出常量，**不是环境变量**。要改注入给模型的英文提示
文案，只能修改 vendor 文件，这与本目录的「不改 vendor」约束冲突。若该文案对模型行为造成实际
问题，须作为升级/分叉决策记录后处理，不能当作可随手调整的旋钮。

## 控制字段保护（本地 wrapper 决策，不改 vendor）

**背景（一次真实事故）：** Codex CLI 自带 `prompt_cache_key`（其 session UUID，36 字符）。
CRG 的通用检测器按 `[A-Za-z0-9]+` 切块后，UUID 末段 12 位 hex 会被 `H`（highEntropy）
判为敏感并换成 `{{Redact:<64hex>}}`（75 字符），整串变 99 字符 —— 超过 OpenAI Responses
对 `prompt_cache_key` 的 64 字符上限，请求被 400 拒绝。实测约 96% 的 UUID 会中招。
即便未超限，占位符也会破坏 prompt 缓存（CRG 运行时盐每次重启即变）与 Responses 会话链接
（`previous_response_id`）。

**这些字段不是自由文本，不该脱敏。** 处理方式沿用媒体的「抠出→拼回」：在 vendor
`handleRequest` 之前把它们从 JSON 文本抠成低熵占位符（`__CRG_M_<id>_<n>__`，下划线切块后
最长块 <=8 字符，达不到熵门 `length>8`，不会被二次改写），转发前逐字节拼回原值。
**vendor `worker.js` 保持原样、不进 `SHA256SUMS`。**

受保护的键（`media-extract.mjs` 的 `CONTROL_KEYS`，按键名匹配、与值大小/熵无关）：

| 键 | 用途 | 不保护的后果 |
| --- | --- | --- |
| `prompt_cache_key` | 上游提示缓存键 | 被改写→超 64 上限→400；缓存失效 |
| `previous_response_id` | Responses 会话链接（`resp_…`） | 被改写→上游找不到上一条响应→会话断链 |
| `safety_identifier` | 上游滥用检测标识 | 被改写→标识漂移 |
| `encrypted_content` | Responses reasoning 加密内容 | 被改写→上游解密失败或 400 |

**权衡（安全含义，需知悉）：** 这四个键的值会**原样**送达上游，绕过脱敏。它们本应是
客户端生成的随机/不透明标识，不含用户内容；但若上游把敏感信息塞进这些字段，将不再被
脱敏。需要收窄/扩充范围时改 `CONTROL_KEYS` 一处即可，并补 `tests/media-extract.test.mjs`。
控制保护**不依赖** `REDACT_MEDIA_EXTRACT` 开关（即便关闭媒体剥离仍生效），因为这些字段无论媒体如何处理都会被通用检测器改写。

**为何走 wrapper 而非改 vendor `CONTROL_KEYS`：** 与「注入文案」那条已知限制同理 —— 改
vendor 会破坏「原样复制、可对照上游」的约束并需维护 `SHA256SUMS`。wrapper 方案在升级 CRG
时无需重新 merge。**升级后仍需复核**：新版 vendor 若已自行豁免这些键，本 wrapper 的保护
会变成无害的冗余（占位符低熵，不影响结果），可另行决定是否精简。

**回归守卫：** `tests/media-extract.test.mjs` 含「测试常量确实触发 CRG 熵门」的前提断言 +
「UUID 形态 key 原样送达且 <=64、同体内真密钥仍被脱敏」的端到端断言。`node --test`（或
宿主机 `node.exe`）即可跑，不需容器。

## 行为要点（源码核对结论，供运维参考）

- 路由形状 `/<flags>$<上游URL>`。`flags` 段为空即等价 `HPSIBEG`（七类检测器全开）。
- 占位符形如 `{{Redact:<64位小写十六进制>}}`；同一请求内同一原值映射同一占位符。
- 运行时盐在**进程启动时**生成，重启即变，占位符随之改变（上游提示缓存前缀会失效）。
- 映射只存在于单次请求的内存中，不落盘、不跨请求。
- `GET` / `HEAD` 不读正文，直接转发。
- 转发前移除的请求头：`host`、`content-length`、`connection`、`transfer-encoding`、
  `keep-alive`、`proxy-authenticate`、`proxy-authorization`、`te`、`trailer`、`upgrade`、
  `accept-encoding`、`x-forwarded-for`、`x-forwarded-proto`、`x-real-ip`、`forwarded`、
  `via`、`cookie`、`cookie2`，以及任何 `cf-` / `sec-` 前缀头。其余头（含 `x-api-key`、
  `authorization`、`anthropic-beta`、`anthropic-version`）原样转发。
- 响应侧删除 `content-encoding` 与 `content-length`，并加上 CORS 头；SSE 逐块还原。
- 上游重定向使用 `redirect: "manual"`，3xx 原样返回，不跟随。

### 状态码

| 码 | 触发条件 |
| --- | --- |
| 404 | 路径中缺少 `$` |
| 400 | `$` 后为空、上游协议非 http/https、上游 URL 含 userinfo、正文非合法 JSON |
| 403 | 上游主机不在 `REDACT_ALLOWED_HOSTS` 内 |
| 413 | 正文超 `REDACT_MAX_BODY_BYTES`，或替换数超 `REDACT_MAX_REDACTIONS` |
| 415 | 正文非空且 `Content-Type` 不是 JSON |
| 502 | 向上游 fetch 失败 |

这些码都会进入 axonhub 的失败计数并可能触发渠道自动禁用，恢复路径见 `../README.md`。

## 升级流程

1. 确认新提交，替换本目录三个文件；
2. 更新本文件的提交号、日期、哈希与 `SHA256SUMS`；
3. 重新核对上表「可配置项」与「状态码」是否变化；
4. 重跑 `../scripts/smoke-local.sh`；
5. 重跑服务器验收阶段 1～2（见 `../README.md`），结果写入 `../ACCEPTANCE-RECORD.md`。
