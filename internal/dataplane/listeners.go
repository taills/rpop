package dataplane

import (
	"crypto/tls"
	"net/http"
	"sync/atomic"

	"github.com/rpop-project/rpop/internal/sharedport"
)

// HeaderTimeout bounds how long a client may take to send request headers. It re-exports sharedport.HeaderTimeout:
// every HTTP server behind a shared address (a site, the console, a future relay TLS owner) now uses that one
// definition, and internal/control.HeaderTimeout re-exports this one in turn, so all three stay equal by
// construction rather than by three separately maintained literals.
const HeaderTimeout = sharedport.HeaderTimeout

// siteRuntime is the immutable serving state of one site version; reloads install a new one.
type siteRuntime struct {
	id          string
	handler     http.Handler
	hostnames   []string
	certificate atomic.Pointer[tls.Certificate]
	transports  []*http.Transport
	// pathGroups lists the upstreams that have candidate paths (D18), for PathHealth; nil for a site whose
	// upstreams all dial a single direct or proxied target with no failover.
	pathGroups []upstreamPathGroup
}

// release closes idle upstream connections of a replaced runtime and stops every path's pending D19 probe timer
// (see pathTransport.closeProbe); in-flight requests keep their connections.
func (r *siteRuntime) release() {
	closeIdle(r.transports)
	for _, group := range r.pathGroups {
		group.failover.closeProbes()
	}
}

// addressGroup tracks, per shared address, the one thing this engine must still enforce that sharedport itself
// does not: a site's listener has never been allowed to mix plaintext and TLS on the same address (sharedport's
// registry, by contrast, happily lets a plaintext owner/site and a TLS owner/site share one address — that is
// the entire point of the package — because it tells them apart by the first byte of each connection before
// either one is reached). Preserving the stricter, pre-existing site-to-site rule exactly is what
// Engine.install's mode-conflict check (and this type) is for; the registry does everything else (binding,
// admission, routing, listener lifecycle).
type addressGroup struct {
	tlsEnabled bool
	siteIDs    map[string]bool
}

// NormalizeHostnames re-exports sharedport.NormalizeHostnames: internal/control's pre-save validation and this
// engine's own site admission (via Registry.PutSite) must agree on exactly which hostnames are valid, so they
// share the one implementation instead of two copies that could drift.
func NormalizeHostnames(hostnames []string) ([]string, error) {
	return sharedport.NormalizeHostnames(hostnames)
}
