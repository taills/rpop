import { useAuthStore } from './stores/auth.js'

// api calls the controller's JSON API. A 401 response means the session ended (expired or logged out elsewhere),
// so it drops the shared auth state, which sends every page back to AuthGate's login screen.
export async function api(path, options = {}) {
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...options.headers }, ...options })
  if (!response.ok) {
    const body = await response.json().catch(() => ({}))
    const error = new Error(body.error || `HTTP ${response.status}`)
    error.status = response.status
    if (response.status === 401) useAuthStore.getState().markUnauthenticated()
    throw error
  }
  return response.status === 204 ? null : response.json()
}
