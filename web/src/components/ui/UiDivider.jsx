import './UiDivider.css'
import { cx } from '@/utils/cx'

export default function UiDivider({ label = '', vertical = false }) {
  return (
    <div className={cx('ui-divider', vertical && 'is-vertical', !!label && 'is-labeled')}>
      {label && <span className="ui-divider__label">{label}</span>}
    </div>
  )
}
