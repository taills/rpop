/**
 * 导航布局模式注册表
 * horizontal 纯横版：一级/二级都在顶部
 * vertical   纯竖版：左侧承载一级+二级
 * hybrid     横一级 + 竖二级（管理后台常用）
 */

export const NAV_MODES = [
  {
    id: 'horizontal',
    name: '横版',
    short: '横',
    desc: '一级/二级均在顶部，适合浅层信息架构',
  },
  {
    id: 'vertical',
    name: '竖版',
    short: '竖',
    desc: '左侧承载一级+二级，适合模块多、层级深',
  },
  {
    id: 'hybrid',
    name: '横一级+竖二级',
    short: '混',
    desc: '顶栏一级 + 侧栏二级，运营后台默认',
  },
]

export const DEFAULT_NAV_MODE = 'hybrid'

export function navModeById(id) {
  return NAV_MODES.find((m) => m.id === id) || NAV_MODES[2]
}

export function applyNavMode(id) {
  const mode = navModeById(id)
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-nav', mode.id)
  }
  return mode
}
