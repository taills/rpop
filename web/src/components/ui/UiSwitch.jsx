import './UiSwitch.css'
import { cx } from '@/utils/cx'

export default function UiSwitch({ value = false, disabled = false, onChange }) {
  return (
    <button
      type="button"
      className={cx('ui-switch', value && 'on', disabled && 'is-disabled')}
      role="switch"
      aria-checked={value}
      disabled={disabled}
      onClick={() => onChange?.(!value)}
    >
      <span className="ui-switch__knob" />
    </button>
  )
}
