// Pure helpers for the request trace timeline (stage 6.3, docs/architecture/control-data-plane.md §5 "阶段
// 6(控制台)设计"): UUID-shape validation shared with the controller's traceIDPattern, tunnel event ordering /
// hop grouping, and the per-hop duration bars TracePage.jsx renders. Also D28's clock skew badge/correction (阶段
// 7 第 6 步): each event from GET /api/logging/tunnels/{id} carries the reporting node's current clockSkewMillis
// (tunnelEventView), and applyClockSkew below optionally rewrites timestamps before the rest of this file's
// ordering/timing math ever sees them. Kept dependency-free and separate from the view so node --test can
// exercise the timeline math without a DOM.
import { formatClockSkew } from './nodeHealth.js'

export { formatClockSkew }

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

// sortTunnelEvents orders events by timestamp ascending. Node clocks may not agree exactly (see applyClockSkew
// below for the optional, opt-in correction), so timestamps are the only ordering signal available; ties break
// on stage progression (arrived < established < ended), then on the original array position, making the sort
// fully deterministic even when two hops report the very same instant.
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
    // clockSkewMillis/clockSkewStatus are the hop's reporting node's current skew and warn/ok status (D28): every
    // event of a hop shares the same values (tunnelEventView attaches them per node, not per event), so the
    // first event that has a clockSkewMillis is enough; null/"" if the node never reported one, same nil-means-
    // unknown convention as nodeView.
    const skewSource = hop.events.find((event) => typeof event.clockSkewMillis === 'number')
    return {
      nodeId: hop.nodeId,
      role: hop.role,
      offsetPercent: Math.min(offsetPercent, 100 - MIN_BAR_PERCENT),
      widthPercent,
      durationMs: hop.endMs - hop.startMs,
      clockSkewMillis: skewSource?.clockSkewMillis ?? null,
      clockSkewStatus: skewSource?.clockSkewStatus ?? '',
    }
  })
}

// CLOCK_SKEW_WARN_THRESHOLD_MILLIS mirrors control.DefaultClockSkewWarnThresholdMillis (D28) and is only a
// fallback: GET /api/logging/tunnels/{id} attaches each hop's own already-decided clockSkewStatus (computed by
// the controller's clockSkewView against its live, possibly operator-raised -clock-skew-warn-threshold — the
// stage 7 review's item 3), which isClockSkewWarn/anyClockSkewWarn honor whenever it is present. This constant
// only decides the outcome for a controller predating that field (an older build that sends clockSkewMillis with
// no clockSkewStatus at all), so an operator-raised threshold on an up-to-date controller is always reflected
// exactly; only talking to a stale controller falls back to this approximation.
const CLOCK_SKEW_WARN_THRESHOLD_MILLIS = 2000

// isClockSkewWarn trusts the controller's own clockSkewStatus ("warn"/"ok") when it sent one, and only falls
// back to comparing millis against CLOCK_SKEW_WARN_THRESHOLD_MILLIS when it did not (status is "" or undefined,
// e.g. an older controller, or millis itself is unknown).
export function isClockSkewWarn(millis, status) {
  if (status === 'warn') return true
  if (status === 'ok') return false
  return typeof millis === 'number' && Math.abs(millis) > CLOCK_SKEW_WARN_THRESHOLD_MILLIS
}

// anyClockSkewWarn drives the tunnel-level "跨节点时钟偏差较大" banner: true as soon as one hop's current skew
// is (or, absent a clockSkewStatus, looks like) a warning — see isClockSkewWarn.
export function anyClockSkewWarn(events) {
  return events.some((event) => isClockSkewWarn(event.clockSkewMillis, event.clockSkewStatus))
}

// applyClockSkew is the opt-in "按偏差校正显示" toggle's math (D28): correcting timestamp by the reporting node's
// own clockSkewMillis (timestamp − skew) before sortTunnelEvents/withRelativeTiming/hopDurationBars ever see it,
// so ordering, relative timing and the duration bars all reflect the correction consistently. A node with no
// known skew is treated as exactly 0 (no correction), matching formatClockSkew's "未知" display convention rather
// than silently dropping the event. Returns a new array of new event objects; the input is never mutated, and
// disabled (enabled=false) is a no-op that hands the same array straight back.
export function applyClockSkew(events, enabled) {
  if (!enabled) return events
  return events.map((event) => {
    const skew = typeof event.clockSkewMillis === 'number' ? event.clockSkewMillis : 0
    return { ...event, timestamp: new Date(Date.parse(event.timestamp) - skew).toISOString() }
  })
}

// tunnelSectionState collapses TracePage's several loading/error/empty booleans into the one state its "隧道
// 时间线" card actually renders on — a pure function so every branch (including the ones that are easy to get
// backwards, like "record failed to load" vs "record loaded but has no tunnel") has a unit test instead of only
// ever being exercised by clicking through the UI.
export function tunnelSectionState({ trackId, recordLoading, recordError, record, eventsLoading, eventsError, events }) {
  if (trackId) {
    if (recordLoading) return 'loading'
    // The record card above already explains a failed lookup; nothing meaningful to add here.
    if (recordError) return 'hidden'
    if (record && !record.tunnelId) return 'no-tunnel'
  }
  if (eventsLoading) return 'loading'
  if (eventsError) return 'error'
  if (events && events.length === 0) return 'empty'
  if (events && events.length > 0) return 'ready'
  return 'hidden'
}
