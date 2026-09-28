import { Link } from 'react-router-dom'
import { UiButton, UiStatusDot, UiTag } from '@/components/ui'
import TimeCell from '@/components/TimeCell.jsx'
import { LinkHealthSummary, LogHealthSummary, PathHealthSummary, ClockSkewSummary, ProtocolHealthSummary } from '@/components/NodeHealthSummary.jsx'
import { nodeCardErrorText, nodeCardHasAnomaly, nodeCardMetrics } from '@/nodeCard'
import { cx } from '@/utils/cx'
import './NodeCard.css'

const METRIC_LABELS = {
  revision: 'Revision / 应用结果',
  cert: '证书',
  links: '链路健康',
  paths: '路径健康',
  logs: '日志 spool',
  protocol: '协议版本',
  clockSkew: '时钟偏差',
}

// MetricValue renders one metric's value cell; it's the same presentation the old table columns used
// (see git history of NodesPage.jsx), just addressed by key so nodeCardMetrics can reorder them.
function MetricValue({ metricKey, node }) {
  switch (metricKey) {
    case 'revision':
      return (
        <div className="stacked-cell">
          <span className="ui-mono">{node.appliedRevision} / {node.publishedRevision}</span>
          <UiTag tone={node.inSync ? 'success' : 'warn'}>{node.inSync ? '已同步' : '待同步'}</UiTag>
        </div>
      )
    case 'cert':
      return node.embedded
        ? <span className="ui-cell-dim">内嵌节点</span>
        : (
          <div className="stacked-cell">
            <UiTag tone={node.certGeneration > 0 ? 'success' : 'muted'}>{node.certGeneration > 0 ? `第 ${node.certGeneration} 代` : '未注册'}</UiTag>
            <TimeCell value={node.certNotAfter} />
          </div>
        )
    case 'links':
      return <LinkHealthSummary links={node.links} />
    case 'paths':
      return <PathHealthSummary paths={node.paths} />
    case 'logs':
      return <LogHealthSummary logs={node.logs} />
    case 'protocol':
      return <ProtocolHealthSummary protocolVersion={node.protocolVersion} protocolStatus={node.protocolStatus} />
    case 'clockSkew':
      return <ClockSkewSummary clockSkewMillis={node.clockSkewMillis} clockSkewStatus={node.clockSkewStatus} />
    default:
      return null
  }
}

// NodeCard replaces one row of the old node table with a self-contained card: header (name/id/embedded flag/
// online status/anomaly tag), a metrics grid (revision/cert/link/path/log/protocol/clock-skew, anomalies sorted
// first by nodeCardMetrics) and a footer with the same one-click actions the table's "操作" column had. The name
// is a real router `Link` (not an onClick-only span) so it is keyboard-focusable and supports middle-click/
// Cmd-click to open in a new tab; onDetail (same navigation, imperative) still backs the "详情" button below.
// hasAnomaly/errorText both derive from nodeCard.js, which in turn derives from nodeHealth.js's
// nodeNeedsAttention — the same predicate NodesPage's banner counts, so this card's "需要关注" tag and the
// banner's count can never disagree.
export default function NodeCard({ node, busy, onDetail, onEdit, onToken, onDelete }) {
  const metrics = nodeCardMetrics(node)
  const hasAnomaly = nodeCardHasAnomaly(node)
  const errorText = nodeCardErrorText(node)
  return (
    <article className={cx('ui-card node-card', hasAnomaly && 'is-anomaly')}>
      <header className="node-card__head">
        <div className="node-card__title-row">
          <Link className="ui-name-link node-card__name" to={`/nodes/${encodeURIComponent(node.id)}`}>{node.name}</Link>
          {node.embedded && <UiTag tone="muted">内嵌</UiTag>}
          {hasAnomaly && <UiTag tone="risk-critical" icon="alert">需要关注</UiTag>}
        </div>
        <div className="ui-owner-line node-card__id">{node.id}</div>
        <div className="node-card__status-row">
          <UiStatusDot tone={node.online ? 'success' : 'danger'}>{node.online ? '在线' : '离线'}</UiStatusDot>
          <TimeCell value={node.lastSeen} />
        </div>
        {errorText && <p className="node-card__error" title={errorText}>{errorText}</p>}
      </header>
      <div className="node-card__body">
        {metrics.map(({ key, anomaly }) => (
          <div className={cx('node-card__metric', anomaly && 'is-anomaly')} key={key}>
            <span className="node-card__metric-label">{METRIC_LABELS[key]}</span>
            <div className="node-card__metric-value"><MetricValue metricKey={key} node={node} /></div>
          </div>
        ))}
      </div>
      <footer className="node-card__foot">
        {node.embedded
          ? <span className="ui-cell-dim">无需管理</span>
          : (
            <div className="row-actions">
              <UiButton size="sm" variant="outline" onClick={() => onDetail(node)}>详情</UiButton>
              <UiButton size="sm" variant="outline" onClick={() => onEdit(node)}>编辑</UiButton>
              <UiButton size="sm" variant="outline" loading={busy === `${node.id}:token`} onClick={() => onToken(node)}>{node.registered ? '重置 token' : '生成 token'}</UiButton>
              <UiButton size="sm" variant="danger" loading={busy === `${node.id}:delete`} onClick={() => onDelete(node)}>删除</UiButton>
            </div>
          )}
      </footer>
    </article>
  )
}
