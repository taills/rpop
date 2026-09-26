import { HEADER_MODES, effectiveRouteOrder, emptyHeaderCondition, emptyRoute, routeLabel } from '../routing.js'

// stripExample shows what "strip prefix" does to a sample request path for this rule.
function stripExample(path) {
  const pattern = path.trim()
  if (!pattern) return '需先填写路径'
  if (!pattern.endsWith('*')) return `例：${pattern} → /`
  const base = pattern.slice(0, -1)
  const prefix = base.endsWith('/') ? base.slice(0, -1) : base
  const sample = `${prefix}/users`
  return `例：${sample} → ${sample.slice(prefix.length) || '/'}`
}

function HeaderConditionRow({ header, onChange, onRemove }) {
  return <div className="header-row">
    <input aria-label="Header 名称" value={header.name} onChange={event => onChange({ ...header, name: event.target.value })} placeholder="X-Env"/>
    <select aria-label="匹配方式" value={header.mode} onChange={event => onChange({ ...header, mode: event.target.value })}>
      {HEADER_MODES.map(mode => <option key={mode.value} value={mode.value}>{mode.label}</option>)}
    </select>
    {header.mode === 'match'
      ? <input aria-label="匹配值" value={header.values.join('|')} onChange={event => onChange({ ...header, values: event.target.value.split('|') })} placeholder="canary|beta*"/>
      : <span className="header-mode-note">{header.mode === 'exists' ? '存在即可，任意值' : '请求中不能带此 Header'}</span>}
    <button type="button" className="icon-btn danger" title="删除条件" onClick={onRemove}>×</button>
  </div>
}

function RouteCard({ route, index, priority, upstreams, matched, onChange, onRemove }) {
  const set = patch => onChange({ ...route, ...patch })
  const setHeader = (position, header) => set({ headers: route.headers.map((item, i) => i === position ? header : item) })
  const hasPath = Boolean(route.path.trim())
  return <div className={`route-card${matched ? ' matched' : ''}`}>
    <div className="route-card-head">
      <span className="route-no">规则 #{index + 1}</span>
      <span className="route-priority" title="生效顺序：数字越小越先匹配">第 {priority} 优先</span>
      <code className="route-label">{routeLabel(route)}</code>
      {matched && <span className="route-hit">模拟命中</span>}
      <button type="button" className="icon-btn danger" title="删除规则" onClick={onRemove}>⌫</button>
    </div>
    <div className="route-fields">
      <label>路径匹配<input value={route.path} onChange={event => set({ path: event.target.value })} placeholder="/api/*（留空表示任意路径）"/></label>
      <label>转发到
        <select value={route.upstream} onChange={event => set({ upstream: Number(event.target.value) })}>
          {upstreams.map((upstream, i) => <option key={i} value={i}>#{i + 1} · {upstream.url || '未填写 URL'}{i === 0 ? '（默认）' : ''}</option>)}
        </select>
      </label>
      <label className="check"><input type="checkbox" checked={route.stripPrefix && hasPath} disabled={!hasPath} onChange={event => set({ stripPrefix: event.target.checked })}/> 转发前去掉匹配的路径前缀 <span>{stripExample(route.path)}</span></label>
    </div>
    <div className="route-headers">
      <div className="route-headers-title">Header 条件 <span>须全部满足；多个值用 | 分隔（任一匹配），支持 前缀*、*后缀、*包含*，值区分大小写</span></div>
      {route.headers.map((header, position) => <HeaderConditionRow key={position} header={header} onChange={next => setHeader(position, next)} onRemove={() => set({ headers: route.headers.filter((_, i) => i !== position) })}/>)}
      <button type="button" className="link-btn" onClick={() => set({ headers: [...route.headers, emptyHeaderCondition()] })}>＋ 添加 Header 条件</button>
    </div>
  </div>
}

// RouteEditor lists the site's routes in configured order and shows each rule's effective priority.
export default function RouteEditor({ routes, upstreams, matchedIndex, onChange }) {
  const order = effectiveRouteOrder(routes)
  return <div className="wide route-list">
    <p className="form-note">路径默认精确匹配，末尾加 * 为前缀匹配（/api/* 不含 /api 本身，/api* 可匹配 /api、/api/x、/apix），不区分大小写，按规范化后的路径匹配。
      优先级：路径越长越优先 → 精确优先于前缀 → Header 条件越多越优先；都未命中时转发到默认上游 #1。</p>
    {routes.map((route, index) => <RouteCard key={index} route={route} index={index} priority={order.indexOf(index) + 1} upstreams={upstreams} matched={matchedIndex === index}
      onChange={next => onChange(routes.map((item, i) => i === index ? next : item))}
      onRemove={() => onChange(routes.filter((_, i) => i !== index))}/>)}
    <button type="button" className="secondary add-route" onClick={() => onChange([...routes, emptyRoute(upstreams.length > 1 ? 1 : 0)])}>＋ 添加路由规则</button>
  </div>
}
