// proxyForm.js — pure helpers for the named-proxy editor (ProxiesPage). Kept dependency-free so the write-only
// credential handling and client-side validation are unit-testable without React.

export const PROXY_TYPES = ['socks5', 'socks5h', 'http', 'https']
const PROXY_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/

export function blankProxyForm() {
  return { id: '', name: '', type: 'socks5', address: '', username: '', password: '' }
}

// proxyFormFromView starts an edit form from the API's view (GET/POST/PUT response), which never carries the
// stored password (hasPassword only says whether one is set). The password field starts blank; buildProxyMutation
// treats a blank field on an existing proxy as "keep the stored password".
export function proxyFormFromView(view) {
  return { id: view.id, name: view.name, type: view.type, address: view.address, username: view.username || '', password: '' }
}

// buildProxyMutation turns a form into the proxyMutation request body. A blank password on an existing proxy is
// sent as null so the server keeps the one it has; a new proxy always sends the field (possibly empty, meaning
// no credentials).
export function buildProxyMutation(form, { isNew }) {
  return {
    id: form.id.trim(),
    name: form.name.trim(),
    type: form.type,
    address: form.address.trim(),
    username: form.username.trim(),
    password: isNew || form.password !== '' ? form.password : null,
  }
}

// proxyFormProblem returns a user-facing validation message, or '' when the form can be submitted. The server
// re-validates everything; this only catches obviously empty/malformed fields before a round trip.
export function proxyFormProblem(form, { isNew }) {
  if (isNew && !form.id.trim()) return '请填写代理 ID'
  if (isNew && !PROXY_ID_PATTERN.test(form.id.trim())) return '代理 ID 只能包含字母、数字、点、下划线或短横线，且以字母或数字开头'
  if (!form.name.trim()) return '请填写代理名称'
  if (!PROXY_TYPES.includes(form.type)) return '请选择代理类型'
  if (!form.address.trim()) return '请填写代理地址（host:port）'
  return ''
}

// proxyDeleteProblem turns the server's 409 usedBy conflict into a friendlier prompt; other errors pass through.
export function proxyDeleteProblem(proxy) {
  if (!proxy.usedBy?.length) return ''
  return `代理“${proxy.name}”仍被站点 ${proxy.usedBy.join('、')} 的候选路径引用，无法删除；请先从这些站点的上游中移除该代理。`
}
