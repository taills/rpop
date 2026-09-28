package control

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gopkg.in/yaml.v3"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/pki"
	"github.com/rpop-project/rpop/internal/routing"
	"github.com/rpop-project/rpop/internal/sharedport"
	"github.com/rpop-project/rpop/internal/store"
)

type Control struct {
	store  *store.Store
	log    *zap.Logger
	engine *dataplane.Engine
	// opMu serializes configuration changes; desired is the set of sites that should run and is guarded by it.
	opMu      sync.Mutex
	desired   map[string]bool
	published *publication
	// embedded reports whether this process also runs the "local" data-plane node; guarded by opMu, like the
	// revision and per-site errors of the last snapshot that node applied.
	embedded      bool
	localRevision int64
	localErrors   map[string]string
	nodes         *nodeRegistry
	caMu          sync.Mutex
	ca            *pki.CA
	registrations *failureLimiter
	// logIngestLocks serializes concurrent log segment uploads from the same node (D24), so two requests for the
	// same node can never race to read-then-advance its high-water mark.
	logIngestLocks *keyedMutex
	// logIngestSemaphore bounds how many log segment uploads southboundLogs processes at once, across every
	// node (stage 5 security review item 5); see DefaultMaxConcurrentLogIngests.
	logIngestSemaphore chan struct{}
	// logIngestRate bounds how fast southboundLogs accepts compressed bytes from any single node (item 5); see
	// DefaultLogIngestRateBytesPerSecond.
	logIngestRate *nodeRateLimiter
	proxies       proxyRegistry
	// overlay carries the embedded node's upstream paths across other nodes; it is created, under opMu, when a
	// site on that node first needs it.
	overlay *localOverlay
	// overlayConfig tunes the HTTP/2 window and per-connection stream limits the embedded node's overlay is built
	// with (D31, see SetOverlayConfig); read under opMu when newLocalOverlay creates that overlay, the same lock
	// that guards overlay itself.
	overlayConfig overlay.Config
	// clockSkewWarnThresholdMillis is nodeView/topologyAPI's cutoff (D28, see SetClockSkewWarnThreshold and
	// DefaultClockSkewWarnThresholdMillis) for marking a node's reported clock skew "warn" instead of "ok". It is
	// its own atomic rather than another opMu-guarded setting like overlayConfig: clockSkewView reads it from
	// nodeView/topologyNode/tunnelEventViews, which run both with and without opMu already held by their caller
	// (nodeAPI holds opMu for the whole handler around GET/PUT/POST /api/nodes/{id}, then calls nodeView itself) —
	// sync.Mutex is not reentrant, so a second Lock from inside that call would deadlock the request (and, since
	// opMu also serializes every other control-plane operation, every other request as well). See stage 7 review.
	clockSkewWarnThresholdMillis atomic.Int64

	accessLogs *accesslog.Registry
	// tunnelEvents stores overlay.TunnelEvent records reported by every node (D22); nil unless the controller
	// was built with NewWithLogDir, like accessLogs.
	tunnelEvents     *tunnelEventStore
	systemSettingsMu sync.RWMutex
	systemSettings   systemSettings
	authMu           sync.Mutex
	setupMu          sync.Mutex
	sessions         map[string]time.Time
	loginAttempts    map[string]loginAttempt
}
type apiError struct {
	Error string `json:"error"`
}

func New(s *store.Store, l *zap.Logger) *Control {
	c := &Control{store: s, log: l, engine: dataplane.New(l), desired: make(map[string]bool), published: newPublication(),
		embedded: true, nodes: newNodeRegistry(), registrations: newFailureLimiter(maxRegisterFailures, registerFailureWindow),
		logIngestLocks: newKeyedMutex(), logIngestSemaphore: make(chan struct{}, DefaultMaxConcurrentLogIngests),
		logIngestRate: newNodeRateLimiter(DefaultLogIngestRateBytesPerSecond), overlayConfig: overlay.DefaultConfig(),
		systemSettings: defaultSystemSettings(), sessions: make(map[string]time.Time), loginAttempts: make(map[string]loginAttempt)}
	c.clockSkewWarnThresholdMillis.Store(DefaultClockSkewWarnThresholdMillis)
	return c
}

// SetOverlayConfig overrides the HTTP/2 window and per-connection stream limits the embedded node's overlay is
// built with (D31, see overlay.DefaultConfig). Call before serving southbound traffic; the caller (cmd/rpop) is
// responsible for validating cfg with overlay.ValidateConfig first, so this only ever stores an already-valid
// value. A no-op on an overlay already created — like SetEmbeddedNode, this is a startup-time setting, not one
// that changes a running embedded node.
func (c *Control) SetOverlayConfig(cfg overlay.Config) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.overlayConfig = cfg
}

// SetSharedPortRegistry gives the embedded local node's engine the same internal/sharedport.Registry the
// console's own listener binds through (see cmd/rpop.runController and dataplane.Engine.SetRegistry), so an
// embedded site can share -addr with the console (told apart by hostname) instead of needing its own port.
// Call before StartAutoSites: like SetOverlayConfig, this is startup-time wiring, not something that moves
// already-running sites off the engine's previous (private) registry.
func (c *Control) SetSharedPortRegistry(registry *sharedport.Registry) {
	c.engine.SetRegistry(registry)
}

// SetPathActiveProbe overrides the engine-wide default for D19 active probing (D30, enabled by default; see
// dataplane.Engine.SetPathActiveProbe) on the embedded node's own engine; an upstream's Failover.ActiveProbe
// still overrides this again for that upstream alone. Unlike SetOverlayConfig this is not a startup-only
// setting: Engine.SetPathActiveProbe only documents "affects sites built or rebuilt afterward," not "call before
// Apply," so calling this later is safe, just not retroactive for already-running sites.
func (c *Control) SetPathActiveProbe(enabled bool) {
	c.engine.SetPathActiveProbe(enabled)
}

// SetClockSkewWarnThreshold overrides nodeView/topologyAPI's cutoff (in milliseconds) for marking a node's
// reported clock skew (D28) "warn" instead of "ok" (see DefaultClockSkewWarnThresholdMillis); ms <= 0 leaves the
// default in place. clockSkewWarnThresholdMillis is its own atomic (see its doc comment), so unlike
// SetOverlayConfig this needs no opMu and is safe to call at any time, not just before serving console traffic.
func (c *Control) SetClockSkewWarnThreshold(ms int64) {
	if ms > 0 {
		c.clockSkewWarnThresholdMillis.Store(ms)
	}
}

// SetLogIngestLimits overrides the default global concurrency cap and per-node upload rate cap for log segment
// ingest (stage 5 security review item 5, see DefaultMaxConcurrentLogIngests and
// DefaultLogIngestRateBytesPerSecond). Call before serving southbound traffic; maxConcurrent <= 0 or
// rateBytesPerSecond <= 0 leaves the corresponding default in place. rateBytesPerSecond only ever lowers or
// raises the steady-state rate: newNodeRateLimiter clamps the bucket's burst up to at least
// southbound.MaxLogSegmentBytes regardless of what rate is requested here, so an operator cannot reintroduce
// nodeRateLimiter's documented deadlock just by configuring a small rate.
func (c *Control) SetLogIngestLimits(maxConcurrent int, rateBytesPerSecond int64) {
	if maxConcurrent > 0 {
		c.logIngestSemaphore = make(chan struct{}, maxConcurrent)
	}
	if rateBytesPerSecond > 0 {
		c.logIngestRate = newNodeRateLimiter(rateBytesPerSecond)
	}
}

// SetTunnelEventStoreCapacity overrides the default total-size cap for the tunnel event store (stage 5 security
// review item 5, see DefaultTunnelEventStoreMaxBytes). Call before serving southbound traffic; maxBytes <= 0
// leaves the default in place. A no-op if the controller was not built with NewWithLogDir (c.tunnelEvents is
// nil, as it is for the plain New() constructor tests commonly use).
func (c *Control) SetTunnelEventStoreCapacity(maxBytes int64) {
	if c.tunnelEvents == nil || maxBytes <= 0 {
		return
	}
	c.tunnelEvents.mu.Lock()
	defer c.tunnelEvents.mu.Unlock()
	c.tunnelEvents.maxBytes = maxBytes
}

func (c *Control) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/health", c.health)
	m.HandleFunc("/api/auth/status", c.authStatus)
	m.HandleFunc("/api/auth/setup", c.authSetup)
	m.HandleFunc("/api/auth/login", c.authLogin)
	m.HandleFunc("/api/auth/logout", c.authLogout)
	m.HandleFunc("/api/auth/password", c.authChangePassword)
	m.HandleFunc("/api/settings", c.systemSettingsAPI)
	m.HandleFunc("/api/logging", c.loggingConfig)
	m.HandleFunc("/api/logging/adapters", c.loggingAdapters)
	m.HandleFunc("/api/logging/adapters/", c.loggingAdapter)
	m.HandleFunc("/api/logs", c.searchLogs)
	m.HandleFunc("/api/logging/trace/", c.loggingTrace)
	m.HandleFunc("/api/logging/tunnels/", c.loggingTunnelEvents)
	m.HandleFunc("/api/config.yaml", c.yamlConfig)
	m.HandleFunc("/api/sites", c.sites)
	m.HandleFunc("/api/sites/", c.site)
	m.HandleFunc("/api/routes/simulate", c.simulateRoute)
	m.HandleFunc("/api/nodes", c.nodesAPI)
	m.HandleFunc("/api/nodes/", c.nodeAPI)
	m.HandleFunc("/api/topology", c.topologyAPI)
	m.HandleFunc("/api/proxies", c.proxiesAPI)
	m.HandleFunc("/api/proxies/", c.proxyAPI)
	return c.authMiddleware(m)
}

type yamlConfig struct {
	Sites []store.Site `yaml:"sites"`
}

func (c *Control) yamlConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		sites, err := c.store.List(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		c.opMu.Lock()
		for i := range sites {
			sites[i].Running = c.siteRunning(sites[i])
		}
		c.opMu.Unlock()
		data, err := yaml.Marshal(yamlConfig{Sites: sites})
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=sites.yaml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	case http.MethodPut:
		defer r.Body.Close()
		decoder := yaml.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
		decoder.KnownFields(true)
		var cfg yamlConfig
		if err := decoder.Decode(&cfg); err != nil {
			writeJSON(w, 400, apiError{err.Error()})
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		for _, site := range cfg.Sites {
			if err := validate(site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
			if err := c.validateSystemCertificateReferences(site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
			if err := c.validateUpstreamTLSMaterial(r.Context(), site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
			if err := c.validateAccessLogAdapter(site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
			if err := c.validateNodeReferences(r.Context(), site); err != nil {
				writeError(w, fmt.Errorf("site %q: %w", site.ID, err))
				return
			}
		}
		if err := c.store.SaveMany(r.Context(), cfg.Sites); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]int{"imported": len(cfg.Sites)})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func (c *Control) health(w http.ResponseWriter, r *http.Request) {
	if err := c.store.Check(r.Context()); err != nil {
		writeJSON(w, 503, apiError{err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
func (c *Control) sites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		xs, e := c.store.List(r.Context())
		if e != nil {
			writeError(w, e)
			return
		}
		c.opMu.Lock()
		for i := range xs {
			xs[i].Running = c.siteRunning(xs[i])
		}
		c.opMu.Unlock()
		writeJSON(w, 200, xs)
	case http.MethodPost:
		var x store.Site
		if !decode(w, r, &x) {
			return
		}
		if err := validate(x); err != nil {
			writeError(w, err)
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		if err := c.validateSystemCertificateReferences(x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateUpstreamTLSMaterial(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateAccessLogAdapter(x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateNodeReferences(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.store.Save(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, x)
	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func (c *Control) site(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	id := parts[2]
	if len(parts) == 3 && r.Method == http.MethodGet {
		site, err := c.store.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		c.opMu.Lock()
		site.Running = c.siteRunning(site)
		c.opMu.Unlock()
		writeJSON(w, http.StatusOK, site)
		return
	}
	if len(parts) == 4 && parts[3] == "metrics" && r.Method == http.MethodGet {
		site, err := c.store.Get(r.Context(), id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c.siteMetrics(site))
		return
	}
	if len(parts) == 5 && parts[3] == "secrets" {
		c.secret(w, r, id, parts[4])
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPut {
		var x store.Site
		if !decode(w, r, &x) {
			return
		}
		x.ID = id
		if err := validate(x); err != nil {
			writeError(w, err)
			return
		}
		c.opMu.Lock()
		defer c.opMu.Unlock()
		if err := c.validateSystemCertificateReferences(x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateUpstreamTLSMaterial(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateAccessLogAdapter(x); err != nil {
			writeError(w, err)
			return
		}
		if err := c.validateNodeReferences(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		active := c.desired[id]
		var previous store.Site
		if active {
			previousSite, getErr := c.store.Get(r.Context(), id)
			if getErr != nil {
				writeError(w, getErr)
				return
			}
			previous = previousSite
		}
		if err := c.store.Save(r.Context(), x); err != nil {
			writeError(w, err)
			return
		}
		if active {
			if err := c.startLocked(r.Context(), id); err != nil {
				if rollbackErr := c.store.Save(r.Context(), previous); rollbackErr != nil {
					writeError(w, fmt.Errorf("site update failed: %v; restoring previous config failed: %w", err, rollbackErr))
					return
				}
				if rollbackErr := c.startLocked(r.Context(), id); rollbackErr != nil {
					writeError(w, fmt.Errorf("site update failed: %v; restoring previous runtime failed: %w", err, rollbackErr))
					return
				}
				writeError(w, err)
				return
			}
		}
		writeJSON(w, 200, x)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodDelete {
		_ = c.stop(r.Context(), id)
		if err := c.store.Delete(r.Context(), id); err != nil {
			writeError(w, err)
			return
		}
		c.engine.ForgetMetrics(id)
		w.WriteHeader(204)
		return
	}
	if len(parts) == 4 && r.Method == http.MethodPost {
		var err error
		switch parts[3] {
		case "start", "reload", "restart":
			err = c.start(r.Context(), id)
		case "stop":
			err = c.stop(r.Context(), id)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"id": id, "status": map[string]string{"start": "running", "reload": "reloaded", "restart": "restarted", "stop": "stopped"}[parts[3]]})
		return
	}
	writeJSON(w, 405, apiError{"method not allowed"})
}
func (c *Control) validateUpstreamTLSMaterial(ctx context.Context, site store.Site) error {
	for index, upstream := range site.Config.Upstreams {
		if upstream.ClientCertSecret == "" && upstream.ClientKeySecret == "" {
			continue
		}
		certificate, err := c.store.Secret(ctx, site.ID, upstream.ClientCertSecret)
		if err != nil {
			return fmt.Errorf("upstreams[%d] client certificate: %w", index, err)
		}
		key, err := c.store.Secret(ctx, site.ID, upstream.ClientKeySecret)
		if err != nil {
			return fmt.Errorf("upstreams[%d] client private key: %w", index, err)
		}
		if _, err := tls.X509KeyPair(certificate, key); err != nil {
			return fmt.Errorf("upstreams[%d] client certificate/key pair is invalid: %w", index, err)
		}
	}
	return nil
}

func validate(x store.Site) error {
	if strings.TrimSpace(x.ID) == "" || strings.TrimSpace(x.Name) == "" {
		return fmt.Errorf("site id and name are required")
	}
	if len(x.Config.Upstreams) == 0 {
		return fmt.Errorf("at least one upstream required")
	}
	if x.Config.ListenPort < 1 || x.Config.ListenPort > 65535 {
		return fmt.Errorf("listenPort must be 1-65535")
	}
	if x.Config.CertificateID != "" && !x.Config.TLS {
		return fmt.Errorf("certificateId requires tls to be enabled")
	}
	if x.Config.CertificateID != "" && (x.Config.CertificateSecret != "" || x.Config.PrivateKeySecret != "") {
		return fmt.Errorf("cannot use both a system server certificate and site certificate secrets")
	}
	if x.Config.AccessLog.MaxBodyBytes < -1 {
		return fmt.Errorf("accessLog.maxBodyBytes must be -1 or a non-negative byte limit")
	}
	if err := validatePlacement(x.Config.Nodes); err != nil {
		return err
	}
	if _, err := dataplane.NormalizeHostnames(x.Config.Hostnames); err != nil {
		return err
	}
	if _, err := routing.Compile(x.Config.Routes, len(x.Config.Upstreams)); err != nil {
		return err
	}
	for index, u := range x.Config.Upstreams {
		parsed, err := url.ParseRequestURI(u.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("invalid upstream URL")
		}
		if err := validatePaths(x.Config, index, u); err != nil {
			return err
		}
		if (u.ClientCertSecret == "") != (u.ClientKeySecret == "") {
			return fmt.Errorf("upstreams[%d] client certificate and key must be configured together", index)
		}
		if u.ClientCertSecret != "" && parsed.Scheme != "https" {
			return fmt.Errorf("upstreams[%d] client certificates require an HTTPS URL", index)
		}
		if u.ClientCertificateID != "" && parsed.Scheme != "https" {
			return fmt.Errorf("upstreams[%d] system client certificates require an HTTPS URL", index)
		}
		if u.ClientCertificateID != "" && (u.ClientCertSecret != "" || u.ClientKeySecret != "") {
			return fmt.Errorf("upstreams[%d] cannot use both a system client certificate and site client certificate secrets", index)
		}
		if len(u.RootCertificateIDs) > 0 && parsed.Scheme != "https" {
			return fmt.Errorf("upstreams[%d] system root certificates require an HTTPS URL", index)
		}
		seenRootIDs := make(map[string]struct{}, len(u.RootCertificateIDs))
		for _, id := range u.RootCertificateIDs {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("upstreams[%d] contains an empty system root certificate ID", index)
			}
			if _, exists := seenRootIDs[id]; exists {
				return fmt.Errorf("upstreams[%d] contains duplicate system root certificate ID %q", index, id)
			}
			seenRootIDs[id] = struct{}{}
		}
	}
	return nil
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		writeJSON(w, 400, apiError{e.Error()})
		return false
	}
	return true
}

func (c *Control) secret(w http.ResponseWriter, r *http.Request, siteID, name string) {
	if name == "" || len(name) > 128 {
		writeJSON(w, 400, apiError{"invalid secret name"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		defer r.Body.Close()
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
		if err != nil {
			writeJSON(w, 413, apiError{"secret exceeds 5 MiB"})
			return
		}
		if len(data) == 0 {
			writeJSON(w, 400, apiError{"secret cannot be empty"})
			return
		}
		if _, err = c.store.Get(r.Context(), siteID); err != nil {
			writeError(w, err)
			return
		}
		if err = c.store.SaveSecret(r.Context(), siteID, name, data); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 201, map[string]string{"name": name, "status": "stored"})
	case http.MethodDelete:
		site, err := c.store.Get(r.Context(), siteID)
		if err != nil {
			writeError(w, err)
			return
		}
		if site.Config.CertificateSecret == name || site.Config.PrivateKeySecret == name {
			writeJSON(w, http.StatusConflict, apiError{"secret is still referenced by the site configuration"})
			return
		}
		for _, upstream := range site.Config.Upstreams {
			if upstream.ClientCertSecret == name || upstream.ClientKeySecret == name {
				writeJSON(w, http.StatusConflict, apiError{"secret is still referenced by an upstream configuration"})
				return
			}
		}
		if err := c.store.DeleteSecret(r.Context(), siteID, name); err != nil {
			writeError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		writeJSON(w, 405, apiError{"method not allowed"})
	}
}
func writeError(w http.ResponseWriter, e error) {
	status := 400
	if errors.Is(e, store.ErrNotFound) || errors.Is(e, store.ErrNodeNotFound) {
		status = 404
	}
	writeJSON(w, status, apiError{e.Error()})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
