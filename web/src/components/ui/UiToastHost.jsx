import { createPortal } from 'react-dom'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'
import { useToastStore } from '@/stores/toast'
import './UiToastHost.css'

const iconMap = {
  success: 'check',
  error: 'alert',
  warn: 'alert',
  info: 'info',
}

export default function UiToastHost() {
  const store = useToastStore()

  return createPortal(
    <div className="ui-toast-stack" aria-live="polite">
      {store.list.map((t) => (
        <div key={t.id} className={cx('ui-toast', `is-${t.type}`)}>
          <span className="ui-toast__icon">
            <UiIcon name={iconMap[t.type] || 'info'} size={14} />
          </span>
          <div className="ui-toast__body">
            {t.title && <div className="ui-toast__title">{t.title}</div>}
            <div className="ui-toast__msg">{t.message}</div>
          </div>
          <button type="button" className="ui-toast__x" aria-label="关闭" onClick={() => store.dismiss(t.id)}>
            <UiIcon name="x" size={12} />
          </button>
        </div>
      ))}
    </div>,
    document.body,
  )
}
