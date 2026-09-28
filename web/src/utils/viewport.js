/**
 * 窄屏断点：与 AppTopNav.css / UiThemeSwitcher.css 里 `@media (max-width: 640px)` 用的是同一个数值，
 * 确保 JS 算出的"侧栏默认收起"初始状态和 CSS 的视觉断点一致。
 */
export const NARROW_VIEWPORT_BREAKPOINT = 640

/**
 * @param {number} width 视口宽度（px），通常来自 window.innerWidth
 * @param {number} [breakpoint] 断点，默认 NARROW_VIEWPORT_BREAKPOINT
 * @returns {boolean} width 是否落在窄屏范围内（含断点本身）
 */
export function isNarrowViewport(width, breakpoint = NARROW_VIEWPORT_BREAKPOINT) {
  return typeof width === 'number' && Number.isFinite(width) && width <= breakpoint
}
