// Pure helpers for RouteSimulator's "全路径" section: turning /api/routes/simulate's optional paths/
// selectedPath fields (docs/architecture/control-data-plane.md §5, "API 契约(6.5 定形)") into a view model ready
// to render, with health states mapped to the tones UiStatusDot/UiTag already use elsewhere in the console.

const PATH_STATUS_LABEL = { healthy: '健康', cooling: '冷却中', unknown: '未知' }
const LINK_STATUS_LABEL = { up: '正常', dialing: '拨号中', down: '已断开', unknown: '未知' }

// healthTone maps a path/link health state to a shared semantic tone: up/healthy=success, dialing/cooling=warn,
// down=danger, anything else (including unknown/undefined)=neutral.
export function healthTone(status) {
  if (status === 'up' || status === 'healthy') return 'success'
  if (status === 'dialing' || status === 'cooling') return 'warn'
  if (status === 'down') return 'danger'
  return 'neutral'
}

// formatTimestamp renders an RFC3339 instant with the browser's locale, same convention as the rest of the
// console (e.g. KeyedCertificateManager's formatDate); empty stays empty, and a value that doesn't parse as a
// date is passed through as-is rather than showing "Invalid Date".
export function formatTimestamp(value) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function hopViewModel(hop, index) {
  const model = { key: `${hop.kind}-${hop.id || index}-${index}`, kind: hop.kind, id: hop.id }
  if (hop.kind === 'node') {
    model.online = hop.online
    model.onlineTone = hop.online === false ? 'danger' : hop.online ? 'success' : 'neutral'
  }
  if (hop.link) {
    model.link = {
      status: hop.link.status,
      label: LINK_STATUS_LABEL[hop.link.status] || hop.link.status,
      tone: healthTone(hop.link.status),
      downUntil: formatTimestamp(hop.link.downUntil),
      lastError: hop.link.lastError || '',
    }
  }
  return model
}

function pathViewModel(path) {
  return {
    index: path.index,
    label: path.label,
    selected: !!path.selected,
    reason: path.reason || '',
    status: path.status,
    statusLabel: PATH_STATUS_LABEL[path.status] || path.status,
    tone: healthTone(path.status),
    until: formatTimestamp(path.until),
    hops: (path.hops || []).map(hopViewModel),
  }
}

// buildSimulatedPaths turns a /api/routes/simulate result into the "全路径" section's view model, or null when
// the matched upstream has no candidate paths (an older response, or a direct upstream), so the caller can skip
// rendering the section entirely instead of showing an empty one.
export function buildSimulatedPaths(result) {
  if (!result || !Array.isArray(result.paths) || !result.paths.length) return null
  return { selectedIndex: result.selectedPath, paths: result.paths.map(pathViewModel) }
}
