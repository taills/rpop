import { useEffect, useRef, useState } from 'react'
import { normalizeRoute, parseHeaderLines, validateRoutes } from '../routing.js'
import SimulatedPaths from './SimulatedPaths.jsx'

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS']
const SIMULATE_DELAY_MS = 350
const CHECK_TEXT = { matched: '✓ 命中', path: '路径不匹配', skipped: '未评估（已命中更高优先级规则）' }

function checkText(check) {
  return check.result === 'header' ? `Header ${check.header} 不满足` : CHECK_TEXT[check.result] || check.result
}

function SimulationSteps({ method, result }) {
  return <ol className="sim-steps">
    <li><small>原始请求</small><code>{method} {result.requestUri}</code></li>
    <li><small>用于匹配的规范化路径</small><code>{result.matchPath}</code></li>
    <li><small>{result.stripPrefix ? <>去掉前缀 <code>{result.stripPrefix}</code></> : '路径前缀'}</small>{result.stripPrefix ? <code>{result.strippedPath}</code> : <span>未去除，原样转发</span>}</li>
    <li><small>拼接上游 URL 路径</small><code>{result.upstreamUri}</code></li>
    <li className="final"><small>最终转发地址</small><code>{result.finalUrl}</code></li>
  </ol>
}

function SimulationResult({ method, result }) {
  return <div className="sim-result">
    <div className={`sim-verdict ${result.matched ? 'hit' : 'miss'}`}>
      {result.matched ? <>命中 <b>规则 #{result.routeIndex + 1}</b> <code>{result.route}</code></> : <>未命中任何规则，使用<b>默认上游</b></>}
      <span>→ 上游 #{result.upstream + 1} <code>{result.upstreamUrl}</code></span>
    </div>
    <div className="sim-rewrite" aria-label="路径改写前后">
      <div><small>改写前</small><code>{result.requestUri}</code></div><span className="sim-arrow">→</span><div><small>改写后</small><code>{result.upstreamUri}</code></div>
    </div>
    <SimulationSteps method={method} result={result}/>
    <table className="sim-checks">
      <thead><tr><th>顺序</th><th>规则</th><th>目标</th><th>结果</th></tr></thead>
      <tbody>
        {result.checks.map((check, order) => <tr key={check.index} className={`sim-${check.result}`}><td>{order + 1}</td><td>#{check.index + 1} <code>{check.label}</code></td><td>#{check.upstream + 1}</td><td>{checkText(check)}</td></tr>)}
        {!result.checks.length && <tr><td colSpan="4">尚未配置规则，所有请求使用默认上游。</td></tr>}
      </tbody>
    </table>
  </div>
}

// RouteSimulator replays the unsaved rules on the server for a sample request and shows how it would be forwarded.
export default function RouteSimulator({ site, onSimulate, onMatch }) {
  const [method, setMethod] = useState('GET')
  const [url, setURL] = useState(() => `http://${site.config.hostnames?.[0] || 'example.com'}/`)
  const [headerText, setHeaderText] = useState('')
  const [result, setResult] = useState(null)
  const [error, setError] = useState('')
  const sequence = useRef(0)
  // via/paths ride along unmodified so the simulator can resolve the matched upstream's full candidate paths
  // (docs/architecture/control-data-plane.md §5, "API 契约(6.5 定形)"); siteId/nodes only correlate the result
  // with this site's live path health on its entry node and are harmless to send for an unsaved site (empty id
  // just means every path reports "unknown" health, see addPathSimulation on the server).
  const upstreams = site.config.upstreams.map(upstream => ({ url: upstream.url, via: upstream.via, paths: upstream.paths }))
  const routes = site.config.routes || []
  const request = JSON.stringify({ siteId: site.id, config: { nodes: site.config.nodes, upstreams, routes: routes.map(normalizeRoute) }, method, url })
  const localError = validateRoutes(routes, upstreams.length) || parseHeaderLines(headerText).error || (url.trim() ? '' : '请输入要模拟的 URL')

  useEffect(() => {
    const current = ++sequence.current
    if (localError) { setError(localError); setResult(null); onMatch(null); return undefined }
    const timer = setTimeout(async () => {
      try {
        const next = await onSimulate({ ...JSON.parse(request), headers: parseHeaderLines(headerText).headers })
        if (current !== sequence.current) return
        setResult(next); setError(''); onMatch(next.matched ? next.routeIndex : null)
      } catch (e) {
        if (current !== sequence.current) return
        setResult(null); setError(e.message); onMatch(null)
      }
    }, SIMULATE_DELAY_MS)
    return () => clearTimeout(timer)
  }, [request, headerText, localError, onSimulate, onMatch])

  return <div className="wide route-simulator">
    <div className="sim-inputs">
      <select aria-label="请求方法" value={method} onChange={event => setMethod(event.target.value)}>{METHODS.map(item => <option key={item}>{item}</option>)}</select>
      <input aria-label="模拟 URL" value={url} onChange={event => setURL(event.target.value)} placeholder="https://app.example.com/api/users?id=1 或 /api/users"/>
    </div>
    <label>请求 Header（每行一个“名称: 值”，可用 Host 覆盖域名）
      <textarea rows="2" value={headerText} onChange={event => setHeaderText(event.target.value)} placeholder={'X-Env: canary\nUser-Agent: Googlebot'}/>
    </label>
    {error ? <div className="sim-error" role="status">{error}</div> : result ? <>
      <SimulationResult method={method} result={result}/>
      <SimulatedPaths result={result}/>
    </> : <div className="sim-pending">正在模拟…</div>}
  </div>
}
