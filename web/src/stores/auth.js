import { create } from 'zustand'

// auth is null while the initial session check is in flight, then {configured, authenticated}. It is shared
// across every route (via AuthGate and ShellView's logout button) and updated by api.js whenever a request
// comes back 401, so any page losing its session drops straight back to the login screen.
export const useAuthStore = create((set) => ({
  auth: null,
  setAuth: (auth) => set({ auth }),
  markUnauthenticated: () => set((state) => (state.auth ? { auth: { ...state.auth, authenticated: false } } : state)),
}))
