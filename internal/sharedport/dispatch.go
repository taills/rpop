package sharedport

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// PeekTimeout bounds how long sharedport waits for a connection's first byte before giving up on it. It matches
// HeaderTimeout: both bound the same kind of "client is slow to say anything at all" wait, one layer apart.
const PeekTimeout = HeaderTimeout

// HandshakeTimeout bounds a TLS handshake driven by the shared dispatch loop; it matches the header timeout the
// relay port used for its own listener before it started sharing addresses through this package.
const HandshakeTimeout = 10 * time.Second

// tlsRecordHeaderByte is the first byte of every TLS record (content type 22, "handshake"), which a ClientHello
// always starts with; RFC 8446 §5.1.
const tlsRecordHeaderByte = 0x16

// acceptLoop is the port's real accept loop: one per bound address, regardless of how many owners or sites share
// it. It hands each accepted connection to its own goroutine immediately, so one slow client peeking or
// handshaking never delays another's.
func (p *port) acceptLoop() {
	for {
		conn, err := p.listener.Accept()
		if err != nil {
			return
		}
		go p.dispatch(conn)
	}
}

// prefixConn replays bytes already read off the front of a connection before further reads reach it, so peeking
// a connection's first byte to classify it does not lose that byte for whoever ends up serving it.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(b)
}

// dispatch classifies one accepted connection by its first byte and routes it: 0x16 (a TLS ClientHello) goes
// through dispatchTLS, everything else through dispatchPlain. A connection that never sends a byte within
// PeekTimeout, or that io errors while doing so, is simply closed.
func (p *port) dispatch(conn net.Conn) {
	tuneAcceptedConn(conn)
	_ = conn.SetReadDeadline(time.Now().Add(PeekTimeout))
	first := make([]byte, 1)
	if _, err := io.ReadFull(conn, first); err != nil {
		conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	peeked := &prefixConn{Conn: conn, prefix: first}
	if first[0] == tlsRecordHeaderByte {
		p.dispatchTLS(peeked)
		return
	}
	p.dispatchPlain(peeked)
}

func (p *port) dispatchPlain(conn net.Conn) {
	if !p.plainListener.deliver(conn) {
		conn.Close()
	}
}

// tlsDispatchTarget carries the virtual listener a TLS connection's handshake selected (see getConfigForClient)
// back out to dispatchTLS, so the lookup that picks the tls.Config to handshake with and the lookup that picks
// where the completed connection goes are the same one lookup, not two.
type tlsDispatchTarget struct {
	listener *virtualListener
}

// dispatchTLS completes the TLS handshake itself (see the package doc comment for why: an owner's tls.Config,
// e.g. the relay port's mTLS, must never apply to a site's connection or vice versa, which requires picking the
// whole config, not just a certificate, before the handshake proceeds — GetConfigForClient is the one hook
// crypto/tls offers for that), then hands the completed *tls.Conn to whichever destination the client's SNI
// resolved to. A handshake failure (including "no TLS route matches this SNI", see getConfigForClient) simply
// closes the connection: RFC 8446 defines no graceful multiplexing failure response at this layer.
func (p *port) dispatchTLS(conn net.Conn) {
	var target tlsDispatchTarget
	tlsConn := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: p.getConfigForClient(&target)})
	ctx, cancel := context.WithTimeout(context.Background(), HandshakeTimeout)
	defer cancel()
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return
	}
	if target.listener == nil || !target.listener.deliver(tlsConn) {
		tlsConn.Close()
	}
}

// getConfigForClient is the port's TLS route selector. An exact SNI match against a registered owner wins first;
// owner names and site hostnames never overlap (the control plane rejects a site hostname in the internal
// namespace an owner's name lives in), so there is never an ambiguous case to break a tie for. Otherwise the
// port's shared TLS-sites config decides by the same hostname/wildcard rules dispatchPlain's Host routing uses.
// Recording the chosen destination in target lets dispatchTLS avoid repeating this lookup.
func (p *port) getConfigForClient(target *tlsDispatchTarget) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		routes := p.routes.Load()
		host := normalizeHost(hello.ServerName)
		if owner, ok := routes.tlsOwners[host]; ok {
			target.listener = owner.listener
			return owner.config, nil
		}
		if routes.tlsSites.len() == 0 {
			return nil, fmt.Errorf("没有为 SNI %q 配置的 TLS 证书", hello.ServerName)
		}
		target.listener = p.tlsListener
		return p.tlsSiteConfig(), nil
	}
}

// tlsSiteConfig is the shared tls.Config every TLS site on this port handshakes with: GetCertificate re-reads
// p.routes on every handshake (not just once per config), so a site added, removed, or given a new certificate
// after this config was first built is picked up without rebuilding it.
func (p *port) tlsSiteConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Offering h2 lifts the browser limit of six HTTP/1.1 connections per origin, which otherwise starves
		// concurrent SSE streams (P3). WebSockets stay on HTTP/1.1 because extended CONNECT is not advertised.
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			sites := p.routes.Load().tlsSites
			route, ok := sites.findByHost(normalizeHost(hello.ServerName))
			if !ok {
				route, ok = sites.only()
			}
			if !ok || route.Certificate == nil {
				return nil, fmt.Errorf("没有为 SNI %q 配置的 TLS 证书", hello.ServerName)
			}
			cert := route.Certificate()
			if cert == nil {
				return nil, fmt.Errorf("没有为 SNI %q 配置的 TLS 证书", hello.ServerName)
			}
			return cert, nil
		},
	}
}

// servePlain is the shared plaintext meta server's handler for this port: a site match by Host wins first (or
// the port's lone plaintext site, if it has no configured hostname, exactly as before sites shared a registry),
// then the plaintext owner (if any), then a plain 404 (or, matching this port's previous behavior when there was
// never an owner concept at all, 421 Misdirected Request when multiple sites share the address and none match).
func (p *port) servePlain(w http.ResponseWriter, r *http.Request) {
	routes := p.routes.Load()
	host := normalizeHost(r.Host)
	route, ok := routes.plainSites.findByHost(host)
	if !ok {
		route, ok = routes.plainSites.only()
	}
	if ok {
		route.Handler.ServeHTTP(w, r)
		return
	}
	if routes.plainOwner != nil {
		if routes.plainOwner.claims(host) {
			routes.plainOwner.handler.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	http.Error(w, "no site configured for this hostname", http.StatusMisdirectedRequest)
}

// serveTLSSite is the shared TLS-sites meta server's handler: reached only once a connection's handshake already
// selected this port's tlsSiteConfig (see getConfigForClient), so every connection here is a site's, never an
// owner's. It tries the Host header first, then the SNI the client handshaked with, then the port's lone TLS
// site if it has no configured hostname — the same order dataplane's per-address listener used before TLS sites
// shared a registry.
func (p *port) serveTLSSite(w http.ResponseWriter, r *http.Request) {
	routes := p.routes.Load()
	route, ok := routes.tlsSites.findByHost(normalizeHost(r.Host))
	if !ok && r.TLS != nil {
		route, ok = routes.tlsSites.findByHost(normalizeHost(r.TLS.ServerName))
	}
	if !ok {
		route, ok = routes.tlsSites.only()
	}
	if !ok {
		http.Error(w, "no site configured for this hostname", http.StatusMisdirectedRequest)
		return
	}
	route.Handler.ServeHTTP(w, r)
}
