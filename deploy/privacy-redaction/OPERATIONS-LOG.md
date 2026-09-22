# 服务器侧变更记录（BR-005）
#
# 任何把受保护渠道恢复为直连、改写渠道 Base URL、修改 REDACT_ALLOWED_HOSTS、
# 启停 redact 服务、升级 CRG vendor 的操作，都必须在此追加一行。
# 回滚必须是显式操作并留有本记录；不得因脱敏层故障而自动发生。
#
# 填写约定：
#   日期：ISO 日期，当地时区
#   操作人：执行该操作的人
#   动作：rewrite / reflag / restore / env-change / config-change / deploy / upgrade / recover-disabled / rollback
#         reflag        改受保护渠道封套的检测标志位（如 $ -> PSIBEG$，关闭高熵检测）；须写明关闭了哪些检测
#         config-change 渠道模型列表 / 默认测试模型 / User-Agent 透传 / 模型条目与渠道关联
#   对象：渠道 id 列表、环境变量名、vendor 提交号等
#   结果：成功 / 失败 / 部分
#   备注：回滚原因、失败现象、关联验收记录行

| 日期 | 操作人 | 动作 | 对象 | 结果 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 2026-09-12 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS=<ch10主机>,echo | 成功 | 阶段2 前置；重建 redact |
| 2026-09-12 | claude(经 owner SSH) | rewrite | channel 10 (ch10) | 成功 | 阶段2 灰度改写；上游当日额度耗尽(429)无法做 AE-02/03，改用渠道27 补测 |
| 2026-09-13 | claude(经 owner SSH) | verify | 阶段2 验收 ch10 | 成功 | AE-02/03/09/11、大文件、时延 x3 全部通过；见 ACCEPTANCE-RECORD |
| 2026-09-13 | claude(经 owner SSH) | tag | channel 26 (官方 OpenAI) += redact-exempt | 成功 | 阶段3 步骤1，唯一豁免渠道（BR-001）|
| 2026-09-13 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS=<10 个第三方精确主机>,echo | 成功 | 阶段3 步骤2；重建 redact |
| 2026-09-13 | claude(经 owner SSH) | verify | AE-07 | 成功 | 阶段3 完成：23 受保护 / 1 豁免(#26) / 0 违规 / 主机集合一致 |
| 2026-09-13 | claude(经 owner SSH) | verify | AE-04 停 redact / 临时禁用#26 / 恢复 | 见 ACCEPTANCE-RECORD | 阶段4；#26 已恢复 enabled；redact 已恢复 healthy |
| 2026-09-13 | claude(经 owner SSH) | tag | channel 26 (官方 OpenAI) tags=["redact-exempt"]（appendTags 在 beta7 无效，改整体写入） | 成功 | 阶段3 步骤1，唯一豁免渠道（BR-001）|
| 2026-09-13 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS=<8 个第三方精确主机>,echo（先误含 2 个软删除渠道主机，随后剔除） | 成功 | 阶段3 步骤2；docker rm -f + up -d 重建 redact |
| 2026-09-13 | claude(经 owner SSH) | rewrite | 21 条第三方渠道 (1-9, 11-19, 21, 22, 25) | 成功 21 / 失败 0 | 阶段3 步骤3 全量改写；#20/#24 为 ent 软删除不改，#26 豁免，#10/#27 已在阶段2 保护 |
| 2026-09-13 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS 移除 echo；docker rm -f axonhub-redact-echo | 成功 | 阶段3 收尾；AE-07 终态核对 EXIT=0 |
| 2026-09-13 | claude(经 owner SSH) | verify | AE-04：docker stop axonhub-redact 两次（01:44:52–01:45:15、01:45:45–01:45:54 UTC）；期间试图 updateChannel(status:disabled) #26 无效 | 成功 | 阶段4；两次 docker start 后 7s healthy；auto_disabled_at 全程为空；#26 全程 enabled |
| 2026-09-15 | claude(经 owner SSH) | deploy | axonhub-redact 重建为 entry.mjs 封装（parse 前剥离超大媒体） | 成功 | vendor worker.js 未改；CMD=node entry.mjs；healthy；同网络 axonhub-app 仍可达 /healthz；3.4MB data-URI 容器内剥离 32ms |
| 2026-09-18 | codex(经 owner SSH) | deploy | axonhub-redact:local（entry.mjs + media-extract.mjs 修复） | 成功 | 先起候选容器并用正式网络别名测活，再摘除旧 sidecar；旧容器保留为 axonhub-redact-prev-20260918；axonhub-app/gateway/postgres 未重启，持续 Up 2 weeks (healthy) |
| 2026-09-18 | codex(经 owner SSH) | reflag | channels 3,8,11,12,13：`$` -> `PSIBEG$` | 成功 5 / 失败 0 | 仅关闭高熵检测；PII、Secret、IBAN、Email、GCP API Key 检测保留 |
| 2026-09-18 | codex(经 owner SSH) | rewrite | channels 28,29,30,31 | 成功 4 / 失败 0 | 新增 enabled 第三方直连渠道按默认 `$` 全检测封套纳入保护 |
| 2026-09-18 | codex(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS：移除 2 个仅对应软删除渠道的旧主机，加入 channels 28–31 当前上游 | 成功 | 终态 10 个当前真实上游主机；核对脚本报告允许主机集合一致 |
| 2026-09-18 | codex(经 owner SSH) | config-change | channels 11,12,13 supported_models/default_test_model | 成功 | 三条均只保留 `claude-fable-5-1`；`headerOverrideOperations` 中 anthropic-beta 1m 覆盖保持不变 |
| 2026-09-18 | codex(经 owner SSH) | config-change | channels 3,8 passThroughUserAgent | 成功 | 渠道级开关设为 true；全局设置未改；原有 environment proxy 语义保持不变 |
| 2026-09-18 | codex(经 owner SSH) | config-change | models any/claude-fable-5-1、agent/claude-opus-5、agent/claude-opus-4-8 | 成功 | 三条均 enabled；关联、优先级与裸上游模型名按 README 11.5；旧 `[1m]` 模型条目未改 |
| 2026-09-18 | codex(经 owner SSH) | verify | restricted relay AE1–AE6 | 部分 | AE5/AE6 通过，AE2 通过；AE1 request #17469 上游 503（11/12/13 各重试 3 次）后按停止条件终止，AE4 与行为层 AE7 未继续探测 |
| 2026-09-18 | codex(经 owner SSH) | deploy | axonhub-redact:candidate-20260918-r2 | 成功 | 代码审查收窄媒体路径：支持 `image_url.url`，普通 `url` 不再被本地封装剥离；媒体测试 14/14；alias 重叠切换；保留 axonhub-redact-prev-20260918 与 axonhub-redact-prev-review-20260918 两个回滚容器；核心三容器未重启 |
| 2026-09-18 | codex(经 owner SSH) | deploy | axonhub-redact:candidate-20260918-r3 | 成功 | 代码审查收紧原始正文体积门禁：已知 Content-Length 超限零读取，未知长度首次越限即取消 reader，保持 vendor 413 JSON 契约；媒体测试 16/16；alias 重叠切换；R2 保留为 axonhub-redact-prev-review-r2-20260918；核心三容器未重启 |
| 2026-09-18 | codex(经本机 AxonHub URL/Key) | verify | any/claude-fable-5-1、agent/claude-opus-5 | 失败 | 每个 alias 各一条 Anthropic-compatible 最小请求，携带 `claude-cli/2.1.274` User-Agent 与 1M beta header；any HTTP 503（1469ms），agent HTTP 503（1168ms），均为 `Service Unavailable`；无循环探测，不能宣称 provider 已恢复 |
| 2026-09-21 | codex(经 owner SSH) | deploy | axonhub-redact 切换为含控制字段豁免的修复版镜像（`536b0862`） | 成功 | 修 agent gpt-6-astra 的 `400 Invalid 'prompt_cache_key': string too long (99>64)`：CRG 高熵检测把 UUID 型 `prompt_cache_key` 改写成 99 字符占位符，超上游 64 上限。本地封装新增 `CONTROL_KEYS`（`prompt_cache_key` / `previous_response_id` / `safety_identifier`）抠出→拼回，vendor `worker.js` 未改（sha 仍 `23fabf64`）。build-context 源码同步为 `entry.mjs=5f5c88d2` / `media-extract.mjs=a5fbd19c`，旧源码留 `crg/.bak-20260921/`；alias 重叠零停机切换，旧容器保留为 `axonhub-redact-prev-astra-20260921`；核心三容器未重启 |
| 2026-09-22 | claude(经 owner SSH) | verify | gpt-6-astra 修复落地核验（只读） | 成功 | 运行容器内 `entry.mjs=5f5c88d2` / `media-extract.mjs=a5fbd19c`、`CONTROL_KEYS` 命中 3；`redact` 别名唯一解析到 `axonhub-redact`；build-context 与容器一致（compose 重建不会回退修复）；近 2 小时 gpt-6-astra 的 400 `prompt_cache_key` 计数 0（总执行 7 / 成功 4）；`axonhub-app` / `gateway` / `postgres` 保持 Up 3 weeks 未重启 |
| 2026-09-22 | claude(经 owner SSH) | deploy | `axonhub-redact:local` 重指向已验证镜像 `candidate-gpt6-20260922`；build-context `crg/entry.mjs`+`crg/media-extract.mjs` 同步为本地 HEAD 版 | 成功 | 修 compose 回退陷阱：原 `:local` 仍为 09-18 旧镜像（crg `59d69d95`/`dca9af41`），任一 `compose up -d` 会静默回退控制字段豁免与 encrypted reasoning 修复。本次为纯 docker tag + 写源码，未动运行容器、未重启任何服务；旧 `:local` 留存为 `:prerollback-20260922`，源码备份 `crg/.bak-20260922/`；vendor `worker.js` 未动（`23fabf64`）。终态：运行容器 / `:local` / build-context / 本地 HEAD 的 crg sha 四者一致（`4d93c83c`/`4f01d07b`） |
