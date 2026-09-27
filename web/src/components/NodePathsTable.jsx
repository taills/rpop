import { UiEmpty, UiStatusDot, UiTag, UiTooltip } from '@/components/ui'
import './NodeHealthBlocks.css'
import TimeCell from './TimeCell.jsx'
import { pathStatusLabel, pathStatusTone } from '@/nodeHealth'

function errorCell(message) {
  if (!message) return <span className="ui-cell-dim">—</span>
  return (
    <UiTooltip content={message}>
      <span>{message.length > 24 ? `${message.slice(0, 22)}…` : message}</span>
    </UiTooltip>
  )
}

// NodePathsTable renders a node's per-upstream candidate-path failover health (D18/D19/D20): one section per
// (siteId, upstream), each with its ordered candidate paths. paths is dataplane.UpstreamPathHealth[].
export default function NodePathsTable({ paths = [] }) {
  if (!paths.length) return <UiEmpty title="暂无路径" desc="该节点当前没有配置候选路径(paths)的上游。" />
  return (
    <div className="node-health-block">
      {paths.map((group) => {
        const cooling = (group.paths || []).filter((p) => p.status === 'cooling').length
        return (
          <div key={`${group.siteId}-${group.upstream}`} className="ui-table-shell">
            <div className="node-paths-group__head">
              <span className="ui-mono">{group.siteId}</span>
              <span className="ui-cell-dim">{group.upstream}</span>
              {cooling > 0 && <UiTag tone="warn" icon="alert">{cooling} 条冷却中</UiTag>}
            </div>
            <div className="ui-table-scroll">
              <table className="ui-table is-hoverable">
                <thead>
                  <tr>
                    <th>优先级</th>
                    <th>路径</th>
                    <th>状态</th>
                    <th>失败次数</th>
                    <th>冷却至</th>
                    <th>最近错误</th>
                  </tr>
                </thead>
                <tbody>
                  {(group.paths || []).map((path) => (
                    <tr key={path.index}>
                      <td>#{path.index + 1}</td>
                      <td className="ui-mono">{path.label}</td>
                      <td>
                        <UiStatusDot tone={pathStatusTone(path.status)}>{pathStatusLabel(path.status)}</UiStatusDot>
                      </td>
                      <td>{path.failures ?? 0}</td>
                      <td><TimeCell value={path.until} /></td>
                      <td>{errorCell(path.lastError)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )
      })}
    </div>
  )
}
