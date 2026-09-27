import { UiTag } from '@/components/ui'
import './NodeHealthBlocks.css'
import { formatBytes, logsNeedAttention, summarizeLinks, summarizePaths } from '@/nodeHealth'

// Compact per-node health summaries for the list page (NodesPage); the full detail lives in
// NodeLinksTable/NodePathsTable/NodeLogHealth on NodeDetailPage.

export function LinkHealthSummary({ links = [] }) {
  const s = summarizeLinks(links)
  if (!s.total) return <span className="ui-cell-dim">无链路</span>
  return (
    <div className="health-summary">
      <UiTag tone="success">{s.up} 正常</UiTag>
      {s.dialing > 0 && <UiTag tone="warn">{s.dialing} 拨号中</UiTag>}
      {s.down > 0 && <UiTag tone="risk-high" icon="alert">{s.down} 故障</UiTag>}
    </div>
  )
}

export function PathHealthSummary({ paths = [] }) {
  const s = summarizePaths(paths)
  if (!s.totalUpstreams) return <span className="ui-cell-dim">无候选路径</span>
  return (
    <div className="health-summary">
      <span className="ui-cell-dim">{s.totalUpstreams} 个上游</span>
      {s.coolingPaths > 0
        ? <UiTag tone="warn" icon="alert">{s.coolingPaths} 条冷却中</UiTag>
        : <UiTag tone="success">全部正常</UiTag>}
    </div>
  )
}

export function LogHealthSummary({ logs }) {
  if (!logs) return <span className="ui-cell-dim">—</span>
  return (
    <div className="health-summary">
      <span className="ui-cell-dim">{logs.pendingSegments ?? 0} 段 / {formatBytes(logs.pendingBytes)}</span>
      {logsNeedAttention(logs) && <UiTag tone="risk-high" icon="alert">异常</UiTag>}
    </div>
  )
}
