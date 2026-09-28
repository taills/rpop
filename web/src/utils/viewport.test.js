import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isNarrowViewport, NARROW_VIEWPORT_BREAKPOINT } from './viewport.js'

test('isNarrowViewport is true at and below the breakpoint', () => {
  assert.equal(isNarrowViewport(390), true)
  assert.equal(isNarrowViewport(NARROW_VIEWPORT_BREAKPOINT), true)
})

test('isNarrowViewport is false above the breakpoint', () => {
  assert.equal(isNarrowViewport(NARROW_VIEWPORT_BREAKPOINT + 1), false)
  assert.equal(isNarrowViewport(1440), false)
})

test('isNarrowViewport accepts a custom breakpoint', () => {
  assert.equal(isNarrowViewport(800, 1024), true)
  assert.equal(isNarrowViewport(1200, 1024), false)
})

test('isNarrowViewport guards against non-finite/missing width instead of throwing', () => {
  assert.equal(isNarrowViewport(undefined), false)
  assert.equal(isNarrowViewport(NaN), false)
  assert.equal(isNarrowViewport(Infinity), false)
})
