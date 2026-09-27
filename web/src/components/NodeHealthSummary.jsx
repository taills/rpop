import { UiTag, UiTooltip } from '@/components/ui'
import './NodeHealthBlocks.css'
import {
  clockSkewStatusLabel,
  clockSkewStatusTone,
  formatBytes,
  formatClockSkew,
  logsNeedAttention,
  protocolStatusLabel,
  protocolStatusTone,
  summarizeLinks,
  summarizePaths,
} from '@/nodeHealth'

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

// ProtocolHealthSummary renders nodeView/topologyNode's D27 protocolVersion/protocolStatus: a plain version
// number when current, a warn-toned tag with an explanatory tooltip once the controller judges the node
// outdated, and an em dash before the node's first authenticated southbound call ever reports one.
export function ProtocolHealthSummary({ protocolVersion, protocolStatus }) {
  if (!protocolStatus) return <span className="ui-cell-dim">—</span>
  const versionLabel = `v${protocolVersion}`
  if (protocolStatus !== 'outdated') return <span className="ui-cell-dim">{versionLabel}</span>
  return (
    <UiTooltip content="节点协议版本落后，建议升级节点">
      <UiTag tone={protocolStatusTone(protocolStatus)} icon="alert">{versionLabel} · {protocolStatusLabel(protocolStatus)}</UiTag>
    </UiTooltip>
  )
}

// ClockSkewSummary renders nodeView/topologyNode's D28 clockSkewMillis/clockSkewStatus: the formatted offset,
// warn-toned with a tooltip once it exceeds the controller's configured threshold, "未知" (via formatClockSkew)
// before the node's first status report carries one.
export function ClockSkewSummary({ clockSkewMillis, clockSkewStatus }) {
  const label = formatClockSkew(clockSkewMillis)
  if (clockSkewStatus !== 'warn') return <span className="ui-cell-dim">{label}</span>
  return (
    <UiTooltip content="跨节点时间受该节点时钟偏差影响">
      <UiTag tone={clockSkewStatusTone(clockSkewStatus)} icon="alert">{label} · {clockSkewStatusLabel(clockSkewStatus)}</UiTag>
    </UiTooltip>
  )
}
