import './UiButton.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiButton({
  variant = 'outline',
  size = 'md',
  disabled = false,
  loading = false,
  block = false,
  icon = '',
  type = 'button',
  onClick,
  children,
}) {
  return (
    <button
      className={cx(
        'ui-btn',
        `ui-btn--${variant}`,
        size === 'sm' ? 'is-sm' : size === 'lg' ? 'is-lg' : '',
        block && 'is-block',
        loading && 'is-loading',
      )}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      type={type}
      onClick={onClick}
    >
      {(loading || icon) && (
        <span className="ui-btn__icon">
          {loading ? <span className="ui-btn__spinner" /> : <UiIcon name={icon} size={size === 'sm' ? 14 : 16} />}
        </span>
      )}
      <span className="ui-btn__label">{children}</span>
    </button>
  )
}
