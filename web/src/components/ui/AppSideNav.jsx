import { cx } from '@/utils/cx'
import UiIcon from './UiIcon'
import './AppSideNav.css'

function childrenOf(item) {
  return item?.children || []
}

export default function AppSideNav({
  primaryMenus = [],
  secondaryMenus = [],
  active = '',
  secondaryActive = '',
  showPrimary = false,
  collapsed = false,
  dark = false,
  sectionTitle = '',
  onActiveChange,
  onSecondaryActiveChange,
  onCollapsedChange,
}) {
  function onPrimary(g) {
    onActiveChange?.(g.key)
    const kids = childrenOf(g)
    if (kids.length && !kids.some((k) => k.key === secondaryActive)) {
      onSecondaryActiveChange?.(kids[0].key)
    }
  }

  return (
    <aside
      className={cx(
        'app-side-nav',
        collapsed && 'is-collapsed',
        dark && 'is-dark',
        showPrimary && primaryMenus.length && 'has-primary',
      )}
    >
      <div className="app-side-nav__head">
        <button
          type="button"
          className="app-side-nav__collapse"
          aria-label={collapsed ? '展开侧栏' : '收起侧栏'}
          onClick={() => onCollapsedChange?.(!collapsed)}
        >
          <UiIcon name={collapsed ? 'chevronRight' : 'chevronLeft'} size={14} />
          {!collapsed && <span>收起</span>}
        </button>
      </div>

      {showPrimary ? (
        /* 竖版：一级 + 展开的二级 */
        primaryMenus.map((g) => (
          <div key={g.key} className="app-side-nav__group">
            <button
              type="button"
              className={cx(
                'app-side-nav__item',
                'is-primary',
                g.key === active && 'on',
                g.key === active && childrenOf(g).length && 'is-parent-open',
              )}
              title={g.label}
              onClick={() => onPrimary(g)}
            >
              {g.icon && <UiIcon name={g.icon} size={15} />}
              {!collapsed && <span className="app-side-nav__label">{g.label}</span>}
              {!collapsed && childrenOf(g).length > 0 && (
                <UiIcon
                  name="chevronDown"
                  size={12}
                  className={cx('app-side-nav__chev', g.key === active && 'open')}
                />
              )}
            </button>
            {!collapsed && g.key === active && childrenOf(g).length > 0 && (
              <div className="app-side-nav__children">
                {childrenOf(g).map((c) => (
                  <button
                    key={c.key}
                    type="button"
                    className={cx('app-side-nav__item', 'is-child', c.key === secondaryActive && 'on')}
                    onClick={() => onSecondaryActiveChange?.(c.key)}
                  >
                    <span className="app-side-nav__child-dot" />
                    <span className="app-side-nav__label">{c.label}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
        ))
      ) : (
        /* 混排：侧栏只放二级 */
        <>
          {!collapsed && sectionTitle && <div className="app-side-nav__section">{sectionTitle}</div>}
          {secondaryMenus.map((m) => (
            <button
              key={m.key}
              type="button"
              className={cx('app-side-nav__item', m.key === secondaryActive && 'on')}
              title={m.label}
              onClick={() => onSecondaryActiveChange?.(m.key)}
            >
              {m.icon && <UiIcon name={m.icon} size={14} />}
              {!collapsed && <span className="app-side-nav__label">{m.label}</span>}
            </button>
          ))}
          {!secondaryMenus.length && (
            <div className="app-side-nav__empty">{!collapsed && <span>当前模块无二级菜单</span>}</div>
          )}
        </>
      )}
    </aside>
  )
}
