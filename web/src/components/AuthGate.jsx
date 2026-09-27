import { useEffect, useState } from 'react'
import { api } from '../api.js'
import { useAuthStore } from '../stores/auth.js'

// AuthGate blocks the whole console behind the admin password: it checks the session once, then shows the
// same setup/login screen the console always has until authenticated. Every route renders only once this
// resolves, so page components never need to handle the "not logged in yet" case themselves.
export default function AuthGate({ children }) {
  const auth = useAuthStore((state) => state.auth)
  const setAuth = useAuthStore((state) => state.setAuth)
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')

  useEffect(() => { api('/auth/status').then(setAuth).catch((e) => setError(e.message)) }, [setAuth])

  async function submit(event) {
    event.preventDefault()
    const setup = !auth?.configured
    if (setup && password !== confirm) { setError('两次输入的密码不一致'); return }
    try {
      await api(`/auth/${setup ? 'setup' : 'login'}`, { method: 'POST', body: JSON.stringify({ password }) })
      setAuth({ configured: true, authenticated: true }); setPassword(''); setConfirm(''); setError('')
    } catch (e) { setError(e.message) }
  }

  if (!auth) return <div className="auth-screen"><section className="auth-card"><div className="brand-mark">r<span>p</span></div><h1>正在检查管理会话</h1><p>请稍候…</p></section></div>
  if (!auth.authenticated) return <div className="auth-screen"><form className="auth-card" onSubmit={submit}><div className="brand-mark">r<span>p</span></div><div className="eyebrow">RPOP CONTROL PLANE</div><h1>{auth.configured ? '管理员登录' : '设置管理密码'}</h1><p>{auth.configured ? '请输入管理密码继续。' : '首次使用请设置至少 12 个字符的管理密码。'}</p>{error && <div className="error">{error}</div>}<label>管理密码<input type="password" required minLength="12" autoComplete={auth.configured ? 'current-password' : 'new-password'} value={password} onChange={e => setPassword(e.target.value)}/></label>{!auth.configured && <label>确认密码<input type="password" required minLength="12" autoComplete="new-password" value={confirm} onChange={e => setConfirm(e.target.value)}/></label>}<button className="primary" type="submit">{auth.configured ? '登录' : '保存密码并进入'}</button></form></div>
  return children
}
