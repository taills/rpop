import { create } from 'zustand'
import { NAV_MODES, applyNavMode, navModeById, resolveNavMode } from '@/themes/nav'

const STORAGE_KEY = 'towere-ui-kit-nav-mode'

// 生产构建下 resolveNavMode 无视 saved，固定竖版侧栏（哪怕 localStorage 里还留着升级前的 'hybrid'）；
// 只有 DEV 的导航布局切换器需要跨刷新记住用户选择。
function loadModeId() {
  const isDev = Boolean(import.meta.env?.DEV)
  const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
  return resolveNavMode({ isDev, storedId: saved })
}

/** 由当前布局推导所有派生状态（对应 Vue 版的 computed） */
function derive(modeId) {
  return {
    modeId,
    mode: navModeById(modeId),
    isHorizontal: modeId === 'horizontal',
    isVertical: modeId === 'vertical',
    isHybrid: modeId === 'hybrid',
    /** 顶部是否展示一级菜单 */
    showTopMenus: modeId !== 'vertical',
    /** 顶部是否展示二级横条 */
    showTopSecondary: modeId === 'horizontal',
    /** 侧栏是否展示 */
    showSide: modeId !== 'horizontal',
    /** 侧栏是否展示一级（竖版全量 / 混排时侧栏只放二级） */
    sideShowPrimary: modeId === 'vertical',
  }
}

const initialId = loadModeId()
applyNavMode(initialId)

export const useNavStore = create((set, get) => ({
  ...derive(initialId),
  modes: NAV_MODES,

  setMode(id) {
    const modeId = navModeById(id).id
    applyNavMode(modeId)
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, modeId)
    }
    set(derive(modeId))
  },

  nextMode() {
    const idx = NAV_MODES.findIndex((m) => m.id === get().modeId)
    get().setMode(NAV_MODES[(idx + 1) % NAV_MODES.length].id)
  },
}))
