import './UiBadge.css'
import { cx } from '@/utils/cx'

export default function UiBadge({ value = '', tone = 'neutral', children }) {
  return <span className={cx('ui-badge', `is-${tone}`)}>{children ?? value}</span>
}
