import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'
import './AppTopNav.css'

export default function AppTopNav({
  title = 'ProtoUI Kit',
  sub = '统一原型组件库',
  menus = [],
  active = '',
  showMenus = true,
  showSecondary = false,
  secondaryMenus = [],
  secondaryActive = '',
  showMenuToggle = false,
  onActiveChange,
  onSecondaryActiveChange,
  onToggleSide,
  right,
}) {
  return (
    <>
      <header
        className={cx(
          'app-top-nav',
          !showMenus && 'is-compact',
          showSecondary && secondaryMenus.length && 'has-secondary',
        )}
      >
        <div className="app-top-nav__brand">
          {showMenuToggle && (
            <button type="button" className="app-top-nav__burger" aria-label="切换侧栏" onClick={() => onToggleSide?.()}>
              <UiIcon name="menu" size={16} />
            </button>
          )}
          <span className="app-top-nav__logo" aria-hidden="true" />
          <div className="app-top-nav__brand-text">
            <div className="app-top-nav__name">{title}</div>
            <div className="app-top-nav__sub">{sub}</div>
          </div>
        </div>

        {showMenus && menus.length ? (
          <nav className="app-top-nav__menus">
            {menus.map((m) => (
              <button
                key={m.key}
                type="button"
                className={cx('app-top-nav__item', m.key === active && 'on')}
                onClick={() => onActiveChange?.(m.key)}
              >
                {m.icon && <UiIcon name={m.icon} size={14} />}
                {m.label}
              </button>
            ))}
          </nav>
        ) : (
          <div className="app-top-nav__menus app-top-nav__menus--spacer" />
        )}

        <div className="app-top-nav__right">{right}</div>
      </header>

      {/* 纯横版：二级横向条 */}
      {showSecondary && secondaryMenus.length > 0 && (
        <div className="app-sub-nav" role="navigation" aria-label="二级菜单">
          {secondaryMenus.map((m) => (
            <button
              key={m.key}
              type="button"
              className={cx('app-sub-nav__item', m.key === secondaryActive && 'on')}
              onClick={() => onSecondaryActiveChange?.(m.key)}
            >
              {m.icon && <UiIcon name={m.icon} size={13} />}
              {m.label}
            </button>
          ))}
        </div>
      )}
    </>
  )
}
