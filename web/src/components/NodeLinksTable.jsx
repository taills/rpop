import { UiEmpty, UiStatusDot, UiTag, UiTooltip } from '@/components/ui'
import './NodeHealthBlocks.css'
import TimeCell from './TimeCell.jsx'
import { linkStatusLabel, linkStatusTone } from '@/nodeHealth'

function proxyChain(proxies = []) {
  if (!proxies.length) return <span className="ui-cell-dim">直连</span>
  const text = proxies.join(' → ')
  return (
    <UiTooltip content={text}>
      <span className="ui-mono">{text.length > 28 ? `${text.slice(0, 26)}…` : text}</span>
    </UiTooltip>
  )
}

function errorCell(message) {
  if (!message) return <span className="ui-cell-dim">—</span>
  return (
    <UiTooltip content={message}>
      <span>{message.length > 24 ? `${message.slice(0, 22)}…` : message}</span>
    </UiTooltip>
  )
}

// NodeLinksTable renders a node's overlay link health (D15/D19: one row per peer + proxy chain); used by
// NodeDetailPage. links is overlay.LinkStatus[] as returned by nodeView.links / topology's per-node source.
export default function NodeLinksTable({ links = [] }) {
  if (!links.length) return <UiEmpty title="暂无链路" desc="该节点当前没有已建立或正在维护的节点间链路。" />
  const down = links.filter((l) => l.status === 'down').length
  return (
    <div className="node-health-block">
      {down > 0 && <UiTag tone="risk-high" icon="alert">{down} 条链路故障</UiTag>}
      <div className="ui-table-shell">
        <div className="ui-table-scroll">
          <table className="ui-table is-hoverable">
            <thead>
              <tr>
                <th>对端</th>
                <th>代理链</th>
                <th>状态</th>
                <th>在途连接</th>
                <th>隧道数</th>
                <th>失败次数</th>
                <th>冷却至</th>
                <th>最近成功</th>
                <th>最近错误</th>
              </tr>
            </thead>
            <tbody>
              {links.map((link, index) => (
                <tr key={`${link.peer}-${index}`}>
                  <td>
                    <div className="stacked-cell">
                      <span className="ui-mono">{link.peer}</span>
                      {link.address && <small className="ui-cell-dim">{link.address}</small>}
                    </div>
                  </td>
                  <td>{proxyChain(link.proxies)}</td>
                  <td>
                    <UiStatusDot tone={linkStatusTone(link.status)}>{linkStatusLabel(link.status)}</UiStatusDot>
                  </td>
                  <td>{link.connections ?? 0}</td>
                  <td>{link.tunnels ?? 0}</td>
                  <td>{link.failures ?? 0}</td>
                  <td><TimeCell value={link.downUntil} /></td>
                  <td><TimeCell value={link.lastSuccess} /></td>
                  <td>{errorCell(link.lastError)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  )
}
