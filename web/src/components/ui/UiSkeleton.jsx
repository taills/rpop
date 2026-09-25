import './UiSkeleton.css'
import { cx } from '@/utils/cx'

export default function UiSkeleton({ type = 'text', width = '100%', height = '14px' }) {
  return (
    <div className={cx('ui-skeleton', `is-${type}`)} style={{ width, height: type === 'text' ? undefined : height }}>
      <span className="ui-skeleton__bar" />
    </div>
  )
}
