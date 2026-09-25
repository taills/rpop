import { useState, useEffect } from 'react'
import { cx } from '@/utils/cx'
import AppTopNav from './AppTopNav'
import AppSideNav from './AppSideNav'
import { useNavStore } from '@/stores/nav'
import './AppShell.css'

export default function AppShell({
  title = 'ProtoUI Kit',
  sub = '统一原型组件库',
  menus = [],
  active = '',
  /** 当前一级的二级菜单 */
  secondaryMenus = [],
  secondaryActive = '',
  /** 混排/竖版侧栏顶部小标题 */
  sectionTitle = '',
  sideDark = false,
  onActiveChange,
  onSecondaryActiveChange,
  navRight,
  children,
}) {
  const navStore = useNavStore()
  const [sideCollapsed, setSideCollapsed] = useState(false)

  // 竖版切换到混排时，避免侧栏一直折叠
  useEffect(() => {
    if (navStore.modeId === 'horizontal') setSideCollapsed(false)
  }, [navStore.modeId])

  return (
    <div
      className={cx('app-shell', `is-nav-${navStore.modeId}`)}
      style={{ '--side-w': sideCollapsed ? '56px' : '200px' }}
    >
      <AppTopNav
        title={title}
        sub={sub}
        menus={menus}
        active={active}
        showMenus={navStore.showTopMenus}
        showSecondary={navStore.showTopSecondary}
        secondaryMenus={secondaryMenus}
        secondaryActive={secondaryActive}
        showMenuToggle={navStore.isVertical}
        onActiveChange={onActiveChange}
        onSecondaryActiveChange={onSecondaryActiveChange}
        onToggleSide={() => setSideCollapsed((v) => !v)}
        right={navRight}
      />

      <div className="app-shell__body">
        {navStore.showSide && (secondaryMenus.length || navStore.sideShowPrimary) ? (
          <AppSideNav
            primaryMenus={menus}
            secondaryMenus={secondaryMenus}
            active={active}
            secondaryActive={secondaryActive}
            showPrimary={navStore.sideShowPrimary}
            collapsed={sideCollapsed}
            sectionTitle={sectionTitle}
            dark={sideDark}
            onActiveChange={onActiveChange}
            onSecondaryActiveChange={onSecondaryActiveChange}
            onCollapsedChange={setSideCollapsed}
          />
        ) : null}

        <main className="app-shell__main">{children}</main>
      </div>
    </div>
  )
}
