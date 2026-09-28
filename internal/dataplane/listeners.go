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

// addressGroup tracks which sites currently share one address, so Engine.install/stopLocked know when the last
// of them leaves and the address can be forgotten from e.listeners; sharedport.Registry itself owns binding,
// admission, routing, and listener lifecycle. A plaintext site and a TLS site may freely belong to the same
// group — sharedport tells them apart by the first byte of each connection, exactly like it already does for a
// site sharing an address with a TLS owner (see engine.go's install and
// docs/architecture/control-data-plane.md §5, "共享端口(第三段)").
type addressGroup struct {
	siteIDs map[string]bool
}

// NormalizeHostnames re-exports sharedport.NormalizeHostnames: internal/control's pre-save validation and this
// engine's own site admission (via Registry.PutSite) must agree on exactly which hostnames are valid, so they
// share the one implementation instead of two copies that could drift.
func NormalizeHostnames(hostnames []string) ([]string, error) {
	return sharedport.NormalizeHostnames(hostnames)
}
