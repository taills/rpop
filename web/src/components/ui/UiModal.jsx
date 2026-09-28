import './UiModal.css'
import { useEffect, useRef, useState } from 'react'
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
  onError,
  footer,
  children,
}) {
  const [loading, setLoading] = useState(false)
  // confirmingRef is the actual reentrancy guard: setLoading only takes effect on the next render, which is
  // too late to stop a second handleConfirm() call landing before the confirm button's disabled attribute has
  // re-rendered (a fast double-click, or Enter held down). The ref is checked synchronously instead.
  const confirmingRef = useRef(false)

  function tryClose() {
    if (persistent || confirmingRef.current) return
    onChange?.(false)
  }

  async function handleConfirm() {
    if (confirmingRef.current) return
    confirmingRef.current = true
    setLoading(true)
    try {
      await onConfirm?.()
    } catch (error) {
      // onConfirm rejecting must not become an unhandled rejection just because this handler is invoked from
      // onClick (React never awaits or catches that returned promise): the modal has no error UI of its own —
      // it never even auto-closes on success, that is entirely onConfirm's/the caller's job — so on failure the
      // only correct move is to leave it open (matching the "no change on error" outcome a thrown error implies)
      // and hand the error to whatever the caller can see: the console, plus an optional onError callback for
      // callers that want to surface it in their own UI (e.g. a toast or an inline message).
      console.error('UiModal onConfirm failed', error)
      onError?.(error)
    } finally {
      confirmingRef.current = false
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
