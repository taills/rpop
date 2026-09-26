import { useEffect, useState } from 'react'
import '../Logs.css'

function bodyText(body, encoding) {
  if (!body) return '(未记录或为空)'
  return encoding === 'base64' ? `Base64 编码的二进制内容：\n${body}` : body
}
function statusClass(status) {
  if (status >= 500) return 'bad'
  if (status >= 400) return 'warn'
  return 'good'
}
function emptyLogsMessage(loading, adapterCount) {
  if (loading) return '正在读取日志…'
  if (adapterCount === 0) return '尚无可查询的日志存储'
  return '没有匹配的访问日志'
}

export default function LogViewer({ api }) {
  const [adapters, setAdapters] = useState([])
  const [filters, setFilters] = useState({ adapterId: '', q: '', siteId: '', status: '', from: '', to: '' })
  const [result, setResult] = useState({ records: [], total: 0, page: 1, pageSize: 25 })
  const [selected, setSelected] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  async function load(page = 1, source = filters) {
    if (!source.adapterId) { setResult({ records: [], total: 0, page: 1, pageSize: 25 }); return }
    setLoading(true); setError('')
    try {
      const params = new URLSearchParams({ page: String(page), pageSize: '25' })
      if (source.siteId.trim()) params.set('siteId', source.siteId.trim())
      else params.set('adapterId', source.adapterId)
      if (source.q.trim()) params.set('q', source.q.trim())
      if (source.status) params.set('status', source.status)
      if (source.from) params.set('from', new Date(source.from).toISOString())
      if (source.to) params.set('to', new Date(source.to).toISOString())
      setResult(await api(`/logs?${params}`))
      setSelected(null)
    } catch (e) { setError(e.message) } finally { setLoading(false) }
  }

  useEffect(() => {
    let active = true
    api('/logging').then(data => {
      if (!active) return
      const list = data.adapters || []
      setAdapters(list)
      const initial = { adapterId: list[0]?.id || '', q: '', siteId: '', status: '', from: '', to: '' }
      setFilters(initial)
      if (initial.adapterId) load(1, initial)
      else setLoading(false)
    }).catch(e => { if (active) { setError(e.message); setLoading(false) } })
    return () => { active = false }
  }, [api])

  function update(name, value) { setFilters(current => ({ ...current, [name]: value })) }
  function submit(event) { event.preventDefault(); load(1) }
  function chooseAdapter(adapterId) { const next = { ...filters, adapterId }; setFilters(next); load(1, next) }
  const pages = Math.max(1, Math.ceil(result.total / result.pageSize))

  return <section className="logs-page">
    <div className="logs-heading"><div><div className="eyebrow">ACCESS LOGS</div><h1>访问日志</h1><p>选择目标适配器后，可按站点、关键词、状态码和时间范围搜索。</p></div><button className="secondary" onClick={() => load(result.page)} disabled={loading || !filters.adapterId}>刷新</button></div>
    {!adapters.length && <div className="adapter-empty">当前没有配置日志适配器。请先到“日志适配器”添加后，再为站点绑定。</div>}
    <form className="log-filters" onSubmit={submit}>
      <label>关键词<input value={filters.q} onChange={e => update('q', e.target.value)} placeholder="路径、Header 或 Body"/></label>
      <label>日志适配器<select value={filters.adapterId} onChange={e => chooseAdapter(e.target.value)} disabled={!adapters.length}><option value="">选择适配器</option>{adapters.map(adapter => <option key={adapter.id} value={adapter.id}>{adapter.name} · {adapter.config.adapter}</option>)}</select></label>
      <label>站点 ID<input value={filters.siteId} onChange={e => update('siteId', e.target.value)} placeholder="全部站点"/></label>
      <label>状态码<input type="number" min="100" max="599" value={filters.status} onChange={e => update('status', e.target.value)} placeholder="全部"/></label>
      <label>开始时间<input type="datetime-local" value={filters.from} onChange={e => update('from', e.target.value)}/></label>
      <label>结束时间<input type="datetime-local" value={filters.to} onChange={e => update('to', e.target.value)}/></label>
      <button className="primary" disabled={loading || !filters.adapterId}>{loading ? '查询中…' : '搜索日志'}</button>
    </form>
    {error && <div className="error">{error}</div>}
    <div className="logs-summary">共 {result.total} 条 · 第 {result.page} / {pages} 页</div>
    <div className="logs-table-wrap"><table className="logs-table"><thead><tr><th>时间</th><th>站点</th><th>请求</th><th>状态</th><th>完整耗时</th><th>流量</th></tr></thead><tbody>
      {result.records.map((record, index) => <tr key={`${record.timestamp}-${record.siteId}-${index}`} tabIndex="0" onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') setSelected(record) }} onClick={() => setSelected(record)} className={selected === record ? 'selected' : ''}>
        <td>{new Date(record.timestamp).toLocaleString()}</td><td>{record.siteId}</td><td><b>{record.method}</b> <span className="log-path">{record.path}</span></td><td><span className={`log-status ${statusClass(record.status)}`}>{record.status}</span></td><td>{Number(record.responseMillis || 0).toFixed(1)} ms</td><td>{record.requestBytes} ⇢ {record.responseBytes} B</td>
      </tr>)}
      {!result.records.length && <tr><td colSpan="6" className="logs-empty">{emptyLogsMessage(loading, adapters.length)}</td></tr>}
    </tbody></table></div>
    <div className="logs-pagination"><button className="secondary" disabled={loading || result.page <= 1} onClick={() => load(result.page - 1)}>上一页</button><button className="secondary" disabled={loading || result.page >= pages} onClick={() => load(result.page + 1)}>下一页</button></div>
    {selected && <div className="log-detail"><div className="logs-heading"><div><div className="eyebrow">REQUEST DETAIL</div><h2>{selected.method} {selected.path}</h2></div><button className="secondary" onClick={() => setSelected(null)}>关闭</button></div><div className="log-detail-grid"><section><h3>请求 Headers</h3><pre>{JSON.stringify(selected.requestHeaders, null, 2)}</pre><h3>请求 Body {selected.requestBodyTruncated && '(已截断)'}</h3><pre>{bodyText(selected.requestBody, selected.requestBodyEncoding)}</pre></section><section><h3>响应 Headers</h3><pre>{JSON.stringify(selected.responseHeaders, null, 2)}</pre><h3>响应 Body {selected.responseBodyTruncated && '(已截断)'}</h3><pre>{bodyText(selected.responseBody, selected.responseBodyEncoding)}</pre></section></div><small>TTFB {Number(selected.ttfbMillis || 0).toFixed(1)} ms · 完整响应 {Number(selected.responseMillis || 0).toFixed(1)} ms · 请求 {selected.requestBodyTotalBytes || selected.requestBytes} B · 响应 {selected.responseBodyTotalBytes || selected.responseBytes} B</small></div>}
  </section>
}
