import './UiCard.css'
import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'

export default function UiCard({
  title = '',
  count = null,
  icon = '',
  /**
   * default | flat | ghost | filled | glass | dark | dashed
   * | gradient | tinted | elevated | accent
   */
  variant = 'default',
  /** brand | success | warn | danger | ai | purple | cyan */
  tone = 'brand',
  /** bar | dot | plain | pill | underline */
  headStyle = 'bar',
  size = 'md',
  flush = false,
  accent = false,
  interactive = false,
  clickable = false,
  media,
  titleExtra,
  extra,
  children,
  footer,
  onClick,
  className = '',
}) {
  const showHead = !!(title || extra != null || titleExtra != null)

  function onCardClick(e) {
    if (!(interactive || clickable)) return
    onClick?.(e)
  }

  return (
    <section
      className={cx(
        'ui-card',
        `is-variant-${variant}`,
        `is-tone-${tone}`,
        `is-head-${headStyle}`,
        `is-size-${size}`,
        flush && 'is-flush',
        (accent || variant === 'accent') && 'is-accent',
        (interactive || clickable) && 'is-interactive',
        className,
      )}
      role={interactive || clickable ? 'button' : undefined}
      tabIndex={interactive || clickable ? 0 : undefined}
      onClick={onCardClick}
      onKeyDown={(e) => {
        if (e.key === 'Enter') onCardClick(e)
      }}
    >
      {media != null && <div className="ui-card__media">{media}</div>}

      {showHead && (
        <header className="ui-card__head">
          <div className="ui-card__title-row">
            {icon && (
              <span className="ui-card__icon">
                <UiIcon name={icon} size={headStyle === 'pill' ? 14 : 16} />
              </span>
            )}
            <h3 className="ui-card__title">
              {headStyle === 'pill' ? <span className="ui-card__title-pill">{title}</span> : title}
            </h3>
            {count !== null && count !== undefined && count !== '' && (
              <span className="ui-card__count">{count}</span>
            )}
            {titleExtra}
          </div>
          {extra != null && <div className="ui-card__extra">{extra}</div>}
        </header>
      )}

      {children != null && <div className="ui-card__body">{children}</div>}
      {footer != null && <footer className="ui-card__foot">{footer}</footer>}
    </section>
  )
}
