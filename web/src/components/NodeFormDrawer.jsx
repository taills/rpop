import { useEffect, useState } from 'react'
import { UiAlert, UiButton, UiDrawer, UiField, UiInput } from '@/components/ui'
import './NodeFormDrawer.css'

const idPattern = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/

// NodeFormDrawer creates a node (POST /api/nodes) or edits its name/relay address (PUT /api/nodes/{id}); the
// server owns id validation and uniqueness, so this only pre-flags the obvious id shape problem before a round
// trip. onSubmit receives {id, name, relayAddress} and is expected to throw on failure (the drawer stays open
// and shows the message) or resolve on success (the caller closes it).
export default function NodeFormDrawer({ value, mode = 'create', initial, onChange, onSubmit }) {
  const [form, setForm] = useState({ id: '', name: '', relayAddress: '' })
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    if (value) {
      setForm({ id: initial?.id || '', name: initial?.name || '', relayAddress: initial?.relayAddress || '' })
      setError('')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, initial?.id])

  function set(field) {
    return (v) => setForm((f) => ({ ...f, [field]: v }))
  }

  async function submit(event) {
    event.preventDefault()
    if (mode === 'create' && !idPattern.test(form.id)) {
      setError('节点 ID 只能包含小写字母、数字和连字符，且不能以连字符开头或结尾')
      return
    }
    if (!form.name.trim()) {
      setError('请填写节点名称')
      return
    }
    setBusy(true)
    setError('')
    try {
      await onSubmit({ id: form.id.trim(), name: form.name.trim(), relayAddress: form.relayAddress.trim() })
    } catch (e) {
      setError(e.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <UiDrawer
      value={value}
      onChange={onChange}
      title={mode === 'create' ? '新建节点' : `编辑节点 ${initial?.id || ''}`}
      eyebrow="节点管理"
      footer={
        <>
          <UiButton variant="outline" size="sm" disabled={busy} onClick={() => onChange?.(false)}>取消</UiButton>
          <UiButton variant="primary" size="sm" loading={busy} onClick={submit}>{mode === 'create' ? '创建并生成 join token' : '保存'}</UiButton>
        </>
      }
    >
      <form className="node-form" onSubmit={submit}>
        {error && <UiAlert type="error">{error}</UiAlert>}
        <UiField label="节点 ID" required hint={mode === 'create' ? '小写字母/数字/连字符，创建后不可修改' : ''}>
          <UiInput value={form.id} onChange={set('id')} disabled={mode !== 'create'} placeholder="node-tokyo-1" />
        </UiField>
        <UiField label="名称" required>
          <UiInput value={form.name} onChange={set('name')} placeholder="东京中继节点" />
        </UiField>
        <UiField label="中继地址" hint="其他节点拨入的 host:port；留空表示该节点不作为中继">
          <UiInput value={form.relayAddress} onChange={set('relayAddress')} placeholder="10.0.0.2:7000" />
        </UiField>
      </form>
    </UiDrawer>
  )
}
