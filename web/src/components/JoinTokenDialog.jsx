import { useState } from 'react'
import { UiAlert, UiButton, UiModal } from '@/components/ui'
import { describeTime } from '@/nodeHealth'
import './JoinTokenDialog.css'

// JoinTokenDialog shows a freshly issued join token exactly once (the server only ever stores its hash — see
// issueJoinToken/hashToken in internal/control/nodes.go): closing this dialog is the point of no return, so it
// stays open until the user explicitly dismisses it (no backdrop/escape close) and offers a one-click copy.
export default function JoinTokenDialog({ value, node, token, expiresAt, onClose }) {
  const [copied, setCopied] = useState(false)
  const expiry = describeTime(expiresAt)

  async function copy() {
    try {
      await navigator.clipboard.writeText(token || '')
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      setCopied(false)
    }
  }

  return (
    <UiModal
      value={value}
      persistent
      title={`节点 ${node?.id || ''} 的 join token`}
      eyebrow="仅显示一次"
      showFooter={false}
    >
      <UiAlert type="warn" title="请立即复制保存">该 token 只会显示这一次，关闭后无法再次查看；如遗失需重新生成（会使旧 token 失效）。</UiAlert>
      <pre className="join-token__value">{token}</pre>
      {expiry && <p className="ui-cell-dim">有效期至 {expiry.absolute}（{expiry.relative}）</p>}
      <div className="join-token__actions">
        <UiButton variant="outline" size="sm" icon="copy" onClick={copy}>{copied ? '已复制' : '复制 token'}</UiButton>
        <UiButton variant="primary" size="sm" onClick={onClose}>我已保存，关闭</UiButton>
      </div>
    </UiModal>
  )
}
