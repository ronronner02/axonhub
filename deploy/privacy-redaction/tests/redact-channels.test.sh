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


printf '
[1m11. 生产形状回归（线上才发现的两个缺陷）[0m
'
run 0 "GraphQL gid:// id：--only 10 能命中" --plan-from-file "${FIX}/channels-gql-shape.json" --only 10
has "rewrite (1)"
has "#10 sotamodel"
run 0 "GraphQL gid:// id：豁免/已保护分类不受 id 形态影响" --plan-from-file "${FIX}/channels-gql-shape.json"
has "skip-exempt (1)"
has "skip-protected (1)"
run 0 "psql 导出含 deleted_at<>0 -> skip-deleted，不进入 rewrite" --plan-from-file "${FIX}/channels-psql-softdeleted.json"
has "skip-deleted (1)"
has "#20 ghost"
has "待写 0 条"

printf '\n\033[1m12. --flags 标志位与带标志位封套的回滚（KTD-5）\033[0m\n'

# 12.1 带标志位封套的 base 回滚 -> 还原为裸上游，箭头右侧不留标志位残段
FR="$(fx flag-restore.json '[{"id":11,"name":"any","status":"enabled","baseURL":"http://redact:8787/PSIBEG$https://any-a.example","tags":[],"endpoints":[]}]')"
run 1 "带标志位封套 --restore -> 还原为裸上游（缺 --yes 仍先打印计划）" --plan-from-file "$FR" --restore 11
has 'http://redact:8787/PSIBEG$https://any-a.example  ->  https://any-a.example'
hasnt '->  http://redact:8787/'

# 12.2 端点 URL 也带标志位封套 -> base 与 endpoint 一并还原（端点行可观测）
run 0 "端点也带标志位封套 -> base 与 endpoint 一并还原" --plan-from-file "${FIX}/channels-flagged.json" --restore 12 --yes
has 'http://redact:8787/PSIBEG$https://ep-base.example  ->  https://ep-base.example'
has 'endpoint: http://redact:8787/PSIBEG$https://ep-one.example -> https://ep-one.example'

# 12.3 无标志位封套的回滚结果与第 8 组期望逐字相同（R8 未回归）
FL="$(fx flagless-restore.json '[{"id":12,"name":"p","status":"enabled","baseURL":"http://redact:8787/$https://third.example/v1","tags":[],"endpoints":[]}]')"
run 1 "无标志位封套回滚 -> 与第 8 组期望逐字相同" --plan-from-file "$FL" --restore 12
has 'http://redact:8787/$https://third.example/v1  ->  https://third.example/v1'

# 12.4 未受保护渠道 + --flags -> rewrite 直接写出带标志位的封套
FNEW="$(fx flag-new.json '[{"id":10,"name":"a","status":"enabled","baseURL":"https://third.example/v1","tags":[],"endpoints":[]}]')"
run 0 "--flags PSIBEG 对未受保护渠道 -> rewrite 写出带标志位封套" --plan-from-file "$FNEW" --flags PSIBEG --only 10 --yes
has "rewrite (1)"
has 'https://third.example/v1  ->  http://redact:8787/PSIBEG$https://third.example/v1'

# 12.5 空段封套 + --flags -> reflag，只替换标志位段
FEMPTY="$(fx reflag-empty.json '[{"id":12,"name":"p","status":"enabled","baseURL":"http://redact:8787/$https://third.example","tags":[],"endpoints":[]}]')"
run 0 "--flags PSIBEG 对空段封套 -> reflag 只替换标志位段" --plan-from-file "$FEMPTY" --flags PSIBEG --only 12 --yes
has "reflag (1)"
has 'http://redact:8787/$https://third.example  ->  http://redact:8787/PSIBEG$https://third.example'
has "待写 1 条"

# 12.6 --flags 与现状等价 -> 幂等跳过，零写入
FSAME="$(fx same-flags.json '[{"id":12,"name":"p","status":"enabled","baseURL":"http://redact:8787/PSIBEG$https://third.example","tags":[],"endpoints":[]}]')"
run 0 "--flags 与现状等价 -> skip-protected，零写入" --plan-from-file "$FSAME" --flags PSIBEG --only 12 --yes
has "skip-protected"
has "待写 0 条"

# 12.7 字母顺序不同但集合相等 -> 同样判为等价
run 0 "--flags 顺序不同但集合相等 -> 仍 skip-protected" --plan-from-file "$FSAME" --flags SPIBEG --only 12 --yes
has "skip-protected"
has "待写 0 条"

# 12.8 全集 HPSIBEG 归一为空段：是「恢复全部检测」而非削弱，无需 --yes
run 0 "--flags HPSIBEG（全集）-> 归一为空段且无需 --yes" --plan-from-file "$FSAME" --flags HPSIBEG --only 12
has "reflag (1)"
has 'http://redact:8787/PSIBEG$https://third.example  ->  http://redact:8787/$https://third.example'

# 12.9 削弱检测缺 --yes -> 先打印计划，再以退出码 1 拦住，并点名被关掉的检测
run 1 "削弱检测缺 --yes -> 打印计划后退出码 1，点名关闭项并要求记录 BR-005" --plan-from-file "$FEMPTY" --flags PSIBEG --only 12
has "改写计划"
has "高熵检测"
has "BR-005"

# 12.10 削弱检测缺 --only -> 拒绝全库范围的削弱
run 1 "削弱检测缺 --only -> 退出码 1，要求限定范围" --plan-from-file "$FSAME" --flags PSIBEG --yes
has "请用 --only"

# 12.11 --flags 取值校验：非法字母 / 小写都属用法错误
run 2 "--flags 含 HPSIBEG 之外的字母 -> 退出码 2" --plan-from-file "$FSAME" --flags XYZ
has "非法字母"
run 2 "--flags 小写 -> 退出码 2" --plan-from-file "$FSAME" --flags psibeg
has "只接受大写字母"

# 12.12 --flags 与 --restore 语义冲突
run 2 "--flags 与 --restore 互斥 -> 退出码 2" --plan-from-file "$FSAME" --flags PSIBEG --restore 12
has "互斥"

# 12.14 非法旧标志位有 `$` 时可由目标 flags 修复，不能被错误地视为集合等价。
FBAD="$(fx bad-flagged.json '[{"id":12,"name":"bad","status":"enabled","baseURL":"http://redact:8787/XPSIBEG$https://third.example","tags":[],"endpoints":[]}]')"
run 0 "非法旧标志位 + --flags -> reflag 为目标段，不得静默跳过" --plan-from-file "$FBAD" --flags PSIBEG --only 12 --yes
has "reflag (1)"
has 'http://redact:8787/XPSIBEG$https://third.example  ->  http://redact:8787/PSIBEG$https://third.example'
hasnt "skip-protected"

# 12.15 缺 `$` 的畸形封套没有可靠上游分界，必须阻塞而不是原样写回或跳过。
FMAL="$(fx malformed-envelope.json '[{"id":12,"name":"malformed","status":"enabled","baseURL":"http://redact:8787/PSIBEGhttps://third.example","tags":[],"endpoints":[]}]')"
run 1 "缺 $ 的畸形封套 -> 阻塞且零写入" --plan-from-file "$FMAL" --flags PSIBEG --only 12 --yes
has "error-invalid-envelope"
has "待写 0 条"

# 12.16 混合状态在指定 flags 时统一到目标段：已有封套与直连 endpoint 都要收敛。
FMIX="$(fx mixed-protection.json '[{"id":12,"name":"mixed","status":"enabled","baseURL":"http://redact:8787/$https://third.example","tags":[],"endpoints":[{"apiFormat":"anthropic","baseURL":"https://endpoint.example"}]}]')"
run 0 "base 已封套、endpoint 直连 + --flags -> 两者统一为目标段" --plan-from-file "$FMIX" --flags PSIBEG --only 12 --yes
has 'http://redact:8787/$https://third.example  ->  http://redact:8787/PSIBEG$https://third.example'
has 'endpoint: https://endpoint.example -> http://redact:8787/PSIBEG$https://endpoint.example'

# 12.17 所有带值选项缺参时都应立即退出，不能 shift 死循环或继续读下一选项。
for missing in --plan-from-file --only --flags --restore; do
  OUT="$(bash "$SCRIPT" "$missing" 2>&1)"; rc=$?
  if [[ "$rc" == 2 ]]; then ok "$missing 缺参 -> exit=2"; else bad "$missing 缺参应 exit=2，实得 $rc"; fi
  has "需要一个参数"
done

# 12.13 --help 覆盖新选项与新动作
OUT="$(bash "$SCRIPT" --help 2>&1)"; rc=$?
if [[ "$rc" == 0 ]]; then ok "--help exit=0（含新选项）"; else bad "--help 应 exit=0，实得 $rc"; fi
has "--flags"
has "reflag"


printf '\n\033[1m13. 行为不变回归：不给 --flags 时与 HEAD 版逐字一致\033[0m\n'
# Verification Contract 的「行为不变」项：--flags 是纯增量，未使用时对既有夹具的
# 计划输出与退出码必须与 HEAD 版逐字相同。取 HEAD 版到 TMP 后同 harness 对跑。
REPO_ROOT="$(cd "${BASE_DIR}/../.." && pwd)"
HEAD_SCRIPT="${TMP}/head-redact-channels.sh"
HEAD_PATH="deploy/privacy-redaction/scripts/redact-channels.sh"
if git -C "$REPO_ROOT" show "HEAD:${HEAD_PATH}" > "$HEAD_SCRIPT" 2>/dev/null && [[ -s "$HEAD_SCRIPT" ]]; then
  if cmp -s "$HEAD_SCRIPT" "$SCRIPT"; then
    info "HEAD 版与工作树版字节相同，本组比对为空转（改动已提交后属正常）"
  else
    for fxname in channels-compliant channels-gql-shape channels-psql-softdeleted channels-violations; do
      for mode in plan only10 restore; do
        case "$mode" in
          plan)    cmp_args=(--plan-from-file "${FIX}/${fxname}.json") ;;
          only10)  cmp_args=(--plan-from-file "${FIX}/${fxname}.json" --only 10) ;;
          restore) cmp_args=(--plan-from-file "${FIX}/${fxname}.json" --restore 1,10,11,12,26,27 --yes) ;;
        esac
        o="$(bash "$HEAD_SCRIPT" "${cmp_args[@]}" 2>&1)"; orc=$?
        n="$(bash "$SCRIPT" "${cmp_args[@]}" 2>&1)"; nrc=$?
        if [[ "$o" == "$n" && "$orc" == "$nrc" ]]; then
          ok "${fxname} / ${mode} 与 HEAD 版逐字一致 (exit=${orc})"
        else
          bad "${fxname} / ${mode} 与 HEAD 版有差异 (exit old=${orc} new=${nrc})"
          info "$(diff <(printf '%s\n' "$o") <(printf '%s\n' "$n") | head -8)"
        fi
      done
    done
  fi
else
  info "跳过：无法从 HEAD 取出旧版脚本（git 不可用或该路径不在版本库中）"
fi
printf '\n\033[1m汇总\033[0m\n  PASS %d    FAIL %d\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] || { printf '\n\033[31m测试未通过\033[0m\n'; exit 1; }
printf '\n\033[32m全部通过\033[0m\n'
