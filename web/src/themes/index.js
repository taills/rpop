/**
 * 主题注册表：id ↔ 展示名 ↔ 预览色块
 * 一键切换时写入 document.documentElement.dataset.theme
 */

export const THEMES = [
  {
    id: 'ocean',
    name: '海洋蓝',
    mode: 'light',
    desc: '浅色管理后台默认',
    swatches: ['#3b82f6', '#f3f6fb', '#0f172a'],
  },
  {
    id: 'indigo',
    name: '靛蓝',
    mode: 'light',
    desc: '浅色靛紫，偏管理控制台',
    swatches: ['#6366f1', '#f4f4fb', '#0f172a'],
  },
  {
    id: 'emerald',
    name: '翡翠绿',
    mode: 'light',
    desc: '浅色青绿，偏运维/合规',
    swatches: ['#059669', '#f3f8f5', '#0f172a'],
  },
  {
    id: 'slate',
    name: '石板深色',
    mode: 'dark',
    desc: '深色石板 + 天蓝强调',
    swatches: ['#38bdf8', '#0f172a', '#f1f5f9'],
  },
  {
    id: 'navy',
    name: '海军深色',
    mode: 'dark',
    desc: '深色海军 + 经典蓝（大屏友好）',
    swatches: ['#3b82f6', '#0b1220', '#e8eef9'],
  },
  {
    id: 'violet',
    name: '紫电深色',
    mode: 'dark',
    desc: '深色紫电，偏 AI 场景',
    swatches: ['#8b5cf6', '#120f1f', '#f3eefc'],
  },
]

export const DEFAULT_THEME = 'ocean'

export function themeById(id) {
  return THEMES.find((t) => t.id === id) || THEMES[0]
}

export function applyTheme(id) {
  const theme = themeById(id)
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-theme', theme.id)
  }
  return theme
}

const MODE_LABELS = { light: '浅色', dark: '深色' }
const MODE_ORDER = ['light', 'dark']

/**
 * 把主题列表按 mode 分组，供 UiThemeSwitcher 下拉的"浅色 / 深色"分组渲染复用。
 * 固定 light 在前、dark 在后；组内保持 themes 原有顺序；某个 mode 一个主题都没有时不返回空分组。
 * @param {Array<{id: string, mode: 'light' | 'dark'}>} themes
 * @returns {Array<{mode: string, label: string, items: Array}>}
 */
export function groupThemesByMode(themes) {
  return MODE_ORDER
    .map((mode) => ({ mode, label: MODE_LABELS[mode], items: themes.filter((t) => t.mode === mode) }))
    .filter((group) => group.items.length > 0)
}
