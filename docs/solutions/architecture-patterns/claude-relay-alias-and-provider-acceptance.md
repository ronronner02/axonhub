---
title: Claude 中转别名、1M 后缀与 provider 验收边界
date: 2026-09-18
category: docs/solutions/architecture-patterns
module: restricted-claude-relay
problem_type: architecture_pattern
component: service_object
severity: high
source_refs:
  - "deploy/privacy-redaction/README.md"
  - "deploy/privacy-redaction/ACCEPTANCE-RECORD.md"
  - "deploy/privacy-redaction/OPERATIONS-LOG.md"
  - "docs/plans/2026-09-15-001-feat-restricted-claude-relay-plan.md"
invalidation_condition: "当 Claude Code 改变 [1m] 解析语义、AxonHub 改变模型 exact-string 路由，或任一 provider 的门禁条件变化时重新验收。"
applies_when:
  - "通过一个 AxonHub URL 和 Key 选择 anyrouter 或 agentrouter Claude 渠道时"
  - "判断配置完成是否等于 provider 已恢复时"
tags: [claude, logical-model, one-million-context, provider-acceptance]
---

# Claude 中转别名、1M 后缀与 provider 验收边界

## Context

AxonHub 的 logical model 使用 exact string 路由。上游真实模型名、AxonHub 中的专用
alias，以及 Claude Code 为 1M 上下文添加的 `[1m]`，属于三个不同层次。把它们混为
一谈会导致模型关联不匹配，或者把 provider 503 误判为 alias 配置问题。

## Guidance

### AxonHub 中保存不带 `[1m]` 的专用 alias

```text
any/claude-fable-5-1
agent/claude-opus-5
agent/claude-opus-4-8
```

关联中的上游模型也使用裸名：

```text
claude-fable-5-1
claude-opus-5
claude-opus-4-8
```

`[1m]` 不是发送给上游的模型名。Claude Code 在用户请求 1M 上下文时处理该后缀，
并附带相应 beta header；AxonHub 和 provider 侧看到的应是无后缀 alias/裸名。

### 客户端使用口径

需要 1M 上下文时，在 Claude Code 中选择：

```text
/model any/claude-fable-5-1[1m]
/model agent/claude-opus-5[1m]
/model agent/claude-opus-4-8[1m]
```

不需要 1M 上下文时，使用无后缀 alias。用户只配置 AxonHub URL 和 Key，不需要为
anyrouter 或 agentrouter 单独配置客户端 URL/Key。

### 配置完成不等于 provider 可用

必须分开记录：

1. **配置层**：alias enabled、关联正确、出站裸模型名正确、UA/header 和封套正确；
2. **机制层**：脱敏、还原、metadata 保留等本地测试通过；
3. **provider 行为层**：真实请求获得 HTTP 200 和合法 Anthropic message/SSE。

只有第三层通过，才能说“可以直接使用”。容器 healthy、`/health` 200、alias 已创建，
都不能替代真实 generation 验收。

## Why This Matters

- 避免把 `[1m]` 字面量错误发送给只接受裸模型名的上游；
- 保留 Claude Code 的 1M 上下文使用方式；
- 避免因本地服务健康而错误宣称 provider 恢复；
- 让 anyrouter 503、agentrouter 401/402 等上游问题与 alias 配置问题分层排查。

## When to Apply

- 新建或调整 Claude logical model；
- Claude Code 报模型不存在、401、402、503；
- 更新 provider 凭据或门禁配置后；
- 对外说明 Claude relay 是否可直接使用前。

## Examples

2026-09-18 的证据表明：any alias 已正确改写成裸上游模型名，但真实 anyrouter 请求
仍返回 503；agent 渠道已经打开 UA 透传，但真实门禁验收未执行。因此当时的准确表述
是“配置完成，provider 可用性未关闭”，而不是“已经可以稳定使用”。

## Related

- `docs/solutions/best-practices/onboard-new-channels-through-privacy-redaction.md`
- `deploy/privacy-redaction/README.md` 第 11 节

