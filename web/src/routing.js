// Client-side helpers for site routes. Matching itself runs on the server (see /api/routes/simulate);
// the ordering, labels and checks here mirror internal/control/routing.go so the editor can explain rules offline.

export const HEADER_MODES = [
  { value: 'match', label: '值匹配' },
  { value: 'exists', label: '存在' },
  { value: 'absent', label: '不存在' },
]
const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const encoder = new TextEncoder()

export function emptyRoute(upstream = 0) {
  return { path: '', stripPrefix: false, upstream, headers: [] }
}

export function emptyHeaderCondition() {
  return { name: '', mode: 'match', values: [] }
}

// routeForEditing turns a stored route into the editor shape, where every header condition carries an explicit mode.
export function routeForEditing(route) {
  const headers = (route.headers || []).map(header => ({
    name: header.name || '',
    mode: header.absent ? 'absent' : (header.values || []).length ? 'match' : 'exists',
    values: [...(header.values || [])],
  }))
  return { path: route.path || '', stripPrefix: Boolean(route.stripPrefix), upstream: Number(route.upstream) || 0, headers }
}

// normalizeRoute produces the API shape: trimmed values, no editor-only fields, and options that do not apply removed.
export function normalizeRoute(route) {
  const path = (route.path || '').trim()
  const out = {}
  if (path) out.path = path
  const headers = (route.headers || []).map(normalizeHeader)
  if (headers.length) out.headers = headers
  if (path && route.stripPrefix) out.stripPrefix = true
  out.upstream = Number(route.upstream) || 0
  return out
}

function normalizeHeader(header) {
  const name = (header.name || '').trim()
  if (header.mode === 'absent') return { name, absent: true }
  const values = header.mode === 'match' ? (header.values || []).map(value => value.trim()).filter(Boolean) : []
  return values.length ? { name, values } : { name }
}

export function canonicalHeaderName(name) {
  const trimmed = (name || '').trim()
  if (!TOKEN.test(trimmed)) return trimmed
  return trimmed.toLowerCase().replace(/(^|-)([a-z])/g, (_, dash, letter) => dash + letter.toUpperCase())
}

export function routeLabel(route) {
  const headers = (route.headers || []).map(header => {
    const name = canonicalHeaderName(header.name)
    const values = header.mode === 'match' ? (header.values || []).map(value => value.trim()).filter(Boolean) : []
    if (header.mode === 'absent') return ` [!${name}]`
    return values.length ? ` [${name}: ${values.join('|')}]` : ` [${name}]`
  })
  return `${(route.path || '').trim() || '*'}${headers.join('')}`
}

function routePattern(route) {
  const pattern = (route.path || '').trim() || '/*'
  const prefix = pattern.endsWith('*')
  return { base: prefix ? pattern.slice(0, -1) : pattern, prefix }
}

// effectiveRouteOrder lists route indices in evaluation order: longer paths (UTF-8 bytes) first, exact before prefix,
// then more header conditions, then configured order.
export function effectiveRouteOrder(routes) {
  return routes
    .map((route, index) => { const { base, prefix } = routePattern(route); return { index, length: encoder.encode(base).length, prefix, headers: (route.headers || []).length } })
    .sort((a, b) => b.length - a.length || Number(a.prefix) - Number(b.prefix) || b.headers - a.headers || a.index - b.index)
    .map(item => item.index)
}

function pathProblem(path) {
  if (!path.startsWith('/')) return '路径必须以 / 开头'
  if (path.slice(0, -1).includes('*')) return '通配符 * 只能出现在路径末尾'
  if (/[\s?#\u007f]/.test(path)) return '路径不能包含空白、? 或 #'
  return ''
}

function headerProblem(headers) {
  const seen = new Set()
  for (const header of headers) {
    const name = (header.name || '').trim()
    if (!TOKEN.test(name)) return `Header 名称“${name}”无效`
    const canonical = canonicalHeaderName(name)
    if (seen.has(canonical)) return `Header “${canonical}” 重复`
    seen.add(canonical)
    if (header.mode === 'match' && !(header.values || []).some(value => value.trim())) return `Header “${canonical}” 请填写匹配值，或改为“存在”`
  }
  return ''
}

function routeProblem(route, upstreamCount) {
  const path = (route.path || '').trim()
  const headers = route.headers || []
  if (!path && !headers.length) return '请填写路径或至少一个 Header 条件'
  if (path && pathProblem(path)) return pathProblem(path)
  if (!path && route.stripPrefix) return '“去除匹配前缀”需要填写路径'
  const upstream = Number(route.upstream) || 0
  if (upstream < 0 || upstream >= upstreamCount) return '目标上游不存在'
  return headerProblem(headers)
}

function routeKey(route) {
  const { base, prefix } = routePattern(route)
  const headers = normalizeRoute(route).headers || []
  const parts = headers.map(header => `${canonicalHeaderName(header.name)}|${Boolean(header.absent)}|${[...(header.values || [])].sort().join('\u0000')}`).sort()
  return `${(base + (prefix ? '*' : '')).toLowerCase()}\u0001${parts.join('\u0001')}`
}

// validateRoutes mirrors the server checks and returns the first problem as a user-facing message, or ''.
export function validateRoutes(routes, upstreamCount) {
  const seen = new Map()
  for (const [index, route] of routes.entries()) {
    const problem = routeProblem(route, upstreamCount)
    if (problem) return `规则 #${index + 1}：${problem}`
    const key = routeKey(route)
    if (seen.has(key)) return `规则 #${index + 1}：与规则 #${seen.get(key) + 1} 重复`
    seen.set(key, index)
  }
  return ''
}

export function remapRoutesAfterRemoval(routes, removed) {
  const kept = routes.filter(route => Number(route.upstream) !== removed)
  return {
    routes: kept.map(route => Number(route.upstream) > removed ? { ...route, upstream: Number(route.upstream) - 1 } : route),
    dropped: routes.length - kept.length,
  }
}

// remapRoutesForDefault keeps routes pointing at the same upstreams after upstream `index` moves to the front.
export function remapRoutesForDefault(routes, index) {
  return routes.map(route => {
    const upstream = Number(route.upstream) || 0
    if (upstream === index) return { ...route, upstream: 0 }
    return upstream < index ? { ...route, upstream: upstream + 1 } : route
  })
}

export function parseHeaderLines(text) {
  const headers = []
  for (const [index, line] of (text || '').split('\n').entries()) {
    if (!line.trim()) continue
    const colon = line.indexOf(':')
    if (colon < 0) return { headers, error: `第 ${index + 1} 行需要使用“名称: 值”格式` }
    const name = line.slice(0, colon).trim()
    if (!TOKEN.test(name)) return { headers, error: `第 ${index + 1} 行的 Header 名称无效` }
    headers.push({ name, value: line.slice(colon + 1).trim() })
  }
  return { headers, error: '' }
}
