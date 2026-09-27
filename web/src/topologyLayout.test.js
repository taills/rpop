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
