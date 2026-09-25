import './UiCheckbox.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiCheckbox({ value = false, label = '', disabled = false, indeterminate = false, children, onChange }) {
  return (
    <label className={cx('ui-checkbox', disabled && 'is-disabled')}>
      <input
        type="checkbox"
        checked={value}
        disabled={disabled}
        ref={(el) => {
          if (el) el.indeterminate = !!indeterminate
        }}
        onChange={(e) => onChange?.(e.target.checked)}
      />
      <span className="ui-checkbox__box">
        {value && <UiIcon name="check" size={12} />}
        {!value && indeterminate && <span className="ui-checkbox__ind" />}
      </span>
      {(label || children != null) && <span className="ui-checkbox__label">{children ?? label}</span>}
    </label>
  )
}
