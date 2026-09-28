import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clampDropdownLeft } from './dropdownPosition.js'

test('clampDropdownLeft right-aligns to the trigger when that already fits inside the viewport', () => {
  // 1440px desktop: trigger sits well away from the left edge, plenty of room on both sides — same position
  // `position: absolute; right: 0` already rendered before this helper existed.
  assert.equal(clampDropdownLeft({ triggerRight: 1400, panelWidth: 240, viewportWidth: 1440 }), 1160)
})

test('clampDropdownLeft matches the un-clamped position at 390px where it already happened to fit', () => {
  // Regression pin for the browser-verified 390px case: trigger's own right edge at x=280, a 240px panel
  // right-aligned to it lands at left=40, comfortably inside [16, 390-16-240=134].
  assert.equal(clampDropdownLeft({ triggerRight: 280, panelWidth: 240, viewportWidth: 390 }), 40)
})

test('clampDropdownLeft pulls the panel back onto the screen when right-aligning would run off the left edge', () => {
  // Regression for the UI review's finding #7: at 320px the theme switcher trigger sits close to the left edge
  // (squeezed by the header's other controls), so right-aligning a 240px panel to it (left = 88 - 240 = -152)
  // would push the panel almost entirely off-screen to the left. Browser-verified before this fix.
  assert.equal(clampDropdownLeft({ triggerRight: 88, panelWidth: 240, viewportWidth: 320 }), 16)
})

test('clampDropdownLeft never pushes the panel past the viewport\'s right edge either', () => {
  assert.equal(clampDropdownLeft({ triggerRight: 500, panelWidth: 240, viewportWidth: 300 }), 44, 'clamped to the right margin (300 - 16 - 240) when right-aligning to the trigger would overflow')
  assert.equal(clampDropdownLeft({ triggerRight: 1000, panelWidth: 100, viewportWidth: 500 }), 384, 'clamped to the right margin (500 - 16 - 100) when the trigger sits past the viewport')
})

test('clampDropdownLeft respects a custom margin', () => {
  assert.equal(clampDropdownLeft({ triggerRight: 50, panelWidth: 240, viewportWidth: 320, margin: 8 }), 8)
})

test('clampDropdownLeft falls back to the left margin when the panel is wider than the viewport allows', () => {
  // maxLeft (viewportWidth - margin - panelWidth) goes negative here; must not invert the clamp range and
  // return something past the viewport's right edge.
  assert.equal(clampDropdownLeft({ triggerRight: 100, panelWidth: 240, viewportWidth: 200 }), 16)
})
