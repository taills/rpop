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

// 生产控制台固定竖版侧栏（见 resolveNavMode）；混排/横版仅在 DEV 的组件目录 / Token 演示页里可切换体验。
export const DEFAULT_NAV_MODE = 'vertical'

export function navModeById(id) {
  return NAV_MODES.find((m) => m.id === id) || NAV_MODES.find((m) => m.id === DEFAULT_NAV_MODE)
}

export function applyNavMode(id) {
  const mode = navModeById(id)
  if (typeof document !== 'undefined') {
    document.documentElement.setAttribute('data-nav', mode.id)
  }
  return mode
}

/**
 * 解析启动时应生效的导航模式。
 * 生产构建无视 localStorage 里的历史值（包括升级前残留的 'hybrid'），一律固定竖版侧栏；
 * 只有 DEV 下的导航布局切换器需要跨刷新记住用户选择，才读取并校验 storedId。
 * @param {{ isDev?: boolean, storedId?: string | null }} [options]
 * @returns {string} 合法的 NAV_MODES id
 */
export function resolveNavMode({ isDev = false, storedId = null } = {}) {
  if (!isDev) return DEFAULT_NAV_MODE
  return storedId && navModeById(storedId).id === storedId ? storedId : DEFAULT_NAV_MODE
}
