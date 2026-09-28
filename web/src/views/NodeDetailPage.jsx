import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { api } from '@/api.js'
import {
  UiAlert,
  UiBreadcrumb,
  UiButton,
  UiCard,
  UiDescriptions,
  UiPageHeader,
  UiSkeleton,
  UiStatusDot,
  UiTag,
} from '@/components/ui'
import NodeLinksTable from '@/components/NodeLinksTable.jsx'
import NodePathsTable from '@/components/NodePathsTable.jsx'
import NodeLogHealth from '@/components/NodeLogHealth.jsx'
import NodeFormDrawer from '@/components/NodeFormDrawer.jsx'
import JoinTokenDialog from '@/components/JoinTokenDialog.jsx'
import TimeCell from '@/components/TimeCell.jsx'
import { ClockSkewSummary, ProtocolHealthSummary } from '@/components/NodeHealthSummary.jsx'
import '@/components/NodeHealthBlocks.css'
import { useToast } from '@/stores/toast'
import './NodeDetailPage.css'

// NodeDetailPage shows one node's full health: basic info, overlay link table, upstream path-failover table and
// log spool health, fetched straight from GET /api/nodes/{id} (its shape matches the list page's rows).
export default function NodeDetailPage() {
  const { id } = useParams()
  const navigate = useNavigate()
  const { toast } = useToast()
  const [node, setNode] = useState(null)
  const [notFound, setNotFound] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [tokenDialog, setTokenDialog] = useState({ open: false, node: null, token: '', expiresAt: '' })
  const [busy, setBusy] = useState(false)
  // requestRef guards against a stale response overwriting a newer one: the 5s poll timer, plus id changing
  // while a request is still in flight (e.g. clicking a different node in NodesPage while this page is
  // navigating), can both leave an older api() call resolving after a newer one already set state (see
  // TracePage.jsx's useTraceQuery, which the same pattern is copied from).
  const requestRef = useRef(0)

  const refresh = useCallback(async () => {
    const requestId = ++requestRef.current
    try {
      const current = await api(`/nodes/${encodeURIComponent(id)}`)
      if (requestRef.current !== requestId) return
      setNode(current)
      setNotFound(false)
      setError('')
    } catch (e) {
      if (requestRef.current !== requestId) return
      if (e.status === 404) {
        setNode(null)
        setNotFound(true)
        return
      }
      setError(e.message)
    }
  }, [id])
  useEffect(() => {
    setNode(null)
    setNotFound(false)
    setError('')
    refresh()
    const timer = setInterval(refresh, 5000)
    return () => clearInterval(timer)
  }, [refresh])

  async function saveEdit(values) {
    await api(`/nodes/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(values) })
    setEditing(false)
    await refresh()
    toast.success('节点已更新')
  }

  async function regenerateToken() {
    if (!window.confirm(`确定为节点“${node.name}”重新生成 join token？旧 token 将立即失效。`)) return
    setBusy(true)
    try {
      const res = await api(`/nodes/${encodeURIComponent(id)}/token`, { method: 'POST' })
      await refresh()
      setTokenDialog({ open: true, node: res.node, token: res.joinToken, expiresAt: res.expiresAt })
    } catch (e) {
      toast.error(e.message)
    } finally {
      setBusy(false)
    }
  }

  async function remove() {
    if (!window.confirm(`确定删除节点“${node.name}”？`)) return
    setBusy(true)
    try {
      await api(`/nodes/${encodeURIComponent(id)}`, { method: 'DELETE' })
      toast.success('节点已删除')
      navigate('/nodes')
    } catch (e) {
      toast.error(e.message)
      setBusy(false)
    }
  }

  if (notFound) {
    return (
      <div className="ui-page node-detail-page">
        <UiBreadcrumb items={[{ label: '节点管理', to: '/nodes' }, { label: id }]} onNavigate={(item) => item.to && navigate(item.to)} />
        <UiAlert type="error" title="节点不存在">未找到 ID 为 “{id}” 的节点，它可能已被删除。</UiAlert>
      </div>
    )
  }

  const errorEntries = node ? Object.entries(node.errors || {}) : []
  const basics = node && [
    { label: '节点 ID', value: node.id, mono: true },
    { label: '在线状态', render: () => <UiStatusDot tone={node.online ? 'success' : 'danger'}>{node.online ? '在线' : '离线'}</UiStatusDot> },
    { label: '最近上报', render: () => <TimeCell value={node.lastSeen} /> },
    { label: '版本', value: node.version, mono: true },
    { label: 'Revision', render: () => <span className="ui-mono">{node.appliedRevision} / {node.publishedRevision}</span> },
    { label: '同步状态', render: () => <UiTag tone={node.inSync ? 'success' : 'warn'}>{node.inSync ? '已同步' : '待同步'}</UiTag> },
    { label: '协议版本', render: () => <ProtocolHealthSummary protocolVersion={node.protocolVersion} protocolStatus={node.protocolStatus} /> },
    { label: '时钟偏差', render: () => <ClockSkewSummary clockSkewMillis={node.clockSkewMillis} clockSkewStatus={node.clockSkewStatus} /> },
    { label: '证书代数', render: () => node.embedded ? <span className="ui-cell-dim">内嵌节点</span> : <UiTag tone={node.certGeneration > 0 ? 'success' : 'muted'}>{node.certGeneration > 0 ? `第 ${node.certGeneration} 代` : '未注册'}</UiTag> },
    { label: '证书有效期至', render: () => <TimeCell value={node.certNotAfter} /> },
    { label: 'join token 有效期至', render: () => <TimeCell value={node.tokenExpiresAt} /> },
    { label: '中继地址', value: node.relayAddress || '（不作为中继）' },
    { label: '创建时间', render: () => <TimeCell value={node.createdAt} /> },
    { label: '首次注册时间', render: () => <TimeCell value={node.registeredAt} /> },
    { label: '正在运行的站点', value: (node.running || []).join('、') || '（无）' },
  ]

  return (
    <div className="ui-page node-detail-page">
      <UiBreadcrumb items={[{ label: '节点管理', to: '/nodes' }, { label: node?.name || id }]} onNavigate={(item) => item.to && navigate(item.to)} />
      {error && <UiAlert type="error" title="加载失败">{error}</UiAlert>}
      {!node
        ? <UiSkeleton type="block" height="280px" />
        : (
          <>
            <UiPageHeader
              title={node.name}
              sub={node.embedded ? '内嵌节点' : `节点 ID: ${node.id}`}
              actions={!node.embedded && (
                <div className="row-actions">
                  <UiButton size="sm" variant="outline" onClick={() => setEditing(true)}>编辑</UiButton>
                  <UiButton size="sm" variant="outline" loading={busy} onClick={regenerateToken}>{node.registered ? '重置 token' : '生成 token'}</UiButton>
                  <UiButton size="sm" variant="danger" loading={busy} onClick={remove}>删除</UiButton>
                </div>
              )}
            />
            {node.relayError && <UiAlert type="error" title="中继端口绑定失败">{node.relayError}</UiAlert>}
            {errorEntries.length > 0 && (
              <UiAlert type="warn" title="部分站点应用失败">
                {errorEntries.map(([site, message]) => <div key={site}><b>{site}</b>：{message}</div>)}
              </UiAlert>
            )}
            <UiCard title="基本信息" variant="flat">
              <UiDescriptions items={basics} />
            </UiCard>
            {/* 链路/路径/日志健康以前挤在一组标签页里，切换才能看到另外两项；改成三张纵向卡片后，
                运维排查时（比如某条链路故障但候选路径正常）不用来回点标签就能一眼对比全部信息
                （task 3 usability pass，2026-09-28，见 docs/architecture/control-data-plane.md §5）。 */}
            <UiCard title="链路健康" variant="flat" flush count={node.links?.length || 0}>
              <div className="node-detail-page__section">
                <NodeLinksTable links={node.links} />
              </div>
            </UiCard>
            <UiCard title="路径健康" variant="flat" flush count={node.paths?.length || 0}>
              <div className="node-detail-page__section">
                <NodePathsTable paths={node.paths} />
              </div>
            </UiCard>
            <UiCard title="日志 spool" variant="flat" flush>
              <div className="node-detail-page__section">
                <NodeLogHealth logs={node.logs} />
              </div>
            </UiCard>
          </>
        )}
      <NodeFormDrawer value={editing} mode="edit" initial={node} onChange={setEditing} onSubmit={saveEdit} />
      <JoinTokenDialog
        value={tokenDialog.open}
        node={tokenDialog.node}
        token={tokenDialog.token}
        expiresAt={tokenDialog.expiresAt}
        onClose={() => setTokenDialog((t) => ({ ...t, open: false }))}
      />
    </div>
  )
}
