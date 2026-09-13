# CRG Vendor 来源与完整性

本目录的 `worker.js`、`node-server.mjs`、`LICENSE` 是从上游仓库**原样复制**的第三方代码，不得修改。
需要调整行为时只能通过环境变量（见下方「可配置项」）。

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
`node:http`、`node:stream` 与 `worker.js`。因此镜像不需要 `npm install`，也不需要 vendor
`package.json`。

## 可配置项（经源码核对，`worker.js` 实际读取的环境变量只有四个）

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `REDACT_ALLOWED_HOSTS` | 空 = **允许一切** | 逗号分隔主机名。匹配精确主机或其子域（`a.example.com` 会被 `example.com` 匹配到）。**必须显式设置**，否则是开放代理。 |
| `REDACT_CORS_ORIGIN` | `*` | 响应的 `access-control-allow-origin` 取值。 |
| `REDACT_MAX_BODY_BYTES` | `16777216`（16 MiB） | 超出返回 413，不转发。 |
| `REDACT_MAX_REDACTIONS` | `16384` | 单请求唯一敏感值上限，超出返回 413。 |

`node-server.mjs` 另读 `HOST`（默认 `127.0.0.1`）与 `PORT`（默认 `8787`）。
**容器内必须设 `HOST=0.0.0.0`**，否则同网络的其它容器无法连接。

### 已知限制：注入文案不可配置

`REDACT_NOTICE` 是 `worker.js` 第 15 行的导出常量，**不是环境变量**。要改注入给模型的英文提示
文案，只能修改 vendor 文件，这与本目录的「不改 vendor」约束冲突。若该文案对模型行为造成实际
问题，须作为升级/分叉决策记录后处理，不能当作可随手调整的旋钮。

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
