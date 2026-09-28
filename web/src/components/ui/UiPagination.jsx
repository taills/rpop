import './UiPagination.css'
import { useEffect, useMemo, useState } from 'react'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiPagination({
  page = 1,
  pageSize = 10,
  total = 0,
  /** left | center | right */
  align = 'right',
  size = 'md',
  simple = false,
  showTotal = true,
  showPageInfo = true,
  /** start 总数在前 | end 总数在后 | both 两处 | none 隐藏 */
  totalPosition = 'start',
  showJumper = false,
  showFirstLast = true,
  showSizeChanger = false,
  pageSizes = [10, 20, 50, 100],
  totalSlot,
  /** 翻页请求进行中：禁用导航按钮并标记 aria-busy，调用方自己决定是否额外显示 spinner/骨架屏 */
  loading = false,
  onPageChange,
  onChange,
  onPageSizeChange,
  onSizeChange,
}) {
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  const [jumper, setJumper] = useState(page)

  useEffect(() => {
    setJumper(page)
  }, [page])

  const pageList = useMemo(() => {
    const n = pageCount
    const cur = page
    if (n <= 7) return Array.from({ length: n }, (_, i) => i + 1)
    const set = new Set([1, n, cur, cur - 1, cur + 1])
    const sorted = [...set].filter((x) => x >= 1 && x <= n).sort((a, b) => a - b)
    const out = []
    for (let i = 0; i < sorted.length; i++) {
      if (i > 0 && sorted[i] - sorted[i - 1] > 1) out.push('…')
      out.push(sorted[i])
    }
    return out
  }, [page, pageCount])

  function go(p) {
    const next = Math.min(Math.max(1, p), pageCount)
    if (next === page) return
    onPageChange?.(next)
    onChange?.(next)
  }

  function onJump() {
    const n = Number(jumper)
    if (!Number.isFinite(n)) return
    go(Math.round(n))
  }

  function handleSizeChange(nextSize) {
    onPageSizeChange?.(nextSize)
    onSizeChange?.(nextSize)
    const maxPage = Math.max(1, Math.ceil(total / nextSize))
    const next = Math.min(Math.max(1, page), maxPage)
    if (next !== page) go(next)
  }

  return (
    <div className={cx('ui-pager', `is-align-${align}`, `is-size-${size}`)}>
      {showTotal && totalPosition !== 'end' && (
        <div className="ui-pager__block ui-pager__total">
          {totalSlot ?? (
            <>
              共 <em className="din">{total}</em> 条
              {showPageInfo && <span className="ui-pager__sep">·</span>}
              {showPageInfo && (
                <span className="ui-pager__page-info">
                  第 <em className="din">{page}</em> / <em className="din">{pageCount}</em> 页
                </span>
              )}
            </>
          )}
        </div>
      )}

      <div className="ui-pager__nav" role="navigation" aria-label="分页" aria-busy={loading || undefined}>
        {showFirstLast && (
          <button
            type="button"
            className="ui-pager__btn is-edge"
            disabled={loading || page <= 1}
            aria-label="首页"
            title="首页"
            onClick={() => go(1)}
          >
            «
          </button>
        )}
        <button
          type="button"
          className="ui-pager__btn is-nav"
          disabled={loading || page <= 1}
          aria-label="上一页"
          onClick={() => go(page - 1)}
        >
          <UiIcon name="chevronLeft" size={14} />
        </button>

        {!simple ? (
          pageList.map((p, i) => (
            <button
              key={`${p}-${i}`}
              type="button"
              className={cx('ui-pager__btn', p === page && 'is-on', p === '…' && 'is-ellipsis')}
              disabled={loading || p === '…'}
              onClick={() => p !== '…' && go(p)}
            >
              {p}
            </button>
          ))
        ) : (
          <span className="ui-pager__simple">
            <em className="din">{page}</em>
            <span className="ui-pager__sep">/</span>
            <em className="din">{pageCount}</em>
          </span>
        )}

        <button
          type="button"
          className="ui-pager__btn is-nav"
          disabled={loading || page >= pageCount}
          aria-label="下一页"
          onClick={() => go(page + 1)}
        >
          <UiIcon name="chevronRight" size={14} />
        </button>
        {showFirstLast && (
          <button
            type="button"
            className="ui-pager__btn is-edge"
            disabled={loading || page >= pageCount}
            aria-label="末页"
            title="末页"
            onClick={() => go(pageCount)}
          >
            »
          </button>
        )}
      </div>

      <div className="ui-pager__block ui-pager__tools">
        {showSizeChanger && (
          <label className="ui-pager__size">
            <span className="ui-pager__size-label">每页</span>
            <select
              className="ui-pager__select"
              value={pageSize}
              disabled={loading}
              onChange={(e) => handleSizeChange(Number(e.target.value))}
            >
              {pageSizes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
            <span className="ui-pager__size-label">条</span>
          </label>
        )}
        {showJumper && (
          <label className="ui-pager__jumper">
            <span className="ui-pager__size-label">跳至</span>
            <input
              className="ui-pager__input"
              type="number"
              min="1"
              max={pageCount}
              value={jumper}
              disabled={loading}
              onChange={(e) => setJumper(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && onJump()}
            />
            <span className="ui-pager__size-label">页</span>
            <button type="button" className="ui-pager__btn is-go" disabled={loading} onClick={onJump}>
              Go
            </button>
          </label>
        )}
      </div>

      {showTotal && totalPosition === 'end' && (
        <div className="ui-pager__block ui-pager__total is-end">
          {totalSlot ?? (
            <>
              共 <em className="din">{total}</em> 条
            </>
          )}
        </div>
      )}
    </div>
  )
}
