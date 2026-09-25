import './UiTag.css'
import { cx } from '@/utils/cx'

const PATHS = {
  bolt: 'M13 2L3 14h9l-1 8 10-12h-9l1-8z',
  check: 'M5 13l4 4L19 7',
  alert: 'M12 9v4m0 4h.01M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z',
  bot: 'M12 8V4H8M4 12h16M9 20h6a2 2 0 002-2v-3H7v3a2 2 0 002 2z',
}

export default function UiTag({
  /**
   * type | risk-critical|high|medium|low
   * success | warn | info | ai | purple | muted
   */
  tone = 'type',
  icon = '',
  children,
}) {
  const iconPath = PATHS[icon] || PATHS.check

  return (
    <span className={cx('ui-tag', `ui-tag--${tone || 'type'}`)}>
      {icon && (
        <svg
          className="ui-tag__icon"
          width="10"
          height="10"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d={iconPath} />
        </svg>
      )}
      {children}
    </span>
  )
}
