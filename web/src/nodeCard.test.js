import { test } from 'node:test'
import assert from 'node:assert/strict'
import { nodeCardHasAnomaly, nodeCardMetrics, nodeMatchesQuery } from './nodeCard.js'

test('nodeMatchesQuery matches the display name or the stable id, case-insensitively', () => {
  const node = { name: '边缘节点一', id: 'edge-1' }
  assert.equal(nodeMatchesQuery(node, ''), true)
  assert.equal(nodeMatchesQuery(node, '  '), true)
  assert.equal(nodeMatchesQuery(node, 'EDGE-1'), true)
  assert.equal(nodeMatchesQuery(node, '边缘'), true)
  assert.equal(nodeMatchesQuery(node, 'edge-2'), false)
})

test('nodeCardMetrics keeps METRIC_ORDER when nothing is anomalous', () => {
  const healthy = {
    embedded: false, inSync: true, certGeneration: 3,
    links: [{ status: 'up' }], paths: [{ paths: [{ status: 'healthy' }] }],
    logs: { pendingSegments: 0 }, protocolStatus: 'current', clockSkewStatus: 'ok',
  }
  const metrics = nodeCardMetrics(healthy)
  assert.deepEqual(metrics.map((m) => m.key), ['revision', 'cert', 'links', 'paths', 'logs', 'protocol', 'clockSkew'])
  assert.equal(metrics.every((m) => m.anomaly === false), true)
  assert.equal(nodeCardHasAnomaly(healthy), false)
})

test('nodeCardMetrics sorts anomalous metrics first but keeps their relative order stable', () => {
  const node = {
    embedded: false, inSync: true, certGeneration: 2,
    links: [{ status: 'down' }], paths: [{ paths: [{ status: 'healthy' }] }],
    logs: { quotaDroppedSegments: 5 }, protocolStatus: 'current', clockSkewStatus: 'ok',
  }
  const metrics = nodeCardMetrics(node)
  // links and logs are anomalous; both must sort before every non-anomalous metric, in their original
  // relative order (links before logs, exactly as METRIC_ORDER lists them).
  assert.deepEqual(metrics.slice(0, 2).map((m) => m.key), ['links', 'logs'])
  assert.equal(metrics.slice(2).some((m) => m.anomaly), false)
  assert.equal(nodeCardHasAnomaly(node), true)
})

test('embedded nodes never flag revision or cert, even when unsynced or uncertified', () => {
  const embedded = { embedded: true, inSync: false, certGeneration: 0, links: [], paths: [], logs: null, protocolStatus: 'current', clockSkewStatus: 'ok' }
  const metrics = nodeCardMetrics(embedded)
  const byKey = Object.fromEntries(metrics.map((m) => [m.key, m.anomaly]))
  assert.equal(byKey.revision, false)
  assert.equal(byKey.cert, false)
  assert.equal(nodeCardHasAnomaly(embedded), false)
})

test('nodeCardMetrics flags an outdated protocol and a warn-level clock skew independently', () => {
  const node = {
    embedded: false, inSync: true, certGeneration: 1,
    links: [], paths: [], logs: null, protocolStatus: 'outdated', clockSkewStatus: 'warn',
  }
  const metrics = nodeCardMetrics(node)
  const byKey = Object.fromEntries(metrics.map((m) => [m.key, m.anomaly]))
  assert.equal(byKey.protocol, true)
  assert.equal(byKey.clockSkew, true)
  assert.equal(nodeCardHasAnomaly(node), true)
})
