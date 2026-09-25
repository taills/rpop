import './UiEmpty.css'
import UiIcon from './UiIcon'

export default function UiEmpty({ title = '暂无数据', desc = '', icon = 'search', children, actions }) {
  return (
    <div className="ui-empty">
      <div className="ui-empty__icon">
        <UiIcon name={icon} size={28} />
      </div>
      <div className="ui-empty__title">{title}</div>
      {(desc || children != null) && <div className="ui-empty__desc">{children ?? desc}</div>}
      {actions != null && <div className="ui-empty__actions">{actions}</div>}
    </div>
  )
}
