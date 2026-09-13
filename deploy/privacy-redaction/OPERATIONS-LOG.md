# 服务器侧变更记录（BR-005）
#
# 任何把受保护渠道恢复为直连、改写渠道 Base URL、修改 REDACT_ALLOWED_HOSTS、
# 启停 redact 服务、升级 CRG vendor 的操作，都必须在此追加一行。
# 回滚必须是显式操作并留有本记录；不得因脱敏层故障而自动发生。
#
# 填写约定：
#   日期：ISO 日期，当地时区
#   操作人：执行该操作的人
#   动作：rewrite / restore / env-change / deploy / upgrade / recover-disabled / rollback
#   对象：渠道 id 列表、环境变量名、vendor 提交号等
#   结果：成功 / 失败 / 部分
#   备注：回滚原因、失败现象、关联验收记录行

| 日期 | 操作人 | 动作 | 对象 | 结果 | 备注 |
| --- | --- | --- | --- | --- | --- |
| 2026-09-12 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS=<ch10主机>,echo | 成功 | 阶段2 前置；重建 redact |
| 2026-09-12 | claude(经 owner SSH) | rewrite | channel 10 sotamodel | 成功 | 阶段2 灰度改写；上游当日额度耗尽(429)无法做 AE-02/03，改用渠道27 补测 |
| 2026-09-13 | claude(经 owner SSH) | verify | 阶段2 验收 ch10 | 成功 | AE-02/03/09/11、大文件、时延 x3 全部通过；见 ACCEPTANCE-RECORD |
| 2026-09-13 | claude(经 owner SSH) | tag | channel 26 openai += redact-exempt | 成功 | 阶段3 步骤1，唯一豁免渠道（BR-001）|
| 2026-09-13 | claude(经 owner SSH) | env-change | REDACT_ALLOWED_HOSTS=<10 个第三方精确主机>,echo | 成功 | 阶段3 步骤2；重建 redact |
| 2026-09-13 | claude(经 owner SSH) | verify | AE-07 | 成功 | 阶段3 完成：23 受保护 / 1 豁免(#26) / 0 违规 / 主机集合一致 |
| 2026-09-13 | claude(经 owner SSH) | verify | AE-04 停 redact / 临时禁用#26 / 恢复 | 见 ACCEPTANCE-RECORD | 阶段4；#26 已恢复 enabled；redact 已恢复 healthy |
