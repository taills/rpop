import { useLocation, useNavigate, Outlet } from 'react-router-dom'
import { AppShell, UiButton, UiThemeSwitcher, UiNavLayoutSwitcher, UiTypeSwitcher } from '@/components/ui'
import ErrorBoundary from '@/components/ErrorBoundary.jsx'
import { useToast } from '@/stores/toast'
import { api } from '@/api'
import { useAuthStore } from '@/stores/auth'
import { useBusyAction } from '../busyAction.js'

export default function ShellView() {
  const location = useLocation()
  const navigate = useNavigate()
  const { toast } = useToast()
  const markUnauthenticated = useAuthStore((state) => state.markUnauthenticated)
  const { busy: loggingOut, run } = useBusyAction()

  const menus = [
    { key: 'sites', label: '站点管理', icon: 'dashboard', to: '/sites' },
    { key: 'nodes', label: '节点', icon: 'bot', to: '/nodes' },
    { key: 'topology', label: '拓扑', icon: 'layers', to: '/topology' },
    { key: 'proxies', label: '具名代理', icon: 'link', to: '/proxies' },
    { key: 'logs', label: '访问日志', icon: 'table', to: '/logs' },
    { key: 'trace', label: '请求追踪', icon: 'clock', to: '/trace' },
    { key: 'log-settings', label: '日志适配器', icon: 'database', to: '/log-settings' },
    { key: 'system-settings', label: '系统设置', icon: 'settings', to: '/system-settings' },
    // The catalog / demo pages / token playground are development aids only (see router/index.jsx, which only
    // routes them under import.meta.env.DEV); keeping them out of the production menu keeps operators from
    // stumbling into pages that a production build doesn't even ship.
    ...(import.meta.env.DEV ? [
      {
        key: 'catalog',
        label: '组件目录',
        icon: 'layers',
        to: '/catalog',
        children: [
          { key: 'foundation', label: '基础', icon: 'shield' },
          { key: 'layout', label: '布局', icon: 'layout' },
          { key: 'form', label: '表单', icon: 'form' },
          { key: 'data', label: '数据', icon: 'table' },
          { key: 'feedback', label: '反馈', icon: 'feedback' },
          { key: 'display', label: '展示', icon: 'dashboard' },
        ],
      },
      {
        key: 'demo',
        label: '页面示例',
        icon: 'layout',
        to: '/demo',
        children: [
          { key: 'list', label: '列表页' },
          { key: 'pager', label: '分页样式' },
        ],
      },
      {
        key: 'tokens',
        label: 'Token / 主题',
        icon: 'palette',
        to: '/tokens',
        children: [
          { key: 'category', label: '业务分类' },
          { key: 'theme', label: '主题' },
          { key: 'type', label: '字体档位' },
          { key: 'nav', label: '导航布局' },
        ],
      },
    ] : []),
  ]

  const active = String(location.pathname.split('/').filter(Boolean)[0] || 'sites')
  const currentPrimary = menus.find((m) => m.key === active)
  const secondaryMenus = currentPrimary?.children || []
  const sectionTitle = currentPrimary?.label || ''
  const secondaryActive = (() => {
    const kids = secondaryMenus
    const q = String(new URLSearchParams(location.search).get('section') || '')
    if (q && kids.some((k) => k.key === q)) return q
    return kids[0]?.key || ''
  })()

  function onNav(key) {
    const m = menus.find((x) => x.key === key)
    if (!m?.to) return
    const first = m.children?.[0]?.key
    navigate({ pathname: m.to, search: first ? `?section=${first}` : '' })
  }

  function onSide(key) {
    const primary = active
    navigate({ pathname: location.pathname, search: `?section=${key}` }, { replace: true })
    if (primary === 'catalog') {
      document.getElementById(`section-${key}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      return
    }
    if (primary === 'demo') {
      const el = document.getElementById(`demo-${key}`)
      if (el) el.scrollIntoView({ behavior: 'smooth', block: 'start' })
      return
    }
    if (primary === 'tokens') {
      document.getElementById(`token-${key}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
      return
    }
    toast.info('已切换分区：' + key)
  }

  async function logout() {
    await run(async () => {
      try { await api('/auth/logout', { method: 'POST' }) } catch {}
      markUnauthenticated()
      navigate('/sites')
    })
  }

  return (
    <AppShell
      title="rpop"
      sub="Reverse Proxy over Proxy 控制台"
      menus={menus}
      active={active}
      secondaryMenus={secondaryMenus}
      secondaryActive={secondaryActive}
      sectionTitle={sectionTitle}
      onActiveChange={onNav}
      onSecondaryActiveChange={onSide}
      navRight={
        <>
          {/* The font-scale and nav-layout switchers are demonstrations of the design system itself (see the
              catalog/demo/tokens dev-only pages above), not settings an operator needs day to day; keeping them
              out of the production top bar is also what keeps it narrow enough not to overflow on small
              screens (docs/architecture/control-data-plane.md §5, "阶段 6 审查与冒烟修复"). */}
          {import.meta.env.DEV && <UiTypeSwitcher />}
          {import.meta.env.DEV && <UiNavLayoutSwitcher />}
          <UiThemeSwitcher />
          <UiButton variant="ghost" size="sm" icon="external" loading={Boolean(loggingOut)} onClick={logout}>退出登录</UiButton>
        </>
      }
    >
      {/* Keyed by pathname so navigating away from a page that crashed while rendering remounts a fresh
          boundary instead of staying stuck on its error card. */}
      <ErrorBoundary key={location.pathname}>
        <Outlet />
      </ErrorBoundary>
    </AppShell>
  )
}
