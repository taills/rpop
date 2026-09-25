import './UiStatusDot.css'
import { cx } from '@/utils/cx'

export default function UiStatusDot({ tone = 'neutral', label = '', children }) {
  return (
    <span className={cx('ui-status-dot', `is-${tone}`)}>
      <span className="ui-status-dot__mark" />
      {children ?? label}
    </span>
  )
}
