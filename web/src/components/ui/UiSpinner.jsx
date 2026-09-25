import './UiSpinner.css'
import { cx } from '@/utils/cx'

export default function UiSpinner({ size = 'md', label = '加载中' }) {
  return (
    <span className={cx('ui-spinner', `is-${size}`)} role="status" aria-label={label}>
      <span className="ui-spinner__ring" />
    </span>
  )
}
