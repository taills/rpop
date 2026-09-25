import './UiTabs.css'
import { cx } from '@/utils/cx'

export default function UiTabs({ tabs, value = '', onChange }) {
  return (
    <div className="ui-tabs" role="tablist">
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          className={cx('ui-tabs__item', tab.key === value && 'on')}
          role="tab"
          aria-selected={tab.key === value}
          onClick={() => onChange?.(tab.key)}
        >
          <span>{tab.label}</span>
          {tab.count !== undefined && <span className="ui-tabs__count">{tab.count}</span>}
        </button>
      ))}
    </div>
  )
}
