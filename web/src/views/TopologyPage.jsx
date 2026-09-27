import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '@/api.js'
import { UiAlert, UiEmpty, UiPageHeader, UiSkeleton, UiTag } from '@/components/ui'
import {
  curveControlPoint,
  edgeColor,
  groupEdges,
  laneOffset,
  layerColor,
  layoutTopology,
} from '@/topologyLayout'
import './TopologyPage.css'

const NODE_RADIUS = 24
const LANE_SPACING = 20
const SELF_LOOP_BASE = NODE_RADIUS + 26

const STATUS_LEGEND = [
  { status: 'up', label: '正常' },
  { status: 'dialing', label: '拨号中' },
  { status: 'down', label: '故障' },
  { status: 'unknown', label: '未知' },
]

function edgePath(edge, from, to) {
  if (edge.selfLoop) {
    const bulge = SELF_LOOP_BASE + edge.lane * 16
    const top = { x: from.x, y: from.y - NODE_RADIUS * 0.6 }
    const bottom = { x: from.x, y: from.y + NODE_RADIUS * 0.6 }
    return `M ${top.x} ${top.y} C ${top.x + bulge} ${top.y - bulge}, ${bottom.x + bulge} ${bottom.y + bulge}, ${bottom.x} ${bottom.y}`
  }
  const offset = laneOffset(edge.lane)
  if (offset === 0) return `M ${from.x} ${from.y} L ${to.x} ${to.y}`
  const control = curveControlPoint(from, to, offset, LANE_SPACING)
  return `M ${from.x} ${from.y} Q ${control.x} ${control.y} ${to.x} ${to.y}`
}

function edgeTitle(edge) {
  const lines = [`${edge.from} → ${edge.to}`, `状态: ${edge.status}`]
  if (edge.proxies?.length) lines.push(`代理链: ${edge.proxies.join(' → ')}`)
  lines.push(`在途连接: ${edge.connections ?? 0} · 隧道: ${edge.tunnels ?? 0} · 失败: ${edge.failures ?? 0}`)
  if (edge.lastError) lines.push(`最近错误: ${edge.lastError}`)
  return lines.join('\n')
}

// TopologyPage draws GET /api/topology as a hand-written, dependency-free SVG (see docs/architecture/
// control-data-plane.md §5's front-end convention and "阶段 6 第 4 步实施记录" for the layering/offset rules).
export default function TopologyPage() {
  const navigate = useNavigate()
  const [data, setData] = useState(null)
  const [error, setError] = useState('')

  const refresh = useCallback(async () => {
    try {
      setData(await api('/topology'))
      setError('')
    } catch (e) {
      setError(e.message)
    }
  }, [])
  useEffect(() => {
    refresh()
    const timer = setInterval(refresh, 5000)
    return () => clearInterval(timer)
  }, [refresh])

  const layout = useMemo(() => layoutTopology(data?.nodes || []), [data])
  const edges = useMemo(() => groupEdges(data?.links || []), [data])

  return (
    <div className="topology-page">
      <UiPageHeader title="拓扑" sub="按入口 → 中继 → 出口分层展示节点与节点间链路的实时健康状态。" />
      {error && <UiAlert type="error" title="加载失败">{error}</UiAlert>}
      {!data
        ? <UiSkeleton type="block" height="360px" />
        : !layout.nodes.length
          ? <UiEmpty title="暂无节点" desc="创建节点后即可在此查看拓扑。" />
          : (
            <>
              <div className="topology-legend">
                <span className="topology-legend__group">
                  {STATUS_LEGEND.map((item) => (
                    <span key={item.status} className="topology-legend__item">
                      <span className="topology-legend__swatch" style={{ background: edgeColor(item.status) }} />
                      {item.label}
                    </span>
                  ))}
                </span>
                <span className="topology-legend__group">
                  {layout.columns.map((col) => (
                    <span key={col.layer} className="topology-legend__item">
                      <span className="topology-legend__dot" style={{ background: layerColor(col.layer) }} />
                      {col.label}
                    </span>
                  ))}
                  <span className="topology-legend__item"><span className="topology-legend__dot is-offline" />离线</span>
                  <span className="topology-legend__item"><span className="topology-legend__dot is-embedded" />内嵌节点</span>
                </span>
              </div>
              <div className="topology-scroll">
                <svg
                  className="topology-svg"
                  width={layout.width}
                  height={layout.height}
                  viewBox={`0 0 ${layout.width} ${layout.height}`}
                >
                  <defs>
                    {STATUS_LEGEND.map((item) => (
                      <marker
                        key={item.status}
                        id={`topology-arrow-${item.status}`}
                        viewBox="0 0 10 10"
                        refX="9"
                        refY="5"
                        markerWidth="7"
                        markerHeight="7"
                        orient="auto-start-reverse"
                      >
                        <path d="M0 1 L8 5 L0 9" fill="none" stroke={edgeColor(item.status)} strokeWidth="1.4" />
                      </marker>
                    ))}
                  </defs>
                  {layout.columns.map((col) => (
                    <text key={col.layer} x={col.x} y={24} textAnchor="middle" className="topology-svg__column-label">
                      {col.label}
                    </text>
                  ))}
                  {edges.map((edge, index) => {
                    const from = layout.byId.get(edge.from)
                    const to = layout.byId.get(edge.to)
                    if (!from || !to) return null
                    const marker = STATUS_LEGEND.some((s) => s.status === edge.status) ? edge.status : 'unknown'
                    return (
                      <path
                        key={`${edge.from}-${edge.to}-${edge.pairKey}-${index}`}
                        d={edgePath(edge, from, to)}
                        fill="none"
                        stroke={edgeColor(edge.status)}
                        strokeWidth={edge.connections > 0 ? 2.2 : 1.4}
                        strokeDasharray={edge.status === 'unknown' ? '4 3' : undefined}
                        markerEnd={`url(#topology-arrow-${marker})`}
                        className="topology-svg__edge"
                      >
                        <title>{edgeTitle(edge)}</title>
                      </path>
                    )
                  })}
                  {layout.nodes.map((node) => {
                    const label = node.name.length > 6 ? `${node.name.slice(0, 5)}…` : node.name
                    return (
                    <g
                      key={node.id}
                      className="topology-svg__node"
                      transform={`translate(${node.x} ${node.y})`}
                      onClick={() => navigate(`/nodes/${encodeURIComponent(node.id)}`)}
                    >
                      <circle
                        r={NODE_RADIUS}
                        fill={node.online ? 'var(--bg-card)' : 'var(--bg-tertiary)'}
                        stroke={layerColor(node.layer)}
                        strokeWidth={node.embedded ? 3 : 2}
                        strokeDasharray={node.embedded ? '4 3' : undefined}
                        opacity={node.online ? 1 : 0.55}
                      />
                      <title>{`${node.name} (${node.id})\n角色: ${(node.roles || []).join('、') || '无'}\n${node.online ? '在线' : '离线'}`}</title>
                      <text y={4} textAnchor="middle" className="topology-svg__label">{label}</text>
                      <text y={NODE_RADIUS + 16} textAnchor="middle" className="topology-svg__sub">{node.id}</text>
                    </g>
                    )
                  })}
                </svg>
              </div>
              {data.proxies?.length > 0 && (
                <div className="topology-proxies">
                  <span className="ui-cell-dim">具名代理（可能出现在上方边的代理链中）：</span>
                  {data.proxies.map((p) => <UiTag key={p.id} tone="purple">{p.name}（{p.type}）</UiTag>)}
                </div>
              )}
            </>
          )}
    </div>
  )
}
