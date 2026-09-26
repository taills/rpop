import { isHTTPSURL } from './siteForm.js'

// Secrets (uploaded PEM files) are stored per site under random names; the site config references them by name.

function secretSuffix() {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

// clientCertificateUploads reports, per upstream, whether staged Client certificate files will be uploaded on save.
export function clientCertificateUploads(site, sections, files) {
  return site.config.upstreams.map((upstream, index) => Boolean(
    sections.upstreams[index]?.mtls && isHTTPSURL(upstream.url) && !upstream.clientCertificateId && (files[index]?.cert || files[index]?.key),
  ))
}

function planServerCertificate(config, certFile, keyFile) {
  const upload = Boolean(config.tls && !config.certificateId && (certFile || keyFile))
  if (!upload) return { config, uploads: [] }
  if (!certFile || !keyFile) throw new Error('站点 HTTPS 需要同时提供证书与私钥')
  const suffix = secretSuffix()
  const next = { ...config, certificateSecret: `site-cert-${suffix}`, privateKeySecret: `site-key-${suffix}` }
  return { config: next, uploads: [[next.certificateSecret, certFile], [next.privateKeySecret, keyFile]] }
}

function planClientCertificate(upstream, index, files = {}, uploading) {
  if (uploading) {
    if (!files.cert || !files.key) throw new Error(`上游 #${index + 1} 的双向 TLS 需要同时提供 Client 证书与私钥`)
    const suffix = secretSuffix()
    const next = { ...upstream, clientCertSecret: `upstream-cert-${suffix}`, clientKeySecret: `upstream-key-${suffix}` }
    return { upstream: next, uploads: [[next.clientCertSecret, files.cert], [next.clientKeySecret, files.key]] }
  }
  if (!upstream.clientCertSecret || !upstream.clientKeySecret) return { upstream: { ...upstream, clientCertSecret: '', clientKeySecret: '' }, uploads: [] }
  return { upstream, uploads: [] }
}

// planSecretUploads assigns fresh secret names to staged files and returns the updated draft plus [name, file] uploads.
export function planSecretUploads(draft, { certFile, keyFile, upstreamFiles = [], uploading = [] }) {
  const server = planServerCertificate(draft.config, certFile, keyFile)
  const clients = server.config.upstreams.map((upstream, index) => planClientCertificate(upstream, index, upstreamFiles[index], uploading[index]))
  const config = { ...server.config, upstreams: clients.map(client => client.upstream) }
  if (config.tls && !config.certificateId && (!config.certificateSecret || !config.privateKeySecret)) throw new Error('启用站点 HTTPS 前请选择系统 HTTPS 证书，或上传证书与私钥')
  return { draft: { ...draft, config }, uploads: [...server.uploads, ...clients.flatMap(client => client.uploads)] }
}

export function referencedSecrets(config = {}) {
  const upstreamSecrets = (config.upstreams || []).flatMap(upstream => [upstream.clientCertSecret, upstream.clientKeySecret])
  return [config.certificateSecret, config.privateKeySecret, ...upstreamSecrets].filter(Boolean)
}

// stagedConfig is saved first when a new site needs uploads, because secrets can only be stored for an existing site.
export function stagedConfig(config) {
  return {
    ...config, tls: false, certificateId: '', certificateSecret: '', privateKeySecret: '',
    upstreams: config.upstreams.map(upstream => ({ ...upstream, clientCertSecret: '', clientKeySecret: '' })),
  }
}
