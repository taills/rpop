import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  blankFailover, editingFailover, failoverProblem, hasFailoverOverride, parseFailoverError, sanitizeFailover,
} from './failoverForm.js'

test('blankFailover renders every field blank', () => {
  assert.deepEqual(blankFailover(), { dialTimeoutMs: '', minCooldownMs: '', maxCooldownMs: '', activeProbe: null })
})

test('editingFailover fills unset fields with blanks and keeps the rest', () => {
  assert.deepEqual(editingFailover({ dialTimeoutMs: 5000 }), { dialTimeoutMs: 5000, minCooldownMs: '', maxCooldownMs: '', activeProbe: null })
  assert.deepEqual(editingFailover(undefined), blankFailover())
  assert.deepEqual(
    editingFailover({ dialTimeoutMs: 2000, minCooldownMs: 500, maxCooldownMs: 30_000, activeProbe: false }),
    { dialTimeoutMs: 2000, minCooldownMs: 500, maxCooldownMs: 30_000, activeProbe: false },
  )
})

test('hasFailoverOverride is false for no override and the blank editor shape', () => {
  assert.equal(hasFailoverOverride(undefined), false)
  assert.equal(hasFailoverOverride(blankFailover()), false)
  assert.equal(hasFailoverOverride(editingFailover(undefined)), false)
})

test('hasFailoverOverride is true once any field is set, including activeProbe:false', () => {
  assert.equal(hasFailoverOverride({ dialTimeoutMs: 5000 }), true)
  assert.equal(hasFailoverOverride(editingFailover({ minCooldownMs: 200 })), true)
  assert.equal(hasFailoverOverride({ activeProbe: false }), true)
  assert.equal(hasFailoverOverride({ activeProbe: true }), true)
})

test('sanitizeFailover drops blank fields and returns undefined for a fully blank form', () => {
  assert.equal(sanitizeFailover(undefined), undefined)
  assert.equal(sanitizeFailover(blankFailover()), undefined)
  assert.deepEqual(sanitizeFailover({ dialTimeoutMs: 5000, minCooldownMs: '', maxCooldownMs: '', activeProbe: null }), { dialTimeoutMs: 5000 })
  assert.deepEqual(
    sanitizeFailover({ dialTimeoutMs: '2000', minCooldownMs: '500', maxCooldownMs: '30000', activeProbe: false }),
    { dialTimeoutMs: 2000, minCooldownMs: 500, maxCooldownMs: 30_000, activeProbe: false },
  )
})

test('failoverProblem accepts a blank or fully-default-range form', () => {
  assert.equal(failoverProblem(undefined), '')
  assert.equal(failoverProblem(blankFailover()), '')
  assert.equal(failoverProblem({ dialTimeoutMs: 1000, minCooldownMs: 1, maxCooldownMs: 600_000, activeProbe: null }), '')
})

test('failoverProblem rejects an out-of-range field', () => {
  assert.match(failoverProblem({ dialTimeoutMs: 999, minCooldownMs: '', maxCooldownMs: '', activeProbe: null }), /建连超时/)
  assert.match(failoverProblem({ dialTimeoutMs: 60_001, minCooldownMs: '', maxCooldownMs: '', activeProbe: null }), /建连超时/)
  assert.match(failoverProblem({ dialTimeoutMs: '', minCooldownMs: 600_001, maxCooldownMs: '', activeProbe: null }), /最小冷却时间/)
})

test('failoverProblem rejects minCooldownMs greater than maxCooldownMs', () => {
  assert.match(failoverProblem({ dialTimeoutMs: '', minCooldownMs: 5000, maxCooldownMs: 1000, activeProbe: null }), /不能大于/)
})

test('parseFailoverError extracts the upstream index from a control.validateFailover message', () => {
  assert.deepEqual(parseFailoverError('upstreams[1].failover.dialTimeoutMs must be between 1000 and 60000'), { upstreamIndex: 1 })
  assert.equal(parseFailoverError('upstreams[0].paths[1].via[2] must name exactly one node or proxy'), null)
  assert.equal(parseFailoverError('at least one upstream is required'), null)
  assert.equal(parseFailoverError(''), null)
})
