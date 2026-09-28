// dropdownPosition.js — pure geometry for keeping an absolutely-positioned dropdown panel (anchored to its
// trigger's right edge, e.g. UiThemeSwitcher.jsx's panel) inside the viewport instead of running off its left
// edge on a narrow screen where the trigger doesn't sit flush against the page's own right edge (it can be
// squeezed left by neighboring header controls — see docs/architecture/control-data-plane.md §5's "UI 审查问题
// 修复" entry). Framework-free so it is covered by node --test independent of any DOM/resize wiring.

/**
 * clampDropdownLeft computes the panel's `left` (viewport px): normally right-aligned to the trigger, exactly
 * what `position: absolute; right: 0` already renders when there is room, but pulled inward so the panel's own
 * left/right edges never cross the viewport's edges (minus `margin` on each side).
 *
 * @param {object} args
 * @param {number} args.triggerRight - the trigger's right edge (Element.getBoundingClientRect().right)
 * @param {number} args.panelWidth - the panel's own rendered width (Element.offsetWidth)
 * @param {number} args.viewportWidth - window.innerWidth
 * @param {number} [args.margin] - minimum gap to keep from either viewport edge, default 16 (matches the
 *   16px side-gutter convention this console's narrow-screen layouts already use elsewhere)
 * @returns {number} the `left` to apply to the panel
 */
export function clampDropdownLeft({ triggerRight, panelWidth, viewportWidth, margin = 16 }) {
  const preferred = triggerRight - panelWidth
  const minLeft = margin
  const maxLeft = viewportWidth - margin - panelWidth
  // A panel wider than the viewport itself (minus both margins) has no position that satisfies both edges;
  // favor keeping the left edge on-screen (maxLeft < minLeft would otherwise invert the clamp range).
  if (maxLeft < minLeft) return minLeft
  return Math.min(Math.max(preferred, minLeft), maxLeft)
}
