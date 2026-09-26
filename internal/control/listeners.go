package control

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

func (c *Control) startLocked(ctx context.Context, id string) error {
	site, err := c.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := validate(site); err != nil {
		return err
	}
	if err := c.validateAccessLogAdapter(site); err != nil {
		return err
	}
	if err := c.stopLocked(id); err != nil {
		return err
	}

	handler, err := c.proxyHandler(ctx, id, site.Config)
	if err != nil {
		return err
	}
	var certificate *tls.Certificate
	if site.Config.TLS {
		certPEM, err := c.store.Secret(ctx, id, site.Config.CertificateSecret)
		if err != nil {
			return err
		}
		keyPEM, err := c.store.Secret(ctx, id, site.Config.PrivateKeySecret)
		if err != nil {
			return err
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return err
		}
		certificate = &cert
	}
	hostnames, err := normalizedHostnames(site.Config.Hostnames)
	if err != nil {
		return err
	}
	address := net.JoinHostPort(site.Config.ListenAddress, fmt.Sprint(site.Config.ListenPort))
	key := address
	route := &siteRuntime{id: id, handler: handler, hostnames: hostnames, certificate: certificate}

	c.mu.Lock()
	group := c.listeners[key]
	newGroup := group == nil
	if group != nil && group.tlsEnabled != site.Config.TLS {
		c.mu.Unlock()
		return fmt.Errorf("listener %s is already serving the other HTTP/TLS mode", address)
	}
	if group == nil {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			c.mu.Unlock()
			return err
		}
		group = &listenerGroup{key: key, address: address, tlsEnabled: site.Config.TLS, routes: make(map[string]*siteRuntime), byHost: make(map[string]*siteRuntime)}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { c.serveGroup(group, w, r) }), ReadHeaderTimeout: HeaderTimeout, IdleTimeout: 60 * time.Second}
		if site.Config.TLS {
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) { return c.certificateForHello(group, hello) }}
			ln = tls.NewListener(ln, server.TLSConfig)
		}
		group.server = server
		group.listener = ln
		c.listeners[key] = group
	}
	group.mu.Lock()
	if len(hostnames) == 0 && len(group.routes) > 0 {
		if newGroup {
			delete(c.listeners, key)
			_ = group.listener.Close()
		}
		group.mu.Unlock()
		c.mu.Unlock()
		return fmt.Errorf("hostnames are required when sharing listener %s", address)
	}
	if len(hostnames) > 0 {
		for _, existing := range group.routes {
			if len(existing.hostnames) == 0 {
				if newGroup {
					delete(c.listeners, key)
					_ = group.listener.Close()
				}
				group.mu.Unlock()
				c.mu.Unlock()
				return fmt.Errorf("existing site %s has no hostnames; configure hostnames before sharing listener", existing.id)
			}
		}
		for _, hostname := range hostnames {
			for existingName, existing := range group.byHost {
				if hostPatternsOverlap(hostname, existingName) {
					if newGroup {
						delete(c.listeners, key)
						_ = group.listener.Close()
					}
					group.mu.Unlock()
					c.mu.Unlock()
					return fmt.Errorf("hostname %q overlaps a hostname assigned to site %s", hostname, existing.id)
				}
			}
		}
	}
	group.routes[id] = route
	for _, hostname := range hostnames {
		group.byHost[hostname] = route
	}
	group.mu.Unlock()
	c.runs[id] = &running{groupKey: key, route: route}
	c.mu.Unlock()

	if newGroup {
		go func() {
			if err := group.server.Serve(group.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				c.log.Error("shared listener stopped", zap.String("address", group.address), zap.Error(err))
			}
		}()
	}
	c.log.Info("site started", zap.String("site_id", id), zap.String("address", address), zap.Strings("hostnames", hostnames))
	return nil
}

func (c *Control) stopLocked(id string) error {
	c.mu.Lock()
	run := c.runs[id]
	if run == nil {
		c.mu.Unlock()
		return nil
	}
	delete(c.runs, id)
	group := c.listeners[run.groupKey]
	if group == nil {
		c.mu.Unlock()
		return nil
	}
	group.mu.Lock()
	delete(group.routes, id)
	for _, hostname := range run.route.hostnames {
		if group.byHost[hostname] == run.route {
			delete(group.byHost, hostname)
		}
	}
	last := len(group.routes) == 0
	if last {
		delete(c.listeners, run.groupKey)
	}
	group.mu.Unlock()
	c.mu.Unlock()
	if last {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := group.server.Shutdown(ctx); err != nil {
			_ = group.server.Close()
			_ = group.listener.Close()
			c.log.Warn("shared listener shutdown timed out", zap.String("address", group.address), zap.Error(err))
		}
	}
	c.log.Info("site stopped", zap.String("site_id", id), zap.String("address", group.address))
	return nil
}

func (c *Control) serveGroup(group *listenerGroup, w http.ResponseWriter, r *http.Request) {
	host := normalizeHost(r.Host)
	group.mu.RLock()
	route := findHostRoute(group, host)
	if route == nil && r.TLS != nil {
		route = findHostRoute(group, normalizeHost(r.TLS.ServerName))
	}
	if route == nil && len(group.routes) == 1 {
		for _, only := range group.routes {
			route = only
		}
	}
	group.mu.RUnlock()
	if route == nil {
		http.Error(w, "no site configured for this hostname", http.StatusMisdirectedRequest)
		return
	}
	route.handler.ServeHTTP(w, r)
}

func (c *Control) certificateForHello(group *listenerGroup, hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := normalizeHost(hello.ServerName)
	group.mu.RLock()
	route := findHostRoute(group, host)
	if route == nil && len(group.routes) == 1 {
		for _, only := range group.routes {
			route = only
		}
	}
	group.mu.RUnlock()
	if route == nil || route.certificate == nil {
		return nil, fmt.Errorf("no TLS certificate configured for SNI %q", hello.ServerName)
	}
	return route.certificate, nil
}

func findHostRoute(group *listenerGroup, host string) *siteRuntime {
	if host == "" {
		return nil
	}
	if route := group.byHost[host]; route != nil {
		return route
	}
	var found *siteRuntime
	longest := 0
	for pattern, route := range group.byHost {
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

func normalizedHostnames(hostnames []string) ([]string, error) {
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
