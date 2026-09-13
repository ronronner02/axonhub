#!/usr/bin/env bash
# 信任边界核对（R-05 / R-07 / AE-07）
#
# 输出三段清单并判定：
#   受保护渠道  —— base_url 与所有 endpoints[].base_url 都经脱敏层
#   豁免清单    —— 带 redact-exempt tag 且未经脱敏层（AE-07 期望只含官方 openai）
#   违规清单    —— 其余任何情况；出现即非零退出
# 另外比对「渠道推导出的期望上游主机集合」与「redact 容器实际 REDACT_ALLOWED_HOSTS」，
# 不相等即判为漂移（KTD-5）。
#
# 用法：
#   bash check-trust-boundary.sh                          # 服务器上：自动读 psql + docker inspect
#   bash check-trust-boundary.sh --channels-file f.json --allowed-hosts a,b   # 离线/测试
#
# 退出码：0 通过；1 存在违规或漂移；2 用法或环境错误。
#
# 依赖：无需宿主机 jq / node。JSON 解析优先用 jq，其次宿主机 node，
# 最后回退到 redact 容器内的 node（服务器上必然存在）。

set -uo pipefail

PREFIX="${REDACT_PREFIX:-http://redact:8787/}"
EXEMPT_TAG="${REDACT_EXEMPT_TAG:-redact-exempt}"
PG_CONTAINER="${PG_CONTAINER:-axonhub-postgres}"
PG_USER="${PG_USER:-axonhub}"
PG_DB="${PG_DB:-axonhub}"
REDACT_CONTAINER="${REDACT_CONTAINER:-axonhub-redact}"

CHANNELS_FILE=""
ALLOWED_HOSTS=""
ALLOWED_HOSTS_SET=0
SKIP_HOST_CHECK=0

usage() {
  cat <<'EOF'
用法: check-trust-boundary.sh [选项]

选项:
  --channels-file <路径>   从 JSON 文件读取渠道数组，跳过 psql。
                           元素字段: id,name,type,status,base_url,tags,endpoints
  --allowed-hosts <列表>   逗号分隔的实际允许主机集合，跳过 docker inspect。
  --skip-host-check        只做渠道分类，不比对允许主机集合。
  -h, --help               显示本帮助。

环境变量:
  REDACT_PREFIX       脱敏层前缀，默认 http://redact:8787/
  REDACT_EXEMPT_TAG   豁免 tag，默认 redact-exempt
  PG_CONTAINER        postgres 容器名，默认 axonhub-postgres
  PG_USER / PG_DB     psql 用户与库，默认 axonhub / axonhub
  REDACT_CONTAINER    脱敏层容器名，默认 axonhub-redact

退出码: 0 通过 / 1 违规或漂移 / 2 用法或环境错误
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --channels-file) CHANNELS_FILE="${2:-}"; shift 2 ;;
    --allowed-hosts) ALLOWED_HOSTS="${2:-}"; ALLOWED_HOSTS_SET=1; SKIP_HOST_CHECK=0; shift 2 ;;
    --skip-host-check) SKIP_HOST_CHECK=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "未知参数: $1" >&2; usage >&2; exit 2 ;;
  esac
done

die() { printf '错误: %s\n' "$1" >&2; exit 2; }

# ── JSON 求值器选择 ───────────────────────────────────────────────────────────
# 统一接口: eval_json <channels-json-file> <allowed-hosts> <skip-host-check>
# 判定逻辑写在 node 里(嵌套结构用 jq 表达可读性差且易错)。

pick_node_runner() {
  if command -v node >/dev/null 2>&1; then
    echo "host"
  elif command -v docker >/dev/null 2>&1 && docker image inspect "${REDACT_IMAGE:-axonhub-redact:local}" >/dev/null 2>&1; then
    echo "image"
  elif command -v docker >/dev/null 2>&1 && docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$REDACT_CONTAINER"; then
    echo "container"
  else
    echo "none"
  fi
}

run_node() {
  # stdin = JS 源码; $1 = channels json 路径; 其余通过环境变量传递
  local runner="$1"; shift
  case "$runner" in
    host)
      node --input-type=module - ;;
    image)
      docker run --rm -i \
        -e CHANNELS_JSON -e ACTUAL_HOSTS -e SKIP_HOST_CHECK -e PREFIX -e EXEMPT_TAG \
        "${REDACT_IMAGE:-axonhub-redact:local}" node --input-type=module - ;;
    container)
      docker exec -i \
        -e CHANNELS_JSON -e ACTUAL_HOSTS -e SKIP_HOST_CHECK -e PREFIX -e EXEMPT_TAG \
        "$REDACT_CONTAINER" node --input-type=module - ;;
    *)
      die "找不到可用的 JSON 求值器（需要宿主机 node，或 redact 镜像/容器）" ;;
  esac
}

# ── 数据获取层 ────────────────────────────────────────────────────────────────

fetch_channels() {
  if [[ -n "$CHANNELS_FILE" ]]; then
    [[ -f "$CHANNELS_FILE" ]] || die "找不到 --channels-file 指定的文件: $CHANNELS_FILE"
    cat "$CHANNELS_FILE"
    return
  fi
  command -v docker >/dev/null 2>&1 || die "需要 docker 来读取渠道，或改用 --channels-file"
  docker ps --format '{{.Names}}' 2>/dev/null | grep -qx "$PG_CONTAINER" \
    || die "postgres 容器 ${PG_CONTAINER} 未运行，或改用 --channels-file"
  # 只读查询；聚合成单个 JSON 数组。排除 archived 由判定层负责（需要计数）。
  # deleted_at<>0 为 ent 软删除：GraphQL/控制台/路由都看不到，与归档同等对待，不参与判定。
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -At -c \
    "SELECT COALESCE(json_agg(row_to_json(c)), '[]'::json) FROM (
       SELECT id, name, type, status, base_url, tags, endpoints FROM channels
       WHERE COALESCE(deleted_at, 0) = 0 ORDER BY id
     ) c;" 2>/dev/null || die "psql 查询失败（检查容器名/用户/库名）"
}

fetch_allowed_hosts() {
  # 显式传了 --allowed-hosts（哪怕是空串）就用它；空集合本身就是要断言的开放代理条件。
  if [[ "$ALLOWED_HOSTS_SET" == "1" ]]; then printf '%s' "$ALLOWED_HOSTS"; return; fi
  command -v docker >/dev/null 2>&1 || die "需要 docker 读取容器环境，或改用 --allowed-hosts"
  docker inspect "$REDACT_CONTAINER" \
    --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null \
    | awk -F= '$1=="REDACT_ALLOWED_HOSTS" { print substr($0, length($1)+2); exit }'
}

# ── 主流程 ────────────────────────────────────────────────────────────────────

CHANNELS_JSON="$(fetch_channels)" || exit 2
[[ -n "$CHANNELS_JSON" ]] || die "渠道数据为空"

if [[ "$SKIP_HOST_CHECK" == "0" ]]; then
  ACTUAL_HOSTS="$(fetch_allowed_hosts)"
else
  ACTUAL_HOSTS=""
fi

RUNNER="$(pick_node_runner)"
[[ "$RUNNER" != "none" ]] || die "找不到可用的 JSON 求值器（需要宿主机 node，或 redact 镜像/容器）"

export CHANNELS_JSON ACTUAL_HOSTS SKIP_HOST_CHECK PREFIX EXEMPT_TAG

run_node "$RUNNER" <<'JS'
const PREFIX = process.env.PREFIX;
const EXEMPT_TAG = process.env.EXEMPT_TAG;
const skipHostCheck = process.env.SKIP_HOST_CHECK === "1";

let channels;
try { channels = JSON.parse(process.env.CHANNELS_JSON); }
catch (e) { console.error("渠道 JSON 解析失败: " + e.message); process.exit(2); }
if (!Array.isArray(channels)) { console.error("渠道数据不是数组"); process.exit(2); }

const norm = (v) => (typeof v === "string" ? v.trim() : "");
const tagsOf = (c) => {
  const t = c.tags;
  if (Array.isArray(t)) return t.map(norm);
  if (typeof t === "string" && t) { try { const p = JSON.parse(t); return Array.isArray(p) ? p.map(norm) : []; } catch { return []; } }
  return [];
};
const endpointsOf = (c) => {
  let e = c.endpoints;
  if (typeof e === "string" && e) { try { e = JSON.parse(e); } catch { return []; } }
  return Array.isArray(e) ? e : [];
};
// 一条渠道涉及的全部出站 URL：base_url 加上非空的 endpoints[].base_url
const pick = (o, ...ks) => { for (const k of ks) if (o && o[k] != null) return o[k]; return undefined; };
// 同时接受 psql 导出（base_url）与 GraphQL（baseURL）两种字段名。
const urlsOf = (c) => {
  const out = [{ label: "base_url", url: norm(pick(c, "base_url", "baseURL")) }];
  endpointsOf(c).forEach((ep, i) => {
    const u = norm(pick(ep, "base_url", "baseURL"));
    if (u) out.push({ label: `endpoints[${i}].base_url`, url: u });
  });
  return out;
};
const upstreamHost = (url) => {
  const d = url.indexOf("$");
  if (d < 0) return null;
  try { return new URL(url.slice(d + 1)).hostname.toLowerCase(); } catch { return null; }
};

const protectedList = [], exemptList = [], violations = [], notes = [];
const expectedHosts = new Set();

for (const c of channels) {
  const id = c.id, name = c.name, status = norm(c.status);
  if (status === "archived") continue;               // 归档渠道不参与判定
  // ent 软删除（deleted_at<>0）：GraphQL/控制台/路由都看不到，同归档处理。
  // psql 层已过滤，这里再过滤一次是为了让 --channels-file 离线输入也一致。
  if (c.deleted_at != null && Number(c.deleted_at) !== 0) continue;
  const tags = tagsOf(c);
  const isExemptTagged = tags.includes(EXEMPT_TAG);
  const urls = urlsOf(c);

  const ws = urls.find((u) => /^wss?:\/\//i.test(u.url));
  if (ws) { violations.push({ id, name, reason: `WebSocket 传输无法经脱敏层（${ws.label}=${ws.url}）` }); continue; }

  const empty = urls.find((u) => u.url === "");
  if (empty) { violations.push({ id, name, reason: "base_url 为空，需先写显式 URL 才能加封套前缀" }); continue; }

  const allPrefixed = urls.every((u) => u.url.startsWith(PREFIX));
  const anyPrefixed = urls.some((u) => u.url.startsWith(PREFIX));

  if (allPrefixed) {
    protectedList.push({ id, name, urls: urls.map((u) => u.url) });
    for (const u of urls) {
      const h = upstreamHost(u.url);
      if (h) expectedHosts.add(h);
      else violations.push({ id, name, reason: `已加前缀但 $ 之后不是合法上游 URL（${u.label}=${u.url}）` });
    }
    if (isExemptTagged) notes.push({ id, name, note: `带 ${EXEMPT_TAG} tag 但实际经脱敏层，tag 多余（不判违规）` });
    continue;
  }

  if (anyPrefixed) {
    const bad = urls.filter((u) => !u.url.startsWith(PREFIX)).map((u) => `${u.label}=${u.url}`).join(", ");
    violations.push({ id, name, reason: `部分出站 URL 绕过脱敏层：${bad}` });
    continue;
  }

  if (isExemptTagged) { exemptList.push({ id, name, urls: urls.map((u) => u.url) }); continue; }
  violations.push({ id, name, reason: `明文直连且无 ${EXEMPT_TAG} tag（${urls.map((u) => u.url).join(", ")}）` });
}

// ── 允许主机集合漂移 ──
let drift = null;
if (!skipHostCheck) {
  const actual = new Set(String(process.env.ACTUAL_HOSTS || "").split(",").map((s) => s.trim().toLowerCase()).filter(Boolean));
  const missing = [...expectedHosts].filter((h) => !actual.has(h));
  const extra = [...actual].filter((h) => !expectedHosts.has(h));
  if (missing.length || extra.length) drift = { missing, extra, expected: [...expectedHosts].sort(), actual: [...actual].sort() };
}

// ── 输出 ──
const line = (s) => console.log(s);
line("");
line(`受保护渠道 (${protectedList.length})`);
for (const c of protectedList) line(`  #${c.id} ${c.name}`);
line("");
line(`豁免清单 (${exemptList.length})  ← AE-07 期望只含官方 openai`);
if (!exemptList.length) line("  (空)");
for (const c of exemptList) line(`  #${c.id} ${c.name}  ${c.urls.join(", ")}`);
if (notes.length) {
  line("");
  line(`提示 (${notes.length})`);
  for (const n of notes) line(`  #${n.id} ${n.name}: ${n.note}`);
}
line("");
line(`违规清单 (${violations.length})`);
if (!violations.length) line("  (空)");
for (const v of violations) line(`  #${v.id} ${v.name}: ${v.reason}`);

if (!skipHostCheck) {
  line("");
  if (drift) {
    line("允许主机集合: 漂移");
    if (drift.missing.length) line(`  缺少（渠道需要但未放行）: ${drift.missing.join(", ")}`);
    if (drift.extra.length) line(`  多余（已放行但无渠道使用）: ${drift.extra.join(", ")}`);
    line(`  期望: ${drift.expected.join(", ") || "(空)"}`);
    line(`  实际: ${drift.actual.join(", ") || "(空)"}`);
  } else {
    line(`允许主机集合: 一致 (${[...expectedHosts].sort().join(", ") || "空"})`);
  }
} else {
  line("");
  line("允许主机集合: 已跳过 (--skip-host-check)");
}

line("");
if (violations.length || drift) { line("结论: 不通过"); process.exit(1); }
line("结论: 通过");
process.exit(0);
JS
