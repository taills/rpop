// SiteListItem renders one row of the site list: status, live metrics, tags and the row-level actions. Split out
// of SitesPage so that component stays focused on data flow rather than markup.
export default function SiteListItem({ site, metrics, logAdapters, busy, onAction, onEdit, onRemove }) {
  const upstreams = site.config?.upstreams || []
  const upstream = upstreams[0] || {}
  const routeCount = (site.config?.routes || []).length
  const logAdapter = logAdapters.find((adapter) => adapter.id === site.config?.accessLog?.adapterId)
  return <article className="site">
    <div className="site-icon">↗</div>
    <div className="site-info">
      <div className="site-title">{site.name}<span className={`badge ${site.running ? 'on' : 'off'}`}><i/> {site.running ? '运行中' : '已停止'}</span></div>
      <div className="site-id">{site.id}</div>
      <div className="site-meta">
        <span>↗ {metrics?.requestCount ?? 0} 请求</span>
        <span>TTFB 均值 {Number(metrics?.averageTtfbMillis || 0).toFixed(1)} ms</span>
        <span>完整响应均值 {Number(metrics?.averageResponseMillis || 0).toFixed(1)} ms</span>
        <span>P95 {Number(metrics?.p95ResponseMillis || 0).toFixed(0)} ms</span>
        <span>5xx {metrics?.errorCount ?? 0}</span>
        <span>日志丢弃 {metrics?.droppedAccessLogCount ?? 0}</span>
        <span>{metrics?.bytesSent ?? 0} B sent</span>
      </div>
      <div className="site-meta">
        <span>◉ {site.config?.listenAddress}:{site.config?.listenPort}</span>
        <span>⇢ {upstream.url}{upstreams.length > 1 && ` 等 ${upstreams.length} 个上游`}</span>
      </div>
    </div>
    <div className="tags">
      <span>{site.config?.tls ? 'HTTPS' : 'HTTP'}</span>
      <span>{upstream.proxyType || (upstream.proxyUrl ? '代理' : 'DIRECT')}</span>
      {routeCount > 0 && <span className="route-tag">{routeCount} 条路由</span>}
      <span className="log-adapter-tag" title={logAdapter?.name || '访问日志未启用'}>{logAdapter ? `LOG · ${logAdapter.name}` : 'LOG OFF'}</span>
      {upstreams.some((item) => item.clientCertificateId || item.clientCertSecret) && <span>mTLS</span>}
      {upstreams.some((item) => item.insecureSkipVerify) && <span className="warn-tag">TLS SKIP</span>}
      {site.autoStart && <span className="auto-tag">AUTO</span>}
    </div>
    <div className="actions">
      {site.running
        ? <button className="icon-btn" title="停止" onClick={() => onAction(site, 'stop')} disabled={Boolean(busy)}>Ⅱ</button>
        : <button className="icon-btn start" title="启动" onClick={() => onAction(site, 'start')} disabled={Boolean(busy)}>▶</button>}
      <button className="icon-btn" title="重启/热重载" onClick={() => onAction(site, 'reload')} disabled={Boolean(busy)}>↻</button>
      <button className="icon-btn" title="编辑" onClick={() => onEdit(site)}>✎</button>
      <button className="icon-btn danger" title="删除" onClick={() => onRemove(site)}>⌫</button>
    </div>
  </article>
}
