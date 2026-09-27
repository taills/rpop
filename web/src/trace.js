// Pure helpers for the request trace timeline (stage 6.3, docs/architecture/control-data-plane.md §5 "阶段
// 6(控制台)设计"): UUID-shape validation shared with the controller's traceIDPattern, tunnel event ordering /
// hop grouping, and the per-hop duration bars TracePage.jsx renders. Kept dependency-free and separate from the
// view so node --test can exercise the timeline math without a DOM.

// Mirrors internal/control/logs_ingest.go's traceIDPattern exactly: shape-only (no UUID version/variant check),
// since the controller itself falls back to a full, unwindowed scan rather than rejecting an ID this generator
// never minted — the frontend should reject the same (and only the same) inputs the API would 400 on.
const UUID_PATTERN = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/

export function isValidTraceId(id) {
  return typeof id === 'string' && UUID_PATTERN.test(id)
}

const STAGE_ORDER = { arrived: 0, established: 1, ended: 2 }

function stageRank(stage) {
  return stage in STAGE_ORDER ? STAGE_ORDER[stage] : 99
}

// sortTunnelEvents orders events by timestamp ascending. Node clocks may not agree exactly (skew display is
// stage 7's job — this page just says so in a caption), so timestamps are the only ordering signal available;
// ties break on stage progression (arrived < established < ended), then on the original array position, making
// the sort fully deterministic even when two hops report the very same instant.
export function sortTunnelEvents(events) {
  return events
    .map((event, index) => ({ event, index }))
    .sort((a, b) => {
      const byTime = Date.parse(a.event.timestamp) - Date.parse(b.event.timestamp)
      if (byTime !== 0) return byTime
      const byStage = stageRank(a.event.stage) - stageRank(b.event.stage)
      if (byStage !== 0) return byStage
      return a.index - b.index
    })
    .map(({ event }) => event)
}

// withRelativeTiming augments each already-sorted event with relativeMs (elapsed since the first event) and
// deltaMs (elapsed since the previous event in the list), so the view never has to re-derive them.
export function withRelativeTiming(sortedEvents) {
  if (!sortedEvents.length) return []
  const firstMs = Date.parse(sortedEvents[0].timestamp)
  let previousMs = firstMs
  return sortedEvents.map((event) => {
    const ms = Date.parse(event.timestamp)
    const timed = { ...event, relativeMs: ms - firstMs, deltaMs: ms - previousMs }
    previousMs = ms
    return timed
  })
}

const ROLE_ORDER = { entry: 0, relay: 1, exit: 2 }

function roleRank(role) {
  return role in ROLE_ORDER ? ROLE_ORDER[role] : 1
}

// groupHops groups timed events (see withRelativeTiming) by nodeId — one hop per node — and orders the hops
// entry -> relay(s) -> exit using each event's own role field first; multiple relays (or an unknown role) break
// ties on the hop's earliest event timestamp, the best available signal for traversal order.
export function groupHops(timedEvents) {
  const byNode = new Map()
  for (const event of timedEvents) {
    let hop = byNode.get(event.nodeId)
    if (!hop) {
      hop = { nodeId: event.nodeId, role: event.role, events: [] }
      byNode.set(event.nodeId, hop)
    }
    hop.events.push(event)
  }
  const hops = [...byNode.values()].map((hop) => ({
    ...hop,
    startMs: Date.parse(hop.events[0].timestamp),
    endMs: Date.parse(hop.events[hop.events.length - 1].timestamp),
  }))
  hops.sort((a, b) => roleRank(a.role) - roleRank(b.role) || a.startMs - b.startMs)
  return hops
}

// MIN_BAR_PERCENT keeps a hop with no measurable duration (a single reported event, e.g. missing "ended",
// or several events sharing one timestamp) visible as a thin bar instead of collapsing to nothing.
const MIN_BAR_PERCENT = 3

// hopDurationBars lays out one proportional bar per hop (see groupHops) across the tunnel's total observed
// span — the earliest hop's start to the latest hop's end — for the per-hop duration visualization beside the
// vertical timeline. Every timestamp span in the whole tunnel collapsing to zero (a single hop, or every event
// sharing one instant) falls back to equal-width bars rather than dividing by zero.
export function hopDurationBars(hops) {
  if (!hops.length) return []
  const spanStart = Math.min(...hops.map((hop) => hop.startMs))
  const spanEnd = Math.max(...hops.map((hop) => hop.endMs))
  const totalMs = spanEnd - spanStart
  return hops.map((hop) => {
    const offsetPercent = totalMs > 0 ? ((hop.startMs - spanStart) / totalMs) * 100 : 0
    const rawWidthPercent = totalMs > 0 ? ((hop.endMs - hop.startMs) / totalMs) * 100 : 100 / hops.length
    const widthPercent = Math.min(Math.max(rawWidthPercent, MIN_BAR_PERCENT), 100)
    return {
      nodeId: hop.nodeId,
      role: hop.role,
      offsetPercent: Math.min(offsetPercent, 100 - MIN_BAR_PERCENT),
      widthPercent,
      durationMs: hop.endMs - hop.startMs,
    }
  })
}
