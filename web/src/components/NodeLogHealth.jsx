import { UiAlert, UiDescriptions, UiEmpty, UiTag } from '@/components/ui'
import './NodeHealthBlocks.css'
import { formatBytes } from '@/nodeHealth'

function dropTone(count) {
  return count > 0 ? 'warn' : 'muted'
}

// NodeLogHealth renders a node's log spool/upload pipeline health (D23-D25): southbound.LogStats, in full, with
// byte counters formatted for humans. logs is nil until a node reports at least one status carrying it, and is
// always nil on the embedded node (it writes access logs directly and never runs a spool).
export default function NodeLogHealth({ logs }) {
  if (!logs) return <UiEmpty title="暂无 spool 数据" desc="嵌入节点直写日志，不经过本地 spool；其余节点会在首次上报状态后显示。" />
  const items = [
    { label: '待上传段数', value: logs.pendingSegments ?? 0 },
    { label: '待上传字节数', value: formatBytes(logs.pendingBytes) },
    { label: '已确认段号', value: logs.ackedSegment ?? 0, mono: true },
    {
      label: '访问日志队列丢弃',
      render: () => (
        <span>
          {logs.accessLogQueueDropped ?? 0}
          {logs.accessLogQueueDropped > 0 && <UiTag tone={dropTone(logs.accessLogQueueDropped)}> 队列已丢弃</UiTag>}
        </span>
      ),
    },
    {
      label: '隧道事件队列丢弃',
      render: () => (
        <span>
          {logs.tunnelEventQueueDropped ?? 0}
          {logs.tunnelEventQueueDropped > 0 && <UiTag tone={dropTone(logs.tunnelEventQueueDropped)}> 队列已丢弃</UiTag>}
        </span>
      ),
    },
    {
      label: '配额丢弃段数',
      render: () => (
        <span>
          {logs.quotaDroppedSegments ?? 0}
          {logs.quotaDroppedSegments > 0 && <UiTag tone="risk-high" icon="alert"> 超配额丢弃</UiTag>}
        </span>
      ),
    },
    { label: '配额丢弃字节数', value: formatBytes(logs.quotaDroppedBytes) },
  ]
  return (
    <div className="node-health-block">
      {logs.lastUploadError && <UiAlert type="warn" title="最近一次回传失败">{logs.lastUploadError}</UiAlert>}
      <UiDescriptions items={items} />
    </div>
  )
}
