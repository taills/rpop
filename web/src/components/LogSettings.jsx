import { useEffect, useState } from 'react'
import '../Logs.css'

const blankConfig = { adapter: 'file', file: { rotation: 'day', maxSizeBytes: 1073741824, compress: true, keepFiles: 30 }, clickhouse: { url: '', database: 'default', table: 'access_logs', username: '', password: '' }, s3: { endpoint: '', region: 'us-east-1', bucket: '', prefix: 'rpop/access', accessKeyId: '', secretAccessKey: '', sessionToken: '', forcePathStyle: true } }
const adapterLabel = { file: '本地文件', clickhouse: 'ClickHouse', s3: 'S3 / S3-compatible' }
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
  return { ...blankConfig, ...value, file: { ...blankConfig.file, ...value.file }, clickhouse: { ...blankConfig.clickhouse, ...value.clickhouse }, s3: { ...blankConfig.s3, ...value.s3 } }
}

export default function LogSettings({ api, onChange }) {
  const [adapters, setAdapters] = useState([])
  const [editing, setEditing] = useState(null)
  const [name, setName] = useState('')
  const [config, setConfig] = useState(blankConfig)
  const [maxSizeText, setMaxSizeText] = useState(formatSize(blankConfig.file.maxSizeBytes))
  const [passwordSaved, setPasswordSaved] = useState(false)
  const [s3CredentialsSaved, setS3CredentialsSaved] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState('')
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
    setPasswordSaved(false); setS3CredentialsSaved(false); setError(''); setMessage('')
  }
  function startEdit(adapter) {
    const next = normalizeConfig(adapter.config)
    setEditing({ id: adapter.id }); setName(adapter.name); setConfig(next); setMaxSizeText(formatSize(next.file.maxSizeBytes))
    setPasswordSaved(adapter.hasClickHousePassword); setS3CredentialsSaved(adapter.hasS3Credentials); setError(''); setMessage('')
  }
  function closeEditor() { setEditing(null); setError('') }

  async function save(event) {
    event.preventDefault(); setSaving(true); setMessage(''); setError('')
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
      setMessage(saved ? `“${saved.name}”已保存` : '日志适配器已保存')
      await onChange?.()
    } catch (e) { setError(e.message) } finally { setSaving(false) }
  }

  async function remove(adapter) {
    if (!window.confirm(`删除日志适配器“${adapter.name}”？已绑定站点的适配器不能删除。`)) return
    setError(''); setMessage('')
    try {
      const data = await api(`/logging/adapters/${encodeURIComponent(adapter.id)}`, { method: 'DELETE' })
      setAdapters(data.adapters || []); setMessage(`“${adapter.name}”已删除`); await onChange?.()
    } catch (e) { setError(e.message) }
  }

  if (loading) return <section className="logs-page"><p>正在读取日志配置…</p></section>
  return <section className="logs-page">
    <div className="logs-heading"><div><div className="eyebrow">LOG STORAGE</div><h1>日志适配器</h1><p>可以配置多个同类型适配器；每个站点独立选择一个目标。</p></div><button className="primary" onClick={startCreate}>＋ 添加适配器</button></div>
    {error && <div className="error">{error}</div>}{message && <div className="log-success">{message}</div>}
    <div className="adapter-list">
      {!adapters.length && <div className="adapter-empty">还没有日志适配器。添加一个后，站点才能启用访问日志。</div>}
      {adapters.map(adapter => <article className="adapter-card" key={adapter.id}><div className="adapter-card-icon">{adapterIcon(adapter.config.adapter)}</div><div className="adapter-card-info"><strong>{adapter.name}</strong><span>{adapterLabel[adapter.config.adapter] || adapter.config.adapter} · ID: {adapter.id}</span></div><span className="adapter-type">{adapter.config.adapter}</span><div className="adapter-actions"><button className="secondary" onClick={() => startEdit(adapter)}>编辑</button><button className="secondary danger-action" onClick={() => remove(adapter)}>删除</button></div></article>)}
    </div>
    {editing && <div className="log-editor"><div className="log-settings-section"><h2>{editing.id ? '编辑适配器' : '添加适配器'}</h2><p>不同适配器可使用相同类型；保存后可在站点设置中绑定。</p></div>
      <form className="log-settings" onSubmit={save}>
        <label className="wide">适配器名称<input required maxLength="128" value={name} onChange={e => setName(e.target.value)} placeholder="例如：生产 ClickHouse"/></label>
        <label className="wide">适配器类型<select value={config.adapter} onChange={e => setConfig(current => ({ ...current, adapter: e.target.value }))} disabled={!!editing.id}><option value="file">本地文件</option><option value="clickhouse">ClickHouse</option><option value="s3">S3 / S3-compatible</option></select><span>{editing.id ? '编辑时不能改变类型；如需切换类型，请新建一个适配器并重新绑定站点。' : '允许创建多个同类适配器。'}</span></label>
        {config.adapter === 'file' && <>
          <div className="log-settings-section"><h3>文件轮转</h3><p>按 UTC 时间窗口或文件大小归档；归档文件可自动 gzip 压缩。</p></div>
          <label>分割模式<select value={config.file.rotation} onChange={e => patch('file', 'rotation', e.target.value)}><option value="day">按天</option><option value="hour">按小时</option><option value="size">按大小</option></select></label>
          <label>大小上限<input type="text" value={maxSizeText} onChange={e => { setMaxSizeText(e.target.value); const size = parseSize(e.target.value); if (size !== null) patch('file', 'maxSizeBytes', size) }}/><span>支持 1G、512MiB 或 1073741824；大小模式生效</span></label>
          <label>保留归档文件数<input type="number" min="0" max="10000" value={config.file.keepFiles} onChange={e => patch('file', 'keepFiles', Number(e.target.value))}/><span>0 表示不清理旧归档</span></label>
          <label className="log-check"><input type="checkbox" checked={config.file.compress} onChange={e => patch('file', 'compress', e.target.checked)}/> 归档后使用 gzip 压缩</label>
        </>}
        {config.adapter === 'clickhouse' && <>
          <div className="log-settings-section"><h3>ClickHouse 连接</h3><p>访问日志以 JSONEachRow 写入；请确保数据库已存在且账号有建表、写入和查询权限。</p></div>
          <label className="wide">HTTP(S) URL<input required type="url" value={config.clickhouse.url} onChange={e => patch('clickhouse', 'url', e.target.value)} placeholder="http://127.0.0.1:8123"/></label>
          <label>Database<input required value={config.clickhouse.database} onChange={e => patch('clickhouse', 'database', e.target.value)}/></label><label>Table<input required value={config.clickhouse.table} onChange={e => patch('clickhouse', 'table', e.target.value)}/></label>
          <label>用户名<input value={config.clickhouse.username} onChange={e => patch('clickhouse', 'username', e.target.value)}/></label><label>密码<input type="password" autoComplete="new-password" placeholder={passwordSaved ? '已保存；留空保持不变' : '可选'} value={config.clickhouse.password} onChange={e => patch('clickhouse', 'password', e.target.value)}/></label>
        </>}
        {config.adapter === 's3' && <>
          <div className="log-settings-section"><h3>S3 / 兼容对象存储</h3><p>每条访问日志压缩为独立 JSONL gzip 对象；为大规模搜索设置时间范围。</p></div>
          <label className="wide">Endpoint（留空使用 AWS S3）<input type="url" value={config.s3.endpoint} onChange={e => patch('s3', 'endpoint', e.target.value)} placeholder="https://s3.example.com"/></label>
          <label>Region<input required value={config.s3.region} onChange={e => patch('s3', 'region', e.target.value)}/></label><label>Bucket<input required value={config.s3.bucket} onChange={e => patch('s3', 'bucket', e.target.value)}/></label>
          <label className="wide">对象前缀<input value={config.s3.prefix} onChange={e => patch('s3', 'prefix', e.target.value)} placeholder="rpop/access"/></label>
          <label>Access key ID<input required={!s3CredentialsSaved} autoComplete="off" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.accessKeyId} onChange={e => patch('s3', 'accessKeyId', e.target.value)}/></label>
          <label>Secret access key<input required={!s3CredentialsSaved} type="password" autoComplete="new-password" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.secretAccessKey} onChange={e => patch('s3', 'secretAccessKey', e.target.value)}/></label>
          <label>Session token（可选）<input type="password" autoComplete="new-password" placeholder={s3CredentialsSaved ? '已保存；留空保持不变' : ''} value={config.s3.sessionToken} onChange={e => patch('s3', 'sessionToken', e.target.value)}/></label>
          <label className="log-check"><input type="checkbox" checked={config.s3.forcePathStyle} onChange={e => patch('s3', 'forcePathStyle', e.target.checked)}/> 使用 Path-style URL（MinIO 常用）</label>
        </>}
        <div className="log-settings-warning">存储凭证保存在 SQLite 中且未加密。请保护数据库和备份文件；更新时密钥留空会保留已有值。</div>
        <div className="log-editor-actions"><button type="button" className="secondary" onClick={closeEditor}>取消</button><button className="primary" disabled={saving}>{saving ? '保存中…' : '保存适配器'}</button></div>
      </form>
    </div>}
  </section>
}
