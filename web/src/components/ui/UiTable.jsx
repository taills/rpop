import './UiTable.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiTable({
  columns,
  rows = [],
  rowKey = 'id',
  emptyText = '暂无数据',
  hoverable = true,
  selectedKeys = [],
  /**
   * auto   按内容/col.width 自适应（默认）
   * equal  列宽均分（表格布局 fixed，忽略内容宽度）
   * fixed  强制使用 col.width，缺省均分剩余
   */
  layout = 'auto',
  minWidth = '',
  empty,
}) {
  const tableMinWidth = minWidth || (layout === 'equal' ? '100%' : '720px')

  function formatCell(col, row) {
    const raw = row[col.key]
    if (typeof col.formatter === 'function') return col.formatter(row)
    return raw === undefined || raw === null || raw === '' ? '—' : raw
  }

  function colStyle(col) {
    const style = {
      textAlign: col.align === 'right' ? 'right' : col.align === 'center' ? 'center' : 'left',
    }
    if (layout === 'equal') {
      style.width = `${100 / Math.max(1, columns.length)}%`
    } else if (col.width) {
      style.width = col.width
    }
    if (col.minWidth && layout !== 'equal') style.minWidth = col.minWidth
    return style
  }

  function colWidth(col) {
    if (layout === 'equal') {
      return { width: `${100 / Math.max(1, columns.length)}%` }
    }
    return { width: col.width || 'auto' }
  }

  return (
    <div className={cx('ui-table-shell', `is-layout-${layout}`)}>
      <div className="ui-table-scroll">
        <table
          className={cx(
            'ui-table',
            hoverable && 'is-hoverable',
            layout === 'equal' && 'is-equal',
            layout === 'fixed' && 'is-fixed',
          )}
          style={{ minWidth: tableMinWidth }}
        >
          <thead>
            <tr>
              {columns.map((col) => (
                <th
                  key={col.key}
                  style={colStyle(col)}
                  className={cx(col.align === 'center' && 'is-center', col.align === 'right' && 'is-right')}
                >
                  <div className="ui-table__th">
                    <span>{col.title}</span>
                    {col.sortable && (
                      <span className="ui-table__sort" aria-hidden="true">
                        ↕
                      </span>
                    )}
                  </div>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {!rows.length && (
              <tr>
                <td colSpan={columns.length} className="ui-table__empty-cell">
                  {empty ?? (
                    <div className="ui-table__empty">
                      <div className="ui-table__empty-icon">
                        <UiIcon name="search" size={28} />
                      </div>
                      <div className="ui-table__empty-title">{emptyText}</div>
                    </div>
                  )}
                </td>
              </tr>
            )}
            {rows.map((row, idx) => (
              <tr
                key={row[rowKey] ?? idx}
                className={cx('ui-table__row', selectedKeys.includes(row[rowKey]) && 'is-selected')}
              >
                {columns.map((col) => (
                  <td
                    key={col.key}
                    className={cx(
                      col.align === 'center' && 'is-center',
                      col.align === 'right' && 'is-right',
                      col.mono && 'ui-mono',
                      col.dim && 'ui-cell-dim',
                    )}
                    style={colWidth(col)}
                  >
                    {typeof col.render === 'function' ? col.render(row, idx) : formatCell(col, row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
