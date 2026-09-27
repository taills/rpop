import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { clientAddress, forwardedFor, protocolLabel, referer, userAgent } from '../logRecord.js'

export function bodyText(body, encoding) {
  if (!body) return '(未记录或为空)'
  return encoding === 'base64' ? `Base64 编码的二进制内容：\n${body}` : body
}
export function statusClass(status) {
  if (status >= 500) return 'bad'
  if (status >= 400) return 'warn'
  return 'good'
}
function headersText(headers) {
  return headers && Object.keys(headers).length ? JSON.stringify(headers, null, 2) : '(无)'
}
function millis(value) {
  return `${Number(value || 0).toFixed(1)} ms`
}

function MessagePanel({ headers, body, encoding, truncated, totalBytes }) {
  return <div className="log-drawer-panel">
    <h3>Headers</h3>
    <pre>{headersText(headers)}</pre>
    <h3>Body {truncated && <span className="log-drawer-flag">已截断</span>}{totalBytes > 0 && <span className="log-drawer-muted">共 {totalBytes} B</span>}</h3>
    <pre>{bodyText(body, encoding)}</pre>
  </div>
}

// LogDetailDrawer shows one access log record beside the list so the page never has to scroll to reach the details.
export default function LogDetailDrawer({ record, position, total, onPrevious, onNext, onClose }) {
  const [tab, setTab] = useState('request')
  const drawerRef = useRef(null)
  const bodyRef = useRef(null)
  const navigate = useNavigate()

  useEffect(() => {
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    drawerRef.current?.focus()
    return () => { document.body.style.overflow = previousOverflow }
  }, [])

  useEffect(() => { bodyRef.current?.scrollTo?.(0, 0) }, [record, tab])

  useEffect(() => {
    function onKeyDown(event) {
      if (event.key === 'Escape') onClose()
      else if (event.key === 'ArrowUp' && onPrevious) { event.preventDefault(); onPrevious() }
      else if (event.key === 'ArrowDown' && onNext) { event.preventDefault(); onNext() }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose, onPrevious, onNext])

  const request = { headers: record.requestHeaders, body: record.requestBody, encoding: record.requestBodyEncoding, truncated: record.requestBodyTruncated, totalBytes: record.requestBodyTotalBytes || record.requestBytes }
  const response = { headers: record.responseHeaders, body: record.responseBody, encoding: record.responseBodyEncoding, truncated: record.responseBodyTruncated, totalBytes: record.responseBodyTotalBytes || record.responseBytes }

  return <div className="log-drawer-root">
    <div className="log-drawer-backdrop" onClick={onClose}/>
    <aside className="log-drawer" role="dialog" aria-modal="true" aria-labelledby="log-drawer-title" tabIndex="-1" ref={drawerRef}>
      <header className="log-drawer-header">
        <div className="log-drawer-title">
          <div className="eyebrow">REQUEST DETAIL · {position} / {total}</div>
          <h2 id="log-drawer-title"><span className={`log-status ${statusClass(record.status)}`}>{record.status}</span> <b>{record.method}</b> <span className="log-drawer-path" title={record.path}>{record.path}</span></h2>
        </div>
        <div className="log-drawer-nav">
          <button type="button" className="secondary" onClick={onPrevious} disabled={!onPrevious} title="上一条（↑）" aria-label="上一条">↑</button>
          <button type="button" className="secondary" onClick={onNext} disabled={!onNext} title="下一条（↓）" aria-label="下一条">↓</button>
          <button type="button" className="close" onClick={onClose} title="关闭（Esc）" aria-label="关闭详情">×</button>
        </div>
      </header>
      <dl className="log-drawer-meta">
        <div><dt>时间</dt><dd>{new Date(record.timestamp).toLocaleString()}</dd></div>
        <div><dt>站点</dt><dd>{record.siteId}</dd></div>
        <div><dt>客户端</dt><dd title={clientAddress(record)}>{clientAddress(record) || '-'}</dd></div>
        <div><dt>Host</dt><dd title={record.host}>{record.host || '-'}</dd></div>
        <div><dt>协议</dt><dd title={protocolLabel(record)}>{protocolLabel(record) || '-'}</dd></div>
        <div><dt>TTFB</dt><dd>{millis(record.ttfbMillis)}</dd></div>
        <div><dt>完整响应</dt><dd>{millis(record.responseMillis)}</dd></div>
        <div><dt>请求大小</dt><dd>{request.totalBytes || 0} B</dd></div>
        <div><dt>响应大小</dt><dd>{response.totalBytes || 0} B</dd></div>
        <div className="wide"><dt>X-Forwarded-For</dt><dd className="wrap">{forwardedFor(record) || '-'}</dd></div>
        <div className="wide"><dt>上游</dt><dd className="wrap">{record.upstream || '-'}</dd></div>
        <div className="wide"><dt>路由规则</dt><dd className="wrap">{record.route || (record.upstream ? '未命中规则（默认上游）' : '-')}</dd></div>
        <div className="wide"><dt>Track ID</dt><dd className="wrap">{record.trackId ? <button type="button" className="log-drawer-link" onClick={() => navigate(`/trace/${record.trackId}`)}>{record.trackId}</button> : '-'}</dd></div>
        <div className="wide"><dt>Tunnel ID</dt><dd className="wrap">{record.tunnelId ? <button type="button" className="log-drawer-link" onClick={() => navigate(`/trace?tunnel=${record.tunnelId}`)}>{record.tunnelId}</button> : '-（直连出口，未经过隧道）'}</dd></div>
        <div className="wide"><dt>Referer</dt><dd className="wrap">{referer(record) || '-'}</dd></div>
        <div className="wide"><dt>User-Agent</dt><dd className="wrap">{userAgent(record) || '-'}</dd></div>
      </dl>
      <div className="log-drawer-tabs" role="tablist" aria-label="请求与响应">
        <button type="button" role="tab" aria-selected={tab === 'request'} onClick={() => setTab('request')}>请求</button>
        <button type="button" role="tab" aria-selected={tab === 'response'} onClick={() => setTab('response')}>响应</button>
      </div>
      <div className="log-drawer-body" role="tabpanel" ref={bodyRef}>
        <MessagePanel {...(tab === 'request' ? request : response)}/>
      </div>
      <footer className="log-drawer-footer">↑ / ↓ 切换记录 · Esc 关闭</footer>
    </aside>
  </div>
}
