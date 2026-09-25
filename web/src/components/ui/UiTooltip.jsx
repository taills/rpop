import './UiTooltip.css'
import { useState } from 'react'
import { cx } from '@/utils/cx'

export default function UiTooltip({ content = '', placement = 'top', children }) {
  const [show, setShow] = useState(false)

  return (
    <div
      className="ui-tooltip-wrap"
      onMouseEnter={() => setShow(true)}
      onMouseLeave={() => setShow(false)}
      onFocus={() => setShow(true)}
      onBlur={() => setShow(false)}
    >
      {children}
      {show && content && (
        <div className={cx('ui-tooltip', `is-${placement}`)} role="tooltip">
          {content}
        </div>
      )}
    </div>
  )
}
