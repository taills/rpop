import { test } from 'node:test'
import assert from 'node:assert/strict'
import { clientCertificateUploads, planSecretUploads, referencedSecrets, stagedConfig } from './siteSecrets.js'

const config = () => ({
  tls: true, certificateId: '', certificateSecret: 'site-cert-old', privateKeySecret: 'site-key-old',
  upstreams: [
    { url: 'https://a', clientCertificateId: '', clientCertSecret: 'upstream-cert-a', clientKeySecret: 'upstream-key-a' },
    { url: 'https://b', clientCertificateId: '', clientCertSecret: '', clientKeySecret: '' },
    { url: 'http://c', clientCertSecret: 'stale', clientKeySecret: '' },
  ],
})

test('clientCertificateUploads flags upstreams with staged mTLS files', () => {
  const site = { config: config() }
  const sections = { upstreams: [{ mtls: true }, { mtls: true }, { mtls: true }] }
  assert.deepEqual(clientCertificateUploads(site, sections, [{}, { cert: 'c', key: 'k' }, { cert: 'c' }]), [false, true, false])
})

test('planSecretUploads names uploads per upstream without mutating the draft', () => {
  const draft = { id: 's', config: config() }
  const { draft: planned, uploads } = planSecretUploads(draft, { upstreamFiles: [{}, { cert: 'cert-b', key: 'key-b' }], uploading: [false, true, false] })
  const b = planned.config.upstreams[1]
  assert.match(b.clientCertSecret, /^upstream-cert-/)
  assert.match(b.clientKeySecret, /^upstream-key-/)
  assert.deepEqual(uploads, [[b.clientCertSecret, 'cert-b'], [b.clientKeySecret, 'key-b']])
  assert.equal(planned.config.upstreams[0].clientCertSecret, 'upstream-cert-a')
  assert.equal(planned.config.upstreams[2].clientCertSecret, '', 'a half-configured pair is cleared')
  assert.equal(draft.config.upstreams[1].clientCertSecret, '', 'input draft must not be mutated')
})

test('planSecretUploads requires both files', () => {
  assert.throws(() => planSecretUploads({ id: 's', config: config() }, { upstreamFiles: [{ cert: 'c' }], uploading: [true] }), /上游 #1 的双向 TLS 需要同时提供 Client 证书与私钥/)
  assert.throws(() => planSecretUploads({ id: 's', config: config() }, { certFile: 'c', uploading: [] }), /站点 HTTPS 需要同时提供证书与私钥/)
})

test('planSecretUploads replaces the site certificate secrets when both files are given', () => {
  const { draft, uploads } = planSecretUploads({ id: 's', config: config() }, { certFile: 'c', keyFile: 'k', uploading: [] })
  assert.match(draft.config.certificateSecret, /^site-cert-/)
  assert.notEqual(draft.config.certificateSecret, 'site-cert-old')
  assert.equal(uploads.length, 2)
})

test('referencedSecrets and stagedConfig cover every upstream', () => {
  assert.deepEqual(referencedSecrets(config()), ['site-cert-old', 'site-key-old', 'upstream-cert-a', 'upstream-key-a', 'stale'])
  const staged = stagedConfig(config())
  assert.equal(staged.tls, false)
  assert.deepEqual(referencedSecrets(staged), [])
})
