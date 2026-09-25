/**
 * 连接真值 class 字符串（Vue :class 的 React 等价物）。
 * 用法：cx('ui-btn', size === 'sm' && 'is-sm', active && 'on')
 */
export function cx(...parts) {
  return parts.filter(Boolean).join(' ')
}

export default cx
