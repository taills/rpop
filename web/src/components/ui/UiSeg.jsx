import './UiSeg.css'
import { cx } from '@/utils/cx'

export default function UiSeg({ value = '', options = [], onChange }) {
  return (
    <div className="ui-seg" role="tablist">
      {options.map((opt) => (
        <button
          key={opt.value}
          type="button"
          className={cx('ui-seg__btn', opt.value === value && 'on')}
          role="tab"
          aria-selected={opt.value === value}
          onClick={() => onChange?.(opt.value)}
        >
          {opt.label}
        </button>
      ))}
    </div>
  )
}
