// Package dataplane is the forwarding engine of a node: it binds site listeners, routes requests to upstreams,
// and observes traffic. It is driven by snapshot specs and has no knowledge of where they come from.
package dataplane

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/snapshot"
)

// Engine runs the sites of one node.
type Engine struct {
	log *zap.Logger
	// opMu serializes Apply and Stop so each sees a consistent set of running sites.
	opMu      sync.Mutex
	mu        sync.Mutex
	runs      map[string]*running
	listeners map[string]*listenerGroup
	// leaving holds, during Apply, the sites that move to another listener; admission on their old listener
	// ignores them so sites can trade listeners in one snapshot.
	leaving map[string]bool
	metrics sync.Map
	logs    *logQueue
	// paths dials upstream paths; guarded by mu.
	paths PathDialer
}

type running struct {
	groupKey       string
	route          *siteRuntime
	spec           snapshot.Site
	runtimeKey     string
	certificateKey string
}

// New creates an idle engine. Access logs go to the logger until SetAccessLogWriter provides a destination.
func New(log *zap.Logger) *Engine {
	e := &Engine{log: log, runs: make(map[string]*running), listeners: make(map[string]*listenerGroup)}
	e.logs = newLogQueue(log, func(siteID string) { e.metricsFor(siteID).dropLog() })
	return e
}

// SetAccessLogWriter directs access logs to writer.
func (e *Engine) SetAccessLogWriter(writer AccessLogWriter) {
	e.logs.setWriter(writer)
}

// DrainAccessLogs waits until every access log produced before the call has been written.
func (e *Engine) DrainAccessLogs(ctx context.Context) error {
	return e.logs.drain(ctx)
}

// Apply makes the running sites match sites. Sites missing from the list stop; unchanged sites keep serving;
// a site whose only change is its certificate swaps it in place. Sites named in rebuild get a fresh runtime
// (new upstream transports) even when unchanged. A site that fails to apply keeps its previous runtime (or stays
// stopped), and its error is reported under its ID.
func (e *Engine) Apply(sites []snapshot.Site, rebuild ...string) map[string]error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	errs := make(map[string]error)
	desired := make(map[string]snapshot.Site, len(sites))
	for _, site := range sites {
		if _, duplicate := desired[site.ID]; duplicate {
			errs[site.ID] = fmt.Errorf("site %q is listed more than once", site.ID)
			continue
		}
		desired[site.ID] = site
	}
	for _, id := range e.RunningSites() {
		if _, keep := desired[id]; !keep {
			e.stopLocked(id)
		}
	}
	e.leaving = e.movingSites(desired)
	defer func() { e.leaving = nil }()
	pending := make([]string, 0, len(desired))
	for id := range desired {
		if errs[id] == nil {
			pending = append(pending, id)
		}
	}
	slices.Sort(pending)
	// A second pass lets sites that trade listeners or hostnames within one snapshot settle regardless of order.
	for pass := 0; pass < 2 && len(pending) > 0; pass++ {
		var failed []string
		for _, id := range pending {
			if err := e.applySiteLocked(desired[id], slices.Contains(rebuild, id)); err != nil {
				errs[id] = err
				failed = append(failed, id)
			} else {
				delete(errs, id)
			}
		}
		if len(failed) == len(pending) {
			break
		}
		pending = failed
	}
	return errs
}

func (e *Engine) movingSites(desired map[string]snapshot.Site) map[string]bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	moving := make(map[string]bool)
	for id, run := range e.runs {
		if site, ok := desired[id]; ok && listenAddress(site) != run.groupKey {
			moving[id] = true
		}
	}
	return moving
}

func listenAddress(site snapshot.Site) string {
	return net.JoinHostPort(site.ListenAddress, strconv.Itoa(site.ListenPort))
}

// Stop stops one site.
func (e *Engine) Stop(id string) {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.stopLocked(id)
}

// StopAll stops every site and releases every listener.
func (e *Engine) StopAll() {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	for _, id := range e.RunningSites() {
		e.stopLocked(id)
	}
}

// Running reports whether a site is serving.
func (e *Engine) Running(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runs[id] != nil
}

// RunningSites lists the IDs of serving sites in sorted order.
func (e *Engine) RunningSites() []string {
	e.mu.Lock()
	ids := make([]string, 0, len(e.runs))
	for id := range e.runs {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	slices.Sort(ids)
	return ids
}

// Spec returns the spec a site is currently serving.
func (e *Engine) Spec(id string) (snapshot.Site, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run := e.runs[id]
	if run == nil {
		return snapshot.Site{}, false
	}
	return run.spec, true
}

// Handler builds the proxy handler of a site without binding a listener.
func (e *Engine) Handler(site snapshot.Site) (http.Handler, error) {
	handler, _, err := e.siteHandler(site)
	return handler, err
}

func (e *Engine) applySiteLocked(site snapshot.Site, rebuild bool) error {
	runtimeKey, certificateKey := site.RuntimeKey(), site.CertificateKey()
	e.mu.Lock()
	run := e.runs[site.ID]
	e.mu.Unlock()
	if run != nil && run.runtimeKey == runtimeKey && !rebuild {
		if run.certificateKey == certificateKey {
			return nil
		}
		certificate, err := siteCertificate(site)
		if err != nil {
			return err
		}
		run.route.certificate.Store(certificate)
		e.mu.Lock()
		run.spec, run.certificateKey = site, certificateKey
		e.mu.Unlock()
		return nil
	}
	route, err := e.buildRuntime(site)
	if err != nil {
		return err
	}
	if err := e.install(site, route, runtimeKey, certificateKey); err != nil {
		route.release()
		return err
	}
	return nil
}

func (e *Engine) buildRuntime(site snapshot.Site) (*siteRuntime, error) {
	if site.ListenPort < 1 || site.ListenPort > 65535 {
		return nil, fmt.Errorf("listenPort must be 1-65535")
	}
	hostnames, err := NormalizeHostnames(site.Hostnames)
	if err != nil {
		return nil, err
	}
	var certificate *tls.Certificate
	if site.TLS {
		if certificate, err = siteCertificate(site); err != nil {
			return nil, err
		}
	}
	handler, transports, err := e.siteHandler(site)
	if err != nil {
		return nil, err
	}
	route := &siteRuntime{id: site.ID, handler: handler, hostnames: hostnames, transports: transports}
	route.certificate.Store(certificate)
	return route, nil
}

func siteCertificate(site snapshot.Site) (*tls.Certificate, error) {
	if !site.TLS {
		return nil, nil
	}
	if site.Certificate == nil {
		return nil, fmt.Errorf("site %q enables TLS without a certificate", site.ID)
	}
	certificate, err := tls.X509KeyPair([]byte(site.Certificate.CertificatePEM), []byte(site.Certificate.PrivateKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("site %q certificate: %w", site.ID, err)
	}
	return &certificate, nil
}

// install puts route in service. On the same listener the swap is atomic, so a reload drops no connections;
// in-flight requests finish on the runtime that accepted them.
func (e *Engine) install(site snapshot.Site, route *siteRuntime, runtimeKey, certificateKey string) error {
	address := listenAddress(site)
	e.mu.Lock()
	previous := e.runs[site.ID]
	group := e.listeners[address]
	if group != nil && group.tlsEnabled != site.TLS {
		// A bound listener cannot switch between HTTP and TLS; release it first when this site is its only user.
		if previous == nil || previous.groupKey != address || group.siteCount() != 1 {
			e.mu.Unlock()
			return fmt.Errorf("listener %s is already serving the other HTTP/TLS mode", address)
		}
		e.mu.Unlock()
		e.stopLocked(site.ID)
		e.mu.Lock()
		previous, group = nil, nil
	}
	newGroup := group == nil
	if newGroup {
		var err error
		if group, err = e.bindGroup(address, site.TLS); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	if err := group.admit(site.ID, route.hostnames, e.leaving); err != nil {
		e.mu.Unlock()
		if newGroup {
			_ = group.listener.Close()
		}
		return err
	}
	if newGroup {
		e.listeners[address] = group
	}
	group.put(route)
	var retired *listenerGroup
	if previous != nil && previous.groupKey != address {
		if old := e.listeners[previous.groupKey]; old != nil && old.remove(previous.route) {
			delete(e.listeners, previous.groupKey)
			retired = old
		}
	}
	e.runs[site.ID] = &running{groupKey: address, route: route, spec: site, runtimeKey: runtimeKey, certificateKey: certificateKey}
	e.mu.Unlock()
	if newGroup {
		go e.serveGroup(group)
	}
	if retired != nil {
		e.shutdownGroup(retired)
	}
	if previous != nil {
		previous.route.release()
	}
	e.log.Info("site started", zap.String("site_id", site.ID), zap.String("address", address), zap.Strings("hostnames", route.hostnames))
	return nil
}

func (e *Engine) stopLocked(id string) {
	e.mu.Lock()
	run := e.runs[id]
	if run == nil {
		e.mu.Unlock()
		return
	}
	delete(e.runs, id)
	group := e.listeners[run.groupKey]
	retire := group != nil && group.remove(run.route)
	if retire {
		delete(e.listeners, run.groupKey)
	}
	e.mu.Unlock()
	if retire {
		e.shutdownGroup(group)
	}
	run.route.release()
	e.log.Info("site stopped", zap.String("site_id", id), zap.String("address", run.groupKey))
}
