import { test } from 'node:test'
import assert from 'node:assert/strict'
import { groupHops, hopDurationBars, isValidTraceId, sortTunnelEvents, withRelativeTiming } from './trace.js'

test('isValidTraceId accepts only UUID-shaped strings, matching the controller pattern', () => {
  assert.equal(isValidTraceId('0190f3d1-9e2b-7c3a-8b1a-1234567890ab'), true)
  assert.equal(isValidTraceId('0190F3D1-9E2B-7C3A-8B1A-1234567890AB'), true)
  assert.equal(isValidTraceId(''), false)
  assert.equal(isValidTraceId('not-a-uuid'), false)
  assert.equal(isValidTraceId('0190f3d1-9e2b-7c3a-8b1a-1234567890ab-extra'), false)
  assert.equal(isValidTraceId(undefined), false)
  assert.equal(isValidTraceId(null), false)
})

test('sortTunnelEvents fixes out-of-order timestamps', () => {
  const events = [
    { nodeId: 'exit', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.030Z' },
    { nodeId: 'entry', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
    { nodeId: 'relay', role: 'relay', stage: 'arrived', timestamp: '2026-01-01T00:00:00.010Z' },
  ]
  assert.deepEqual(sortTunnelEvents(events).map((e) => e.nodeId), ['entry', 'relay', 'exit'])
})

test('sortTunnelEvents breaks identical timestamps by stage order, then original position', () => {
  const events = [
    { nodeId: 'a', stage: 'ended', timestamp: '2026-01-01T00:00:00.000Z' },
    { nodeId: 'a', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
    { nodeId: 'a', stage: 'established', timestamp: '2026-01-01T00:00:00.000Z' },
  ]
  assert.deepEqual(sortTunnelEvents(events).map((e) => e.stage), ['arrived', 'established', 'ended'])
})

test('withRelativeTiming computes elapsed-since-first and elapsed-since-previous', () => {
  const sorted = [
    { timestamp: '2026-01-01T00:00:00.000Z' },
    { timestamp: '2026-01-01T00:00:00.050Z' },
    { timestamp: '2026-01-01T00:00:00.120Z' },
  ]
  const timed = withRelativeTiming(sorted)
  assert.deepEqual(timed.map((e) => e.relativeMs), [0, 50, 120])
  assert.deepEqual(timed.map((e) => e.deltaMs), [0, 50, 70])
})

test('withRelativeTiming returns an empty array for no events', () => {
  assert.deepEqual(withRelativeTiming([]), [])
})

test('groupHops orders hops entry -> relay -> exit and keeps each hop\'s own events', () => {
  const timed = withRelativeTiming(
    sortTunnelEvents([
      { nodeId: 'exit-1', role: 'exit', stage: 'ended', timestamp: '2026-01-01T00:00:00.200Z' },
      { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.150Z' },
      { nodeId: 'entry-1', role: 'entry', stage: 'established', timestamp: '2026-01-01T00:00:00.010Z' },
      { nodeId: 'entry-1', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
      { nodeId: 'relay-1', role: 'relay', stage: 'arrived', timestamp: '2026-01-01T00:00:00.050Z' },
    ]),
  )
  const hops = groupHops(timed)
  assert.deepEqual(hops.map((h) => h.nodeId), ['entry-1', 'relay-1', 'exit-1'])
  assert.equal(hops[0].events.length, 2)
  assert.equal(hops[1].events.length, 1)
})

test('groupHops orders several relays by their earliest event when roles tie', () => {
  const timed = withRelativeTiming(
    sortTunnelEvents([
      { nodeId: 'relay-b', role: 'relay', stage: 'arrived', timestamp: '2026-01-01T00:00:00.080Z' },
      { nodeId: 'relay-a', role: 'relay', stage: 'arrived', timestamp: '2026-01-01T00:00:00.040Z' },
    ]),
  )
  assert.deepEqual(groupHops(timed).map((h) => h.nodeId), ['relay-a', 'relay-b'])
})

test('groupHops handles a single hop (direct exit, no relay)', () => {
  const timed = withRelativeTiming(
    sortTunnelEvents([
      { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
      { nodeId: 'exit-1', role: 'exit', stage: 'ended', timestamp: '2026-01-01T00:00:00.100Z' },
    ]),
  )
  const hops = groupHops(timed)
  assert.equal(hops.length, 1)
  assert.equal(hops[0].events.length, 2)
})

test('groupHops tolerates a hop missing its ended event', () => {
  const timed = withRelativeTiming(
    sortTunnelEvents([
      { nodeId: 'entry-1', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
      { nodeId: 'entry-1', role: 'entry', stage: 'established', timestamp: '2026-01-01T00:00:00.010Z' },
    ]),
  )
  const hops = groupHops(timed)
  assert.equal(hops.length, 1)
  assert.deepEqual(hops[0].events.map((e) => e.stage), ['arrived', 'established'])
})

test('hopDurationBars gives a full-width bar for a single hop', () => {
  const hops = groupHops(
    withRelativeTiming(
      sortTunnelEvents([
        { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'ended', timestamp: '2026-01-01T00:00:00.100Z' },
      ]),
    ),
  )
  const bars = hopDurationBars(hops)
  assert.equal(bars.length, 1)
  assert.equal(bars[0].offsetPercent, 0)
  assert.equal(bars[0].widthPercent, 100)
})

test('hopDurationBars splits proportionally across multiple hops', () => {
  const hops = groupHops(
    withRelativeTiming(
      sortTunnelEvents([
        { nodeId: 'entry-1', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
        { nodeId: 'entry-1', role: 'entry', stage: 'established', timestamp: '2026-01-01T00:00:00.020Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.020Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'ended', timestamp: '2026-01-01T00:00:00.100Z' },
      ]),
    ),
  )
  const bars = hopDurationBars(hops)
  assert.equal(bars[0].offsetPercent, 0)
  assert.equal(bars[0].widthPercent, 20)
  assert.equal(bars[1].offsetPercent, 20)
  assert.equal(bars[1].widthPercent, 80)
})

test('hopDurationBars falls back to equal widths when every timestamp is identical', () => {
  const hops = groupHops(
    withRelativeTiming(
      sortTunnelEvents([
        { nodeId: 'entry-1', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
      ]),
    ),
  )
  const bars = hopDurationBars(hops)
  assert.deepEqual(bars.map((b) => b.widthPercent), [50, 50])
  assert.deepEqual(bars.map((b) => b.offsetPercent), [0, 0])
})

test('hopDurationBars keeps a zero-duration hop visible with the minimum bar width', () => {
  const hops = groupHops(
    withRelativeTiming(
      sortTunnelEvents([
        { nodeId: 'entry-1', role: 'entry', stage: 'arrived', timestamp: '2026-01-01T00:00:00.000Z' },
        { nodeId: 'relay-1', role: 'relay', stage: 'arrived', timestamp: '2026-01-01T00:00:00.100Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'arrived', timestamp: '2026-01-01T00:00:00.200Z' },
        { nodeId: 'exit-1', role: 'exit', stage: 'ended', timestamp: '2026-01-01T00:00:00.200Z' },
      ]),
    ),
  )
  const bars = hopDurationBars(hops)
  const exitBar = bars.find((b) => b.nodeId === 'exit-1')
  assert.equal(exitBar.widthPercent, 3)
  assert.ok(exitBar.offsetPercent + exitBar.widthPercent <= 100)
})

test('hopDurationBars returns nothing for an empty hop list', () => {
  assert.deepEqual(hopDurationBars([]), [])
})
