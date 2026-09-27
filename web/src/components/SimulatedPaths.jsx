import { Fragment } from 'react'
import { UiStatusDot, UiTag } from '@/components/ui'
import { buildSimulatedPaths } from '../routeSimulatorPaths.js'
import './SimulatedPaths.css'

function Hop({ hop }) {
  return <span className={`sim-hop sim-hop-${hop.kind}`} title={hop.link?.lastError || undefined}>
    {hop.kind === 'proxy' ? <UiTag tone="purple">代理</UiTag> : null}
    <code>{hop.id || '(未命名)'}</code>
    {hop.kind === 'node' && <UiStatusDot tone={hop.onlineTone}>{hop.online === false ? '离线' : '在线'}</UiStatusDot>}
    {hop.link && <UiStatusDot tone={hop.link.tone}>链路 {hop.link.label}</UiStatusDot>}
  </span>
}

function PathRow({ path }) {
  return <div className={`sim-path-row${path.selected ? ' selected' : ''}`}>
    <div className="sim-path-row-head">
      <span className="sim-path-index">路径 #{path.index + 1}</span>
      <UiStatusDot tone={path.tone}>{path.statusLabel}</UiStatusDot>
      {path.status === 'cooling' && path.until && <span className="sim-path-until">冷却至 {path.until}</span>}
      {path.selected && <UiTag tone="success" icon="check">当前选中</UiTag>}
    </div>
    <div className="sim-path-hops">
      {path.hops.map((hop, index) => <Fragment key={hop.key}>
        {index > 0 && <span className="sim-hop-arrow" aria-hidden="true">→</span>}
        <Hop hop={hop}/>
      </Fragment>)}
      <span className="sim-hop-arrow" aria-hidden="true">→</span>
      <span className="sim-hop sim-hop-target"><code>{path.label}</code></span>
    </div>
    {path.selected && path.reason && <div className="sim-path-reason">{path.reason}</div>}
  </div>
}

// SimulatedPaths renders the "全路径" section appended after RouteSimulator's existing single-hop result: every
// candidate path of the matched upstream, its full hop chain with live health, and which one the live proxy
// would pick right now. It renders nothing when the response has no paths field (no candidate paths configured,
// or an upstream reached without any), leaving the existing single-hop result untouched above it.
export default function SimulatedPaths({ result }) {
  const view = buildSimulatedPaths(result)
  if (!view) return null
  return <div className="sim-paths">
    <div className="sim-paths-head"><small>全路径</small><span>{view.paths.length} 条候选路径，按优先级排列</span></div>
    {view.paths.map(path => <PathRow key={path.index} path={path}/>)}
  </div>
}
