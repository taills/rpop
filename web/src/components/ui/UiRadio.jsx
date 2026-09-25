import './UiRadio.css'
import { cx } from '@/utils/cx'

export default function UiRadio({ value = '', optionValue, label = '', disabled = false, children, onChange }) {
  return (
    <label className={cx('ui-radio', disabled && 'is-disabled')}>
      <input
        type="radio"
        checked={value === optionValue}
        disabled={disabled}
        value={optionValue}
        onChange={() => onChange?.(optionValue)}
      />
      <span className="ui-radio__dot" />
      {(label || children != null) && <span className="ui-radio__label">{children ?? label}</span>}
    </label>
  )
}
