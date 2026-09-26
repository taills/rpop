// Optional parts of a site configuration. Each is shown only while its checkbox is on; values of a switched-off
// section are kept while editing (so re-enabling restores them) and cleared by applySections when saving.
export function isHTTPSURL(url) {
  return (url || '').trim().toLowerCase().startsWith('https://')
}

export function sectionsForSite(site) {
  const upstream = site.config.upstreams[0] || {}
  return {
    proxy: Boolean(upstream.proxyUrl),
    dialAddress: Boolean(upstream.dialAddress),
    serverName: Boolean(upstream.serverName),
    rootCertificates: (upstream.rootCertificateIds || []).length > 0,
    mtls: Boolean(upstream.clientCertificateId || upstream.clientCertSecret),
    accessLog: Boolean(site.config.accessLog?.adapterId),
  }
}

export function applySections(site, sections) {
  const upstream = site.config.upstreams[0]
  const https = isHTTPSURL(upstream.url)
  const mtls = https && sections.mtls
  const nextUpstream = {
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
  const accessLog = { ...site.config.accessLog, adapterId: sections.accessLog ? site.config.accessLog?.adapterId || '' : '' }
  const tls = Boolean(site.config.tls)
  return {
    ...site,
    config: {
      ...site.config,
      accessLog,
      certificateId: tls ? site.config.certificateId || '' : '',
      certificateSecret: tls && !site.config.certificateId ? site.config.certificateSecret || '' : '',
      privateKeySecret: tls && !site.config.certificateId ? site.config.privateKeySecret || '' : '',
      upstreams: [nextUpstream, ...site.config.upstreams.slice(1)],
    },
  }
}

// validateSections reports sections that are switched on but left without a usable value.
export function validateSections(site, sections, { uploadingClientCertificate }) {
  const upstream = site.config.upstreams[0]
  const https = isHTTPSURL(upstream.url)
  if (sections.proxy && !upstream.proxyUrl?.trim()) return '已勾选“通过代理连接上游”，请填写代理 URL'
  if (sections.dialAddress && !upstream.dialAddress?.trim()) return '已勾选“覆盖连接地址”，请填写连接地址'
  if (https && sections.serverName && !upstream.serverName?.trim()) return '已勾选“自定义 SNI”，请填写 SNI'
  if (https && sections.rootCertificates && !(upstream.rootCertificateIds || []).length) return '已勾选“信任自定义 CA 根证书”，请至少选择一张根证书'
  if (https && sections.mtls && !upstream.clientCertificateId && !upstream.clientCertSecret && !uploadingClientCertificate) return '已勾选“上游双向 TLS（mTLS）”，请选择系统 Client 证书或上传证书与私钥'
  if (sections.accessLog && !site.config.accessLog?.adapterId) return '已勾选“记录访问日志”，请选择日志适配器'
  return ''
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
