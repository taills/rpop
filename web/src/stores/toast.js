import { create } from 'zustand'

let seq = 0

export const useToastStore = create((set, get) => ({
  list: [],

  push({ type = 'info', title = '', message = '', duration = 2600 } = {}) {
    const id = ++seq
    set((s) => ({ list: [...s.list, { id, type, title, message }] }))
    if (duration > 0) {
      setTimeout(() => get().dismiss(id), duration)
    }
    return id
  },

  dismiss(id) {
    set((s) => ({ list: s.list.filter((t) => t.id !== id) }))
  },

  success(message, title = '成功') {
    return get().push({ type: 'success', title, message })
  },
  error(message, title = '失败') {
    return get().push({ type: 'error', title, message })
  },
  warn(message, title = '注意') {
    return get().push({ type: 'warn', title, message })
  },
  info(message, title = '提示') {
    return get().push({ type: 'info', title, message })
  },
}))

export function useToast() {
  const { push, success, error, warn, info } = useToastStore()
  return {
    toast: (msg, type = 'info', title = '') => push({ type, message: msg, title }),
    success,
    error,
    warn,
    info,
  }
}
