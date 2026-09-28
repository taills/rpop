import './UiDrawer.css'
import { useEffect } from 'react'
import { createPortal } from 'react-dom'
import UiIcon from './UiIcon'

export default function UiDrawer({
  value = false,
  title = '',
  eyebrow = '',
  width = '480px',
  // persistent blocks the mask click / × button / Esc from closing the drawer, same as UiModal's prop of the
  // same name — pass the caller's busy/saving flag so a request in flight can't be abandoned mid-air by an
  // accidental outside click, leaving its eventual success or error toast with nothing left open to show it in.
  persistent = false,
  onChange,
  subtitle,
  footer,
  children,
}) {
  function tryClose() {
    if (persistent) return
    onChange?.(false)
  }

  useEffect(() => {
    function onKey(e) {
      if (e.key === 'Escape' && value && !persistent) tryClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [value, persistent, onChange])

  if (!value) return null

  return createPortal(
    <div
      className="ui-drawer"
      role="dialog"
      aria-label={title}
      onKeyDown={(e) => e.key === 'Escape' && tryClose()}
    >
      <div className="ui-drawer__mask" onClick={tryClose} />
      <aside
        className="ui-drawer__panel"
        style={{ width }}
        onClick={(e) => e.stopPropagation()}
      >
        <header className="ui-drawer__head">
          <div>
            {eyebrow && <div className="ui-drawer__eyebrow">{eyebrow}</div>}
            <h3 className="ui-drawer__title">{title}</h3>
            {subtitle}
          </div>
          <button type="button" className="ui-drawer__x" aria-label="关闭" disabled={persistent} onClick={tryClose}>
            <UiIcon name="x" size={16} />
          </button>
        </header>
        <div className="ui-drawer__body">{children}</div>
        {footer != null && <footer className="ui-drawer__foot">{footer}</footer>}
      </aside>
    </div>,
    document.body,
  )
}
