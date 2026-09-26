import { useEffect, useState } from 'react'

export default function SystemSettings({ api, onChange }) {
  const [timeZone, setTimeZone] = useState('UTC')
  const [rootCertificates, setRootCertificates] = useState([])
  const [loading, setLoading] = useState(true)
  const [savingSettings, setSavingSettings] = useState(false)
  const [settingsError, setSettingsError] = useState('')
  const [settingsMessage, setSettingsMessage] = useState('')
  const [currentPassword, setCurrentPassword] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [savingPassword, setSavingPassword] = useState(false)
  const [passwordError, setPasswordError] = useState('')
  const [passwordMessage, setPasswordMessage] = useState('')

  useEffect(() => {
    api('/settings')
      .then(settings => {
        setTimeZone(settings.timeZone || 'UTC')
        setRootCertificates(Array.isArray(settings.rootCertificates) ? settings.rootCertificates : [])
      })
      .catch(err => setSettingsError(err.message))
      .finally(() => setLoading(false))
  }, [api])

  async function saveSettings(event) {
    event.preventDefault()
    setSavingSettings(true)
    setSettingsError('')
    setSettingsMessage('')
    try {
      const settings = await api('/settings', {
        method: 'PUT',
        body: JSON.stringify({
          timeZone,
          rootCertificates: rootCertificates.map(certificate => ({
            id: certificate.id,
            name: certificate.name.trim(),
            pem: certificate.pem.trim(),
          })).filter(certificate => certificate.pem),
        }),
      })
      setTimeZone(settings.timeZone || 'UTC')
      setRootCertificates(Array.isArray(settings.rootCertificates) ? settings.rootCertificates : [])
      setSettingsMessage('系统设置已保存。上游根证书在站点下次启动或重载时生效。')
      onChange?.()
    } catch (err) {
      setSettingsError(err.message)
    } finally {
      setSavingSettings(false)
    }
  }

  async function changePassword(event) {
    event.preventDefault()
    setSavingPassword(true)
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
    } finally {
      setSavingPassword(false)
    }
  }

  function updateRootCertificate(index, field, value) {
    setRootCertificates(current => current.map((certificate, item) => item === index ? { ...certificate, [field]: value } : certificate))
  }

  function removeRootCertificate(index) {
    setRootCertificates(current => current.filter((_, item) => item !== index))
  }

  return <div className="system-settings-page">
    <section className="settings-panel">
      <div className="eyebrow">SYSTEM</div>
      <h2>系统设置</h2>
      <p>统一管理系统时区和可供上游 HTTPS 站点选择的 CA 根证书。</p>
      {settingsError && <div className="error settings-message">{settingsError}</div>}
      {settingsMessage && <div className="log-success settings-message">{settingsMessage}</div>}
      {loading && <p>正在读取系统设置…</p>}
      {!loading && <form className="settings-form" onSubmit={saveSettings}>
        <label className="wide">系统时区
          <input required value={timeZone} onChange={event => setTimeZone(event.target.value)} placeholder="UTC 或 Asia/Shanghai" autoComplete="off" spellCheck="false"/>
          <span>默认为 UTC。填写 IANA 时区名，例如 Asia/Shanghai 或 America/Los_Angeles。日志时间戳和搜索时间范围仍为绝对时间。</span>
        </label>
        <div className="wide ca-settings">
          <div className="ca-settings-heading">
            <div><strong>上游 HTTPS CA 根证书</strong><span>为每张证书设置名称并粘贴一个 PEM 证书；随后可在站点上游配置中按站点选择。</span></div>
            <button type="button" className="secondary" onClick={() => setRootCertificates(current => [...current, { name: '', pem: '' }])}>＋ 添加根证书</button>
          </div>
          {rootCertificates.length === 0 && <p className="ca-empty">尚未添加系统级 CA 根证书。</p>}
          <div className="ca-certificate-list">
            {rootCertificates.map((certificate, index) => <div className="ca-certificate" key={certificate.id || `new-${index}`}>
              <label>证书名称
                <input required value={certificate.name || ''} onChange={event => updateRootCertificate(index, 'name', event.target.value)} placeholder="例如：公司内部根 CA"/>
              </label>
              <label className="wide">CA 根证书 PEM
                <textarea required rows="7" value={certificate.pem || ''} onChange={event => updateRootCertificate(index, 'pem', event.target.value)} placeholder={'-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----'} spellCheck="false" autoCapitalize="off" autoComplete="off"/>
              </label>
              <button type="button" className="secondary ca-remove" onClick={() => removeRootCertificate(index)}>移除</button>
            </div>)}
          </div>
          <p className="ca-help">保存后不会自动信任所有自定义 CA；请在需要的站点上游中选择证书。被站点引用的证书不能直接移除。所选自定义 CA 会加入系统默认根证书池，并与该站点现有 CA Bundle 一起用于校验。</p>
        </div>
        <button className="primary" disabled={savingSettings}>{savingSettings ? '保存中…' : '保存系统设置'}</button>
      </form>}
    </section>

    <section className="settings-panel">
      <div className="eyebrow">SECURITY</div>
      <h2>管理密码</h2>
      <p>密码至少 12 个字符。更新后其他浏览器会话将立即失效。</p>
      {passwordError && <div className="error settings-message">{passwordError}</div>}
      {passwordMessage && <div className="log-success settings-message">{passwordMessage}</div>}
      <form className="settings-form" onSubmit={changePassword}>
        <label>当前密码<input type="password" required autoComplete="current-password" value={currentPassword} onChange={event => setCurrentPassword(event.target.value)}/></label>
        <label>新密码<input type="password" required minLength="12" autoComplete="new-password" value={newPassword} onChange={event => setNewPassword(event.target.value)}/></label>
        <button className="primary" disabled={savingPassword}>{savingPassword ? '更新中…' : '更新管理密码'}</button>
      </form>
    </section>
  </div>
}
