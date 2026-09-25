import './UiAlert.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

const iconMap = {
  info: 'info',
  success: 'check',
  warn: 'alert',
  error: 'alert',
  ai: 'bot',
}

export default function UiAlert({ type = 'info', title = '', children }) {
  return (
    <div className={cx('ui-alert', `is-${type}`)} role="status">
      <span className="ui-alert__icon">
        <UiIcon name={iconMap[type] || 'info'} size={16} />
      </span>
      <div className="ui-alert__body">
        {title && <div className="ui-alert__title">{title}</div>}
        <div className="ui-alert__msg">{children}</div>
      </div>
    </div>
  )
}
