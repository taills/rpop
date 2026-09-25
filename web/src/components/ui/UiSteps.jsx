import './UiSteps.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiSteps({ steps = [], current = 0, direction = 'horizontal' }) {
  function statusClass(step, i) {
    const status = step.status || (i < current ? 'done' : i === current ? 'process' : 'wait')
    return `is-${status}`
  }

  return (
    <ol className={cx('ui-steps', `is-${direction}`)}>
      {steps.map((step, i) => (
        <li key={i} className={cx('ui-steps__item', statusClass(step, i))}>
          <div className="ui-steps__marker">
            <span className="ui-steps__dot">
              {step.status === 'done' || (current > i && !step.status) ? (
                <UiIcon name="check" size={12} />
              ) : step.status === 'failed' ? (
                <UiIcon name="x" size={12} />
              ) : (
                <span className="ui-steps__num">{i + 1}</span>
              )}
            </span>
            {i < steps.length - 1 && <span className="ui-steps__line" />}
          </div>
          <div className="ui-steps__body">
            <div className="ui-steps__title">{step.title}</div>
            {step.desc && <div className="ui-steps__desc">{step.desc}</div>}
          </div>
        </li>
      ))}
    </ol>
  )
}
