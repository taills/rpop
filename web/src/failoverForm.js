// failoverForm.js — pure helpers for editing an upstream's optional D19/D30 degradation overrides
// (store.Upstream.Failover): a per-upstream dial timeout, cooldown bounds, and the D19 active-probe switch.
// Leaving a field blank keeps the node's global default; see internal/dataplane/paths.go's pathFailoverConfig
// and internal/control/paths.go's validateFailover, which the ranges and ordering check below mirror.

export const FAILOVER_RANGE = {
  dialTimeoutMs: { min: 1000, max: 60_000 },
  minCooldownMs: { min: 1, max: 600_000 },
  maxCooldownMs: { min: 1, max: 600_000 },
}

const FAILOVER_LABEL = { dialTimeoutMs: '建连超时', minCooldownMs: '最小冷却时间', maxCooldownMs: '最大冷却时间' }

// blankFailover is the editor's "no overrides yet" shape: every field blank so its inputs render empty
// ("跟随全局默认") instead of a literal 0.
export function blankFailover() {
  return { dialTimeoutMs: '', minCooldownMs: '', maxCooldownMs: '', activeProbe: null }
}

// editingFailover fills in blanks for whichever fields a saved store.Upstream.Failover leaves unset, so
// reopening the editor shows its overrides and leaves everything else blank.
export function editingFailover(failover) {
  if (!failover) return blankFailover()
  return {
    dialTimeoutMs: numberOrBlank(failover.dialTimeoutMs),
    minCooldownMs: numberOrBlank(failover.minCooldownMs),
    maxCooldownMs: numberOrBlank(failover.maxCooldownMs),
    activeProbe: failover.activeProbe ?? null,
  }
}

function numberOrBlank(value) {
  return value === undefined || value === null ? '' : value
}

// hasFailoverOverride reports whether an upstream's Failover (in either the raw store shape or the editor's
// blank-string shape from editingFailover) actually overrides anything, so sectionsForUpstream can tell a
// configured override apart from the always-present blank shape prepareSiteForEditing normalizes it to.
export function hasFailoverOverride(failover) {
  if (!failover) return false
  const blank = (value) => value === undefined || value === null || value === ''
  return !blank(failover.dialTimeoutMs) || !blank(failover.minCooldownMs) || !blank(failover.maxCooldownMs) || typeof failover.activeProbe === 'boolean'
}

// sanitizeFailover drops blank fields for saving; an editor state with no override at all saves as undefined,
// so a site untouched by this feature round-trips identically (see siteForm.js's applySections, which calls
// this only while the failover section is switched on).
export function sanitizeFailover(failover) {
  if (!failover) return undefined
  const sanitized = {}
  for (const key of ['dialTimeoutMs', 'minCooldownMs', 'maxCooldownMs']) {
    if (failover[key] !== '' && failover[key] !== undefined && failover[key] !== null) sanitized[key] = Number(failover[key])
  }
  if (typeof failover.activeProbe === 'boolean') sanitized.activeProbe = failover.activeProbe
  return Object.keys(sanitized).length ? sanitized : undefined
}

// failoverProblem mirrors control.validateFailover's range and min<=max ordering checks client-side, before a
// round trip; the server remains the source of truth.
export function failoverProblem(failover) {
  if (!failover) return ''
  for (const key of ['dialTimeoutMs', 'minCooldownMs', 'maxCooldownMs']) {
    if (failover[key] === '' || failover[key] === undefined || failover[key] === null) continue
    const value = Number(failover[key])
    const { min, max } = FAILOVER_RANGE[key]
    if (!Number.isFinite(value) || value < min || value > max) return `${FAILOVER_LABEL[key]}必须在 ${min} 到 ${max} 毫秒之间`
  }
  const min = failover.minCooldownMs === '' ? null : Number(failover.minCooldownMs)
  const max = failover.maxCooldownMs === '' ? null : Number(failover.maxCooldownMs)
  if (min !== null && max !== null && min > max) return '最小冷却时间不能大于最大冷却时间'
  return ''
}

// parseFailoverError extracts the upstream index a control.validateFailover error names (e.g.
// `upstreams[0].failover.dialTimeoutMs must be between 1000 and 60000`), so the editor can show it next to the
// failover fields instead of only in the form's general error banner.
const FAILOVER_ERROR = /^upstreams\[(\d+)\]\.failover\b/

export function parseFailoverError(message) {
  const match = FAILOVER_ERROR.exec(message || '')
  return match ? { upstreamIndex: Number(match[1]) } : null
}
