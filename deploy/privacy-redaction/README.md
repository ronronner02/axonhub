# 出站隐私脱敏层 运维手册

在请求离开自己的服务器、发往第三方中转站之前，把密钥与个人信息替换成不可逆推的占位符；
上游只看到占位符，回程再还原为原值返回客户端。客户端与渠道凭据零改动。

- 机制：CosyRedactGateway（CRG）以 sidecar 形式加入现有 axonhub compose。
- 接入方式：受保护渠道的 Base URL 前加封套前缀 `http://redact:8787/$`，
  axonhub 原有的字符串拼接会生成 `http://redact:8787/$https://<上游>/v1/messages`。
- 失败语义：**fail-closed**。脱敏层不可用时该渠道尝试失败，绝不明文外发到受保护渠道上游。

> 本手册面向服务器操作。仓库内路径形如 `deploy/privacy-redaction/…`；
> 服务器路径形如 `/srv/apps/axonhub/…`。两者不要混淆。

## 目录

1. [前置](#1-前置)
2. [部署](#2-部署)
3. [阶段 1：回显验证](#3-阶段-1回显验证)
4. [阶段 2：灰度一条渠道](#4-阶段-2灰度一条渠道)
5. [阶段 3：全量切换](#5-阶段-3全量切换)
6. [阶段 4：失败语义验证](#6-阶段-4失败语义验证)
7. [日常 SOP](#7-日常-sop)
8. [脱敏层故障恢复](#8-脱敏层故障恢复)
9. [回滚](#9-回滚)
10. [升级 CRG](#10-升级-crg)

任一阶段失败就停在该阶段，不进入下一阶段，**不得为了让验收通过而把受保护渠道回退为直连**（BR-005）。

---

## 1. 前置

### 1.1 本地先跑通冒烟

在开发机（有 docker 的任意机器）上先证明镜像与配置可用：

```bash
bash deploy/privacy-redaction/scripts/smoke-local.sh
```

必须退出码 0、逐项 PASS。这一步不涉及服务器，也不需要真实上游。

### 1.2 备份并把服务器现状快照入库

服务器上的 `docker-compose.yml`、`nginx.conf` 不在任何 git 仓库中，改动前先留档：

```bash
cd /srv/apps/axonhub
cp docker-compose.yml docker-compose.yml.bak.$(date +%Y%m%d)
cp nginx.conf nginx.conf.bak.$(date +%Y%m%d)
cp .env .env.bak.$(date +%Y%m%d)      # 备份留在服务器，永不入库
```

然后把 `docker-compose.yml` 与 `nginx.conf` **去密**后复制到仓库
`deploy/privacy-redaction/server-baseline/`：删除口令、DSN、真实第三方主机名、
隧道/CDN 标识。`.env` 永不入库。

### 1.3 同步仓库产物到服务器

```bash
# 在服务器上，把仓库的 deploy/privacy-redaction/ 放到 compose 同级目录
/srv/apps/axonhub/privacy-redaction/
```

同步后校验 vendor 完整性：

```bash
cd /srv/apps/axonhub/privacy-redaction/crg
sha256sum -c SHA256SUMS      # 三个文件必须 OK
```

### 1.4 追加环境变量

把 `privacy-redaction/.env.example` 中的键**追加**到现有 `/srv/apps/axonhub/.env`
（不要新建 .env，现有文件里还有 `DB_PASSWORD`、`AXONHUB_IMAGE` 等）：

```bash
# 值写成精确主机名的逗号列表，例如：
#   REDACT_ALLOWED_HOSTS$host_a,$host_b,echo
REDACT_ALLOWED_HOSTS=$YOUR_UPSTREAM_HOSTS,echo
REDACT_MAX_BODY_BYTES=16777216
REDACT_MAX_REDACTIONS=16384
```

**两个必须注意的语义：**

- 留空 = CRG 允许转发到任意主机（开放代理）。overlay 用 `${REDACT_ALLOWED_HOSTS:?}`
  强制非空，所以留空会在 `docker compose config` 阶段直接失败。
- 匹配规则是「精确主机名**或其子域**」。写 `example.com` 会连带放行 `a.example.com`。
  **只写精确主机名。**

阶段 1 需要 `echo` 在列表中；阶段 3 完成后可按需移除。

---

## 2. 部署

### 2.1 复核网络键名（阻塞项）

overlay 默认假设 axonhub 所在网络的 YAML 键名是 `axonhub-network`。先复核：

```bash
cd /srv/apps/axonhub
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml config
```

- 渲染成功且 `redact` 与 `axonhub` 在同一网络 → 继续。
- 报网络定义冲突（服务器 compose 已用同名键非 external 声明该网络）→
  删除 `privacy-redaction/docker-compose.redaction.yml` 末尾的整个 `networks:` 块，
  让 overlay 复用上层声明，再重新 `config`。
- 网络键名不同 → 把 overlay 中 `services.redact.networks`、`services.echo.networks`
  与 `networks:` 块的键名一并改成实际键名。

把复核结论写入 `ACCEPTANCE-RECORD.md` 的「遗留观察」。

### 2.2 启动脱敏层

```bash
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  up -d --build redact

docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  ps redact          # 期望 healthy
```

此时**尚未改任何渠道**，流量行为与之前完全一致。

确认 `redact` 没有发布端口（只应在内部网络可达）：

```bash
docker port axonhub-redact          # 期望无输出
```

在 `OPERATIONS-LOG.md` 追加一行：动作 `deploy`。

---

## 3. 阶段 1：回显验证

目的：用**生产的 redact 实例** + 受控回显上游，证明上游看到的是占位符。
判定依据是 `docker logs` 的回显记录，不是响应体 —— CRG 在回程会还原占位符。

### 3.1 启动回显容器

```bash
cd /srv/apps/axonhub
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  --profile verify up -d --build echo
```

### 3.2 建一条常态禁用的测试渠道

在 axonhub 控制台新建渠道：

| 字段 | 值 |
| --- | --- |
| 名称 | `redact-verify-echo` |
| 类型 | `anthropic` |
| Base URL | `http://redact:8787/$http://echo:8080` |
| 支持模型 | 只填 `redact-verify-echo`（专属模型名，防止被负载均衡选中） |
| 状态 | 先建为 disabled，验证时才启用 |
| 凭据 | 任意占位值（回显上游不校验） |

若 API key `self` 配了模型限制，验证前临时放行 `redact-verify-echo` 并在
`OPERATIONS-LOG.md` 记录，验证后收回。

### 3.3 跑断言

启用该渠道，然后用客户端或 curl 发请求，`model` 指定 `redact-verify-echo`，
正文按 `scripts/smoke-local.sh` 中的形状构造（含伪造 `sk-` 密钥两次、邮箱、手机号、PEM 块）。

读回显日志做判定：

```bash
docker logs axonhub-redact-echo --tail 5
```

需要确认的项（对应 `ACCEPTANCE-RECORD.md`）：

- **AE-01**：四类原值零出现；占位符形如 `{{Redact:<64位小写十六进制>}}`；
  两次伪造密钥映射为**同一个**占位符。
- **AE-08**：日志含 `x-api-key`、`anthropic-version`、`anthropic-beta`；
  不含 `x-forwarded-for`、`x-real-ip`、`cf-connecting-ip`、`cookie`。
- **AE-05**：发一个 `Content-Type: multipart/form-data` 的非空正文 → 脱敏层返回 415，
  回显日志**无新增记录**。客户端侧可能是错误，也可能是故障转移后其它渠道的响应，
  判据只看「回显上游没收到」。
- **AE-06**：把测试渠道 Base URL 临时改为白名单外主机（如
  `http://redact:8787/$http://not-allowed.internal`）→ 403，回显无新增，
  `docker logs axonhub-redact` 无 502。测完改回。
- **AE-10**：发含 12 个 BIP39 单词的助记词 → 回显中**原样出现**。
  这是 BR-002 已声明的边界，不是缺陷。
- **钱包 hex 实测**：发 64 位十六进制串与 `0x` 开头地址，记录是否被替换（只记录）。

全部通过后**禁用测试渠道**，并把 `echo` 停掉：

```bash
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  --profile verify stop echo
```

---

## 4. 阶段 2：灰度一条渠道

选一条同时承载 Claude Code 与 Codex 流量的第三方渠道。

### 4.1 先看计划

```bash
cd /srv/apps/axonhub/privacy-redaction
export AXONHUB_ADMIN_URL=http://127.0.0.1:8090
export AXONHUB_EMAIL=<管理员邮箱>
export AXONHUB_PASSWORD=<口令>

bash scripts/redact-channels.sh --only <渠道id>
```

dry-run 会打印 `旧值 -> 新值`。若出现 `error-empty-url` 或 `error-websocket`，
先在控制台修正该渠道再重跑（脚本会以非零退出码阻塞）。

确认该渠道的上游主机已在 `.env` 的 `REDACT_ALLOWED_HOSTS` 中，否则请求会得到 403。

### 4.2 执行

```bash
bash scripts/redact-channels.sh --only <渠道id> --apply
```

立刻在 `OPERATIONS-LOG.md` 追加一行：动作 `rewrite`，对象为该渠道 id。

### 4.3 验收

- **AE-02**：非流式请求「原样复述下面这个密钥」+ 伪造 `sk-` 密钥 → 客户端收到**原值**。
- **AE-03**：流式请求让模型复述一个长占位符 → 客户端收到完整原值；
  首块在上游首块到达后即发出，不等整个响应。
- **AE-09**：不改任何客户端配置，用 Claude Code 与 Codex 各跑一次含工具调用的流式会话
  → 正常完成，工具参数与结果往返正确。
- **时延**：同渠道同提示词，启用前后各 3 次，记录首块与总时长填入验收记录。
- 顺带观察：注入的英文 notice 是否影响回答质量；高熵误报是否替换掉代码里的哈希/ID。

---

## 5. 阶段 3：全量切换

### 5.1 先给官方渠道打豁免 tag

在控制台给官方 `api.openai.com` 渠道（id 26）加 tag `redact-exempt`。
**顺序很重要**：先打 tag，改写脚本才会跳过它。

### 5.2 补全允许主机

把其余 25 条渠道的上游主机全部加入 `.env` 的 `REDACT_ALLOWED_HOSTS`，然后重建：

```bash
cd /srv/apps/axonhub
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  up -d --force-recreate redact
```

### 5.3 改写

```bash
cd privacy-redaction
bash scripts/redact-channels.sh              # 先看全量计划
bash scripts/redact-channels.sh --apply      # 执行
```

脚本会跳过 `skip-exempt`（豁免）与 `skip-protected`（已加前缀），幂等可重跑。
在 `OPERATIONS-LOG.md` 记录。

### 5.4 核对信任边界（AE-07）

```bash
bash scripts/check-trust-boundary.sh
```

期望：豁免清单**只含 id 26**，违规清单为空，允许主机集合「一致」，退出码 0。
任一第三方渠道出现在豁免清单或违规清单即判为不通过。

---

## 6. 阶段 4：失败语义验证

### 6.1 先记录系统重试设置

控制台 → 系统设置 → 重试策略，把以下值填入 `ACCEPTANCE-RECORD.md`：
最大渠道切换次数、自动禁用是否启用、触发自动禁用的状态码与次数。
**本期不改这些设置，只记录。**

### 6.2 AE-04

选一个「至少两条受保护渠道 + 豁免渠道」都支持的模型，然后停掉脱敏层：

```bash
docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
  stop redact
```

发请求并记录：

- 受保护渠道尝试失败并按现有策略转移；
- 候选序列**可能包含豁免渠道**，此时明文会发往官方直连上游 ——
  这是 BR-001 允许的结果，不是缺陷；
- **判定标准：任何第三方上游未收到明文。** 客户端可能收到错误，也可能收到豁免渠道的正常响应；
- 记录 axonhub 实际记录的状态码、被自动禁用的渠道 id。

然后恢复（见下一节）。

### 6.3 AE-11

让模型故意改写占位符（例如要求它复述时截断）→ 客户端收到改写后的字符串原样，
不重试、不报错（BR-004）。

### 6.4 大文件工具结果

发一个 ≥1 MiB 的工具结果，确认往返成功（未触及 16 MiB 上限）。

---

## 7. 日常 SOP

### 新增渠道（顺序不能颠倒）

1. 先把该渠道的上游主机加入 `.env` 的 `REDACT_ALLOWED_HOSTS`，重建 `redact`；
2. 在控制台创建渠道（此时可先保持 disabled）；
3. 跑 `bash scripts/redact-channels.sh --only <新id>` 看计划，确认无误后 `--apply`；
4. 启用渠道；
5. 跑 `bash scripts/check-trust-boundary.sh` 确认无违规、无漂移；
6. 在 `OPERATIONS-LOG.md` 记录。

**若要新增的是可信官方直连渠道**：跳过 1、3，改为在控制台给它打 `redact-exempt` tag，
然后跑核对脚本确认它出现在豁免清单中。

### 定期核对

```bash
bash /srv/apps/axonhub/privacy-redaction/scripts/check-trust-boundary.sh
```

退出码非 0 就要处理。可接入 cron，但通知渠道尚未决定（见计划的 Deferred）。

---

## 8. 脱敏层故障恢复

脱敏层是受保护渠道的单点。它宕机时受保护渠道尝试失败，
若命中全局自动禁用策略的状态码与次数，这些渠道会被**自动禁用**并设置 `auto_disabled_at`。
beta7 **没有**渠道级自动重新启用。

恢复顺序：

1. 确认脱敏层已恢复：
   ```bash
   docker compose -f docker-compose.yml -f privacy-redaction/docker-compose.redaction.yml \
     up -d redact
   docker compose ... ps redact        # 期望 healthy
   ```
2. 在控制台把被自动禁用的渠道批量「恢复」，或逐条启用；
3. 跑 `bash scripts/check-trust-boundary.sh` 确认边界完好；
4. 在 `OPERATIONS-LOG.md` 记录：动作 `recover-disabled`。

**禁止**以关闭自动禁用策略或把渠道回退为直连来规避这个问题。

---

## 9. 回滚

回滚会让渠道恢复明文直连，**隐私防护随之消失**。必须是显式操作并留记录（BR-005）。

```bash
cd /srv/apps/axonhub/privacy-redaction
bash scripts/redact-channels.sh --restore <id,...>          # 先看计划（无 --yes 不执行）
bash scripts/redact-channels.sh --restore <id,...> --yes --apply
```

然后：

1. 在 `OPERATIONS-LOG.md` 追加一行：动作 `rollback`，写明**回滚原因**；
2. 跑核对脚本 —— 被回滚的渠道会进入违规清单（除非同时打了 `redact-exempt` tag），
   这是预期的，说明该渠道确实不再受保护；
3. 客户端无感知，但这些渠道的出站已恢复明文。

---

## 10. 升级 CRG

1. 在仓库 `deploy/privacy-redaction/crg/` 替换 `worker.js`、`node-server.mjs`、`LICENSE`；
2. 更新 `crg/UPSTREAM.md` 的提交号、日期、哈希，并重新生成 `SHA256SUMS`：
   ```bash
   cd deploy/privacy-redaction/crg && sha256sum worker.js node-server.mjs LICENSE > SHA256SUMS
   ```
3. 重新核对 `UPSTREAM.md` 中「可配置项」与「状态码」两张表是否仍准确；
4. 本地重跑 `bash scripts/smoke-local.sh`；
5. 同步到服务器，`sha256sum -c SHA256SUMS`，重建 `redact`；
6. **重跑阶段 1～2**，结果写入新的 `ACCEPTANCE-RECORD.md`；
7. 在 `OPERATIONS-LOG.md` 记录：动作 `upgrade`。

---

## 已知边界

- 助记词（12/24 个自然单词）不在检测范围（BR-002）。hex 私钥与 `0x` 地址预期由高熵检测器
  命中，阶段 1 实测记录。
- 注入给模型的英文提示文案是 vendor 代码里的常量，**不是环境变量**，无法配置。
  详见 `crg/UPSTREAM.md`。
- 脱敏层重启会更换运行时盐，占位符随之改变，上游提示缓存前缀失效。
- 本机 Postgres 中的请求/响应正文仍是明文存档，本期不动。
- 图片、音频等二进制字段不检查；非 JSON 端点一律拒绝（415）。
- 故障转移候选可能包含豁免渠道，此时明文会发往官方直连上游（BR-001 允许）。
