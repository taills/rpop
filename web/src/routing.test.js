import { test } from 'node:test'
import assert from 'node:assert/strict'
import { effectiveRouteOrder, normalizeRoute, parseHeaderLines, remapRoutesForDefault, remapRoutesAfterRemoval, routeForEditing, routeLabel, validateRoutes } from './routing.js'

test('effectiveRouteOrder mirrors the server specificity order', () => {
  const routes = [
    { path: '/*', upstream: 0 },
    { path: '/api/*', upstream: 1 },
    { path: '/api/v1/*', upstream: 2 },
    { path: '/api', upstream: 3 },
    { path: '', headers: [{ name: 'X-Canary', mode: 'exists', values: [] }], upstream: 0 },
    { path: '/api/*', headers: [{ name: 'X-Env', mode: 'match', values: ['beta'] }], upstream: 2 },
    { path: '/API*', upstream: 1 },
  ]
  assert.deepEqual(effectiveRouteOrder(routes), [2, 5, 1, 3, 6, 4, 0])
})

test('effectiveRouteOrder compares UTF-8 byte lengths like Go', () => {
  assert.deepEqual(effectiveRouteOrder([{ path: '/abcd/*' }, { path: '/文档/*' }]), [1, 0])
})

test('routeForEditing and normalizeRoute round-trip header modes', () => {
  const stored = { path: '/a/*', stripPrefix: true, upstream: 1, headers: [{ name: 'X-Env', values: ['a', 'b*'] }, { name: 'Authorization' }, { name: 'X-Debug', absent: true }] }
  const editing = routeForEditing(stored)
  assert.deepEqual(editing.headers.map(header => header.mode), ['match', 'exists', 'absent'])
  assert.deepEqual(normalizeRoute(editing), stored)
})

test('normalizeRoute trims input and drops options that do not apply', () => {
  const editing = { path: '  ', stripPrefix: true, upstream: '2', headers: [{ name: ' x-env ', mode: 'match', values: [' a ', '', 'b'] }, { name: 'X-Gone', mode: 'absent', values: ['ignored'] }] }
  assert.deepEqual(normalizeRoute(editing), { upstream: 2, headers: [{ name: 'x-env', values: ['a', 'b'] }, { name: 'X-Gone', absent: true }] })
})

test('routeLabel matches the server label format', () => {
  assert.equal(routeLabel({ path: '/a', headers: [{ name: 'x-env', mode: 'match', values: ['a', 'b*'] }, { name: 'authorization', mode: 'exists' }, { name: 'X-Debug', mode: 'absent' }] }), '/a [X-Env: a|b*] [Authorization] [!X-Debug]')
  assert.equal(routeLabel({ path: '', headers: [{ name: 'X', mode: 'exists' }] }), '* [X]')
})

test('validateRoutes reports the first problem with the rule number', () => {
  const header = (name, mode = 'exists', values = []) => ({ name, mode, values })
  const cases = [
    [[{ path: '', headers: [] }], '规则 #1：请填写路径或至少一个 Header 条件'],
    [[{ path: 'api/*' }], '规则 #1：路径必须以 / 开头'],
    [[{ path: '/a/*/b' }], '规则 #1：通配符 * 只能出现在路径末尾'],
    [[{ path: '/a b' }], '规则 #1：路径不能包含空白、? 或 #'],
    [[{ path: '', stripPrefix: true, headers: [header('X')] }], '规则 #1：“去除匹配前缀”需要填写路径'],
    [[{ path: '/a', upstream: 3 }], '规则 #1：目标上游不存在'],
    [[{ path: '/a', headers: [header('Bad Name')] }], '规则 #1：Header 名称“Bad Name”无效'],
    [[{ path: '/a', headers: [header('X'), header('x')] }], '规则 #1：Header “X” 重复'],
    [[{ path: '/a', headers: [header('X', 'match', ['', ' '])] }], '规则 #1：Header “X” 请填写匹配值，或改为“存在”'],
    [[{ path: '/ok' }, { path: '/A/*' }, { path: '/a/*', upstream: 1 }], '规则 #3：与规则 #2 重复'],
    [[{ path: '/*', headers: [header('X')] }, { path: '', headers: [header('x')] }], '规则 #2：与规则 #1 重复'],
  ]
  for (const [routes, message] of cases) assert.equal(validateRoutes(routes, 2), message)
  assert.equal(validateRoutes([{ path: '/a/*', headers: [header('X-Env', 'match', ['a'])] }, { path: '/a/*', upstream: 1 }], 2), '')
})

test('remapRoutesAfterRemoval drops routes to the removed upstream and shifts later ones', () => {
  const routes = [{ path: '/a', upstream: 0 }, { path: '/b', upstream: 1 }, { path: '/c', upstream: 2 }]
  assert.deepEqual(remapRoutesAfterRemoval(routes, 1), { routes: [{ path: '/a', upstream: 0 }, { path: '/c', upstream: 1 }], dropped: 1 })
})

test('remapRoutesForDefault follows the upstream moved to the front', () => {
  const routes = [{ path: '/a', upstream: 0 }, { path: '/b', upstream: 1 }, { path: '/c', upstream: 2 }]
  assert.deepEqual(remapRoutesForDefault(routes, 2).map(route => route.upstream), [1, 2, 0])
})

test('parseHeaderLines reads "Name: Value" lines', () => {
  assert.deepEqual(parseHeaderLines('X-Env: beta\n\n  Host:admin.example.com  \nX-Empty:'), { headers: [{ name: 'X-Env', value: 'beta' }, { name: 'Host', value: 'admin.example.com' }, { name: 'X-Empty', value: '' }], error: '' })
  assert.equal(parseHeaderLines('no colon').error, '第 1 行需要使用“名称: 值”格式')
  assert.equal(parseHeaderLines('Bad Name: 1').error, '第 1 行的 Header 名称无效')
})
