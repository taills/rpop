import { useCallback, useEffect, useRef, useState } from 'react'
import LogDetailDrawer, { statusClass } from './LogDetailDrawer.jsx'
import { clientIP, forwardedFor, userAgent } from '../logRecord.js'
import { UiButton, UiCard, UiPageHeader, UiSpinner } from '@/components/ui'
import { cx } from '@/utils/cx'
import '../Logs.css'

function emptyLogsMessage(loading, adapterCount) {
  if (loading) return '正在读取日志…'
  if (adapterCount === 0) return '尚无可查询的日志存储'
  return '没有匹配的访问日志'
}

export default function LogViewer({ api }) {
  const [adapters, setAdapters] = useState([])
  const [filters, setFilters] = useState({ adapterId: '', q: '', siteId: '', status: '', from: '', to: '' })
  const [result, setResult] = useState({ records: [], total: 0, page: 1, pageSize: 25 })
  const [selectedIndex, setSelectedIndex] = useState(-1)
  const rowRefs = useRef([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  // loadingRef mirrors `loading` but synchronously: a click handler reads it before React has had a chance to
  // re-render the disabled buttons, which is the only way to actually stop a second click (double-click, or an
  // impatient extra click while the network is slow) from firing a second /logs request — `disabled={loading}`
  // alone only takes effect on the next render, one tick too late. requestRef instead orders responses: paging/
  // filtering can still overlap on purpose (e.g. picking a different adapter while a page is still loading, see
  // chooseAdapter below), so whichever request was started last is the only one allowed to apply its result,
  // and an older one that resolves afterwards is discarded instead of clobbering newer data (same pattern as
  // TracePage's useTraceQuery / NodeDetailPage's refresh).
  const loadingRef = useRef(false)
  const requestRef = useRef(0)

  async function load(page = 1, source = filters) {
    const requestId = ++requestRef.current
    loadingRef.current = true
    setLoading(true)
    setError('')
    if (!source.adapterId) {
      if (requestRef.current === requestId) { setResult({ records: [], total: 0, page: 1, pageSize: 25 }); loadingRef.current = false; setLoading(false) }
      return
    }
    try {
      const params = new URLSearchParams({ page: String(page), pageSize: '25' })
      if (source.siteId.trim()) params.set('siteId', source.siteId.trim())
      else params.set('adapterId', source.adapterId)
      if (source.q.trim()) params.set('q', source.q.trim())
      if (source.status) params.set('status', source.status)
      if (source.from) params.set('from', new Date(source.from).toISOString())
      if (source.to) params.set('to', new Date(source.to).toISOString())
      const next = await api(`/logs?${params}`)
      if (requestRef.current !== requestId) return // a newer request already started; this response is stale
      setResult(next)
      setSelectedIndex(-1)
    } catch (e) {
      if (requestRef.current !== requestId) return
      setError(e.message)
    } finally {
      if (requestRef.current === requestId) { loadingRef.current = false; setLoading(false) }
    }
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
  // submit/goToPage/refresh all repeat the exact same request a moment later if clicked again, so they check
  // loadingRef and no-op while one is already running. chooseAdapter is a deliberate change of query target
  // instead — it is allowed to interrupt an in-flight page load rather than being swallowed by it.
  function submit(event) { event.preventDefault(); if (loadingRef.current) return; load(1) }
  function goToPage(page) { if (loadingRef.current) return; load(page) }
  function refresh() { if (loadingRef.current) return; load(result.page) }
  function chooseAdapter(adapterId) { const next = { ...filters, adapterId }; setFilters(next); load(1, next) }
  const pages = Math.max(1, Math.ceil(result.total / result.pageSize))
  const selected = result.records[selectedIndex] || null
  const closeDetail = useCallback(() => {
    rowRefs.current[selectedIndex]?.focus()
    setSelectedIndex(-1)
  }, [selectedIndex])
  const showPrevious = useCallback(() => setSelectedIndex(index => Math.max(0, index - 1)), [])
  const showNext = useCallback(() => setSelectedIndex(index => Math.min(result.records.length - 1, index + 1)), [result.records.length])

  // Keep the highlighted row visible behind the drawer while stepping through records.
  useEffect(() => {
    rowRefs.current[selectedIndex]?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  return <div className="ui-page">
    <UiPageHeader
      eyebrow="ACCESS LOGS"
      title="访问日志"
      sub="选择目标适配器后，可按站点、关键词、状态码和时间范围搜索。"
      actions={<UiButton variant="outline" loading={loading} onClick={refresh} disabled={!filters.adapterId}>刷新</UiButton>}
    />
    {!adapters.length && <div className="adapter-empty">当前没有配置日志适配器。请先到“日志适配器”添加后，再为站点绑定。</div>}
    <UiCard>
      <form className="log-filters" onSubmit={submit}>
        <label>关键词<input value={filters.q} onChange={e => update('q', e.target.value)} placeholder="路径、IP、UA、Header 或 Body"/></label>
        <label>日志适配器<select value={filters.adapterId} onChange={e => chooseAdapter(e.target.value)} disabled={!adapters.length}><option value="">选择适配器</option>{adapters.map(adapter => <option key={adapter.id} value={adapter.id}>{adapter.name} · {adapter.config.adapter}</option>)}</select></label>
        <label>站点 ID<input value={filters.siteId} onChange={e => update('siteId', e.target.value)} placeholder="全部站点"/></label>
        <label>状态码<input type="number" min="100" max="599" value={filters.status} onChange={e => update('status', e.target.value)} placeholder="全部"/></label>
        <label>开始时间<input type="datetime-local" value={filters.from} onChange={e => update('from', e.target.value)}/></label>
        <label>结束时间<input type="datetime-local" value={filters.to} onChange={e => update('to', e.target.value)}/></label>
        <button className="primary" disabled={loading || !filters.adapterId} aria-busy={loading || undefined}>{loading ? '查询中…' : '搜索日志'}</button>
      </form>
      {error && <div className="error">{error}</div>}
      <div className="logs-summary">共 {result.total} 条 · 第 {result.page} / {pages} 页{loading && '（更新中…）'}</div>
      <div className={cx('logs-table-wrap', loading && 'is-loading')}>
        {loading && <div className="logs-table-wrap__loading" role="status"><UiSpinner size="sm" label="正在加载"/>加载中…</div>}
        <table className="logs-table"><thead><tr><th>时间</th><th>站点</th><th>客户端 IP</th><th>请求</th><th>状态</th><th>完整耗时</th><th>流量</th><th>User-Agent</th></tr></thead><tbody>
        {result.records.map((record, index) => <tr key={`${record.timestamp}-${record.siteId}-${index}`} ref={element => { rowRefs.current[index] = element }} tabIndex="0" onKeyDown={event => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); setSelectedIndex(index) } }} onClick={() => setSelectedIndex(index)} className={selectedIndex === index ? 'selected' : ''}>
          <td>{new Date(record.timestamp).toLocaleString()}</td><td>{record.siteId}</td><td title={forwardedFor(record) ? `X-Forwarded-For: ${forwardedFor(record)}` : undefined}>{clientIP(record) || '-'}</td><td><b>{record.method}</b> <span className="log-path" title={record.host ? `${record.host}${record.path}` : record.path}>{record.path}</span></td><td><span className={`log-status ${statusClass(record.status)}`}>{record.status}</span></td><td>{Number(record.responseMillis || 0).toFixed(1)} ms</td><td>{record.requestBytes} ⇢ {record.responseBytes} B</td><td><span className="log-ua" title={userAgent(record)}>{userAgent(record) || '-'}</span></td>
        </tr>)}
        {!result.records.length && <tr><td colSpan="8" className="logs-empty">{emptyLogsMessage(loading, adapters.length)}</td></tr>}
      </tbody></table></div>
      <div className="logs-pagination">
        <button className="secondary" disabled={loading || result.page <= 1} aria-busy={loading || undefined} onClick={() => goToPage(result.page - 1)}>{loading ? <span className="logs-pagination__pending"><UiSpinner size="sm" label="加载中"/>加载中…</span> : '上一页'}</button>
        <button className="secondary" disabled={loading || result.page >= pages} aria-busy={loading || undefined} onClick={() => goToPage(result.page + 1)}>{loading ? <span className="logs-pagination__pending"><UiSpinner size="sm" label="加载中"/>加载中…</span> : '下一页'}</button>
      </div>
    </UiCard>
    {selected && <LogDetailDrawer record={selected} position={selectedIndex + 1} total={result.records.length} onPrevious={selectedIndex > 0 ? showPrevious : null} onNext={selectedIndex < result.records.length - 1 ? showNext : null} onClose={closeDetail}/>}
  </div>
}
