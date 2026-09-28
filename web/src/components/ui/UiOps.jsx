import './UiOps.css'
import { Fragment } from 'react'
import UiLink from './UiLink'

export default function UiOps({
  items = [],
  /** 默认列表操作不显红字；确认弹窗才用 danger 实心 */
  allowRed = false,
  max = 0,
}) {
  const visible = max > 0 ? items.slice(0, max) : items

  return (
    <div className="ui-ops">
      {visible.map((item, i) => (
        <Fragment key={i}>
          {item.divider && i > 0 && <span className="ui-ops__sep" />}
          <UiLink
            tone={item.tone === 'danger' && !allowRed ? 'muted' : item.tone || 'primary'}
            disabled={item.disabled || item.loading}
            aria-busy={item.loading || undefined}
            onClick={() => item.onClick?.()}
          >
            {item.loading ? item.loadingLabel || item.label : item.label}
          </UiLink>
        </Fragment>
      ))}
    </div>
  )
}
