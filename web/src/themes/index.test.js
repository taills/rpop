import { test } from 'node:test'
import assert from 'node:assert/strict'
import { THEMES, groupThemesByMode } from './index.js'

test('groupThemesByMode splits the real theme registry into light-then-dark groups', () => {
  const groups = groupThemesByMode(THEMES)
  assert.deepEqual(groups.map((g) => g.mode), ['light', 'dark'])
  assert.deepEqual(groups.map((g) => g.label), ['浅色', '深色'])
  assert.ok(groups.every((g) => g.items.length > 0))
  assert.equal(groups.flatMap((g) => g.items).length, THEMES.length)
})

test('groupThemesByMode keeps each group in the original registry order', () => {
  const groups = groupThemesByMode(THEMES)
  const light = groups.find((g) => g.mode === 'light').items
  const dark = groups.find((g) => g.mode === 'dark').items
  assert.deepEqual(light.map((t) => t.id), THEMES.filter((t) => t.mode === 'light').map((t) => t.id))
  assert.deepEqual(dark.map((t) => t.id), THEMES.filter((t) => t.mode === 'dark').map((t) => t.id))
})

test('groupThemesByMode omits a mode entirely when no theme uses it', () => {
  const onlyLight = [{ id: 'a', mode: 'light' }, { id: 'b', mode: 'light' }]
  assert.deepEqual(groupThemesByMode(onlyLight).map((g) => g.mode), ['light'])
})

test('groupThemesByMode returns an empty array for an empty theme list', () => {
  assert.deepEqual(groupThemesByMode([]), [])
})
