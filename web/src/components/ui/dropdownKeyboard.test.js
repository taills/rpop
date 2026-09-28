import { test } from 'node:test'
import assert from 'node:assert/strict'
import { moveActiveIndex } from './dropdownKeyboard.js'

test('moveActiveIndex returns -1 for an empty list regardless of current/delta', () => {
  assert.equal(moveActiveIndex(-1, 1, 0), -1)
  assert.equal(moveActiveIndex(2, -1, 0), -1)
})

test('moveActiveIndex starts at the first item on ArrowDown when nothing is highlighted yet', () => {
  assert.equal(moveActiveIndex(-1, 1, 3), 0)
})

test('moveActiveIndex starts at the last item on ArrowUp when nothing is highlighted yet', () => {
  assert.equal(moveActiveIndex(-1, -1, 3), 2)
})

test('moveActiveIndex advances forward and wraps past the last item', () => {
  assert.equal(moveActiveIndex(0, 1, 3), 1)
  assert.equal(moveActiveIndex(1, 1, 3), 2)
  assert.equal(moveActiveIndex(2, 1, 3), 0)
})

test('moveActiveIndex moves backward and wraps past the first item', () => {
  assert.equal(moveActiveIndex(2, -1, 3), 1)
  assert.equal(moveActiveIndex(1, -1, 3), 0)
  assert.equal(moveActiveIndex(0, -1, 3), 2)
})

test('moveActiveIndex handles a single-item list by staying put', () => {
  assert.equal(moveActiveIndex(0, 1, 1), 0)
  assert.equal(moveActiveIndex(0, -1, 1), 0)
})
