import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  admitSite, classifyAddresses, conflictMessage, hostnamesOverlap, isInternalHostname, isSharedPortError,
  placementNodeIds, relayListenAddress, siteSharedPortHints,
} from './sharedPortHints.js'

test('classifyAddresses treats every wildcard spelling as the same address', () => {
  assert.equal(classifyAddresses(':8080', '0.0.0.0:8080'), 'same')
  assert.equal(classifyAddresses(':8080', ':8080'), 'same')
  assert.equal(classifyAddresses('[::]:8080', ':8080'), 'same')
})

test('classifyAddresses is case-insensitive for hostnames but not for literal IPs', () => {
  assert.equal(classifyAddresses('Example.test:8080', 'example.test:8080'), 'same')
  assert.equal(classifyAddresses('127.0.0.1:8080', '127.0.0.1:8080'), 'same')
})

test('classifyAddresses flags a wildcard vs. specific address on the same port as conflicting', () => {
  assert.equal(classifyAddresses(':8080', '127.0.0.1:8080'), 'conflicting')
  assert.equal(classifyAddresses('127.0.0.1:8080', ':8080'), 'conflicting')
})

test('classifyAddresses reports distinct for different ports or unrelated hosts', () => {
  assert.equal(classifyAddresses('127.0.0.1:8080', '127.0.0.1:8081'), 'distinct')
  assert.equal(classifyAddresses('127.0.0.1:8080', '10.0.0.5:8080'), 'distinct')
})

test('classifyAddresses returns null instead of guessing at an address it cannot parse', () => {
  assert.equal(classifyAddresses('not-an-address', '127.0.0.1:8080'), null)
  // An unbracketed literal IPv6 address is ambiguous (which colon is the port separator?) — net.SplitHostPort
  // rejects it too, so this must stay null rather than silently misparse it.
  assert.equal(classifyAddresses('::1:8080', '127.0.0.1:8080'), null)
  assert.equal(classifyAddresses('127.0.0.1:notaport', '127.0.0.1:8080'), null)
})

test('conflictMessage matches sharedport.ConflictError\'s wording exactly', () => {
  assert.equal(
    conflictMessage(':8080', '站点 a', '127.0.0.1:8080', '控制台'),
    '站点 a(:8080)与控制台(127.0.0.1:8080)端口相同但绑定范围不同(通配地址与具体地址无法共用同一端口): 请把两者改成完全相同的地址以复用端口,或改用不同端口',
  )
})

test('isInternalHostname matches the controller name and any node name, case-insensitively', () => {
  assert.equal(isInternalHostname('controller.rpop'), true)
  assert.equal(isInternalHostname('CONTROLLER.RPOP'), true)
  assert.equal(isInternalHostname('edge-1.nodes.rpop'), true)
  assert.equal(isInternalHostname('*.nodes.rpop'), true)
  assert.equal(isInternalHostname('controller.rpop.example.com'), false)
  assert.equal(isInternalHostname('app.example.com'), false)
  assert.equal(isInternalHostname(''), false)
})

test('hostnamesOverlap matches exact names and wildcards in either direction', () => {
  assert.equal(hostnamesOverlap('a.test', 'a.test'), true)
  assert.equal(hostnamesOverlap('*.example.com', 'app.example.com'), true)
  assert.equal(hostnamesOverlap('app.example.com', '*.example.com'), true)
  assert.equal(hostnamesOverlap('*.example.com', '*.internal.example.com'), true)
  assert.equal(hostnamesOverlap('*.example.com', '*.other.com'), false)
  assert.equal(hostnamesOverlap('a.test', 'b.test'), false)
})

test('placementNodeIds defaults an empty placement to the embedded node', () => {
  assert.deepEqual(placementNodeIds([]), ['local'])
  assert.deepEqual(placementNodeIds(undefined), ['local'])
  assert.deepEqual(placementNodeIds(['edge-1']), ['edge-1'])
})

test('relayListenAddress derives the wildcard bind from a node\'s relayAddress', () => {
  assert.equal(relayListenAddress('203.0.113.5:9000'), ':9000')
  assert.equal(relayListenAddress(''), '')
  assert.equal(relayListenAddress('not-an-address'), '')
})

test('admitSite admits a lone site with no owner and no siblings', () => {
  assert.equal(admitSite('a', [], false, []), '')
})

test('admitSite requires a hostname when an owner is already on the address', () => {
  assert.equal(admitSite('a', [], true, []), '与其他站点或控制台/中继共用监听地址时必须配置 hostname')
})

test('admitSite requires a hostname when a sibling site is already on the address, even with its own hostname', () => {
  assert.equal(
    admitSite('a', [], false, [{ id: 'b', hostnames: ['b.test'] }]),
    '与其他站点或控制台/中继共用监听地址时必须配置 hostname',
  )
})

test('admitSite rejects joining a hostname-less sibling', () => {
  assert.equal(
    admitSite('a', ['x.test'], false, [{ id: 'b', hostnames: [] }]),
    '已有站点 b 未配置 hostname; 请先为它配置 hostname 再共用监听地址',
  )
})

test('admitSite rejects an overlapping hostname', () => {
  assert.equal(
    admitSite('a', ['x.test'], false, [{ id: 'b', hostnames: ['x.test'] }]),
    'hostname "x.test" 与站点 b 已使用的 hostname 重叠',
  )
})

test('admitSite admits disjoint hostnames alongside a hostname-carrying sibling', () => {
  assert.equal(admitSite('a', ['x.test'], false, [{ id: 'b', hostnames: ['y.test'] }]), '')
})

test('admitSite excludes the site\'s own id from its sibling set', () => {
  assert.equal(admitSite('a', [], true, [{ id: 'a', hostnames: [] }]), '与其他站点或控制台/中继共用监听地址时必须配置 hostname')
})

// --- siteSharedPortHints: each scenario mirrors one of internal/control/shared_port_validate_test.go's cases,
// down to reusing the same fixture addresses, so a passing test here is also evidence the two rule sets agree.

function bootstrapInfo(overrides = {}) {
  return { consoleAddr: '127.0.0.1:8080', consoleHostnames: null, southboundEnabled: false, southboundAddr: '', southboundPort: 0, ...overrides }
}

test('siteSharedPortHints flags a missing hostname on the console\'s own address (mirrors RejectsMissingHostnameOnConsolesAddress)', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 8080, hostnames: [], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.ok(hints.some((h) => h.kind === 'reuse' && h.text.includes('控制台')))
  assert.ok(hints.some((h) => h.kind === 'hostname-admission' && h.text === '放置节点 内嵌节点: 与其他站点或控制台/中继共用监听地址时必须配置 hostname'))
})

test('siteSharedPortHints flags a wildcard/specific scope conflict against the console (mirrors RejectsConflictingScope)', () => {
  const site = { id: 'a', config: { listenAddress: '', listenPort: 8080, hostnames: ['a.test'], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.deepEqual(
    hints.filter((h) => h.kind === 'address-conflict').map((h) => h.text),
    ['站点 a(:8080)与控制台(127.0.0.1:8080)端口相同但绑定范围不同(通配地址与具体地址无法共用同一端口): 请把两者改成完全相同的地址以复用端口,或改用不同端口'],
  )
})

test('siteSharedPortHints flags an internal reserved hostname (mirrors RejectsInternalHostname)', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 18080, hostnames: ['controller.rpop'], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.deepEqual(
    hints.filter((h) => h.kind === 'internal-hostname').map((h) => h.text),
    ['hostname "controller.rpop" 是内部保留名(控制器或节点专用),请改用其他 hostname'],
  )
})

test('siteSharedPortHints flags an overlap with -console-hostnames (mirrors RejectsConsoleHostnameOverlap)', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 8080, hostnames: ['console.test'], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleHostnames: ['console.test'] }), nodes: [], sites: [] })
  assert.deepEqual(
    hints.filter((h) => h.kind === 'console-hostname-overlap').map((h) => h.text),
    ['hostname "console.test" 与控制台 -console-hostnames 限定的 "console.test" 重叠,请改用不同的 hostname'],
  )
})

test('siteSharedPortHints flags a missing hostname on a node\'s relay port (mirrors RejectsMissingHostnameOnNodeRelayPort)', () => {
  const site = { id: 'a', config: { nodes: ['edge-1'], listenAddress: '', listenPort: 9000, tls: true, hostnames: [] } }
  const nodes = [{ id: 'edge-1', name: 'Edge 1', relayAddress: '203.0.113.5:9000' }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes, sites: [] })
  assert.ok(hints.some((h) => h.kind === 'reuse' && h.text.includes('节点 edge-1 的中继端口')))
  assert.ok(hints.some((h) => h.kind === 'hostname-admission' && h.text === '放置节点 edge-1: 与其他站点或控制台/中继共用监听地址时必须配置 hostname'))
})

test('siteSharedPortHints flags a hostname-less sibling sharing a node\'s relay port, but only once it is running', () => {
  const site = { id: 'a', config: { nodes: ['edge-1'], listenAddress: '', listenPort: 9000, tls: true, hostnames: ['a.test'] } }
  const nodes = [{ id: 'edge-1', relayAddress: '203.0.113.5:9000' }]
  const notRunning = [{ id: 'x', running: false, config: { nodes: ['edge-1'], listenAddress: '', listenPort: 9000, tls: true, hostnames: [] } }]
  assert.deepEqual(siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes, sites: notRunning }).filter((h) => h.kind === 'hostname-admission'), [])
  const running = [{ id: 'x', running: true, config: { nodes: ['edge-1'], listenAddress: '', listenPort: 9000, tls: true, hostnames: [] } }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes, sites: running })
  assert.ok(hints.some((h) => h.kind === 'hostname-admission' && h.text === '放置节点 edge-1: 已有站点 x 未配置 hostname; 请先为它配置 hostname 再共用监听地址'))
})

test('siteSharedPortHints flags an overlapping hostname against a sibling on the embedded node, but only once it is running', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: ['a.test'], tls: false, nodes: [] } }
  const notRunning = [{ id: 'X', running: false, config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: ['a.test'], tls: false, nodes: [] } }]
  assert.deepEqual(siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleAddr: '' }), nodes: [], sites: notRunning }), [])
  const running = [{ id: 'X', running: true, config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: ['a.test'], tls: false, nodes: [] } }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleAddr: '' }), nodes: [], sites: running })
  assert.deepEqual(
    hints.filter((h) => h.kind === 'hostname-admission').map((h) => h.text),
    ['放置节点 内嵌节点: hostname "a.test" 与站点 X 已使用的 hostname 重叠'],
  )
})

// --- running-sibling gate: mirrors internal/control/shared_port_validate_test.go's stage 3 review regressions
// (TestValidateSharedPortPlacementAllowsSavingAnUnstartedSibling*/IgnoresAnUnstartedSiblingWhenStarting) — a
// saved-but-not-running sibling is not a real collision surface, so this module must say nothing about it.

test('siteSharedPortHints ignores a not-running sibling with a conflicting wildcard/specific address scope', () => {
  const site = { id: 'a', config: { listenAddress: '', listenPort: 9090, hostnames: ['a.test'], tls: false, nodes: [] } }
  const sites = [{ id: 'b', running: false, config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: ['b.test'], tls: false, nodes: [] } }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleAddr: '' }), nodes: [], sites })
  assert.deepEqual(hints.filter((h) => h.kind === 'address-conflict'), [])
})

test('siteSharedPortHints treats a sibling with running left undefined the same as not running', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: [], tls: false, nodes: [] } }
  const sites = [{ id: 'X', config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: [], tls: false, nodes: [] } }] // no `running` field at all
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleAddr: '' }), nodes: [], sites })
  assert.deepEqual(hints, [])
})

test('siteSharedPortHints says nothing about a TLS site colocated with a plaintext-only console (no southbound/relay there)', () => {
  // sharedport dispatches by first byte before either table is reached, so a TLS site does not share the
  // console's plaintext table and needs no hostname on that account alone.
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 8080, tls: true, hostnames: [], nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.deepEqual(hints, [])
})

test('siteSharedPortHints says nothing about a plaintext site colocated with a node\'s TLS-only relay port (the mirror image of the test above)', () => {
  // ownersForNode always marks a node's relay port `tls: true` (see its doc comment), so the same "dispatches
  // by first byte before either table is reached" rule as the TLS-site/plaintext-console case above applies in
  // reverse here: a plaintext site sharing the exact same address as a relay port never reaches that TLS table
  // either, and needs no hostname on that account alone. Same idea as
  // "flags a missing hostname on a node's relay port" above, but with the site's own tls flipped to false so it
  // no longer matches the owner's mode — matchingOwners ends up empty, so admitSite is never even asked.
  const site = { id: 'a', config: { nodes: ['edge-1'], listenAddress: '', listenPort: 9000, tls: false, hostnames: [] } }
  const nodes = [{ id: 'edge-1', relayAddress: '203.0.113.5:9000' }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes, sites: [] })
  assert.deepEqual(hints, [])
})

test('siteSharedPortHints shows only an informational reuse hint once a hostname is already set (mirrors AllowsLegitimateReuseToSaveAndStart)', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 8080, hostnames: ['shared.test'], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.equal(hints.length, 1)
  assert.equal(hints[0].severity, 'info')
  assert.equal(hints[0].kind, 'reuse')
})

test('siteSharedPortHints says nothing for addresses that share nothing', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 19090, hostnames: [], tls: false, nodes: [] } }
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes: [], sites: [] })
  assert.deepEqual(hints, [])
})

test('siteSharedPortHints excludes the site being edited from its own sibling checks', () => {
  const site = { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: 9090, hostnames: [], tls: false, nodes: [] } }
  // Same id, stale config: would otherwise look like a self-conflict (a specific address vs. this edit's, or a
  // hostname-less "sibling") if it were not excluded by id.
  const sites = [{ id: 'a', config: { listenAddress: '', listenPort: 9090, hostnames: [], tls: false, nodes: [] } }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo({ consoleAddr: '' }), nodes: [], sites })
  assert.deepEqual(hints, [])
})

test('siteSharedPortHints combines hints from every placement node', () => {
  const site = { id: 'a', config: { nodes: ['edge-1', 'edge-2'], listenAddress: '', listenPort: 9000, tls: true, hostnames: [] } }
  const nodes = [{ id: 'edge-1', relayAddress: '203.0.113.5:9000' }, { id: 'edge-2', relayAddress: '203.0.113.6:9000' }]
  const hints = siteSharedPortHints({ site, bootstrapInfo: bootstrapInfo(), nodes, sites: [] })
  const admissionNodes = hints.filter((h) => h.kind === 'hostname-admission').map((h) => h.text)
  assert.deepEqual(admissionNodes.sort(), [
    '放置节点 edge-1: 与其他站点或控制台/中继共用监听地址时必须配置 hostname',
    '放置节点 edge-2: 与其他站点或控制台/中继共用监听地址时必须配置 hostname',
  ])
})

test('siteSharedPortHints stays silent (not crashes) while the listen address/port is still being edited', () => {
  assert.deepEqual(siteSharedPortHints({ site: { id: 'a', config: { listenAddress: '', listenPort: '', hostnames: [], nodes: [] } }, bootstrapInfo: bootstrapInfo() }), [])
  assert.deepEqual(siteSharedPortHints({ site: { id: 'a', config: { listenAddress: '127.0.0.1', listenPort: undefined, hostnames: [], nodes: [] } }, bootstrapInfo: bootstrapInfo() }), [])
  // An internal-hostname hint does not depend on the address parsing successfully, so it still fires.
  const hints = siteSharedPortHints({ site: { id: 'a', config: { listenAddress: '', listenPort: '', hostnames: ['controller.rpop'], nodes: [] } }, bootstrapInfo: bootstrapInfo() })
  assert.deepEqual(hints.map((h) => h.kind), ['internal-hostname'])
})

test('siteSharedPortHints handles a missing site/bootstrapInfo gracefully', () => {
  assert.deepEqual(siteSharedPortHints({}), [])
  assert.deepEqual(siteSharedPortHints(), [])
})

// --- isSharedPortError: every marker below is a real server message, copied from
// internal/control/shared_port_validate.go, internal/sharedport (route.go/owner.go/hostmatch.go) and
// internal/dataplane/engine.go's own apply-time internal-hostname check.

test('isSharedPortError matches every shared-port rejection the backend can return', () => {
  const messages = [
    'hostname "controller.rpop" 是内部保留名(控制器或节点专用),请改用其他 hostname',
    '放置节点 内嵌节点: 与其他站点或控制台/中继共用监听地址时必须配置 hostname',
    '放置节点 edge-1: 已有站点 X 未配置 hostname; 请先为它配置 hostname 再共用监听地址',
    '放置节点 内嵌节点: hostname "a.test" 与站点 X 已使用的 hostname 重叠',
    'hostname "console.test" 与控制台 -console-hostnames 限定的 "console.test" 重叠,请改用不同的 hostname',
    '站点 a(:8080)与控制台(127.0.0.1:8080)端口相同但绑定范围不同(通配地址与具体地址无法共用同一端口): 请把两者改成完全相同的地址以复用端口,或改用不同端口',
    '站点 a 的 hostname "controller.rpop" 是内部保留名(控制器或节点专用),请改用其他 hostname', // dataplane.Engine.buildRuntime, apply time
    'invalid hostname "bad host"', // sharedport.NormalizeHostnames
  ]
  for (const message of messages) assert.equal(isSharedPortError(message), true, message)
})

test('isSharedPortError leaves unrelated errors for the general error banner', () => {
  assert.equal(isSharedPortError('上游 #1：请填写上游 URL'), false)
  assert.equal(isSharedPortError('invalid node id "??"'), false)
  assert.equal(isSharedPortError('site not found'), false)
  assert.equal(isSharedPortError(''), false)
  assert.equal(isSharedPortError(undefined), false)
})
