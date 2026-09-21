---
spec_id: 2026-09-12-001-feat-privacy-redaction-gateway
artifact_kind: prd-requirements
target_surface: Backend
status: ready-for-planning
evidence_grade: mixed
source_authority: mixed
readiness_authority: engineering-owned
created: 2026-09-12
source_inputs:
  - docs/brainstorms/inputs/2026-09-12-privacy-redaction-evidence.md
  - docs/en/guides/prompt-protection-rules.md
  - docs/en/guides/channel-management.md
  - docs/en/getting-started/request-processing.md
  - docs/en/development/erd.md
  - config.example.yml
write_mode: final-prd
can_enter_spec_plan: yes
clarification_evidence: asked-owner
preflight_sweep_closure: closed
next_owner_question: none
readiness_verified_by: check-prd-artifact.js
readiness_verified_at: 2026-09-12T01:17:25.282Z
readiness_checker_schema: spec-prd-artifact-check.v1
readiness_finding_count: 1
readiness_blocking_count: 0
readiness_prd_hash: sha256:3623343d13a74ae6e75350ec5b9b1b52d1b3495ebdb7e4420cdda26ecbbea7a8
readiness_inputs_hash: sha256:9478091f8f7ac75f9384db50aa819be5eaadfc43a0ed901bbd90bfc1fa0fae17
---

# 中转站出站隐私脱敏层 增量需求文档

## PRD 元数据

| 项 | 内容 |
| --- | --- |
| 需求名称 | 中转站出站隐私脱敏层（CosyRedactGateway sidecar 接入 axonhub） |
| 需求编号 / spec_id | 2026-09-12-001-feat-privacy-redaction-gateway |
| 业务域 | axonhub 中转站 · 渠道出站链路 |
| 目标 surface | Backend（网关运行时，无新增 UI） |
| 目标地区 / 市场 / tenant | 不涉及（单实例、单用户） |
| 目标用户 / 客户类型 | 中转站 owner 本人（唯一 API key 持有者），客户端为 Claude Code / Codex 类编码工具 |
| 是否触及付费、资金或交易 | 否（不改变计费；上游 token 用量可能因占位符与提示注入略增，见 NFR） |
| 是否触及个人信息或敏感数据 | 是：请求正文中的密钥/令牌、PRC 身份证号、手机号、银行卡号、邮箱、私钥块；处理方式为可逆占位符替换 |
| 是否需要外部规则或专业意见 | 否；威胁模型由 owner 自定 |
| 相关文档 | `docs/brainstorms/inputs/2026-09-12-privacy-redaction-evidence.md`（取证快照）；`docs/en/guides/prompt-protection-rules.md`；`docs/en/guides/channel-management.md` |

<!-- prd:section=summary -->

## Summary

为中转站 owner 在「请求离开自己的服务器、发往第三方中转站上游之前」增加一层可逆脱敏：命中检测器的密钥与个人信息在出站时被替换为不可逆推的占位符，上游只看到占位符；上游回显占位符时在返回客户端前还原为原值。客户端与渠道凭据零改动，脱敏层不可用时宁可失败也不明文外发。

## Problem Frame

- **现状**：owner 的 axonhub 汇集了 27 个渠道，其中 26 个是第三方中转站；近 30 天 6400+ 次请求（Claude Code / Codex 场景）把代码、配置、工具结果等原文直接发给这些上游。上游是否存储、检视、转卖这些内容不可控。
- **触发**：近期公开的第三方中转站窃取用户隐私与钱包资产的事件，使 owner 无法再把「上游不会看」当作默认假设。
- **不做的风险**：任何一次含 API key、云凭据、私钥块或个人证件号的对话都可能被不可信上游留存；owner 现有的零配置内置规则等于没有防护。
- **业务价值**：把「信任上游」改成「默认不信任上游」，且不牺牲现有客户端体验和渠道调度能力。

<!-- prd:section=change_delta -->

## Change Delta

| 变化类型 | 内容 | 涉及现有能力 | 用户/数据/运营影响 | 证据 tag |
| --- | --- | --- | --- | --- |
| keep | 客户端接入方式、唯一 API key、三种入站协议与流式行为 | axonhub 入站层 | 客户端零改动 | confirmed-source（流量分布见取证快照 §2） |
| keep | 渠道凭据、渠道选择、负载均衡、故障转移、追踪 | axonhub 渠道调度 | 不改 | confirmed-source（`docs/en/getting-started/request-processing.md`） |
| keep | axonhub 镜像 `v1.0.0-beta7` 与代码，不改、不换 | 部署 | 升级路径与上游解耦 | user-stated（OQ-7） |
| keep | 内置 Prompt Protection Rules 保持 0 条，本期不启用 | axonhub 内置规则 | 作为后续可选补充保留 | user-stated（OQ-1） |
| keep | 本机 Postgres 中请求/响应正文明文存档 | axonhub 追踪存储 | 本期不动，记为已知风险 | user-stated（OQ-6） |
| add | 一个独立的可逆脱敏层组件（CosyRedactGateway，sidecar），只在本机内部网络可达 | 无 | 新增一个需运维的进程 | user-stated（OQ-1、OQ-7）+ confirmed-source（取证快照 §3） |
| add | 一条可复现的验证路径（受控回显上游 + 真实模型往返） | 无 | owner 可自证脱敏生效 | user-stated（OQ-5） |
| replace | 受保护渠道的出站目的地：由直连第三方上游改为经脱敏层再到上游 | 渠道 Base URL / 端点 | 上游只收到占位符；出站多一跳 | user-stated（OQ-2、OQ-7）+ source-candidate（拼接兼容见 Planning Recheck） |
| extend | 渠道信任标记：可辨识哪些渠道被显式豁免、哪些受保护 | 渠道管理 | 新增渠道流程多一步 | user-stated（OQ-2） |
| remove | 无 | | | |
| unknown | 无（beta7 与所读文档的版本差异放入 Planning Recheck，不改 WHAT） | | | |

## Current System Snapshot

| 现状项 | 当前行为 | 证据 tag |
| --- | --- | --- |
| 部署形态 | 官方镜像 `looplj/axonhub:v1.0.0-beta7`，docker compose 三容器（nginx 网关 → axonhub → postgres），同一 bridge 网络；容器已做只读根文件系统与 cap_drop 加固 | confirmed-source（取证快照 §2） |
| 渠道构成 | 27 个渠道：26 个第三方中转站 + 1 个官方 `api.openai.com`（id 26） | confirmed-source |
| 使用者 | 1 个 API key（`self`）；客户端为 Claude Code（Anthropic Messages）与 Codex（OpenAI Responses），近 30 天 Chat Completions 仅 3 次 | confirmed-source |
| 请求体形态 | 近 30 天 100% 为 JSON 协议，无音频/文件二进制端点流量 | confirmed-source |
| 出站脱敏 | 无。内置 Prompt Protection Rules 支持正则 mask/reject，但 0 条规则 | confirmed-source |
| 渠道 Base URL 规则 | 可自定义；`#` 后缀关闭版本号追加，`##` 完全原样 | confirmed-source（`docs/en/guides/channel-management.md`） |
| 请求处理顺序 | 协议转换 → 权限 → 模型映射 → 选渠道 → 内容处理（prompt protection）→ 负载均衡 → 请求改写 → 调上游 → 失败重试/换渠道 → 响应转换 | confirmed-source（`docs/en/getting-started/request-processing.md`） |
| 请求存档 | 全部 6409 条请求的请求/响应正文保存在本机 Postgres | confirmed-source |
| 渠道健康 | 存在自动禁用机制（`auto_disabled_at`）；恢复行为未读 | source-candidate |
| 宿主机 | 只有 docker，无 node/deno | confirmed-source |
| 公网入口 | nginx 读取 Cloudflare 头，推断经 Cloudflare 隧道进入 | source-candidate |

## Change Topology

- `primary_topology`：`add`（新增出站前置组件）+ `replace`（受保护渠道出站目的地）+ `policy-change`（信任边界默认值）。
- `load_bearing_surfaces`：渠道出站配置（axonhub 控制台）、服务器 docker compose、脱敏层运行参数。客户端 API、admin API、入站鉴权不在改动面。
- `source_of_truth_risk`：服务器上的 compose / nginx 不在任何 git 仓库内；本 PRD 只要求「变更可追溯」，把纳入版本控制的做法留给 `spec-plan`（见 Planning Recheck）。
- `producer_consumer_risk`：axonhub 是脱敏层唯一的调用方；脱敏层是第三方上游的唯一调用方（受保护渠道）。豁免渠道保持 axonhub 直连。
- `negative_space_risk`：新增渠道若绕过脱敏层即形成明文旁路，见 R-04 / R-05 / AE-07。

### Producer / Artifact / Consumer

| Producer | Artifact / 请求 | Consumer | 成功结果 | 失败结果 |
| --- | --- | --- | --- | --- |
| 客户端（Claude Code / Codex） | 原文请求 | axonhub | 与现状一致 | 与现状一致 |
| axonhub（受保护渠道） | 已完成协议转换的上游请求 | 脱敏层 | 上游请求被替换为占位符版本 | 该次尝试失败，触发现有故障转移（R-06） |
| 脱敏层 | 占位符版本请求 | 第三方上游 | 上游正常响应 | 上游错误原样透传 |
| 第三方上游 | 响应（可能含占位符） | 脱敏层 | 占位符被还原 | 改写过的占位符原样透传（BR-004） |
| 脱敏层 | 还原后的响应 | axonhub → 客户端 | 客户端看到原值 | — |

### Source-Of-Truth Resolution

| 事项 | 权威来源 | 说明 |
| --- | --- | --- |
| 哪些渠道受保护 / 豁免 | axonhub 渠道配置（出站是否经脱敏层）+ 渠道标记 | R-05 要求两者一致且可核对 |
| 敏感值 ↔ 占位符映射 | 脱敏层进程内存（请求内） | 不落盘、不跨请求、不可查询（BR-003） |
| 脱敏层允许的上游主机集合 | 脱敏层运行配置 | 应与受保护渠道主机集合一致（R-07） |
| 请求原文历史 | 本机 Postgres（现状，不变） | 本期不动，见 Data / Compliance Boundaries |

## Glossary

| 术语 | 定义 | 来源 |
| --- | --- | --- |
| 中转站 | owner 自建的 axonhub 实例，汇聚多个上游 | 用户原话 |
| 上游 / 渠道 | axonhub 中的 channel，指向一个第三方或官方 API 地址 | axonhub 文档 |
| 脱敏层 | 本期新增的 CosyRedactGateway sidecar 进程 | OQ-1、OQ-7 |
| 受保护渠道 | 出站流量必须经脱敏层的渠道；默认所有渠道都是 | OQ-2 |
| 豁免渠道 | owner 显式标记为可信、允许直连的官方直连渠道；当前仅 `api.openai.com` | OQ-2 |
| 占位符 | 形如 `{{Redact:64位十六进制摘要}}` 的替换串，不可逆推原值 | CRG README |
| 还原 | 上游回显占位符时在返回客户端前替换回原值 | CRG README |
| 检测器 | CRG 的七类规则 H/P/S/I/B/E/G | CRG README |

## Actors

| 角色 | 使用场景 | 用户目标 | 权限 / 数据边界 |
| --- | --- | --- | --- |
| owner（管理员 + 唯一使用者） | 日常用 Claude Code / Codex 经中转站调用模型；偶尔新增/调整渠道；抽查脱敏是否生效 | 不改变使用习惯的前提下，第三方上游拿不到原始敏感值 | 拥有 axonhub 全部管理权限与服务器 root |
| 客户端（Claude Code / Codex） | 发起三种协议的流式/非流式请求，含工具调用与工具结果 | 行为与现状一致 | 只持有中转站 API key |
| 第三方上游 | 接收请求并返回响应 | — | 视为不可信：可能存储与检视一切收到的内容 |
| 脱敏层 | 在 axonhub 与受保护上游之间 | — | 只在内部网络可达；仅允许既定上游主机 |

<!-- prd:section=requirements -->

## Requirements

| 编号 | 触发条件 | 角色 | 系统行为 | 用户可见结果 | 证据 / 约束引用 |
| --- | --- | --- | --- | --- | --- |
| R-01 | 当一条请求被路由到任一受保护渠道时 | 系统（中转站） | 应在请求离开本服务器前，把请求正文中命中已启用检测器的敏感值替换为不可逆推的占位符，再转发给该渠道上游；同一请求内同一原值映射为同一占位符 | 上游收到的正文不含原始敏感值；客户端无感知 | BR-001、BR-003；取证快照 §3 |
| R-02 | 当上游在普通文本、JSON、流式增量或工具调用参数中原样回显占位符时 | 系统 | 应在返回客户端前把已知占位符还原为原值；流式响应逐块还原，不整体缓冲 | 客户端看到原值，与不脱敏时一致 | 取证快照 §3「流式」；BR-004 |
| R-03 | 当脱敏层处理任一受保护渠道的请求时 | 系统 | 应启用全部七类检测器（高熵串、手机号、`sk-` 密钥、PRC 身份证、银行卡、邮箱、Gitleaks 兼容规则包），覆盖消息文本、工具输入、工具结果及其它字符串字段；图片/音频等二进制字段不检查 | 命中类别被替换；未命中类别（含钱包助记词）原样发出 | OQ-3；BR-002 |
| R-04 | 当渠道被创建或启用时 | owner | 系统默认将其视为受保护渠道；只有 owner 显式标记为豁免渠道的官方直连渠道可不经脱敏层 | 未显式豁免的渠道流量一律经脱敏层 | OQ-2；BR-001 |
| R-05 | 当 owner 需要核对信任边界时 | owner | 应能得到「当前未经脱敏层的渠道」清单，且该清单只应包含被显式豁免的渠道 | 一眼看出哪些渠道在直连 | OQ-2；AE-07 |
| R-06 | 当脱敏层不可用、返回错误，或因请求体非 JSON / 超体积上限 / 超替换上限而拒绝时 | 系统 | 该次对受保护渠道的上游尝试应失败；系统可按现有故障转移到其它受保护渠道或豁免渠道；任何情况下不得把明文正文发往受保护渠道上游 | 客户端收到失败，或收到来自另一条受保护/豁免渠道的正常响应；绝不会静默明文外发 | OQ-4；BR-005 |
| R-07 | 当脱敏层收到指向未配置上游主机的转发请求时 | 系统 | 应拒绝且不建立上游连接；脱敏层只在本机内部网络可达，不对公网暴露 | 无 | 取证快照 §3 `REDACT_ALLOWED_HOSTS` |
| R-08 | 当脱敏层转发请求时 | 系统 | 应原样转发渠道凭据与上游协议头；应移除代理/网络身份类头（转发 IP、Cookie、CF-*、Sec-*）；不跟随上游重定向 | 上游鉴权照常成功；上游看不到本机来源身份头 | 取证快照 §3「头策略」 |
| R-09 | 当 owner 执行验收或例行抽查时 | owner | 应能用一条可复现路径证明脱敏与还原确实发生：受控回显上游看到的是占位符、真实模型往返得到原值 | 得到明确的通过 / 不通过结论 | OQ-5 |
| R-10 | 当客户端以 Anthropic Messages、OpenAI Responses 或 OpenAI Chat Completions 访问时 | 客户端 | 请求形状、鉴权方式、工具调用与流式行为保持不变 | 客户端零改动 | 取证快照 §2 协议分布 |

业务规则：

- BR-001：受保护渠道 = 默认所有渠道；豁免渠道 = owner 显式标记为可信的官方直连渠道，当前仅 `api.openai.com`（id 26）。
- BR-002：加密钱包助记词（12/24 个自然单词）不在本期检测范围；owner 的行为约束是不在提示词、文件或工具结果中出现助记词。hex 私钥与 `0x` 地址预期由高熵检测器命中，但未实测（见 Evidence And Assumptions）。
- BR-003：敏感值与占位符的映射只存在于脱敏层处理该请求的内存中；不落盘、不写日志、不跨请求复用、不可事后查询。
- BR-004：上游改写（截断、拼错）占位符导致无法还原时，改写后的字符串原样透传给客户端；不重试、不报错。
- BR-005：把任一受保护渠道恢复为直连（回滚）必须是 owner 的显式操作并留有变更记录；不得因脱敏层故障而自动发生。

优先级分级：

| 编号 | 优先级 | 可降级方案 | 是否阻塞上线 |
| --- | --- | --- | --- |
| R-01 | P0 / Must | 不可降级（涉及隐私外泄） | 是 |
| R-02 | P0 / Must | 不可降级：不还原则 Claude Code 写出的代码里会留占位符，功能不可用 | 是 |
| R-03 | P0 / Must | 可降级为只开密钥类检测器（S/G/H），但需 owner 重新确认（OQ-3 已否决） | 是 |
| R-04 | P0 / Must | 不可降级 | 是 |
| R-05 | P1 / Should | 首期可用一条手工核对命令代替控制台标记 | 否 |
| R-06 | P0 / Must | 不可降级（fail-open 已被 OQ-4 否决） | 是 |
| R-07 | P0 / Must | 不可降级（开放代理即安全事故） | 是 |
| R-08 | P1 / Should | 头清理若与某上游鉴权冲突，可按渠道豁免该渠道并记录 | 否 |
| R-09 | P1 / Should | 首期至少完成一次人工验证并记录结果 | 否 |
| R-10 | P0 / Must | 不可降级 | 是 |

<!-- prd:section=acceptance_examples -->

## Acceptance Examples

```text
AE-01（对应 R-01、R-03）
Given 一条受保护渠道的上游临时指向 owner 控制的回显服务
When 发送一条用户消息与一条工具结果，内容含伪造的 sk- 密钥、邮箱、PRC 手机号、PEM 私钥块，且伪造密钥出现两次
Then 回显服务记录到的正文里这些值全部是 {{Redact:64位十六进制摘要}} 形式的占位符，原值一次都不出现，且两次伪造密钥映射为同一个占位符

AE-02（对应 R-02、R-09）
Given 一条真实的受保护第三方渠道
When 用户消息要求模型「原样复述下面这个密钥」并给出一个伪造 sk- 密钥
Then 客户端收到的回复里是该伪造密钥的原值，而不是占位符

AE-03（对应 R-02、R-10）
Given 流式请求经受保护渠道发出
When 上游把一个占位符切分在多个 SSE 事件 / HTTP 块中回显
Then 客户端收到完整还原后的原值；首个输出块在上游首块到达后即发出，不等待整个响应

AE-04（对应 R-06）
Given 脱敏层进程已停止，且存在另一条可用的受保护渠道或豁免渠道
When 请求被路由到一条受保护渠道
Then 该渠道的尝试失败；请求按现有故障转移策略切到另一条受保护渠道或豁免渠道；回显 / 抓包证明第三方上游没有收到任何明文正文

AE-05（对应 R-06，异常）
Given 受保护渠道
When 请求体为非 JSON（例如 multipart 音频上传）
Then 请求被拒绝并返回错误，不转发到上游；客户端收到错误响应

AE-06（对应 R-07）
Given 脱敏层配置了允许的上游主机集合
When 收到一条指向集合外主机的转发请求
Then 返回拒绝，且不向该主机发起任何连接

AE-07（对应 R-04、R-05）
Given 27 个渠道，且只有官方 openai（id 26）被 owner 标记为豁免
When owner 核对「未经脱敏层的渠道」清单
Then 清单只含官方 openai 一条；任一第三方渠道出现在清单中即判为不通过

AE-08（对应 R-08）
Given 受控回显上游
When 经受保护渠道发送请求
Then 回显记录中包含渠道凭据头且上游鉴权成功；不包含 X-Forwarded-For、X-Real-IP、CF-Connecting-IP、Cookie

AE-09（对应 R-10）
Given Claude Code（Anthropic Messages）与 Codex（OpenAI Responses）保持现有配置
When 不改任何客户端配置发起含工具调用的流式对话
Then 对话正常完成，工具调用参数与工具结果往返正确，与启用前体验一致

AE-10（对应 R-03，负向，BR-002）
Given 受保护渠道 + 受控回显上游
When 用户消息中含 12 个 BIP39 英文单词组成的助记词
Then 回显正文中助记词原样出现（这是本期已声明的边界，本例用于确认文档口径与行为一致，不是缺陷）

AE-11（对应 BR-004）
Given 真实模型渠道
When 模型在回复中改写了占位符（例如截断为 {{Redact:abc）
Then 客户端收到改写后的字符串原样；请求不重试，不返回错误
```

## Negative Acceptance

- 不得为了让 AE-04 通过而把任何受保护渠道回退为直连；回退只能是 BR-005 的显式操作。
- 不得在脱敏层、nginx 或 axonhub 日志中新增请求正文、占位符映射或运行时盐的输出。
- 不得改变客户端可见的 API 路径、鉴权头或错误格式（上游错误仍按 axonhub 现有方式透传）。
- 不得修改 axonhub 代码或替换镜像版本来实现本期需求（OQ-7）。
- 不得把豁免渠道以外的任何渠道排除在脱敏层之外，即使它是「看起来可信」的中转站。

<!-- prd:section=scope_boundaries -->

## Scope Boundaries

### 本期做

- 以 CosyRedactGateway 作为可逆脱敏层，以独立 sidecar 形式加入服务器现有 axonhub compose，只在内部网络可达（OQ-1、OQ-7）。
- 所有 26 个第三方渠道纳入受保护渠道；官方 `api.openai.com` 渠道作为唯一豁免渠道（OQ-2）。
- 启用全部七类检测器（OQ-3）。
- fail-closed 失败语义与故障转移边界（OQ-4）。
- 一条可复现的验证路径与验收记录（OQ-5）。
- 渠道信任边界的可核对性（R-05）与新增渠道流程约束。

### 本期不做（Non-Goals）

- 钱包助记词、钱包地址、WIF 私钥的专用检测（OQ-3；BR-002）。
- 运行时「是否发生脱敏 / 替换了多少个值」的可见信号或审计事件（OQ-5）。
- 本机 Postgres 中请求/响应正文明文存档的加密、脱敏或保留期限治理（OQ-6）。
- 上游响应中的恶意指令 / 提示注入防护（脱敏层只处理出站泄露方向）。
- 配置或启用 axonhub 内置 Prompt Protection Rules（OQ-1；保留为后续可选补充）。
- 图片、音频、文件等二进制内容的检查；非 JSON 端点的支持（现状无此流量，且 fail-closed 已接受）。
- 修改 axonhub 代码、自建镜像、把脱敏层放在客户端与 axonhub 之间（OQ-7）。
- 多用户 / 多租户差异化策略（单用户实例）。

### 与其它模块/需求的关系

- 依赖 axonhub 渠道 Base URL / 端点配置能力（`docs/en/guides/channel-management.md` §Base URL Special Configuration）。
- 与 axonhub 内置 Prompt Protection Rules（`docs/en/guides/prompt-protection-rules.md`）可叠加：内置规则在选渠道之后、调上游之前生效，脱敏层在其后；本期不配置。
- 后续候选：本机存档治理（可复用 `config.example.yml` 的 `gc` 与 DataStorage 能力）。

### 跨地区 / 市场 / tenant 边界

不涉及：单实例、单用户、无地区差异。

## Exception Handling

| 场景 | 系统表现 | 用户提示 | 是否可重试 | 是否产生状态/数据/审计副作用 |
| --- | --- | --- | --- | --- |
| 脱敏层进程不可用 | 受保护渠道尝试失败，按现有策略转移到其它受保护/豁免渠道；无可用渠道则请求失败 | 客户端收到 axonhub 现有的上游失败错误 | 是 | 可能触发渠道自动禁用（恢复方式见 Planning Recheck） |
| 上游不可用 / 5xx | 脱敏层原样透传上游错误 | 与现状一致 | 是 | 与现状一致 |
| 请求体非 JSON | 脱敏层拒绝（415 类错误），不转发 | 客户端收到错误 | 否（需改用 JSON 端点） | 无 |
| 请求体超体积上限（默认 16 MiB） | 拒绝，不转发 | 客户端收到错误 | 否 | 无 |
| 唯一敏感值数量超上限（默认 16384） | 拒绝，不转发 | 客户端收到错误 | 否 | 无 |
| 上游返回重定向 | 不跟随，按错误处理 | 客户端收到错误 | 视上游而定 | 无 |
| 模型改写占位符 | 原样透传（BR-004） | 用户看到被改写的串 | 用户自行重问 | 无 |
| 脱敏层重启导致运行时盐变化 | 后续请求生成不同占位符，功能不受影响 | 无 | — | 上游侧提示缓存前缀失效（见 NFR） |
| 新增渠道未纳入受保护集合 | 该渠道明文直连，属于配置错误 | R-05 清单可发现 | — | 已外发内容不可撤回 |

## Data / Compliance Boundaries

| 数据/记录 | 类型 | 展示规则 | 操作规则 | 留痕/保存口径 | 待确认项 |
| --- | --- | --- | --- | --- | --- |
| 请求正文中的密钥/令牌/私钥块 | 凭据类敏感数据 | 上游：只见占位符；客户端：原值 | 出站替换、回程还原 | 脱敏层不保存；本机 Postgres 现状明文（本期不动） | 无 |
| PRC 身份证号、手机号、银行卡号、邮箱 | 个人信息 / 敏感个人信息 | 同上 | 同上 | 同上 | 无 |
| 占位符 ↔ 原值映射 | 运行时派生数据 | 不可见 | 仅请求内内存态 | 不落盘、不记录、请求结束即丢弃 | 无 |
| 运行时盐 | 密钥材料 | 不可见 | 进程内生成 | 不导出、不记录 | 无 |
| 渠道凭据 | 凭据 | 不变 | 经内部网络原样转发到上游 | 与现状一致 | 无 |
| 请求/响应正文存档 | 业务记录 | 与现状一致（控制台可见） | 与现状一致 | 现状保留，本期不动（OQ-6） | 后续候选 |

## 非功能需求（NFR）

| 类别 | 产品级要求 | 衡量口径 | 是否阻塞上线 |
| --- | --- | --- | --- |
| 性能 / 时延 | 流式首块与整体完成时间不因脱敏层出现 owner 可感知的恶化；无既定目标值 | 验收时对同一渠道、同一提示词各测 3 次，记录启用前后首块时延与总时长，写入验收记录 | 否（记录即可） |
| 并发 / 容量 | 单用户场景，与现状一致；单请求体上限 16 MiB | 大文件工具结果（≥1 MiB）往返成功 | 否 |
| 可用性 / 连续性 | 脱敏层是受保护渠道的单点；进程异常退出后应自动拉起；不可用期间行为按 R-06 | 停止进程后自动恢复；恢复后受保护渠道恢复可用（恢复路径见 Planning Recheck） | 是（自动拉起） |
| 安全 | 脱敏层只在内部网络可达；只允许既定上游主机；不跟随重定向；渠道凭据不经公网多余一跳 | AE-06、AE-08 | 是 |
| 隐私 / 合规 | 映射与盐不持久化、不记录；上游不再收到命中类别的原值 | AE-01、日志检查 | 是 |
| 可观测 | 本期不要求脱敏事件信号（OQ-5）；进程存活与错误率沿用现有容器健康检查 | 健康检查通过 | 否 |
| 成本 | 占位符（约 75 字符）与固定英文提示注入使上游 token 略增；脱敏层重启会使上游提示缓存前缀失效 | 不设目标值；作为已知影响记录 | 否 |
| 可访问性 / 国际化 | 不涉及 | — | — |

## Release / Operation Readiness

| 项 | 结论 | 证据 / 决定路径 | 是否阻塞上线 |
| --- | --- | --- | --- |
| 必要专业审阅 | 不涉及；威胁模型由 owner 自定 | OQ-1～OQ-7 | 否 |
| 灰度策略 | 先只把一条第三方渠道切到脱敏层并完成 AE-01～AE-03、AE-09，再切其余 25 条 | 本 PRD | 是 |
| 老版本兼容 | 客户端零改动（R-10）；axonhub 版本不变 | OQ-7 | 否 |
| 存量数据处理 | 无迁移；历史存档不动（OQ-6） | — | 否 |
| 运营 SOP | 新增渠道：先纳入受保护集合或显式豁免，再启用（R-04）；定期执行 R-05 核对 | 本 PRD | 否 |
| 版本锁定 | 脱敏层锁定到已验收的具体提交（当前依据 v0.3.0 / `dfce67a17c30`），升级需重跑 AE-01～AE-03 | 取证快照 §3 | 否 |
| 回滚后用户感知 | 回滚 = 显式把渠道恢复直连（BR-005）；客户端无感知，但隐私防护随之消失，需记录 | BR-005 | 否 |
| 部署配置可追溯 | 服务器 compose / nginx 不在 git 中；本期至少要求变更前后备份并记录 | Planning Recheck | 否 |

## Dependencies / Constraints / Risks

**前置依赖**

| 依赖项 | 依赖方 | 需要对方做什么 | 是否阻塞开工 | 当前状态 |
| --- | --- | --- | --- | --- |
| CosyRedactGateway | 第三方开源项目（单作者，v0.3.0，MIT，2026-09-11 仍活跃） | 无；本期按锁定提交使用 | 否 | 已就绪 |
| axonhub 渠道 Base URL 自定义能力 | axonhub v1.0.0-beta7 | 允许把出站地址指向脱敏层并保留路径拼接 | 否（有 `##` 原样模式兜底） | 待 plan 实测 |
| 服务器 docker 运行时 | 现有 | 能运行一个 Node 20+ 容器 | 否 | 已就绪 |

**约束**

- 不修改 axonhub 代码、不替换镜像版本（OQ-7）。
- 脱敏层不得对公网暴露，不得成为开放代理（R-07）。
- 不引入新的日志面暴露正文或映射（Negative Acceptance）。

**交付风险与缓解**

| 风险 | 影响 | 缓解 / 接受 |
| --- | --- | --- |
| 检测漏报：未被任何检测器命中的敏感值仍会外发 | 隐私泄露 | 接受（OQ-1）；owner 行为约束（BR-002）；后续可叠加内置规则 |
| 检测误报：普通高熵串（如哈希、base64 片段、随机 ID）被替换 | 模型看不到该值，可能影响回答质量；回程可还原 | 接受；验收 AE-09 观察编码场景是否可用 |
| 模型改写占位符 | 用户看到坏串 | 接受（BR-004） |
| 脱敏层单点故障 | 受保护渠道整体不可用 | fail-closed 已接受（OQ-4）；自动拉起（NFR） |
| 渠道健康探测在脱敏层宕机期间失败并自动禁用渠道 | 恢复后需要手工或自动重新启用 | Planning Recheck 确认恢复路径并写入 SOP |
| 提示缓存失效（重启换盐） | 上游成本与时延略增 | 接受；尽量减少脱敏层重启 |
| 上游依赖 `X-Forwarded-*` 等被移除的头 | 个别上游鉴权或风控异常 | R-08 允许按渠道豁免并记录 |
| CosyRedactGateway 停更或引入破坏性变更 | 长期维护成本 | 锁定提交；升级必须重跑验收 |
| 新增渠道忘记纳入受保护集合 | 明文旁路 | R-04 默认值 + R-05 核对 + SOP |

<!-- prd:section=evidence_assumptions -->

## Evidence And Assumptions

| 主张 | 类型 | 证据来源 / 为何是假设 | 确认路径 |
| --- | --- | --- | --- |
| 线上 axonhub 是官方镜像 v1.0.0-beta7，三容器 compose，加固配置如取证快照所述 | confirmed-source | SSH 只读 `docker ps` / compose | 已确认 |
| 27 个渠道中 26 个为第三方中转站、1 个官方 openai；1 个 API key；近 30 天协议分布 | confirmed-source | Postgres 只读查询（未读凭据与 key 值） | 已确认 |
| 全部请求正文存档在本机 Postgres | confirmed-source | `requests` 表 `content_saved` | 已确认 |
| axonhub 内置 Prompt Protection Rules 为正则 mask/reject，当前 0 条 | confirmed-source | `docs/en/guides/prompt-protection-rules.md`；表计数 | 已确认 |
| CRG 支持的三种协议、流式还原、fail-closed、头策略、`REDACT_ALLOWED_HOSTS`、无助记词检测、无自定义规则 | confirmed-source | 浅克隆源码、README、SECURITY.md、测试文件（取证快照 §3） | 已确认 |
| CRG URL 封套接受 axonhub 追加路径与查询串后的完整上游 URL | confirmed-source | 本地 node 实测 `parseProxyTarget`（取证快照 §4） | 已确认 |
| axonhub 对含 `$` 的 Base URL 会按现有规则追加路径且不转义 `$` | source-candidate | 仅读文档，未实测；若不成立可用每端点 `##` 原样模式 | Planning Recheck（不改 WHAT） |
| hex 私钥与 `0x` 地址会被高熵检测器命中 | source-candidate | 依据 README 对随机 hex/base62 的召回描述，未针对钱包格式实测 | 验收前实测一次并写入记录；不改 R-03 |
| 公网入口经 Cloudflare 隧道 | source-candidate | nginx 使用 Cloudflare 头；未读 cloudflared 配置 | 不影响本期 WHAT |
| 渠道自动禁用后的恢复方式 | source-candidate | 仅见 `auto_disabled_at` 列 | Planning Recheck |
| 客户端全部为 JSON 协议，因此 fail-closed 对非 JSON 的拒绝不影响现有使用 | confirmed-source + assumption | 30 天流量 100% JSON；假设未来不新增音频/文件端点使用 | 若新增此类使用需重新评估 |
| 脱敏层引入的时延对 owner 不可感知 | assumption | 单进程内存正则；未实测 | NFR 验收记录 |
| 提示缓存失效仅在脱敏层重启时发生 | assumption | 同一运行时同一原值映射稳定（README） | 实测观察 |
| beta7 的 `#/##` 规则与内置规则文档与所读版本一致 | assumption | 文档来自 fork 2026-08-30 与上游 2026-09-10；beta7 具体提交未核对 | Planning Recheck |
| 威胁模型只包含「第三方上游看到出站内容」，不包含上游返回的恶意指令 | user-stated | owner 原话与 OQ-1～OQ-7 | 已确认（Non-Goal） |

### Engineering Clarification Coverage Pack

| coverage_item | status | source_tag | evidence_ref | deferred_owner | deferred_unblock_condition |
| --- | --- | --- | --- | --- | --- |
| source_authority | filled | mixed | OQ-1～OQ-7（product-owned）；取证快照 §2～§5（engineering-owned） | — | — |
| current_state | filled | confirmed-source | Current System Snapshot；取证快照 §2 | — | — |
| change_delta | filled | user-stated + confirmed-source | Change Delta 表 | — | — |
| requirements_acceptance | filled | — | R-01～R-10 ↔ AE-01～AE-11 全部有映射 | — | — |
| scope_boundaries | filled | user-stated | Scope Boundaries；Non-Goals 8 条 | — | — |
| owner_oq_trace | filled | user-stated | Outstanding Questions OQ-1～OQ-7 ↔ Owner Decision Trace 逐行绑定 | — | — |
| stakeholders_actors | filled | confirmed-source | Actors（单用户 + 客户端 + 不可信上游 + 脱敏层） | — | — |
| interaction_exception | filled | confirmed-source | Exception Handling 9 行 | — | — |
| data_compliance_security | filled | confirmed-source + user-stated | Data / Compliance Boundaries；R-07、R-08 | — | — |
| nfr_operational | filled | assumption（时延）+ confirmed-source（限制值） | NFR 表 | — | — |
| design_source | not-applicable | — | 无 UI / 设计稿 | — | — |
| cross_surface_consistency | filled | confirmed-source | Producer / Artifact / Consumer；Source-Of-Truth Resolution | — | — |
| release_rollout | filled | user-stated | Release / Operation Readiness（灰度、回滚 BR-005、版本锁定） | — | — |
| regression_guard | filled | confirmed-source | Negative Acceptance；R-10 / AE-09 | — | — |
| handoff_context_slice | filled | — | Handoff 段 | — | — |
| supporting_evidence_refs | filled | confirmed-source | 取证快照全文；`source_inputs` 六个文件 | — | — |

## Planning Recheck

| item | why recheck | required before | blocks planning? |
| --- | --- | --- | --- |
| axonhub 对含 `$` 的渠道 Base URL 的拼接与转义行为（六种渠道类型：anthropic、openai_responses、zhipu_anthropic、deepseek、xai、openai） | 仅读文档未实测；决定用「Base URL 指向脱敏层」还是「每端点 `##` 原样模式」实现 R-01，不改 WHAT | 选定实现方式前 | no |
| 渠道健康探测与模型列表同步经脱敏层的 GET/无正文请求是否正常；脱敏层宕机导致渠道自动禁用后的恢复路径 | 影响 Release/Ops 的 SOP 与 NFR 可用性验收，不改 WHAT | 编写 SOP 前 | no |
| 线上 v1.0.0-beta7 与所读文档版本（fork 2026-08-30 / 上游 2026-09-10）在 `#/##` 规则、内置规则上的差异 | 文档可能领先于线上版本 | 选定实现方式前 | no |
| 脱敏层容器的监听地址、端口、允许上游主机集合的取值来源与同步方式 | HOW；R-07 要求集合与受保护渠道主机一致 | 实现前 | no |
| 服务器 compose / nginx 纳入版本控制的方式 | Source-of-truth 风险；本 PRD 只要求可追溯 | 变更前 | no |
| 钱包 hex 私钥 / `0x` 地址被高熵检测器命中的实测 | source-candidate；结果只写入验收记录 | 验收时 | no |

## Decision Notes

| 决策 | 结论 | 理由 | 影响的 PRD 段落 |
| --- | --- | --- | --- |
| 机制选型 | CRG 可逆脱敏为主层，内置规则本期不配置 | 可逆还原是 Claude Code 场景可用性的前提；内置规则不可逆且规则需自写 | R-01～R-03、Non-Goals |
| 信任边界 | 默认全部渠道受保护，仅官方直连可豁免 | 威胁模型是第三方中转站；保留一条可信直连作为故意发送敏感值的出口 | R-04、R-05、BR-001 |
| 检测范围 | 七类检测器全开；助记词列为 Non-Goal | CRG 无助记词检测器，自造正则误报高；owner 以行为约束替代 | R-03、BR-002、AE-10 |
| 失败语义 | fail-closed，可转移到受保护/豁免渠道 | 隐私优先于可用性，但不因单点让整站不可用 | R-06、BR-005、AE-04 |
| 可见性 | 只要可验证路径，不要运行时信号 | CRG 有意不记录事件；改造范围过大 | R-09、NFR 可观测 |
| 本机存档 | 不纳入本期 | 威胁模型在上游；服务器自有 | Non-Goals、Data / Compliance |
| 拓扑 | 服务器 compose 加 sidecar，改渠道出站 | 不改代码即可实现，升级解耦 | Change Topology、Negative Acceptance |
| PRD 存放位置 | `E:\axonhub`（owner 的 fork） | owner 选择；非产品决策 | 本文件路径 |

<!-- prd:section=outstanding_questions -->

## Outstanding Questions

| id | question | PRD write target | owner_status | blocks_planning | closure_disposition | planning_would_invent_what | closure_state | recommended_default/deferred_reason |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| OQ-1 | 隐私脱敏采用哪种机制与分层：CRG 可逆脱敏 / 只用 axonhub 内置规则 / 两层叠加？ | R-01、R-02、R-03、Non-Goals | answered | no | owner-answered | no | closed | owner 选定 CRG 为主层并接受其文档化限制 |
| OQ-2 | 哪些上游渠道必须经过脱敏层？ | R-04、R-05、BR-001、AE-07 | answered | no | owner-answered | no | closed | owner 选定默认全部受保护、仅官方直连可豁免 |
| OQ-3 | 本期必须脱敏的敏感类别是什么？助记词怎么处理？ | R-03、BR-002、AE-10 | answered | no | owner-answered | no | closed | owner 选定七类全开、助记词列为 Non-Goal |
| OQ-4 | 脱敏层不可用或拒绝请求时的结果？ | R-06、BR-005、AE-04、AE-05 | answered | no | owner-answered | no | closed | owner 选定 fail-closed，可转移到受保护/豁免渠道 |
| OQ-5 | 需要运行时可见信号还是只要可验证的验收路径？ | R-09、AE-01、AE-02、NFR 可观测 | answered | no | owner-answered | no | closed | owner 选定只要可验证路径 |
| OQ-6 | 本机 Postgres 明文存档是否纳入本期？ | Non-Goals、Data / Compliance Boundaries | answered | no | owner-answered | no | closed | owner 选定不纳入、记为风险 |
| OQ-7 | 脱敏层以什么形态进入中转站？ | Change Topology、Negative Acceptance、Release / Operation Readiness | answered | no | owner-answered | no | closed | owner 选定服务器 compose 加 sidecar |

<!-- prd:section=owner_decision_trace -->

## Owner Decision Trace

| question | owner_answer/source | chosen_answer | PRD write target | consequence | closure_state |
| --- | --- | --- | --- | --- | --- |
| 隐私脱敏采用哪种机制与分层？（OQ-1） | owner 于 2026-09-12 通过阻塞问答选择「CRG 为主层 (Recommended)」 | CosyRedactGateway 作为可逆脱敏主层，接受其文档化限制（占位符被改写不可还原、二进制不检查、检测有漏报、无自定义规则）；内置 Prompt Protection Rules 保留为可选补充，本期不配置 | R-01、R-02、R-03、Non-Goals、BR-004 | 需求围绕「替换 + 还原」展开；漏报与改写风险作为已接受风险记录 | closed |
| 哪些上游渠道必须经过脱敏层？（OQ-2） | owner 选择「默认全包，官方可豁免 (Recommended)」 | 默认所有渠道受保护；只有 owner 显式标记的官方直连渠道（当前仅 id 26 `openai`）可不经脱敏层 | R-04、R-05、BR-001、AE-07 | 故意发送敏感值只能走豁免渠道；新增渠道默认受保护，需要可核对清单 | closed |
| 本期必须脱敏的敏感类别？助记词怎么处理？（OQ-3） | owner 选择「CRG 全开，助记词列为非目标 (Recommended)」 | 启用 H/P/S/I/B/E/G 全部检测器；钱包助记词不在检测范围，写成显式 Non-Goal 并附行为约束 | R-03、BR-002、AE-10 | 不引入自定义正则层；助记词防护依赖 owner 行为 | closed |
| 脱敏层不可用或拒绝请求时的结果？（OQ-4） | owner 选择「fail-closed，可转移到受保护渠道 (Recommended)」 | 该次受保护渠道尝试失败；允许按现有机制转移到其它受保护渠道或豁免渠道；任何情况下不回退到明文直连第三方 | R-06、BR-005、AE-04、AE-05 | 脱敏层成为受保护渠道的单点；回滚只能是显式操作 | closed |
| 需要运行时可见信号还是只要可验证路径？（OQ-5） | owner 选择「只要可验证，不要运行时信号 (Recommended)」 | 本期不要求脱敏事件信号；验收依靠受控回显上游 + 真实模型往返的可复现路径 | R-09、AE-01、AE-02、NFR 可观测 | 不改造 CRG / axonhub 以输出事件 | closed |
| 本机 Postgres 明文存档是否纳入本期？（OQ-6） | owner 选择「不纳入本期，记为风险 (Recommended)」 | 本期不动本机存档；只在 PRD 记为已知相关风险与后续候选 | Non-Goals、Data / Compliance Boundaries | 本期范围保持单一：出站泄露方向 | closed |
| 脱敏层以什么形态进入中转站？（OQ-7） | owner 选择「服务器 compose 加 sidecar (Recommended)」 | 在服务器现有 axonhub compose 中新增独立脱敏容器，只通过改渠道出站地址导入流量；不改 axonhub 代码、不换镜像 | Change Topology、Negative Acceptance、Release / Operation Readiness | 升级解耦；可行性依赖 URL 拼接规则（CRG 侧已实测，axonhub 侧进 Planning Recheck） | closed |

<!-- prd:section=readiness_self_check -->

## Readiness Self-Check

write_mode: final-prd

clarification_evidence: asked-owner

preflight_sweep_closure: closed

decision_card_highest_risk_gap: 脱敏机制与分层未定（OQ-1）——它决定后面所有需求条目、验收和范围

decision_card_next_action: final-prd

decision_card_why_no_invention: 七个会改变产品行为的决策（机制、信任边界、检测范围、失败语义、可见性、本机存档范围、拓扑）均由 owner 逐题答复并绑定到 R-01～R-10、BR-001～BR-005 与 AE-01～AE-11；规划无需推测「保护什么、保护到哪、失败怎么办、装在哪」，剩余项全部是不改 WHAT 的实现核对（Planning Recheck）

design_source_coverage: not-applicable

first_unclosed_owner_question: none

recommended default: none

can_enter_spec_plan: yes

why_not: none

当前状态：`status: draft`；ready receipt 由 finalize 脚本写入，本文件不自填。

## 变更记录

| 日期 | 修改人 | 变更内容 |
| --- | --- | --- |
| 2026-09-12 | owner（经 spec-prd 会话） | 初稿。由用户原话经远程服务器只读取证、CRG 与 axonhub 源码/文档取证、七轮 owner 澄清写成 |

## Handoff

下一步进入 `spec-plan`。规划需要注意：

1. **拓扑已定，实现方式未定**：R-01 的落地有两条路——渠道 Base URL 直接指向脱敏层并依赖 axonhub 追加路径，或每个端点用 `##` 原样模式写全 URL。CRG 侧对四种拼接形态已实测通过（取证快照 §4）；axonhub 侧含 `$` 的 Base URL 行为需先实测（Planning Recheck 第 1 行）。
2. **fail-closed 的连带效应**：脱敏层宕机会让渠道探测失败并可能触发自动禁用，恢复路径要在 SOP 里写清（Planning Recheck 第 2 行）。
3. **R-05 的最小实现**：首期可以是一条核对命令（列出出站不经脱敏层的渠道），不必改控制台；但 AE-07 必须能执行。
4. **不要越界**：不改 axonhub 代码、不换镜像、不启用内置规则、不碰本机存档（Non-Goals）。这些都是 owner 明确决定，不是遗漏。
5. **验收顺序**：先一条第三方渠道灰度跑 AE-01～AE-03、AE-08、AE-09，再切其余渠道，最后跑 AE-04～AE-07、AE-10、AE-11。
