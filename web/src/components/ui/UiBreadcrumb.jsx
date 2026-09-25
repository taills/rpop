import './UiBreadcrumb.css'
import { Fragment } from 'react'
import { cx } from '@/utils/cx'

export default function UiBreadcrumb({ items = [], onNavigate }) {
  return (
    <nav className="ui-breadcrumb" aria-label="面包屑">
      {items.map((item, i) => (
        <Fragment key={i}>
          {item.to && i < items.length - 1 ? (
            <button
              type="button"
              className="ui-breadcrumb__item is-link"
              onClick={() => onNavigate?.(item)}
            >
              {item.label}
            </button>
          ) : (
            <span className={cx('ui-breadcrumb__item', i === items.length - 1 && 'is-current')}>
              {item.label}
            </span>
          )}
          {i < items.length - 1 && <span className="ui-breadcrumb__sep">/</span>}
        </Fragment>
      ))}
    </nav>
  )
}
