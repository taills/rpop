import './UiModal.css'
import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { cx } from '@/utils/cx'
import UiButton from './UiButton'
import UiIcon from './UiIcon'

export default function UiModal({
  value = false,
  title = '',
  eyebrow = '',
  confirmText = '确定',
  confirmVariant = 'primary',
  confirmDisabled = false,
  showFooter = true,
  size = 'md',
  persistent = false,
  onChange,
  onConfirm,
  footer,
  children,
}) {
  const [loading, setLoading] = useState(false)

  function tryClose() {
    if (persistent) return
    onChange?.(false)
  }

  async function handleConfirm() {
    setLoading(true)
    try {
      await onConfirm?.()
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    function onKey(e) {
      if (e.key === 'Escape' && value && !persistent) onChange?.(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [value, persistent, onChange])

  if (!value) return null

  return createPortal(
    <div
      className="ui-modal"
      role="dialog"
      aria-label={title}
      onKeyDown={(e) => e.key === 'Escape' && tryClose()}
    >
      <div className="ui-modal__mask" onClick={tryClose} />
      <div
        className={cx('ui-modal__panel', `is-${size}`)}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="ui-modal__head">
          <div className="ui-modal__head-main">
            {eyebrow && <div className="ui-modal__eyebrow">{eyebrow}</div>}
            <h3 className="ui-modal__title">{title}</h3>
          </div>
          <button type="button" className="ui-modal__x" aria-label="关闭" onClick={tryClose}>
            <UiIcon name="x" size={16} />
          </button>
        </div>
        <div className="ui-modal__body">{children}</div>
        {showFooter && (
          <footer className="ui-modal__foot">
            {footer ?? (
              <>
                <UiButton variant="outline" size="sm" disabled={loading} onClick={tryClose}>
                  取消
                </UiButton>
                <UiButton
                  variant={confirmVariant}
                  size="sm"
                  loading={loading}
                  disabled={loading || confirmDisabled}
                  onClick={handleConfirm}
                >
                  {confirmText}
                </UiButton>
              </>
            )}
          </footer>
        )}
      </div>
    </div>,
    document.body,
  )
}
