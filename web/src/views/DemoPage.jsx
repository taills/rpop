import { useEffect, useMemo, useRef, useState } from 'react'
import {
  UiPageHeader,
  UiButton,
  UiStats,
  UiStatCard,
  UiCard,
  UiFilterBar,
  UiSearch,
  UiSeg,
  UiSelect,
  UiCheckbox,
  UiTable,
  UiTag,
  UiStatusDot,
  UiOps,
  UiEmpty,
  UiPagination,
  UiDrawer,
  UiDescriptions,
  UiDivider,
  UiProgress,
  UiModal,
  UiField,
  UiInput,
} from '@/components/ui'
import { useToast } from '@/stores/toast'
import './DemoPage.css'

export default function DemoPage() {
  const { toast } = useToast()

  const [kw, setKw] = useState('')
  const [status, setStatus] = useState('all')
  const [type, setType] = useState('')
  const [equalCols, setEqualCols] = useState(false)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(5)
  const [detailVisible, setDetailVisible] = useState(false)
  const [formVisible, setFormVisible] = useState(false)
  const [archiveVisible, setArchiveVisible] = useState(false)
  const [editing, setEditing] = useState(false)
  const [current, setCurrent] = useState(null)
  const [archiveTarget, setArchiveTarget] = useState(null)
  const [err, setErr] = useState('')

  const [p1, setP1] = useState(3)
  const [ps1, setPs1] = useState(10)
  const [p2, setP2] = useState(2)
  const [p3, setP3] = useState(4)
  const [p4, setP4] = useState(2)

  const [form, setForm] = useState({ name: '', type: 'service', owner: '', status: 'draft' })
  const [rows, setRows] = useState([
    { id: 1, name: '用户中心服务', code: 'RES-1001', type: 'service', status: 'published', owner: '张明', time: '2026-09-18 10:20', updated: '09-18', progress: 100 },
    { id: 2, name: '订单列表页', code: 'RES-1002', type: 'page', status: 'review', owner: '李娜', time: '2026-09-18 09:12', updated: '09-18', progress: 72 },
    { id: 3, name: '数据表格组件', code: 'RES-1003', type: 'component', status: 'published', owner: '王强', time: '2026-09-17 16:40', updated: '09-17', progress: 100 },
    { id: 4, name: '接入指南', code: 'RES-1004', type: 'doc', status: 'draft', owner: '赵敏', time: '2026-09-17 11:05', updated: '09-17', progress: 24 },
    { id: 5, name: '权限配置页', code: 'RES-1005', type: 'page', status: 'published', owner: '张明', time: '2026-09-16 15:30', updated: '09-16', progress: 100 },
    { id: 6, name: '消息推送服务', code: 'RES-1006', type: 'service', status: 'review', owner: '李娜', time: '2026-09-16 09:48', updated: '09-16', progress: 60 },
    { id: 7, name: '主题 Token 文档', code: 'RES-1007', type: 'doc', status: 'archived', owner: '王强', time: '2026-09-15 18:02', updated: '09-15', progress: 100 },
    { id: 8, name: '表单校验组件', code: 'RES-1008', type: 'component', status: 'draft', owner: '赵敏', time: '2026-09-15 10:11', updated: '09-15', progress: 35 },
  ])

  const statusOptions = [
    { label: '全部', value: 'all' },
    { label: '已发布', value: 'published' },
    { label: '审核中', value: 'review' },
    { label: '草稿', value: 'draft' },
    { label: '已归档', value: 'archived' },
  ]
  const statusSelect = statusOptions.slice(1)
  const typeOptions = [
    { label: '全部类型', value: '' },
    { label: '服务', value: 'service' },
    { label: '页面', value: 'page' },
    { label: '组件', value: 'component' },
    { label: '文档', value: 'doc' },
  ]
  const statusMap = {
    published: { label: '已发布', tone: 'success' },
    review: { label: '审核中', tone: 'warn' },
    draft: { label: '草稿', tone: 'ai' },
    archived: { label: '已归档', tone: 'muted' },
  }

  function countBy(s) {
    return rows.filter((r) => r.status === s).length
  }
  function typeLabel(t) {
    return typeOptions.find((x) => x.value === t)?.label || t
  }
  function progressTone(row) {
    if (row.progress >= 100) return 'success'
    if (row.progress >= 50) return 'brand'
    return 'warn'
  }
  function reset() {
    setKw('')
    setStatus('all')
    setType('')
    setPage(1)
    toast.info('已重置筛选')
  }
  function openDetail(row) {
    setCurrent(row)
    setDetailVisible(true)
  }
  function openCreate() {
    setEditing(false)
    setForm({ name: '', type: 'service', owner: '', status: 'draft' })
    setErr('')
    setFormVisible(true)
  }
  function openEdit(row) {
    setEditing(true)
    setForm({ name: row.name, type: row.type, owner: row.owner, status: row.status, id: row.id })
    setErr('')
    setFormVisible(true)
  }
  function save() {
    if (!form.name.trim()) {
      setErr('请输入名称')
      toast.error('请填写必填项')
      return
    }
    setErr('')
    if (editing) {
      setRows((prev) =>
        prev.map((r) => (r.id === form.id ? { ...r, ...form, time: '刚刚', updated: '今天' } : r)),
      )
      toast.success('已更新')
    } else {
      setRows((prev) => [
        {
          id: Date.now(),
          name: form.name,
          code: 'RES-' + Math.floor(1000 + Math.random() * 9000),
          type: form.type,
          status: form.status,
          owner: form.owner || '未指派',
          time: '刚刚',
          updated: '今天',
          progress: 0,
        },
        ...prev,
      ])
      toast.success('已创建')
    }
    setFormVisible(false)
  }
  function askArchive(row) {
    setArchiveTarget(row)
    setArchiveVisible(true)
  }
  function doArchive() {
    if (!archiveTarget) return
    setRows((prev) => prev.map((r) => (r.id === archiveTarget.id ? { ...r, status: 'archived' } : r)))
    toast.success('已归档')
    setArchiveVisible(false)
  }

  // Vue watch([kw, status, type]) 默认非 immediate —— 用 ref 跳过首次，避免初始 reset
  const filterWatchSkipped = useRef(false)
  useEffect(() => {
    if (!filterWatchSkipped.current) {
      filterWatchSkipped.current = true
      return
    }
    setPage(1)
  }, [kw, status, type])

  const filtered = useMemo(() => {
    const k = kw.trim().toLowerCase()
    return rows.filter((r) => {
      const okKw =
        !k || r.name.toLowerCase().includes(k) || r.code.toLowerCase().includes(k) || r.owner.includes(k)
      const okStatus = status === 'all' || r.status === status
      const okType = !type || r.type === type
      return okKw && okStatus && okType
    })
  }, [kw, status, type, rows])

  const paged = useMemo(() => {
    const start = (page - 1) * pageSize
    return filtered.slice(start, start + pageSize)
  }, [filtered, page, pageSize])

  const cols = [
    {
      key: 'name',
      title: '名称',
      render: (row) => (
        <>
          <span className="ui-name-link" onClick={() => openDetail(row)}>
            {row.name}
          </span>
          <div className="ui-owner-line">
            {row.code} · {row.owner}
          </div>
        </>
      ),
    },
    {
      key: 'type',
      title: '类型',
      width: '90px',
      render: (row) => <UiTag tone="type">{typeLabel(row.type)}</UiTag>,
    },
    {
      key: 'status',
      title: '状态',
      width: '100px',
      render: (row) => (
        <UiStatusDot tone={statusMap[row.status].tone}>{statusMap[row.status].label}</UiStatusDot>
      ),
    },
    { key: 'updated', title: '更新时间', width: '120px', mono: true },
    {
      key: 'ops',
      title: '操作',
      width: '160px',
      align: 'right',
      render: (row) => (
        <UiOps
          items={[
            { label: '详情', tone: 'primary', onClick: () => openDetail(row) },
            { label: '编辑', tone: 'default', onClick: () => openEdit(row) },
            { label: '归档', tone: 'default', divider: true, onClick: () => askArchive(row) },
          ]}
        />
      ),
    },
  ]

  return (
    <div className="ui-page">
      <UiPageHeader
        eyebrow="管理后台 · 列表页模板"
        title="资源列表"
        sub="通用中后台列表骨架：筛选 · 表格 · 分页 · 详情 · 新建（可直接当原型模板）"
        actions={
          <>
            <UiButton variant="outline" size="sm" onClick={() => toast.info('已刷新')}>
              刷新
            </UiButton>
            <UiButton variant="primary" size="sm" icon="plus" onClick={openCreate}>
              新建
            </UiButton>
          </>
        }
      />

      <UiStats>
        <UiStatCard label="总数" value={rows.length} tone="brand" variant="tinted" hint="当前库内记录" icon="database" />
        <UiStatCard label="已发布" value={countBy('published')} tone="success" variant="flat" icon="check" />
        <UiStatCard label="审核中" value={countBy('review')} tone="warn" variant="banner" icon="clock" />
        <UiStatCard label="草稿" value={countBy('draft')} tone="ai" variant="dark" icon="file" />
      </UiStats>

      <UiCard flush title="列表" icon="table">
        <div className="pad">
          <UiFilterBar
            right={
              <>
                <UiCheckbox value={equalCols} onChange={setEqualCols} label="等宽列" />
                <UiButton size="sm" variant="ghost" onClick={reset}>
                  重置
                </UiButton>
              </>
            }
          >
            <UiSearch value={kw} onChange={setKw} placeholder="名称 / 编号 / 负责人" />
            <UiSeg value={status} onChange={setStatus} options={statusOptions} />
            <UiSelect value={type} onChange={setType} options={typeOptions} placeholder="类型" style={{ width: '128px' }} />
          </UiFilterBar>

          {paged.length ? (
            <UiTable
              columns={cols}
              rows={paged}
              rowKey="id"
              layout={equalCols ? 'equal' : 'auto'}
            />
          ) : (
            <UiEmpty
              title="无匹配数据"
              actions={
                <UiButton size="sm" variant="outline" onClick={reset}>
                  清空筛选
                </UiButton>
              }
            />
          )}

          <UiPagination
            page={page}
            onPageChange={setPage}
            pageSize={pageSize}
            onPageSizeChange={setPageSize}
            total={filtered.length}
            align="right"
            showJumper
            showFirstLast
            showSizeChanger
            totalPosition="start"
          />
        </div>
      </UiCard>

      <div id="demo-pager" className="scroll-anchor" />
      <UiCard title="分页样式对照" className="mt" icon="table">
        <div className="ui-cell-dim mb8">
          同一组件通过 props 切换：对齐方式、总数位置、首页/末页、跳页、每页条数、simple 模式。
        </div>
        <div className="pager-demos">
          <div>
            <div className="demo-label">右对齐 · 总数在前 · 首末页 · 跳页 · 改每页</div>
            <UiPagination
              page={p1}
              onPageChange={setP1}
              pageSize={ps1}
              onPageSizeChange={setPs1}
              total={128}
              align="right"
              showJumper
              showFirstLast
              showSizeChanger
            />
          </div>
          <div>
            <div className="demo-label">居中 · 总数在后 · 首末页</div>
            <UiPagination page={p2} onPageChange={setP2} pageSize={10} total={96} align="center" totalPosition="end" showFirstLast />
          </div>
          <div>
            <div className="demo-label">左对齐 · simple · 无总数</div>
            <UiPagination page={p3} onPageChange={setP3} pageSize={10} total={240} align="left" simple showTotal={false} showFirstLast />
          </div>
          <div>
            <div className="demo-label">紧凑 sm · 居中</div>
            <UiPagination page={p4} onPageChange={setP4} pageSize={10} total={45} align="center" size="sm" showFirstLast />
          </div>
        </div>
      </UiCard>

      <UiDrawer value={detailVisible} onChange={setDetailVisible} title={current?.name || '详情'} eyebrow="记录" width="460px" footer={
        <>
          <UiButton variant="outline" size="sm" onClick={() => setDetailVisible(false)}>
            关闭
          </UiButton>
          <UiButton
            variant="primary"
            size="sm"
            onClick={() => {
              toast.success('已保存')
              setDetailVisible(false)
            }}
          >
            保存
          </UiButton>
        </>
      }>
        {current ? (
          <>
            <UiDescriptions
              column="1"
              items={[
                { label: '编号', key: 'code', value: current.code, mono: true },
                { label: '类型', key: 'type', value: typeLabel(current.type) },
                { label: '状态', key: 'status', value: statusMap[current.status].label },
                { label: '负责人', key: 'owner', value: current.owner },
                { label: '更新时间', key: 'time', value: current.time, mono: true },
              ]}
            />
            <UiDivider label="处理进度" />
            <UiProgress value={current.progress} tone={progressTone(current)} sub={`${current.progress}%`} />
          </>
        ) : null}
      </UiDrawer>

      <UiModal
        value={formVisible}
        onChange={setFormVisible}
        title={editing ? '编辑记录' : '新建记录'}
        eyebrow="表单"
        confirmText="保存"
        onConfirm={save}
      >
        <div className="form-grid">
          <UiField label="名称" required error={err}>
            <UiInput value={form.name} onChange={(v) => setForm({ ...form, name: v })} placeholder="请输入名称" />
          </UiField>
          <UiField label="类型">
            <UiSelect value={form.type} onChange={(v) => setForm({ ...form, type: v })} options={typeOptions.slice(1)} />
          </UiField>
          <UiField label="负责人">
            <UiInput value={form.owner} onChange={(v) => setForm({ ...form, owner: v })} placeholder="姓名" />
          </UiField>
          <UiField label="状态">
            <UiSelect value={form.status} onChange={(v) => setForm({ ...form, status: v })} options={statusSelect} />
          </UiField>
        </div>
      </UiModal>

      <UiModal
        value={archiveVisible}
        onChange={setArchiveVisible}
        title="确认归档？"
        eyebrow="危险操作"
        confirmText="归档"
        confirmVariant="danger"
        onConfirm={doArchive}
      >
        <p>归档后记录将移出默认列表，仍可在「已归档」筛选中查看。</p>
      </UiModal>
    </div>
  )
}
