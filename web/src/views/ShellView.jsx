import { useLocation, useNavigate, Outlet } from 'react-router-dom'
import { AppShell, UiThemeSwitcher, UiNavLayoutSwitcher, UiTypeSwitcher } from '@/components/ui'
import { useToast } from '@/stores/toast'

export default function ShellView() {
  const location = useLocation()
  const navigate = useNavigate()
  const { toast } = useToast()

  const menus = [
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
  ]

  const active = String(location.pathname.split('/').filter(Boolean)[0] || 'catalog')
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

  return (
    <AppShell
      title="Towere UI Kit"
      sub="通用管理后台组件库"
      menus={menus}
      active={active}
      secondaryMenus={secondaryMenus}
      secondaryActive={secondaryActive}
      sectionTitle={sectionTitle}
      onActiveChange={onNav}
      onSecondaryActiveChange={onSide}
      navRight={
        <>
          <UiTypeSwitcher />
          <UiNavLayoutSwitcher />
          <UiThemeSwitcher />
        </>
      }
    >
      <Outlet />
    </AppShell>
  )
}
