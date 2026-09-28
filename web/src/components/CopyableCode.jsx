import { useState } from 'react'
import { UiButton } from '@/components/ui'
import { useToast } from '@/stores/toast'
import './CopyableCode.css'

// CopyableCode renders one generated command or config block with its own copy button, so a long, multi-step
// guide (see NodeBootstrapGuide) never forces an operator to copy-paste a whole page of text to get one command.
export default function CopyableCode({ label, code }) {
  const [copied, setCopied] = useState(false)
  const { toast } = useToast()

  async function copy() {
    try {
      await navigator.clipboard.writeText(code || '')
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch (e) {
      setCopied(false)
      toast.error(e?.message || '复制失败，请手动选中并复制')
    }
  }

  return (
    <div className="copyable-code">
      <div className="copyable-code__head">
        <span className="copyable-code__label">{label}</span>
        <UiButton variant="outline" size="sm" icon="copy" onClick={copy}>{copied ? '已复制' : '复制'}</UiButton>
      </div>
      <pre className="copyable-code__body"><code>{code}</code></pre>
    </div>
  )
}
