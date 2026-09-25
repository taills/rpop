import { create } from 'zustand'
import { CATEGORIES, DEFAULT_CATEGORY, categoryById } from '@/themes/categories'

const STORAGE_KEY = 'towere-ui-kit-category'

function loadCategoryId() {
  const saved = typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) : null
  return saved && categoryById(saved).id === saved ? saved : DEFAULT_CATEGORY
}

const initialId = loadCategoryId()

export const useCategoryStore = create((set) => ({
  categoryId: initialId,
  category: categoryById(initialId),
  categories: CATEGORIES,
  readyCategories: CATEGORIES.filter((c) => c.status === 'ready'),

  setCategory(id) {
    const categoryId = categoryById(id).id
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(STORAGE_KEY, categoryId)
    }
    set({ categoryId, category: categoryById(categoryId) })
  },
}))
