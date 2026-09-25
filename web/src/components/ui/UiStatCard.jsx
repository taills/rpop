import './UiStatCard.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiStatCard({
  label,
  value,
  hint = '',
  /**
   * brand | accent | success | warn | danger | ai | purple | cyan | neutral
   * 兼容旧 API：accent/warn/danger/ai 仍可用
   */
  tone = 'brand',
  /**
   * default   标准白底统计卡
   * flat      无阴影
   * filled    次级底
   * tinted    语义色浅底
   * gradient  渐变强调
   * glass     毛玻璃
   * dark      深色抬升
   * banner    横幅（左色条 + 宽行）
   * compact   紧凑（小数字）
   * dashed    虚线占位
   */
  variant = 'default',
  /** sm 用 stat-sm 数字 / md 标准 / lg 加大 */
  size = 'md',
  /** stacked 上下堆叠 | inline 标签在上数字右侧对齐 | banner 横幅左右 */
  layout = 'stacked',
  suffix = '',
  icon = '',
  /** 环比文案，如 +12% / -3% */
  trend = '',
  trendDir = 'up',
  /** 0-100，显示顶部进度条；null 不显示 */
  progress = null,
  /** 占位数字弱化显示（如 —） */
  placeholder = false,
  clickable = false,
  extra,
  onClick,
}) {
  const toneClass = `is-${tone || 'brand'}`
  const trendClass = (() => {
    if (trendDir === 'down') return 'is-down'
    return trend.startsWith('-') ? 'is-down' : 'is-up'
  })()
  const display = (() => {
    if (typeof value === 'number') return value.toLocaleString('zh-CN') + suffix
    return String(value) + suffix
  })()

  return (
    <div
      className={cx(
        'ui-stat',
        `is-variant-${variant}`,
        `is-tone-${tone || 'brand'}`,
        `is-size-${size}`,
        `is-layout-${layout}`,
        clickable && 'is-interactive',
      )}
      role={clickable ? 'button' : undefined}
      tabIndex={clickable ? 0 : undefined}
      onClick={() => {
        if (clickable) onClick?.()
      }}
      onKeyDown={(e) => {
        if (e.key === 'Enter' && clickable) onClick?.()
      }}
    >
      {progress !== null && progress !== undefined && (
        <div className="ui-stat__progress-track" aria-hidden="true">
          <div
            className="ui-stat__progress-bar"
            style={{ width: `${Math.min(100, Math.max(0, Number(progress) || 0))}%` }}
          />
        </div>
      )}
      <div className="ui-stat__glow" aria-hidden="true" />

      <div className="ui-stat__top">
        <div className="ui-stat__label-wrap">
          {icon && (
            <span className="ui-stat__icon">
              <UiIcon name={icon} size={size === 'lg' ? 18 : 16} />
            </span>
          )}
          <span className="ui-stat__label">{label}</span>
        </div>
        {(extra != null || !!trend) && (
          <span className="ui-stat__extra">
            {extra ??
              (trend ? (
                <span className={cx('ui-stat__trend', trendClass)}>
                  <UiIcon name={trendDir === 'down' ? 'chevronDown' : 'trend'} size={12} />
                  {trend}
                </span>
              ) : null)}
          </span>
        )}
      </div>

      <div className={cx('ui-stat__num din', placeholder && 'is-placeholder')}>{display}</div>
      {((typeof hint === 'string' ? hint !== '' : hint != null) || clickable) && (
        <div className="ui-stat__hint">
          {typeof hint === 'string' ? (hint !== '' && <span className="ui-stat__hint-text">{hint}</span>) : hint}
          {clickable && <UiIcon name="chevronRight" size={12} className="ui-stat__chev" />}
        </div>
      )}
    </div>
  )
}
