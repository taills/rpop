import './UiSelect.css'
import { useEffect, useRef, useState } from 'react'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiSelect({ value = '', options = [], placeholder = '请选择', disabled = false, onChange }) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef(null)
  const selected = options.find((o) => o.value === value)

  function toggle() {
    if (disabled) return
    setOpen((o) => !o)
  }
  function pick(v) {
    onChange?.(v)
    setOpen(false)
  }
  useEffect(() => {
    function onClickAway(e) {
      if (!rootRef.current?.contains(e.target)) setOpen(false)
    }
    document.addEventListener('mousedown', onClickAway)
    return () => document.removeEventListener('mousedown', onClickAway)
  }, [])

  return (
    <div ref={rootRef} className={cx('ui-select', open && 'open', disabled && 'disabled')}>
      <button
        type="button"
        className={cx('ui-select__trigger', !selected && 'is-placeholder')}
        disabled={disabled}
        onClick={toggle}
      >
        <span className="ui-select__value">{selected?.label || placeholder || '请选择'}</span>
        <span className={cx('ui-select__caret', open && 'open')}>
          <UiIcon name="chevronDown" size={14} />
        </span>
      </button>
      {open && (
        <div className="ui-select__panel" role="listbox">
          {options.map((opt) => (
            <button
              key={opt.value}
              type="button"
              className={cx('ui-select__option', opt.value === value && 'on')}
              role="option"
              aria-selected={opt.value === value}
              onClick={() => pick(opt.value)}
            >
              <span>{opt.label}</span>
              {opt.value === value && <UiIcon name="check" size={14} />}
            </button>
          ))}
          {!options.length && <div className="ui-select__empty">无选项</div>}
        </div>
      )}
    </div>
  )
}
