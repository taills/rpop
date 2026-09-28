import { useEffect, useState } from 'react'
import { useToast } from '../stores/toast.js'
import { UiAlert, UiButton, UiCard, UiPageHeader } from '@/components/ui'
import '../Logs.css'

const blankConfig = { adapter: 'file', file: { rotation: 'day', maxSizeBytes: 1073741824, compress: true, keepFiles: 30 }, clickhouse: { url: '', database: 'default', table: 'access_logs', splitMode: 'none', username: '', password: '' }, elasticsearch: { url: '', index: 'rpop-access-logs', splitMode: 'none', authType: 'none', username: '', password: '', apiKey: '' }, s3: { endpoint: '', region: 'us-east-1', bucket: '', prefix: 'rpop/access', splitMode: 'hour', accessKeyId: '', secretAccessKey: '', sessionToken: '', forcePathStyle: true } }
const adapterLabel = { file: '本地文件', clickhouse: 'ClickHouse', elasticsearch: 'Elasticsearch', s3: 'S3 / S3-compatible' }
function adapterIcon(type) {
  if (type === 'file') return '▤'
  if (type === 's3') return '☁'
  return '▥'
}

function parseSize(value) {
  const match = String(value).trim().match(/^([0-9]+(?:\.[0-9]+)?)\s*(B|KB|MB|GB|TB|K|M|G|T|KIB|MIB|GIB|TIB)?$/i)
  if (!match) return null
  const unit = (match[2] || 'B').toUpperCase()
  const binary = ['K', 'M', 'G', 'T', 'KIB', 'MIB', 'GIB', 'TIB'].includes(unit)
  const power = ({ B: 0, K: 1, KB: 1, KIB: 1, M: 2, MB: 2, MIB: 2, G: 3, GB: 3, GIB: 3, T: 4, TB: 4, TIB: 4 })[unit]
  const bytes = Number(match[1]) * (binary ? 1024 : 1000) ** power
  return Number.isSafeInteger(bytes) && bytes > 0 ? bytes : null
}
function formatSize(bytes) { return bytes === 1073741824 ? '1G' : String(bytes) }
function normalizeConfig(value = {}) {
  return { ...blankConfig, ...value, file: { ...blankConfig.file, ...value.file }, clickhouse: { ...blankConfig.clickhouse, ...value.clickhouse }, elasticsearch: { ...blankConfig.elasticsearch, ...value.elasticsearch }, s3: { ...blankConfig.s3, ...value.s3 } }
}

export default function LogSettings({ api, onChange }) {
  const { toast } = useToast()
  const [adapters, setAdapters] = useState([])
  const [editing, setEditing] = useState(null)
  const [name, setName] = useState('')
  const [config, setConfig] = useState(blankConfig)
  const [maxSizeText, setMaxSizeText] = useState(formatSize(blankConfig.file.maxSizeBytes))
  const [passwordSaved, setPasswordSaved] = useState(false)
  const [s3CredentialsSaved, setS3CredentialsSaved] = useState(false)
  const [elasticsearchCredentialsSaved, setElasticsearchCredentialsSaved] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function loadAdapters() {
    const data = await api('/logging')
    setAdapters(data.adapters || [])
  }
  useEffect(() => {
    loadAdapters().catch(e => setError(e.message)).finally(() => setLoading(false))
  }, [api])

  function patch(section, key, value) { setConfig(current => ({ ...current, [section]: { ...current[section], [key]: value } })) }
  function startCreate() {
    setEditing({ id: '' }); setName(''); setConfig(structuredClone(blankConfig)); setMaxSizeText(formatSize(blankConfig.file.maxSizeBytes))
    setPasswordSaved(false); setS3CredentialsSaved(false); setElasticsearchCredentialsSaved(false); setError('')
  }
  function startEdit(adapter) {
    const next = normalizeConfig(adapter.config)
    setEditing({ id: adapter.id }); setName(adapter.name); setConfig(next); setMaxSizeText(formatSize(next.file.maxSizeBytes))
    setPasswordSaved(adapter.hasClickHousePassword); setS3CredentialsSaved(adapter.hasS3Credentials); setElasticsearchCredentialsSaved(adapter.hasElasticsearchCredentials); setError('')
  }
  function closeEditor() { setEditing(null); setError('') }

  async function save(event) {
    event.preventDefault(); setSaving(true); setError('')
    try {
      const size = parseSize(maxSizeText)
      if (config.adapter === 'file' && size === null) throw new Error('大小上限请输入如 1G、512MiB 或 1073741824 的正数')
      const payload = { name: name.trim(), config: config.adapter === 'file' ? { ...config, file: { ...config.file, maxSizeBytes: size } } : config }
      const path = editing.id ? `/logging/adapters/${encodeURIComponent(editing.id)}` : '/logging/adapters'
      const data = await api(path, { method: editing.id ? 'PUT' : 'POST', body: JSON.stringify(payload) })
      const savedId = editing.id || data.savedAdapterId
      const saved = (data.adapters || []).find(item => item.id === savedId)
      setAdapters(data.adapters || [])
      setEditing(null)
      toast.success(saved ? `“${saved.name}”已保存` : '日志适配器已保存')
      await onChange?.()
    } catch (e) { setError(e.message) } finally { setSaving(false) }
  }

  async function remove(adapter) {
    if (!window.confirm(`删除日志适配器“${adapter.name}”？已绑定站点的适配器不能删除。`)) return
    setError('')
    try {
      const data = await api(`/logging/adapters/${encodeURIComponent(adapter.id)}`, { method: 'DELETE' })
      setAdapters(data.adapters || []); toast.success(`“${adapter.name}”已删除`); await onChange?.()
    } catch (e) { toast.error(e.message) }
  }

  if (loading) return <div className="ui-page"><p>正在读取日志配置…</p></div>
  return <div className="ui-page">
    <UiPageHeader
      eyebrow="LOG STORAGE"
      title="日志适配器"
      sub="可以配置多个同类型适配器；每个站点独立选择一个目标。"
      actions={<UiButton variant="primary" icon="plus" onClick={startCreate}>添加适配器</UiButton>}
    />
    {error && <UiAlert type="error">{error}</UiAlert>}
    <UiCard title="适配器列表" icon="database" count={adapters.length || null}>
      <div className="pad">
        {!adapters.length && <div className="adapter-empty">还没有日志适配器。添加一个后，站点才能启用访问日志。</div>}
        <div className="adapter-list">
          {adapters.map(adapter => <article className="adapter-card" key={adapter.id}><div className="adapter-card-icon">{adapterIcon(adapter.config.adapter)}</div><div className="adapter-card-info"><strong>{adapter.name}</strong><span>{adapterLabel[adapter.config.adapter] || adapter.config.adapter} · ID: {adapter.id}</span></div><span className="adapter-type">{adapter.config.adapter}</span><div className="adapter-actions"><button className="secondary" onClick={() => startEdit(adapter)}>编辑</button><button className="secondary danger-action" onClick={() => remove(adapter)}>删除</button></div></article>)}
        </div>
      </div>
    </UiCard>
    {editing && <UiCard title={editing.id ? '编辑适配器' : '添加适配器'} icon={editing.id ? 'edit' : 'plus'}>
      <div className="pad">
        <p className="ui-cell-dim">不同适配器可使用相同类型；保存后可在站点设置中绑定。</p>
        <form className="log-settings" onSubmit={save}>
        <label className="wide">适配器名称<input required maxLength="128" value={name} onChange={e => setName(e.target.value)} placeholder="例如：生产 ClickHouse"/></label>
        <label className="wide">适配器类型<select value={config.adapter} onChange={e => setConfig(current => ({ ...current, adapter: e.target.value }))} disabled={!!editing.id}><option value="file">本地文件</option><option value="clickhouse">ClickHouse</option><option value="elasticsearch">Elasticsearch</option><option value="s3">S3 / S3-compatible</option></select><span>{editing.id ? '编辑时不能改变类型；如需切换类型，请新建一个适配器并重新绑定站点。' : '允许创建多个同类适配器。'}</span></label>
        {config.adapter === 'file' && <>
          <div className="log-settings-section"><h3>文件轮转</h3><p>按系统设置时区的日/小时窗口或文件大小归档；归档文件可自动 gzip 压缩。</p></div>
          <label>分割模式<select value={config.file.rotation} onChange={e => patch('file', 'rotation', e.target.value)}><option value="day">按天</option><option value="hour">按小时</option><option value="size">按大小</option></select></label>
          <label>大小上限<input type="text" value={maxSizeText} onChange={e => { setMaxSizeText(e.target.value); const size = parseSize(e.target.value); if (size !== null) patch('file', 'maxSizeBytes', size) }}/><span>支持 1G、512MiB 或 1073741824；大小模式生效</span></label>
          <label>保留归档文件数<input type="number" min="0" max="10000" value={config.file.keepFiles} onChange={e => patch('file', 'keepFiles', Number(e.target.value))}/><span>0 表示不清理旧归档</span></label>
          <label className="log-check"><input type="checkbox" checked={config.file.compress} onChange={e => patch('file', 'compress', e.target.checked)}/> 归档后使用 gzip 压缩</label>
        </>}
        {config.adapter === 'clickhouse' && <>
          <div className="log-settings-section"><h3>ClickHouse 连接</h3><p>访问日志以 JSONEachRow 写入；数据库需已存在，账号需有建表、写入、查询权限，并能读取 system.tables 以检索分表。</p></div>
          <label className="wide">HTTP(S) URL<input required type="url" value={config.clickhouse.url} onChange={e => patch('clickhouse', 'url', e.target.value)} placeholder="http://127.0.0.1:8123"/></label>
          <label>Database<input required value={config.clickhouse.database} onChange={e => patch('clickhouse', 'database', e.target.value)}/></label><label>Table<input required value={config.clickhouse.table} onChange={e => patch('clickhouse', 'table', e.target.value)}/></label>
          <label className="wide">分割模式<select value={config.clickhouse.splitMode} onChange={e => patch('clickhouse', 'splitMode', e.target.value)}><option value="none">不分割</option><option value="day">按天</option><option value="hour">按小时</option></select><span>按系统设置时区生成表名日期/小时后缀；查询自动检索分表及历史基础表。</span></label>
          <label>用户名<input value={config.clickhouse.username} onChange={e => patch('clickhouse', 'username', e.target.value)}/></label><label>密码<input type="password" autoComplete="new-password" placeholder={passwordSaved ? '已保存；留空保持不变' : '可选'} value={config.clickhouse.password} onChange={e => patch('clickhouse', 'password', e.target.value)}/></label>
        </>}
        {config.adapter === 'elasticsearch' && <>
          <div className="log-settings-section"><h3>Elasticsearch 连接</h3><p>通过 Elasticsearch Bulk API 写入，并用 Search API 查询访问记录。</p></div>
          <label className="wide">集群 HTTP(S) URL<input required type="url" value={config.elasticsearch.url} onChange={e => patch('elasticsearch', 'url', e.target.value)} placeholder="https://elasticsearch.example.com:9200"/></label>
          <label className="wide">索引名称<input required value={config.elasticsearch.index} onChange={e => patch('elasticsearch', 'index', e.target.value)} placeholder="rpop-access-logs"/><span>必须为小写，允许字母、数字、点、连字符和下划线；按天时最多 246 字符，按小时最多 244 字符。</span></label>
          <label className="wide">分割模式<select value={config.elasticsearch.splitMode} onChange={e => patch('elasticsearch', 'splitMode', e.target.value)}><option value="none">不分割</option><option value="day">按天</option><option value="hour">按小时</option></select><span>按系统设置时区生成索引日期/小时后缀；查询自动跨基础索引和分区索引。</span></label>
          <label className="wide">认证方式<select value={config.elasticsearch.authType} onChange={e => { setElasticsearchCredentialsSaved(false); setConfig(current => ({ ...current, elasticsearch: { ...current.elasticsearch, authType: e.target.value, username: '', password: '', apiKey: '' } })) }}><option value="none">无认证</option><option value="basic">用户名与密码</option><option value="apiKey">API Key</option></select></label>
          {config.elasticsearch.authType === 'basic' && <>
            <label>用户名<input required value={config.elasticsearch.username} onChange={e => patch('elasticsearch', 'username', e.target.value)}/></label>
            <label>密码<input type="password" autoComplete="new-password" required={!elasticsearchCredentialsSaved} placeholder={elasticsearchCredentialsSaved ? '已保存；留空保持不变' : ''} value={config.elasticsearch.password} onChange={e => patch('elasticsearch', 'password', e.target.value)}/></label>
          </>}
          {config.elasticsearch.authType === 'apiKey' && <label className="wide">API Key<input type="password" autoComplete="new-password" required={!elasticsearchCredentialsSaved} placeholder={elasticsearchCredentialsSaved ? '已保存；留空保持不变' : '粘贴 Elasticsearch 返回的 Base64 API Key'} value={config.elasticsearch.apiKey} onChange={e => patch('elasticsearch', 'apiKey', e.target.value)}/><span>填写 Base64 编码值，不要包含 Authorization 前缀。</span></label>}
          <div className="log-settings-warning">生产环境建议使用 HTTPS。账号需要有目标索引的写入和搜索权限；索引不存在时，集群还需允许自动创建索引。</div>
        </>}
        {config.adapter === 's3' && <>
          <div className="log-settings-section"><h3>S3 / 兼容对象存储</h3><p>每条访问日志压缩为独立 JSONL gzip 对象；为大规模搜索设置时间范围。</p></div>
          <label className="wide">Endpoint（留空使用 AWS S3）<input type="url" value={config.s3.endpoint} onChange={e => patch('s3', 'endpoint', e.target.value)} placeholder="https://s3.example.com"/></label>
          <label>Region<input required value={config.s3.region} onChange={e => patch('s3', 'region', e.target.value)}/></label><label>Bucket<input required value={config.s3.bucket} onChange={e => patch('s3', 'bucket', e.target.value)}/></label>
          <label className="wide">对象前缀<input value={config.s3.prefix} onChange={e => patch('s3', 'prefix', e.target.value)} placeholder="rpop/access"/></label>
          <label className="wide">分割模式<select value={config.s3.splitMode} onChange={e => patch('s3', 'splitMode', e.target.value)}><option value="none">不分割</option><option value="day">按天</option><option value="hour">按小时</option></select><span>每条日志仍写入独立对象；按系统设置时区的日期/小时分目录，时间范围可缩小对象读取。兼容历史无分割对象；按大小模式仅适用于本地文件。</span></label>
          <label>Access key ID<input required={!s3CredentialsSaved} autoComplete="off" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.accessKeyId} onChange={e => patch('s3', 'accessKeyId', e.target.value)}/></label>
          <label>Secret access key<input required={!s3CredentialsSaved} type="password" autoComplete="new-password" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.secretAccessKey} onChange={e => patch('s3', 'secretAccessKey', e.target.value)}/></label>
          <label>Session token（可选）<input type="password" autoComplete="new-password" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.sessionToken} onChange={e => patch('s3', 'sessionToken', e.target.value)}/></label>
          <label className="log-check"><input type="checkbox" checked={config.s3.forcePathStyle} onChange={e => patch('s3', 'forcePathStyle', e.target.checked)}/> 使用 Path-style URL（MinIO 常用）</label>
        </>}
        <div className="log-settings-warning">存储凭证保存在 SQLite 中且未加密。请保护数据库和备份文件；更新时密钥留空会保留已有值。</div>
        <div className="log-editor-actions"><button type="button" className="secondary" onClick={closeEditor}>取消</button><button className="primary" disabled={saving}>{saving ? '保存中…' : '保存适配器'}</button></div>
      </form>
      </div>
    </UiCard>}
  </div>
}
