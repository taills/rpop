package control

import (
	"context"
	"errors"
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

// registryWriter stores data-plane access logs in the configured adapters.
type registryWriter struct{ registry *accesslog.Registry }

func (w registryWriter) WriteAccessLog(ctx context.Context, adapterID string, record accesslog.Record) error {
	return w.registry.Write(ctx, adapterID, record)
}

// resolveSite validates a stored site and inlines every certificate, key, and CA it references.
func (c *Control) resolveSite(ctx context.Context, site store.Site) (snapshot.Site, error) {
	if err := validate(site); err != nil {
		return snapshot.Site{}, err
	}
	if err := c.validateAccessLogAdapter(site); err != nil {
		return snapshot.Site{}, err
	}
	return c.resolveSpec(ctx, site)
}

// resolveSpec inlines every certificate, key, and CA a site references.
func (c *Control) resolveSpec(ctx context.Context, site store.Site) (snapshot.Site, error) {
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
			return snapshot.Site{}, err
		}
		spec.Certificate = pair
	}
	for index, upstream := range cfg.Upstreams {
		resolved, err := c.resolveUpstream(ctx, site.ID, upstream)
		if err != nil {
			return snapshot.Site{}, fmt.Errorf("upstreams[%d]: %w", index, err)
		}
		spec.Upstreams = append(spec.Upstreams, resolved)
	}
	return spec, nil
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

// applyLocked hands the engine the resolved spec of every site that should run. A site that cannot be resolved
// keeps the spec it is currently serving. Sites named in rebuild get fresh runtimes even when unchanged.
func (c *Control) applyLocked(ctx context.Context, rebuild ...string) map[string]error {
	errs := make(map[string]error)
	specs := make([]snapshot.Site, 0, len(c.desired))
	for id := range c.desired {
		site, err := c.store.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			delete(c.desired, id)
			continue
		}
		var spec snapshot.Site
		if err == nil {
			spec, err = c.resolveSite(ctx, site)
		}
		if err != nil {
			errs[id] = err
			if current, ok := c.engine.Spec(id); ok {
				specs = append(specs, current)
			}
			continue
		}
		specs = append(specs, spec)
	}
	for id, err := range c.engine.Apply(specs, rebuild...) {
		errs[id] = err
	}
	return errs
}

// startLocked starts a site, or rebuilds it from the stored configuration when it already runs.
func (c *Control) startLocked(ctx context.Context, id string) error {
	if _, err := c.store.Get(ctx, id); err != nil {
		return err
	}
	wasDesired := c.desired[id]
	c.desired[id] = true
	if err := c.applyLocked(ctx, id)[id]; err != nil {
		if !wasDesired && !c.engine.Running(id) {
			delete(c.desired, id)
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

func (c *Control) stopLocked(id string) {
	delete(c.desired, id)
	c.engine.Stop(id)
}

func (c *Control) stop(id string) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.stopLocked(id)
	return nil
}

// reapplyLocked pushes changed shared settings, such as renewed certificates, to the running sites.
func (c *Control) reapplyLocked(ctx context.Context) {
	for id, err := range c.applyLocked(ctx) {
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
	for id, err := range c.applyLocked(ctx) {
		c.log.Error("auto-start site failed", zap.String("site", id), zap.Error(err))
		if !c.engine.Running(id) {
			delete(c.desired, id)
		}
	}
	return nil
}

// StopAll stops every site.
func (c *Control) StopAll() {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	clear(c.desired)
	c.engine.StopAll()
}

// DrainAccessLogs waits until every access log produced so far has been written.
func (c *Control) DrainAccessLogs(ctx context.Context) error {
	return c.engine.DrainAccessLogs(ctx)
}

// proxyHandler builds the proxy of a site configuration without binding a listener.
func (c *Control) proxyHandler(ctx context.Context, id string, cfg store.Config) (http.Handler, error) {
	spec, err := c.resolveSpec(ctx, store.Site{ID: id, Name: id, Config: cfg})
	if err != nil {
		return nil, err
	}
	return c.engine.Handler(spec)
}
