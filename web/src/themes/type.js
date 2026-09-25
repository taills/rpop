/**
 * 字体档位：ratio 乘在语义字号 token 上，切换全局生效。
 * 组件禁止写死 px，一律读 --fs-* 语义 token。
 */

export const TYPE_SCALES = [
  {
    id: 'compact',
    name: '紧凑',
    ratio: 0.92,
    desc: '高密度列表、一屏信息更多',
  },
  {
    id: 'standard',
    name: '标准',
    ratio: 1,
    desc: '默认运营后台档位',
  },
  {
    id: 'comfortable',
    name: '宽松',
    ratio: 1.08,
    desc: '演示投屏、可读性优先',
  },
  {
    id: 'large',
    name: '大号',
    ratio: 1.16,
    desc: '无障碍 / 远距离观看',
  },
]

export const DEFAULT_TYPE_SCALE = 'standard'

export function typeScaleById(id) {
  return TYPE_SCALES.find((t) => t.id === id) || TYPE_SCALES[1]
}

export function applyTypeScale(id) {
  const scale = typeScaleById(id)
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-type-scale', scale.id)
  }
  return scale
}

/**
 * 组件 → 语义字号 token 对照（文档与 Catalog 演示同源）
 * 业务页新写样式时按下表取 token，不要自创字号。
 */
export const TYPE_ROLE_MAP = [
  {
    role: 'caption',
    token: '--fs-caption',
    base: '11px',
    usage: '标签 UiTag / 徽标 UiBadge / 角标 / 密度辅助说明',
  },
  {
    role: 'meta',
    token: '--fs-xs',
    base: '11px',
    usage: '副文案、统计卡 hint、面包屑分隔、次级说明',
  },
  {
    role: 'control',
    token: '--fs-sm',
    base: '12px',
    usage: '按钮 UiButton / 输入 UiInput·UiSelect·UiSearch / 表格 UiTable 单元格与表头 / 表单标签 UiField / 分页数字',
  },
  {
    role: 'body',
    token: '--fs-md',
    base: '13px',
    usage: '正文、卡片 body、Modal/Drawer 内容、Alert 正文、描述列表 value',
  },
  {
    role: 'emphasis',
    token: '--fs-lg',
    base: '14px',
    usage: '强调正文、侧栏一级菜单、顶栏品牌名',
  },
  {
    role: 'cardTitle',
    token: '--fs-xl',
    base: '16px',
    usage: '卡片 UiCard 标题、Modal/Drawer 标题、Tabs 选中强提示',
  },
  {
    role: 'pageTitle',
    token: '--fs-title',
    base: '22px',
    usage: '页头 UiPageHeader 主标题',
  },
  {
    role: 'stat',
    token: '--fs-stat',
    base: '28px',
    usage: '统计卡 UiStatCard 主数字（DIN）',
  },
  {
    role: 'statSm',
    token: '--fs-stat-sm',
    base: '22px',
    usage: '统计卡紧凑变体 / 次级 KPI',
  },
  {
    role: 'display',
    token: '--fs-display',
    base: '26px',
    usage: '展示型标题字（克制使用，需 --font-display）',
  },
]
