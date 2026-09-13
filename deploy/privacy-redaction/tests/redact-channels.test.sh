#!/usr/bin/env bash
# redact-channels.sh 计划层测试（离线，不连网、不需要 docker）
#
# 用法: bash deploy/privacy-redaction/tests/redact-channels.test.sh

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "${TEST_DIR}/.." && pwd)"
SCRIPT="${BASE_DIR}/scripts/redact-channels.sh"
FIX="${TEST_DIR}/fixtures"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL+1)); }
info() { printf '        %s\n' "$1"; }

OUT=""
# run <期望退出码> <描述> <参数...>   —— 计划走 stderr，这里合并捕获
run() {
  local want="$1" desc="$2"; shift 2
  OUT="$(bash "$SCRIPT" "$@" 2>&1)"; local got=$?
  if [[ "$got" == "$want" ]]; then ok "$desc (exit=$got)"; else
    bad "$desc — 期望 exit=$want，实得 $got"; info "$(printf '%s' "$OUT" | tail -6)"; fi
}
has()   { if printf '%s' "$OUT" | grep -qF -- "$1"; then ok "  ↳ 含: $1"; else bad "  ↳ 缺: $1"; fi; }
hasnt() { if printf '%s' "$OUT" | grep -qF -- "$1"; then bad "  ↳ 不应含: $1"; else ok "  ↳ 正确地不含: $1"; fi; }

fx() { printf '%s\n' "$2" > "${TMP}/$1"; echo "${TMP}/$1"; }

printf '\n\033[1m1. 基本改写\033[0m\n'
F="$(fx one.json '[{"id":10,"name":"a","status":"enabled","baseURL":"https://third.example/v1","tags":[],"endpoints":[]}]')"
run 0 "直连第三方 -> rewrite，新值 = 前缀 + \$ + 旧值" --plan-from-file "$F"
has 'https://third.example/v1  ->  http://redact:8787/$https://third.example/v1'
has "待写 1 条"

printf '\n\033[1m2. 后缀语义保留\033[0m\n'
F="$(fx hash.json '[{"id":11,"name":"h","status":"enabled","baseURL":"https://third.example/custom#","tags":[],"endpoints":[]}]')"
run 0 "旧值末尾 # 原样保留在新值中" --plan-from-file "$F"
has 'http://redact:8787/$https://third.example/custom#'

printf '\n\033[1m3. 幂等\033[0m\n'
F="$(fx idem.json '[{"id":12,"name":"p","status":"enabled","baseURL":"http://redact:8787/$https://third.example","tags":[],"endpoints":[]}]')"
run 0 "已加前缀 -> skip-protected" --plan-from-file "$F"
has "skip-protected"
has "待写 0 条"

printf '\n\033[1m4. 豁免优先于 --only\033[0m\n'
F="$(fx ex.json '[{"id":26,"name":"openai","status":"enabled","baseURL":"https://api.openai.com/v1","tags":["redact-exempt"],"endpoints":[]}]')"
run 0 "带 redact-exempt -> skip-exempt" --plan-from-file "$F"
has "skip-exempt"
run 0 "即使 --only 指定该 id 仍 skip-exempt" --plan-from-file "$F" --only 26
has "skip-exempt"
hasnt "rewrite (1)"

printf '\n\033[1m5. endpoints 处理\033[0m\n'
F="$(fx eps.json '[{"id":13,"name":"e","status":"enabled","baseURL":"https://third.example","tags":[],"endpoints":[{"apiFormat":"anthropic","baseURL":"https://ep-one.example"},{"apiFormat":"openai","baseURL":""}]}]')"
run 0 "非空 endpoint.baseURL 一并改写，空的不动" --plan-from-file "$F"
has 'endpoint: https://ep-one.example -> http://redact:8787/$https://ep-one.example'

printf '\n\033[1m6. 阻塞错误\033[0m\n'
F="$(fx empty.json '[{"id":14,"name":"n","status":"enabled","baseURL":"","tags":[],"endpoints":[]}]')"
run 1 "空 baseURL -> error-empty-url，退出码 1" --plan-from-file "$F"
has "error-empty-url"
hasnt "待写 1 条"

F="$(fx ws.json '[{"id":15,"name":"w","status":"enabled","baseURL":"wss://rt.example/v1","tags":[],"endpoints":[]}]')"
run 1 "wss:// -> error-websocket" --plan-from-file "$F"
has "error-websocket"

printf '\n\033[1m7. 归档\033[0m\n'
F="$(fx arch.json '[{"id":16,"name":"old","status":"archived","baseURL":"https://legacy.example","tags":[],"endpoints":[]}]')"
run 0 "已归档 -> skip-archived" --plan-from-file "$F"
has "skip-archived"

printf '\n\033[1m8. 回滚\033[0m\n'
F="$(fx res.json '[{"id":12,"name":"p","status":"enabled","baseURL":"http://redact:8787/$https://third.example/v1","tags":[],"endpoints":[]}]')"
run 1 "--restore 无 --yes -> 只打印计划并非零退出" --plan-from-file "$F" --restore 12
has "回滚计划"
has 'http://redact:8787/$https://third.example/v1  ->  https://third.example/v1'
has "请加 --yes"
has "BR-005"

run 0 "--restore 带 --yes（离线）-> 计划通过门禁" --plan-from-file "$F" --restore 12 --yes
has "dry-run 结束，未发出任何写操作"

F="$(fx nopfx.json '[{"id":17,"name":"d","status":"enabled","baseURL":"https://third.example/v1","tags":[],"endpoints":[]}]')"
run 1 "--restore 目标未加前缀 -> 报错不改" --plan-from-file "$F" --restore 17 --yes
has "无法回滚"

printf '\n\033[1m9. 写操作门禁\033[0m\n'
run 0 "无 --apply 时明确声明未发出写操作" --plan-from-file "${FIX}/channels-compliant.json"
has "未发出任何写操作"

OUT="$(bash "$SCRIPT" --plan-from-file "${FIX}/channels-compliant.json" --apply 2>&1)"; rc=$?
if [[ "$rc" == 2 ]]; then ok "--plan-from-file 与 --apply 同用 -> 拒绝 (exit=2)"; else
  bad "--plan-from-file + --apply 应 exit=2，实得 $rc"; fi
has "不能同用"

# 即使 ADMIN_URL 指向不可达地址，--plan-from-file 仍能完成（证明计划层零网络依赖）
OUT="$(AXONHUB_ADMIN_URL=http://127.0.0.1:1 bash "$SCRIPT" --plan-from-file "${FIX}/channels-compliant.json" 2>&1)"; rc=$?
if [[ "$rc" == 0 ]]; then ok "ADMIN_URL 不可达时 --plan-from-file 仍成功（零网络依赖）"; else
  bad "计划层不应依赖网络，实得 exit=$rc"; fi

printf '\n\033[1m10. 数据形状容错与 --help\033[0m\n'
F="$(fx snake.json '[{"id":18,"name":"s","status":"enabled","base_url":"https://third.example","tags":"[]","endpoints":"[]"}]')"
run 0 "snake_case 字段与字符串化 tags/endpoints（psql 形状）-> 正确解析" --plan-from-file "$F"
has "rewrite (1)"

OUT="$(bash "$SCRIPT" --help 2>&1)"; rc=$?
if [[ "$rc" == 0 ]]; then ok "--help exit=0"; else bad "--help 应 exit=0，实得 $rc"; fi
has "skip-exempt"
has "退出码: 0 成功"

printf '\n\033[1m汇总\033[0m\n  PASS %d    FAIL %d\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || { printf '\n\033[31m测试未通过\033[0m\n'; exit 1; }
printf '\n\033[32m全部通过\033[0m\n'
