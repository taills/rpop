import { test } from 'node:test'
import assert from 'node:assert/strict'
import { nodeCardErrorText, nodeCardHasAnomaly, nodeCardMetrics, nodeMatchesQuery } from './nodeCard.js'
import { nodeNeedsAttention } from './nodeHealth.js'

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
    embedded: false, online: true, inSync: true, certGeneration: 3,
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
    embedded: false, online: true, inSync: true, certGeneration: 2,
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
  const embedded = { embedded: true, online: false, inSync: false, certGeneration: 0, links: [], paths: [], logs: null, protocolStatus: 'current', clockSkewStatus: 'ok' }
  const metrics = nodeCardMetrics(embedded)
  const byKey = Object.fromEntries(metrics.map((m) => [m.key, m.anomaly]))
  assert.equal(byKey.revision, false)
  assert.equal(byKey.cert, false)
  // embedded nodes are never "offline" either (see nodeHealth.js's NODE_ATTENTION_CHECKS.offline), so a
  // healthy-otherwise embedded node stays un-flagged at the card level too.
  assert.equal(nodeCardHasAnomaly(embedded), false)
})

// Regression for the UI review's MEDIUM finding: revision/cert used to turn the grid cell red for a plain,
// non-embedded node too (a node freshly published to, or not yet issued a certificate, is normal — not an
// anomaly). Only the cell's own warn/muted tag should reflect that state.
test('a non-embedded node never flags revision or cert either, even when unsynced or uncertified', () => {
  const node = { embedded: false, online: true, inSync: false, certGeneration: 0, links: [], paths: [], logs: null, protocolStatus: 'current', clockSkewStatus: 'ok' }
  const metrics = nodeCardMetrics(node)
  const byKey = Object.fromEntries(metrics.map((m) => [m.key, m.anomaly]))
  assert.equal(byKey.revision, false)
  assert.equal(byKey.cert, false)
  assert.equal(nodeCardHasAnomaly(node), false)
})

test('nodeCardMetrics flags an outdated protocol and a warn-level clock skew independently', () => {
  const node = {
    embedded: false, online: true, inSync: true, certGeneration: 1,
    links: [], paths: [], logs: null, protocolStatus: 'outdated', clockSkewStatus: 'warn',
  }
  const metrics = nodeCardMetrics(node)
  const byKey = Object.fromEntries(metrics.map((m) => [m.key, m.anomaly]))
  assert.equal(byKey.protocol, true)
  assert.equal(byKey.clockSkew, true)
  assert.equal(nodeCardHasAnomaly(node), true)
})

// This is the UI review's core requirement: the card's "needs attention" flag and the NodesPage banner's count
// must never disagree. nodeCardHasAnomaly is defined as nodeHealth.js's nodeNeedsAttention, so this is really a
// guard against someone reintroducing a second, drifting definition later.
test('nodeCardHasAnomaly agrees with nodeHealth.js nodeNeedsAttention across offline/link/relay/apply-error cases', () => {
  const base = { embedded: false, online: true, inSync: true, certGeneration: 1, links: [], paths: [], logs: null, protocolStatus: 'current', clockSkewStatus: 'ok' }
  const cases = [
    base,
    { ...base, online: false },
    { ...base, links: [{ status: 'down' }] },
    { ...base, paths: [{ paths: [{ status: 'cooling' }] }] },
    { ...base, logs: { lastUploadError: 'timeout' } },
    { ...base, relayError: 'port in use' },
    { ...base, errors: { 'site-a': 'bad config' } },
    { ...base, protocolStatus: 'outdated' },
    { ...base, clockSkewStatus: 'warn' },
    { ...base, inSync: false, certGeneration: 0 }, // still not an anomaly (see the tests above)
  ]
  for (const node of cases) {
    assert.equal(nodeCardHasAnomaly(node), nodeNeedsAttention(node), `mismatch for ${JSON.stringify(node)}`)
  }
})

test('nodeCardErrorText renders a relay bind failure and per-site apply failures, and is empty otherwise', () => {
  const clean = { relayError: '', errors: {} }
  assert.equal(nodeCardErrorText(clean), '')

  const relayOnly = { relayError: 'listen tcp :9000: address already in use', errors: {} }
  assert.equal(nodeCardErrorText(relayOnly), '中继绑定失败：listen tcp :9000: address already in use')

  const applyOnly = { relayError: '', errors: { 'site-a': 'bad config', 'site-b': 'upstream unreachable' } }
  assert.equal(nodeCardErrorText(applyOnly), 'site-a：bad config；site-b：upstream unreachable')

  const both = { relayError: 'port in use', errors: { 'site-a': 'bad config' } }
  assert.equal(nodeCardErrorText(both), '中继绑定失败：port in use；site-a：bad config')
})
