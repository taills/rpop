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
  UiStatusDot,
  UiTable,
  UiTag,
} from '@/components/ui'
import NodeFormDrawer from '@/components/NodeFormDrawer.jsx'
import JoinTokenDialog from '@/components/JoinTokenDialog.jsx'
import TimeCell from '@/components/TimeCell.jsx'
import { LinkHealthSummary, LogHealthSummary, PathHealthSummary } from '@/components/NodeHealthSummary.jsx'
import '@/components/NodeHealthBlocks.css'
import { nodeNeedsAttention } from '@/nodeHealth'
import { useToast } from '@/stores/toast'
import './NodesPage.css'

// NodesPage lists every known node (embedded + registered) with its online/sync/link/path/log health, and owns
// node lifecycle management (create, rename/re-point relay address, reissue join token, delete). Detail tables
// live on NodeDetailPage; this page only shows compact summaries (see NodeHealthSummary).
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

  const filtered = nodes.filter((n) => `${n.name} ${n.id}`.toLowerCase().includes(query.toLowerCase()))
  const attentionCount = nodes.filter(nodeNeedsAttention).length

  const columns = [
    {
      key: 'name', title: '节点', minWidth: '160px',
      render: (row) => (
        <div>
          <a className="ui-name-link" onClick={() => navigate(`/nodes/${encodeURIComponent(row.id)}`)}>{row.name}</a>
          <div className="ui-owner-line">{row.id}{row.embedded && ' · 内嵌'}</div>
        </div>
      ),
    },
    {
      key: 'online', title: '在线状态', minWidth: '140px',
      render: (row) => (
        <div className="stacked-cell">
          <UiStatusDot tone={row.online ? 'success' : 'danger'}>{row.online ? '在线' : '离线'}</UiStatusDot>
          <TimeCell value={row.lastSeen} />
        </div>
      ),
    },
    {
      key: 'revision', title: 'Revision / 应用结果', minWidth: '150px',
      render: (row) => (
        <div className="stacked-cell">
          <span className="ui-mono">{row.appliedRevision} / {row.publishedRevision}</span>
          <UiTag tone={row.inSync ? 'success' : 'warn'}>{row.inSync ? '已同步' : '待同步'}</UiTag>
        </div>
      ),
    },
    {
      key: 'cert', title: '证书', minWidth: '150px',
      render: (row) => row.embedded
        ? <span className="ui-cell-dim">内嵌节点</span>
        : (
          <div className="stacked-cell">
            <UiTag tone={row.certGeneration > 0 ? 'success' : 'muted'}>{row.certGeneration > 0 ? `第 ${row.certGeneration} 代` : '未注册'}</UiTag>
            <TimeCell value={row.certNotAfter} />
          </div>
        ),
    },
    { key: 'links', title: '链路健康', minWidth: '160px', render: (row) => <LinkHealthSummary links={row.links} /> },
    { key: 'paths', title: '路径健康', minWidth: '160px', render: (row) => <PathHealthSummary paths={row.paths} /> },
    { key: 'logs', title: '日志 spool', minWidth: '150px', render: (row) => <LogHealthSummary logs={row.logs} /> },
    {
      key: 'actions', title: '操作', minWidth: '220px',
      render: (row) => row.embedded
        ? <span className="ui-cell-dim">无需管理</span>
        : (
          <div className="row-actions">
            <UiButton size="sm" variant="outline" onClick={() => navigate(`/nodes/${encodeURIComponent(row.id)}`)}>详情</UiButton>
            <UiButton size="sm" variant="outline" onClick={() => openEdit(row)}>编辑</UiButton>
            <UiButton size="sm" variant="outline" loading={busy === `${row.id}:token`} onClick={() => regenerateToken(row)}>{row.registered ? '重置 token' : '生成 token'}</UiButton>
            <UiButton size="sm" variant="danger" loading={busy === `${row.id}:delete`} onClick={() => remove(row)}>删除</UiButton>
          </div>
        ),
    },
  ]

  return (
    <div className="nodes-page">
      <UiPageHeader
        title="节点管理"
        sub="查看每个节点的在线状态、链路/路径健康与日志 spool 情况，并管理节点的注册与 join token。"
        actions={<UiButton variant="primary" icon="plus" onClick={openCreate}>新建节点</UiButton>}
      />
      {error && <UiAlert type="error" title="加载失败">{error}</UiAlert>}
      {attentionCount > 0 && (
        <UiAlert type="warn" title="需要关注">{attentionCount} 个节点离线、链路/路径异常或日志 spool 有问题，请查看下表或详情页。</UiAlert>
      )}
      <div className="nodes-toolbar">
        <UiSearch value={query} onChange={setQuery} placeholder="搜索节点 ID 或名称" />
      </div>
      {loading
        ? <UiSkeleton type="block" height="240px" />
        : (
          <UiTable
            columns={columns}
            rows={filtered}
            rowKey="id"
            empty={<UiEmpty title={nodes.length ? '没有匹配的节点' : '还没有节点'} desc={nodes.length ? '调整搜索词' : '创建第一个节点以生成 join token。'} />}
          />
        )}
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
