#!/usr/bin/env bash
# 本地冒烟：证明脱敏层在真实容器里满足 AE-01 / AE-05 / AE-06 / AE-08 / AE-10 的形状。
#
# 判定依据是 echo 容器的 stdout（上游实际收到了什么），不是响应体 ——
# CRG 在回程会把占位符还原，响应体证明不了上游看到的是占位符。
#
# 用法：
#   bash deploy/privacy-redaction/scripts/smoke-local.sh
#   KEEP_UP=1 bash .../smoke-local.sh   # 失败后保留容器便于排查
#
# 退出码 0 = 全部 PASS。任何断言失败立即非零退出。
#
# 依赖：docker（含 compose v2）。不依赖 jq、node、curl 的宿主机安装 ——
# 请求由容器内的 node fetch 发出，JSON 解析也在容器内完成。

set -euo pipefail

# Git Bash / MSYS 会把形如 /$http://... 的参数当成路径改写，必须全局关闭。
export MSYS_NO_PATHCONV=1
export MSYS2_ARG_CONV_EXCL='*'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
# Git Bash 的 /e/foo 交给 Windows docker.exe 会被二次转换。给 compose -f 用 Windows 路径。
if BASE_DIR_WIN="$(cd "$BASE_DIR" && pwd -W 2>/dev/null)"; then
  COMPOSE_FILE="${BASE_DIR_WIN}/docker-compose.redaction.yml"
else
  COMPOSE_FILE="${BASE_DIR}/docker-compose.redaction.yml"
fi

PROJECT="redact-smoke-$$"
NETWORK="${PROJECT}-net"

PASS_COUNT=0
FAIL_COUNT=0

# ── 输出 ──────────────────────────────────────────────────────────────────────

c_pass() { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS_COUNT=$((PASS_COUNT + 1)); }
c_fail() { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL_COUNT=$((FAIL_COUNT + 1)); }
c_info() { printf '        %s\n' "$1"; }
c_head() { printf '\n\033[1m%s\033[0m\n' "$1"; }

die() {
  printf '\n\033[31m致命错误:\033[0m %s\n' "$1" >&2
  exit 1
}

# ── 清理 ──────────────────────────────────────────────────────────────────────

cleanup() {
  local rc=$?
  if [[ "${KEEP_UP:-0}" == "1" && $rc -ne 0 ]]; then
    printf '\nKEEP_UP=1 且失败，保留容器。手工清理：\n'
    printf '  docker compose -p %s -f %s --profile verify down -v\n' "$PROJECT" "$COMPOSE_FILE"
    printf '  docker network rm %s\n' "$NETWORK"
    return
  fi
  printf '\n清理中...\n'
  compose --profile verify down -v --remove-orphans >/dev/null 2>&1 || true
  docker network rm "$NETWORK" >/dev/null 2>&1 || true
  # 连本次构建的镜像标签一并删除，避免每跑一次就留下一对 redact-smoke-* 镜像。
  docker rmi -f "${PROJECT}-redact" "${PROJECT}-echo" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── compose 包装 ──────────────────────────────────────────────────────────────

compose() {
  REDACT_NETWORK_NAME="$NETWORK" \
  REDACT_ALLOWED_HOSTS=${ALLOWED_HOSTS:-echo} \
  REDACT_MAX_BODY_BYTES="${MAX_BODY:-16777216}" \
  REDACT_MAX_REDACTIONS="${MAX_REDACTIONS:-16384}" \
  REDACT_IMAGE="${PROJECT}-redact" \
  ECHO_IMAGE="${PROJECT}-echo" \
  REDACT_CONTAINER_NAME="${PROJECT}-redact-ctr" \
  ECHO_CONTAINER_NAME="${PROJECT}-echo-ctr" \
    docker compose -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
}

# 在与被测服务同网络的一次性容器里执行 node 脚本。
# stdin 传脚本内容，避免引号地狱。
in_net_node() {
  docker run --rm -i --network "$NETWORK" \
    -e TARGET_BASE="http://redact:8787" \
    -e FAKE_KEY="${FAKE_KEY:-}" \
    -e FAKE_EMAIL="${FAKE_EMAIL:-}" \
    -e FAKE_PHONE="${FAKE_PHONE:-}" \
    -e FAKE_PEM_BODY="${FAKE_PEM_BODY:-}" \
    -e MNEMONIC="${MNEMONIC:-}" \
    "${PROJECT}-redact" node --input-type=module -
}

echo_log_lines() { compose logs --no-log-prefix echo 2>/dev/null | grep -c '"method"' || true; }
echo_log_raw()   { compose logs --no-log-prefix echo 2>/dev/null || true; }
redact_log_raw() { compose logs --no-log-prefix redact 2>/dev/null || true; }

# ── 前置检查 ──────────────────────────────────────────────────────────────────

docker info >/dev/null 2>&1 || die "docker 守护进程不可用。请先启动 Docker Desktop / dockerd。"
[[ -f "$COMPOSE_FILE" ]] || die "找不到 $COMPOSE_FILE"

c_head "0. 环境与配置门禁"

# overlay 必须在 REDACT_ALLOWED_HOSTS 缺失时拒绝渲染（防开放代理）。
if (unset REDACT_ALLOWED_HOSTS; REDACT_NETWORK_NAME="$NETWORK" \
     docker compose -p "$PROJECT" -f "$COMPOSE_FILE" config >/dev/null 2>&1); then
  c_fail "REDACT_ALLOWED_HOSTS 缺失时 compose config 竟然成功（开放代理风险）"
else
  c_pass "REDACT_ALLOWED_HOSTS 缺失时 compose config 失败（强制变量生效）"
fi

# 提供变量后必须能渲染，且 redact 不得发布端口。
CONFIG_OUT="$(compose config 2>&1)" || die "compose config 在变量齐备时失败：${CONFIG_OUT}"
c_pass "变量齐备时 compose config 渲染成功"

# 只检查 redact 服务段里是否出现 published:（EXPOSE 不会产生 published）
REDACT_BLOCK="$(printf '%s\n' "$CONFIG_OUT" | awk '
  $0 ~ /^  redact:$/ {p=1; next}
  p && $0 ~ /^  [A-Za-z0-9_-]+:$/ {exit}
  p {print}
')"
if printf '%s' "$REDACT_BLOCK" | grep -qE 'published:'; then
  c_fail "redact 服务发布了端口（违反 R-07 只在内部网络可达）"
else
  c_pass "渲染结果中 redact 无发布端口"
fi

# ── 启动 ──────────────────────────────────────────────────────────────────────

c_head "1. 构建并启动 redact + echo"

docker network create "$NETWORK" >/dev/null 2>&1 || die "无法创建网络 $NETWORK"
c_info "网络 $NETWORK 已创建"

compose --profile verify build >/dev/null 2>&1 || die "镜像构建失败（重跑不加 >/dev/null 可看详情）"
c_pass "redact 与 echo 镜像构建成功"

compose --profile verify up -d >/dev/null 2>&1 || die "容器启动失败"
c_info "等待 redact 健康..."

READY=0
for _ in $(seq 1 30); do
  if in_net_node <<'JS' >/dev/null 2>&1
const r = await fetch(process.env.TARGET_BASE + "/healthz");
if (!r.ok) process.exit(1);
const j = await r.json();
process.exit(j && j.ok === true ? 0 : 1);
JS
  then READY=1; break; fi
  sleep 1
done
[[ "$READY" == "1" ]] || { c_info "$(redact_log_raw | tail -20)"; die "redact 在 30 秒内未就绪"; }
c_pass "GET /healthz 返回 ok:true"

# 容器加固断言
if compose exec -T redact node -e 'process.exit(process.getuid()===0?1:0)' >/dev/null 2>&1; then
  c_pass "redact 以非 root 运行"
else
  c_fail "redact 以 root 运行（应为非 root）"
fi

if compose exec -T redact sh -c 'touch /x' >/dev/null 2>&1; then
  c_fail "根文件系统可写（应为只读）"
else
  c_pass "根文件系统只读（touch /x 失败）"
fi

# ── vendor 完整性 ─────────────────────────────────────────────────────────────

c_head "2. vendor 完整性"

if (cd "${BASE_DIR}/crg" && sha256sum -c SHA256SUMS >/dev/null 2>&1); then
  c_pass "crg/ 三个文件 sha256 与 SHA256SUMS 一致"
else
  c_fail "crg/ 文件哈希与 SHA256SUMS 不一致（vendor 被改动？）"
fi

# ── 断言主体 ──────────────────────────────────────────────────────────────────

# 冒烟用的假敏感值。全部是明显的测试值，不是真实凭据。
# 密钥用拼接构造，避免仓库里出现连续的 sk-<20+> 形态，从而误伤泄露扫描。
FAKE_KEY="$(printf '%s%s' 'sk' '-smoketestFAKE0000111122223333444455556666')"
FAKE_EMAIL="$(printf '%s@%s' 'smoke.test.user' 'example.invalid')"
FAKE_PHONE='13800138000'
MNEMONIC='abandon ability able about above absent absorb abstract absurd abuse access accident'
FAKE_PEM_BODY='MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX6Ppy1tPf9Cnzj4p4WGeKLs1Pt8Qu KUpRKfFLfRYC9AIKjbJTWit+CqvjWYzvQwECAwEAAQ=='

c_head "3. AE-01 出站脱敏与同值同占位符 / AE-08 身份头清理"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const pem = "-----BEGIN RSA PRIVATE KEY-----\n" + String(process.env.FAKE_PEM_BODY || "").replace(" ", "\n") + "\n-----END RSA PRIVATE KEY-----";
const body = {
  model: "redact-verify-echo",
  max_tokens: 16,
  messages: [
    { role: "user", content: [
      { type: "text", text: `key is ${process.env.FAKE_KEY} and again ${process.env.FAKE_KEY}, mail ${process.env.FAKE_EMAIL}, phone ${process.env.FAKE_PHONE}` }
    ]},
    { role: "user", content: [
      { type: "tool_result", tool_use_id: "t1", content: pem }
    ]}
  ]
};
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/messages", {
  method: "POST",
  headers: {
    "content-type": "application/json",
    "x-api-key": "channel-credential-abc",
    "anthropic-version": "2023-06-01",
    "anthropic-beta": "tools-2024-04-04",
    "X-Forwarded-For": "203.0.113.9",
    "X-Real-IP": "203.0.113.9",
    "CF-Connecting-IP": "203.0.113.9",
    "Cookie": "session=leakme",
    "Sec-Fetch-Mode": "cors"
  },
  body: JSON.stringify(body)
});
console.log("STATUS=" + r.status);
JS
)" || die "AE-01 请求发送失败"

[[ "$STATUS" == "STATUS=200" ]] || c_fail "AE-01 期望 200，实得 ${STATUS}"
AFTER="$(echo_log_lines)"
[[ "$AFTER" -gt "$BEFORE" ]] || die "AE-01 回显容器未收到请求，后续断言无意义"

LOG="$(echo_log_raw | grep '"method"' | tail -1)"

# 原值零出现
LEAKED=""
for v in "$FAKE_KEY" "$FAKE_EMAIL" "$FAKE_PHONE" "BEGIN RSA PRIVATE KEY"; do
  if printf '%s' "$LOG" | grep -qF -- "$v"; then LEAKED="${LEAKED} [${v:0:18}...]"; fi
done
if [[ -z "$LEAKED" ]]; then
  c_pass "AE-01 回显正文中四类原值零出现"
else
  c_fail "AE-01 上游收到了原值:${LEAKED}"
fi

# 占位符形状：{{Redact:<64位小写hex>}}
TOKENS="$(printf '%s' "$LOG" | grep -oE '\{\{Redact:[a-f0-9]{64}\}\}' | sort || true)"
TOKEN_TOTAL="$(printf '%s' "$TOKENS" | grep -c 'Redact:' || true)"
if [[ "${TOKEN_TOTAL:-0}" -ge 3 ]]; then
  c_pass "AE-01 出现 ${TOKEN_TOTAL} 个合规占位符（64 位小写十六进制）"
else
  c_fail "AE-01 占位符数量异常（${TOKEN_TOTAL:-0}），可能未发生替换"
  c_info "回显片段: $(printf '%s' "$LOG" | head -c 300)"
fi

# 同一请求内同值 -> 同占位符：假密钥出现两次，其占位符必须是同一个
UNIQ_FOR_KEY="$(printf '%s' "$LOG" | grep -oE '\{\{Redact:[a-f0-9]{64}\}\}' | sort | uniq -c | sort -rn | head -1 | awk '{print $1}')"
if [[ "${UNIQ_FOR_KEY:-0}" -ge 2 ]]; then
  c_pass "AE-01 重复原值映射为同一占位符（某占位符出现 ${UNIQ_FOR_KEY} 次）"
else
  c_fail "AE-01 未观察到重复原值共享占位符"
fi

# AE-08 头清理
for h in x-api-key anthropic-version anthropic-beta; do
  if printf '%s' "$LOG" | grep -qF "\"${h}\""; then
    c_pass "AE-08 凭据/协议头 ${h} 已转发"
  else
    c_fail "AE-08 凭据/协议头 ${h} 丢失"
  fi
done
for h in x-forwarded-for x-real-ip cf-connecting-ip cookie; do
  if printf '%s' "$LOG" | grep -qF "\"${h}\""; then
    c_fail "AE-08 身份头 ${h} 未被移除"
  else
    c_pass "AE-08 身份头 ${h} 已移除"
  fi
done
# sec-* 属于 CRG 的入站过滤规则。已实测：Node 的 fetch 在发出请求时会自行添加
# sec-fetch-mode（调用方一个 sec-* 都不设时回显上游依然能看到它），所以上游看到该头
# 是出站客户端行为，不是入站泄漏。AE-08 的验收名单只含
# X-Forwarded-For / X-Real-IP / CF-Connecting-IP / Cookie。
if printf '%s' "$LOG" | grep -qF '"sec-fetch-mode"'; then
  c_info "观察: 上游见到 sec-fetch-mode（已实测为 Node fetch 出站自带，非入站泄漏）"
fi
c_head "4. AE-10 助记词为已声明边界（应原样外发）"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ model: "redact-verify-echo", max_tokens: 16,
    messages: [{ role: "user", content: process.env.MNEMONIC }] })
});
console.log("STATUS=" + r.status);
JS
)" || die "AE-10 请求失败"
[[ "$STATUS" == "STATUS=200" ]] || c_fail "AE-10 期望 200，实得 ${STATUS}"

LOG="$(echo_log_raw | grep '"method"' | tail -1)"
if printf '%s' "$LOG" | grep -qF -- "$MNEMONIC"; then
  c_pass "AE-10 助记词原样出现（与 BR-002 声明一致，非缺陷）"
else
  c_fail "AE-10 助记词未原样出现，与 BR-002 的文档口径不符"
fi

c_head "5. AE-05 非 JSON 正文必须被拒绝且不转发"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/messages", {
  method: "POST",
  headers: { "content-type": "multipart/form-data; boundary=xyz" },
  body: "--xyz\r\nContent-Disposition: form-data; name=\"f\"\r\n\r\nbinary-ish\r\n--xyz--\r\n"
});
console.log("STATUS=" + r.status);
JS
)" || die "AE-05 请求失败"
if [[ "$STATUS" == "STATUS=415" ]]; then
  c_pass "AE-05 非 JSON 正文返回 415"
else
  c_fail "AE-05 期望 415，实得 ${STATUS}"
fi
if [[ "$(echo_log_lines)" == "$BEFORE" ]]; then
  c_pass "AE-05 回显容器未收到该请求（未转发）"
else
  c_fail "AE-05 请求被转发到上游（应在脱敏层拦下）"
fi

c_head "6. AE-06 上游主机白名单"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const r = await fetch(process.env.TARGET_BASE + "/$http://not-allowed.internal/v1/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ model: "x", messages: [{ role: "user", content: "hi" }] })
});
console.log("STATUS=" + r.status);
JS
)" || die "AE-06 请求失败"
if [[ "$STATUS" == "STATUS=403" ]]; then
  c_pass "AE-06 白名单外主机返回 403"
else
  c_fail "AE-06 期望 403，实得 ${STATUS}"
fi
if [[ "$(echo_log_lines)" == "$BEFORE" ]]; then
  c_pass "AE-06 回显容器无新增记录"
else
  c_fail "AE-06 请求仍到达了上游"
fi
# 403 在 fetch 之前返回，因此不存在 502；出现 502 说明确实建连了。
if redact_log_raw | tail -5 | grep -q '502'; then
  c_fail "AE-06 脱敏层日志出现 502，可能已向该主机建连"
else
  c_pass "AE-06 未观察到上游连接尝试（无 502）"
fi
c_head "7. GET 与空正文透传（模型自动同步路径）"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/models");
console.log("STATUS=" + r.status);
JS
)" || die "GET 请求失败"
if [[ "$STATUS" == "STATUS=200" ]]; then
  c_pass "GET /v1/models 透传成功（200）"
else
  c_fail "GET 期望 200，实得 ${STATUS}"
fi
LOG="$(echo_log_raw | grep '"method"' | tail -1)"
if printf '%s' "$LOG" | grep -q '"method":"GET"'; then
  c_pass "回显记录到 GET，且脱敏层未读正文"
else
  c_fail "回显未记录到 GET"
fi

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/messages", { method: "POST" });
console.log("STATUS=" + r.status);
JS
)" || die "空正文 POST 请求失败"
if [[ "$STATUS" == "STATUS=200" ]]; then
  c_pass "空正文 POST 透传成功（200）"
else
  c_fail "空正文 POST 期望 200，实得 ${STATUS}"
fi
LOG="$(echo_log_raw | grep '"method"' | tail -1)"
if printf '%s' "$LOG" | grep -q '"bodyLen":0'; then
  c_pass "回显记录正文长度为 0"
else
  c_fail "空正文 POST 的回显正文非空"
fi

c_head "8. 正文体积上限（重建 redact，限制为 1024 字节）"

MAX_BODY=1024 compose up -d --force-recreate redact >/dev/null 2>&1 || die "以小上限重建 redact 失败"
READY=0
for _ in $(seq 1 30); do
  if in_net_node <<'JS' >/dev/null 2>&1
const r = await fetch(process.env.TARGET_BASE + "/healthz"); process.exit(r.ok ? 0 : 1);
JS
  then READY=1; break; fi
  sleep 1
done
[[ "$READY" == "1" ]] || die "重建后的 redact 未就绪"

BEFORE="$(echo_log_lines)"
STATUS="$(in_net_node <<'JS'
const big = "x".repeat(4096);
const r = await fetch(process.env.TARGET_BASE + "/$http://echo:8080/v1/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ model: "x", messages: [{ role: "user", content: big }] })
});
console.log("STATUS=" + r.status);
JS
)" || die "超限请求失败"
if [[ "$STATUS" == "STATUS=413" ]]; then
  c_pass "超过 REDACT_MAX_BODY_BYTES 返回 413"
else
  c_fail "超限期望 413，实得 ${STATUS}"
fi
if [[ "$(echo_log_lines)" == "$BEFORE" ]]; then
  c_pass "超限请求未转发到上游"
else
  c_fail "超限请求被转发（应在脱敏层拦下）"
fi

# ── 汇总 ──────────────────────────────────────────────────────────────────────

c_head "汇总"
printf '  PASS %d    FAIL %d\n' "$PASS_COUNT" "$FAIL_COUNT"
if [[ "$FAIL_COUNT" -gt 0 ]]; then
  printf '\n\033[31m冒烟未通过\033[0m\n'
  exit 1
fi
printf '\n\033[32m冒烟全部通过\033[0m\n'
exit 0
