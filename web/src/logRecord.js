// Standard access-log fields of a record. Records written before these fields existed fall back to the
// logged request headers (header names are stored in Go's canonical form).
function requestHeader(record, name) {
  return record.requestHeaders?.[name]?.join(', ') || ''
}

export function clientIP(record) {
  return record.clientIp || ''
}

export function clientAddress(record) {
  const ip = clientIP(record)
  if (!ip || !record.clientPort) return ip
  return ip.includes(':') ? `[${ip}]:${record.clientPort}` : `${ip}:${record.clientPort}`
}

export function forwardedFor(record) {
  return record.forwardedFor || requestHeader(record, 'X-Forwarded-For')
}

export function userAgent(record) {
  return record.userAgent || requestHeader(record, 'User-Agent')
}

export function referer(record) {
  return record.referer || requestHeader(record, 'Referer')
}

export function protocolLabel(record) {
  return [record.scheme?.toUpperCase(), record.protocol, record.tlsVersion].filter(Boolean).join(' · ')
}
