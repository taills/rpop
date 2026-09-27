import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildSimulatedPaths, formatTimestamp, healthTone } from './routeSimulatorPaths.js'

test('healthTone maps path/link/node health states to the shared UiStatusDot/UiTag semantic tones', () => {
  assert.equal(healthTone('up'), 'success')
  assert.equal(healthTone('healthy'), 'success')
  assert.equal(healthTone('dialing'), 'warn')
  assert.equal(healthTone('cooling'), 'warn')
  assert.equal(healthTone('down'), 'danger')
  assert.equal(healthTone('unknown'), 'neutral')
  assert.equal(healthTone(undefined), 'neutral')
})

test('formatTimestamp renders a local string and passes through empty/invalid values', () => {
  assert.equal(formatTimestamp(''), '')
  assert.equal(formatTimestamp(undefined), '')
  assert.equal(formatTimestamp('not-a-date'), 'not-a-date')
  const iso = '2099-01-01T00:00:00Z'
  assert.equal(formatTimestamp(iso), new Date(iso).toLocaleString())
})

test('buildSimulatedPaths returns null when the response has no candidate paths', () => {
  assert.equal(buildSimulatedPaths(null), null)
  assert.equal(buildSimulatedPaths({}), null)
  assert.equal(buildSimulatedPaths({ paths: [] }), null)
})

test('buildSimulatedPaths builds a view model with hop, link and selection detail', () => {
  const result = {
    selectedPath: 1,
    paths: [
      {
        index: 0, label: 'proxy:socks5-A > node2 > node3', status: 'cooling', until: '2099-01-01T00:00:00Z',
        selected: false, reason: '',
        hops: [
          { kind: 'node', id: 'node1', online: true },
          { kind: 'proxy', id: 'socks5-A' },
          { kind: 'node', id: 'node2', online: true, link: { status: 'up' } },
          { kind: 'node', id: 'node3', online: false, link: { status: 'down', downUntil: '2099-01-01T00:00:00Z', lastError: 'dial timeout' } },
        ],
      },
      { index: 1, label: 'node3', status: 'healthy', selected: true, reason: '第一条未冷却的路径', hops: [{ kind: 'node', id: 'node1', online: true }] },
    ],
  }
  const view = buildSimulatedPaths(result)
  assert.equal(view.selectedIndex, 1)
  assert.equal(view.paths.length, 2)

  const [first, second] = view.paths
  assert.equal(first.statusLabel, '冷却中')
  assert.equal(first.tone, 'warn')
  assert.equal(first.selected, false)
  assert.equal(first.until, new Date('2099-01-01T00:00:00Z').toLocaleString())
  assert.equal(first.hops.length, 4)

  const [entryHop, proxyHop, relayHop, exitHop] = first.hops
  assert.equal(entryHop.kind, 'node')
  assert.equal(entryHop.onlineTone, 'success')
  assert.equal(entryHop.link, undefined)
  assert.equal(proxyHop.kind, 'proxy')
  assert.equal(proxyHop.online, undefined)
  assert.equal(relayHop.link.tone, 'success')
  assert.equal(relayHop.link.label, '正常')
  assert.equal(exitHop.onlineTone, 'danger')
  assert.equal(exitHop.link.tone, 'danger')
  assert.equal(exitHop.link.label, '已断开')
  assert.equal(exitHop.link.downUntil, new Date('2099-01-01T00:00:00Z').toLocaleString())
  assert.equal(exitHop.link.lastError, 'dial timeout')

  assert.equal(second.selected, true)
  assert.equal(second.reason, '第一条未冷却的路径')
  assert.equal(second.tone, 'success')
})
