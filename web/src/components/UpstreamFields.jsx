import { isHTTPSURL } from '../siteForm.js'
import { OptionToggle, certificateLabel } from './FormParts.jsx'
import PathsEditor from './PathsEditor.jsx'

function ClientCertificateFields({ upstream, clientCertificates, setUpstream, setFile }) {
  return <>
    <label className="wide">Client 证书来源
      <select value={upstream.clientCertificateId || ''} onChange={event => { const clientCertificateId = event.target.value; if (clientCertificateId) { setFile('cert', null); setFile('key', null) } setUpstream({ clientCertificateId }) }}>
        <option value="">上传站点专属证书</option>
        {clientCertificates.map(certificate => <option key={certificate.id} value={certificate.id}>{certificateLabel(certificate, certificate.subject)}</option>)}
      </select>
      <span>{upstream.clientCertificateId ? '使用系统设置中集中管理的 Client 证书。' : upstream.clientCertSecret ? '已配置站点专属证书；不选择文件表示保留，上传新证书与私钥可替换。' : clientCertificates.length ? '可选择系统 Client 证书，或上传站点专属证书与私钥。' : '暂无系统 Client 证书；可上传站点专属证书，或在系统设置的“mTLS Client 证书”中添加。'}</span>
    </label>
    {!upstream.clientCertificateId && <>
      <label>站点 Client 证书 PEM<input type="file" accept=".pem,.crt,.cer" onChange={event => setFile('cert', event.target.files?.[0] || null)}/></label>
      <label>站点 Client 私钥 PEM<input type="file" accept=".pem,.key" onChange={event => setFile('key', event.target.files?.[0] || null)}/></label>
    </>}
  </>
}

function TLSOptions({ upstream, sections, toggle, catalog, setUpstream, setFile }) {
  const { rootCertificates, clientCertificates } = catalog
  return <>
    <OptionToggle checked={sections.serverName} onChange={toggle('serverName')} label="自定义 SNI / 证书校验域名" hint="默认使用上游 URL 的域名">
      <label className="wide">SNI<input required value={upstream.serverName || ''} onChange={event => setUpstream({ serverName: event.target.value })} placeholder="backend.example.com"/></label>
    </OptionToggle>
    <OptionToggle checked={sections.rootCertificates} onChange={toggle('rootCertificates')} label="信任自定义 CA 根证书" hint="与系统默认根证书一起用于校验">
      <label className="wide">CA 根证书（可多选）
        <select multiple required size={Math.min(Math.max(rootCertificates.length, 2), 6)} disabled={!rootCertificates.length} value={upstream.rootCertificateIds || []} onChange={event => setUpstream({ rootCertificateIds: Array.from(event.target.selectedOptions, option => option.value) })}>
          {rootCertificates.map(certificate => <option key={certificate.id} value={certificate.id}>{certificate.name} · {certificate.id.slice(0, 12)}</option>)}
        </select>
        <span>{rootCertificates.length ? '按住 Ctrl / ⌘ 可多选；该上游已有 CA Bundle 也会一并使用。' : '暂无系统 CA 根证书；请先在系统设置的“上游 CA 根证书”中添加。'}</span>
      </label>
    </OptionToggle>
    <OptionToggle checked={sections.mtls} onChange={toggle('mtls')} label="上游双向 TLS（mTLS）Client 证书" hint="上游要求客户端证书认证时启用">
      <ClientCertificateFields upstream={upstream} clientCertificates={clientCertificates} setUpstream={setUpstream} setFile={setFile}/>
    </OptionToggle>
    <label className="check"><input type="checkbox" checked={!!upstream.insecureSkipVerify} onChange={event => setUpstream({ insecureSkipVerify: event.target.checked })}/> 忽略上游 TLS 证书校验 <span className="warn-text">仅建议用于受控环境</span></label>
  </>
}

// UpstreamFields edits one upstream; the first upstream is the default for requests that match no route.
export default function UpstreamFields({ index, count, upstream, sections, routeCount, catalog, placementIds, error, setUpstream, setSections, setFile, onRemove, onMakeDefault }) {
  const toggle = key => checked => setSections({ ...sections, [key]: checked })
  return <div className="wide upstream-card">
    <div className="upstream-card-head">
      <span className="upstream-no">#{index + 1}</span>
      <strong>{index === 0 ? '默认上游' : `上游 #${index + 1}`}</strong>
      <small>{routeCount ? `${routeCount} 条规则指向此上游` : index === 0 ? '未命中规则的请求转发到这里' : '尚无规则指向此上游'}</small>
      <span className="upstream-card-actions">
        {index > 0 && <button type="button" className="link-btn" onClick={onMakeDefault}>设为默认</button>}
        {count > 1 && <button type="button" className="link-btn danger" onClick={onRemove}>删除</button>}
      </span>
    </div>
    <div className="form-grid">
      <label className="wide">上游 URL<input required type="url" value={upstream.url} onChange={event => setUpstream({ url: event.target.value })} placeholder="https://backend.example.com/base"/><span className="cert-coverage">URL 中的路径会作为前缀拼接在转发路径之前。</span></label>
      <OptionToggle checked={sections.proxy} onChange={toggle('proxy')} label="通过代理连接上游" hint="HTTP(S)、SOCKS5 / SOCKS5H">
        <label className="wide">代理 URL<input required value={upstream.proxyUrl || ''} onChange={event => setUpstream({ proxyUrl: event.target.value })} placeholder="socks5://127.0.0.1:1080"/></label>
      </OptionToggle>
      <OptionToggle checked={sections.dialAddress} onChange={toggle('dialAddress')} label="覆盖连接地址（不使用 DNS 解析）" hint="仅直连时生效">
        <label className="wide">连接地址<input required value={upstream.dialAddress || ''} onChange={event => setUpstream({ dialAddress: event.target.value })} placeholder="203.0.113.10:443"/>
          <span className={sections.proxy ? 'cert-coverage warn' : 'cert-coverage'}>{sections.proxy ? '已启用代理：连接地址不会生效，由代理解析上游域名。' : '省略端口时按上游协议使用 80 或 443。'}</span>
        </label>
      </OptionToggle>
      <OptionToggle checked={sections.paths} onChange={toggle('paths')} label="候选路径（节点 / 具名代理链路降级）" hint="按优先级排列，建连失败自动切换下一条">
        <PathsEditor upstream={upstream} upstreamIndex={index} catalog={catalog} placementIds={placementIds} error={error} setUpstream={setUpstream}/>
      </OptionToggle>
      {isHTTPSURL(upstream.url)
        ? <TLSOptions upstream={upstream} sections={sections} toggle={toggle} catalog={catalog} setUpstream={setUpstream} setFile={setFile}/>
        : <p className="form-note wide">上游使用 https:// 时，可配置自定义 SNI、CA 根证书、mTLS Client 证书与证书校验选项。</p>}
    </div>
  </div>
}
