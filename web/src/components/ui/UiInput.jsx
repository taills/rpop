import './UiInput.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiInput({
  value = '',
  type = 'text',
  placeholder = '',
  disabled = false,
  readonly = false,
  prefixIcon = '',
  onChange,
}) {
  return (
    <div className={cx('ui-input-wrap', disabled && 'is-disabled')}>
      {prefixIcon && <UiIcon className="ui-input__prefix" name={prefixIcon} size={14} />}
      <input
        className={cx('ui-input', prefixIcon && 'has-prefix')}
        type={type}
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        readOnly={readonly}
        onChange={(e) => onChange?.(e.target.value)}
      />
    </div>
  )
}
