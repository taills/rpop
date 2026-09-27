import { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import '../Rpop.css'
import '../Admin.css'
import SiteListItem from '../components/SiteListItem.jsx'
import SiteEditor from '../components/SiteEditor.jsx'
import { addUpstream, applySections, blankUpstream, makeDefaultUpstream, prepareSiteForEditing, removeUpstream, sectionsForSite, uploadSlot, validateSections } from '../siteForm.js'
import { clientCertificateUploads, planSecretUploads, referencedSecrets, stagedConfig } from '../siteSecrets.js'
import { useToast } from '../stores/toast.js'

const blank = { id: '', name: '', autoStart: false, config: { listenAddress: '127.0.0.1', listenPort: 8081, tls: false, certificateId: '', hostnames: [], nodes: [], accessLog: { adapterId: '', includeBodies: false, maxBodyBytes: 1048576 }, upstreams: [blankUpstream()], routes: [] } }

// SitesPage owns the site list and the site editor modal; it is the console's landing page. Data fetching lives
// here (rather than a shared store) because nothing outside this page needs it live: the editor re-fetches the
// certificate/log-adapter catalog itself whenever it opens, so edits made on other pages show up the next time
// this page polls (every 5s, same cadence as before the router split) or the editor is reopened.
export default function SitesPage() {
  const { toast } = useToast()
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
  const [nodes, setNodes] = useState([])
  const [proxies, setProxies] = useState([])

  const refresh = useCallback(async () => {
    try {
      const [list, logging] = await Promise.all([api('/sites'), api('/logging')])
      setSites(list); setLogAdapters(logging.adapters || [])
      const entries = await Promise.all(list.map(async site => [site.id, await api(`/sites/${encodeURIComponent(site.id)}/metrics`).catch(() => null)]))
      setMetrics(Object.fromEntries(entries.filter(([, value]) => value))); setError('')
    } catch (e) { setError(e.message) }
  }, [])
  const refreshSystemSettings = useCallback(async () => {
    try {
      const [settings, nodeList, proxyList] = await Promise.all([api('/settings'), api('/nodes'), api('/proxies')])
      setRootCertificates(settings.rootCertificates || []); setClientCertificates(settings.clientCertificates || []); setServerCertificates(settings.serverCertificates || [])
      setNodes(nodeList || []); setProxies(proxyList || [])
    } catch (e) { setError(e.message) }
  }, [])
  useEffect(() => { refresh(); refreshSystemSettings(); const timer = setInterval(refresh, 5000); return () => clearInterval(timer) }, [refresh, refreshSystemSettings])

  async function action(site, op) { setBusy(`${site.id}:${op}`); try { await api(`/sites/${encodeURIComponent(site.id)}/${op}`, { method: 'POST' }); await refresh() } catch (e) { toast.error(e.message) } finally { setBusy('') } }
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
      if (cleanupWarning) toast.warn(cleanupWarning)
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
  async function importYaml(file) { if (!file) return; try { await api('/config.yaml', { method: 'PUT', headers: { 'Content-Type': 'application/yaml' }, body: file }); await refresh() } catch (e) { toast.error(e.message) } }
  async function remove(site) { if (!window.confirm(`确定删除站点“${site.name}”？`)) return; try { await api(`/sites/${encodeURIComponent(site.id)}`, { method: 'DELETE' }); await refresh() } catch (e) { toast.error(e.message) } }

  const filtered = sites.filter(x => `${x.name} ${x.id} ${(x.config?.upstreams || []).map(up => up.url).join(' ')}`.toLowerCase().includes(query.toLowerCase()))
  const running = sites.filter(x => x.running).length

  return <div className="main">
    <section className="hero"><div><div className="eyebrow">REVERSE PROXY OVER PROXY</div><h1>站点代理管理</h1><p>统一管理反向代理站点与独立出站链路。</p></div><button className="primary" onClick={() => openEditor(blank)}>＋ 新建站点</button></section>
    {error && <div className="error">{error}<button onClick={() => setError('')}>×</button></div>}
    <section className="stats"><article><small>站点总数</small><strong>{sites.length.toString().padStart(2, '0')}</strong><span>已配置代理站点</span></article><article><small>运行中</small><strong className="green">{running.toString().padStart(2, '0')}</strong><span>正在接收流量</span></article><article><small>已停止</small><strong>{(sites.length - running).toString().padStart(2, '0')}</strong><span>可随时启动</span></article><article className="health"><small>系统状态</small><strong><i className="live-dot"/> HEALTHY</strong><span>SQLite 持久化 · API 正常</span></article></section>
    <section className="list-head"><div><h2>代理站点</h2><p>每个站点拥有独立监听器、TLS 与上游连接策略</p></div><label className="search">⌕ <input value={query} onChange={e => setQuery(e.target.value)} placeholder="搜索站点"/></label><a className="yaml-link" href="/api/config.yaml">导出 YAML</a><label className="yaml-upload">导入 YAML<input type="file" accept=".yaml,.yml" onChange={e => importYaml(e.target.files?.[0])}/></label></section>
    <section className="site-list">
      {filtered.map(site => <SiteListItem key={site.id} site={site} metrics={metrics[site.id]} logAdapters={logAdapters} busy={busy} onAction={action} onEdit={openEditor} onRemove={remove}/>)}
      {!filtered.length && <div className="empty"><div>⌘</div><b>{sites.length ? '没有匹配的站点' : '还没有代理站点'}</b><p>{sites.length ? '调整搜索词，或清空搜索。' : '新建站点后即可配置监听地址、TLS 证书和上游代理。'}</p>{!sites.length && <button className="primary" onClick={() => openEditor(blank)}>创建第一个站点</button>}</div>}
    </section>
    <footer>RPOP <span>·</span> Reverse Proxy over Proxy <span className="foot-right">API auto-refresh every 5 seconds</span></footer>
    {editing && <SiteEditor site={editing} isNew={!sites.some(x => x.id === editing.id)} sections={sections} catalog={{ rootCertificates, clientCertificates, serverCertificates, logAdapters, nodes, proxies }} saving={busy === 'save'} error={formError} upstreamFiles={upstreamFiles} onChange={setEditing} onSectionsChange={setSections} setFile={(name, file) => fileSetters[name](file)} setUpstreamFile={setUpstreamFile} onAddUpstream={() => changeUpstreams(addUpstream)} onRemoveUpstream={removeUpstreamAt} onMakeDefault={index => changeUpstreams(state => makeDefaultUpstream(state, index))} onSimulate={simulateRoute} onCancel={closeEditor} onSubmit={save}/>}
  </div>
}
