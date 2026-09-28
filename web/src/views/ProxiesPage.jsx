import { useCallback, useEffect, useState } from 'react'
import { api } from '../api.js'
import {
  UiPageHeader, UiButton, UiCard, UiTable, UiTag, UiEmpty, UiSkeleton, UiAlert,
  UiDrawer, UiField, UiInput, UiSelect, UiOps,
} from '@/components/ui'
import { PROXY_TYPES, blankProxyForm, buildProxyMutation, proxyDeleteProblem, proxyFormFromView, proxyFormProblem } from '../proxyForm.js'
import { useToast } from '../stores/toast.js'
import { canStartBusyAction, useBusyAction } from '../busyAction.js'
import './ProxiesPage.css'

const typeOptions = PROXY_TYPES.map((value) => ({ label: value, value }))

function usedByLabel(proxy) {
  if (!proxy.usedBy?.length) return '—'
  return <span title={proxy.usedBy.join(', ')}>{proxy.usedBy.length} 个站点</span>
}

// ProxiesPage lists the named overlay proxies registered on the controller (SOCKS5/SOCKS5H/HTTP(S) CONNECT) and
// lets an operator create, edit and delete them. Credentials are write-only: editing never shows the stored
// password, and the form leaves it blank to mean "keep the current one" (see proxyForm.js).
export default function ProxiesPage() {
  const { toast } = useToast()
  const [proxies, setProxies] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [drawer, setDrawer] = useState(null)
  const { busy, isBusy, run } = useBusyAction()

  const load = useCallback(async () => {
    try { setProxies(await api('/proxies')); setError('') }
    catch (e) { setError(e.message) }
    finally { setLoading(false) }
  }, [])
  useEffect(() => { load() }, [load])

  function openCreate() { setDrawer({ isNew: true, form: blankProxyForm(), error: '' }) }
  function openEdit(proxy) { setDrawer({ isNew: false, form: proxyFormFromView(proxy), error: '' }) }
  function closeDrawer() { setDrawer(null) }
  function patch(field, value) { setDrawer((current) => ({ ...current, form: { ...current.form, [field]: value } })) }

  // submit is guarded by useBusyAction's synchronous ref, same as remove() below, instead of checking
  // drawer.saving: that state only takes effect on the next render, one tick too late to stop a fast
  // double-click (or Enter held down) from firing the create/update request twice.
  async function submit() {
    const problem = proxyFormProblem(drawer.form, { isNew: drawer.isNew })
    if (problem) { setDrawer((current) => ({ ...current, error: problem })); return }
    await run('save', async () => {
      setDrawer((current) => ({ ...current, error: '' }))
      try {
        const { isNew } = drawer
        const path = isNew ? '/proxies' : `/proxies/${encodeURIComponent(drawer.form.id)}`
        await api(path, { method: isNew ? 'POST' : 'PUT', body: JSON.stringify(buildProxyMutation(drawer.form, { isNew })) })
        closeDrawer()
        await load()
        toast.success(isNew ? '代理已创建' : '代理已更新')
      } catch (e) { setDrawer((current) => ({ ...current, error: e.message })) }
    })
  }

  // remove is keyed per proxy id (via useBusyAction) so the busy row can show its own "删除中…" label; the
  // hook is still a single global lock (see busyAction.js), so every row's links are disabled while ANY row
  // is being deleted or the drawer is saving — two overlapping mutations here both refresh the same `proxies`
  // list via load(), and letting one queue behind the other is simpler than reconciling out-of-order refreshes.
  // The canStartBusyAction check runs before window.confirm (not only inside run()) so a click on a row that's
  // already blocked by another one's in-flight delete/save doesn't pop a confirm dialog that run() then
  // silently drops after the operator answers it.
  async function remove(proxy) {
    const blocked = proxyDeleteProblem(proxy)
    if (blocked) { setError(blocked); return }
    if (!canStartBusyAction(busy)) return
    if (!window.confirm(`确定删除代理“${proxy.name}”？`)) return
    await run(proxy.id, async () => {
      try { await api(`/proxies/${encodeURIComponent(proxy.id)}`, { method: 'DELETE' }); await load(); toast.success('代理已删除') }
      catch (e) { setError(e.message); await load() }
    })
  }

  const columns = [
    { key: 'name', title: '名称', render: (row) => <>{row.name}<div className="ui-owner-line ui-mono">{row.id}</div></> },
    { key: 'type', title: '类型', width: '110px', render: (row) => <UiTag tone="type">{row.type}</UiTag> },
    { key: 'address', title: '地址', mono: true },
    { key: 'auth', title: '认证', width: '160px', render: (row) => row.username ? <span className="row proxies-auth-cell">{row.username}{row.hasPassword && <UiTag tone="success">已设密码</UiTag>}</span> : '—' },
    { key: 'usedBy', title: '使用中', width: '110px', render: usedByLabel },
    {
      key: 'ops', title: '操作', width: '140px', align: 'right',
      render: (row) => <UiOps items={[
        { label: '编辑', disabled: Boolean(busy), onClick: () => openEdit(row) },
        { label: '删除', tone: 'danger', divider: true, disabled: Boolean(busy), loading: isBusy(row.id), loadingLabel: '删除中…', onClick: () => remove(row) },
      ]}/>,
    },
  ]

  return (
    <div className="ui-page">
      <UiPageHeader
        eyebrow="OVERLAY · 具名代理"
        title="具名代理"
        sub="SOCKS5 / SOCKS5H / HTTP(S) CONNECT 代理注册表；密码只写不读，被上游候选路径引用时不能删除。"
        actions={<UiButton variant="primary" icon="plus" onClick={openCreate}>新建代理</UiButton>}
      />
      {error && <UiAlert type="error" title="操作失败">{error}</UiAlert>}
      <UiCard flush title="代理列表" icon="link" count={proxies.length || null}>
        <div className="pad">
          {loading ? <div className="row" style={{ flexDirection: 'column', gap: '8px' }}>{[0, 1, 2, 3].map((i) => <UiSkeleton key={i} type="block" height="40px" />)}</div> : proxies.length ? (
            <UiTable columns={columns} rows={proxies} rowKey="id" />
          ) : (
            <UiEmpty title="还没有具名代理" desc="新建一个后，可在站点上游的候选路径中把它加入链路。" actions={<UiButton size="sm" variant="outline" onClick={openCreate}>新建代理</UiButton>}/>
          )}
        </div>
      </UiCard>

      <UiDrawer value={!!drawer} onChange={(open) => !open && closeDrawer()} title={drawer?.isNew ? '新建代理' : '编辑代理'} eyebrow="具名代理" persistent={isBusy('save')} footer={drawer && (
        <>
          <UiButton variant="outline" size="sm" disabled={isBusy('save')} onClick={closeDrawer}>取消</UiButton>
          <UiButton variant="primary" size="sm" loading={isBusy('save')} onClick={submit}>保存</UiButton>
        </>
      )}>
        {drawer && <div className="form-grid">
          {drawer.error && <UiAlert type="error">{drawer.error}</UiAlert>}
          <UiField label="代理 ID" required hint={drawer.isNew ? '保存后不可修改' : ''}>
            <UiInput value={drawer.form.id} disabled={!drawer.isNew} onChange={(v) => patch('id', v)} placeholder="socks5-a"/>
          </UiField>
          <UiField label="名称" required>
            <UiInput value={drawer.form.name} onChange={(v) => patch('name', v)} placeholder="生产 SOCKS5"/>
          </UiField>
          <UiField label="类型" required>
            <UiSelect value={drawer.form.type} onChange={(v) => patch('type', v)} options={typeOptions}/>
          </UiField>
          <UiField label="地址" required hint="host:port">
            <UiInput value={drawer.form.address} onChange={(v) => patch('address', v)} placeholder="127.0.0.1:1080"/>
          </UiField>
          <UiField label="用户名">
            <UiInput value={drawer.form.username} onChange={(v) => patch('username', v)} placeholder="可选"/>
          </UiField>
          <UiField label="密码" hint={drawer.isNew ? '可选' : '留空表示保持不变' + (drawer.form.username ? '' : '；清空用户名会同时清除密码')}>
            <UiInput type="password" value={drawer.form.password} onChange={(v) => patch('password', v)} placeholder={drawer.isNew ? '可选' : '已保存；留空保持不变'}/>
          </UiField>
        </div>}
      </UiDrawer>
    </div>
  )
}
