import { useState } from 'react'

const MAX_KEYED_CERTIFICATES = 64

export const CLIENT_CERTIFICATE_KIND = {
  key: 'client',
  title: '上游 mTLS Client 证书',
  noun: 'Client 证书',
  description: '集中管理访问需要双向 HTTPS 认证（mTLS）的上游时出示的 Client 证书与私钥；添加后可在站点上游配置中按站点选择。私钥只写入服务端，不会通过接口回显。',
  emptyText: '当上游要求客户端证书认证时，添加证书与私钥后即可在站点中选择。',
  namePlaceholder: '例如：合作方 API mTLS',
  usageNote: '证书必须允许 TLS 客户端认证（Extended Key Usage 包含 clientAuth 或未限制）',
  replaceNote: '证书被站点引用时不能删除或替换为不同证书；请先在相关站点取消选择。',
  footer: '证书仅在站点明确选择后生效；与站点内单独上传的 Client 证书二选一。',
}

export const SERVER_CERTIFICATE_KIND = {
  key: 'server',
  title: '站点 HTTPS 证书',
  noun: 'HTTPS 证书',
  description: '预先添加站点 HTTPS 使用的证书与私钥（如 *.example.com 泛域名证书），多个站点可共用同一张证书。私钥只写入服务端，不会通过接口回显。',
  emptyText: '添加证书后，可在站点的“启用站点 HTTPS”中直接选择，无需为每个站点重复上传。',
  namePlaceholder: '例如：*.example.com 泛域名证书',
  usageNote: '证书必须允许 TLS 服务端认证（Extended Key Usage 包含 serverAuth 或未限制），建议附带完整中间证书链',
  replaceNote: '续期时可直接编辑并替换证书内容，证书 ID 保持不变，所有引用它的运行中站点会立即使用新证书；被站点引用时不能删除。',
  footer: '证书仅在站点明确选择后生效；与站点内单独上传的 HTTPS 证书二选一。',
}

export function cleanKeyedCertificate(certificate) {
  return {
    id: certificate.id,
    name: (certificate.name || '').trim(),
    certificatePem: (certificate.certificatePem || '').trim(),
  }
}

function formatDate(value) {
  if (!value) return '未知'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

export function certificateValidity(certificate) {
  const now = Date.now()
  const notBefore = Date.parse(certificate.notBefore)
  const notAfter = Date.parse(certificate.notAfter)
  if (!Number.isNaN(notAfter) && notAfter < now) return { label: '已过期', className: 'expired' }
  if (!Number.isNaN(notBefore) && notBefore > now) return { label: '尚未生效', className: 'expired' }
  if (!Number.isNaN(notAfter) && notAfter - now < 30 * 24 * 3600 * 1000) return { label: '30 天内过期', className: 'expiring' }
  return { label: '有效', className: 'valid' }
}

async function readPEMFile(event, apply) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (file) apply(await file.text())
}

export default function KeyedCertificateManager({ kind, certificates, loaded, loading, persist }) {
  const [draft, setDraft] = useState(null)
  const [editingID, setEditingID] = useState(null)
  const [confirmDeleteID, setConfirmDeleteID] = useState(null)
  const [query, setQuery] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [message, setMessage] = useState('')

  async function save(nextCertificates, successMessage) {
    setSaving(true)
    setError('')
    setMessage('')
    try {
      await persist(nextCertificates)
      setDraft(null)
      setEditingID(null)
      setConfirmDeleteID(null)
      setMessage(successMessage)
    } catch (err) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  async function submitDraft(event) {
    event.preventDefault()
    if (!draft.name.trim() || !draft.certificatePem.trim()) {
      setError(`请填写证书名称并提供 ${kind.noun} PEM。`)
      return
    }
    if (!editingID && !draft.privateKeyPem.trim()) {
      setError(`添加 ${kind.noun}时必须同时提供私钥 PEM。`)
      return
    }
    const privateKeyPem = draft.privateKeyPem.trim()
    const entry = { ...cleanKeyedCertificate({ id: editingID || undefined, name: draft.name, certificatePem: draft.certificatePem }), ...(privateKeyPem ? { privateKeyPem } : {}) }
    const others = certificates.map(cleanKeyedCertificate)
    const next = editingID ? others.map(item => item.id === editingID ? entry : item) : [...others, entry]
    await save(next, editingID ? `${kind.noun}已更新。` : `${kind.noun}已添加。`)
  }

  async function remove(certificate) {
    const next = certificates.filter(item => item.id !== certificate.id).map(cleanKeyedCertificate)
    await save(next, `${kind.noun}已删除。`)
  }

  function openEditor(certificate = null) {
    setError('')
    setMessage('')
    setConfirmDeleteID(null)
    setEditingID(certificate?.id || null)
    setDraft({ name: certificate?.name || '', certificatePem: certificate?.certificatePem || '', privateKeyPem: '' })
  }

  function closeEditor() {
    setDraft(null)
    setEditingID(null)
    setError('')
  }

  const canManage = loaded && !loading
  const normalizedQuery = query.trim().toLowerCase()
  const visible = certificates.filter(certificate => !normalizedQuery || `${certificate.name} ${certificate.id} ${certificate.fingerprint || ''} ${certificate.subject || ''} ${(certificate.dnsNames || []).join(' ')}`.toLowerCase().includes(normalizedQuery))
  const panelID = `system-settings-panel-${kind.key}`

  return <section id={panelID} role="tabpanel" aria-labelledby={`system-settings-tab-${kind.key}`} className="settings-panel ca-management-panel">
    <div className="ca-management-heading">
      <div><h3>{kind.title}</h3><p>{kind.description}</p></div>
      <button type="button" className="secondary" disabled={!canManage || saving || certificates.length >= MAX_KEYED_CERTIFICATES} onClick={() => openEditor()}>＋ 添加 {kind.noun}</button>
    </div>
    {error && <div className="error settings-message">{error}</div>}
    {message && <div className="log-success settings-message">{message}</div>}
    {loading && <p>正在读取 {kind.noun}…</p>}
    {!loading && !loaded && <p className="settings-load-error">系统设置未能加载，暂不能管理 {kind.noun}。</p>}
    {loaded && certificates.length === 0 && !draft && <div className="ca-empty-state"><strong>尚未添加 {kind.noun}</strong><span>{kind.emptyText}</span></div>}
    {certificates.length > 0 && <label className="ca-search">搜索 {kind.noun}<input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder="按名称、域名、主题或指纹搜索" autoComplete="off"/></label>}
    {draft && <form className="ca-editor" onSubmit={submitDraft}>
      <div className="ca-editor-heading"><h4>{editingID ? `编辑 ${kind.noun}` : `添加 ${kind.noun}`}</h4><button type="button" className="icon-button" aria-label="关闭证书编辑器" onClick={closeEditor}>×</button></div>
      <label>证书名称<input required maxLength="128" value={draft.name} onChange={event => setDraft(current => ({ ...current, name: event.target.value }))} placeholder={kind.namePlaceholder}/></label>
      <label>证书 PEM（可附带中间证书）
        <textarea required rows="8" value={draft.certificatePem} onChange={event => setDraft(current => ({ ...current, certificatePem: event.target.value }))} placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'} spellCheck="false" autoCapitalize="off" autoComplete="off"/>
        <span className="pem-file-picker">或从文件读取<input type="file" accept=".pem,.crt,.cer" onChange={event => readPEMFile(event, text => setDraft(current => ({ ...current, certificatePem: text })))}/></span>
      </label>
      <label>私钥 PEM{editingID ? '（留空则保留已保存的私钥）' : ''}
        <textarea required={!editingID} rows="6" value={draft.privateKeyPem} onChange={event => setDraft(current => ({ ...current, privateKeyPem: event.target.value }))} placeholder={editingID ? '留空保留当前私钥；更换为不同密钥的证书时需重新提供' : '-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----'} spellCheck="false" autoCapitalize="off" autoComplete="off"/>
        <span className="pem-file-picker">或从文件读取<input type="file" accept=".pem,.key" onChange={event => readPEMFile(event, text => setDraft(current => ({ ...current, privateKeyPem: text })))}/></span>
      </label>
      <p className="ca-editor-note">{kind.usageNote}；私钥需为未加密的 PKCS#1 / PKCS#8 / EC PEM，并与证书匹配。{kind.replaceNote}</p>
      <div className="ca-editor-actions"><button type="button" className="secondary" onClick={closeEditor} disabled={saving}>取消</button><button className="primary" disabled={saving}>{saving ? '保存中…' : '保存证书'}</button></div>
    </form>}
    {loaded && certificates.length > 0 && visible.length === 0 && <div className="ca-empty-state">没有匹配的 {kind.noun}。</div>}
    {loaded && visible.length > 0 && <div className="ca-record-list">
      {visible.map(certificate => {
        const validity = certificateValidity(certificate)
        return <article className="ca-record" key={certificate.id}>
          <div className="ca-record-main"><div>
            <h4>{certificate.name} <span className={`cert-validity ${validity.className}`}>{validity.label}</span></h4>
            <div className="cert-meta">
              {certificate.dnsNames?.length > 0 && <span>域名 · {certificate.dnsNames.join(', ')}</span>}
              <span>主题 · {certificate.subject || '未知'}</span><span>签发者 · {certificate.issuer || '未知'}</span><span>有效期至 · {formatDate(certificate.notAfter)}</span><span>私钥 · {certificate.hasPrivateKey ? '已保存' : '缺失'}</span>
            </div>
            <code>SHA-256 · {certificate.fingerprint || certificate.id}</code>
          </div><div className="ca-record-actions">
            {confirmDeleteID === certificate.id ? (
              <><span className="ca-delete-prompt">确认删除？被站点引用时会被拒绝。</span><button type="button" className="danger" disabled={saving} onClick={() => remove(certificate)}>确认删除</button><button type="button" className="secondary" onClick={() => setConfirmDeleteID(null)}>取消</button></>
            ) : (
              <><button type="button" className="secondary" disabled={saving || draft !== null} onClick={() => openEditor(certificate)}>编辑</button><button type="button" className="secondary" disabled={saving || draft !== null} onClick={() => { setError(''); setMessage(''); setConfirmDeleteID(certificate.id) }}>删除</button></>
            )}
          </div></div>
          <details className="ca-record-details"><summary>查看证书 PEM</summary><pre>{certificate.certificatePem}</pre></details>
        </article>
      })}
    </div>}
    <p className="ca-management-note">最多 {MAX_KEYED_CERTIFICATES} 张 {kind.noun}。{kind.footer}</p>
  </section>
}
