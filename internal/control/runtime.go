package control

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/snapshot"
	"github.com/rpop-project/rpop/internal/store"
)

// HeaderTimeout bounds how long a client may take to send request headers to the control API.
const HeaderTimeout = dataplane.HeaderTimeout

// registryWriter stores data-plane access logs in the configured adapters. It is wired only to the controller's
// own in-process dataplane.Engine, which applies exclusively the embedded ("local") node's snapshot (see
// publish.go), so every record it sees was genuinely produced by this process.
type registryWriter struct{ registry *accesslog.Registry }

func (w registryWriter) WriteAccessLog(ctx context.Context, adapterID string, record accesslog.Record) error {
	// Stamp the embedded node's own identity, overwriting anything already set: the same "trust the identity
	// behind the write, not whatever the record claims" rule the southbound ingest path applies to a remote
	// node's uploads (see internal/control/logs_ingest.go's ingestLogLine, stage 5 security review item 3).
	record.ReportedBy = LocalNodeID
	return w.registry.Write(ctx, adapterID, record)
}

// resolveSite validates a stored site and inlines every certificate, key, CA, and proxy it references.
func (c *Control) resolveSite(ctx context.Context, site store.Site) (snapshot.Site, []plannedRoute, error) {
	if err := validate(site); err != nil {
		return snapshot.Site{}, nil, err
	}
	if err := c.validateAccessLogAdapter(site); err != nil {
		return snapshot.Site{}, nil, err
	}
	return c.resolveSpec(ctx, site)
}

// resolveSpec inlines every certificate, key, CA, and proxy a site references. It also returns the relay
// routes the site's paths need on the nodes they pass through.
func (c *Control) resolveSpec(ctx context.Context, site store.Site) (snapshot.Site, []plannedRoute, error) {
	cfg := site.Config
	spec := snapshot.Site{
		ID: site.ID, ListenAddress: cfg.ListenAddress, ListenPort: cfg.ListenPort, TLS: cfg.TLS,
		Hostnames: slices.Clone(cfg.Hostnames), CertificateID: cfg.CertificateID, Routes: slices.Clone(cfg.Routes),
		AccessLog: snapshot.AccessLog{
			AdapterID: cfg.AccessLog.AdapterID, IncludeBodies: cfg.AccessLog.IncludeBodies,
			IncludeSensitiveHeaders: cfg.AccessLog.IncludeSensitiveHeaders, MaxBodyBytes: cfg.AccessLog.MaxBodyBytes,
		},
	}
	if cfg.TLS {
		pair, err := c.siteServerKeyPair(ctx, site)
		if err != nil {
			return snapshot.Site{}, nil, err
		}
		spec.Certificate = pair
	}
	var routes []plannedRoute
	for index, upstream := range cfg.Upstreams {
		resolved, err := c.resolveUpstream(ctx, site.ID, upstream)
		if err == nil {
			var planned []plannedRoute
			resolved.Paths, planned, err = c.resolvePaths(ctx, upstream)
			routes = append(routes, planned...)
		}
		if err != nil {
			return snapshot.Site{}, nil, fmt.Errorf("upstreams[%d]: %w", index, err)
		}
		spec.Upstreams = append(spec.Upstreams, resolved)
	}
	return spec, routes, nil
}

func (c *Control) siteServerKeyPair(ctx context.Context, site store.Site) (*snapshot.KeyPair, error) {
	if site.Config.CertificateID != "" {
		return c.selectedKeyPair(serverCertificateKind, site.Config.CertificateID)
	}
	certificatePEM, err := c.store.Secret(ctx, site.ID, site.Config.CertificateSecret)
	if err != nil {
		return nil, fmt.Errorf("site certificate: %w", err)
	}
	keyPEM, err := c.store.Secret(ctx, site.ID, site.Config.PrivateKeySecret)
	if err != nil {
		return nil, fmt.Errorf("site private key: %w", err)
	}
	return &snapshot.KeyPair{CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(keyPEM)}, nil
}

func (c *Control) resolveUpstream(ctx context.Context, siteID string, upstream store.Upstream) (snapshot.Upstream, error) {
	roots, err := c.selectedRootCertificates(upstream.RootCertificateIDs)
	if err != nil {
		return snapshot.Upstream{}, err
	}
	resolved := snapshot.Upstream{
		URL: upstream.URL, ProxyURL: upstream.ProxyURL, ProxyType: upstream.ProxyType, InsecureSkipVerify: upstream.InsecureSkipVerify,
		RootCAs: roots, CABundle: upstream.CABundle, DialAddress: upstream.DialAddress, ServerName: upstream.ServerName,
		Failover: renderUpstreamFailover(upstream.Failover),
	}
	hasSecrets := upstream.ClientCertSecret != "" || upstream.ClientKeySecret != ""
	switch {
	case upstream.ClientCertificateID != "" && hasSecrets:
		return snapshot.Upstream{}, fmt.Errorf("upstream cannot use both a system client certificate and site client certificate secrets")
	case upstream.ClientCertificateID != "":
		if resolved.ClientCertificate, err = c.selectedKeyPair(clientCertificateKind, upstream.ClientCertificateID); err != nil {
			return snapshot.Upstream{}, err
		}
	case hasSecrets:
		if upstream.ClientCertSecret == "" || upstream.ClientKeySecret == "" {
			return snapshot.Upstream{}, fmt.Errorf("both upstream client certificate and key are required")
		}
		certificatePEM, err := c.store.Secret(ctx, siteID, upstream.ClientCertSecret)
		if err != nil {
			return snapshot.Upstream{}, err
		}
		keyPEM, err := c.store.Secret(ctx, siteID, upstream.ClientKeySecret)
		if err != nil {
			return snapshot.Upstream{}, err
		}
		resolved.ClientCertificate = &snapshot.KeyPair{CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(keyPEM)}
	}
	return resolved, nil
}

// renderUpstreamFailover copies an upstream's optional D19/D30 override into a fresh snapshot value (never
// aliasing the stored one, per the project's immutability convention); nil stays nil.
func renderUpstreamFailover(f *store.UpstreamFailover) *snapshot.UpstreamFailover {
	if f == nil {
		return nil
	}
	rendered := &snapshot.UpstreamFailover{DialTimeoutMs: f.DialTimeoutMs, MinCooldownMs: f.MinCooldownMs, MaxCooldownMs: f.MaxCooldownMs}
	if f.ActiveProbe != nil {
		activeProbe := *f.ActiveProbe
		rendered.ActiveProbe = &activeProbe
	}
	return rendered
}

// startLocked starts a site, or rebuilds it from the stored configuration when it already runs.
func (c *Control) startLocked(ctx context.Context, id string) error {
	if _, err := c.store.Get(ctx, id); err != nil {
		return err
	}
	wasDesired := c.desired[id]
	c.desired[id] = true
	if err := c.publishLocked(ctx, publishScope{sites: []string{id}})[id]; err != nil {
		if !wasDesired && !c.engine.Running(id) {
			delete(c.desired, id)
			c.publishLocked(ctx, publishScope{})
		}
		return err
	}
	return nil
}

func (c *Control) start(ctx context.Context, id string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.startLocked(ctx, id)
}

func (c *Control) stopLocked(ctx context.Context, id string) {
	delete(c.desired, id)
	for siteID, err := range c.publishLocked(ctx, publishScope{}) {
		c.log.Warn("site did not apply while stopping another", zap.String("site_id", siteID), zap.Error(err))
	}
}

func (c *Control) stop(ctx context.Context, id string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.stopLocked(ctx, id)
	return nil
}

// reapplyLocked pushes changed shared settings, such as renewed certificates, to the running sites.
func (c *Control) reapplyLocked(ctx context.Context) {
	for id, err := range c.publishLocked(ctx, publishScope{all: true}) {
		c.log.Warn("could not apply updated settings to site", zap.String("site_id", id), zap.Error(err))
	}
}

// StartAutoSites starts every site marked autoStart.
func (c *Control) StartAutoSites(ctx context.Context) error {
	sites, err := c.store.AutoStartSites(ctx)
	if err != nil {
		return err
	}
	c.opMu.Lock()
	defer c.opMu.Unlock()
	for _, site := range sites {
		c.desired[site.ID] = true
	}
	failed := false
	for id, err := range c.publishLocked(ctx, publishScope{all: true}) {
		c.log.Error("auto-start site failed", zap.String("site", id), zap.Error(err))
		if !c.engine.Running(id) && (!c.published.published(id) || c.placedOnLocal(ctx, id)) {
			delete(c.desired, id)
			failed = true
		}
	}
	if failed {
		c.publishLocked(ctx, publishScope{})
	}
	return nil
}

// StopAll stops every site.
func (c *Control) StopAll() {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	clear(c.desired)
	c.publishLocked(context.Background(), publishScope{})
	c.engine.StopAll()
	if c.overlay != nil {
		c.overlay.Close()
		c.overlay = nil
	}
}

// placedOnLocal reports whether a stored site runs on the embedded node.
func (c *Control) placedOnLocal(ctx context.Context, id string) bool {
	site, err := c.store.Get(ctx, id)
	return err == nil && slices.Contains(siteNodes(site.Config), LocalNodeID)
}

// siteRunning reports whether a site is started: on the embedded node it must be serving, elsewhere it is
// running once it has been published.
func (c *Control) siteRunning(site store.Site) bool {
	if !c.desired[site.ID] {
		return false
	}
	if slices.Contains(siteNodes(site.Config), LocalNodeID) {
		return c.engine.Running(site.ID)
	}
	return c.published.published(site.ID)
}

// DrainAccessLogs waits until every access log produced so far has been written.
func (c *Control) DrainAccessLogs(ctx context.Context) error {
	return c.engine.DrainAccessLogs(ctx)
}

// proxyHandler builds the proxy of a site configuration without binding a listener.
func (c *Control) proxyHandler(ctx context.Context, id string, cfg store.Config) (http.Handler, error) {
	spec, _, err := c.resolveSpec(ctx, store.Site{ID: id, Name: id, Config: cfg})
	if err != nil {
		return nil, err
	}
	return c.engine.Handler(spec)
}
