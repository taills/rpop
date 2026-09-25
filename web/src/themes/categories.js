/**
 * 业务分类注册表
 * 组件库本身不绑定具体业务；分类用于「按业务场景组织演示与文档」。
 * 当前默认：管理后台。后续可扩展数据大屏、工作流等。
 */

export const CATEGORIES = [
  {
    id: 'admin',
    name: '管理后台',
    desc: '通用中后台：导航、列表、表单、详情、配置、反馈',
    status: 'ready',
    modules: [
      { key: 'navigation', label: '导航布局', icon: 'layout' },
      { key: 'form', label: '表单录入', icon: 'form' },
      { key: 'data', label: '数据列表', icon: 'table' },
      { key: 'feedback', label: '反馈弹层', icon: 'feedback' },
      { key: 'display', label: '状态展示', icon: 'dashboard' },
    ],
  },
  {
    id: 'dashboard',
    name: '数据看板',
    desc: '指标卡、趋势、筛选联动（规划中，可复用 admin 组件）',
    status: 'planned',
    modules: [],
  },
]

export const DEFAULT_CATEGORY = 'admin'

export function categoryById(id) {
  return CATEGORIES.find((c) => c.id === id) || CATEGORIES[0]
}
