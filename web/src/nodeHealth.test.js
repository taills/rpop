import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  absoluteTime,
  clockSkewStatusLabel,
  clockSkewStatusTone,
  describeTime,
  formatBytes,
  formatClockSkew,
  linkStatusTone,
  linksNeedAttention,
  logsNeedAttention,
  nodeNeedsAttention,
  pathStatusTone,
  pathsNeedAttention,
  protocolStatusLabel,
  protocolStatusTone,
  relativeTime,
  summarizeLinks,
  summarizePaths,
} from './nodeHealth.js'

test('formatBytes scales to the largest unit that keeps the number readable', () => {
  assert.equal(formatBytes(0), '0 B')
  assert.equal(formatBytes(-5), '0 B')
  assert.equal(formatBytes(512), '512 B')
  assert.equal(formatBytes(1536), '1.50 KiB')
  assert.equal(formatBytes(15 * 1024 * 1024), '15.0 MiB')
  assert.equal(formatBytes(2 * 1024 ** 3), '2.00 GiB')
})

test('relativeTime and absoluteTime treat an empty RFC3339 field as "not applicable"', () => {
  assert.equal(relativeTime(''), '')
  assert.equal(absoluteTime(''), '')
  assert.equal(describeTime(''), null)
  assert.equal(describeTime(undefined), null)
})

test('relativeTime buckets a duration into the largest whole unit', () => {
  const now = new Date('2026-09-27T12:00:00Z')
  assert.equal(relativeTime('2026-09-27T11:59:30Z', now), '刚刚')
  assert.equal(relativeTime('2026-09-27T11:55:00Z', now), '5分钟前')
  assert.equal(relativeTime('2026-09-27T09:00:00Z', now), '3小时前')
  assert.equal(relativeTime('2026-09-25T12:00:00Z', now), '2天前')
  assert.equal(relativeTime('2026-09-27T12:05:00Z', now), '5分钟后')
})

test('describeTime pairs a relative label with an absolute local timestamp', () => {
  const now = new Date('2026-09-27T12:00:00Z')
  const described = describeTime('2026-09-27T11:00:00Z', now)
  assert.equal(described.relative, '1小时前')
  assert.equal(described.absolute, absoluteTime('2026-09-27T11:00:00Z'))
})

test('summarizeLinks counts every known status and folds the rest into unknown', () => {
  const links = [{ status: 'up' }, { status: 'up' }, { status: 'dialing' }, { status: 'down' }, { status: 'weird' }]
  assert.deepEqual(summarizeLinks(links), { total: 5, up: 2, dialing: 1, down: 1, unknown: 1 })
  assert.deepEqual(summarizeLinks(), { total: 0, up: 0, dialing: 0, down: 0, unknown: 0 })
})

test('linksNeedAttention only fires on a down link', () => {
  assert.equal(linksNeedAttention([{ status: 'up' }, { status: 'dialing' }]), false)
  assert.equal(linksNeedAttention([{ status: 'up' }, { status: 'down' }]), true)
  assert.equal(linksNeedAttention(), false)
})

test('summarizePaths counts cooling paths and the upstreams that have at least one', () => {
  const paths = [
    { siteId: 'a', upstream: 'https://a', paths: [{ status: 'healthy' }, { status: 'cooling' }] },
    { siteId: 'a', upstream: 'https://b', paths: [{ status: 'healthy' }] },
    { siteId: 'b', upstream: 'https://c', paths: [{ status: 'cooling' }, { status: 'cooling' }] },
  ]
  assert.deepEqual(summarizePaths(paths), { totalUpstreams: 3, totalPaths: 5, coolingPaths: 3, coolingUpstreams: 2 })
  assert.equal(pathsNeedAttention(paths), true)
  assert.equal(pathsNeedAttention([]), false)
})

test('logsNeedAttention fires on a quota drop or the most recent upload error, not on queue drops alone', () => {
  assert.equal(logsNeedAttention(null), false)
  assert.equal(logsNeedAttention({ accessLogQueueDropped: 40 }), false)
  assert.equal(logsNeedAttention({ quotaDroppedSegments: 1 }), true)
  assert.equal(logsNeedAttention({ lastUploadError: 'timeout' }), true)
})

test('nodeNeedsAttention flags offline, unhealthy links/paths/logs, relay errors and apply errors', () => {
  assert.equal(nodeNeedsAttention({ online: true, links: [], paths: [], logs: null }), false)
  assert.equal(nodeNeedsAttention({ online: false, links: [], paths: [], logs: null }), true)
  assert.equal(nodeNeedsAttention({ embedded: true, online: false, links: [], paths: [], logs: null }), false)
  assert.equal(nodeNeedsAttention({ online: true, links: [{ status: 'down' }] }), true)
  assert.equal(nodeNeedsAttention({ online: true, paths: [{ paths: [{ status: 'cooling' }] }] }), true)
  assert.equal(nodeNeedsAttention({ online: true, logs: { lastUploadError: 'boom' } }), true)
  assert.equal(nodeNeedsAttention({ online: true, relayError: 'port in use' }), true)
  assert.equal(nodeNeedsAttention({ online: true, errors: { site1: 'bad config' } }), true)
})

test('nodeNeedsAttention also flags an outdated protocol version (D27) or a warn-level clock skew (D28)', () => {
  assert.equal(nodeNeedsAttention({ online: true, protocolStatus: 'current', clockSkewStatus: 'ok' }), false)
  assert.equal(nodeNeedsAttention({ online: true, protocolStatus: 'outdated', clockSkewStatus: 'ok' }), true)
  assert.equal(nodeNeedsAttention({ online: true, protocolStatus: 'current', clockSkewStatus: 'warn' }), true)
})

test('status tone mappers default to neutral for unrecognized values', () => {
  assert.equal(linkStatusTone('up'), 'success')
  assert.equal(linkStatusTone('dialing'), 'warn')
  assert.equal(linkStatusTone('down'), 'danger')
  assert.equal(linkStatusTone('unknown'), 'neutral')
  assert.equal(pathStatusTone('healthy'), 'success')
  assert.equal(pathStatusTone('cooling'), 'warn')
  assert.equal(pathStatusTone(''), 'neutral')
})

test('formatClockSkew signs and scales a skew, switching to seconds once big enough, and unknown renders 未知', () => {
  assert.equal(formatClockSkew(1200), '+1.2s')
  assert.equal(formatClockSkew(-350), '−350ms')
  assert.equal(formatClockSkew(0), '+0ms')
  assert.equal(formatClockSkew(-1000), '−1.0s')
  assert.equal(formatClockSkew(null), '未知')
  assert.equal(formatClockSkew(undefined), '未知')
})

test('clockSkewStatusTone/clockSkewStatusLabel cover ok/warn and fall back for empty ("never reported")', () => {
  assert.equal(clockSkewStatusTone('ok'), 'success')
  assert.equal(clockSkewStatusTone('warn'), 'warn')
  assert.equal(clockSkewStatusTone(''), 'muted')
  assert.equal(clockSkewStatusLabel('ok'), '正常')
  assert.equal(clockSkewStatusLabel('warn'), '偏差较大')
  assert.equal(clockSkewStatusLabel(''), '未知')
})

test('protocolStatusTone/protocolStatusLabel cover current/outdated and fall back for empty ("never reported")', () => {
  assert.equal(protocolStatusTone('current'), 'success')
  assert.equal(protocolStatusTone('outdated'), 'warn')
  assert.equal(protocolStatusTone(''), 'muted')
  assert.equal(protocolStatusLabel('current'), '当前')
  assert.equal(protocolStatusLabel('outdated'), '落后，建议升级节点')
  assert.equal(protocolStatusLabel(''), '未知')
})
