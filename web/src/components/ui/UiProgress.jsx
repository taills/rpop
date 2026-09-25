import './UiProgress.css'
import { cx } from '@/utils/cx'

export default function UiProgress({
  value = 0,
  label = '',
  sub = '',
  /** brand | success | warn | danger | ai | purple */
  tone = 'brand',
  size = 'md',
  showLabel = true,
}) {
  const clamped = Math.min(100, Math.max(0, Number(value) || 0))

  return (
    <div className={cx('ui-progress', `is-${size}`, `is-tone-${tone}`)}>
      <div className="ui-progress__track">
        <div
          className="ui-progress__bar"
          style={{ width: `${clamped}%` }}
          role="progressbar"
          aria-valuenow={value}
          aria-valuemin="0"
          aria-valuemax="100"
        />
      </div>
      {showLabel && (
        <div className="ui-progress__meta">
          <span className="ui-progress__label">{label || `${clamped}%`}</span>
          {sub ? <span className="ui-progress__sub">{sub}</span> : null}
        </div>
      )}
    </div>
  )
}
