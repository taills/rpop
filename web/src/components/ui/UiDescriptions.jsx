import './UiDescriptions.css'
import { cx } from '@/utils/cx'

export default function UiDescriptions({ items = [], column = '2' }) {
  return (
    <dl className={cx('ui-desc', `is-${column}`)}>
      {items.map((item) => (
        <div key={item.label} className="ui-desc__item">
          <dt className="ui-desc__label">{item.label}</dt>
          <dd className={cx('ui-desc__value', item.mono && 'ui-mono')}>
            {typeof item.render === 'function' ? item.render(item) : item.value ?? '—'}
          </dd>
        </div>
      ))}
    </dl>
  )
}
