# 验收记录

CRG 锁定提交：`dfce67a17c308faf8c84c10b852623506cf444ff`（v0.3.0）
axonhub 版本：`v1.0.0-beta7`

每次升级 CRG 或 axonhub 后需重跑阶段 1～2 并新建一份本记录。

---

## AE 结论

结论填 `通过` / `不通过` / `未执行`。证据位置填命令输出所在文件、`docker logs` 片段路径或控制台截图位置。

| AE | 需求 | 阶段 | 结论 | 日期 | 证据位置 | 备注 |
| --- | --- | --- | --- | --- | --- | --- |
| AE-01 | R-01、R-03 出站脱敏与同值同占位符 | 1 | 通过 | 2026-09-12 | docker logs axonhub-redact-echo；本会话记录 | 生产 redact + echo；4 类原值零出现，5 占位符/4 唯一，重复密钥同占位符 |
| AE-02 | R-02、R-09 回程还原（非流式） | 2 | 通过 | 2026-09-13 | requests#6568 ch10 completed 2872ms | claude-opus-5-max 非流式复述伪造密钥，客户端得到原值、无占位符（昨日 429 为上游额度，今日重置后通过） |
| AE-03 | R-02、R-10 流式跨块还原 | 2 | 通过 | 2026-09-12 | requests#6563 ch27 completed stream；客户端得到完整原值，流中无占位符 | ch27 claude-sonnet-5；首块 2109ms/总 2775ms |
| AE-04 | R-06 脱敏层停止时 fail-closed 与转移 | 4 | 通过 | 2026-09-13 | requests#6600(gpt-5.6-sol→#26 成功,转移链 #14 dial失败×2→switches=2)、#6605(claude-opus-5-max 仅#10→500 dial tcp lookup redact 失败,switches=0,same_channel_retries=2)；#6602/6603 为本会话在停机窗口内发出的流式探测（source=api，与其它探测同源 IP），非 owner 真实流量 | 停 redact 01:44:52–01:45:15 与 01:45:45–01:45:54 UTC。受保护渠道对 redact 的连接失败被记为传输错误(非 HTTP 状态码)；有豁免候选时转移到 #26 明文成功(BR-001 允许)，无豁免候选时客户端收到 500 且零明文外发；auto_disabled_at 全程为空(自动禁用未配置) |
| AE-05 | R-06 非 JSON 正文被拒绝 | 1 | 通过 | 2026-09-12 | echo 无新增记录；CRG 415 | multipart 被 415 拒绝且未转发 |
| AE-06 | R-07 上游主机白名单 | 1 | 通过 | 2026-09-12 | CRG 403；echo 无新增 | not-allowed.internal 与 api.anthropic.com 均 403 |
| AE-07 | R-04、R-05 信任边界核对 | 3 | 通过 | 2026-09-13 | check-trust-boundary.sh 终态 EXIT=0；本会话记录 | 受保护 23、豁免仅 #26、违规 0、允许主机集合与渠道推导集合一致（8 主机）；#20/#24 为 ent 软删除（deleted_at<>0，GraphQL 不可见）与 #23 归档均不参与 |
| AE-08 | R-08 凭据头转发与身份头清理 | 1 | 通过 | 2026-09-12 | echo 记录头 | x-api-key/anthropic-* 转发；x-forwarded-for/x-real-ip/cf-connecting-ip/cookie 移除 |
| AE-09 | R-10 客户端零改动含工具调用流式会话 | 2 | 通过 | 2026-09-13 | requests#6569(stream tool_use)/#6570(tool_result 回传) | get_secret_len 工具名一致、参数中密钥还原为原值、流中无占位符、第二轮 200 |
| AE-10 | R-03 助记词为已声明边界（应原样外发） | 1 | 通过 | 2026-09-12 | echo 记录正文 | 12 词助记词原样外发（BR-002 口径一致） |
| AE-11 | BR-004 模型改写占位符原样透传 | 2 | 通过 | 2026-09-13 | requests#6571 ch10 completed；单行无重试 | 要求只输出前 12 字符：模型 thinking 明示看到的是 redacted 占位符并未复述原值；200 无重试无报错（BR-004 透传语义成立） |

P0 相关项（AE-01～AE-06、AE-08～AE-10）必须全部通过才算验收完成。

---

## 时延对比（NFR，记录即可，不阻塞）

同一渠道、同一提示词，启用脱敏层前后各测 3 次。

| 轮次 | 启用前首块 (ms) | 启用前总时长 (ms) | 启用后首块 (ms) | 启用后总时长 (ms) |
| --- | --- | --- | --- | --- |
| 1 | — | — | 1673 | 1743 |
| 2 | — | — | 1474 | 1670 |
| 3 | — | — | 1521 | 1571 |
| 中位数 | 19426(p50,历史1313条) | 29690(p50) | 1521 | 1670 |

结论（owner 是否可感知恶化）：无恶化。启用后 3 次首块 1.5–1.7s，远低于历史 p50 19.4s（历史含真实长提示，不完全同口径，仅作量级参考）；同提示词经 CRG 未见可感知增量。

---

## 钱包 hex 私钥 / `0x` 地址实测（OQ 遗留，只记录不改需求）

阶段 1 在回显上游观察，判断是否被高熵检测器命中。

| 样本类型 | 样本形态（勿填真实值） | 是否被替换 | 备注 |
| --- | --- | --- | --- |
| hex 私钥（64 位十六进制） | 64 hex | 已被替换为占位符 | 阶段1 生产 echo 实测 |
| `0x` 开头地址（40 位十六进制） | 0x+40 hex | 已被替换为占位符 | 阶段1 生产 echo 实测 |

结论：BR-002 声明助记词不在检测范围；本表只记录 hex 形态的实际行为，不构成需求变更。

---

## AE-04 观察到的 axonhub 行为（KTD-7）

验收前先记录系统重试设置，停 `redact` 后观察实际状态码。

| 项 | 值 | 来源 |
| --- | --- | --- |
| 最大渠道切换次数 | 3（MaxChannelRetries，代码默认；systems 表无覆盖） | beta7 internal/server/biz/system_default.go |
| 自动禁用启用状态 | 未配置（AutoDisableChannel 零值 = 关闭） | 同上 |
| 自动禁用触发状态码与次数 | 无（Statuses 为空） | 同上 |
| 停 `redact` 后 axonhub 实际记录的状态码 | 无 HTTP 状态码：`failed to do request: HTTP request failed: Post http://redact:8787/$… dial tcp: lookup redact` 传输错误；客户端最终 500 internal_server_error | axonhub-app 日志 / requests#6605 |
| 被自动禁用的渠道 id | 无（AutoDisableChannel 未配置） | channels.auto_disabled_at 全程 NULL |
| 客户端最终看到的结果 | 有豁免候选：200 来自 #26；无豁免候选：500 | requests#6600 / #6605 |
| 是否有第三方上游收到明文 | 否：受保护渠道全部在 dial redact 阶段失败，未建立到任何第三方上游的连接（axonhub 出站 URL 全为 redact:8787） | axonhub-app 日志 |
| 恢复方式与耗时 | `docker start axonhub-redact`，7s healthy；无需恢复渠道 | 本会话记录 |

判据（BR-001 允许的结果）：受保护渠道尝试失败并按现有策略转移；候选可能包含豁免渠道，此时明文会发往官方直连上游。
**判定标准是「任何第三方上游未收到明文」**，客户端可能收到错误，也可能收到豁免渠道的正常响应。

---

## 遗留观察

| 项 | 结论 | 备注 |
| --- | --- | --- |
| 服务器 compose 网络 YAML 键名 | 键名 axonhub-network（非 external，driver bridge）；实际网络名 axonhub_axonhub-network；overlay 复用上层声明 | docker-compose v1.29.2 config |
| 27 条渠道中是否存在空 Base URL / endpoint 覆盖 / `ws://` | 无空 URL、无 endpoint 覆盖、无 ws://；26 待写 + 1 归档 | redact-channels.sh dry-run |
| CRG 注入的英文 notice 是否影响回答质量 | AE-02/09 未见异常；AE-11 中模型 thinking 明确意识到“token is redacted”，属预期 | requests#6571 |
| 高熵误报是否替换掉代码中的哈希/ID | 阶段 2 编码类提示未做专项；真实使用中观察 | 后续 |
| 大文件工具结果（≥1 MiB）往返 | 通过：1,080,326 B tool_result 经 ch10 往返 200，16.0s | requests#6572 |
