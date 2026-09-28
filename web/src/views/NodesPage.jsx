import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api } from '@/api.js'
import {
  UiAlert,
  UiButton,
  UiEmpty,
  UiPageHeader,
  UiSearch,
  UiSkeleton,
} from '@/components/ui'
import NodeFormDrawer from '@/components/NodeFormDrawer.jsx'
import JoinTokenDialog from '@/components/JoinTokenDialog.jsx'
import NodeCard from '@/components/NodeCard.jsx'
import { nodeNeedsAttention } from '@/nodeHealth'
import { nodeMatchesQuery } from '@/nodeCard'
import { useToast } from '@/stores/toast'
import './NodesPage.css'

// NodesPage lists every known node (embedded + registered) with its online/sync/link/path/log health, and owns
// node lifecycle management (create, rename/re-point relay address, reissue join token, delete). Detail tables
// live on NodeDetailPage; this page only shows compact summaries, one card per node (see NodeCard.jsx), laid
// out in a responsive grid instead of the wide table the console used before (see docs/architecture/
// control-data-plane.md §5, "节点卡片网格" entry) so 1440px never needs a horizontal scrollbar and a phone-width
// viewport gets one full-width card per row instead of a squeezed table.
export default function NodesPage() {
  const navigate = useNavigate()
  const { toast } = useToast()
  const [nodes, setNodes] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [query, setQuery] = useState('')
  const [form, setForm] = useState({ open: false, mode: 'create', initial: null })
  const [tokenDialog, setTokenDialog] = useState({ open: false, node: null, token: '', expiresAt: '' })

  const refresh = useCallback(async () => {
    try {
      const list = await api('/nodes')
      setNodes(list)
      setError('')
    } catch (e) {
      setError(e.message)
    } finally {
      setLoading(false)
    }
  }, [])
  useEffect(() => {
    refresh()
    const timer = setInterval(refresh, 5000)
    return () => clearInterval(timer)
  }, [refresh])

  function openCreate() {
    setForm({ open: true, mode: 'create', initial: null })
  }
  function openEdit(node) {
    setForm({ open: true, mode: 'edit', initial: node })
  }
  function openDetail(node) {
    navigate(`/nodes/${encodeURIComponent(node.id)}`)
  }

  async function submitForm(values) {
    if (form.mode === 'create') {
      const res = await api('/nodes', { method: 'POST', body: JSON.stringify(values) })
      setForm((f) => ({ ...f, open: false }))
      await refresh()
      toast.success(`节点 ${res.node.id} 已创建`)
      setTokenDialog({ open: true, node: res.node, token: res.joinToken, expiresAt: res.expiresAt })
    } else {
      await api(`/nodes/${encodeURIComponent(form.initial.id)}`, { method: 'PUT', body: JSON.stringify(values) })
      setForm((f) => ({ ...f, open: false }))
      await refresh()
      toast.success('节点已更新')
    }
  }

  async function regenerateToken(node) {
    if (!window.confirm(`确定为节点“${node.name}”重新生成 join token？旧 token 将立即失效。`)) return
    setBusy(`${node.id}:token`)
    try {
      const res = await api(`/nodes/${encodeURIComponent(node.id)}/token`, { method: 'POST' })
      await refresh()
      setTokenDialog({ open: true, node: res.node, token: res.joinToken, expiresAt: res.expiresAt })
    } catch (e) {
      toast.error(e.message)
    } finally {
      setBusy('')
    }
  }

  async function remove(node) {
    if (!window.confirm(`确定删除节点“${node.name}”？`)) return
    setBusy(`${node.id}:delete`)
    try {
      await api(`/nodes/${encodeURIComponent(node.id)}`, { method: 'DELETE' })
      await refresh()
      toast.success('节点已删除')
    } catch (e) {
      toast.error(e.message)
    } finally {
      setBusy('')
    }
  }

  const filtered = nodes.filter((n) => nodeMatchesQuery(n, query))
  const attentionCount = nodes.filter(nodeNeedsAttention).length

  return (
    <div className="ui-page nodes-page">
      <UiPageHeader
        title="节点管理"
        sub="查看每个节点的在线状态、链路/路径健康与日志 spool 情况，并管理节点的注册与 join token。"
        actions={<UiButton variant="primary" icon="plus" onClick={openCreate}>新建节点</UiButton>}
      />
      {error && <UiAlert type="error" title="加载失败">{error}</UiAlert>}
      {attentionCount > 0 && (
        <UiAlert type="warn" title="需要关注">{attentionCount} 个节点离线、链路/路径异常、日志 spool 有问题、协议版本落后或时钟偏差过大，请查看下方卡片或详情页。</UiAlert>
      )}
      <div className="nodes-toolbar">
        <UiSearch value={query} onChange={setQuery} placeholder="搜索节点 ID 或名称" />
      </div>
      {loading
        ? (
          <div className="node-card-grid">
            {[0, 1, 2].map((i) => <UiSkeleton key={i} type="block" height="280px" />)}
          </div>
        )
        : filtered.length
          ? (
            <div className="node-card-grid">
              {filtered.map((node) => (
                <NodeCard
                  key={node.id}
                  node={node}
                  busy={busy}
                  onDetail={openDetail}
                  onEdit={openEdit}
                  onToken={regenerateToken}
                  onDelete={remove}
                />
              ))}
            </div>
          )
          : <UiEmpty title={nodes.length ? '没有匹配的节点' : '还没有节点'} desc={nodes.length ? '调整搜索词' : '创建第一个节点以生成 join token。'} />}
      <NodeFormDrawer value={form.open} mode={form.mode} initial={form.initial} onChange={(open) => setForm((f) => ({ ...f, open }))} onSubmit={submitForm} />
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
