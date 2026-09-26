import { normalizeRoute, remapRoutesAfterRemoval, remapRoutesForDefault, routeForEditing, validateRoutes } from './routing.js'

// Optional parts of a site configuration. Each is shown only while its checkbox is on; values of a switched-off
// section are kept while editing (so re-enabling restores them) and cleared by applySections when saving.
export function isHTTPSURL(url) {
  return (url || '').trim().toLowerCase().startsWith('https://')
}

export function blankUpstream() {
  return { url: '', proxyUrl: '', proxyType: 'direct', serverName: '', dialAddress: '', insecureSkipVerify: false, rootCertificateIds: [], clientCertificateId: '', clientCertSecret: '', clientKeySecret: '' }
}

export function sectionsForUpstream(upstream = {}) {
  return {
    proxy: Boolean(upstream.proxyUrl),
    dialAddress: Boolean(upstream.dialAddress),
    serverName: Boolean(upstream.serverName),
    rootCertificates: (upstream.rootCertificateIds || []).length > 0,
    mtls: Boolean(upstream.clientCertificateId || upstream.clientCertSecret),
  }
}

export function sectionsForSite(site) {
  return {
    upstreams: site.config.upstreams.map(sectionsForUpstream),
    accessLog: Boolean(site.config.accessLog?.adapterId),
  }
}

// prepareSiteForEditing returns a copy whose routes use the editor shape (explicit header modes).
export function prepareSiteForEditing(site) {
  const copy = structuredClone(site)
  const upstreams = copy.config.upstreams?.length ? copy.config.upstreams : [blankUpstream()]
  return { ...copy, config: { ...copy.config, upstreams, routes: (copy.config.routes || []).map(routeForEditing) } }
}

function applyUpstreamSections(upstream, sections = {}) {
  const https = isHTTPSURL(upstream.url)
  const mtls = https && sections.mtls
  return {
    ...upstream,
    proxyUrl: sections.proxy ? upstream.proxyUrl : '',
    proxyType: sections.proxy && upstream.proxyUrl ? 'proxy' : 'direct',
    dialAddress: sections.dialAddress ? upstream.dialAddress : '',
    serverName: https && sections.serverName ? upstream.serverName : '',
    rootCertificateIds: https && sections.rootCertificates ? upstream.rootCertificateIds || [] : [],
    clientCertificateId: mtls ? upstream.clientCertificateId || '' : '',
    clientCertSecret: mtls && !upstream.clientCertificateId ? upstream.clientCertSecret || '' : '',
    clientKeySecret: mtls && !upstream.clientCertificateId ? upstream.clientKeySecret || '' : '',
    insecureSkipVerify: https && Boolean(upstream.insecureSkipVerify),
  }
}

export function applySections(site, sections) {
  // An emptied body-limit field is saved as 0, which the server treats as the 1 MiB default.
  const accessLog = { ...site.config.accessLog, adapterId: sections.accessLog ? site.config.accessLog?.adapterId || '' : '', maxBodyBytes: Number(site.config.accessLog?.maxBodyBytes) || 0 }
  const tls = Boolean(site.config.tls)
  return {
    ...site,
    config: {
      ...site.config,
      accessLog,
      certificateId: tls ? site.config.certificateId || '' : '',
      certificateSecret: tls && !site.config.certificateId ? site.config.certificateSecret || '' : '',
      privateKeySecret: tls && !site.config.certificateId ? site.config.privateKeySecret || '' : '',
      upstreams: site.config.upstreams.map((upstream, index) => applyUpstreamSections(upstream, sections.upstreams[index])),
      routes: (site.config.routes || []).map(normalizeRoute),
    },
  }
}

function upstreamProblem(upstream, sections = {}, uploadingClientCertificate) {
  const https = isHTTPSURL(upstream.url)
  if (!upstream.url?.trim()) return '请填写上游 URL'
  if (sections.proxy && !upstream.proxyUrl?.trim()) return '已勾选“通过代理连接上游”，请填写代理 URL'
  if (sections.dialAddress && !upstream.dialAddress?.trim()) return '已勾选“覆盖连接地址”，请填写连接地址'
  if (https && sections.serverName && !upstream.serverName?.trim()) return '已勾选“自定义 SNI”，请填写 SNI'
  if (https && sections.rootCertificates && !(upstream.rootCertificateIds || []).length) return '已勾选“信任自定义 CA 根证书”，请至少选择一张根证书'
  if (https && sections.mtls && !upstream.clientCertificateId && !upstream.clientCertSecret && !uploadingClientCertificate) return '已勾选“上游双向 TLS（mTLS）”，请选择系统 Client 证书或上传证书与私钥'
  return ''
}

// validateSections reports sections that are switched on but left without a usable value, then invalid routes.
export function validateSections(site, sections, { uploadingClientCertificates = [] }) {
  for (const [index, upstream] of site.config.upstreams.entries()) {
    const problem = upstreamProblem(upstream, sections.upstreams[index], uploadingClientCertificates[index])
    if (problem) return `上游 #${index + 1}：${problem}`
  }
  const routeProblem = validateRoutes(site.config.routes || [], site.config.upstreams.length)
  if (routeProblem) return routeProblem
  if (sections.accessLog && !site.config.accessLog?.adapterId) return '已勾选“记录访问日志”，请选择日志适配器'
  return ''
}

let uploadSlotCounter = 0

// uploadSlot holds an upstream's staged Client certificate files; uid gives its editor card a stable React key.
export function uploadSlot() {
  uploadSlotCounter += 1
  return { uid: `upstream-${uploadSlotCounter}` }
}

// The editor keeps upstreams, their sections and their staged certificate files in parallel arrays;
// these helpers change all three (and the routes' upstream indices) together without mutating the input.
function withUpstreams({ site, sections, files }, order, routes) {
  return {
    site: { ...site, config: { ...site.config, upstreams: order.map(index => site.config.upstreams[index]), routes } },
    sections: { ...sections, upstreams: order.map(index => sections.upstreams[index]) },
    files: order.map(index => files[index] || uploadSlot()),
  }
}

export function addUpstream({ site, sections, files }) {
  return {
    site: { ...site, config: { ...site.config, upstreams: [...site.config.upstreams, blankUpstream()] } },
    sections: { ...sections, upstreams: [...sections.upstreams, sectionsForUpstream()] },
    files: [...site.config.upstreams.map((_, index) => files[index] || uploadSlot()), uploadSlot()],
  }
}

export function removeUpstream(state, removed) {
  const order = state.site.config.upstreams.map((_, index) => index).filter(index => index !== removed)
  const { routes, dropped } = remapRoutesAfterRemoval(state.site.config.routes || [], removed)
  return { state: withUpstreams(state, order, routes), dropped }
}

export function makeDefaultUpstream(state, index) {
  const order = [index, ...state.site.config.upstreams.map((_, i) => i).filter(i => i !== index)]
  return withUpstreams(state, order, remapRoutesForDefault(state.site.config.routes || [], index))
}

function certificateNameCovers(certificateName, hostname) {
  const name = certificateName.toLowerCase()
  if (name === hostname) return true
  if (!name.startsWith('*.') || hostname.startsWith('*.')) return false
  const [label, ...rest] = hostname.split('.')
  return Boolean(label) && rest.join('.') === name.slice(2)
}

export function uncoveredHostnames(hostnames = [], dnsNames = []) {
  return hostnames.map(host => host.toLowerCase()).filter(host => !dnsNames.some(name => certificateNameCovers(name, host)))
}

export function isExpired(certificate) {
  return Boolean(certificate.notAfter) && Date.parse(certificate.notAfter) < Date.now()
}
