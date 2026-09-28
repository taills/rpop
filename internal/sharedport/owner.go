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
	p, err := r.acquirePort(address)
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
