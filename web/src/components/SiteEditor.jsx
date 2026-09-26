import { isExpired, isHTTPSURL, uncoveredHostnames } from '../siteForm.js'

function FormSection({ title, description, children }) {
  return <>
    <div className="form-section-title wide"><strong>{title}</strong>{description && <span>{description}</span>}</div>
    {children}
  </>
}

// OptionToggle shows its settings only while the checkbox is on.
function OptionToggle({ checked, onChange, label, hint, children }) {
  return <>
    <label className="check"><input type="checkbox" checked={checked} onChange={event => onChange(event.target.checked)}/> {label} {hint && <span>{hint}</span>}</label>
    {checked && children && <div className="wide toggle-panel form-grid">{children}</div>}
  </>
}

function certificateLabel(certificate, detail) {
  return `${certificate.name}${detail ? ` · ${detail}` : ''}${isExpired(certificate) ? ' · 已过期' : ''}`
}

function ServerCertificateFields({ site, serverCertificates, setConfig, setFile }) {
  const selected = serverCertificates.find(certificate => certificate.id === site.config.certificateId)
  const missing = selected ? uncoveredHostnames(site.config.hostnames, selected.dnsNames) : []
  let hint
  if (!serverCertificates.length) hint = '暂无系统 HTTPS 证书；可在系统设置的“站点 HTTPS 证书”中预先添加泛域名证书供多个站点共用。'
  else if (!selected) hint = site.config.certificateSecret ? '当前使用站点专属证书；不选择文件表示保留，上传新证书与私钥可替换。' : '选择系统证书可与其他站点共用同一张（泛域名）证书。'
  else if (!(site.config.hostnames || []).length) hint = '站点未配置域名，所有 SNI 都将使用此证书。'
  else hint = missing.length ? `证书未覆盖以下站点域名：${missing.join(', ')}` : '证书覆盖全部站点域名。'
  return <>
    <label className="wide">证书来源
      <select value={site.config.certificateId || ''} onChange={event => { const certificateId = event.target.value; if (certificateId) { setFile('certFile', null); setFile('keyFile', null) } setConfig({ certificateId }) }}>
        <option value="">上传站点专属证书</option>
        {serverCertificates.map(certificate => <option key={certificate.id} value={certificate.id}>{certificateLabel(certificate, certificate.dnsNames?.join(', '))}</option>)}
      </select>
      <span className={missing.length ? 'cert-coverage warn' : 'cert-coverage'}>{hint}</span>
    </label>
    {!site.config.certificateId && <>
      <label>站点 HTTPS 证书 PEM<input type="file" accept=".pem,.crt,.cer" onChange={event => setFile('certFile', event.target.files?.[0] || null)}/></label>
      <label>站点 HTTPS 私钥 PEM<input type="file" accept=".pem,.key" onChange={event => setFile('keyFile', event.target.files?.[0] || null)}/></label>
    </>}
  </>
}

function ClientCertificateFields({ upstream, clientCertificates, setUpstream, setFile }) {
  return <>
    <label className="wide">Client 证书来源
      <select value={upstream.clientCertificateId || ''} onChange={event => { const clientCertificateId = event.target.value; if (clientCertificateId) { setFile('upstreamCertFile', null); setFile('upstreamKeyFile', null) } setUpstream({ clientCertificateId }) }}>
        <option value="">上传站点专属证书</option>
        {clientCertificates.map(certificate => <option key={certificate.id} value={certificate.id}>{certificateLabel(certificate, certificate.subject)}</option>)}
      </select>
      <span>{upstream.clientCertificateId ? '使用系统设置中集中管理的 Client 证书。' : upstream.clientCertSecret ? '已配置站点专属证书；不选择文件表示保留，上传新证书与私钥可替换。' : clientCertificates.length ? '可选择系统 Client 证书，或上传站点专属证书与私钥。' : '暂无系统 Client 证书；可上传站点专属证书，或在系统设置的“mTLS Client 证书”中添加。'}</span>
    </label>
    {!upstream.clientCertificateId && <>
      <label>站点 Client 证书 PEM<input type="file" accept=".pem,.crt,.cer" onChange={event => setFile('upstreamCertFile', event.target.files?.[0] || null)}/></label>
      <label>站点 Client 私钥 PEM<input type="file" accept=".pem,.key" onChange={event => setFile('upstreamKeyFile', event.target.files?.[0] || null)}/></label>
    </>}
  </>
}

export default function SiteEditor({ site, isNew, sections, catalog, saving, error, onChange, onSectionsChange, setFile, onCancel, onSubmit }) {
  const { rootCertificates, clientCertificates, serverCertificates, logAdapters } = catalog
  const upstream = site.config.upstreams[0]
  const accessLog = site.config.accessLog || {}
  const httpsUpstream = isHTTPSURL(upstream.url)
  const setConfig = patch => onChange({ ...site, config: { ...site.config, ...patch } })
  const setUpstream = patch => setConfig({ upstreams: [{ ...upstream, ...patch }, ...site.config.upstreams.slice(1)] })
  const setAccessLog = patch => setConfig({ accessLog: { ...accessLog, ...patch } })
  const toggle = key => checked => onSectionsChange({ ...sections, [key]: checked })

  return <div className="overlay" onMouseDown={event => event.target === event.currentTarget && onCancel()}>
    <form className="modal" onSubmit={onSubmit}>
      <div className="modal-head"><div><div className="eyebrow">SITE CONFIGURATION</div><h2>{isNew ? '新建站点' : '编辑站点'}</h2></div><button type="button" className="close" onClick={onCancel}>×</button></div>
      {error && <div className="error modal-error" role="alert">{error}</div>}
      <div className="form-grid">
        <FormSection title="基本信息">
          <label>站点 ID<input required disabled={!isNew} value={site.id} onChange={event => onChange({ ...site, id: event.target.value })} placeholder="site-main"/></label>
          <label>显示名称<input required value={site.name} onChange={event => onChange({ ...site, name: event.target.value })} placeholder="生产站点"/></label>
          <label className="check"><input type="checkbox" checked={!!site.autoStart} onChange={event => onChange({ ...site, autoStart: event.target.checked })}/> 程序启动时自动启动该站点 <span>程序重启后自动拉起</span></label>
        </FormSection>

        <FormSection title="监听" description="共享同一地址与端口的站点按域名路由">
          <label>监听地址<input required value={site.config.listenAddress} onChange={event => setConfig({ listenAddress: event.target.value })}/></label>
          <label>监听端口<input required type="number" min="1" max="65535" value={site.config.listenPort} onChange={event => setConfig({ listenPort: Number(event.target.value) })}/></label>
          <label className="wide">站点域名（Host / SNI，逗号分隔）<input value={(site.config.hostnames || []).join(', ')} onChange={event => setConfig({ hostnames: event.target.value.split(',').map(value => value.trim()).filter(Boolean) })} placeholder="app.example.com, api.example.com"/></label>
          <OptionToggle checked={!!site.config.tls} onChange={tls => setConfig({ tls })} label="启用站点 HTTPS" hint="选择系统证书，或上传站点专属证书与私钥">
            <ServerCertificateFields site={site} serverCertificates={serverCertificates} setConfig={setConfig} setFile={setFile}/>
          </OptionToggle>
        </FormSection>

        <FormSection title="上游" description="当前使用第一个上游转发请求">
          <label className="wide">上游 URL<input required type="url" value={upstream.url} onChange={event => setUpstream({ url: event.target.value })} placeholder="https://backend.example.com"/></label>
          <OptionToggle checked={sections.proxy} onChange={toggle('proxy')} label="通过代理连接上游" hint="HTTP(S)、SOCKS5 / SOCKS5H">
            <label className="wide">代理 URL<input required value={upstream.proxyUrl || ''} onChange={event => setUpstream({ proxyUrl: event.target.value })} placeholder="socks5://127.0.0.1:1080"/></label>
          </OptionToggle>
          <OptionToggle checked={sections.dialAddress} onChange={toggle('dialAddress')} label="覆盖连接地址（不使用 DNS 解析）" hint="仅直连时生效">
            <label className="wide">连接地址<input required value={upstream.dialAddress || ''} onChange={event => setUpstream({ dialAddress: event.target.value })} placeholder="203.0.113.10:443"/>
              <span className={sections.proxy ? 'cert-coverage warn' : 'cert-coverage'}>{sections.proxy ? '已启用代理：连接地址不会生效，由代理解析上游域名。' : '省略端口时按上游协议使用 80 或 443。'}</span>
            </label>
          </OptionToggle>
          {httpsUpstream ? <>
            <OptionToggle checked={sections.serverName} onChange={toggle('serverName')} label="自定义 SNI / 证书校验域名" hint="默认使用上游 URL 的域名">
              <label className="wide">SNI<input required value={upstream.serverName || ''} onChange={event => setUpstream({ serverName: event.target.value })} placeholder="backend.example.com"/></label>
            </OptionToggle>
            <OptionToggle checked={sections.rootCertificates} onChange={toggle('rootCertificates')} label="信任自定义 CA 根证书" hint="与系统默认根证书一起用于校验">
              <label className="wide">CA 根证书（可多选）
                <select multiple required size={Math.min(Math.max(rootCertificates.length, 2), 6)} disabled={!rootCertificates.length} value={upstream.rootCertificateIds || []} onChange={event => setUpstream({ rootCertificateIds: Array.from(event.target.selectedOptions, option => option.value) })}>
                  {rootCertificates.map(certificate => <option key={certificate.id} value={certificate.id}>{certificate.name} · {certificate.id.slice(0, 12)}</option>)}
                </select>
                <span>{rootCertificates.length ? '按住 Ctrl / ⌘ 可多选；该站点已有 CA Bundle 也会一并使用。' : '暂无系统 CA 根证书；请先在系统设置的“上游 CA 根证书”中添加。'}</span>
              </label>
            </OptionToggle>
            <OptionToggle checked={sections.mtls} onChange={toggle('mtls')} label="上游双向 TLS（mTLS）Client 证书" hint="上游要求客户端证书认证时启用">
              <ClientCertificateFields upstream={upstream} clientCertificates={clientCertificates} setUpstream={setUpstream} setFile={setFile}/>
            </OptionToggle>
            <label className="check"><input type="checkbox" checked={!!upstream.insecureSkipVerify} onChange={event => setUpstream({ insecureSkipVerify: event.target.checked })}/> 忽略上游 TLS 证书校验 <span className="warn-text">仅建议用于受控环境</span></label>
          </> : <p className="form-note wide">上游使用 https:// 时，可配置自定义 SNI、CA 根证书、mTLS Client 证书与证书校验选项。</p>}
        </FormSection>

        <FormSection title="访问日志">
          <OptionToggle checked={sections.accessLog} onChange={toggle('accessLog')} label="记录该站点的访问日志" hint={logAdapters.length ? '写入所选日志适配器' : '尚未配置日志适配器'}>
            <label className="wide">日志适配器
              <select required value={accessLog.adapterId || ''} onChange={event => setAccessLog({ adapterId: event.target.value })}>
                <option value="" disabled>请选择日志适配器</option>
                {logAdapters.map(adapter => <option key={adapter.id} value={adapter.id}>{adapter.name} · {adapter.config.adapter}</option>)}
              </select>
              {!logAdapters.length && <span>尚未配置日志适配器；请先到“日志适配器”页面添加。</span>}
            </label>
            <label className="check"><input type="checkbox" checked={!!accessLog.includeSensitiveHeaders} onChange={event => setAccessLog({ includeSensitiveHeaders: event.target.checked })}/> 记录敏感 Header 原值 <span>关闭时脱敏 Authorization、Cookie 等字段</span></label>
            <OptionToggle checked={!!accessLog.includeBodies} onChange={includeBodies => setAccessLog({ includeBodies })} label="记录完整请求与响应 Body" hint="可能包含个人或业务敏感信息">
              <label className="wide">Body 日志上限（字节，-1 表示无限）<input type="number" min="-1" max="8388608" value={accessLog.maxBodyBytes || 1048576} onChange={event => setAccessLog({ maxBodyBytes: Number(event.target.value) })}/><span>默认 1 MiB，正数最大 8 MiB；-1 不截断，但会增加内存占用</span></label>
            </OptionToggle>
          </OptionToggle>
        </FormSection>
      </div>
      <div className="modal-foot"><button type="button" className="secondary" onClick={onCancel}>取消</button><button className="primary" disabled={saving}>{saving ? '保存中…' : '保存配置'}</button></div>
    </form>
  </div>
}
