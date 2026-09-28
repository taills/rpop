import { test } from 'node:test'
import assert from 'node:assert/strict'
import { curveControlPoint, edgeColor, groupEdges, laneOffset, layerColor, layoutTopology, primaryLayer } from './topologyLayout.js'

test('primaryLayer picks the first pipeline role and falls back to a trailing idle layer', () => {
  assert.equal(primaryLayer(['entry']), 0)
  assert.equal(primaryLayer(['relay']), 1)
  assert.equal(primaryLayer(['exit']), 2)
  assert.equal(primaryLayer(['entry', 'exit']), 0, 'a multi-role node groups under its earliest pipeline role')
  assert.equal(primaryLayer(['relay', 'exit']), 1)
  assert.equal(primaryLayer([]), 3, 'a node with no role at all gets its own trailing column')
  assert.equal(primaryLayer(undefined), 3)
  assert.equal(primaryLayer(null), 3, 'a null roles value (e.g. a malformed API response) must not throw')
})

test('layoutTopology tolerates a node whose roles is null instead of an array', () => {
  // GET /api/topology is expected to always send an array (see internal/control/topology.go), but a node with
  // roles: null must not white-screen the whole topology page; it should just land in the trailing idle column
  // like a node with an empty roles array.
  const nodes = [
    { id: 'a', online: true, roles: null },
    { id: 'b', online: true, roles: ['entry'] },
  ]
  const layout = layoutTopology(nodes)
  assert.equal(layout.byId.get('a').layer, 3)
  assert.equal(layout.byId.get('b').layer, 0)
})

test('layoutTopology places each layer at a deterministic x and orders rows online-first then by id', () => {
  const nodes = [
    { id: 'zzz', online: false, roles: ['entry'] },
    { id: 'aaa', online: true, roles: ['entry'] },
    { id: 'relay1', online: true, roles: ['relay'] },
    { id: 'exit1', online: true, roles: ['exit'] },
    { id: 'idle1', online: true, roles: [] },
  ]
  const layout = layoutTopology(nodes, { columnWidth: 200, rowHeight: 80, marginX: 50, marginY: 40 })
  const byId = Object.fromEntries(layout.nodes.map((n) => [n.id, n]))

  assert.equal(byId.aaa.x, 50)
  assert.equal(byId.relay1.x, 250)
  assert.equal(byId.exit1.x, 450)
  assert.equal(byId.idle1.x, 650)
  assert.ok(byId.aaa.y < byId.zzz.y, 'the online entry node sorts before the offline one within its column')
  assert.equal(layout.columns.length, 4)
  assert.equal(layout.width, 50 * 2 + 3 * 200)
})

test('layoutTopology compacts columns when a pipeline role has no nodes, so idle-only nodes stay inside width', () => {
  // Regression test for a real bug found during the task 3 usability pass (2026-09-28): when every node is
  // role-less (no entry/relay/exit — e.g. every registered node is still unregistered/idle), all of them land
  // in the trailing idle layer (index 3), but only one column is actually drawn. Positioning that column at
  // x = marginX + 3 * columnWidth while `width` only accounts for 1 drawn column placed every node past the
  // SVG's right edge, clipping the whole diagram to blank. Columns must be compacted to the columns actually
  // drawn (0-based), not the raw ROLE_ORDER index, so idle-only data still renders inside `width`.
  const nodes = [
    { id: 'a', online: true, roles: [] },
    { id: 'b', online: false, roles: [] },
  ]
  const layout = layoutTopology(nodes, { columnWidth: 200, rowHeight: 80, marginX: 50, marginY: 40 })
  const byId = Object.fromEntries(layout.nodes.map((n) => [n.id, n]))
  assert.equal(layout.columns.length, 1)
  assert.equal(layout.width, 50 * 2, 'a single drawn column must not reserve space for the three unused entry/relay/exit slots before it')
  assert.equal(byId.a.x, 50, 'the lone column sits at the first slot, not at its raw ROLE_ORDER index (3)')
  assert.equal(byId.b.x, 50)
  assert.ok(byId.a.x + 24 <= layout.width, 'a node (even with its radius) must fit inside the reported width')
})

test('layoutTopology compacts columns when only entry and exit roles are present (no relay in between)', () => {
  // A narrower variant of the "idle-only" regression above: the gap is in the *middle* of the pipeline rather
  // than at the end. Entry (raw ROLE_ORDER index 0) and exit (raw index 2) are the only two populated layers,
  // so they must be drawn as two adjacent columns (0 and 1), not left 200px apart at their raw indices with an
  // empty relay-shaped gap between them.
  const nodes = [
    { id: 'e1', online: true, roles: ['entry'] },
    { id: 'x1', online: true, roles: ['exit'] },
  ]
  const layout = layoutTopology(nodes, { columnWidth: 200, rowHeight: 80, marginX: 50, marginY: 40 })
  const byId = Object.fromEntries(layout.nodes.map((n) => [n.id, n]))
  assert.equal(layout.columns.length, 2, 'entry and exit are the only two columns actually drawn')
  assert.deepEqual(layout.columns.map((c) => c.layer), [0, 2], 'each column still remembers its real ROLE_ORDER index for labeling/coloring, even though it is compacted for layout')
  assert.equal(byId.e1.x, 50)
  assert.equal(byId.x1.x, 250, 'exit is compacted into the second drawn slot (250), not positioned at its raw index (450)')
  assert.equal(layout.width, 50 * 2 + 1 * 200, 'width only reserves space for the 2 columns actually drawn, not the 3 raw entry/relay/exit slots')
  for (const node of layout.nodes) {
    assert.ok(node.x >= 0 && node.x <= layout.width, `${node.id}'s x (${node.x}) must fall inside the SVG's reported width (${layout.width})`)
  }
})

test('layoutTopology is a pure function of each node\'s own roles, so link cycles cannot affect it', () => {
  // Two nodes that relay to each other (a cycle) still each get a single, well-defined column from their roles
  // alone; layoutTopology never looks at links, so there is nothing here that could recurse.
  const nodes = [
    { id: 'a', online: true, roles: ['relay'] },
    { id: 'b', online: true, roles: ['relay'] },
  ]
  const layout = layoutTopology(nodes)
  assert.equal(layout.nodes.length, 2)
  assert.equal(layout.byId.get('a').layer, 1)
  assert.equal(layout.byId.get('b').layer, 1)
})

test('groupEdges spreads multiple edges between the same pair of nodes, including the reverse direction', () => {
  const links = [
    { from: 'a', to: 'b', proxies: [] },
    { from: 'a', to: 'b', proxies: ['socks5://p1'] },
    { from: 'b', to: 'a', proxies: [] },
  ]
  const grouped = groupEdges(links)
  assert.deepEqual(grouped.map((e) => e.lane), [0, 1, 2])
  assert.equal(grouped[0].pairKey, grouped[1].pairKey)
  assert.equal(grouped[1].pairKey, grouped[2].pairKey)
  assert.deepEqual(grouped.map((e) => e.selfLoop), [false, false, false])
})

test('groupEdges gives a self-loop its own lane sequence without touching other pairs', () => {
  const links = [
    { from: 'c', to: 'c', proxies: [] },
    { from: 'c', to: 'c', proxies: ['socks5://p1'] },
    { from: 'c', to: 'd', proxies: [] },
  ]
  const grouped = groupEdges(links)
  assert.deepEqual(grouped.map((e) => e.lane), [0, 1, 0])
  assert.equal(grouped[0].selfLoop, true)
  assert.equal(grouped[2].selfLoop, false)
})

test('laneOffset fans lanes out symmetrically around the straight line', () => {
  assert.deepEqual([0, 1, 2, 3, 4].map(laneOffset), [0, 1, -1, 2, -2])
})

test('edgeColor maps every known status and defaults unrecognized ones to the unknown color', () => {
  assert.equal(edgeColor('up'), edgeColor('up'))
  assert.notEqual(edgeColor('up'), edgeColor('down'))
  assert.notEqual(edgeColor('dialing'), edgeColor('down'))
  assert.equal(edgeColor('something-new'), edgeColor('unknown'))
})

test('layerColor gives every column a distinct, deterministic color and falls back for anything past idle', () => {
  const colors = [0, 1, 2, 3].map(layerColor)
  assert.equal(new Set(colors).size, 4, 'entry/relay/exit/idle are all visually distinct')
  assert.equal(layerColor(99), layerColor(3), 'an out-of-range layer reuses the idle color rather than crashing')
})

test('curveControlPoint offsets perpendicular to the line between two nodes, working for any orientation', () => {
  const horizontal = curveControlPoint({ x: 0, y: 0 }, { x: 100, y: 0 }, 1, 10)
  assert.deepEqual(horizontal, { x: 50, y: 10 })
  const opposite = curveControlPoint({ x: 0, y: 0 }, { x: 100, y: 0 }, -1, 10)
  assert.deepEqual(opposite, { x: 50, y: -10 })
  const vertical = curveControlPoint({ x: 0, y: 0 }, { x: 0, y: 100 }, 1, 10)
  assert.deepEqual(vertical, { x: -10, y: 50 })
  const zeroOffset = curveControlPoint({ x: 0, y: 0 }, { x: 100, y: 0 }, 0, 10)
  assert.deepEqual(zeroOffset, { x: 50, y: 0 }, 'lane 0 sits exactly on the straight line')
})
