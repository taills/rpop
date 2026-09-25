import { create } from 'zustand'
import { DEFAULT_TYPE_SCALE, TYPE_ROLE_MAP, TYPE_SCALES, applyTypeScale, typeScaleById } from '@/themes/type'

const STORAGE_KEY = 'towere-ui-kit-type-scale'

function loadScaleId() {
  const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
  return saved && typeScaleById(saved).id === saved ? saved : DEFAULT_TYPE_SCALE
}

function derive(scaleId) {
  const scale = typeScaleById(scaleId)
  return { scaleId, scale, ratio: scale.ratio }
}

const initialId = loadScaleId()
applyTypeScale(initialId)

export const useTypeStore = create((set, get) => ({
  ...derive(initialId),
  scales: TYPE_SCALES,
  roleMap: TYPE_ROLE_MAP,

  setScale(id) {
    const scaleId = typeScaleById(id).id
    applyTypeScale(scaleId)
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, scaleId)
    }
    set(derive(scaleId))
  },

  nextScale() {
    const idx = TYPE_SCALES.findIndex((t) => t.id === get().scaleId)
    get().setScale(TYPE_SCALES[(idx + 1) % TYPE_SCALES.length].id)
  },

  /** 读取当前文档中某语义字号的计算值 */
  resolveToken(token) {
    if (typeof window === 'undefined') return ''
    return getComputedStyle(document.documentElement).getPropertyValue(token).trim()
  },
}))
