import { test } from 'node:test'
import assert from 'node:assert/strict'
import { addUpstream, applySections, makeDefaultUpstream, prepareSiteForEditing, removeUpstream, sectionsForSite, validateSections } from './siteForm.js'

const site = (upstreams, routes = []) => ({ id: 's', name: 'S', config: { listenAddress: '127.0.0.1', listenPort: 8081, upstreams, routes, accessLog: {} } })

test('sections are tracked per upstream', () => {
  const sections = sectionsForSite(site([{ url: 'http://a', proxyUrl: 'socks5://p' }, { url: 'https://b', serverName: 'b.internal' }]))
  assert.equal(sections.upstreams.length, 2)
  assert.equal(sections.upstreams[0].proxy, true)
  assert.equal(sections.upstreams[1].serverName, true)
  assert.equal(sections.upstreams[1].proxy, false)
})

test('applySections normalizes every upstream and the routes', () => {
  const editing = prepareSiteForEditing(site(
    [{ url: 'http://a', proxyUrl: 'socks5://p', serverName: 'ignored' }, { url: 'https://b', serverName: 'b.internal', dialAddress: '10.0.0.1' }],
    [{ path: '/api/*', upstream: 1, headers: [{ name: 'X-Env', values: ['beta'] }] }],
  ))
  const sections = sectionsForSite(editing)
  const saved = applySections(editing, { ...sections, upstreams: [{ ...sections.upstreams[0], proxy: false }, sections.upstreams[1]] })
  assert.equal(saved.config.upstreams[0].proxyUrl, '')
  assert.equal(saved.config.upstreams[0].serverName, '')
  assert.equal(saved.config.upstreams[1].serverName, 'b.internal')
  assert.equal(saved.config.upstreams[1].dialAddress, '10.0.0.1')
  assert.deepEqual(saved.config.routes, [{ path: '/api/*', upstream: 1, headers: [{ name: 'X-Env', values: ['beta'] }] }])
})

test('validateSections names the upstream and checks routes', () => {
  const editing = prepareSiteForEditing(site([{ url: 'http://a' }, { url: 'http://b', dialAddress: '' }], [{ path: 'bad' }]))
  const sections = sectionsForSite(editing)
  const withDial = { ...sections, upstreams: [sections.upstreams[0], { ...sections.upstreams[1], dialAddress: true }] }
  assert.equal(validateSections(editing, withDial, {}), '上游 #2：已勾选“覆盖连接地址”，请填写连接地址')
  assert.equal(validateSections(editing, sections, {}), '规则 #1：路径必须以 / 开头')
})

test('removeUpstream keeps sections, files and routes aligned', () => {
  const editing = prepareSiteForEditing(site([{ url: 'http://a' }, { url: 'http://b' }, { url: 'http://c' }], [{ path: '/b', upstream: 1 }, { path: '/c', upstream: 2 }]))
  const state = { site: editing, sections: sectionsForSite(editing), files: [{}, { cert: 'b.pem' }, { cert: 'c.pem' }] }
  const { state: next, dropped } = removeUpstream(state, 1)
  assert.equal(dropped, 1)
  assert.deepEqual(next.site.config.upstreams.map(upstream => upstream.url), ['http://a', 'http://c'])
  assert.deepEqual(next.files, [{}, { cert: 'c.pem' }])
  assert.equal(next.sections.upstreams.length, 2)
  assert.deepEqual(next.site.config.routes.map(route => [route.path, route.upstream]), [['/c', 1]])
  assert.equal(state.site.config.upstreams.length, 3, 'input state must not be mutated')
})

test('makeDefaultUpstream moves an upstream to the front and follows it in routes', () => {
  const editing = prepareSiteForEditing(site([{ url: 'http://a' }, { url: 'http://b' }], [{ path: '/b', upstream: 1 }, { path: '/a', upstream: 0 }]))
  const next = makeDefaultUpstream({ site: editing, sections: sectionsForSite(editing), files: [{ cert: 'a' }, { cert: 'b' }] }, 1)
  assert.deepEqual(next.site.config.upstreams.map(upstream => upstream.url), ['http://b', 'http://a'])
  assert.deepEqual(next.files, [{ cert: 'b' }, { cert: 'a' }])
  assert.deepEqual(next.site.config.routes.map(route => route.upstream), [0, 1])
})

test('addUpstream appends an empty upstream with its own sections and files', () => {
  const editing = prepareSiteForEditing(site([{ url: 'http://a' }]))
  const next = addUpstream({ site: editing, sections: sectionsForSite(editing), files: [{}] })
  assert.equal(next.site.config.upstreams.length, 2)
  assert.equal(next.site.config.upstreams[1].url, '')
  assert.equal(next.sections.upstreams.length, 2)
  assert.equal(next.files.length, 2)
})
