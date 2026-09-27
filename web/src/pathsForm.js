// pathsForm.js — pure helpers for editing an upstream's ordered candidate paths (store.UpstreamPath[]) and the
// node/proxy hop sequence each one dials through. Dependency-free so the list editing (add/remove/reorder),
// legacy `via` migration and server error mapping are unit-testable without React; see components/PathsEditor.jsx.

export const MAX_PATHS = 8
export const MAX_HOPS = 8

export function blankPath() { return { via: [] } }
export function blankHop() { return { node: '', proxy: '' } }

// normalizeUpstreamPaths expands the legacy `via` shorthand (store.Upstream.Via, a single path) into the explicit
// `paths` array the editor always works with, and clears `via` so saving never re-emits both fields (the server
// rejects an upstream that sets both). An upstream that already has `paths` is left alone.
export function normalizeUpstreamPaths(upstream) {
  if ((upstream.paths || []).length || !(upstream.via || []).length) return { ...upstream, paths: upstream.paths || [], via: [] }
  return { ...upstream, paths: [{ via: upstream.via }], via: [] }
}

export function addPath(paths) {
  return [...paths, blankPath()]
}
export function removePath(paths, index) {
  return paths.filter((_, i) => i !== index)
}
export function movePath(paths, index, delta) {
  return moveItem(paths, index, delta)
}

export function addHop(paths, pathIndex) {
  return updatePath(paths, pathIndex, (path) => ({ ...path, via: [...path.via, blankHop()] }))
}
export function removeHop(paths, pathIndex, hopIndex) {
  return updatePath(paths, pathIndex, (path) => ({ ...path, via: path.via.filter((_, i) => i !== hopIndex) }))
}
export function moveHop(paths, pathIndex, hopIndex, delta) {
  return updatePath(paths, pathIndex, (path) => ({ ...path, via: moveItem(path.via, hopIndex, delta) }))
}
export function setHop(paths, pathIndex, hopIndex, hop) {
  return updatePath(paths, pathIndex, (path) => ({ ...path, via: path.via.map((h, i) => (i === hopIndex ? hop : h)) }))
}

function updatePath(paths, index, transform) {
  return paths.map((path, i) => (i === index ? transform(path) : path))
}
function moveItem(list, index, delta) {
  const target = index + delta
  if (target < 0 || target >= list.length) return list
  const next = list.slice()
  ;[next[index], next[target]] = [next[target], next[index]]
  return next
}

// hopSelection/hopFromSelection convert a hop to and from the single <select> value the editor shows, e.g.
// "node:relay-1" or "proxy:socks5-a"; node and proxy ids never contain ':' (see nodeIDPattern/proxyIDPattern).
export function hopSelection(hop) {
  if (hop.proxy) return `proxy:${hop.proxy}`
  if (hop.node) return `node:${hop.node}`
  return ''
}
export function hopFromSelection(value) {
  if (value.startsWith('proxy:')) return { node: '', proxy: value.slice('proxy:'.length) }
  if (value.startsWith('node:')) return { node: value.slice('node:'.length), proxy: '' }
  return blankHop()
}

// hopNodeOptions classifies every catalog node for use as a path hop, given the ids of the nodes currently
// serving the site being edited (its live placement — see PlacementFields/siteForm.js's config.nodes, not
// necessarily what was last saved). Ineligible nodes stay in the list (with a `reason`) instead of being
// dropped, so PathsEditor can render them as disabled options instead of making them silently vanish.
export function hopNodeOptions(nodes, placementIds) {
  const placed = new Set(placementIds)
  return nodes.map((node) => ({ node, reason: hopNodeReason(node, placed) }))
}

// hopNodeReason mirrors, in priority order, the three ways control.validatePaths/validatePathReferences reject a
// node as a hop: the embedded node can never relay, a node serving the site cannot be passed through, and a node
// without a configured relayAddress cannot be dialed by other nodes yet. '' means the node is a valid hop.
function hopNodeReason(node, placed) {
  if (node.embedded) return '内嵌节点不能作为中继'
  if (placed.has(node.id)) return '该节点服务此站点，路径不能经过它'
  if (!node.relayAddress) return '未配置 relayAddress，无法作为中继'
  return ''
}

// hopNodeWarning reports why an already-saved hop is no longer usable — placement changed after the path was
// saved, the node lost its relayAddress, or the node was deleted — so PathsEditor can flag it even though a
// disabled option, once already selected, stays selectable. '' for a proxy hop or a still-valid node hop.
export function hopNodeWarning(hop, nodes, placementIds) {
  if (!hop.node) return ''
  const node = nodes.find((candidate) => candidate.id === hop.node)
  if (!node) return '该节点已不存在'
  return hopNodeReason(node, new Set(placementIds))
}

// pathLabel renders a path for display; unlike the controller's internal label it uses catalog names for
// readability and a Chinese "direct" for an empty hop list.
export function pathLabel(via, { nodes = [], proxies = [] } = {}) {
  if (!via.length) return '直连'
  return via.map((hop) => hop.proxy
    ? `代理 · ${proxies.find((p) => p.id === hop.proxy)?.name || hop.proxy}`
    : `节点 · ${nodes.find((n) => n.id === hop.node)?.name || hop.node}`).join(' → ')
}

// pathsProblem returns a client-side validation message before a round trip, mirroring the shape checks in the
// controller's control.validatePaths (a full duplicate-path check is left to the server's response).
export function pathsProblem(paths) {
  if (paths.length > MAX_PATHS) return `候选路径最多 ${MAX_PATHS} 条`
  for (const [index, path] of paths.entries()) {
    if (path.via.length > MAX_HOPS) return `路径 #${index + 1} 最多 ${MAX_HOPS} 跳`
    for (const [hopIndex, hop] of path.via.entries()) {
      if (Boolean(hop.node) === Boolean(hop.proxy)) return `路径 #${index + 1} 第 ${hopIndex + 1} 跳需要选择一个节点或一个代理`
    }
  }
  return ''
}

// parsePathsError extracts the upstream/path/hop indices named by a control.validatePaths error message (e.g.
// `upstreams[0].paths[1].via[2] must name exactly one node or proxy`), so the editor can show it next to the
// offending row instead of only in the form's general error banner. Returns null for errors with no such prefix
// (e.g. "upstreams[0] cannot combine proxyUrl with paths"), which stay in the general banner.
const PATH_ERROR = /^upstreams\[(\d+)\]\.paths\[(\d+)\](?:\.via\[(\d+)\])?/

export function parsePathsError(message) {
  const match = PATH_ERROR.exec(message || '')
  if (!match) return null
  return { upstreamIndex: Number(match[1]), pathIndex: Number(match[2]), hopIndex: match[3] === undefined ? -1 : Number(match[3]) }
}
