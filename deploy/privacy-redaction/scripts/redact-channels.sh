#!/usr/bin/env bash
# 渠道批量改写 / 回滚（KTD-1 封套前缀，KTD-4 管理端 GraphQL）
#
# 默认 dry-run：只打印计划，不发出任何写操作。
#
# 用法:
#   # 1) 离线看计划（不连网，最安全的第一步）
#   bash redact-channels.sh --plan-from-file channels.json
#
#   # 2) 连接 axonhub 看计划
#   AXONHUB_ADMIN_URL=http://127.0.0.1:8090 \
#   AXONHUB_EMAIL=<管理员邮箱> AXONHUB_PASSWORD=... \
#     bash redact-channels.sh
#
#   # 3) 真正改写
#   ... bash redact-channels.sh --apply
#
#   # 4) 回滚指定渠道（必须同时给 --yes）
#   ... bash redact-channels.sh --restore 12,13 --yes
#
# 走管理端 GraphQL 而非直改数据库：updateChannel 会触发
# reloadChannelsAfterCommit 刷新内存缓存并留下 updated_at。
#
# 退出码: 0 计划/执行成功；1 存在阻塞错误（空 URL / WebSocket / 回滚缺 --yes）；2 用法或环境错误。

set -uo pipefail

PREFIX="${REDACT_PREFIX:-http://redact:8787/}"
EXEMPT_TAG="${REDACT_EXEMPT_TAG:-redact-exempt}"
ADMIN_URL="${AXONHUB_ADMIN_URL:-http://127.0.0.1:8090}"

APPLY=0
YES=0
PLAN_FILE=""
RESTORE_IDS=""
ONLY_IDS=""
FLAGS_RAW=""
FLAGS_SET=0
FLAGS=""

# CRG 封套标志位，取自 crg/worker.js 的 ALL_FLAG_LETTERS / FLAG_NAMES。
# 空段等价全开；中文名只用于「关闭了哪些检测」的提示文案。
ALL_FLAG_LETTERS="HPSIBEG"
flag_cn() {
  case "$1" in
    H) printf '高熵检测' ;; P) printf '手机号' ;; S) printf '密钥' ;;
    I) printf '身份证件' ;; B) printf '银行卡' ;; E) printf '邮箱' ;;
    G) printf 'gitleaks 规则' ;; *) printf '%s' "$1" ;;
  esac
}

usage() {
  cat <<'EOF'
用法: redact-channels.sh [选项]

选项:
  --plan-from-file <路径>  从 JSON 文件读渠道并只出计划，完全不连网。
  --only <id,...>          只处理指定渠道 id。
  --flags <LETTERS>        封套标志位，HPSIBEG 的子集（大写、不重复）。
                           省略 = 沿用各渠道现状；全集 HPSIBEG 等价空段（全部检测开启）。
                           削弱检测（非全集）时必须同时给 --only 与 --yes。
  --restore <id,...>       生成去前缀（回滚）计划；执行需同时给 --yes。
  --apply                  真正执行改写（默认只 dry-run）。
  --yes                    确认执行回滚或削弱检测。
  -h, --help               显示本帮助。

环境变量:
  AXONHUB_ADMIN_URL   默认 http://127.0.0.1:8090
  AXONHUB_EMAIL       管理员邮箱
  AXONHUB_PASSWORD    管理员口令
  AXONHUB_TOKEN       已有 JWT，设置后跳过 signin
  REDACT_PREFIX       封套前缀，默认 http://redact:8787/
  REDACT_EXEMPT_TAG   豁免 tag，默认 redact-exempt

标志位字母（与 crg/worker.js 一致）:
  H 高熵  P 手机  S 密钥  I 身份  B 银行  E 邮箱  G gitleaks

动作分类:
  rewrite          未受保护的第三方渠道 -> 加封套前缀
  reflag           已加前缀但标志位与 --flags 不同 -> 只替换标志位段
  skip-protected   已加前缀（且标志位与 --flags 等价），幂等跳过
  skip-exempt      带豁免 tag，永不改写（即使 --only 指定）
  skip-archived    已归档
  skip-deleted     ent 软删除行（GraphQL 不可见，仅 psql 导出会出现）
  restore          去掉封套前缀（仅 --restore）
  error-empty-url  base_url 为空，需先写显式 URL
  error-websocket  ws:// / wss:// 无法经脱敏层

退出码: 0 成功 / 1 阻塞错误 / 2 用法或环境错误
EOF
}

die() { printf '错误: %s\n' "$1" >&2; exit 2; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --plan-from-file|--only|--flags|--restore)
      [[ $# -ge 2 && -n "${2:-}" && "$2" != -* ]] || die "$1 需要一个参数（且不能为空）"
      case "$1" in
        --plan-from-file) PLAN_FILE="$2" ;;
        --only) ONLY_IDS="$2" ;;
        --flags) FLAGS_RAW="$2"; FLAGS_SET=1 ;;
        --restore) RESTORE_IDS="$2" ;;
      esac
      shift 2 ;;
    --apply) APPLY=1; shift ;;
    --yes) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage >&2; exit 2 ;;
  esac
done

# ── --flags 校验与规范化 ──────────────────────────────────────────────────────
# 字母必须是 ALL_FLAG_LETTERS 的子集、大写、不重复；按 ALL_FLAG_LETTERS 顺序
# 规范化；全集归一为空段，使写出的封套与现状 "$" 逐字一致。
if [[ "$FLAGS_SET" == "1" ]]; then
  [[ -z "$RESTORE_IDS" ]] || die "--flags 与 --restore 互斥，不能同用"
  [[ "$FLAGS_RAW" =~ ^[A-Z]*$ ]] \
    || die "--flags 只接受大写字母（合法字母: ${ALL_FLAG_LETTERS}），实得: ${FLAGS_RAW}"
  _seen=""
  for (( _i=0; _i<${#FLAGS_RAW}; _i++ )); do
    _ch="${FLAGS_RAW:$_i:1}"
    [[ "$ALL_FLAG_LETTERS" == *"$_ch"* ]] \
      || die "--flags 含非法字母 '${_ch}'（合法字母: ${ALL_FLAG_LETTERS}）"
    [[ "$_seen" != *"$_ch"* ]] || die "--flags 含重复字母 '${_ch}'"
    _seen="${_seen}${_ch}"
  done
  _norm=""
  for (( _i=0; _i<${#ALL_FLAG_LETTERS}; _i++ )); do
    _ch="${ALL_FLAG_LETTERS:$_i:1}"
    [[ "$FLAGS_RAW" == *"$_ch"* ]] && _norm="${_norm}${_ch}"
  done
  [[ "$_norm" != "$ALL_FLAG_LETTERS" ]] || _norm=""
  FLAGS="$_norm"
  unset _seen _i _ch _norm
fi

# ── node 求值器（与 check-trust-boundary.sh 相同策略）──────────────────────────
# 目标服务器只有 docker、没有 node。所有 JS 片段都必须经由同一个分派器，
# 不允许在脚本任何位置直接调用裸的 node 可执行文件。

# 容器内 node 需要显式透传的环境变量（内联 JS 片段读取的全部键）。
NODE_ENV_KEYS=(CHANNELS_JSON PREFIX EXEMPT_TAG RESTORE_IDS ONLY_IDS FLAGS FLAGS_SET EMAIL PASS AFTER Q V ACC RESP P I X R)

pick_node_runner() {
  if command -v node >/dev/null 2>&1; then echo host
  elif command -v docker >/dev/null 2>&1 && docker image inspect "${REDACT_IMAGE:-axonhub-redact:local}" >/dev/null 2>&1; then echo image
  elif command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "${REDACT_CONTAINER:-axonhub-redact}"; then echo container
  else echo none; fi
}

NODE_RUNNER="$(pick_node_runner)"
[[ "$NODE_RUNNER" != "none" ]] || die "找不到可用的 JSON 求值器（需要宿主机 node，或 redact 镜像/容器）"

# 组装容器分派前缀。--network host 让容器内的 curl 之外的逻辑不受影响；
# 这里的 node 只做 JSON 变换，不发网络请求，网络由宿主 curl 负责。
_node_prefix() {
  local envflags=()
  local k
  for k in "${NODE_ENV_KEYS[@]}"; do envflags+=(-e "$k"); done
  case "$NODE_RUNNER" in
    host)      printf '%s\n' node ;;
    image)     printf '%s\n' docker run --rm -i "${envflags[@]}" "${REDACT_IMAGE:-axonhub-redact:local}" node ;;
    container) printf '%s\n' docker exec -i "${envflags[@]}" "${REDACT_CONTAINER:-axonhub-redact}" node ;;
  esac
}
mapfile -t NODE_CMD < <(_node_prefix)

# js '<script>'      —— 等价 node -e '<script>'，环境变量按 NODE_ENV_KEYS 透传
# js_module          —— 等价 node --input-type=module -，脚本从 stdin 读
js()        { "${NODE_CMD[@]}" -e "$1"; }
js_module() { "${NODE_CMD[@]}" --input-type=module -; }

# ── 网络层 ────────────────────────────────────────────────────────────────────

http_json() {
  # $1=method $2=url $3=body $4=auth-header-or-empty
  local extra=()
  [[ -n "${4:-}" ]] && extra=(-H "Authorization: Bearer $4")
  curl -sS -X "$1" "$2" \
    -H 'content-type: application/json' \
    "${extra[@]}" \
    --data-binary "$3"
}

get_token() {
  if [[ -n "${AXONHUB_TOKEN:-}" ]]; then printf '%s' "$AXONHUB_TOKEN"; return; fi
  [[ -n "${AXONHUB_EMAIL:-}" && -n "${AXONHUB_PASSWORD:-}" ]] \
    || die "需要 AXONHUB_EMAIL 与 AXONHUB_PASSWORD（或直接给 AXONHUB_TOKEN）"
  command -v curl >/dev/null 2>&1 || die "需要 curl"
  local body resp
  body="$(EMAIL="$AXONHUB_EMAIL" PASS="$AXONHUB_PASSWORD" js \
    'process.stdout.write(JSON.stringify({email:process.env.EMAIL,password:process.env.PASS}))' 2>/dev/null)" \
    || die "构造登录请求失败（JSON 求值器不可用）"
  resp="$(http_json POST "${ADMIN_URL}/admin/auth/signin" "$body" "")" || die "signin 请求失败"
  printf '%s' "$resp" | js \
    'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);if(!j.token)throw 0;process.stdout.write(j.token)}catch{process.stderr.write("signin 未返回 token: "+s.slice(0,200)+"\n");process.exit(2)}})' \
    || exit 2
}

fetch_channels_gql() {
  local token="$1" after="null" out="[]" q resp
  q='query($after:Cursor){channels(first:100,after:$after){pageInfo{hasNextPage endCursor} edges{node{id name type status baseURL tags endpoints{apiFormat path baseURL transport}}}}}'
  local acc="[]"
  while :; do
    local vars body
    vars="$(AFTER="$after" js 'const a=process.env.AFTER;process.stdout.write(JSON.stringify({after:a==="null"?null:a}))')"
    body="$(Q="$q" V="$vars" js 'process.stdout.write(JSON.stringify({query:process.env.Q,variables:JSON.parse(process.env.V)}))')"
    resp="$(http_json POST "${ADMIN_URL}/admin/graphql" "$body" "$token")" || die "channels 查询失败"
    local page
    page="$(ACC="$acc" RESP="$resp" js '
let r;try{r=JSON.parse(process.env.RESP)}catch{console.error("GraphQL 响应非 JSON");process.exit(2)}
if(r.errors){console.error("GraphQL 错误: "+JSON.stringify(r.errors).slice(0,300));process.exit(2)}
const c=r.data&&r.data.channels;if(!c){console.error("响应缺少 channels");process.exit(2)}
const acc=JSON.parse(process.env.ACC);
for(const e of (c.edges||[])) if(e&&e.node) acc.push(e.node);
process.stdout.write(JSON.stringify({acc,hasNext:!!(c.pageInfo&&c.pageInfo.hasNextPage),cursor:(c.pageInfo&&c.pageInfo.endCursor)||null}));
')" || exit 2
    acc="$(ACC="$page" js 'let s=process.env.ACC;process.stdout.write(JSON.stringify(JSON.parse(s).acc))')"
    local hasNext cursor
    hasNext="$(P="$page" js 'process.stdout.write(String(JSON.parse(process.env.P).hasNext))')"
    cursor="$(P="$page" js 'const c=JSON.parse(process.env.P).cursor;process.stdout.write(c===null?"null":String(c))')"
    [[ "$hasNext" == "true" ]] || break
    after="$cursor"
  done
  printf '%s' "$acc"
}

# ── 取渠道 ────────────────────────────────────────────────────────────────────

TOKEN=""
if [[ -n "$PLAN_FILE" ]]; then
  [[ -f "$PLAN_FILE" ]] || die "找不到 --plan-from-file 指定的文件: $PLAN_FILE"
  CHANNELS_JSON="$(cat "$PLAN_FILE")"
  if [[ "$APPLY" == "1" ]]; then die "--plan-from-file 与 --apply 不能同用（离线模式无法写入）"; fi
else
  TOKEN="$(get_token)" || exit 2
  CHANNELS_JSON="$(fetch_channels_gql "$TOKEN")" || exit 2
fi
[[ -n "$CHANNELS_JSON" ]] || die "渠道数据为空"


export CHANNELS_JSON PREFIX EXEMPT_TAG RESTORE_IDS ONLY_IDS FLAGS FLAGS_SET

# ── 计划层 ────────────────────────────────────────────────────────────────────
# 输出人类可读计划到 stderr，机器可读动作（JSON 行）到 stdout。

PLAN="$(js_module <<'JS'
const PREFIX = process.env.PREFIX, EXEMPT_TAG = process.env.EXEMPT_TAG;
const restoreIds = new Set(String(process.env.RESTORE_IDS || "").split(",").map(s=>s.trim()).filter(Boolean));
const onlyIds = new Set(String(process.env.ONLY_IDS || "").split(",").map(s=>s.trim()).filter(Boolean));
const isRestore = restoreIds.size > 0;

// 封套标志位。FLAGS 已由 shell 规范化（按 ALL_FLAG_LETTERS 排序，全集归一为空段）；
// FLAGS_SET 区分「没给 --flags」与「给了全集」——前者沿用现状，后者要求写成 "$"。
const ALL_FLAG_LETTERS = "HPSIBEG";
const flagsSet = process.env.FLAGS_SET === "1";
const wantFlags = process.env.FLAGS || "";
// 与 shell 侧同一套规范化，使集合相等而顺序不同的两个段判为等价。
const normFlags = (seg) => {
  let out = "";
  for (const c of ALL_FLAG_LETTERS) if (seg.includes(c)) out += c;
  return out === ALL_FLAG_LETTERS ? "" : out;
};
// 已加封套的 URL：PREFIX 之后、第一个 $ 之前是标志位段。无 $ 视为形状异常（返回 null）。
const envelopeOf = (u) => {
  if (!u.startsWith(PREFIX)) return null;
  const rest = u.slice(PREFIX.length);
  const dollar = rest.indexOf("$");
  if (dollar < 0) return { segment: null, upstream: null, valid: false };
  const segment = rest.slice(0, dollar);
  const valid = /^[A-Z]*$/.test(segment) && [...segment].every((c) => ALL_FLAG_LETTERS.includes(c));
  return { segment, upstream: rest.slice(dollar + 1), valid };
};
const segOf = (u) => envelopeOf(u)?.segment ?? null;
// 去封套：按第一个 `$` 剥掉完整标志位段（R8）。
const unwrap = (u) => {
  const rest = u.slice(PREFIX.length);
  const dollar = rest.indexOf("$");
  return dollar < 0 ? u : rest.slice(dollar + 1);
};
// 换标志位段，保留其后的上游 URL 原样。
const reseg = (u) => {
  const rest = u.slice(PREFIX.length);
  const dollar = rest.indexOf("$");
  if (dollar < 0) return u;
  return PREFIX + wantFlags + "$" + rest.slice(dollar + 1);
};

let channels;
try { channels = JSON.parse(process.env.CHANNELS_JSON); }
catch (e) { console.error("渠道 JSON 解析失败: " + e.message); process.exit(2); }

const norm = (v) => (typeof v === "string" ? v.trim() : "");
const pick = (o, ...ks) => { for (const k of ks) if (o && o[k] != null) return o[k]; return undefined; };
const tagsOf = (c) => {
  let t = pick(c, "tags");
  if (typeof t === "string" && t) { try { t = JSON.parse(t); } catch { return []; } }
  return Array.isArray(t) ? t.map(norm) : [];
};
const endpointsOf = (c) => {
  let e = pick(c, "endpoints");
  if (typeof e === "string" && e) { try { e = JSON.parse(e); } catch { return []; } }
  return Array.isArray(e) ? e : [];
};
const baseOf = (c) => norm(pick(c, "baseURL", "base_url"));
const epBase = (ep) => norm(pick(ep, "baseURL", "base_url"));
const epFormat = (ep) => norm(pick(ep, "apiFormat", "api_format"));

const actions = [];
const rows = [];

// GraphQL 返回 gid://axonhub/Channel/N，psql 导出返回数字 N。
// 内部比对（--only / --restore）统一用数字；写回 updateChannel 用 gid。
const GID_PREFIX = "gid://axonhub/Channel/";
const numericId = (raw) => { const s = String(raw); return s.startsWith(GID_PREFIX) ? s.slice(GID_PREFIX.length) : s; };
const gidOf = (n) => GID_PREFIX + n;

for (const c of channels) {
  const id = numericId(pick(c, "id"));
  const gid = gidOf(id);
  const name = pick(c, "name");
  const status = norm(pick(c, "status")).toLowerCase();
  const tags = tagsOf(c);
  const eps = endpointsOf(c);
  const base = baseOf(c);
  const urls = [base, ...eps.map(epBase).filter(Boolean)];

  const add = (action, detail, extra) => { rows.push({ id, name, action, detail }); if (extra) actions.push({ id, gid, name, action, ...extra }); };

  if (onlyIds.size && !onlyIds.has(id)) continue;
  if (isRestore && !restoreIds.has(id)) continue;

  if (status === "archived") { add("skip-archived", "已归档"); continue; }
  const deletedAt = pick(c, "deleted_at", "deletedAt");
  if (deletedAt != null && Number(deletedAt) !== 0) { add("skip-deleted", "ent 软删除（GraphQL 不可见）"); continue; }
  if (!isRestore && tags.includes(EXEMPT_TAG)) { add("skip-exempt", `带 ${EXEMPT_TAG} tag，永不改写`); continue; }
  if (urls.some((u) => /^wss?:\/\//i.test(u))) { add("error-websocket", "ws:// / wss:// 无法经脱敏层"); continue; }
  if (!base) { add("error-empty-url", "base_url 为空，需先在控制台写显式 URL"); continue; }

  const prefixed = urls.filter((u) => u.startsWith(PREFIX));
  const malformed = prefixed.find((u) => {
    const envelope = envelopeOf(u);
    return !envelope || envelope.segment === null || !envelope.upstream;
  });
  if (malformed) {
    add("error-invalid-envelope", `封套缺少 $ 分界或上游 URL（${malformed}）`);
    continue;
  }
  const invalidFlags = prefixed.find((u) => !envelopeOf(u).valid);
  if (invalidFlags && !flagsSet) {
    add("error-invalid-envelope", `封套标志位段含非法字符，未提供 --flags 无法安全修复（${invalidFlags}）`);
    continue;
  }

  if (isRestore) {
    if (!base.startsWith(PREFIX)) { add("error-not-prefixed", `未加前缀，无法回滚（${base}）`); continue; }
    const newBase = unwrap(base);
    const newEps = eps.map((ep) => {
      const b = epBase(ep);
      return { apiFormat: epFormat(ep), path: pick(ep, "path") ?? null,
               baseURL: b.startsWith(PREFIX) ? unwrap(b) : (b || null),
               transport: pick(ep, "transport") ?? null };
    });
    // 与 reflag 分支同一套 epNote 写法；只有 baseURL 真的变了的端点才打印，
    // 未加前缀或为空的端点在回滚下保持原样，因此既有夹具输出逐字不变。
    const epNote = eps.map((ep, i) => {
      const ob = epBase(ep);
      return ob && newEps[i].baseURL !== ob ? `
      endpoint: ${ob} -> ${newEps[i].baseURL}` : "";
    }).join("");
    add("restore", `${base}  ->  ${newBase}${epNote}`, { newBase, newEps, oldBase: base });
    continue;
  }

  const allPrefixed = urls.every((u) => u.startsWith(PREFIX));
  if (allPrefixed) {
    // 没给 --flags 时保持原语义：已加前缀即幂等跳过，不看标志位。
    const needsReflag = flagsSet && urls.some((u) => {
      const envelope = envelopeOf(u);
      return !envelope.valid || normFlags(envelope.segment) !== wantFlags;
    });
    if (!needsReflag) { add("skip-protected", "已加前缀，幂等跳过"); continue; }
    const newBase = reseg(base);
    const newEps = eps.map((ep) => {
      const b = epBase(ep);
      return { apiFormat: epFormat(ep), path: pick(ep, "path") ?? null,
               baseURL: b ? reseg(b) : null, transport: pick(ep, "transport") ?? null };
    });
    const epNote = eps.map((ep, i) => {
      const ob = epBase(ep);
      return ob ? `\n      endpoint: ${ob} -> ${newEps[i].baseURL}` : "";
    }).join("");
    add("reflag", `${base}  ->  ${newBase}${epNote}`, { newBase, newEps, oldBase: base });
    continue;
  }

  // 未给 --flags 时段为空，写出的封套与改动前逐字相同。
  const wrapSeg = flagsSet ? wantFlags : "";
  const wrap = (u) => {
    if (!u.startsWith(PREFIX)) return PREFIX + wrapSeg + "$" + u;
    return flagsSet ? reseg(u) : u;
  };
  const newBase = wrap(base);
  const newEps = eps.map((ep) => {
    const b = epBase(ep);
    return { apiFormat: epFormat(ep), path: pick(ep, "path") ?? null,
             baseURL: b ? wrap(b) : null, transport: pick(ep, "transport") ?? null };
  });
  const epNote = newEps.filter((e, i) => epBase(eps[i])).map((e, i) => `\n      endpoint: ${epBase(eps[i])} -> ${e.baseURL}`).join("");
  add("rewrite", `${base}  ->  ${newBase}${epNote}`, { newBase, newEps, oldBase: base });
}

// 人类可读计划 -> stderr
const w = (s) => process.stderr.write(s + "\n");
const order = ["rewrite","reflag","restore","skip-protected","skip-exempt","skip-archived","skip-deleted","error-empty-url","error-websocket","error-invalid-envelope","error-not-prefixed"];
w("");
w(isRestore ? "回滚计划" : "改写计划");
w("");
for (const a of order) {
  const g = rows.filter((r) => r.action === a);
  if (!g.length) continue;
  w(`  ${a} (${g.length})`);
  for (const r of g) w(`    #${r.id} ${r.name}\n      ${r.detail}`);
  w("");
}
const errCount = rows.filter((r) => r.action.startsWith("error-")).length;
w(`合计: ${rows.length} 条，其中待写 ${actions.length} 条，阻塞错误 ${errCount} 条`);

// 机器可读 -> stdout
process.stdout.write(JSON.stringify({ actions, errCount, isRestore }));
JS
)" || exit 2

ERR_COUNT="$(P="$PLAN" js 'process.stdout.write(String(JSON.parse(process.env.P).errCount))' 2>/dev/null || echo 0)"
ACTION_COUNT="$(P="$PLAN" js 'process.stdout.write(String(JSON.parse(process.env.P).actions.length))' 2>/dev/null || echo 0)"

# ── 执行门禁 ──────────────────────────────────────────────────────────────────

if [[ "$ERR_COUNT" -gt 0 ]]; then
  printf '\n阻塞：存在 %s 条错误（空 URL / WebSocket / 未加前缀），请先在控制台修正后重跑。\n' "$ERR_COUNT" >&2
  exit 1
fi

if [[ -n "$RESTORE_IDS" && "$YES" != "1" ]]; then
  printf '\n回滚需要显式确认：请加 --yes。\n回滚会让这些渠道恢复明文直连，隐私防护随之消失，并须在 OPERATIONS-LOG.md 记录（BR-005）。\n' >&2
  exit 1
fi

# 削弱检测（--flags 非全集）必须限定范围并显式确认：影响的是隐私防护强度，
# 与回滚同级，因此同样要求 --yes 并提示记录 BR-005。
if [[ "$FLAGS_SET" == "1" && -n "$FLAGS" ]]; then
  OFF_NAMES=""
  for (( i=0; i<${#ALL_FLAG_LETTERS}; i++ )); do
    ch="${ALL_FLAG_LETTERS:$i:1}"
    [[ "$FLAGS" == *"$ch"* ]] || OFF_NAMES="${OFF_NAMES}${OFF_NAMES:+、}$(flag_cn "$ch")"
  done
  if [[ -z "$ONLY_IDS" || "$YES" != "1" ]]; then
    printf '\n削弱检测需要显式确认：--flags %s 会在这些渠道上关闭 %s。\n' "$FLAGS" "$OFF_NAMES" >&2
    printf '请用 --only <id,...> 限定范围，并加 --yes 确认；变更须在 OPERATIONS-LOG.md 记录（BR-005）。\n' >&2
    exit 1
  fi
fi

if [[ "$APPLY" != "1" ]]; then
  printf '\ndry-run 结束，未发出任何写操作。加 --apply 才会真正执行。\n' >&2
  exit 0
fi

[[ "$ACTION_COUNT" -gt 0 ]] || { printf '\n无需改动。\n' >&2; exit 0; }
[[ -n "$TOKEN" ]] || die "--apply 需要连接 axonhub（不能与 --plan-from-file 同用）"

# ── 逐条写入 ──────────────────────────────────────────────────────────────────

printf '\n开始执行 %s 条更新...\n' "$ACTION_COUNT" >&2
FAILED=0
for i in $(seq 0 $((ACTION_COUNT - 1))); do
  ITEM="$(P="$PLAN" I="$i" js 'process.stdout.write(JSON.stringify(JSON.parse(process.env.P).actions[Number(process.env.I)]))')"
  CID="$(X="$ITEM" js 'process.stdout.write(String(JSON.parse(process.env.X).id))')"
  CNAME="$(X="$ITEM" js 'process.stdout.write(String(JSON.parse(process.env.X).name))')"
  OLDB="$(X="$ITEM" js 'process.stdout.write(String(JSON.parse(process.env.X).oldBase))')"
  NEWB="$(X="$ITEM" js 'process.stdout.write(String(JSON.parse(process.env.X).newBase))')"
  BODY="$(X="$ITEM" js '
const a = JSON.parse(process.env.X);
const input = { baseURL: a.newBase };
const eps = (a.newEps || []).filter(e => e && e.apiFormat);
if (eps.length) input.endpoints = eps;
process.stdout.write(JSON.stringify({
  query: "mutation($id:ID!,$input:UpdateChannelInput!){updateChannel(id:$id,input:$input){id name baseURL}}",
  variables: { id: a.gid, input }
}));')"
  RESP="$(http_json POST "${ADMIN_URL}/admin/graphql" "$BODY" "$TOKEN")" || { printf '  #%s %s 请求失败\n' "$CID" "$CNAME" >&2; FAILED=$((FAILED+1)); continue; }
  if R="$RESP" js 'const r=JSON.parse(process.env.R);process.exit(r.errors?1:0)' 2>/dev/null; then
    printf '  #%s %s\n    %s\n    -> %s\n' "$CID" "$CNAME" "$OLDB" "$NEWB" >&2
  else
    printf '  #%s %s 失败: %s\n' "$CID" "$CNAME" "$(printf '%s' "$RESP" | head -c 200)" >&2
    FAILED=$((FAILED+1))
  fi
done

printf '\n完成：成功 %s 条，失败 %s 条。\n' "$((ACTION_COUNT - FAILED))" "$FAILED" >&2
printf '请立即在 deploy/privacy-redaction/OPERATIONS-LOG.md 追加一行记录本次变更（BR-005）。\n' >&2
[[ "$FAILED" -eq 0 ]] || exit 1
exit 0
