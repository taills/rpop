import './CatalogPage.css'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import { useThemeStore } from '@/stores/theme'
import { useToast } from '@/stores/toast'
import {
  UiButton,
  UiLink,
  UiOps,
  UiTag,
  UiBadge,
  UiDivider,
  UiSpinner,
  UiCard,
  UiPageHeader,
  UiBreadcrumb,
  UiTabs,
  UiSeg,
  UiSteps,
  UiDescriptions,
  UiStatCard,
  UiStats,
  UiField,
  UiInput,
  UiTextarea,
  UiSelect,
  UiCheckbox,
  UiRadio,
  UiSwitch,
  UiSearch,
  UiFilterBar,
  UiTable,
  UiPagination,
  UiProgress,
  UiTimeline,
  UiEmpty,
  UiSkeleton,
  UiModal,
  UiDrawer,
  UiAlert,
  UiTooltip,
  UiAiBadge,
  UiStatusDot,
  UiRelGraph,
} from '@/components/ui'

const tabs = [
  { key: 'all', label: '全部', count: 24 },
  { key: 'open', label: '待处置', count: 6 },
  { key: 'done', label: '已闭环' },
]
const segOptions = [
  { label: '实时', value: 'live' },
  { label: '近 24h', value: 'd1' },
  { label: '近 7d', value: 'd7' },
]
const typeOptions = [
  { label: '服务', value: 'service' },
  { label: '页面', value: 'page' },
  { label: '组件', value: 'component' },
]
const levelSeg = [
  { label: '全部', value: 'all' },
  { label: '高优先级', value: 'high' },
  { label: '中优先级', value: 'medium' },
]
const levelMap = {
  critical: { label: '紧急', tone: 'risk-critical' },
  high: { label: '高', tone: 'risk-high' },
  medium: { label: '中', tone: 'risk-medium' },
  low: { label: '低', tone: 'risk-low' },
}
const statusMap = {
  open: { label: '待处理', tone: 'warn' },
  doing: { label: '处理中', tone: 'brand' },
  done: { label: '已完成', tone: 'success' },
  ai: { label: 'AI 辅助', tone: 'ai' },
}
const rows = [
  { id: 1, name: '首页配置更新', owner: '张明', level: 'critical', status: 'open', code: 'RES-2001', source: 'CMS' },
  { id: 2, name: '接口鉴权调整', owner: '李娜', level: 'high', status: 'ai', code: 'RES-2002', source: 'API' },
  { id: 3, name: '列表页改版', owner: '王强', level: 'high', status: 'doing', code: 'RES-2003', source: 'Portal' },
  { id: 4, name: '文档归档任务', owner: '赵敏', level: 'medium', status: 'done', code: 'RES-2004', source: 'Wiki' },
  { id: 5, name: '主题 Token 同步', owner: '张明', level: 'medium', status: 'open', code: 'RES-2005', source: 'Kit' },
  { id: 6, name: '移动端适配检查', owner: '李娜', level: 'low', status: 'done', code: 'RES-2006', source: 'QA' },
  { id: 7, name: '权限模型评审', owner: '王强', level: 'critical', status: 'open', code: 'RES-2007', source: 'IAM' },
]
const graphNodes = [
  { id: 'user', short: '用户', label: 'account', x: 320, y: 70 },
  { id: 'role', short: '角色', label: 'editor', x: 160, y: 160, fill: 'var(--soft-orange)', stroke: 'var(--accent-orange)' },
  { id: 'res', short: '资源', label: 'page/home', x: 320, y: 200, fill: 'var(--soft-red)', stroke: 'var(--accent-red)' },
  { id: 'dept', short: '部门', label: 'content', x: 480, y: 160, fill: 'var(--soft-purple)', stroke: 'var(--accent-purple)' },
]
const graphEdges = [
  { from: 'user', to: 'role' },
  { from: 'role', to: 'res' },
  { from: 'user', to: 'dept' },
]

const componentList = [
  'UiIcon', 'UiButton', 'UiLink', 'UiOps', 'UiTag', 'UiBadge', 'UiDivider', 'UiSpinner',
  'UiCard', 'UiPageHeader', 'UiBreadcrumb', 'UiTabs', 'UiSeg', 'UiSteps', 'UiDescriptions',
  'UiStatCard', 'UiStats', 'UiField', 'UiInput', 'UiTextarea', 'UiSelect', 'UiCheckbox',
  'UiRadio', 'UiSwitch', 'UiSearch', 'UiFilterBar', 'UiTable', 'UiPagination', 'UiProgress',
  'UiTimeline', 'UiEmpty', 'UiSkeleton', 'UiModal', 'UiDrawer', 'UiToastHost', 'UiAlert',
  'UiTooltip', 'UiThemeSwitcher', 'UiTypeSwitcher', 'UiNavLayoutSwitcher', 'UiCategorySwitcher',
  'UiAiBadge', 'UiStatusDot', 'UiRelGraph',
  'AppTopNav', 'AppSideNav', 'AppShell',
].join(' · ')

export default function CatalogPage() {
  const themeStore = useThemeStore()
  const { toast } = useToast()
  const location = useLocation()

  const [tab, setTab] = useState('all')
  const [seg, setSeg] = useState('live')
  const [autoRefresh, setAutoRefresh] = useState(true)

  const [demoName, setDemoName] = useState('资源列表页')
  const [demoType, setDemoType] = useState('service')
  const [demoLevel, setDemoLevel] = useState('p1')
  const [demoEnabled, setDemoEnabled] = useState(true)
  const [demoNotify, setDemoNotify] = useState(false)
  const [demoNote, setDemoNote] = useState('')
  const [kw, setKw] = useState('')
  const [nameError, setNameError] = useState('')

  const [modalVisible, setModalVisible] = useState(false)
  const [dangerVisible, setDangerVisible] = useState(false)
  const [drawerVisible, setDrawerVisible] = useState(false)

  function validateForm() {
    if (!demoName.trim()) {
      setNameError('请输入名称')
      toast.error('请填写必填项')
      return
    }
    setNameError('')
    toast.success('校验通过并已提交')
  }
  function resetForm() {
    setDemoName('')
    setDemoType('service')
    setDemoLevel('p1')
    setDemoEnabled(true)
    setDemoNotify(false)
    setDemoNote('')
    setNameError('')
    toast.info('已重置')
  }
  function doDelete() {
    toast.success('已删除（演示）')
    setDangerVisible(false)
  }

  const [tableKw, setTableKw] = useState('')
  const [tableSeg, setTableSeg] = useState('all')
  const [tableEqual, setTableEqual] = useState(false)
  const [page, setPage] = useState(1)

  // 对照源 watch([tableKw, tableSeg])（非 immediate）：首轮跳过
  const skipTableWatchRef = useRef(true)
  useEffect(() => {
    if (skipTableWatchRef.current) {
      skipTableWatchRef.current = false
      return
    }
    setPage(1)
  }, [tableKw, tableSeg])

  const filtered = useMemo(() => {
    const kw = tableKw.trim().toLowerCase()
    return rows.filter((r) => {
      const okKw = !kw || r.name.toLowerCase().includes(kw) || r.code.toLowerCase().includes(kw)
      const okSeg =
        tableSeg === 'all' ||
        (tableSeg === 'high' && (r.level === 'high' || r.level === 'critical')) ||
        (tableSeg === 'medium' && r.level === 'medium')
      return okKw && okSeg
    })
  }, [rows, tableKw, tableSeg])

  const paged = useMemo(() => {
    const start = (page - 1) * 4
    return filtered.slice(start, start + 4)
  }, [filtered, page])

  function resetTable() {
    setTableKw('')
    setTableSeg('all')
    setPage(1)
    toast.info('已清空筛选')
  }
  function openRow(row) {
    setDrawerVisible(true)
    toast.info('查看：' + row.name)
  }
  function confirmDelete(row) {
    setDemoName(row.name)
    setDangerVisible(true)
  }

  const cols = [
    {
      key: 'name',
      title: '名称',
      minWidth: '160px',
      render: (row) => (
        <>
          <span className="ui-name-link">{row.name}</span>
          <div className="ui-owner-line">{row.owner}</div>
        </>
      ),
    },
    { key: 'level', title: '优先级', width: '80px', render: (row) => <UiTag tone={levelMap[row.level].tone}>{levelMap[row.level].label}</UiTag> },
    { key: 'status', title: '状态', width: '100px', render: (row) => <UiStatusDot tone={statusMap[row.status].tone}>{statusMap[row.status].label}</UiStatusDot> },
    { key: 'code', title: '编号', width: '130px', mono: true, render: (row) => <span className="ui-mono">{row.code}</span> },
    { key: 'source', title: '来源', width: '90px', render: (row) => <span className="ui-cell-dim">{row.source}</span> },
    {
      key: 'ops',
      title: '操作',
      width: '180px',
      align: 'right',
      render: (row) => (
        <UiOps
          items={[
            { label: '查看', tone: 'primary', onClick: () => openRow(row) },
            { label: '编辑', tone: 'default', onClick: () => toast.info('编辑 ' + row.name) },
            { label: '导出', tone: 'default', onClick: () => toast.info('导出 ' + row.name) },
            { label: '删除', tone: 'danger', divider: true, onClick: () => confirmDelete(row) },
          ]}
        />
      ),
    },
  ]

  // 对照源 watch(() => route.query.section, ..., { immediate: true })
  const section = new URLSearchParams(location.search).get('section')
  useEffect(() => {
    if (!section) return
    requestAnimationFrame(() => {
      document.getElementById(`section-${section}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    })
  }, [section])

  return (
    <div className="ui-page">
      <UiPageHeader
        eyebrow="Towere UI Kit"
        title="组件目录"
        sub="Token 驱动 · 主题 / 导航 / 字体档位可切换 · 业务页禁止私搭样式"
        actions={
          <>
            <UiButton variant="outline" size="sm" onClick={() => toast.info('当前主题：' + themeStore.theme.name)}>当前主题</UiButton>
            <UiButton variant="primary" size="sm" onClick={() => themeStore.nextTheme()}>换下一个主题</UiButton>
          </>
        }
      />

      <UiAlert type="info" title="使用约定">
        业务页只用 <code>src/components/ui</code> 组件与语义 Token；主题 / 导航 / 字体档位通过{' '}
        <code>data-*</code> 属性切换，组件无需改样式。
      </UiAlert>

      {/* 基础 */}
      <div id="section-foundation" className="scroll-anchor" />
      <UiCard title="按钮 UiButton" icon="zap">
        <div className="row">
          <UiButton variant="primary" onClick={() => toast.success('主操作完成')}>主要操作</UiButton>
          <UiButton variant="accent" onClick={() => setModalVisible(true)}>打开 Modal</UiButton>
          <UiButton variant="outline" onClick={() => setDrawerVisible(true)}>打开 Drawer</UiButton>
          <UiButton variant="soft">软强调</UiButton>
          <UiButton variant="ghost">弱操作</UiButton>
          <UiButton variant="danger" onClick={() => setDangerVisible(true)}>危险（确认）</UiButton>
          <UiButton size="sm" variant="primary" icon="plus">紧凑</UiButton>
          <UiButton size="lg" variant="primary">大号</UiButton>
          <UiButton variant="outline" loading>加载中</UiButton>
          <UiButton variant="outline" disabled>禁用</UiButton>
        </div>
      </UiCard>

      <UiCard title="标签 / 徽标 / 状态" className="mt" icon="shield">
        <div className="row">
          <UiTag tone="type">类型</UiTag>
          <UiTag tone="risk-critical">严重</UiTag>
          <UiTag tone="risk-high">高危</UiTag>
          <UiTag tone="risk-medium">中危</UiTag>
          <UiTag tone="risk-low" icon="bolt">低危</UiTag>
          <UiTag tone="success">已完成</UiTag>
          <UiTag tone="warn">待处置</UiTag>
          <UiTag tone="info">处置中</UiTag>
          <UiTag tone="ai">AI 推荐</UiTag>
          <UiTag tone="purple">扩展</UiTag>
          <UiTag tone="muted">归档</UiTag>
          <UiAiBadge>AI 辅助</UiAiBadge>
          <UiBadge value={12} tone="brand" />
          <UiBadge value={3} tone="danger" />
          <UiBadge value="99+" tone="warn" />
        </div>
        <UiDivider label="状态点" />
        <div className="row">
          <UiStatusDot tone="success">在线</UiStatusDot>
          <UiStatusDot tone="warn">降级</UiStatusDot>
          <UiStatusDot tone="danger">离线</UiStatusDot>
          <UiStatusDot tone="brand">同步中</UiStatusDot>
          <UiStatusDot tone="ai">Agent 运行</UiStatusDot>
          <UiStatusDot>未知</UiStatusDot>
          <UiTooltip content="悬停显示说明">
            <UiButton size="sm" variant="outline">Tooltip</UiButton>
          </UiTooltip>
          <UiLink onClick={() => toast.info('文字链')}>文字链</UiLink>
        </div>
      </UiCard>

      {/* 布局 */}
      <div id="section-layout" className="scroll-anchor" />
      <UiCard title="页头 / 面包屑 / Tabs / Seg" className="mt" icon="layout">
        <UiBreadcrumb
          items={[{ label: '首页', to: true }, { label: '内容', to: true }, { label: '资源列表' }]}
          onNavigate={(item) => toast.info('导航：' + item.label)}
        />
        <UiTabs value={tab} onChange={setTab} tabs={tabs} />
        <div className="row">
          <UiSeg value={seg} onChange={setSeg} options={segOptions} />
          <UiSwitch value={autoRefresh} onChange={setAutoRefresh} />
          <span className="ui-cell-dim">自动刷新 {autoRefresh ? '开' : '关'}</span>
        </div>
      </UiCard>

      <UiCard title="卡片风格 UiCard（variant / tone / headStyle）" className="mt" icon="layout">
        <div className="ui-cell-dim mb8">
          统一 Token 驱动：默认 · flat · ghost · filled · glass · dark · dashed · gradient · tinted · elevated · accent；
          headStyle：bar · dot · plain · pill · underline；支持 media / footer 插槽与 interactive。
        </div>
        <div className="card-gallery">
          <UiCard title="默认 default" headStyle="bar" size="sm" icon="layers">标准运营面板卡，带竖条标题。</UiCard>
          <UiCard title="圆点 dot" headStyle="dot" size="sm" count="12">面板圆点标题，信息块常用。</UiCard>
          <UiCard title="PLAIN 标题" headStyle="plain" size="sm">弱化标题，嵌套子区块。</UiCard>
          <UiCard title="胶囊 pill" headStyle="pill" size="sm" icon="bot">标题做语义胶囊。</UiCard>
          <UiCard title="下划线 underline" headStyle="underline" size="sm">标题行通栏分割。</UiCard>
          <UiCard title="浅色强调" variant="accent" size="sm" icon="zap" interactive onClick={() => toast.info('accent 卡')}>顶部品牌软渐变。</UiCard>
          <UiCard title="语义绿 tinted" variant="tinted" tone="success" size="sm" icon="check">成功 / 已闭环语义底。</UiCard>
          <UiCard title="语义橙 tinted" variant="tinted" tone="warn" size="sm" icon="alert">待处置 / 警告语义底。</UiCard>
          <UiCard title="语义红 tinted" variant="tinted" tone="danger" size="sm">严重 / 失败语义底。</UiCard>
          <UiCard title="AI 紫 tinted" variant="tinted" tone="ai" size="sm" icon="bot" count="AI">AI 推荐语义底。</UiCard>
          <UiCard title="填充 filled" variant="filled" headStyle="dot" size="sm">次级底，适合分区。</UiCard>
          <UiCard title="无边框 ghost" variant="ghost" headStyle="plain" size="sm">弱容器，贴内容。</UiCard>
          <UiCard title="扁平 flat" variant="flat" size="sm">无阴影描边卡。</UiCard>
          <UiCard title="毛玻璃 glass" variant="glass" size="sm">半透明磨砂，适合叠层。</UiCard>
          <UiCard title="投影 elevated" variant="elevated" size="sm" interactive onClick={() => toast.success('elevated 可点')}>加重投影 + hover 抬升。</UiCard>
          <UiCard title="深色 dark" variant="dark" headStyle="dot" size="sm" icon="shield">任意主题下的深色抬升卡。</UiCard>
          <UiCard title="品牌渐变" variant="gradient" size="sm" icon="trend">KPI / 英雄数据强调。</UiCard>
          <UiCard title="AI 渐变" variant="gradient" tone="ai" size="sm" icon="bot">AI 域渐变强调卡。</UiCard>
          <UiCard title="危险渐变" variant="gradient" tone="danger" size="sm">风险强调（克制使用）。</UiCard>
          <UiCard title="虚线 dashed" variant="dashed" headStyle="plain" size="sm">占位 / 投放区。</UiCard>
          <UiCard title="紧凑 sm" size="sm" headStyle="dot">size=sm 更紧内边距。</UiCard>
          <UiCard title="宽松 lg" size="lg" headStyle="bar">size=lg 展示型区块。</UiCard>
          <UiCard
            variant="flat"
            size="sm"
            title="媒体 + 底栏"
            headStyle="underline"
            media={<div className="card-media-demo">media slot</div>}
            footer={
              <>
                <UiLink onClick={() => toast.info('footer 操作')}>次要操作</UiLink>
                <UiButton size="sm" variant="primary" onClick={() => toast.success('主操作')}>主操作</UiButton>
              </>
            }
          >
            上方 media 区 + 标题 + 正文。
          </UiCard>
          <UiCard title="flush 表格壳" flush size="sm" count="4">
            <div className="pad8">flush 去 body 边距，表格/列表通栏。</div>
          </UiCard>
        </div>
      </UiCard>

      <UiCard title="统计卡 UiStatCard（variant / tone / layout）" className="mt">
        <div className="ui-cell-dim mb8">
          variant：default · flat · filled · tinted · gradient · glass · dark · banner · compact · dashed；
          tone：brand · success · warn · danger · ai · purple · cyan · neutral；
          layout：stacked · inline · banner；支持 trend / progress / size。
        </div>
        <UiStats>
          <UiStatCard label="记录总数" value={1284} tone="brand" variant="default" hint="点击下钻" clickable icon="database" trend="+12%" onClick={() => toast.info('跳转列表')} />
          <UiStatCard label="风险分" value="72" tone="warn" variant="tinted" hint="语义色" icon="trend" trend="-4%" trendDir="down" />
          <UiStatCard label="严重事件" value={3} tone="danger" variant="gradient" icon="shield" />
          <UiStatCard label="AI 处理率" value="97.6%" tone="ai" variant="dark" hint="近 7 日" icon="bot" />
        </UiStats>
        <div className="card-gallery">
          <UiStatCard label="填充 filled" value={42} variant="filled" tone="cyan" size="sm" icon="database" />
          <UiStatCard label="扁平 flat" value={18} variant="flat" tone="success" size="sm" hint="无阴影" />
          <UiStatCard label="毛玻璃 glass" value="96%" variant="glass" tone="purple" size="sm" icon="eye" />
          <UiStatCard label="横幅 banner" value={7} variant="banner" tone="warn" hint="左侧色条" icon="alert" />
          <UiStatCard label="紧凑 compact" value={256} variant="compact" tone="brand" icon="bolt" />
          <UiStatCard label="进度 progress" value="68%" variant="tinted" tone="success" progress={68} hint="顶部进度条" />
          <UiStatCard label="虚线占位" value="—" variant="dashed" tone="neutral" size="sm" placeholder hint="预留位" />
          <UiStatCard label="渐变成功" value="128" variant="gradient" tone="success" size="sm" icon="check" trend="+8" />
          <UiStatCard label="渐变 AI" value="54" variant="gradient" tone="ai" size="sm" icon="bot" hint="推荐闭环" />
          <UiStatCard label="大号 lg" value={2048} variant="default" tone="brand" size="lg" icon="trend" />
        </div>
      </UiCard>

      <UiCard title="描述列表 / 步骤" className="mt">
        <div className="grid-2">
          <UiDescriptions
            column="2"
            items={[
              { label: '主机', key: 'host', value: '192.168.81.98', mono: true },
              { label: '案件', key: 'case', value: 'INV-0425-01', mono: true },
              { label: '负责人', key: 'owner', value: '张伟' },
              { label: '状态', key: 'status', value: '处置中' },
            ]}
          />
          <UiSteps
            current={1}
            steps={[
              { title: '创建', desc: '提交录入' },
              { title: '校验', desc: '规则 + AI 辅助' },
              { title: '审核', desc: '人工确认' },
              { title: '闭环', desc: '报告归档' },
            ]}
          />
        </div>
      </UiCard>

      {/* 表单 */}
      <div id="section-form" className="scroll-anchor" />
      <UiCard title="表单控件" className="mt" icon="form">
        <div className="form-grid">
          <UiField label="名称" required error={nameError}>
            <UiInput value={demoName} onChange={setDemoName} placeholder="请输入" prefixIcon="file" />
          </UiField>
          <UiField label="类型">
            <UiSelect value={demoType} onChange={setDemoType} options={typeOptions} />
          </UiField>
          <UiField label="优先级">
            <div className="row">
              <UiRadio value={demoLevel} onChange={setDemoLevel} optionValue="p0" label="P0" />
              <UiRadio value={demoLevel} onChange={setDemoLevel} optionValue="p1" label="P1" />
              <UiRadio value={demoLevel} onChange={setDemoLevel} optionValue="p2" label="P2" />
            </div>
          </UiField>
          <UiField label="选项">
            <div className="row">
              <UiCheckbox value={demoEnabled} onChange={setDemoEnabled} label="立即启用" />
              <UiCheckbox value={demoNotify} onChange={setDemoNotify} label="通知运营" />
            </div>
          </UiField>
          <UiField label="备注">
            <UiTextarea value={demoNote} onChange={setDemoNote} placeholder="可选说明" rows={2} />
          </UiField>
        </div>
        <div className="row mt">
          <UiSearch value={kw} onChange={setKw} placeholder="筛选名称 / IP" />
          <UiButton size="sm" variant="primary" onClick={validateForm}>校验并提交</UiButton>
          <UiButton size="sm" variant="ghost" onClick={resetForm}>重置</UiButton>
        </div>
      </UiCard>

      {/* 数据 */}
      <div id="section-data" className="scroll-anchor" />
      <UiCard title="表格 UiTable + 筛选 + 分页（可交互）" className="mt" flush icon="table">
        <div className="pad">
          <UiFilterBar
            right={
              <>
                <UiButton size="sm" variant="ghost" onClick={resetTable}>重置</UiButton>
                <UiButton size="sm" variant="primary" icon="download" onClick={() => toast.success('已导出当前页')}>导出</UiButton>
              </>
            }
          >
            <UiSearch value={tableKw} onChange={setTableKw} placeholder="筛选名称 / 编号" />
            <UiSeg value={tableSeg} onChange={setTableSeg} options={levelSeg} />
            <UiCheckbox value={tableEqual} onChange={setTableEqual} label="等宽列" />
          </UiFilterBar>

          {paged.length ? (
            <UiTable
              columns={cols}
              rows={paged}
              rowKey="id"
              layout={tableEqual ? 'equal' : 'auto'}
            />
          ) : (
            <UiEmpty
              title="无匹配数据"
              actions={<UiButton size="sm" variant="outline" onClick={resetTable}>清空筛选</UiButton>}
            />
          )}
          {filtered.length > 0 && (
            <UiPagination
              page={page}
              onPageChange={setPage}
              pageSize={4}
              total={filtered.length}
              align="right"
              showJumper
              showFirstLast
            />
          )}
        </div>
      </UiCard>

      <UiCard title="进度 / 时间线 / 骨架 / 空态" className="mt">
        <div className="grid-2">
          <div>
            <UiProgress value={68} tone="brand" sub="进行中" />
            <div className="mt">
              <UiProgress value={100} tone="success" label="已完成" />
            </div>
            <div className="mt">
              <UiProgress value={24} tone="warn" sub="处理中" />
            </div>
            <div className="mt">
              <UiProgress value={42} tone="ai" sub="智能处理" />
            </div>
            <div className="row mt">
              <UiSpinner size="sm" />
              <UiSpinner />
              <UiSpinner size="lg" />
            </div>
            <div className="col-gap mt">
              <UiSkeleton width="70%" />
              <UiSkeleton width="100%" />
              <UiSkeleton width="40%" />
            </div>
          </div>
          <UiTimeline
            items={[
              { title: '创建记录', time: '09:12', desc: '提交表单', tone: 'brand' },
              { title: '自动校验', time: '09:13', desc: '规则通过', tone: 'ai' },
              { title: '进入审核', time: '09:14', desc: '等待人工确认', tone: 'warn' },
              { title: '发布完成', time: '09:21', desc: '状态变为已发布', tone: 'success' },
            ]}
          />
        </div>
      </UiCard>

      {/* 反馈 */}
      <div id="section-feedback" className="scroll-anchor" />
      <UiCard title="反馈组件" className="mt" icon="feedback">
        <UiAlert type="info" title="信息">默认信息提示，用于规范说明。</UiAlert>
        <UiAlert type="success" title="成功">操作已完成。</UiAlert>
        <UiAlert type="warn" title="警告">存在待确认项。</UiAlert>
        <UiAlert type="error" title="错误">配置校验未通过。</UiAlert>
        <UiAlert type="ai" title="AI 建议">
          <span className="row" style={{ gap: '6px' }}>
            <UiAiBadge />
            建议将该 IP 加入观察名单 24 小时。
          </span>
        </UiAlert>
        <div className="row">
          <UiButton size="sm" variant="outline" onClick={() => toast.success('已保存')}>Toast 成功</UiButton>
          <UiButton size="sm" variant="outline" onClick={() => toast.error('网络超时')}>Toast 失败</UiButton>
          <UiButton size="sm" variant="outline" onClick={() => toast.warn('将影响 3 台设备')}>Toast 警告</UiButton>
          <UiButton size="sm" variant="outline" onClick={() => setDrawerVisible(true)}>Drawer</UiButton>
        </div>
      </UiCard>

      {/* 展示 */}
      <div id="section-display" className="scroll-anchor" />
      <UiCard title="展示辅助（通用状态 / 关系）" className="mt" icon="dashboard">
        <div className="grid-2">
          <UiRelGraph height="260px" nodes={graphNodes} edges={graphEdges} />
          <div>
            <div className="section-title">语义色（全局唯一用法）</div>
            <div className="row mt">
              <UiTag tone="success">已完成</UiTag>
              <UiTag tone="warn">进行中</UiTag>
              <UiTag tone="info">处理中</UiTag>
              <UiTag tone="ai">AI</UiTag>
              <UiTag tone="purple">扩展</UiTag>
              <UiTag tone="muted">归档</UiTag>
            </div>
            <div className="row mt">
              <UiTag tone="risk-critical">紧急</UiTag>
              <UiTag tone="risk-high">高</UiTag>
              <UiTag tone="risk-medium">中</UiTag>
              <UiTag tone="risk-low">低</UiTag>
              <span className="ui-cell-dim">等级色可映射优先级 / 严重度等业务语义</span>
            </div>
            <UiDivider label="字体样例" />
            <div className="font-samples">
              <div><span className="label">正文</span><span>列表正文 12–13px</span></div>
              <div><span className="label">等宽</span><span className="ui-mono">RES-1001 · 2026-09-18</span></div>
              <div><span className="label">DIN</span><span className="din" style={{ fontSize: '22px', fontWeight: 700 }}>97.6%</span></div>
              <div><span className="label">标题字</span><span className="display" style={{ fontSize: '18px' }}>管理后台总览</span></div>
            </div>
          </div>
        </div>
      </UiCard>

      <UiCard title="组件清单" className="mt">
        <p className="mono-line">
          {componentList}
        </p>
      </UiCard>

      <UiModal value={modalVisible} onChange={setModalVisible} title="标准 Modal" eyebrow="示例" confirmText="确定" onConfirm={() => toast.success('Modal 确认')}>
        <p>弹层支持 Esc / 遮罩 / 关闭钮；危险操作请用 <code>danger</code> 按钮二次确认。</p>
      </UiModal>

      <UiModal value={dangerVisible} onChange={setDangerVisible} title="确认删除？" eyebrow="危险操作" confirmText="删除" confirmVariant="danger" onConfirm={doDelete}>
        <p>将删除「{demoName || '未命名'}」，此操作不可撤销。</p>
      </UiModal>

      <UiDrawer
        value={drawerVisible}
        onChange={setDrawerVisible}
        title="记录详情"
        eyebrow="Drawer"
        width="420px"
        footer={
          <>
            <UiButton variant="outline" size="sm" onClick={() => setDrawerVisible(false)}>关闭</UiButton>
            <UiButton variant="primary" size="sm" onClick={() => { toast.success('已保存'); setDrawerVisible(false) }}>保存</UiButton>
          </>
        }
      >
        <UiDescriptions
          column="1"
          items={[
            { label: '名称', key: 'n', value: '资源列表页' },
            { label: '编号', key: 'code', value: 'RES-1002', mono: true },
            { label: '状态', key: 's', value: '处理中' },
          ]}
        />
        <UiDivider />
        <UiTimeline
          items={[
            { title: '创建', time: '09:12', tone: 'brand' },
            { title: '校验', time: '09:13', tone: 'ai' },
            { title: '发布', time: '09:14', tone: 'success' },
          ]}
        />
      </UiDrawer>
    </div>
  )
}
