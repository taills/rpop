import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  addHop, addPath, availableHopNodes, hopFromSelection, hopSelection, moveHop, movePath,
  normalizeUpstreamPaths, parsePathsError, pathLabel, pathsProblem, removeHop, removePath, setHop,
} from './pathsForm.js'

test('normalizeUpstreamPaths expands the legacy via shorthand into a single path', () => {
  const upstream = { url: 'http://a', via: [{ node: 'relay-1' }, { proxy: 'socks5-a' }] }
  const next = normalizeUpstreamPaths(upstream)
  assert.deepEqual(next.paths, [{ via: [{ node: 'relay-1' }, { proxy: 'socks5-a' }] }])
  assert.deepEqual(next.via, [])
})

test('normalizeUpstreamPaths leaves an upstream that already has paths alone', () => {
  const upstream = { url: 'http://a', paths: [{ via: [] }], via: [{ node: 'ignored' }] }
  const next = normalizeUpstreamPaths(upstream)
  assert.deepEqual(next.paths, [{ via: [] }])
  assert.deepEqual(next.via, [])
})

test('normalizeUpstreamPaths defaults an upstream with neither to an empty paths array', () => {
  assert.deepEqual(normalizeUpstreamPaths({ url: 'http://a' }).paths, [])
})

test('addPath appends a direct (empty via) path', () => {
  const next = addPath([{ via: [{ node: 'a' }] }])
  assert.equal(next.length, 2)
  assert.deepEqual(next[1], { via: [] })
})

test('removePath drops only the targeted path', () => {
  const paths = [{ via: [] }, { via: [{ node: 'a' }] }, { via: [{ node: 'b' }] }]
  assert.deepEqual(removePath(paths, 1), [{ via: [] }, { via: [{ node: 'b' }] }])
})

test('movePath swaps with the neighbor and clamps at the edges', () => {
  const paths = [{ via: [{ node: 'a' }] }, { via: [{ node: 'b' }] }, { via: [{ node: 'c' }] }]
  const moved = movePath(paths, 0, 1)
  assert.deepEqual(moved.map((p) => p.via[0].node), ['b', 'a', 'c'])
  assert.equal(movePath(paths, 0, -1), paths)
  assert.equal(movePath(paths, 2, 1), paths)
})

test('addHop, setHop and removeHop only change the targeted path', () => {
  let paths = [{ via: [] }, { via: [] }]
  paths = addHop(paths, 1)
  assert.deepEqual(paths[0].via, [])
  assert.deepEqual(paths[1].via, [{ node: '', proxy: '' }])
  paths = setHop(paths, 1, 0, { node: 'relay-1', proxy: '' })
  assert.deepEqual(paths[1].via, [{ node: 'relay-1', proxy: '' }])
  paths = removeHop(paths, 1, 0)
  assert.deepEqual(paths[1].via, [])
})

test('moveHop reorders hops within one path', () => {
  const paths = [{ via: [{ node: 'a', proxy: '' }, { node: '', proxy: 'p' }] }]
  const next = moveHop(paths, 0, 0, 1)
  assert.deepEqual(next[0].via, [{ node: '', proxy: 'p' }, { node: 'a', proxy: '' }])
})

test('hopSelection and hopFromSelection round-trip nodes and proxies', () => {
  assert.equal(hopSelection({ node: 'relay-1', proxy: '' }), 'node:relay-1')
  assert.equal(hopSelection({ node: '', proxy: 'socks5-a' }), 'proxy:socks5-a')
  assert.equal(hopSelection({ node: '', proxy: '' }), '')
  assert.deepEqual(hopFromSelection('node:relay-1'), { node: 'relay-1', proxy: '' })
  assert.deepEqual(hopFromSelection('proxy:socks5-a'), { node: '', proxy: 'socks5-a' })
  assert.deepEqual(hopFromSelection(''), { node: '', proxy: '' })
})

test('availableHopNodes excludes the embedded node and the site’s own placement', () => {
  const nodes = [{ id: 'local', embedded: true }, { id: 'relay-1' }, { id: 'relay-2' }]
  assert.deepEqual(availableHopNodes(nodes, ['relay-1']).map((n) => n.id), ['relay-2'])
})

test('pathLabel names hops from the node/proxy catalog, or "direct" for an empty path', () => {
  const catalog = { nodes: [{ id: 'relay-1', name: 'Relay 1' }], proxies: [{ id: 'socks5-a', name: 'SOCKS5 A' }] }
  assert.equal(pathLabel([], catalog), '直连')
  assert.equal(pathLabel([{ node: 'relay-1', proxy: '' }, { node: '', proxy: 'socks5-a' }], catalog), '节点 · Relay 1 → 代理 · SOCKS5 A')
  assert.equal(pathLabel([{ node: 'unknown-id', proxy: '' }], catalog), '节点 · unknown-id')
})

test('pathsProblem catches an empty hop before a round trip', () => {
  assert.equal(pathsProblem([{ via: [{ node: '', proxy: '' }] }]), '路径 #1 第 1 跳需要选择一个节点或一个代理')
  assert.equal(pathsProblem([{ via: [{ node: 'a', proxy: '' }] }]), '')
})

test('pathsProblem enforces the path and hop limits', () => {
  const tooManyPaths = Array.from({ length: 9 }, () => ({ via: [] }))
  assert.match(pathsProblem(tooManyPaths), /最多 8 条/)
  const tooManyHops = [{ via: Array.from({ length: 9 }, () => ({ node: 'a', proxy: '' })) }]
  assert.match(pathsProblem(tooManyHops), /最多 8 跳/)
})

test('parsePathsError locates the upstream, path and hop named by a server error', () => {
  assert.deepEqual(parsePathsError('upstreams[0].paths[1].via[2] must name exactly one node or proxy'), { upstreamIndex: 0, pathIndex: 1, hopIndex: 2 })
  assert.deepEqual(parsePathsError('upstreams[2].paths[0]: node "x" does not exist'), { upstreamIndex: 2, pathIndex: 0, hopIndex: -1 })
})

test('parsePathsError returns null for errors with no path location', () => {
  assert.equal(parsePathsError('upstreams[0] cannot combine proxyUrl with paths; register the proxy and add it to the path'), null)
  assert.equal(parsePathsError(''), null)
})
