package sharedport

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
)

// PutTLSOwner registers owner at address under sni, an exact TLS SNI match (never a wildcard — a TLS owner is
// always one specific name, e.g. a node's own relay name from pki.NodeName). It returns a net.Listener the
// caller drives with its own *http.Server, so it keeps full control of its own HTTP/2 settings, ConnContext, and
// handler (the relay port needs exactly this, see internal/overlay/relay.go); tlsConfig is used as-is to
// complete the handshake once dispatch.go has matched the connection's SNI to sni, so it must already be
// configured the way the owner wants (certificates, client auth, ALPN, and so on).
//
// Closing the returned listener unregisters the owner; if it was the address's last registration, the address is
// freed synchronously (see port.teardown), exactly like the relay port's drain freed its own listener before it
// started sharing addresses through this package.
func (r *Registry) PutTLSOwner(address, sni string, tlsConfig *tls.Config) (net.Listener, error) {
	name := normalizeHost(sni)
	if name == "" {
		return nil, fmt.Errorf("TLS 所有者名称不能为空")
	}
	p, err := r.acquirePort(address, "TLS 所有者 "+name)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("共享端口 %s 正在关闭,请重试", address)
	}
	current := p.routes.Load()
	if _, exists := current.tlsOwners[name]; exists {
		return nil, fmt.Errorf("地址 %s 已经注册了名称为 %q 的 TLS 所有者", address, sni)
	}
	entry := &tlsOwnerEntry{config: tlsConfig, listener: newVirtualListener(p.listener.Addr())}
	next := current.clone()
	next.tlsOwners[name] = entry
	p.routes.Store(next)
	return &tlsOwnerListener{virtualListener: entry.listener, port: p, sni: name}, nil
}

// tlsOwnerListener is the net.Listener PutTLSOwner hands its caller; closing it removes the owner's registration
// in addition to the usual virtualListener.Close behavior.
type tlsOwnerListener struct {
	*virtualListener
	port *port
	sni  string
	once sync.Once
}

func (l *tlsOwnerListener) Close() error {
	l.once.Do(func() {
		l.virtualListener.Close()
		l.port.removeTLSOwner(l.sni)
	})
	return nil
}

func (p *port) removeTLSOwner(sni string) {
	p.mu.Lock()
	current := p.routes.Load()
	if _, ok := current.tlsOwners[sni]; !ok {
		p.mu.Unlock()
		return
	}
	next := current.clone()
	delete(next.tlsOwners, sni)
	p.routes.Store(next)
	empty := next.occupants() == 0
	p.mu.Unlock()
	if empty {
		p.teardown()
	}
}

// PutDefaultTLSOwner registers owner as address's fallback for a TLS connection whose SNI — including no SNI at
// all — matched neither an exact TLS owner (PutTLSOwner) nor a TLS site's hostname (see dispatch.go's
// getConfigForClient for the full match order). Southbound is the first user: a node whose binary predates
// pinning the bootstrap SNI to pki.ControllerName (see pki.BootstrapClientConfig's doc comment) sends no SNI at
// all when its -controller URL names a literal IP address, or an arbitrary, non-matching one otherwise — a
// current node's bootstrap dial, by contrast, always sends pki.ControllerName as SNI regardless of the
// -controller URL's own host, so it reaches the exact TLS owner above instead; the default owner here exists for
// the older node, and for any other client that dials with no SNI on purpose. At most one default TLS owner may
// be registered per address at a time; tlsConfig is used as-is, exactly like PutTLSOwner's.
//
// Registering fails if address already has a TLS site with no configured hostname: that site currently answers
// every SNI the address's other TLS owners/sites do not claim (see siteTable.only), which would otherwise
// silently race the new default owner for the same fallback role. Once a default owner is registered, PutSite
// requires every TLS site sharing this address to have a hostname (mirroring how an exact TLS owner already
// does), so this conflict can only arise from registration order, not from a later PutSite call.
//
// Closing the returned listener unregisters the owner; if it was the address's last registration, the address is
// freed synchronously (see port.teardown), exactly like PutTLSOwner's.
func (r *Registry) PutDefaultTLSOwner(address string, tlsConfig *tls.Config) (net.Listener, error) {
	p, err := r.acquirePort(address, "默认 TLS 所有者")
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("共享端口 %s 正在关闭,请重试", address)
	}
	current := p.routes.Load()
	if current.defaultOwner != nil {
		return nil, fmt.Errorf("地址 %s 已经注册了默认 TLS 所有者", address)
	}
	if _, ok := current.tlsSites.only(); ok {
		return nil, fmt.Errorf("地址 %s 已有未配置 hostname 的 TLS 站点,请先为它配置 hostname 再注册默认 TLS 所有者", address)
	}
	entry := &tlsOwnerEntry{config: tlsConfig, listener: newVirtualListener(p.listener.Addr())}
	next := current.clone()
	next.defaultOwner = entry
	p.routes.Store(next)
	return &defaultTLSOwnerListener{virtualListener: entry.listener, port: p}, nil
}

// defaultTLSOwnerListener is the net.Listener PutDefaultTLSOwner hands its caller; closing it removes the
// default owner's registration in addition to the usual virtualListener.Close behavior.
type defaultTLSOwnerListener struct {
	*virtualListener
	port *port
	once sync.Once
}

func (l *defaultTLSOwnerListener) Close() error {
	l.once.Do(func() {
		l.virtualListener.Close()
		l.port.removeDefaultTLSOwner()
	})
	return nil
}

func (p *port) removeDefaultTLSOwner() {
	p.mu.Lock()
	current := p.routes.Load()
	if current.defaultOwner == nil {
		p.mu.Unlock()
		return
	}
	next := current.clone()
	next.defaultOwner = nil
	p.routes.Store(next)
	empty := next.occupants() == 0
	p.mu.Unlock()
	if empty {
		p.teardown()
	}
}
