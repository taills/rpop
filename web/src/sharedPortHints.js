// sharedPortHints.js predicts, on the client and before any round trip, the same shared-port errors
// internal/control.validateSharedPortPlacement would return for real once a site is saved/started (see
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)"). It mirrors the rules
// internal/sharedport.PutSite/PutPlaintextOwner/PutTLSOwner and internal/control/shared_port_validate.go
// enforce closely enough to reuse their exact Chinese wording, so a hint here reads identically to the real
// 400 it is predicting. It is pure and framework-free on purpose (see sharedPortHints.test.js) — SiteEditor.jsx
// is just presentation over these functions.
//
// This is advisory only: SiteEditor never blocks saving on what this module returns, and the backend's own
// rejection (surfaced verbatim in the form's error banner) is what actually gates the save. Every address or
// hostname this module cannot confidently parse is silently skipped rather than guessed at — an incomplete
// hint set is preferred over a false positive, since the operator may simply be mid-edit (see
// normalizeAddressKey's doc comment).

import { relayPortFromAddress } from './nodeBootstrap.js'

// LOCAL_NODE_ID mirrors internal/control.LocalNodeID: an empty site placement means "the embedded node only".
const LOCAL_NODE_ID = 'local'

// --- address parsing/classification (mirrors internal/sharedport.NormalizeAddress/Classify) -------------------

// splitHostPort mirrors net.SplitHostPort closely enough for this module's purposes: it accepts "host:port" and
// "[ipv6]:port", and — like the Go stdlib — refuses to guess at an unbracketed host that itself contains a
// colon (a literal IPv6 address with no brackets is ambiguous: which colon is the port separator?), returning
// null instead of picking one arbitrarily.
function splitHostPort(address) {
  const trimmed = String(address ?? '').trim()
  const bracketed = trimmed.match(/^\[(.*)\]:(\d+)$/)
  if (bracketed) return { host: bracketed[1], port: bracketed[2] }
  const lastColon = trimmed.lastIndexOf(':')
  if (lastColon < 0) return null
  const host = trimmed.slice(0, lastColon)
  const port = trimmed.slice(lastColon + 1)
  if (host.includes(':') || !/^\d+$/.test(port)) return null
  return { host, port }
}

// joinHostPort mirrors net.JoinHostPort: brackets a host that itself contains a colon (IPv6) before appending
// the port, so the result round-trips through splitHostPort unambiguously.
function joinHostPort(host, port) {
  const h = String(host ?? '')
  return h.includes(':') && !h.startsWith('[') ? `[${h}]:${port}` : `${h}:${port}`
}

// normalizeAddressKey returns null for anything it cannot confidently parse (see splitHostPort), otherwise
// {host, port, wildcard}: host is '*' for any wildcard spelling ('', '0.0.0.0', '::', '*', matching
// NormalizeAddress), a lower-cased hostname, or a literal IP otherwise. Unlike NormalizeAddress this does not
// canonicalize IPv6 (net.IP.String()'s zero-compression) — "::1" and "0:0:0:0:0:0:0:1" are treated as distinct
// hosts here. That can only make this module under-detect a real "same address" match, never invent a false
// one, which is the safe direction for a hint that must never be wrong, only occasionally silent.
function normalizeAddressKey(address) {
  const parsed = splitHostPort(address)
  if (!parsed) return null
  const port = Number(parsed.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) return null
  const raw = parsed.host.trim()
  const wildcard = raw === '' || raw === '0.0.0.0' || raw === '::' || raw === '*'
  return { host: wildcard ? '*' : raw.toLowerCase(), port, wildcard }
}

// classifyAddresses mirrors sharedport.Classify: 'same' (one physical listener), 'conflicting' (same port, one
// wildcard and one specific — the OS can never bind both), 'distinct' (unrelated), or null when either address
// could not be parsed with confidence (see normalizeAddressKey) — callers should treat null like 'distinct'
// (say nothing) rather than surface an error about the address field itself; that is siteForm.js's job.
export function classifyAddresses(a, b) {
  const keyA = normalizeAddressKey(a)
  const keyB = normalizeAddressKey(b)
  if (!keyA || !keyB) return null
  if (keyA.port !== keyB.port) return 'distinct'
  if (keyA.host === keyB.host) return 'same'
  return keyA.wildcard || keyB.wildcard ? 'conflicting' : 'distinct'
}

// conflictMessage mirrors sharedport.ConflictError's wording exactly, so a predicted conflict reads identically
// to the real 400 (see shared_port_validate_test.go's TestValidateSharedPortPlacementRejectsConflictingScope).
export function conflictMessage(addr, addrUse, otherAddr, otherUse) {
  return `${addrUse}(${addr})与${otherUse}(${otherAddr})端口相同但绑定范围不同(通配地址与具体地址无法共用同一端口): 请把两者改成完全相同的地址以复用端口,或改用不同端口`
}

// --- hostname rules (mirrors internal/pki.IsInternalHostname / internal/sharedport.hostPatternsOverlap) -------

function normalizeHostname(host) {
  return String(host ?? '').trim().toLowerCase().replace(/\.+$/, '')
}

// isInternalHostname mirrors pki.IsInternalHostname: the controller's own name and every node's name (via the
// "*.nodes.rpop" suffix) are reserved and can never be claimed by a site hostname.
export function isInternalHostname(host) {
  const h = normalizeHostname(host)
  return h === 'controller.rpop' || h.endsWith('.nodes.rpop')
}

function hostPatternMatches(pattern, host) {
  if (!pattern.startsWith('*.')) return pattern === host
  const suffix = pattern.slice(1)
  return host.length > suffix.length && host.endsWith(suffix)
}

// hostnamesOverlap mirrors sharedport.hostPatternsOverlap (also exposed server-side as sharedport.HostnamesOverlap):
// two hostname patterns overlap when they are identical, or one is a "*.suffix" wildcard matching the other.
export function hostnamesOverlap(a, b) {
  const x = normalizeHostname(a)
  const y = normalizeHostname(b)
  if (x === y) return true
  const xWild = x.startsWith('*.')
  const yWild = y.startsWith('*.')
  if (xWild && !yWild) return hostPatternMatches(x, y)
  if (yWild && !xWild) return hostPatternMatches(y, x)
  if (xWild && yWild) return x.slice(1).endsWith(y.slice(1)) || y.slice(1).endsWith(x.slice(1))
  return false
}

// normalizeHostnameList lower-cases/trims/de-duplicates, mirroring sharedport.NormalizeHostnames closely enough
// for a hint (it does not reject malformed hostnames the way the server does — siteForm.js's own validation and
// the server's final say already cover that; this module only ever adds hints, never blocks saving).
function normalizeHostnameList(hostnames) {
  const seen = new Set()
  const out = []
  for (const raw of hostnames || []) {
    const host = normalizeHostname(raw)
    if (host && !seen.has(host)) { seen.add(host); out.push(host) }
  }
  return out
}

// --- placement (mirrors internal/control.siteNodes / relayListenAddress) ---------------------------------------

// placementNodeIds mirrors control.siteNodes.
export function placementNodeIds(nodeIds) {
  return nodeIds && nodeIds.length ? nodeIds : [LOCAL_NODE_ID]
}

// relayListenAddress mirrors control.relayListenAddress: the wildcard ":port" bind a node's relay port listens
// on, guessed from the port other nodes dial it on (node.relayAddress) — '' when relayAddress has no valid
// trailing ":port" (see relayPortFromAddress, already used by the node onboarding guide for the same
// extraction). This is necessarily a guess, same as the server's own: an operator's local -relay-listen can
// bind a different address, which neither the controller nor this module has any way to know — see
// sharedPortOwner's doc comment in shared_port_validate.go for why the server never hard-rejects a mismatch
// here (see the `authoritative` flag below, which this module mirrors for the same reason).
export function relayListenAddress(relayAddress) {
  const port = relayPortFromAddress(relayAddress)
  return port ? `:${port}` : ''
}

// nodeLabel mirrors control.nodeLabel — used only to prefix admitSite's error exactly the way
// validateSharedPortForNodeLocked's caller wraps it ("放置节点 %s: %w").
function nodeLabel(id) {
  return id === LOCAL_NODE_ID ? '内嵌节点' : id
}

// --- the admission rule itself (mirrors internal/sharedport/route.go's siteTable.admit) --------------------------

// admitSite mirrors PredictedSiteSet.Admit / siteTable.admit: whether a site with id/hostnames (already
// normalized, see normalizeHostnameList) may join an address alongside hasOwner (a same-mode console/southbound/
// relay owner already there) and siblings (other sites believed to share the address *and* TLS mode — see
// siteSharedPortHints, which builds this the same way validateSharedPortForNodeLocked does: same-mode is what
// determines whether two claims land in the same plaintext-or-TLS routing table). Returns '' when admitted, or
// the same Chinese error text PutSite would return for real otherwise.
export function admitSite(id, hostnames, hasOwner, siblings) {
  let others = 0
  for (const sibling of siblings) {
    if (sibling.id === id) continue
    others += 1
    if (hostnames.length > 0 && sibling.hostnames.length === 0) {
      return `已有站点 ${sibling.id} 未配置 hostname; 请先为它配置 hostname 再共用监听地址`
    }
  }
  if (hostnames.length === 0 && (others > 0 || hasOwner)) {
    return '与其他站点或控制台/中继共用监听地址时必须配置 hostname'
  }
  for (const hostname of hostnames) {
    for (const sibling of siblings) {
      if (sibling.id === id) continue
      for (const existingHost of sibling.hostnames) {
        if (hostnamesOverlap(hostname, existingHost)) {
          return `hostname "${hostname}" 与站点 ${sibling.id} 已使用的 hostname 重叠`
        }
      }
    }
  }
  return ''
}

// ownersForNode lists the fixed-role occupants of nodeId's shared address: the console and southbound for the
// embedded node (LOCAL_NODE_ID), or a registered node's own relay port otherwise — mirrors
// validateSharedPortForNodeLocked's owners construction.
function ownersForNode(nodeId, bootstrapInfo, nodes) {
  if (nodeId === LOCAL_NODE_ID) {
    const owners = []
    if (bootstrapInfo?.consoleAddr) {
      owners.push({
        label: '控制台', addr: bootstrapInfo.consoleAddr, tls: false,
        restrictHostnames: normalizeHostnameList(bootstrapInfo.consoleHostnames), authoritative: true,
      })
    }
    if (bootstrapInfo?.southboundEnabled && bootstrapInfo.southboundAddr) {
      owners.push({ label: 'southbound', addr: bootstrapInfo.southboundAddr, tls: true, restrictHostnames: [], authoritative: true })
    }
    return owners
  }
  const node = (nodes || []).find((candidate) => candidate.id === nodeId)
  const relayListen = node ? relayListenAddress(node.relayAddress) : ''
  if (!relayListen) return []
  // Not authoritative: see relayListenAddress's doc comment — a node's own -relay-listen override could bind
  // somewhere else, so a same-port-but-different-scope guess here is not trustworthy enough to warn about.
  return [{ label: `节点 ${nodeId} 的中继端口`, addr: relayListen, tls: true, restrictHostnames: [], authoritative: false }]
}

function siteLabel(id) {
  return id ? `站点 ${id}` : '当前站点'
}

// siteSharedPortHints predicts, without a round trip, the same errors internal/control.validateSharedPortPlacement
// would return for real when this site is saved or started (see docs/architecture/control-data-plane.md §5,
// "共享端口(第三段)"). Never blocks saving — SiteEditor shows these as dismissable hints alongside the field
// they concern; the backend's own 400 (shown verbatim in the form's error banner) is what actually gates a save.
//
// Unlike the server, which fails fast on the first node that rejects a multi-node placement, this returns every
// hint found across every placement node: more complete information is more useful in a non-blocking hint, and
// which specific error the server returns first for an invalid multi-node placement is an implementation detail
// this module does not try to reproduce.
//
// Sibling *sites* (as opposed to the console/southbound/relay owners, which occupy their address regardless of
// any site's state) are only a real collision surface once they are actually registered — a saved-but-never-
// started site parked on the same address is not one (see internal/control.desiredSiblingSitesLocked's doc
// comment: comparing against every stored site, not just the ones actually desired, was a stage 3 review
// finding — it wrongly rejected "several configs parked on one port, started one at a time"). This module has
// no access to the server's desired-set bookkeeping, so it approximates it with each sibling's own `running`
// flag (see store.Site's `running` json field, and internal/control.siteRunning, which is what populates it) —
// close enough for an advisory hint that is not the actual gate.
//
// - site: the config being edited (site.id, site.config.{listenAddress,listenPort,hostnames,tls,nodes}).
// - bootstrapInfo: the GET /api/nodes/bootstrap-info response (consoleAddr/consoleHostnames/southboundEnabled/
//   southboundAddr).
// - nodes: the full GET /api/nodes list (used for each placement node's relayAddress).
// - sites: every other already-saved site (GET /api/sites, each with its `running` flag) — the site being
//   edited is excluded by id, and any sibling with `running` falsy is ignored, so callers may pass the full
//   list unfiltered.
export function siteSharedPortHints({ site, bootstrapInfo, nodes = [], sites = [] } = {}) {
  const config = site?.config || {}
  const hostnames = normalizeHostnameList(config.hostnames)
  const hints = []

  for (const hostname of hostnames) {
    if (isInternalHostname(hostname)) {
      hints.push({ severity: 'warning', kind: 'internal-hostname', text: `hostname "${hostname}" 是内部保留名(控制器或节点专用),请改用其他 hostname` })
    }
  }

  const siteAddr = joinHostPort(config.listenAddress, config.listenPort)
  if (!normalizeAddressKey(siteAddr)) return hints // still mid-edit (blank/invalid address or port); say nothing more

  for (const nodeId of placementNodeIds(config.nodes)) {
    const owners = ownersForNode(nodeId, bootstrapInfo, nodes)
    const sameAddressOwners = []
    for (const owner of owners) {
      const relation = classifyAddresses(siteAddr, owner.addr)
      if (relation === 'conflicting' && owner.authoritative) {
        hints.push({ severity: 'warning', kind: 'address-conflict', text: conflictMessage(siteAddr, siteLabel(site?.id), owner.addr, owner.label) })
      } else if (relation === 'same') {
        sameAddressOwners.push(owner)
      }
    }

    const siblings = []
    for (const other of sites) {
      if (!other || other.id === site?.id) continue
      if (!other.running) continue // not a real collision surface yet — see this function's doc comment
      if (!placementNodeIds(other.config?.nodes).includes(nodeId)) continue
      const otherAddr = joinHostPort(other.config?.listenAddress, other.config?.listenPort)
      const relation = classifyAddresses(siteAddr, otherAddr)
      if (relation === 'conflicting') {
        hints.push({ severity: 'warning', kind: 'address-conflict', text: conflictMessage(siteAddr, siteLabel(site?.id), otherAddr, siteLabel(other.id)) })
      } else if (relation === 'same' && Boolean(other.config?.tls) === Boolean(config.tls)) {
        siblings.push({ id: other.id, hostnames: normalizeHostnameList(other.config?.hostnames) })
      }
    }

    // Only a same-mode owner (plaintext owner for a plaintext site, TLS owner for a TLS site) shares this
    // site's routing table — sharedport dispatches by first byte before either is reached, so e.g. a TLS site
    // sharing a port with a plaintext-only console (no southbound/relay there) never touches the console's
    // table and needs no hostname on that account; mentioning it anyway would read as a false conflict.
    const matchingOwners = sameAddressOwners.filter((owner) => owner.tls === Boolean(config.tls))
    for (const owner of matchingOwners) {
      hints.push({
        severity: 'info', kind: 'reuse',
        text: `监听地址将与${owner.label}(${owner.addr})共用同一端口,按${config.tls ? ' SNI' : ' Host'}区分;请确保已为该站点配置 hostname。`,
      })
    }
    const admitError = admitSite(site?.id, hostnames, matchingOwners.length > 0, siblings)
    if (admitError) {
      hints.push({ severity: 'warning', kind: 'hostname-admission', text: `放置节点 ${nodeLabel(nodeId)}: ${admitError}` })
    }

    if (!config.tls) {
      for (const owner of sameAddressOwners) {
        if (owner.tls || !owner.restrictHostnames.length) continue
        for (const hostname of hostnames) {
          const overlap = owner.restrictHostnames.find((restricted) => hostnamesOverlap(hostname, restricted))
          if (overlap) {
            hints.push({
              severity: 'warning', kind: 'console-hostname-overlap',
              text: `hostname "${hostname}" 与控制台 -console-hostnames 限定的 "${overlap}" 重叠,请改用不同的 hostname`,
            })
          }
        }
      }
    }
  }

  return hints
}

// isSharedPortError reports whether a server error is one of the shared-port hostname/address-scope rejections
// this module predicts — from pre-validation (shared_port_validate.go) or, since pre-validation is necessarily
// best-effort for a node's own -relay-listen override (see relayListenAddress's doc comment), the same checks
// re-run for real at apply time (internal/sharedport/route.go, owner.go, address.go and
// internal/dataplane.Engine.buildRuntime's own internal-hostname check). So SiteEditor.jsx can echo it next to
// the "监听" fields it concerns, instead of only in the form's general error banner.
//
// Every one of those errors, at either layer, names "hostname" or the address-conflict wording verbatim — a
// grep across internal/ (see sharedPortHints.test.js's comment) turns up no other error in the codebase using
// either phrase, so this is a reliable match rather than a guess; anything else stays in the general banner.
const SHARED_PORT_ERROR_MARKERS = ['hostname', '端口相同但绑定范围不同']

export function isSharedPortError(message) {
  const text = message || ''
  return SHARED_PORT_ERROR_MARKERS.some((marker) => text.includes(marker))
}
