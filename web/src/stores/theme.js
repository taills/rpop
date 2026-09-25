import { create } from 'zustand'
import { DEFAULT_THEME, THEMES, applyTheme, themeById } from '@/themes'

const STORAGE_KEY = 'towere-ui-kit-theme'

function loadThemeId() {
  const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
  return saved && themeById(saved).id === saved ? saved : DEFAULT_THEME
}

const initialId = loadThemeId()
applyTheme(initialId)

export const useThemeStore = create((set, get) => ({
  themeId: initialId,
  theme: themeById(initialId),
  isDark: themeById(initialId).mode === 'dark',
  themes: THEMES,

  setTheme(id) {
    const themeId = themeById(id).id
    const theme = themeById(themeId)
    applyTheme(themeId)
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, themeId)
    }
    set({ themeId, theme, isDark: theme.mode === 'dark' })
  },

  nextTheme() {
    const idx = THEMES.findIndex((t) => t.id === get().themeId)
    get().setTheme(THEMES[(idx + 1) % THEMES.length].id)
  },
}))
