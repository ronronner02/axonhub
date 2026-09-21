---
title: 将本地 AI 宿主运行时与产品改动分离
date: 2026-09-18
category: docs/solutions/workflow-issues
module: repository-governance
problem_type: workflow_issue
component: development_workflow
severity: medium
source_refs:
  - ".gitignore"
  - ".gitattributes"
  - "AGENTS.md"
  - ".agent/rules/workflows/add-channel.md"
invalidation_condition: "当 spec-first 改变宿主安装目录、项目决定共享这些宿主资产，或 Git 属性策略发生变化时重新核对。"
applies_when:
  - "本地安装或刷新 spec-first、Codex、OpenCode、Pi 等 AI 宿主能力时"
  - "一个功能工作树同时出现产品代码、计划文档、运行时缓存和工具安装文件时"
tags: [git-hygiene, spec-first, host-runtime, line-endings]
---

# 将本地 AI 宿主运行时与产品改动分离

## Context

本地安装 spec-first 后，`.agents/`、`.codex/`、`.opencode/`、`.pi/` 和
`.spec-first/` 可能产生数百个宿主文件。如果这些文件与产品补丁、计划、验收记录
和测试同时出现在 `git status` 中，后续很容易执行过宽的 `git add`，也难以判断
哪些变化属于功能交付。

同一轮工作还出现了根级 Markdown 和 `.gitignore` 的 CRLF/LF 整文件噪声，使真实的
十几行治理修改表现成数百行增删。

## Guidance

1. 将宿主生成目录视为本机运行能力，不与产品提交混合：

   ```text
   .agents/
   .codex/
   .opencode/
   .pi/
   .spec-first/config.local.example.yaml
   .agent/summary/
   ```

2. 团队共享的产品规则保留在可追踪位置：

   ```text
   AGENTS.md
   .agent/rules/
   docs/plans/
   docs/brainstorms/
   docs/solutions/
   deploy/privacy-redaction/
   ```

3. 用 `.gitattributes` 固定治理文档、计划、运维脚本和 durable learning 的 LF，避免
   Windows 工具导致整文件换行漂移。
4. `CLAUDE.md` 已通过 `@AGENTS.md` 引入项目规则时，不再复制同一段治理文本。
5. 收尾时按提交候选分组检查，不执行 broad `git add .`：

   ```powershell
   git status --short
   git diff --check
   git diff --ignore-space-at-eol -- .gitignore AGENTS.md
   git status --short --ignored -- .agents .codex .opencode .pi .spec-first
   ```

## Why This Matters

- 产品补丁的 review 范围更小；
- 本地工具升级不会制造无关的千文件变更；
- 计划和 durable learning 不会被误当成缓存删除；
- 行尾归一化让 diff 表达语义变化而不是编辑器差异；
- 后续提交可以按功能、规划、项目治理分别组织。

## When to Apply

- 安装或更新 AI 编码宿主后；
- 一次工作横跨代码、远端运维、计划和知识沉淀时；
- `git diff --stat` 显示根文件异常大幅增删时；
- 创建 commit 前进行提交候选划分时。

## Examples

清理前，宿主安装目录和旧报告都是 untracked；清理后这些本地资产仍保留在磁盘上，
但 `git status` 只展示产品、计划、治理和 durable learning 等可审查修改。

## Related

- `AGENTS.md`
- `.agent/rules/workflows/add-channel.md`
- `docs/solutions/architecture-patterns/claude-relay-alias-and-provider-acceptance.md`
