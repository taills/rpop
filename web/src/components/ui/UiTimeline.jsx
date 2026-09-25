import './UiTimeline.css'
import { cx } from '@/utils/cx'

export default function UiTimeline({ items = [] }) {
  return (
    <ol className="ui-timeline">
      {items.map((item, i) => (
        <li key={i} className={cx('ui-timeline__item', `is-${item.tone || 'default'}`)}>
          <div className="ui-timeline__rail">
            <span className="ui-timeline__dot" />
            {i < items.length - 1 && <span className="ui-timeline__line" />}
          </div>
          <div className="ui-timeline__body">
            <div className="ui-timeline__title">
              {item.title}
              {item.time && <span className="ui-timeline__time">{item.time}</span>}
            </div>
            {item.desc && <div className="ui-timeline__desc">{item.desc}</div>}
          </div>
        </li>
      ))}
    </ol>
  )
}
