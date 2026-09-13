#!/usr/bin/env bash
# check-trust-boundary.sh 的判定层测试（fixture 驱动，不需要 docker / postgres）
#
# 用法: bash deploy/privacy-redaction/tests/check-trust-boundary.test.sh

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BASE_DIR="$(cd "${TEST_DIR}/.." && pwd)"
SCRIPT="${BASE_DIR}/scripts/check-trust-boundary.sh"
FIX="${TEST_DIR}/fixtures"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0
ok()   { printf '  \033[32mPASS\033[0m  %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m  %s\n' "$1"; FAIL=$((FAIL+1)); }
info() { printf '        %s\n' "$1"; }

# run <期望退出码> <描述> <参数...>
OUT=""
run() {
  local want="$1"; local desc="$2"; shift 2
  OUT="$(bash "$SCRIPT" "$@" 2>&1)"; local got=$?
  if [[ "$got" == "$want" ]]; then ok "$desc (exit=$got)"; else
    bad "$desc — 期望 exit=$want，实得 $got"; info "$(printf '%s' "$OUT" | tail -8)"
  fi
}
# 断言输出包含 / 不包含
has()  { if printf '%s' "$OUT" | grep -qF -- "$1"; then ok "  ↳ 输出含: $1"; else bad "  ↳ 输出缺: $1"; fi; }
hasnt(){ if printf '%s' "$OUT" | grep -qF -- "$1"; then bad "  ↳ 输出不应含: $1"; else ok "  ↳ 输出正确地不含: $1"; fi; }

write_fixture() { printf '%s\n' "$2" > "${TMP}/$1"; echo "${TMP}/$1"; }

printf '\n\033[1m1. 合规集（AE-07）\033[0m\n'
run 0 "26 条前缀正确 + id 26 豁免 → 通过" \
  --channels-file "${FIX}/channels-compliant.json" \
  --allowed-hosts "upstream-a.example,upstream-b.example,upstream-c.example"
has "豁免清单 (1)"
has "#26 openai"
has "结论: 通过"

printf '\n\033[1m2. 违规集\033[0m\n'
run 1 "五类违规 → 不通过" \
  --channels-file "${FIX}/channels-violations.json" \
  --allowed-hosts "upstream-a.example,upstream-d.example"
has "#10 direct-no-tag"
has "#11 endpoint-bypass"
has "#12 websocket-chan"
has "#13 empty-url"
has "tag 多余"

printf '\n\033[1m3. 单项违规隔离验证\033[0m\n'

F="$(write_fixture direct.json '[{"id":5,"name":"x","status":"enabled","base_url":"https://third.example/v1","tags":[],"endpoints":[]}]')"
run 1 "第三方直连且无 tag → 违规" --channels-file "$F" --skip-host-check
has "明文直连"

F="$(write_fixture ep.json '[{"id":6,"name":"y","status":"enabled","base_url":"http://redact:8787/$https://a.example","tags":[],"endpoints":[{"base_url":"https://direct.example"}]}]')"
run 1 "base_url 有前缀但 endpoint 直连 → 违规" --channels-file "$F" --skip-host-check
has "绕过脱敏层"

F="$(write_fixture ws.json '[{"id":7,"name":"z","status":"enabled","base_url":"wss://rt.example/v1","tags":[],"endpoints":[]}]')"
run 1 "wss:// → 违规且注明 WebSocket" --channels-file "$F" --skip-host-check
has "WebSocket"

F="$(write_fixture empty.json '[{"id":8,"name":"e","status":"enabled","base_url":"","tags":[],"endpoints":[]}]')"
run 1 "空 base_url → 违规且注明需显式 URL" --channels-file "$F" --skip-host-check
has "需先写显式 URL"

printf '\n\033[1m4. 允许主机集合漂移（KTD-5）\033[0m\n'

F="$(write_fixture hosts.json '[
  {"id":1,"name":"a","status":"enabled","base_url":"http://redact:8787/$https://a.example","tags":[],"endpoints":[]},
  {"id":2,"name":"b","status":"enabled","base_url":"http://redact:8787/$https://b.example","tags":[],"endpoints":[]}
]')"
run 0 "集合相等 → 通过" --channels-file "$F" --allowed-hosts "a.example,b.example"
has "允许主机集合: 一致"

run 1 "少一个主机 → 漂移并打印差集" --channels-file "$F" --allowed-hosts "a.example"
has "缺少（渠道需要但未放行）: b.example"

run 1 "多一个主机 → 漂移并打印差集" --channels-file "$F" --allowed-hosts "a.example,b.example,stale.example"
has "多余（已放行但无渠道使用）: stale.example"

run 0 "顺序与大小写不影响相等判定" --channels-file "$F" --allowed-hosts "B.EXAMPLE , a.example"

printf '\n\033[1m5. 状态与集合归属\033[0m\n'

F="$(write_fixture archived.json '[
  {"id":1,"name":"ok","status":"enabled","base_url":"http://redact:8787/$https://a.example","tags":[],"endpoints":[]},
  {"id":9,"name":"old","status":"archived","base_url":"https://legacy.example/v1","tags":[],"endpoints":[]}
]')"
run 0 "已归档渠道直连 → 忽略，不影响结果" --channels-file "$F" --allowed-hosts "a.example"
hasnt "#9 old"

F="$(write_fixture disabled.json '[
  {"id":3,"name":"off","status":"disabled","base_url":"http://redact:8787/$https://c.example","tags":[],"endpoints":[]}
]')"
run 0 "禁用但未归档的受保护渠道 → 主机计入期望集合" --channels-file "$F" --allowed-hosts "c.example"
has "#3 off"
run 1 "同上：若该主机未放行则判漂移" --channels-file "$F" --allowed-hosts ""

printf '\n\033[1m6. 数据形状容错\033[0m\n'

F="$(write_fixture strtags.json '[{"id":26,"name":"openai","status":"enabled","base_url":"https://api.openai.com/v1","tags":"[\"redact-exempt\"]","endpoints":"[]"}]')"
run 0 "tags/endpoints 为 JSON 字符串（psql 常见形状）→ 正确解析" --channels-file "$F" --skip-host-check
has "豁免清单 (1)"

F="$(write_fixture badup.json '[{"id":4,"name":"m","status":"enabled","base_url":"http://redact:8787/$not-a-url","tags":[],"endpoints":[]}]')"
run 1 "有前缀但 \$ 后不是合法 URL → 违规" --channels-file "$F" --skip-host-check
has "不是合法上游 URL"

run 2 "--channels-file 指向不存在的文件 → 用法错误" --channels-file "${TMP}/nope.json"

printf '\n\033[1m7. --help\033[0m\n'
OUT="$(bash "$SCRIPT" --help 2>&1)"; rc=$?
if [[ "$rc" == 0 ]]; then ok "--help exit=0"; else bad "--help 应 exit=0，实得 $rc"; fi
has "退出码: 0 通过"
has "--channels-file"
has "--allowed-hosts"


printf '
[1m8. 生产形状回归（线上才发现的两个缺陷）[0m
'
run 1 "GraphQL gid:// id + baseURL 驼峰 + endpoints:null -> 正确解析（#10 直连=违规，#26 豁免，#27 受保护）" --channels-file "${FIX}/channels-gql-shape.json" --skip-host-check
has "受保护渠道 (1)"
has "豁免清单 (1)"
has "违规清单 (1)"
has "Channel/10 sotamodel: 明文直连"
run 0 "psql 导出含 deleted_at<>0 的软删除行 -> 判定层忽略（不判违规）" --channels-file "${FIX}/channels-psql-softdeleted.json" --skip-host-check
hasnt "#20 ghost"
has "违规清单 (0)"
run 1 "--allowed-hosts "" 表示空集合（开放代理条件），不再回退到 docker inspect" --channels-file "${FIX}/channels-compliant.json" --allowed-hosts ""
has "缺少（渠道需要但未放行）"
printf '\n\033[1m汇总\033[0m\n  PASS %d    FAIL %d\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || { printf '\n\033[31m测试未通过\033[0m\n'; exit 1; }
printf '\n\033[32m全部通过\033[0m\n'
