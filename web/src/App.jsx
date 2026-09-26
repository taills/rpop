import { useCallback, useEffect, useState } from 'react'
import './Rpop.css'
import './Admin.css'
import './Sidebar.css'
import LogViewer from './components/LogViewer.jsx'
import LogSettings from './components/LogSettings.jsx'

const blank = { id: '', name: '', autoStart: false, config: { listenAddress: '127.0.0.1', listenPort: 8081, tls: false, hostnames: [], accessLog: { adapterId: '', includeBodies: false, maxBodyBytes: 1048576 }, upstreams: [{ url: '', proxyUrl: '', proxyType: 'direct', serverName: '', dialAddress: '', insecureSkipVerify: false }] } }
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
  const [query, setQuery] = useState('')
  const [certFile, setCertFile] = useState(null)
  const [keyFile, setKeyFile] = useState(null)
  const [auth, setAuth] = useState(null)
  const [authPassword, setAuthPassword] = useState('')
  const [authConfirm, setAuthConfirm] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [page, setPage] = useState('sites')
  const refresh = useCallback(async () => { try { const [list, logging] = await Promise.all([api('/sites'), api('/logging')]); setSites(list); setLogAdapters(logging.adapters || []); const entries = await Promise.all(list.map(async site => [site.id, await api(`/sites/${encodeURIComponent(site.id)}/metrics`).catch(() => null)])); setMetrics(Object.fromEntries(entries.filter(([, value]) => value))); setError('') } catch (e) { setError(e.message); if (e.status === 401) setAuth(current => current ? { ...current, authenticated: false } : current) } }, [])
  useEffect(() => { api('/auth/status').then(setAuth).catch(e => setError(e.message)) }, [])
  useEffect(() => { if (!auth?.authenticated) return; refresh(); const timer = setInterval(refresh, 5000); return () => clearInterval(timer) }, [refresh, auth?.authenticated])
  async function action(site, op) { setBusy(`${site.id}:${op}`); try { await api(`/sites/${encodeURIComponent(site.id)}/${op}`, { method: 'POST' }); await refresh() } catch (e) { setError(e.message) } finally { setBusy('') } }
  async function save(event) {
    event.preventDefault(); setBusy('save')
    try {
      const exists = sites.some(x => x.id === editing.id)
      const path = exists ? `/sites/${encodeURIComponent(editing.id)}` : '/sites'
      const method = exists ? 'PUT' : 'POST'
      const draft = structuredClone(editing)
      if (draft.config.tls && (certFile || keyFile)) {
        if (!certFile || !keyFile) throw new Error('站点 HTTPS 需要同时提供证书与私钥')
        draft.config.certificateSecret = 'site-cert'
        draft.config.privateKeySecret = 'site-key'
        const pending = structuredClone(draft); pending.config.tls = false
        await api(path, { method, body: JSON.stringify(pending) })
        for (const [name, file] of [['site-cert', certFile], ['site-key', keyFile]]) await api(`/sites/${encodeURIComponent(draft.id)}/secrets/${name}`, { method: 'PUT', headers: { 'Content-Type': 'application/octet-stream' }, body: file })
        await api(`/sites/${encodeURIComponent(draft.id)}`, { method: 'PUT', body: JSON.stringify(draft) })
      } else await api(path, { method, body: JSON.stringify(draft) })
      setEditing(null); setCertFile(null); setKeyFile(null); await refresh()
    } catch (e) { setError(e.message) } finally { setBusy('') }
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
  async function changePassword(event) {
    event.preventDefault()
    try {
      await api('/auth/password', { method: 'PUT', body: JSON.stringify({ currentPassword, newPassword }) })
      setCurrentPassword(''); setNewPassword(''); setError('管理密码已更新')
    } catch (e) { setError(e.message) }
  }
  const filtered = sites.filter(x => `${x.name} ${x.id} ${x.config?.upstreams?.[0]?.url}`.toLowerCase().includes(query.toLowerCase()))
  const running = sites.filter(x => x.running).length
  if (!auth) return <div className="auth-screen"><section className="auth-card"><div className="brand-mark">r<span>p</span></div><h1>正在检查管理会话</h1><p>请稍候…</p></section></div>
  if (!auth.authenticated) return <div className="auth-screen"><form className="auth-card" onSubmit={submitAuthentication}><div className="brand-mark">r<span>p</span></div><div className="eyebrow">RPOP CONTROL PLANE</div><h1>{auth.configured ? '管理员登录' : '设置管理密码'}</h1><p>{auth.configured ? '请输入管理密码继续。' : '首次使用请设置至少 12 个字符的管理密码。'}</p>{error && <div className="error">{error}</div>}<label>管理密码<input type="password" required minLength="12" autoComplete={auth.configured ? 'current-password' : 'new-password'} value={authPassword} onChange={e => setAuthPassword(e.target.value)}/></label>{!auth.configured && <label>确认密码<input type="password" required minLength="12" autoComplete="new-password" value={authConfirm} onChange={e => setAuthConfirm(e.target.value)}/></label>}<button className="primary" type="submit">{auth.configured ? '登录' : '保存密码并进入'}</button></form></div>
  return <div className="app"><aside className="rail"><div className="rail-brand"><div className="brand-mark">r<span>p</span></div><div className="rail-brand-copy"><strong>rpop</strong><small>CONTROL PANEL</small></div></div><div className="rail-section-title">工作区</div><nav className="side-nav" aria-label="主菜单"><button title="站点管理" aria-current={page==='sites'?'page':undefined} className={page==='sites'?'side-link active':'side-link'} onClick={()=>setPage('sites')}><span className="side-icon">▦</span><span>站点管理</span></button><button title="访问日志" aria-current={page==='logs'?'page':undefined} className={page==='logs'?'side-link active':'side-link'} onClick={()=>setPage('logs')}><span className="side-icon">≋</span><span>访问日志</span></button><button title="日志适配器" aria-current={page==='log-settings'?'page':undefined} className={page==='log-settings'?'side-link active':'side-link'} onClick={()=>setPage('log-settings')}><span className="side-icon">▤</span><span>日志适配器</span></button></nav><div className="rail-bottom"><div className="rail-section-title">账户</div><button title="安全设置" aria-current={page==='security'?'page':undefined} className={page==='security'?'side-link active':'side-link'} onClick={()=>setPage('security')}><span className="side-icon">⚙</span><span>安全设置</span></button><button className="side-link side-logout" title="退出登录" onClick={logout}><span className="side-icon">↪</span><span>退出登录</span></button></div></aside><main className={`main ${page === 'sites' ? '' : 'subpage'}`}>
    <header className="top"><div className="crumb">WORKSPACE <span>/</span> {page.toUpperCase()}</div></header>
    <section className="hero"><div><div className="eyebrow">REVERSE PROXY OVER PROXY</div><h1>站点代理管理</h1><p>统一管理反向代理站点与独立出站链路。</p></div><button className="primary" onClick={() => { setCertFile(null); setKeyFile(null); setEditing(structuredClone(blank)) }}>＋ 新建站点</button></section>
    {error && <div className="error">{error}<button onClick={() => setError('')}>×</button></div>}
    {page==='logs' && <LogViewer api={api}/>}
    {page==='log-settings' && <LogSettings api={api} onChange={refresh}/>}
    {page==='security' && <section className="settings-panel"><div className="eyebrow">SECURITY</div><h2>管理密码</h2><p>密码至少 12 个字符。更新后其他浏览器会话将立即失效。</p><form className="settings-form" onSubmit={changePassword}><label>当前密码<input type="password" required autoComplete="current-password" value={currentPassword} onChange={e=>setCurrentPassword(e.target.value)}/></label><label>新密码<input type="password" required minLength="12" autoComplete="new-password" value={newPassword} onChange={e=>setNewPassword(e.target.value)}/></label><button className="primary">更新管理密码</button></form></section>}
    <section className="stats"><article><small>站点总数</small><strong>{sites.length.toString().padStart(2,'0')}</strong><span>已配置代理站点</span></article><article><small>运行中</small><strong className="green">{running.toString().padStart(2,'0')}</strong><span>正在接收流量</span></article><article><small>已停止</small><strong>{(sites.length-running).toString().padStart(2,'0')}</strong><span>可随时启动</span></article><article className="health"><small>系统状态</small><strong><i className="live-dot"/> HEALTHY</strong><span>SQLite 持久化 · API 正常</span></article></section>
    <section className="list-head"><div><h2>代理站点</h2><p>每个站点拥有独立监听器、TLS 与上游连接策略</p></div><label className="search">⌕ <input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索站点"/></label><a className="yaml-link" href="/api/config.yaml">导出 YAML</a><label className="yaml-upload">导入 YAML<input type="file" accept=".yaml,.yml" onChange={e=>importYaml(e.target.files?.[0])}/></label></section>
    <section className="site-list">{filtered.map(site => { const up=site.config?.upstreams?.[0]||{}; const logAdapter=logAdapters.find(adapter=>adapter.id===site.config?.accessLog?.adapterId); return <article className="site" key={site.id}><div className="site-icon">↗</div><div className="site-info"><div className="site-title">{site.name}<span className={`badge ${site.running?'on':'off'}`}><i/> {site.running?'运行中':'已停止'}</span></div><div className="site-id">{site.id}</div><div className="site-meta"><span>↗ {metrics[site.id]?.requestCount ?? 0} 请求</span><span>TTFB 均值 {Number(metrics[site.id]?.averageTtfbMillis||0).toFixed(1)} ms</span><span>完整响应均值 {Number(metrics[site.id]?.averageResponseMillis||0).toFixed(1)} ms</span><span>P95 {Number(metrics[site.id]?.p95ResponseMillis||0).toFixed(0)} ms</span><span>5xx {metrics[site.id]?.errorCount ?? 0}</span><span>日志丢弃 {metrics[site.id]?.droppedAccessLogCount ?? 0}</span><span>{metrics[site.id]?.bytesSent ?? 0} B sent</span></div><div className="site-meta"><span>◉ {site.config?.listenAddress}:{site.config?.listenPort}</span><span>⇢ {up.url}</span></div></div><div className="tags"><span>{site.config?.tls?'HTTPS':'HTTP'}</span><span>{up.proxyType || (up.proxyUrl ? '代理' : 'DIRECT')}</span><span className="log-adapter-tag" title={logAdapter?.name||'访问日志未启用'}>{logAdapter?`LOG · ${logAdapter.name}`:'LOG OFF'}</span>{up.insecureSkipVerify&&<span className="warn-tag">TLS SKIP</span>}{site.autoStart&&<span className="auto-tag">AUTO</span>}</div><div className="actions">{site.running?<button className="icon-btn" title="停止" onClick={()=>action(site,'stop')} disabled={Boolean(busy)}>Ⅱ</button>:<button className="icon-btn start" title="启动" onClick={()=>action(site,'start')} disabled={Boolean(busy)}>▶</button>}<button className="icon-btn" title="重启/热重载" onClick={()=>action(site,'reload')} disabled={Boolean(busy)}>↻</button><button className="icon-btn" title="编辑" onClick={()=>setEditing(structuredClone(site))}>✎</button><button className="icon-btn danger" title="删除" onClick={()=>remove(site)}>⌫</button></div></article> })}{!filtered.length&&<div className="empty"><div>⌘</div><b>{sites.length?'没有匹配的站点':'还没有代理站点'}</b><p>{sites.length?'调整搜索词，或清空搜索。':'新建站点后即可配置监听地址、TLS 证书和上游代理。'}</p>{!sites.length&&<button className="primary" onClick={()=>setEditing(structuredClone(blank))}>创建第一个站点</button>}</div>}</section>
    <footer>RPOP <span>·</span> Reverse Proxy over Proxy <span className="foot-right">API auto-refresh every 5 seconds</span></footer>
    {editing&&<div className="overlay" onMouseDown={e=>e.target===e.currentTarget&&setEditing(null)}><form className="modal" onSubmit={save}><div className="modal-head"><div><div className="eyebrow">SITE CONFIGURATION</div><h2>{sites.some(x=>x.id===editing.id)?'编辑站点':'新建站点'}</h2></div><button type="button" className="close" onClick={()=>setEditing(null)}>×</button></div><div className="form-grid"><label>站点 ID<input required disabled={sites.some(x=>x.id===editing.id)} value={editing.id} onChange={e=>setEditing({...editing,id:e.target.value})} placeholder="site-main"/></label><label>显示名称<input required value={editing.name} onChange={e=>setEditing({...editing,name:e.target.value})} placeholder="生产站点"/></label><label>监听地址<input required value={editing.config.listenAddress} onChange={e=>setEditing({...editing,config:{...editing.config,listenAddress:e.target.value}})}/></label><label>监听端口<input required type="number" min="1" max="65535" value={editing.config.listenPort} onChange={e=>setEditing({...editing,config:{...editing.config,listenPort:Number(e.target.value)}})}/></label><label className="wide">站点域名（Host / SNI，逗号分隔）<input value={(editing.config.hostnames||[]).join(', ')} onChange={e=>setEditing({...editing,config:{...editing.config,hostnames:e.target.value.split(',').map(v=>v.trim()).filter(Boolean)}})} placeholder="app.example.com, api.example.com"/></label><label className="wide">上游 URL<input required type="url" value={editing.config.upstreams[0].url} onChange={e=>setEditing({...editing,config:{...editing.config,upstreams:[{...editing.config.upstreams[0],url:e.target.value}]}})}/></label><label>代理 URL<input value={editing.config.upstreams[0].proxyUrl||''} onChange={e=>setEditing({...editing,config:{...editing.config,upstreams:[{...editing.config.upstreams[0],proxyUrl:e.target.value,proxyType:e.target.value?'proxy':'direct'}]}})} placeholder="socks5://127.0.0.1:1080"/></label><label>连接地址（覆盖 DNS）<input value={editing.config.upstreams[0].dialAddress||''} onChange={e=>setEditing({...editing,config:{...editing.config,upstreams:[{...editing.config.upstreams[0],dialAddress:e.target.value}]}})} placeholder="203.0.113.10:443"/></label><label>自定义 SNI<input value={editing.config.upstreams[0].serverName||''} onChange={e=>setEditing({...editing,config:{...editing.config,upstreams:[{...editing.config.upstreams[0],serverName:e.target.value}]}})} placeholder="backend.example.com"/></label><label className="check"><input type="checkbox" checked={!!editing.autoStart} onChange={e=>setEditing({...editing,autoStart:e.target.checked})}/> 程序启动时自动启动该站点 <span>程序重启后自动拉起</span></label><label className="wide">访问日志适配器<select value={editing.config.accessLog?.adapterId||''} onChange={e=>setEditing({...editing,config:{...editing.config,accessLog:{...editing.config.accessLog,adapterId:e.target.value}}})}><option value="">关闭访问日志</option>{logAdapters.map(adapter=><option key={adapter.id} value={adapter.id}>{adapter.name} · {adapter.config.adapter}</option>)}</select><span>{logAdapters.length?'选择一个适配器以启用该站点访问日志':'尚未配置日志适配器；请先到“日志适配器”添加'}</span></label><label className="check"><input type="checkbox" disabled={!editing.config.accessLog?.adapterId} checked={editing.config.accessLog?.includeBodies ?? false} onChange={e=>setEditing({...editing,config:{...editing.config,accessLog:{...editing.config.accessLog,includeBodies:e.target.checked}}})}/> 记录完整请求与响应 Body <span>默认最多记录 1 MiB；可能包含个人或业务敏感信息</span></label><label className="check"><input type="checkbox" disabled={!editing.config.accessLog?.adapterId} checked={!!editing.config.accessLog?.includeSensitiveHeaders} onChange={e=>setEditing({...editing,config:{...editing.config,accessLog:{...editing.config.accessLog,includeSensitiveHeaders:e.target.checked}}})}/> 记录敏感 Header 原值 <span>关闭时脱敏 Authorization、Cookie 等字段</span></label><label>Body 日志上限（字节，-1 表示无限）<input type="number" disabled={!editing.config.accessLog?.adapterId} min="-1" max="8388608" value={editing.config.accessLog?.maxBodyBytes||1048576} onChange={e=>setEditing({...editing,config:{...editing.config,accessLog:{...editing.config.accessLog,maxBodyBytes:Number(e.target.value)}}})}/><span>正数最大 8 MiB；-1 不截断，但会增加内存占用</span></label><label className="check"><input type="checkbox" checked={!!editing.config.tls} onChange={e=>setEditing({...editing,config:{...editing.config,tls:e.target.checked}})}/> 启用站点 HTTPS <span>上传证书与私钥并存入 SQLite</span></label>{editing.config.tls&&<><label>站点 HTTPS 证书 PEM<input type="file" accept=".pem,.crt,.cer" onChange={e=>setCertFile(e.target.files?.[0]||null)}/></label><label>站点 HTTPS 私钥 PEM<input type="file" accept=".pem,.key" onChange={e=>setKeyFile(e.target.files?.[0]||null)}/></label></>}<label className="check"><input type="checkbox" checked={!!editing.config.upstreams[0].insecureSkipVerify} onChange={e=>setEditing({...editing,config:{...editing.config,upstreams:[{...editing.config.upstreams[0],insecureSkipVerify:e.target.checked}]}})}/> 忽略上游 TLS 证书校验 <span>仅建议用于受控环境</span></label></div><div className="modal-foot"><button type="button" className="secondary" onClick={()=>setEditing(null)}>取消</button><button className="primary" disabled={busy==='save'}>{busy==='save'?'保存中…':'保存配置'}</button></div></form></div>}
  </main></div>
}
