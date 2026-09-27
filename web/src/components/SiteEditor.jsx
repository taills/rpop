import { useState } from 'react'
import '../Routing.css'
import { uncoveredHostnames } from '../siteForm.js'
import { FormSection, OptionToggle, certificateLabel } from './FormParts.jsx'
import PlacementFields from './PlacementFields.jsx'
import UpstreamFields from './UpstreamFields.jsx'
import RouteEditor from './RouteEditor.jsx'
import RouteSimulator from './RouteSimulator.jsx'

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

function AccessLogFields({ accessLog, enabled, logAdapters, onToggle, setAccessLog }) {
  return <OptionToggle checked={enabled} onChange={onToggle} label="记录该站点的访问日志" hint={logAdapters.length ? '写入所选日志适配器' : '尚未配置日志适配器'}>
    <label className="wide">日志适配器
      <select required value={accessLog.adapterId || ''} onChange={event => setAccessLog({ adapterId: event.target.value })}>
        <option value="" disabled>请选择日志适配器</option>
        {logAdapters.map(adapter => <option key={adapter.id} value={adapter.id}>{adapter.name} · {adapter.config.adapter}</option>)}
      </select>
      {!logAdapters.length && <span>尚未配置日志适配器；请先到“日志适配器”页面添加。</span>}
    </label>
    <label className="check"><input type="checkbox" checked={!!accessLog.includeSensitiveHeaders} onChange={event => setAccessLog({ includeSensitiveHeaders: event.target.checked })}/> 记录敏感 Header 原值 <span>关闭时脱敏 Authorization、Cookie 等字段</span></label>
    <OptionToggle checked={!!accessLog.includeBodies} onChange={includeBodies => setAccessLog({ includeBodies })} label="记录完整请求与响应 Body" hint="可能包含个人或业务敏感信息">
      <label className="wide">Body 日志上限（字节，-1 表示无限）<input type="number" min="-1" max="8388608" value={accessLog.maxBodyBytes ?? 1048576} onChange={event => setAccessLog({ maxBodyBytes: event.target.value === '' ? '' : Number(event.target.value) })}/><span>留空或 0 使用默认 1 MiB，正数最大 8 MiB；-1 不截断，但会增加内存占用</span></label>
    </OptionToggle>
  </OptionToggle>
}

export default function SiteEditor({ site, isNew, sections, catalog, saving, error, upstreamFiles, onChange, onSectionsChange, setFile, setUpstreamFile, onAddUpstream, onRemoveUpstream, onMakeDefault, onSimulate, onCancel, onSubmit }) {
  const { serverCertificates, logAdapters } = catalog
  const [matchedRoute, setMatchedRoute] = useState(null)
  const { upstreams } = site.config
  const routes = site.config.routes || []
  const accessLog = site.config.accessLog || {}
  const setConfig = patch => onChange({ ...site, config: { ...site.config, ...patch } })
  const setUpstream = index => patch => setConfig({ upstreams: upstreams.map((upstream, i) => i === index ? { ...upstream, ...patch } : upstream) })
  const setUpstreamSections = index => next => onSectionsChange({ ...sections, upstreams: sections.upstreams.map((item, i) => i === index ? next : item) })
  const setAccessLog = patch => setConfig({ accessLog: { ...accessLog, ...patch } })

  return <div className="overlay" onMouseDown={event => event.target === event.currentTarget && onCancel()}>
    <form className="modal site-modal" onSubmit={onSubmit}>
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

        <FormSection title="站点放置" description="选择运行该站点的数据面节点（D5）；候选路径可用的跳点会随此设置实时更新">
          <PlacementFields nodeIds={site.config.nodes || []} nodes={catalog.nodes || []} error={error} onChange={nodes => setConfig({ nodes })}/>
        </FormSection>

        <FormSection title="上游" description="每个上游拥有独立的代理、TLS 与连接设置；#1 为默认上游">
          {upstreams.map((upstream, index) => <UpstreamFields key={upstreamFiles[index]?.uid ?? index} index={index} count={upstreams.length} upstream={upstream} sections={sections.upstreams[index]}
            routeCount={routes.filter(route => Number(route.upstream) === index).length} catalog={catalog} placementIds={site.config.nodes || []} error={error}
            setUpstream={setUpstream(index)} setSections={setUpstreamSections(index)} setFile={(kind, file) => setUpstreamFile(index, kind, file)}
            onRemove={() => onRemoveUpstream(index)} onMakeDefault={() => onMakeDefault(index)}/>)}
          <button type="button" className="secondary add-upstream" onClick={onAddUpstream}>＋ 添加上游</button>
        </FormSection>

        <FormSection title="路由规则" description="参照 Caddy：按路径与 Header 将请求转发到不同上游">
          <RouteEditor routes={routes} upstreams={upstreams} matchedIndex={matchedRoute} onChange={next => setConfig({ routes: next })}/>
        </FormSection>

        <FormSection title="路由模拟" description="输入 URL 与 Header，实时查看命中的规则及路径改写（使用未保存的配置）">
          <RouteSimulator site={site} onSimulate={onSimulate} onMatch={setMatchedRoute}/>
        </FormSection>

        <FormSection title="访问日志">
          <AccessLogFields accessLog={accessLog} enabled={sections.accessLog} logAdapters={logAdapters} onToggle={accessLogEnabled => onSectionsChange({ ...sections, accessLog: accessLogEnabled })} setAccessLog={setAccessLog}/>
        </FormSection>
      </div>
      <div className="modal-foot"><button type="button" className="secondary" onClick={onCancel}>取消</button><button className="primary" disabled={saving}>{saving ? '保存中…' : '保存配置'}</button></div>
    </form>
  </div>
}
