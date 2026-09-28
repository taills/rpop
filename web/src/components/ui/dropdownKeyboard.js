/**
 * 计算下拉列表 / listbox 按方向键移动后的高亮索引，供 UiThemeSwitcher 等自定义下拉复用。
 * current < 0（尚未高亮任何选项，通常是刚打开面板）时，↓ 从第一项开始、↑ 从最后一项开始，
 * 这样用户打开面板后立刻按方向键就能选到东西，而不必先按两次。到达首/尾后循环，不撞墙。
 * @param {number} current 当前高亮索引，-1 表示未高亮
 * @param {1 | -1} delta 方向：1 为下一项（↓），-1 为上一项（↑）
 * @param {number} length 可选项总数
 * @returns {number} 移动后的索引；length <= 0 时返回 -1
 */
export function moveActiveIndex(current, delta, length) {
  if (length <= 0) return -1
  if (current < 0) return delta > 0 ? 0 : length - 1
  return (current + delta + length) % length
}
