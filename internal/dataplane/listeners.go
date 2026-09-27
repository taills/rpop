package dataplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const (
	// HeaderTimeout bounds how long a client may take to send request headers.
	HeaderTimeout           = 8 * time.Second
	listenerIdleTimeout     = 60 * time.Second
	listenerShutdownTimeout = 5 * time.Second
)

// siteRuntime is the immutable serving state of one site version; reloads install a new one.
type siteRuntime struct {
	id          string
	handler     http.Handler
	hostnames   []string
	certificate atomic.Pointer[tls.Certificate]
	transports  []*http.Transport
}

// release closes idle upstream connections of a replaced runtime; in-flight requests keep theirs.
func (r *siteRuntime) release() {
	closeIdle(r.transports)
}

// listenerGroup is one bound address shared by sites that route by hostname.
type listenerGroup struct {
	mu         sync.RWMutex
	address    string
	tlsEnabled bool
	server     *http.Server
	listener   net.Listener
	routes     map[string]*siteRuntime
	byHost     map[string]*siteRuntime
}

func (e *Engine) bindGroup(address string, tlsEnabled bool) (*listenerGroup, error) {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	group := &listenerGroup{address: address, tlsEnabled: tlsEnabled, routes: make(map[string]*siteRuntime), byHost: make(map[string]*siteRuntime)}
	server := &http.Server{Handler: http.HandlerFunc(group.serve), ReadHeaderTimeout: HeaderTimeout, IdleTimeout: listenerIdleTimeout}
	if tlsEnabled {
		// Offering h2 lifts the browser limit of six HTTP/1.1 connections per origin, which otherwise starves
		// concurrent SSE streams. WebSockets stay on HTTP/1.1 because extended CONNECT is not advertised.
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1"}, GetCertificate: group.certificateForHello}
		ln = tls.NewListener(ln, server.TLSConfig)
	}
	group.server = server
	group.listener = ln
	return group, nil
}

// siteCount reports how many sites the listener routes to.
func (g *listenerGroup) siteCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.routes)
}

func (e *Engine) serveGroup(group *listenerGroup) {
	if err := group.server.Serve(group.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		e.log.Error("shared listener stopped", zap.String("address", group.address), zap.Error(err))
	}
}

func (e *Engine) shutdownGroup(group *listenerGroup) {
	ctx, cancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
	defer cancel()
	if err := group.server.Shutdown(ctx); err != nil {
		_ = group.server.Close()
		_ = group.listener.Close()
		e.log.Warn("shared listener shutdown timed out", zap.String("address", group.address), zap.Error(err))
	}
}

// admit reports whether site id may serve hostnames on this listener alongside the other sites; sites in
// ignore are about to leave it.
func (g *listenerGroup) admit(id string, hostnames []string, ignore map[string]bool) error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	others := 0
	for otherID, existing := range g.routes {
		if otherID == id || ignore[otherID] {
			continue
		}
		others++
		if len(hostnames) > 0 && len(existing.hostnames) == 0 {
			return fmt.Errorf("existing site %s has no hostnames; configure hostnames before sharing listener", existing.id)
		}
	}
	if len(hostnames) == 0 && others > 0 {
		return fmt.Errorf("hostnames are required when sharing listener %s", g.address)
	}
	for _, hostname := range hostnames {
		for existingName, existing := range g.byHost {
			if existing.id != id && !ignore[existing.id] && hostPatternsOverlap(hostname, existingName) {
				return fmt.Errorf("hostname %q overlaps a hostname assigned to site %s", hostname, existing.id)
			}
		}
	}
	return nil
}

// put installs route for its site, replacing the site's previous runtime on this listener.
func (g *listenerGroup) put(route *siteRuntime) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if previous := g.routes[route.id]; previous != nil {
		g.unmapLocked(previous)
	}
	g.routes[route.id] = route
	for _, hostname := range route.hostnames {
		g.byHost[hostname] = route
	}
}

// remove detaches route and reports whether the listener has no sites left.
func (g *listenerGroup) remove(route *siteRuntime) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.routes[route.id] == route {
		delete(g.routes, route.id)
		g.unmapLocked(route)
	}
	return len(g.routes) == 0
}

func (g *listenerGroup) unmapLocked(route *siteRuntime) {
	for _, hostname := range route.hostnames {
		if g.byHost[hostname] == route {
			delete(g.byHost, hostname)
		}
	}
}

func (g *listenerGroup) serve(w http.ResponseWriter, r *http.Request) {
	host := normalizeHost(r.Host)
	g.mu.RLock()
	route := g.findRoute(host)
	if route == nil && r.TLS != nil {
		route = g.findRoute(normalizeHost(r.TLS.ServerName))
	}
	if route == nil {
		route = g.onlyRoute()
	}
	g.mu.RUnlock()
	if route == nil {
		http.Error(w, "no site configured for this hostname", http.StatusMisdirectedRequest)
		return
	}
	route.handler.ServeHTTP(w, r)
}

func (g *listenerGroup) certificateForHello(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := normalizeHost(hello.ServerName)
	g.mu.RLock()
	route := g.findRoute(host)
	if route == nil {
		route = g.onlyRoute()
	}
	g.mu.RUnlock()
	if route == nil {
		return nil, fmt.Errorf("no TLS certificate configured for SNI %q", hello.ServerName)
	}
	certificate := route.certificate.Load()
	if certificate == nil {
		return nil, fmt.Errorf("no TLS certificate configured for SNI %q", hello.ServerName)
	}
	return certificate, nil
}

func (g *listenerGroup) onlyRoute() *siteRuntime {
	if len(g.routes) != 1 {
		return nil
	}
	for _, only := range g.routes {
		return only
	}
	return nil
}

func (g *listenerGroup) findRoute(host string) *siteRuntime {
	if host == "" {
		return nil
	}
	if route := g.byHost[host]; route != nil {
		return route
	}
	var found *siteRuntime
	longest := 0
	for pattern, route := range g.byHost {
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:]
			if strings.HasSuffix(host, suffix) && len(suffix) > longest {
				found = route
				longest = len(suffix)
			}
		}
	}
	return found
}

func hostPatternsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	aWildcard, bWildcard := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	if aWildcard && !bWildcard {
		return hostPatternMatches(a, b)
	}
	if bWildcard && !aWildcard {
		return hostPatternMatches(b, a)
	}
	if aWildcard && bWildcard {
		return strings.HasSuffix(a[1:], b[1:]) || strings.HasSuffix(b[1:], a[1:])
	}
	return false
}

func hostPatternMatches(pattern, host string) bool {
	if !strings.HasPrefix(pattern, "*.") {
		return pattern == host
	}
	suffix := pattern[1:]
	return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
}

// NormalizeHostnames lower-cases, validates, and de-duplicates site hostnames; "*.example.com" wildcards are allowed.
func NormalizeHostnames(hostnames []string) ([]string, error) {
	out := make([]string, 0, len(hostnames))
	seen := make(map[string]bool, len(hostnames))
	for _, name := range hostnames {
		name = normalizeHost(name)
		if !validHostname(name) {
			return nil, fmt.Errorf("invalid hostname %q", name)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	return strings.TrimSuffix(host, ".")
}

func validHostname(host string) bool {
	if host == "" || strings.ContainsAny(host, "/\\ \t\r\n") {
		return false
	}
	if strings.HasPrefix(host, "*.") {
		host = host[2:]
	} else if strings.Contains(host, "*") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}
