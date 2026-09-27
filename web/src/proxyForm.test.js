import { test } from 'node:test'
import assert from 'node:assert/strict'
import { blankProxyForm, buildProxyMutation, proxyDeleteProblem, proxyFormFromView, proxyFormProblem } from './proxyForm.js'

test('blankProxyForm starts with an empty socks5 draft', () => {
  const form = blankProxyForm()
  assert.equal(form.type, 'socks5')
  assert.equal(form.password, '')
})

test('proxyFormFromView never carries the stored password', () => {
  const form = proxyFormFromView({ id: 'p1', name: 'Proxy 1', type: 'https', address: 'proxy.example:443', username: 'alice', hasPassword: true })
  assert.equal(form.password, '')
  assert.equal(form.username, 'alice')
})

test('buildProxyMutation always sends the password for a new proxy', () => {
  const form = { id: 'p1', name: ' Proxy 1 ', type: 'socks5', address: ' 127.0.0.1:1080 ', username: '', password: '' }
  const mutation = buildProxyMutation(form, { isNew: true })
  assert.equal(mutation.password, '')
  assert.equal(mutation.name, 'Proxy 1')
  assert.equal(mutation.address, '127.0.0.1:1080')
})

test('buildProxyMutation keeps the stored password when the field is left blank on edit', () => {
  const form = { id: 'p1', name: 'Proxy 1', type: 'socks5', address: '127.0.0.1:1080', username: 'alice', password: '' }
  const mutation = buildProxyMutation(form, { isNew: false })
  assert.equal(mutation.password, null)
})

test('buildProxyMutation sends a new password typed on edit', () => {
  const form = { id: 'p1', name: 'Proxy 1', type: 'socks5', address: '127.0.0.1:1080', username: 'alice', password: 'new-secret' }
  const mutation = buildProxyMutation(form, { isNew: false })
  assert.equal(mutation.password, 'new-secret')
})

test('proxyFormProblem requires an id and name only for new proxies', () => {
  const problem = proxyFormProblem({ ...blankProxyForm(), name: 'x', address: 'h:1' }, { isNew: true })
  assert.equal(problem, '请填写代理 ID')
  assert.equal(proxyFormProblem({ id: 'ok-1', name: 'x', type: 'socks5', address: 'h:1', password: '' }, { isNew: true }), '')
  assert.equal(proxyFormProblem({ id: '', name: 'x', type: 'socks5', address: 'h:1', password: '' }, { isNew: false }), '')
})

test('proxyFormProblem rejects a malformed id', () => {
  assert.match(proxyFormProblem({ id: 'bad id!', name: 'x', type: 'socks5', address: 'h:1' }, { isNew: true }), /代理 ID/)
})

test('proxyFormProblem requires name, type and address', () => {
  const base = { id: 'ok', name: '', type: 'socks5', address: '' }
  assert.equal(proxyFormProblem(base, { isNew: true }), '请填写代理名称')
  assert.equal(proxyFormProblem({ ...base, name: 'x', type: '' }, { isNew: true }), '请选择代理类型')
  assert.equal(proxyFormProblem({ ...base, name: 'x' }, { isNew: true }), '请填写代理地址（host:port）')
})

test('proxyDeleteProblem names the sites still using the proxy', () => {
  assert.equal(proxyDeleteProblem({ usedBy: [] }), '')
  assert.match(proxyDeleteProblem({ name: 'p1', usedBy: ['site-a', 'site-b'] }), /site-a、site-b/)
})
