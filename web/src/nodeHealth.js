// nodeHealth.js — pure helpers for summarizing a node's link/path/log health and formatting bytes and RFC3339
// timestamps. Kept framework-free so it is covered by node --test and shared between NodesPage and
// NodeDetailPage (see docs/architecture/control-data-plane.md §5, "阶段 6 第 4 步实施记录").

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB']

// formatBytes renders a byte count the way the console's other numbers are rendered: compact, base-1024, with
// just enough decimals to stay readable at every magnitude (0 at the KiB mark already has 0-4 significant
// digits' worth of headroom, so trimming decimals below 10 units keeps things tidy without hiding precision).
export function formatBytes(bytes) {
  const value = Number(bytes)
  if (!Number.isFinite(value) || value <= 0) return '0 B'
  let size = value
  let unit = 0
  while (size >= 1024 && unit < BYTE_UNITS.length - 1) {
    size /= 1024
    unit++
  }
  const digits = unit === 0 ? 0 : size < 10 ? 2 : size < 100 ? 1 : 0
  return `${size.toFixed(digits)} ${BYTE_UNITS[unit]}`
}

const RELATIVE_UNITS = [
  ['年', 365 * 24 * 3600],
  ['个月', 30 * 24 * 3600],
  ['天', 24 * 3600],
  ['小时', 3600],
  ['分钟', 60],
]

// relativeTime renders a coarse "N单位前/后" label; iso is the RFC3339 string convention used throughout the
// nodes/topology API (empty string means "not applicable", never the zero time literal — see nodeView.LastSeen
// and overlay.LinkStatus's doc comments), so an empty input returns '' rather than a bogus duration.
export function relativeTime(iso, now = new Date()) {
  if (!iso) return ''
  const then = new Date(iso)
  if (Number.isNaN(then.getTime())) return ''
  const diffSeconds = Math.round((now.getTime() - then.getTime()) / 1000)
  const past = diffSeconds >= 0
  const abs = Math.abs(diffSeconds)
  if (abs < 45) return past ? '刚刚' : '即将'
  for (const [label, secs] of RELATIVE_UNITS) {
    if (abs >= secs) return past ? `${Math.round(abs / secs)}${label}前` : `${Math.round(abs / secs)}${label}后`
  }
  return past ? '刚刚' : '即将'
}

function pad2(n) {
  return String(n).padStart(2, '0')
}

// absoluteTime renders the same timestamp as a local "YYYY-MM-DD HH:mm:ss" string, next to relativeTime's coarse
// label (see describeTime) so the console never shows only a fuzzy duration.
export function absoluteTime(iso) {
  if (!iso) return ''
  const then = new Date(iso)
  if (Number.isNaN(then.getTime())) return ''
  return `${then.getFullYear()}-${pad2(then.getMonth() + 1)}-${pad2(then.getDate())} ${pad2(then.getHours())}:${pad2(then.getMinutes())}:${pad2(then.getSeconds())}`
}

// describeTime is the single entry point views should use for an optional RFC3339 field: null means "field is
// empty, render an em dash", otherwise both renderings are ready to display together.
export function describeTime(iso, now = new Date()) {
  if (!iso) return null
  const relative = relativeTime(iso, now)
  const absolute = absoluteTime(iso)
  if (!relative || !absolute) return null
  return { relative, absolute }
}

const KNOWN_LINK_STATUSES = ['up', 'dialing', 'down']

// summarizeLinks counts a node's reported overlay links by status (D19's three-state machine), for the list
// page's compact badges; anything outside the known set (there is no such value today, but the node is a
// half-trusted reporter — see status_sanitize.go) is folded into "unknown" rather than thrown away.
export function summarizeLinks(links = []) {
  const summary = { total: links.length, up: 0, dialing: 0, down: 0, unknown: 0 }
  for (const link of links) {
    if (KNOWN_LINK_STATUSES.includes(link?.status)) summary[link.status]++
    else summary.unknown++
  }
  return summary
}

export function linksNeedAttention(links = []) {
  return links.some((link) => link?.status === 'down')
}

// summarizePaths counts an upstream's candidate paths that are currently cooling down (D18/D19/D20's failover),
// across every upstream the node currently runs.
export function summarizePaths(paths = []) {
  let totalPaths = 0
  let coolingPaths = 0
  let coolingUpstreams = 0
  for (const upstream of paths) {
    const list = upstream?.paths || []
    totalPaths += list.length
    const cooling = list.filter((p) => p?.status === 'cooling').length
    coolingPaths += cooling
    if (cooling > 0) coolingUpstreams++
  }
  return { totalUpstreams: paths.length, totalPaths, coolingPaths, coolingUpstreams }
}

export function pathsNeedAttention(paths = []) {
  return summarizePaths(paths).coolingPaths > 0
}

// logsNeedAttention flags a node whose spool (D23-D25) is currently unhealthy: it has dropped segments to stay
// under quota, or its most recent upload attempt failed. Queue drops alone are not flagged here because they
// are a normal, expected consequence of a slow disk under load (P8) rather than data loss.
export function logsNeedAttention(logs) {
  if (!logs) return false
  return Boolean(logs.lastUploadError) || Number(logs.quotaDroppedSegments) > 0
}

// nodeNeedsAttention decides whether a node's row should be flagged in the list: offline (embedded nodes are
// always "online" by definition, see localNodeView), a down link, a cooling path, an unhealthy spool, a relay
// port that failed to bind, or a site that failed to apply.
export function nodeNeedsAttention(node) {
  if (!node.embedded && !node.online) return true
  if (linksNeedAttention(node.links)) return true
  if (pathsNeedAttention(node.paths)) return true
  if (logsNeedAttention(node.logs)) return true
  if (node.relayError) return true
  if (node.errors && Object.keys(node.errors).length > 0) return true
  return false
}

const LINK_STATUS_TONE = { up: 'success', dialing: 'warn', down: 'danger' }
const PATH_STATUS_TONE = { healthy: 'success', cooling: 'warn' }

export function linkStatusTone(status) {
  return LINK_STATUS_TONE[status] || 'neutral'
}

export function pathStatusTone(status) {
  return PATH_STATUS_TONE[status] || 'neutral'
}

const LINK_STATUS_LABEL = { up: '正常', dialing: '拨号中', down: '故障' }
const PATH_STATUS_LABEL = { healthy: '正常', cooling: '冷却中' }

export function linkStatusLabel(status) {
  return LINK_STATUS_LABEL[status] || status || '未知'
}

export function pathStatusLabel(status) {
  return PATH_STATUS_LABEL[status] || status || '未知'
}
