import { useCallback, useEffect, useState } from 'react'
import './Rpop.css'
import './Admin.css'
import './Sidebar.css'
import LogViewer from './components/LogViewer.jsx'
import LogSettings from './components/LogSettings.jsx'
import SystemSettings from './components/SystemSettings.jsx'
import SiteEditor from './components/SiteEditor.jsx'
import { applySections, isHTTPSURL, sectionsForSite, validateSections } from './siteForm.js'

const blank = { id: '', name: '', autoStart: false, config: { listenAddress: '127.0.0.1', listenPort: 8081, tls: false, certificateId: '', hostnames: [], accessLog: { adapterId: '', includeBodies: false, maxBodyBytes: 1048576 }, upstreams: [{ url: '', proxyUrl: '', proxyType: 'direct', serverName: '', dialAddress: '', insecureSkipVerify: false, rootCertificateIds: [], clientCertificateId: '', clientCertSecret: '', clientKeySecret: '' }] } }
async function api(path, options = {}) {
  const response = await fetch(`/api${path}`, { credentials: 'same-origin', headers: { 'Content-Type': 'application/json', ...options.headers }, ...options })
  if (!response.ok) { const body = await response.json().catch(() => ({})); const error = new Error(body.error || `HTTP ${response.status}`); error.status = response.status; throw error }
  return response.status === 204 ? null : response.json()
}

export default function App() {
  const [sites, setSites] = useState([])
  const [metrics, setMetrics] = useState({})
  const [logAdapters, setLogAdapters] = useState([])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [editing, setEditing] = useState(null)
  const [sections, setSections] = useState(null)
  const [formError, setFormError] = useState('')
  const [query, setQuery] = useState('')
  const [certFile, setCertFile] = useState(null)
  const [keyFile, setKeyFile] = useState(null)
  const [upstreamCertFile, setUpstreamCertFile] = useState(null)
  const [upstreamKeyFile, setUpstreamKeyFile] = useState(null)
  const [rootCertificates, setRootCertificates] = useState([])
  const [clientCertificates, setClientCertificates] = useState([])
  const [serverCertificates, setServerCertificates] = useState([])
  const [auth, setAuth] = useState(null)
  const [authPassword, setAuthPassword] = useState('')
  const [authConfirm, setAuthConfirm] = useState('')
  const [page, setPage] = useState('sites')
  const refresh = useCallback(async () => { try { const [list, logging] = await Promise.all([api('/sites'), api('/logging')]); setSites(list); setLogAdapters(logging.adapters || []); const entries = await Promise.all(list.map(async site => [site.id, await api(`/sites/${encodeURIComponent(site.id)}/metrics`).catch(() => null)])); setMetrics(Object.fromEntries(entries.filter(([, value]) => value))); setError('') } catch (e) { setError(e.message); if (e.status === 401) setAuth(current => current ? { ...current, authenticated: false } : current) } }, [])
  const refreshSystemSettings = useCallback(async () => { try { const settings = await api('/settings'); setRootCertificates(settings.rootCertificates || []); setClientCertificates(settings.clientCertificates || []); setServerCertificates(settings.serverCertificates || []) } catch (e) { setError(e.message); if (e.status === 401) setAuth(current => current ? { ...current, authenticated: false } : current) } }, [])
  useEffect(() => { api('/auth/status').then(setAuth).catch(e => setError(e.message)) }, [])
  useEffect(() => { if (!auth?.authenticated) return; refresh(); refreshSystemSettings(); const timer = setInterval(refresh, 5000); return () => clearInterval(timer) }, [refresh, refreshSystemSettings, auth?.authenticated])
  async function action(site, op) { setBusy(`${site.id}:${op}`); try { await api(`/sites/${encodeURIComponent(site.id)}/${op}`, { method: 'POST' }); await refresh() } catch (e) { setError(e.message) } finally { setBusy('') } }
  async function openEditor(site) {
    await refreshSystemSettings()
    setCertFile(null); setKeyFile(null); setUpstreamCertFile(null); setUpstreamKeyFile(null)
    const next = structuredClone(site)
    setSections(sectionsForSite(next)); setFormError(''); setEditing(next)
  }
  function closeEditor() { setEditing(null); setSections(null); setFormError('') }
  const fileSetters = { certFile: setCertFile, keyFile: setKeyFile, upstreamCertFile: setUpstreamCertFile, upstreamKeyFile: setUpstreamKeyFile }
  async function save(event) {
    event.preventDefault()
    const uploadClientCertificate = Boolean(sections.mtls && isHTTPSURL(editing.config.upstreams[0].url) && !editing.config.upstreams[0].clientCertificateId && (upstreamCertFile || upstreamKeyFile))
    const sectionError = validateSections(editing, sections, { uploadingClientCertificate: uploadClientCertificate })
    if (sectionError) { setFormError(sectionError); return }
    setBusy('save'); setFormError('')
    const exists = sites.some(x => x.id === editing.id)
    const path = exists ? `/sites/${encodeURIComponent(editing.id)}` : '/sites'
    const method = exists ? 'PUT' : 'POST'
    const draft = structuredClone(applySections(editing, sections))
    const original = editing.config.upstreams[0] || {}
    const upstream = draft.config.upstreams[0]
    const uploadServerCertificate = Boolean(draft.config.tls && !draft.config.certificateId && (certFile || keyFile))
    const uploadedSecrets = []
    let stagedNewSite = false
    let cleanupWarning = ''
    try {
      if (uploadServerCertificate && (!certFile || !keyFile)) throw new Error('站点 HTTPS 需要同时提供证书与私钥')
      if (uploadClientCertificate && (!upstreamCertFile || !upstreamKeyFile)) throw new Error('上游双向 TLS 需要同时提供 Client 证书与私钥')
      if (draft.config.tls && !draft.config.certificateId) {
        if (uploadServerCertificate) {
          const suffix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
          draft.config.certificateSecret = `site-cert-${suffix}`
          draft.config.privateKeySecret = `site-key-${suffix}`
        }
        if (!draft.config.certificateSecret || !draft.config.privateKeySecret) throw new Error('启用站点 HTTPS 前请选择系统 HTTPS 证书，或上传证书与私钥')
      }
      const uploadSpecs = []
      if (uploadServerCertificate) uploadSpecs.push([draft.config.certificateSecret, certFile], [draft.config.privateKeySecret, keyFile])
      if (uploadClientCertificate) {
        if (!upstream.url?.toLowerCase().startsWith('https://')) throw new Error('上游 Client 证书仅适用于 HTTPS 上游')
        const suffix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
        upstream.clientCertSecret = `upstream-cert-${suffix}`
        upstream.clientKeySecret = `upstream-key-${suffix}`
        uploadSpecs.push([upstream.clientCertSecret, upstreamCertFile], [upstream.clientKeySecret, upstreamKeyFile])
      }
      if (!upstream.clientCertSecret || !upstream.clientKeySecret) {
        upstream.clientCertSecret = ''
        upstream.clientKeySecret = ''
      }
      const oldSecrets = [editing.config.certificateSecret, editing.config.privateKeySecret, original.clientCertSecret, original.clientKeySecret].filter(Boolean)
      const newSecrets = new Set([draft.config.certificateSecret, draft.config.privateKeySecret, upstream.clientCertSecret, upstream.clientKeySecret].filter(Boolean))
      stagedNewSite = !exists && uploadSpecs.length > 0
      if (stagedNewSite) {
        const pending = structuredClone(draft)
        pending.config.tls = false
        pending.config.certificateId = ''
        pending.config.certificateSecret = ''
        pending.config.privateKeySecret = ''
        pending.config.upstreams[0].clientCertSecret = ''
        pending.config.upstreams[0].clientKeySecret = ''
        await api(path, { method, body: JSON.stringify(pending) })
      }
      for (const [name, file] of uploadSpecs) {
        await api(`/sites/${encodeURIComponent(draft.id)}/secrets/${encodeURIComponent(name)}`, { method: 'PUT', headers: { 'Content-Type': 'application/octet-stream' }, body: file })
        uploadedSecrets.push(name)
      }
      if (stagedNewSite) await api(`/sites/${encodeURIComponent(draft.id)}`, { method: 'PUT', body: JSON.stringify(draft) })
      else await api(path, { method, body: JSON.stringify(draft) })
      for (const name of new Set(oldSecrets)) {
        if (newSecrets.has(name)) continue
        try { await api(`/sites/${encodeURIComponent(draft.id)}/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' }) }
        catch { cleanupWarning = '站点已保存，但部分旧证书密钥未能清理。' }
      }
      closeEditor(); setCertFile(null); setKeyFile(null); setUpstreamCertFile(null); setUpstreamKeyFile(null)
      await refresh()
      if (cleanupWarning) setError(cleanupWarning)
    } catch (e) {
      for (const name of uploadedSecrets) {
        try { await api(`/sites/${encodeURIComponent(draft.id)}/secrets/${encodeURIComponent(name)}`, { method: 'DELETE' }) } catch {}
      }
      if (stagedNewSite) {
        try { await api(`/sites/${encodeURIComponent(draft.id)}`, { method: 'DELETE' }) } catch {}
      }
      setFormError(e.message)
    } finally { setBusy('') }
  }
  async function importYaml(file) { if (!file) return; try { await api('/config.yaml', { method: 'PUT', headers: { 'Content-Type': 'application/yaml' }, body: file }); await refresh() } catch (e) { setError(e.message) } }
  async function remove(site) { if (!window.confirm(`确定删除站点“${site.name}”？`)) return; try { await api(`/sites/${encodeURIComponent(site.id)}`, { method: 'DELETE' }); await refresh() } catch (e) { setError(e.message) } }
  async function submitAuthentication(event) {
    event.preventDefault()
    const setup = !auth?.configured
    if (setup && authPassword !== authConfirm) { setError('两次输入的密码不一致'); return }
    try {
      await api(`/auth/${setup ? 'setup' : 'login'}`, { method: 'POST', body: JSON.stringify({ password: authPassword }) })
      setAuth({ configured: true, authenticated: true }); setAuthPassword(''); setAuthConfirm(''); setError('')
    } catch (e) { setError(e.message) }
  }
  async function logout() {
    try { await api('/auth/logout', { method: 'POST' }) } catch {}
    setAuth(current => current ? { ...current, authenticated: false } : current); setPage('sites')
  }
  const filtered = sites.filter(x => `${x.name} ${x.id} ${x.config?.upstreams?.[0]?.url}`.toLowerCase().includes(query.toLowerCase()))
  const running = sites.filter(x => x.running).length
  if (!auth) return <div className="auth-screen"><section className="auth-card"><div className="brand-mark">r<span>p</span></div><h1>正在检查管理会话</h1><p>请稍候…</p></section></div>
  if (!auth.authenticated) return <div className="auth-screen"><form className="auth-card" onSubmit={submitAuthentication}><div className="brand-mark">r<span>p</span></div><div className="eyebrow">RPOP CONTROL PLANE</div><h1>{auth.configured ? '管理员登录' : '设置管理密码'}</h1><p>{auth.configured ? '请输入管理密码继续。' : '首次使用请设置至少 12 个字符的管理密码。'}</p>{error && <div className="error">{error}</div>}<label>管理密码<input type="password" required minLength="12" autoComplete={auth.configured ? 'current-password' : 'new-password'} value={authPassword} onChange={e => setAuthPassword(e.target.value)}/></label>{!auth.configured && <label>确认密码<input type="password" required minLength="12" autoComplete="new-password" value={authConfirm} onChange={e => setAuthConfirm(e.target.value)}/></label>}<button className="primary" type="submit">{auth.configured ? '登录' : '保存密码并进入'}</button></form></div>
  return <div className="app"><aside className="rail"><div className="rail-brand"><div className="brand-mark">r<span>p</span></div><div className="rail-brand-copy"><strong>rpop</strong><small>CONTROL PANEL</small></div></div><div className="rail-section-title">工作区</div><nav className="side-nav" aria-label="主菜单"><button title="站点管理" aria-current={page==='sites'?'page':undefined} className={page==='sites'?'side-link active':'side-link'} onClick={()=>setPage('sites')}><span className="side-icon">▦</span><span>站点管理</span></button><button title="访问日志" aria-current={page==='logs'?'page':undefined} className={page==='logs'?'side-link active':'side-link'} onClick={()=>setPage('logs')}><span className="side-icon">≋</span><span>访问日志</span></button><button title="日志适配器" aria-current={page==='log-settings'?'page':undefined} className={page==='log-settings'?'side-link active':'side-link'} onClick={()=>setPage('log-settings')}><span className="side-icon">▤</span><span>日志适配器</span></button></nav><div className="rail-bottom"><div className="rail-section-title">账户</div><button title="系统设置" aria-current={page==='system-settings'?'page':undefined} className={page==='system-settings'?'side-link active':'side-link'} onClick={()=>setPage('system-settings')}><span className="side-icon">⚙</span><span>系统设置</span></button><button className="side-link side-logout" title="退出登录" onClick={logout}><span className="side-icon">↪</span><span>退出登录</span></button></div></aside><main className={`main ${page === 'sites' ? '' : 'subpage'}`}>
    <header className="top"><div className="crumb">WORKSPACE <span>/</span> {page.toUpperCase()}</div></header>
    <section className="hero"><div><div className="eyebrow">REVERSE PROXY OVER PROXY</div><h1>站点代理管理</h1><p>统一管理反向代理站点与独立出站链路。</p></div><button className="primary" onClick={() => openEditor(blank)}>＋ 新建站点</button></section>
    {error && <div className="error">{error}<button onClick={() => setError('')}>×</button></div>}
    {page==='logs' && <LogViewer api={api}/>}
    {page==='log-settings' && <LogSettings api={api} onChange={refresh}/>}
    {page==='system-settings' && <SystemSettings api={api} onChange={refreshSystemSettings}/>}
    <section className="stats"><article><small>站点总数</small><strong>{sites.length.toString().padStart(2,'0')}</strong><span>已配置代理站点</span></article><article><small>运行中</small><strong className="green">{running.toString().padStart(2,'0')}</strong><span>正在接收流量</span></article><article><small>已停止</small><strong>{(sites.length-running).toString().padStart(2,'0')}</strong><span>可随时启动</span></article><article className="health"><small>系统状态</small><strong><i className="live-dot"/> HEALTHY</strong><span>SQLite 持久化 · API 正常</span></article></section>
    <section className="list-head"><div><h2>代理站点</h2><p>每个站点拥有独立监听器、TLS 与上游连接策略</p></div><label className="search">⌕ <input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索站点"/></label><a className="yaml-link" href="/api/config.yaml">导出 YAML</a><label className="yaml-upload">导入 YAML<input type="file" accept=".yaml,.yml" onChange={e=>importYaml(e.target.files?.[0])}/></label></section>
    <section className="site-list">{filtered.map(site => { const up=site.config?.upstreams?.[0]||{}; const logAdapter=logAdapters.find(adapter=>adapter.id===site.config?.accessLog?.adapterId); return <article className="site" key={site.id}><div className="site-icon">↗</div><div className="site-info"><div className="site-title">{site.name}<span className={`badge ${site.running?'on':'off'}`}><i/> {site.running?'运行中':'已停止'}</span></div><div className="site-id">{site.id}</div><div className="site-meta"><span>↗ {metrics[site.id]?.requestCount ?? 0} 请求</span><span>TTFB 均值 {Number(metrics[site.id]?.averageTtfbMillis||0).toFixed(1)} ms</span><span>完整响应均值 {Number(metrics[site.id]?.averageResponseMillis||0).toFixed(1)} ms</span><span>P95 {Number(metrics[site.id]?.p95ResponseMillis||0).toFixed(0)} ms</span><span>5xx {metrics[site.id]?.errorCount ?? 0}</span><span>日志丢弃 {metrics[site.id]?.droppedAccessLogCount ?? 0}</span><span>{metrics[site.id]?.bytesSent ?? 0} B sent</span></div><div className="site-meta"><span>◉ {site.config?.listenAddress}:{site.config?.listenPort}</span><span>⇢ {up.url}</span></div></div><div className="tags"><span>{site.config?.tls?'HTTPS':'HTTP'}</span><span>{up.proxyType || (up.proxyUrl ? '代理' : 'DIRECT')}</span><span className="log-adapter-tag" title={logAdapter?.name||'访问日志未启用'}>{logAdapter?`LOG · ${logAdapter.name}`:'LOG OFF'}</span>{(up.clientCertificateId||up.clientCertSecret)&&<span>mTLS</span>}{up.insecureSkipVerify&&<span className="warn-tag">TLS SKIP</span>}{site.autoStart&&<span className="auto-tag">AUTO</span>}</div><div className="actions">{site.running?<button className="icon-btn" title="停止" onClick={()=>action(site,'stop')} disabled={Boolean(busy)}>Ⅱ</button>:<button className="icon-btn start" title="启动" onClick={()=>action(site,'start')} disabled={Boolean(busy)}>▶</button>}<button className="icon-btn" title="重启/热重载" onClick={()=>action(site,'reload')} disabled={Boolean(busy)}>↻</button><button className="icon-btn" title="编辑" onClick={()=>openEditor(site)}>✎</button><button className="icon-btn danger" title="删除" onClick={()=>remove(site)}>⌫</button></div></article> })}{!filtered.length&&<div className="empty"><div>⌘</div><b>{sites.length?'没有匹配的站点':'还没有代理站点'}</b><p>{sites.length?'调整搜索词，或清空搜索。':'新建站点后即可配置监听地址、TLS 证书和上游代理。'}</p>{!sites.length&&<button className="primary" onClick={()=>openEditor(blank)}>创建第一个站点</button>}</div>}</section>
    <footer>RPOP <span>·</span> Reverse Proxy over Proxy <span className="foot-right">API auto-refresh every 5 seconds</span></footer>
    {editing && <SiteEditor site={editing} isNew={!sites.some(x => x.id === editing.id)} sections={sections} catalog={{ rootCertificates, clientCertificates, serverCertificates, logAdapters }} saving={busy === 'save'} error={formError} onChange={setEditing} onSectionsChange={setSections} setFile={(name, file) => fileSetters[name](file)} onCancel={closeEditor} onSubmit={save}/>}
  </main></div>
}
