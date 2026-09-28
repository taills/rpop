import { useEffect, useState } from 'react'
import TimezoneSelect from './TimezoneSelect.jsx'
import KeyedCertificateManager, { CLIENT_CERTIFICATE_KIND, SERVER_CERTIFICATE_KIND } from './KeyedCertificateManager.jsx'
import { UiPageHeader } from '@/components/ui'
import { useBusyAction } from '../busyAction.js'

function cleanRootCertificate(certificate) {
  return {
    id: certificate.id,
    name: (certificate.name || '').trim(),
    pem: (certificate.pem || '').trim(),
  }
}

export default function SystemSettings({ api, onChange }) {
  const [timeZone, setTimeZone] = useState('UTC')
  const [savedTimeZone, setSavedTimeZone] = useState('UTC')
  const [rootCertificates, setRootCertificates] = useState([])
  const [clientCertificates, setClientCertificates] = useState([])
  const [serverCertificates, setServerCertificates] = useState([])
  const [loading, setLoading] = useState(true)
  const [settingsLoaded, setSettingsLoaded] = useState(false)
  const [activeTab, setActiveTab] = useState('general')
  const [settingsError, setSettingsError] = useState('')
  const [settingsMessage, setSettingsMessage] = useState('')
  const [certificateError, setCertificateError] = useState('')
  const [certificateMessage, setCertificateMessage] = useState('')
  const [editingCertificateID, setEditingCertificateID] = useState(null)
  const [certificateDraft, setCertificateDraft] = useState(null)
  const [confirmDeleteID, setConfirmDeleteID] = useState(null)
  const [certificateQuery, setCertificateQuery] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [passwordError, setPasswordError] = useState('')
  const [passwordMessage, setPasswordMessage] = useState('')
  // This page's forms (timezone, node deployment defaults, admin password, CA root certificates, and — via
  // props — the server/client KeyedCertificateManager tabs) all PUT the same /api/settings resource except the
  // password change, and each one that does resends whatever it is not itself changing from its last-saved
  // snapshot (see nodeSettingsFields() below) — so two of them actually in flight at once could race and have
  // the slower one's stale snapshot clobber the faster one's just-saved field, even though the server
  // serializes the PUTs themselves: the client-side body is built from React state captured before either
  // request resolved, so serialising on the server does not stop the second body from carrying stale values.
  // One shared useBusyAction lock (rather than one boolean per form) makes that impossible instead of just
  // discouraging it, at the minor cost of not letting an operator save the timezone and change their password
  // in the same instant. KeyedCertificateManager no longer owns its own lock — it runs its save through this
  // same `runSettingsAction`, via the `busy`/`run` props below, so its PUTs are serialized with all the others
  // too instead of only with itself.
  const { busy: settingsBusy, run: runSettingsAction } = useBusyAction()
  const savingTimeZone = settingsBusy === 'timezone'
  const savingCertificate = settingsBusy === 'certificate'
  const savingNodeSettings = settingsBusy === 'nodeSettings'
  const savingPassword = settingsBusy === 'password'
  // The general tab renders the timezone/node-settings/password forms together (and the CA tab's buttons are
  // reachable by switching tabs mid-save), so a click on any of these forms' buttons while ANOTHER one is
  // mid-PUT must be visibly blocked too, not just silently dropped by runSettingsAction's lock — hence
  // disabling every one of them (including the tab buttons themselves, see the tablist below) on
  // `anySettingsBusy`, while each button's own "保存中…" text/aria-busy still only lights up for its own
  // matching key.
  const anySettingsBusy = Boolean(settingsBusy)
  // nodeControllerUrl/nodeImage feed the node onboarding guide (see NodeBootstrapGuide.jsx); saved* mirrors
  // savedTimeZone's role below: every PUT to /api/settings must resend the CURRENTLY SAVED value of every
  // plain-string field it is not itself changing (see the nodeSettingsFields() calls throughout this file), or
  // that field is silently cleared — unlike clientCertificates/serverCertificates, a plain string has no
  // "omitted means keep the current value" treatment on the server (see mergeKeyedCertificateKeys's doc
  // comment), so rootCertificates already gets the same treatment further down.
  const [nodeControllerUrl, setNodeControllerUrl] = useState('')
  const [savedNodeControllerUrl, setSavedNodeControllerUrl] = useState('')
  const [nodeImage, setNodeImage] = useState('')
  const [savedNodeImage, setSavedNodeImage] = useState('')
  const [nodeSettingsError, setNodeSettingsError] = useState('')
  const [nodeSettingsMessage, setNodeSettingsMessage] = useState('')

  useEffect(() => {
    api('/settings')
      .then(settings => {
        const zone = settings.timeZone || 'UTC'
        setTimeZone(zone)
        setSavedTimeZone(zone)
        applyNodeSettings(settings)
        applyCertificateLists(settings)
        setSettingsLoaded(true)
      })
      .catch(err => setSettingsError(err.message))
      .finally(() => setLoading(false))
  }, [api])

  function applyNodeSettings(settings) {
    setNodeControllerUrl(settings.nodeControllerUrl || '')
    setSavedNodeControllerUrl(settings.nodeControllerUrl || '')
    setNodeImage(settings.nodeImage || '')
    setSavedNodeImage(settings.nodeImage || '')
  }

  // nodeSettingsFields() is spread into every /api/settings PUT body in this file (see the doc comment on
  // nodeControllerUrl's useState above), always carrying the last-saved value so an unrelated save (timezone, a
  // certificate) never clears these two fields.
  function nodeSettingsFields() {
    return { nodeControllerUrl: savedNodeControllerUrl, nodeImage: savedNodeImage }
  }

  function applyCertificateLists(settings) {
    setRootCertificates(Array.isArray(settings.rootCertificates) ? settings.rootCertificates : [])
    setClientCertificates(Array.isArray(settings.clientCertificates) ? settings.clientCertificates : [])
    setServerCertificates(Array.isArray(settings.serverCertificates) ? settings.serverCertificates : [])
  }

  // clientCertificates/serverCertificates are omitted unless they change: the server keeps the stored lists and their write-only private keys.
  async function saveTimeZone(event) {
    event.preventDefault()
    await runSettingsAction('timezone', async () => {
      setSettingsError('')
      setSettingsMessage('')
      try {
        const settings = await api('/settings', {
          method: 'PUT',
          body: JSON.stringify({ timeZone, rootCertificates: rootCertificates.map(cleanRootCertificate), ...nodeSettingsFields() }),
        })
        const zone = settings.timeZone || 'UTC'
        setTimeZone(zone)
        setSavedTimeZone(zone)
        applyNodeSettings(settings)
        applyCertificateLists(settings)
        setSettingsMessage('系统时区已保存。')
        onChange?.()
      } catch (err) {
        setSettingsError(err.message)
      }
    })
  }

  async function persistRootCertificates(nextCertificates, successMessage) {
    await runSettingsAction('certificate', async () => {
      setCertificateError('')
      setCertificateMessage('')
      try {
        const settings = await api('/settings', {
          method: 'PUT',
          body: JSON.stringify({
            timeZone: savedTimeZone,
            rootCertificates: nextCertificates.map(cleanRootCertificate),
            ...nodeSettingsFields(),
          }),
        })
        applyNodeSettings(settings)
        applyCertificateLists(settings)
        setSavedTimeZone(settings.timeZone || 'UTC')
        setSettingsLoaded(true)
        setCertificateDraft(null)
        setEditingCertificateID(null)
        setConfirmDeleteID(null)
        setCertificateMessage(successMessage)
        onChange?.()
      } catch (err) {
        setCertificateError(err.message)
      }
    })
  }

  async function persistKeyedCertificates(field, nextCertificates) {
    const settings = await api('/settings', {
      method: 'PUT',
      body: JSON.stringify({
        timeZone: savedTimeZone,
        rootCertificates: rootCertificates.map(cleanRootCertificate),
        ...nodeSettingsFields(),
        [field]: nextCertificates,
      }),
    })
    applyNodeSettings(settings)
    applyCertificateLists(settings)
    setSavedTimeZone(settings.timeZone || 'UTC')
    onChange?.()
  }

  async function saveNodeSettings(event) {
    event.preventDefault()
    await runSettingsAction('nodeSettings', async () => {
      setNodeSettingsError('')
      setNodeSettingsMessage('')
      try {
        const settings = await api('/settings', {
          method: 'PUT',
          body: JSON.stringify({
            timeZone: savedTimeZone,
            rootCertificates: rootCertificates.map(cleanRootCertificate),
            nodeControllerUrl: nodeControllerUrl.trim(),
            nodeImage: nodeImage.trim(),
          }),
        })
        applyNodeSettings(settings)
        applyCertificateLists(settings)
        setSavedTimeZone(settings.timeZone || 'UTC')
        setNodeSettingsMessage('节点部署默认值已保存。')
        onChange?.()
      } catch (err) {
        setNodeSettingsError(err.message)
      }
    })
  }

  async function saveRootCertificate(event) {
    event.preventDefault()
    if (!certificateDraft?.name.trim() || !certificateDraft?.pem.trim()) {
      setCertificateError('请填写证书名称并粘贴 CA 根证书 PEM。')
      return
    }
    let candidate
    let successMessage
    if (editingCertificateID) {
      candidate = rootCertificates.map(certificate => certificate.id === editingCertificateID ? { ...certificate, ...certificateDraft } : certificate)
      successMessage = 'CA 根证书已更新。'
    } else {
      candidate = [...rootCertificates, certificateDraft]
      successMessage = 'CA 根证书已添加。'
    }
    await persistRootCertificates(candidate, successMessage)
  }

  async function deleteRootCertificate(certificate) {
    const candidate = rootCertificates.filter(item => item.id !== certificate.id)
    await persistRootCertificates(candidate, 'CA 根证书已删除。')
  }

  function openCertificateEditor(certificate = null) {
    setCertificateError('')
    setCertificateMessage('')
    setConfirmDeleteID(null)
    setEditingCertificateID(certificate?.id || null)
    setCertificateDraft(certificate ? { name: certificate.name, pem: certificate.pem } : { name: '', pem: '' })
  }

  function cancelCertificateEditor() {
    setCertificateDraft(null)
    setEditingCertificateID(null)
    setCertificateError('')
  }

  async function changePassword(event) {
    event.preventDefault()
    await runSettingsAction('password', async () => {
      setPasswordError('')
      setPasswordMessage('')
      try {
        await api('/auth/password', {
          method: 'PUT',
          body: JSON.stringify({ currentPassword, newPassword }),
        })
        setCurrentPassword('')
        setNewPassword('')
        setPasswordMessage('管理密码已更新，其他登录会话已失效。')
      } catch (err) {
        setPasswordError(err.message)
      }
    })
  }

  const canManageSettings = settingsLoaded && !loading
  const normalizedCertificateQuery = certificateQuery.trim().toLowerCase()
  const visibleRootCertificates = rootCertificates.filter(certificate => !normalizedCertificateQuery || `${certificate.name} ${certificate.id}`.toLowerCase().includes(normalizedCertificateQuery))

  return <div className="ui-page system-settings-page">
    <UiPageHeader
      eyebrow="SYSTEM"
      title="系统设置"
      sub="管理系统时区、站点 HTTPS 证书、上游 HTTPS CA 根证书、mTLS Client 证书和管理员密码。"
    />

    {/* Tab buttons stay disabled while any settings form is saving: switching tabs mid-save would unmount
        KeyedCertificateManager (for the server/client tabs) while its own persist() is still in flight, losing
        the success/error message and racing setState on an unmounted component. Blocking the switch is simpler
        and safer than trying to keep an unmounted form's result visible. */}
    <div className="system-settings-tabs" role="tablist" aria-label="系统设置分类">
      <button type="button" role="tab" id="system-settings-tab-general" aria-selected={activeTab === 'general'} aria-controls="system-settings-panel-general" disabled={anySettingsBusy} onClick={() => setActiveTab('general')}>常规设置</button>
      <button type="button" role="tab" id="system-settings-tab-server" aria-selected={activeTab === 'server'} aria-controls="system-settings-panel-server" disabled={anySettingsBusy} onClick={() => setActiveTab('server')}>站点 HTTPS 证书 <span className="tab-count">{serverCertificates.length}</span></button>
      <button type="button" role="tab" id="system-settings-tab-ca" aria-selected={activeTab === 'ca'} aria-controls="system-settings-panel-ca" disabled={anySettingsBusy} onClick={() => setActiveTab('ca')}>上游 CA 根证书 <span className="tab-count">{rootCertificates.length}</span></button>
      <button type="button" role="tab" id="system-settings-tab-client" aria-selected={activeTab === 'client'} aria-controls="system-settings-panel-client" disabled={anySettingsBusy} onClick={() => setActiveTab('client')}>mTLS Client 证书 <span className="tab-count">{clientCertificates.length}</span></button>
    </div>

    {activeTab === 'general' && <div id="system-settings-panel-general" role="tabpanel" aria-labelledby="system-settings-tab-general" className="system-settings-tabpanel">
      <section className="settings-panel">
        <h3>系统时区</h3>
        <p>日志分区与本地文件轮转使用此时区；日志时间戳和搜索时间范围仍表示绝对时间。</p>
        {settingsError && <div className="error settings-message">{settingsError}</div>}
        {!settingsLoaded && !loading && <p className="settings-load-error">系统设置未能加载，暂不能修改。</p>}
        {loading && <p>正在读取系统设置…</p>}
        {settingsLoaded && <form className="settings-form timezone-settings-form" onSubmit={saveTimeZone}>
          <label className="wide">选择系统时区
            <TimezoneSelect value={timeZone} onChange={setTimeZone} disabled={!canManageSettings || anySettingsBusy}/>
            <span>通过搜索过滤 IANA 时区并从列表中选择；不能输入列表以外的值。默认 UTC。</span>
          </label>
          {settingsMessage && <div className="log-success settings-message wide">{settingsMessage}</div>}
          <button className="primary" disabled={!canManageSettings || anySettingsBusy || timeZone === savedTimeZone} aria-busy={savingTimeZone || undefined}>{savingTimeZone ? '保存中…' : '保存时区'}</button>
        </form>}
      </section>

      <section className="settings-panel">
        <h3>节点部署默认值</h3>
        <p>供“新建节点”弹窗里的节点接入向导使用，两者都可留空。</p>
        {nodeSettingsError && <div className="error settings-message">{nodeSettingsError}</div>}
        {settingsLoaded && <form className="settings-form" onSubmit={saveNodeSettings}>
          <label className="wide">控制器地址
            <input value={nodeControllerUrl} onChange={event => setNodeControllerUrl(event.target.value)} disabled={!canManageSettings || anySettingsBusy} placeholder="https://controller.example.com:7443"/>
            <span>节点接入向导里“控制器地址”的默认值，形如 https://host[:port]，不能带路径或查询参数。留空则由向导根据浏览器地址和 southbound 端口自动推导。</span>
          </label>
          <label className="wide">节点镜像
            <input value={nodeImage} onChange={event => setNodeImage(event.target.value)} disabled={!canManageSettings || anySettingsBusy} placeholder="registry.example.com/rpop:1.4.0"/>
            <span>节点接入向导里 docker run / docker-compose recipe 使用的镜像名，不能包含空白字符。留空则默认为 rpop:&lt;控制器版本&gt;。</span>
          </label>
          {nodeSettingsMessage && <div className="log-success settings-message wide">{nodeSettingsMessage}</div>}
          <button className="primary" disabled={!canManageSettings || anySettingsBusy || (nodeControllerUrl === savedNodeControllerUrl && nodeImage === savedNodeImage)} aria-busy={savingNodeSettings || undefined}>{savingNodeSettings ? '保存中…' : '保存节点部署默认值'}</button>
        </form>}
      </section>

      <section className="settings-panel">
        <div className="eyebrow">SECURITY</div>
        <h3>管理密码</h3>
        <p>密码至少 12 个字符。更新后其他浏览器会话将立即失效。</p>
        {passwordError && <div className="error settings-message">{passwordError}</div>}
        {passwordMessage && <div className="log-success settings-message">{passwordMessage}</div>}
        <form className="settings-form" onSubmit={changePassword}>
          <label>当前密码<input type="password" required autoComplete="current-password" value={currentPassword} disabled={anySettingsBusy} onChange={event => setCurrentPassword(event.target.value)}/></label>
          <label>新密码<input type="password" required minLength="12" autoComplete="new-password" value={newPassword} disabled={anySettingsBusy} onChange={event => setNewPassword(event.target.value)}/></label>
          <button className="primary" disabled={anySettingsBusy} aria-busy={savingPassword || undefined}>{savingPassword ? '更新中…' : '更新管理密码'}</button>
        </form>
      </section>
    </div>}

    {activeTab === 'ca' && <section id="system-settings-panel-ca" role="tabpanel" aria-labelledby="system-settings-tab-ca" className="settings-panel ca-management-panel">
      <div className="ca-management-heading">
        <div><h3>上游 HTTPS CA 根证书</h3><p>集中管理多个根证书；添加后可在站点上游配置中按站点选择。所选证书会与系统默认根证书池及该站点已有 CA Bundle 一起用于校验。</p></div>
        <button type="button" className="secondary" disabled={!canManageSettings || anySettingsBusy || rootCertificates.length >= 64} onClick={() => openCertificateEditor()}>＋ 添加根证书</button>
      </div>
      {certificateError && <div className="error settings-message">{certificateError}</div>}
      {certificateMessage && <div className="log-success settings-message">{certificateMessage}</div>}
      {loading && <p>正在读取 CA 根证书…</p>}
      {!loading && !settingsLoaded && <p className="settings-load-error">系统设置未能加载，暂不能管理根证书。</p>}
      {settingsLoaded && rootCertificates.length === 0 && !certificateDraft && <div className="ca-empty-state"><strong>尚未添加系统级 CA 根证书</strong><span>添加证书后，可在站点的 HTTPS 上游配置中选择一个或多个信任根。</span></div>}
      {rootCertificates.length > 0 && <label className="ca-search">搜索根证书<input type="search" value={certificateQuery} onChange={event => setCertificateQuery(event.target.value)} placeholder="按证书名称或 SHA-256 指纹搜索" autoComplete="off"/></label>}
      {certificateDraft && <form className="ca-editor" onSubmit={saveRootCertificate}>
        <div className="ca-editor-heading"><h4>{editingCertificateID ? '编辑 CA 根证书' : '添加 CA 根证书'}</h4><button type="button" className="icon-button" aria-label="关闭证书编辑器" disabled={anySettingsBusy} onClick={cancelCertificateEditor}>×</button></div>
        <label>证书名称<input required maxLength="128" value={certificateDraft.name} onChange={event => setCertificateDraft(current => ({ ...current, name: event.target.value }))} placeholder="例如：公司内部根 CA"/></label>
        <label>CA 根证书 PEM<textarea required rows="8" value={certificateDraft.pem} onChange={event => setCertificateDraft(current => ({ ...current, pem: event.target.value }))} placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'} spellCheck="false" autoCapitalize="off" autoComplete="off"/></label>
        <p className="ca-editor-note">只接受单张有效的 X.509 CA 证书。证书被站点引用时不能删除或替换其内容；请先在相关站点取消选择。</p>
        <div className="ca-editor-actions"><button type="button" className="secondary" onClick={cancelCertificateEditor} disabled={anySettingsBusy}>取消</button><button className="primary" disabled={anySettingsBusy} aria-busy={savingCertificate || undefined}>{savingCertificate ? '保存中…' : '保存证书'}</button></div>
      </form>}
      {settingsLoaded && rootCertificates.length > 0 && visibleRootCertificates.length === 0 && <div className="ca-empty-state">没有匹配的根证书。</div>}
      {settingsLoaded && visibleRootCertificates.length > 0 && <div className="ca-record-list">
        {visibleRootCertificates.map(certificate => <article className="ca-record" key={certificate.id}>
          <div className="ca-record-main"><div><h4>{certificate.name}</h4><code>SHA-256 · {certificate.id}</code></div><div className="ca-record-actions">
            {confirmDeleteID === certificate.id ? (
              <><span className="ca-delete-prompt">确认删除？被站点引用时会被拒绝。</span><button type="button" className="danger" disabled={anySettingsBusy} onClick={() => deleteRootCertificate(certificate)}>确认删除</button><button type="button" className="secondary" onClick={() => setConfirmDeleteID(null)}>取消</button></>
            ) : (
              <><button type="button" className="secondary" disabled={anySettingsBusy || certificateDraft !== null} onClick={() => openCertificateEditor(certificate)}>编辑</button><button type="button" className="secondary" disabled={anySettingsBusy || certificateDraft !== null} onClick={() => { setCertificateError(''); setCertificateMessage(''); setConfirmDeleteID(certificate.id) }}>删除</button></>
            )}
          </div></div>
          <details className="ca-record-details"><summary>查看证书 PEM</summary><pre>{certificate.pem}</pre></details>
        </article>)}
      </div>}
      <p className="ca-management-note">最多 64 张证书。根证书仅在站点明确选择后生效；移除已被站点引用的证书前，需先修改对应站点配置。</p>
    </section>}

    {activeTab === 'server' && <KeyedCertificateManager kind={SERVER_CERTIFICATE_KIND} certificates={serverCertificates} loaded={settingsLoaded} loading={loading} persist={next => persistKeyedCertificates('serverCertificates', next)} busy={settingsBusy} run={runSettingsAction}/>}
    {activeTab === 'client' && <KeyedCertificateManager kind={CLIENT_CERTIFICATE_KIND} certificates={clientCertificates} loaded={settingsLoaded} loading={loading} persist={next => persistKeyedCertificates('clientCertificates', next)} busy={settingsBusy} run={runSettingsAction}/>}
  </div>
}
