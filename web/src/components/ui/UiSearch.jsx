import './UiSearch.css'
import { useState } from 'react'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiSearch({ value = '', placeholder = '搜索…', onChange }) {
  const [focused, setFocused] = useState(false)
  return (
    <div className={cx('ui-search', focused && 'is-focused')}>
      <UiIcon name="search" size={14} className="ui-search__icon" />
      <input
        className="ui-search__input"
        type="search"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange?.(e.target.value)}
        onFocus={() => setFocused(true)}
        onBlur={() => setFocused(false)}
      />
      {value && (
        <button type="button" className="ui-search__clear" aria-label="清空" onClick={() => onChange?.('')}>
          <UiIcon name="x" size={12} />
        </button>
      )}
    </div>
  )
}
