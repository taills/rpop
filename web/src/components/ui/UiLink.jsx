import './UiLink.css'
import { cx } from '@/utils/cx'

export default function UiLink({ tone = 'primary', disabled = false, onClick, children }) {
  return (
    <button
      type="button"
      className={cx('ui-link', tone === 'danger' && 'is-danger', tone === 'muted' && 'is-muted')}
      disabled={disabled}
      onClick={onClick}
    >
      {children}
    </button>
  )
}
