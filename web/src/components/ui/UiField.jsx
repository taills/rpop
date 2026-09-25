import './UiField.css'
import { cx } from '@/utils/cx'

export default function UiField({ label = '', hint = '', error = '', required = false, children }) {
  return (
    <div className={cx('ui-field', !!error && 'is-error', required && 'is-required')}>
      {label && (
        <label className="ui-field__label">
          {label}
          {required && <span className="ui-field__req">*</span>}
          {hint && <span className="ui-field__hint">{hint}</span>}
        </label>
      )}
      <div className="ui-field__control">{children}</div>
      {error && <div className="ui-field__error">{error}</div>}
    </div>
  )
}
