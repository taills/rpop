import { useCallback, useEffect, useState } from 'react'
import './Rpop.css'
import './Admin.css'
import './Sidebar.css'
import LogViewer from './components/LogViewer.jsx'
import LogSettings from './components/LogSettings.jsx'
import SystemSettings from './components/SystemSettings.jsx'
import SiteEditor from './components/SiteEditor.jsx'
import { addUpstream, applySections, blankUpstream, makeDefaultUpstream, prepareSiteForEditing, removeUpstream, sectionsForSite, uploadSlot, validateSections } from './siteForm.js'
import { clientCertificateUploads, planSecretUploads, referencedSecrets, stagedConfig } from './siteSecrets.js'

const blank = { id: '', name: '', autoStart: false, config: { listenAddress: '127.0.0.1', listenPort: 8081, tls: false, certificateId: '', hostnames: [], accessLog: { adapterId: '', includeBodies: false, maxBodyBytes: 1048576 }, upstreams: [blankUpstream()], routes: [] } }
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
  const [upstreamFiles, setUpstreamFiles] = useState([])
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
    setCertFile(null); setKeyFile(null)
    const next = prepareSiteForEditing(site)
    setUpstreamFiles(next.config.upstreams.map(() => uploadSlot()))
    setSections(sectionsForSite(next)); setFormError(''); setEditing(next)
  }
  function closeEditor() { setEditing(null); setSections(null); setFormError(''); setCertFile(null); setKeyFile(null); setUpstreamFiles([]) }
  const fileSetters = { certFile: setCertFile, keyFile: setKeyFile }
  const setUpstreamFile = (index, kind, file) => setUpstreamFiles(files => files.map((slot, i) => i === index ? { ...slot, [kind]: file } : slot))
  // changeUpstreams applies an editor transform that keeps upstreams, their sections, staged files and routes aligned.
  function changeUpstreams(transform) {
    const next = transform({ site: editing, sections, files: upstreamFiles })
    setEditing(next.site); setSections(next.sections); setUpstreamFiles(next.files)
  }
  function removeUpstreamAt(index) {
    const affected = (editing.config.routes || []).filter(route => Number(route.upstream) === index).length
    if (affected && !window.confirm(`上游 #${index + 1} 仍被 ${affected} 条路由规则使用，删除该上游会同时删除这些规则。确定继续？`)) return
    changeUpstreams(state => removeUpstream(state, index).state)
  }
  const simulateRoute = useCallback(body => api('/routes/simulate', { method: 'POST', body: JSON.stringify(body) }), [])
  async function save(event) {
    event.preventDefault()
    const uploading = clientCertificateUploads(editing, sections, upstreamFiles)
    const sectionError = validateSections(editing, sections, { uploadingClientCertificates: uploading })
    if (sectionError) { setFormError(sectionError); return }
    setBusy('save'); setFormError('')
    const persisted = sites.find(x => x.id === editing.id)
    const path = persisted ? `/sites/${encodeURIComponent(editing.id)}` : '/sites'
    const method = persisted ? 'PUT' : 'POST'
    const secretPath = name => `/sites/${encodeURIComponent(editing.id)}/secrets/${encodeURIComponent(name)}`
    const uploadedSecrets = []
    let stagedNewSite = false
    let cleanupWarning = ''
    try {
      const { draft, uploads } = planSecretUploads(applySections(editing, sections), { certFile, keyFile, upstreamFiles, uploading })
      const newSecrets = new Set(referencedSecrets(draft.config))
      stagedNewSite = !persisted && uploads.length > 0
      if (stagedNewSite) await api(path, { method, body: JSON.stringify({ ...draft, config: stagedConfig(draft.config) }) })
      for (const [name, file] of uploads) {
        await api(secretPath(name), { method: 'PUT', headers: { 'Content-Type': 'application/octet-stream' }, body: file })
        uploadedSecrets.push(name)
      }
      if (stagedNewSite) await api(`/sites/${encodeURIComponent(draft.id)}`, { method: 'PUT', body: JSON.stringify(draft) })
      else await api(path, { method, body: JSON.stringify(draft) })
      for (const name of new Set(referencedSecrets(persisted?.config))) {
        if (newSecrets.has(name)) continue
        try { await api(secretPath(name), { method: 'DELETE' }) }
        catch { cleanupWarning = '站点已保存，但部分旧证书密钥未能清理。' }
      }
      closeEditor()
      await refresh()
      if (cleanupWarning) setError(cleanupWarning)
    } catch (e) {
      for (const name of uploadedSecrets) {
        try { await api(secretPath(name), { method: 'DELETE' }) } catch {}
      }
      if (stagedNewSite) {
        try { await api(`/sites/${encodeURIComponent(editing.id)}`, { method: 'DELETE' }) } catch {}
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
  const filtered = sites.filter(x => `${x.name} ${x.id} ${(x.config?.upstreams || []).map(up => up.url).join(' ')}`.toLowerCase().includes(query.toLowerCase()))
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
    <section className="site-list">{filtered.map(site => { const ups=site.config?.upstreams||[]; const up=ups[0]||{}; const routeCount=(site.config?.routes||[]).length; const logAdapter=logAdapters.find(adapter=>adapter.id===site.config?.accessLog?.adapterId); return <article className="site" key={site.id}><div className="site-icon">↗</div><div className="site-info"><div className="site-title">{site.name}<span className={`badge ${site.running?'on':'off'}`}><i/> {site.running?'运行中':'已停止'}</span></div><div className="site-id">{site.id}</div><div className="site-meta"><span>↗ {metrics[site.id]?.requestCount ?? 0} 请求</span><span>TTFB 均值 {Number(metrics[site.id]?.averageTtfbMillis||0).toFixed(1)} ms</span><span>完整响应均值 {Number(metrics[site.id]?.averageResponseMillis||0).toFixed(1)} ms</span><span>P95 {Number(metrics[site.id]?.p95ResponseMillis||0).toFixed(0)} ms</span><span>5xx {metrics[site.id]?.errorCount ?? 0}</span><span>日志丢弃 {metrics[site.id]?.droppedAccessLogCount ?? 0}</span><span>{metrics[site.id]?.bytesSent ?? 0} B sent</span></div><div className="site-meta"><span>◉ {site.config?.listenAddress}:{site.config?.listenPort}</span><span>⇢ {up.url}{ups.length>1&&` 等 ${ups.length} 个上游`}</span></div></div><div className="tags"><span>{site.config?.tls?'HTTPS':'HTTP'}</span><span>{up.proxyType || (up.proxyUrl ? '代理' : 'DIRECT')}</span>{routeCount>0&&<span className="route-tag">{routeCount} 条路由</span>}<span className="log-adapter-tag" title={logAdapter?.name||'访问日志未启用'}>{logAdapter?`LOG · ${logAdapter.name}`:'LOG OFF'}</span>{ups.some(item=>item.clientCertificateId||item.clientCertSecret)&&<span>mTLS</span>}{ups.some(item=>item.insecureSkipVerify)&&<span className="warn-tag">TLS SKIP</span>}{site.autoStart&&<span className="auto-tag">AUTO</span>}</div><div className="actions">{site.running?<button className="icon-btn" title="停止" onClick={()=>action(site,'stop')} disabled={Boolean(busy)}>Ⅱ</button>:<button className="icon-btn start" title="启动" onClick={()=>action(site,'start')} disabled={Boolean(busy)}>▶</button>}<button className="icon-btn" title="重启/热重载" onClick={()=>action(site,'reload')} disabled={Boolean(busy)}>↻</button><button className="icon-btn" title="编辑" onClick={()=>openEditor(site)}>✎</button><button className="icon-btn danger" title="删除" onClick={()=>remove(site)}>⌫</button></div></article> })}{!filtered.length&&<div className="empty"><div>⌘</div><b>{sites.length?'没有匹配的站点':'还没有代理站点'}</b><p>{sites.length?'调整搜索词，或清空搜索。':'新建站点后即可配置监听地址、TLS 证书和上游代理。'}</p>{!sites.length&&<button className="primary" onClick={()=>openEditor(blank)}>创建第一个站点</button>}</div>}</section>
    <footer>RPOP <span>·</span> Reverse Proxy over Proxy <span className="foot-right">API auto-refresh every 5 seconds</span></footer>
    {editing && <SiteEditor site={editing} isNew={!sites.some(x => x.id === editing.id)} sections={sections} catalog={{ rootCertificates, clientCertificates, serverCertificates, logAdapters }} saving={busy === 'save'} error={formError} upstreamFiles={upstreamFiles} onChange={setEditing} onSectionsChange={setSections} setFile={(name, file) => fileSetters[name](file)} setUpstreamFile={setUpstreamFile} onAddUpstream={() => changeUpstreams(addUpstream)} onRemoveUpstream={removeUpstreamAt} onMakeDefault={index => changeUpstreams(state => makeDefaultUpstream(state, index))} onSimulate={simulateRoute} onCancel={closeEditor} onSubmit={save}/>}
  </main></div>
}
