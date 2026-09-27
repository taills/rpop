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

// bindToastApi wires a store's push/success/error/warn/info into the shape useToast() hands out. It is a plain
// function of its arguments (no hook call inside), so toast.test.js can exercise the shape directly with mock
// functions instead of having to render a component.
//
// Every call site in the console destructures `const { toast } = useToast()` and then calls
// `toast.success(...)`/`toast.error(...)`/etc, not the bare `toast(msg, type, title)` form (that pattern
// predates most of these call sites and nothing currently relies on the plain-call form, but it is kept working
// too since it costs nothing to keep). Rather than rewrite every one of those call sites, `toast` itself is a
// function with success/error/warn/info attached as properties, so both styles resolve to the same store calls.
export function bindToastApi({ push, success, error, warn, info }) {
  const toast = (msg, type = 'info', title = '') => push({ type, message: msg, title })
  toast.success = success
  toast.error = error
  toast.warn = warn
  toast.info = info
  return { toast, success, error, warn, info }
}

export function useToast() {
  return bindToastApi(useToastStore())
}
