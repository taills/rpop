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

	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// Engine runs the sites of one node.
type Engine struct {
	log *zap.Logger
	// opMu serializes Apply and Stop so each sees a consistent set of running sites.
	opMu      sync.Mutex
	mu        sync.Mutex
	runs      map[string]*running
	listeners map[string]*addressGroup
	// registry binds every site's listener; a private one (the default) means nothing outside this engine can
	// ever share one of its addresses. WithRegistry injects a shared one instead — the console's, in an
	// all-in-one/controller process's embedded local node (see cmd/rpop.runController), and, from stage two
	// onward, a node's own relay port.
	registry *sharedport.Registry
	// leaving holds, during Apply, the sites that move to another listener; admission on their old listener
	// ignores them so sites can trade listeners in one snapshot.
	leaving map[string]bool
	metrics sync.Map
	logs    *logQueue
	// paths dials upstream paths; guarded by mu.
	paths PathDialer
	// activeProbe is the engine-wide default for D19 active probing (D30); guarded by mu. SetPathActiveProbe
	// overrides it; an upstream's Failover.ActiveProbe overrides it again for that upstream alone.
	activeProbe bool
}

type running struct {
	groupKey       string
	route          *siteRuntime
	spec           snapshot.Site
	runtimeKey     string
	certificateKey string
}

// Option configures an Engine at construction time.
type Option func(*Engine)

// WithRegistry makes the engine bind every site's listener through registry instead of a private one it creates
// for itself. Sites behave identically either way; sharing a registry only means something else registered on
// it (the console's plaintext owner, or a node's relay TLS owner) may end up on the same address as one of this
// engine's sites, per internal/sharedport's admission rules.
func WithRegistry(registry *sharedport.Registry) Option {
	return func(e *Engine) { e.registry = registry }
}

// SetRegistry replaces the engine's shared-port registry. Call it before Apply ever runs: like
// SetAccessLogWriter, this is startup-time wiring, not something safe to change on a running engine (a site
// already bound through the previous registry would not move to the new one). WithRegistry is the same
// injection point at construction time; this exists for a caller that must build the engine before it has a
// registry to give it — internal/control.NewWithLogDir builds Control (and its embedded engine) first, then
// cmd/rpop wires in the registry it also gave the console (see Control.SetSharedPortRegistry).
func (e *Engine) SetRegistry(registry *sharedport.Registry) {
	e.registry = registry
}

// New creates an idle engine. Access logs go to the logger until SetAccessLogWriter provides a destination.
func New(log *zap.Logger, opts ...Option) *Engine {
	e := &Engine{
		log: log, runs: make(map[string]*running), listeners: make(map[string]*addressGroup),
		activeProbe: true, registry: sharedport.NewRegistry(),
	}
	for _, opt := range opts {
		opt(e)
	}
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

// Handler builds the proxy handler of a site without binding a listener. Nothing ever calls release() on this
// one-off runtime, so D19 active probing stays off for it regardless of engine/upstream settings: a timer with
// no owner to close it would keep re-arming itself past the caller's use of the handler.
func (e *Engine) Handler(site snapshot.Site) (http.Handler, error) {
	handler, _, _, err := e.siteHandler(site, false)
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
	for _, hostname := range hostnames {
		if pki.IsInternalHostname(hostname) {
			return nil, fmt.Errorf("站点 %s 的 hostname %q 是内部保留名(控制器或节点专用),请改用其他 hostname", site.ID, hostname)
		}
	}
	var certificate *tls.Certificate
	if site.TLS {
		if certificate, err = siteCertificate(site); err != nil {
			return nil, err
		}
	}
	handler, transports, pathGroups, err := e.siteHandler(site, true)
	if err != nil {
		return nil, err
	}
	route := &siteRuntime{id: site.ID, handler: handler, hostnames: hostnames, transports: transports, pathGroups: pathGroups}
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
	// A plaintext site and a TLS site are free to share one address (told apart by the connection's first byte,
	// exactly like a site and a TLS owner already were — see sharedport.Registry.PutSite, which keeps entirely
	// separate plaintext/TLS site tables per address): this engine used to forbid mixing two *sites*' modes on
	// one address regardless, a stricter rule than sharedport ever needed that predates shared ports existing at
	// all. It was relaxed once sharedport's first-byte dispatch made it unnecessary — see
	// docs/architecture/control-data-plane.md §5, "共享端口(第三段)".
	newGroup := group == nil
	// A site staying on this address but flipping its TLS bit needs its stale same-ID registration in the
	// *other* mode's table detached first: PutSite below only ever replaces the entry in the table matching the
	// *new* registration's mode (plainSites when !route.TLS, tlsSites when route.TLS — see sharedport's
	// route.go), so without this the previous mode's entry would linger forever, continuing to route traffic
	// through the runtime install releases below. This has to run before PutSite, not after — RemoveSite is
	// by-ID and would otherwise also delete the fresh entry PutSite is about to add.
	switchingMode := previous != nil && previous.groupKey == address && previous.spec.TLS != site.TLS
	if switchingMode {
		e.registry.RemoveSite(address, site.ID)
	}
	siteRoute := sharedport.SiteRoute{
		ID: site.ID, Hostnames: route.hostnames, TLS: site.TLS, Certificate: route.certificate.Load, Handler: route.handler,
	}
	// A brand-new address can never fail admission here (no owner, no other site yet to conflict with), so there
	// is nothing to unwind on error the way a direct net.Listen would have needed: PutSite only just bound the
	// address's listener when this call is what makes newGroup true, and it stays bound, owned by nothing, only
	// while admission is still being decided inside the same call — never past a failed return.
	if err := e.registry.PutSite(address, siteRoute, e.leaving); err != nil {
		if switchingMode {
			// The stale entry removed above was this site's only working registration (D8: a site that fails to
			// apply keeps serving its previous config); put it back under its old mode so the site does not go
			// dark just because the new mode couldn't be admitted. previous.route has not been release()d yet —
			// that only happens once install fully succeeds, below — so its handler and certificate are still
			// good to hand back to the registry.
			oldRoute := sharedport.SiteRoute{
				ID: site.ID, Hostnames: previous.route.hostnames, TLS: previous.spec.TLS,
				Certificate: previous.route.certificate.Load, Handler: previous.route.handler,
			}
			if restoreErr := e.registry.PutSite(address, oldRoute, e.leaving); restoreErr != nil {
				e.log.Error("could not restore previous registration after a failed TLS/plaintext switch",
					zap.String("site_id", site.ID), zap.Error(restoreErr))
			}
		}
		e.mu.Unlock()
		return err
	}
	if newGroup {
		group = &addressGroup{siteIDs: make(map[string]bool)}
		e.listeners[address] = group
	}
	group.siteIDs[site.ID] = true
	var retiredAddress string
	if previous != nil && previous.groupKey != address {
		if old := e.listeners[previous.groupKey]; old != nil {
			delete(old.siteIDs, site.ID)
			if len(old.siteIDs) == 0 {
				delete(e.listeners, previous.groupKey)
			}
		}
		retiredAddress = previous.groupKey
	}
	e.runs[site.ID] = &running{groupKey: address, route: route, spec: site, runtimeKey: runtimeKey, certificateKey: certificateKey}
	e.mu.Unlock()
	if retiredAddress != "" {
		// Synchronous, like the direct net.Listen this replaced: once RemoveSite returns, a site can rebind the
		// retired address immediately if the snapshot moves it right back (see TestSitesCanTradeListenersInOneApply).
		e.registry.RemoveSite(retiredAddress, site.ID)
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
	if group := e.listeners[run.groupKey]; group != nil {
		delete(group.siteIDs, id)
		if len(group.siteIDs) == 0 {
			delete(e.listeners, run.groupKey)
		}
	}
	e.mu.Unlock()
	e.registry.RemoveSite(run.groupKey, id)
	run.route.release()
	e.log.Info("site stopped", zap.String("site_id", id), zap.String("address", run.groupKey))
}
