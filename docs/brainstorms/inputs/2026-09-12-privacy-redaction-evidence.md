# 取证快照：中转站出站隐私脱敏层（2026-09-12）

本文件是 `docs/brainstorms/2026-09-12-001-feat-privacy-redaction-gateway-requirements.md` 的原始输入记录。它把只存在于远程服务器、临时克隆目录和对话里的证据固化到仓库内，供 finalize/checker 计算 `inputs_hash`，也供后续 `spec-plan` 复核。敏感值一律遮盖；第三方中转站主机名不在此落盘（权威清单在 axonhub 控制台的渠道列表）。

## 1. 原始需求（用户原话，SSH 连接信息已省略）

> 我在我的远程服务器上开了一个中转站汇集我的第三方api供我使用，然后最近遇到一个很严重的问题就是网上暴露出很多第三方偷窃个人隐私和钱包的事件，然后基于此，我又看到一个保护隐私的项目，所以我想试试能不能合并进我的中转站项目来保护我自己的隐私，我的远程服务器连接为 <SSH 连接信息，已省略>，项目为 axonhub，然后这是一个安全过滤的项目 https://github.com/CassiopeiaCode/CosyRedactGateway

## 2. 远程服务器现状（SSH 只读 + Postgres 只读，2026-09-12）

| 事实 | 取值 | 来源 |
| --- | --- | --- |
| axonhub 镜像 | `looplj/axonhub:v1.0.0-beta7`（官方镜像，无自定义代码） | `docker ps`、`/srv/apps/axonhub/docker-compose.yml` |
| 部署文件 | `/srv/apps/axonhub/{docker-compose.yml,nginx.conf,.env}`；未见 git 仓库 | `find` |
| 容器拓扑 | `axonhub-gateway`(nginx 1.28, 127.0.0.1:8020→8080) → `axonhub-app`(:8090) → `axonhub-postgres`(16-alpine)；同一 bridge 网络 `axonhub-network` | docker-compose.yml |
| 容器加固 | `read_only: true`、`cap_drop: ALL`、`no-new-privileges`、tmpfs `/tmp` | docker-compose.yml |
| 入口 | nginx 使用 `$http_cf_connecting_ip`，推断公网经 Cloudflare 进入（source-candidate，未读 cloudflared 配置） | nginx.conf |
| axonhub 关键环境 | `AXONHUB_SERVER_API_AUTH_ALLOW_NO_AUTH=false`、`AXONHUB_LOG_LEVEL=warn`、`AXONHUB_METRICS_ENABLED=false`（值已遮盖其余） | `docker inspect` |
| 宿主机运行时 | 只有 docker 29.1.3；无 node / deno | `which` |
| 渠道数量 | 27 个（26 个第三方中转站主机 + 1 个官方 `api.openai.com`，id 26 `openai`）；1 个已归档 | `channels` 表（只读 id/type/name/base_url/status，未读凭据） |
| 渠道类型 | `anthropic`、`openai_responses`、`zhipu_anthropic`、`deepseek`、`xai`、`openai` | `channels` 表 |
| API key | 1 个（`self`），单用户 | `api_keys` 表（未读 key 值） |
| 近 30 天协议分布 | `anthropic/messages` 4364、`openai/responses` 2040、`openai/chat_completions` 3、`aisdk/datastream` 2 | `requests` 表 |
| 请求正文存档 | 6409 条请求全部带 `request_body` / `response_body`（`content_saved`） | `requests` 表 |
| 内置 Prompt Protection Rules | 表存在，0 条规则 | `prompt_protection_rules` 表 |
| 渠道自动禁用机制 | `channels.auto_disabled_at` 列存在 | 表结构 |

## 3. CosyRedactGateway 事实（浅克隆，commit `dfce67a17c30`，2026-09-11，v0.3.0，MIT）

- 单文件 `worker.js`（890 行），Node ≥20 / Deno / Cloudflare Workers；`node-server.mjs` 为本地适配器，默认 `127.0.0.1:8787`。
- 路由封套：`https://<proxy>/<flags>$<upstream-url>`；flags 为空 = 全开 `HPSIBEG`；未知 flag 返回 400。
- 检测器：`H` 高熵串（长度>8）、`P` 手机号、`S` `sk-` 密钥、`I` PRC 身份证（校验位）、`B` 银行卡（Luhn）、`E` 邮箱、`G` Gitleaks 兼容规则包（218 条 JS 条目，含 PEM 私钥块、各云厂商/AI 厂商 token、JWT）。
- **没有**加密钱包助记词、钱包地址、WIF 私钥的专用检测器；不支持用户自定义规则。
- 占位符 `{{Redact:<sha256(原文+运行时盐)>}}`；映射请求内、内存态、不持久化、不写日志；进程重启换盐。
- 显式支持并注入 Redact Notice 的协议：OpenAI Chat Completions、OpenAI Responses、Anthropic Messages；其它 JSON 端点通用脱敏但不注入 notice。
- 流式：SSE 逐块还原，占位符跨块/跨事件切分也能还原（测试覆盖每个切分位置）。
- 失败即关闭：非空且非 JSON 请求体 → 415；`REDACT_MAX_BODY_BYTES` 默认 16 MiB；`REDACT_MAX_REDACTIONS` 默认 16384。
- 头策略：转发 `Authorization`、`x-api-key`、`anthropic-version` 等；移除 `Cookie`、`CF-*`、`Sec-*`、`Forwarded`、`X-Forwarded-*`、`X-Real-IP`；`redirect: "manual"` 不跟随重定向。
- `REDACT_ALLOWED_HOSTS` 未设置时是开放代理；SECURITY.md 要求生产部署设置。
- 多模态：`image_url` / `input_image` / `input_audio` / `file_data` / base64 字段跳过文本脱敏。
- 已文档化限制：模型改写占位符则无法还原；检测漏报仍会外发；二进制不检查。

## 4. 本地实测（node v24.12.0，2026-09-12）

`parseProxyTarget` 对下列 axonhub 拼接后形态全部解析成功，上游 URL 保留路径与查询串：

```text
http://redact:8787/$https://<host>/v1/messages                       -> https://<host>/v1/messages
http://redact:8787/$https://<host>/v1/responses?beta=true            -> https://<host>/v1/responses?beta=true
http://redact:8787/HPSIBEG$https://<host>/api/anthropic/v1/messages  -> https://<host>/api/anthropic/v1/messages
http://redact:8787/$https://<host>/v1/chat/completions               -> https://<host>/v1/chat/completions
```

axonhub 一侧对含 `$` 的 Base URL 的拼接行为**未实测**，见 PRD `Planning Recheck`。

## 5. axonhub 文档依据（本 fork 检出于 2026-08-30；上游 master 2026-09-10 同名文档内容一致）

- `docs/en/guides/prompt-protection-rules.md`：正则 mask / reject，按消息角色 scope，规则按 id 顺序叠加。
- `docs/en/guides/channel-management.md` §Base URL Special Configuration：`#` 关闭版本号追加，`##` 完全原样。
- `docs/en/getting-started/request-processing.md`：Content Processing 阶段包含 prompt injection / prompt protection，位于 Channel Selection 之后、Call Upstream 之前。
- `docs/en/development/erd.md`：Request / RequestExecution 保存请求与响应 payload。
- `config.example.yml`：`gc` 清理配置、`server.trace`、`db` 等键。
