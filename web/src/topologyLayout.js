// topologyLayout.js — pure layered-graph layout for TopologyPage: turns GET /api/topology's flat nodes/links
// into deterministic SVG coordinates, hand-written (zero new dependencies, per docs/architecture/
// control-data-plane.md §5's "阶段 6(控制台)设计" front-end convention). No React and no DOM here so it is
// covered directly by node --test.

// ROLE_ORDER mirrors topologyRoleOrder in internal/control/topology.go: a request always reaches a node's entry
// role (if any) before its relay role, and its relay role (if any) before its exit role.
export const ROLE_ORDER = ['entry', 'relay', 'exit']
export const ROLE_LABELS = { entry: '入口', relay: '中继', exit: '出口', idle: '未分配角色' }

// primaryLayer picks one column for a node that holds more than one role. The API contract
// (control-data-plane.md §5, "阶段 6 第 1 步") leaves this undecided for the multi-role case, so this file fixes
// a deterministic rule: use the first role in the fixed entry -> relay -> exit pipeline order, i.e. the role a
// request passing through this node would reach first. roles is already returned pre-ordered by the API, so
// this is equivalent to roles[0], but is written against ROLE_ORDER directly so it does not depend on that
// upstream ordering being preserved. A node with no role at all (registered but currently idle: no site, no
// relay route, no direct-dial upstream) gets a trailing column of its own so it stays visible rather than
// silently vanishing from the graph.
export function primaryLayer(roles = []) {
  for (let i = 0; i < ROLE_ORDER.length; i++) {
    if (roles.includes(ROLE_ORDER[i])) return i
  }
  return ROLE_ORDER.length
}

export function layerLabel(layerIndex) {
  return ROLE_LABELS[ROLE_ORDER[layerIndex]] || ROLE_LABELS.idle
}

const DEFAULTS = { columnWidth: 220, rowHeight: 88, marginX: 70, marginY: 60 }

// layoutTopology places every node into its primaryLayer column, ordered within the column online-first then by
// id (both are already known from the API response, so this needs no follow-up request and never reorders
// itself between polls unless the underlying data actually changes). It never inspects links, so a link cycle
// (or a self-link) cannot make it recurse or loop: layering is a pure function of each node's own roles.
export function layoutTopology(nodes = [], options = {}) {
  const { columnWidth, rowHeight, marginX, marginY } = { ...DEFAULTS, ...options }

  const columns = new Map()
  for (const node of nodes) {
    const layer = primaryLayer(node.roles)
    if (!columns.has(layer)) columns.set(layer, [])
    columns.get(layer).push(node)
  }
  for (const list of columns.values()) {
    list.sort((a, b) => Number(b.online) - Number(a.online) || String(a.id).localeCompare(String(b.id)))
  }

  const layers = [...columns.keys()].sort((a, b) => a - b)
  const maxRows = layers.reduce((max, layer) => Math.max(max, columns.get(layer).length), 1)
  const columnHeight = maxRows * rowHeight

  const positioned = []
  const byId = new Map()
  for (const layer of layers) {
    const list = columns.get(layer)
    const offsetY = marginY + (columnHeight - list.length * rowHeight) / 2
    list.forEach((node, row) => {
      const placed = { ...node, layer, x: marginX + layer * columnWidth, y: offsetY + row * rowHeight + rowHeight / 2 }
      positioned.push(placed)
      byId.set(placed.id, placed)
    })
  }

  return {
    nodes: positioned,
    byId,
    columns: layers.map((layer) => ({ layer, label: layerLabel(layer), x: marginX + layer * columnWidth })),
    width: marginX * 2 + Math.max(0, layers.length - 1) * columnWidth,
    height: marginY * 2 + columnHeight,
  }
}

// groupEdges assigns each link a 0-based lane among every other link that shares the same *unordered* pair of
// node ids: A->B and B->A are drawn as the same visual line and must be spread apart from each other too, not
// only from a second A->B link with a different proxy chain (the API already keys links by the (from, to, proxy
// chain) triple, so distinct entries here are exactly the edges that need to be told apart). A self-link
// (from === to) is its own pair and simply accumulates lanes 0, 1, 2, ... for however many distinct proxy chains
// loop back to the same node.
export function groupEdges(links = []) {
  const laneCounts = new Map()
  return links.map((link) => {
    const pairKey = [String(link.from), String(link.to)].sort().join('\u0000')
    const lane = laneCounts.get(pairKey) || 0
    laneCounts.set(pairKey, lane + 1)
    return { ...link, pairKey, lane, selfLoop: link.from === link.to }
  })
}

// laneOffset spreads lane 0, 1, 2, 3, ... into 0, +1, -1, +2, -2, ... so a group of edges fans out symmetrically
// around the straight line between two nodes instead of accumulating on one side.
export function laneOffset(lane) {
  if (lane <= 0) return 0
  const magnitude = Math.ceil(lane / 2)
  return lane % 2 === 1 ? magnitude : -magnitude
}

const EDGE_STATUS_COLOR = {
  up: 'var(--accent-green)',
  dialing: 'var(--accent-orange)',
  down: 'var(--accent-red)',
  unknown: 'var(--border-strong)',
}

// edgeColor maps a topology link's health status to the same semantic palette UiStatusDot/UiTag use elsewhere
// in the console (success/warn/danger/neutral), see control-data-plane.md §5's front-end convention.
export function edgeColor(status) {
  return EDGE_STATUS_COLOR[status] || EDGE_STATUS_COLOR.unknown
}
