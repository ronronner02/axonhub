---
title: 新渠道必须显式接入 privacy-redaction 信任边界
date: 2026-09-18
category: docs/solutions/best-practices
module: privacy-redaction
problem_type: best_practice
component: authentication
severity: critical
source_refs:
  - ".agent/rules/workflows/add-channel.md"
  - "deploy/privacy-redaction/README.md"
  - "deploy/privacy-redaction/scripts/redact-channels.sh"
  - "deploy/privacy-redaction/scripts/check-trust-boundary.sh"
  - "deploy/privacy-redaction/tests/check-trust-boundary.test.sh"
invalidation_condition: "当 AxonHub 新增自动渠道监听与强制封套能力，或 privacy-redaction 的路由、白名单、豁免语义变化时重新核对。"
applies_when:
  - "在生产环境新建或重新启用第三方渠道时"
  - "渠道上游主机、Base URL、endpoint 或脱敏 flags 发生变化时"
tags: [privacy, channel-onboarding, trust-boundary, fail-closed]
---

# 新渠道必须显式接入 privacy-redaction 信任边界

## Context

当前 AxonHub 创建渠道后，不会自动修改其 Base URL，也不会自动把新主机加入
`REDACT_ALLOWED_HOSTS`。`redact-channels.sh` 和 `check-trust-boundary.sh` 是显式运行
的改写与审计工具，不是持续监听器。

因此，新渠道如果直接以 enabled 状态上线，可能短时间保持明文直连；如果只改封套
但不更新 allowlist，则会 fail-closed 为 403。两种状态都需要在上线前通过固定顺序
消除。

## Guidance

### 安全接入顺序

1. 先把上游精确主机名加入服务器 `.env` 的 `REDACT_ALLOWED_HOSTS`；
2. 只重建或切换 `axonhub-redact` sidecar，不重启 AxonHub 核心容器；
3. 创建新渠道时先保持 disabled；
4. 对精确渠道 ID 运行 dry-run：

   ```bash
   cd /srv/apps/axonhub/privacy-redaction
   bash scripts/redact-channels.sh --only CHANNEL_ID
   ```

5. 确认计划为默认全检测封套后执行：

   ```bash
   bash scripts/redact-channels.sh --only CHANNEL_ID --apply
   ```

6. 启用渠道；
7. 运行信任边界核对：

   ```bash
   bash scripts/check-trust-boundary.sh
   ```

8. 只有“违规清单 0 + 允许主机集合一致”时才进入真实 provider 验收；
9. 用合成敏感值验证出站替换和回程还原，并记录操作与回滚命令。

### flags 选择

- 默认使用 `$`，即启用全部检测；
- 只有经过记录的上游协议兼容问题才能使用 `PSIBEG$` 等降级封套；
- 降级必须用 `--only` 限定渠道并通过 `--yes` 门禁；
- 不得把第三方渠道改为直连来绕过 provider 故障。

### 自动化边界

当前没有自动接管新渠道的 watcher。最低限度应把
`check-trust-boundary.sh` 接入定时任务或部署后门禁，使新增直连、endpoint 绕过和
allowlist 漂移能够被发现；在 watcher 实现前，创建渠道的操作流程必须负责执行改写。

## Why This Matters

- 防止新增渠道绕过脱敏层直接发送消息、工具输入和工具结果；
- 保证主机白名单保持 fail-closed；
- 防止 endpoint 与 base URL 一致性漂移；
- 把降级检测变成显式、可审计、可回滚的例外；
- 避免把健康检查误当成敏感数据保护和 provider 可用性验收。

## When to Apply

- 新建第三方渠道；
- 将 disabled 渠道重新启用；
- 更换 Base URL、endpoint 或上游域名；
- 调整 envelope flags；
- sidecar 或 allowlist 升级后进行回归检查。

## Examples

2026-09-18 发现四条新 enabled 渠道仍是直连。处理方式不是修改 AxonHub 核心服务，
而是先补 allowlist，再用默认 `$` 封套改写四条渠道，最后运行信任边界核对，终态为
23 条受保护、1 条明确豁免、0 条违规、允许主机集合一致。

## Related

- `.agent/rules/workflows/add-channel.md`
- `docs/solutions/architecture-patterns/claude-relay-alias-and-provider-acceptance.md`
