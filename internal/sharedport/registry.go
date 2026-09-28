package sharedport

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// HeaderTimeout bounds how long a client may take to send request headers, for the meta *http.Server this
// package runs per shared address (plaintext sites+owner, and TLS sites). It is the same value
// dataplane.HeaderTimeout/control.HeaderTimeout already used before those servers existed, kept here as the one
// definition both packages now share (dataplane re-exports it; see internal/dataplane/listeners.go).
const HeaderTimeout = 8 * time.Second

// siteIdleTimeout bounds how long a keep-alive connection may sit idle between requests, matching the value
// dataplane's per-address listener used before sites were routed through this package.
const siteIdleTimeout = 60 * time.Second

// metaShutdownTimeout bounds how long this package waits for a shared meta server to drain gracefully once the
// last thing registered on its address goes away, before force-closing it; matches dataplane's previous
// per-listener shutdown timeout.
const metaShutdownTimeout = 5 * time.Second

// Registry is a process-wide table of shared TCP listeners keyed by normalized address (see NormalizeAddress).
// The zero value is not usable; construct one with NewRegistry. A Registry is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	ports map[string]*port
}

// NewRegistry creates an empty registry. Most processes need exactly one, shared by everything in it that binds a
// TCP listener a site, the console, or a node's relay port might need to share (see the package doc comment).
func NewRegistry() *Registry {
	return &Registry{ports: make(map[string]*port)}
}

// port is one bound TCP listener and everything registered to share it.
type port struct {
	registry *Registry
	address  string // normalized key; also used as the registry's map key
	listener net.Listener

	// routes is this port's complete routing state, copy-on-write and swapped atomically so dispatching a
	// connection (acceptLoop/dispatch, in dispatch.go) never takes a lock.
	routes atomic.Pointer[routeTable]

	// mu serializes Put*/Remove* calls on this port; it is never held while a connection is being dispatched or
	// routed, only while the (cheap, infrequent) registration tables are read, copied, and swapped.
	mu     sync.Mutex
	closed bool

	// plainListener/plainServer and tlsListener/tlsServer are this port's own meta servers: one shared
	// *http.Server per encoding that every site of that encoding is dispatched into (mirroring how multiple sites
	// already shared one *http.Server before this package existed), plus, for plaintext, the fallback owner.
	// Neither sets http.Server.TLSConfig: plainServer never sees TLS at all, and tlsServer only ever receives
	// connections whose handshake dispatch.go has already completed, so leaving TLSConfig nil keeps Go's
	// automatic HTTP/2 negotiation working (see (*http.Server).shouldConfigureHTTP2ForServe: it configures h2
	// whenever TLSConfig is nil, precisely for a caller that terminated TLS itself before calling Serve).
	plainListener *virtualListener
	plainServer   *http.Server
	tlsListener   *virtualListener
	tlsServer     *http.Server
}

func newPort(r *Registry, address string, ln net.Listener) *port {
	p := &port{registry: r, address: address, listener: ln}
	p.routes.Store(emptyRouteTable())
	p.plainListener = newVirtualListener(ln.Addr())
	p.plainServer = &http.Server{Handler: http.HandlerFunc(p.servePlain), ReadHeaderTimeout: HeaderTimeout, IdleTimeout: siteIdleTimeout}
	p.tlsListener = newVirtualListener(ln.Addr())
	p.tlsServer = &http.Server{Handler: http.HandlerFunc(p.serveTLSSite), ReadHeaderTimeout: HeaderTimeout, IdleTimeout: siteIdleTimeout}
	go func() { _ = p.plainServer.Serve(p.plainListener) }()
	go func() { _ = p.tlsServer.Serve(p.tlsListener) }()
	go p.acceptLoop()
	return p
}

// acquirePort returns the port already bound at address, or binds a fresh one. use briefly names what is
// registering (e.g. "控制台", "站点 web", "TLS 所有者 edge-1.nodes.rpop") and is only ever used to build a clear
// error when address's port turns out to already be bound under a different, conflicting normalization (see
// Classify) — binding that at the OS level would otherwise fail with a bare "address already in use" that does
// not say which of the process's own addresses is the problem or how to fix it.
func (r *Registry) acquirePort(address, use string) (*port, error) {
	key, dial, err := NormalizeAddress(address)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.ports[key]; ok {
		return p, nil
	}
	if existingKey, existingUse := r.conflictLocked(key); existingKey != "" {
		return nil, ConflictError(displayAddress(key), use, displayAddress(existingKey), existingUse)
	}
	ln, err := net.Listen("tcp", dial)
	if err != nil {
		return nil, err
	}
	p := newPort(r, key, ln)
	r.ports[key] = p
	return p, nil
}

// conflictLocked scans already-bound ports for one whose normalized key conflicts with key (same port, but one
// wildcard and one specific — see Classify) and returns that port's key and a description of what is registered
// on it, so acquirePort can report a clear error before ever attempting a bind the OS would refuse. Called with
// r.mu held.
func (r *Registry) conflictLocked(key string) (existingKey, use string) {
	for candidateKey, p := range r.ports {
		if candidateKey == key {
			continue
		}
		if relation, err := Classify(candidateKey, key); err == nil && relation == Conflicting {
			return candidateKey, p.describeLocked()
		}
	}
	return "", ""
}

// displayAddress renders a normalized key ("*:8080") the way an operator actually wrote a wildcard address
// ("0.0.0.0:8080" or ":8080"), for error messages; NormalizeAddress's own "*:port" spelling is otherwise never
// something a caller typed.
func displayAddress(key string) string {
	if rest, ok := strings.CutPrefix(key, "*:"); ok {
		return ":" + rest
	}
	return key
}

// describeLocked summarizes what is currently registered on p, for the conflict error acquirePort builds when a
// different address's port collides with p's. Reads p.routes without p.mu: this only ever runs while the
// registry's own mu is held (from conflictLocked, itself called from another port's acquirePort), so it never
// races p's own registration calls for the address this describes, only for the *new* one still being decided.
func (p *port) describeLocked() string {
	table := p.routes.Load()
	var uses []string
	if table.plainOwner != nil {
		uses = append(uses, "控制台")
	}
	for id := range table.plainSites.byID {
		uses = append(uses, "站点 "+id)
	}
	for id := range table.tlsSites.byID {
		uses = append(uses, "站点 "+id)
	}
	for name := range table.tlsOwners {
		uses = append(uses, "TLS 所有者 "+name)
	}
	if table.defaultOwner != nil {
		uses = append(uses, "默认 TLS 所有者")
	}
	if len(uses) == 0 {
		return "另一个用途"
	}
	sort.Strings(uses)
	return strings.Join(uses, "、")
}

// lookupPort returns the port already bound at address, if any, without binding one.
func (r *Registry) lookupPort(address string) *port {
	key, _, err := NormalizeAddress(address)
	if err != nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ports[key]
}

// teardown closes p's real listener and, once nothing is registered on it, removes it from the registry and lets
// its meta servers drain. Called with p.mu NOT held; safe to call more than once.
func (p *port) teardown() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	p.registry.mu.Lock()
	if p.registry.ports[p.address] == p {
		delete(p.registry.ports, p.address)
	}
	p.registry.mu.Unlock()

	// Closing the real listener is synchronous and immediate, so a fresh registration (even a wholly unrelated
	// one) can bind the same address again right after this call returns, exactly like the relay port guaranteed
	// before it started sharing listeners through this package (see relay.go's drain, which this mirrors).
	_ = p.listener.Close()
	p.plainListener.Close()
	p.tlsListener.Close()
	go shutdownServer(p.plainServer)
	go shutdownServer(p.tlsServer)
}

func shutdownServer(s *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), metaShutdownTimeout)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		_ = s.Close()
	}
}

// PutSite registers or replaces one site's claim on address. hostnames must already be normalized (see
// NormalizeHostnames) and non-empty whenever the site will end up sharing the address with anything else — a
// second site, or a plaintext/TLS owner; ignore lists site IDs that are mid-move to a different address within
// the same batch of changes (see dataplane's "leaving" set) so they do not count against the "hostname required
// to share" and hostname-overlap checks.
func (r *Registry) PutSite(address string, route SiteRoute, ignore map[string]bool) error {
	if route.TLS && route.Certificate == nil {
		return fmt.Errorf("站点 %s 启用了 TLS 但未提供证书", route.ID)
	}
	p, err := r.acquirePort(address, "站点 "+route.ID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("共享端口 %s 正在关闭,请重试", address)
	}
	current := p.routes.Load()
	table, hasOwner := current.plainSites, current.plainOwner != nil
	if route.TLS {
		// A default TLS owner (PutDefaultTLSOwner) claims every SNI an exact owner or a site's own hostname does
		// not, exactly like siteTable.only's "lone site with no hostname" fallback does when there is no owner
		// at all — so once one is registered, a TLS site sharing this address must have a hostname of its own,
		// the same requirement an exact TLS owner already imposes.
		table, hasOwner = current.tlsSites, len(current.tlsOwners) > 0 || current.defaultOwner != nil
	}
	if err := table.admit(route.ID, route.Hostnames, hasOwner, ignore); err != nil {
		return err
	}
	if !route.TLS {
		if err := checkPlainOwnerOverlap(current.plainOwner, route.ID, route.Hostnames); err != nil {
			return err
		}
	}
	next := current.clone()
	if route.TLS {
		next.tlsSites = table.put(route)
	} else {
		next.plainSites = table.put(route)
	}
	p.routes.Store(next)
	return nil
}

// RemoveSite detaches a site's registration at address, if any. A no-op if the site or the address is not
// registered. Once the address has nothing registered on it at all, its listener is freed synchronously (see
// port.teardown).
func (r *Registry) RemoveSite(address, id string) {
	p := r.lookupPort(address)
	if p == nil {
		return
	}
	p.mu.Lock()
	current := p.routes.Load()
	next := current.clone()
	changed := false
	if _, ok := current.plainSites.byID[id]; ok {
		next.plainSites = current.plainSites.remove(id)
		changed = true
	}
	if _, ok := current.tlsSites.byID[id]; ok {
		next.tlsSites = current.tlsSites.remove(id)
		changed = true
	}
	if !changed {
		p.mu.Unlock()
		return
	}
	p.routes.Store(next)
	empty := next.occupants() == 0
	p.mu.Unlock()
	if empty {
		p.teardown()
	}
}

// PutPlaintextOwner installs handler as address's fallback for plaintext requests its sites do not claim (the
// console). hostnames, once normalized, restricts which Host headers reach it (see -console-hostnames); empty
// means "every Host the sites did not claim" (the default, and the only behavior before console-hostnames
// existed). At most one plaintext owner may be registered per address at a time.
func (r *Registry) PutPlaintextOwner(address string, handler http.Handler, hostnames []string) error {
	p, err := r.acquirePort(address, "控制台")
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("共享端口 %s 正在关闭,请重试", address)
	}
	current := p.routes.Load()
	if current.plainOwner != nil {
		return fmt.Errorf("地址 %s 已经注册了明文所有者", address)
	}
	normalized := make([]string, len(hostnames))
	names := make(map[string]bool, len(hostnames))
	for i, h := range hostnames {
		normalized[i] = normalizeHost(h)
		names[normalized[i]] = true
	}
	if err := checkOwnerSiteOverlap(current.plainSites, normalized); err != nil {
		return err
	}
	next := current.clone()
	next.plainOwner = &plaintextOwner{handler: handler, hostnames: names}
	p.routes.Store(next)
	return nil
}

// RemovePlaintextOwner detaches address's plaintext owner, if any.
func (r *Registry) RemovePlaintextOwner(address string) {
	p := r.lookupPort(address)
	if p == nil {
		return
	}
	p.mu.Lock()
	current := p.routes.Load()
	if current.plainOwner == nil {
		p.mu.Unlock()
		return
	}
	next := current.clone()
	next.plainOwner = nil
	p.routes.Store(next)
	empty := next.occupants() == 0
	p.mu.Unlock()
	if empty {
		p.teardown()
	}
}
