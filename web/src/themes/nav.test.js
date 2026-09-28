import { test } from 'node:test'
import assert from 'node:assert/strict'
import { resolveNavMode, navModeById, DEFAULT_NAV_MODE } from './nav.js'

test('DEFAULT_NAV_MODE is vertical so a fresh production install ships the left sidebar', () => {
  assert.equal(DEFAULT_NAV_MODE, 'vertical')
})

test('resolveNavMode ignores any stored value in production, including a pre-upgrade "hybrid" leftover', () => {
  assert.equal(resolveNavMode({ isDev: false, storedId: 'hybrid' }), 'vertical')
  assert.equal(resolveNavMode({ isDev: false, storedId: 'horizontal' }), 'vertical')
  assert.equal(resolveNavMode({ isDev: false, storedId: null }), 'vertical')
})

test('resolveNavMode honors a valid stored value in DEV', () => {
  assert.equal(resolveNavMode({ isDev: true, storedId: 'horizontal' }), 'horizontal')
  assert.equal(resolveNavMode({ isDev: true, storedId: 'hybrid' }), 'hybrid')
})

test('resolveNavMode falls back to the default in DEV when nothing is stored or the value is unknown', () => {
  assert.equal(resolveNavMode({ isDev: true, storedId: null }), 'vertical')
  assert.equal(resolveNavMode({ isDev: true, storedId: 'not-a-mode' }), 'vertical')
})

test('resolveNavMode defaults isDev/storedId when called with no arguments', () => {
  assert.equal(resolveNavMode(), 'vertical')
})

test('navModeById falls back to the default mode (not a hardcoded array index) for an unknown id', () => {
  assert.equal(navModeById('not-a-mode').id, DEFAULT_NAV_MODE)
  assert.equal(navModeById('vertical').id, 'vertical')
})
