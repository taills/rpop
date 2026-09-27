package dataplane

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/rpop-project/rpop/internal/routing"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// upstreamTarget is one upstream with its own transport; the per-request routing.Decision decides the path rewrite.
type upstreamTarget struct {
	label      string
	proxy      *httputil.ReverseProxy
	transports []*http.Transport
	// failover is non-nil when the upstream has candidate paths (D18); it lets siteHandler report their health.
	failover *failoverTransport
}

type routeDecisionKey struct{}

// siteHandler builds the routed, observed proxy of a site together with the transports it owns and the
// per-upstream path failover state PathHealth reports. managed reports whether the caller will call
// siteRuntime.release() on the result, which alone makes it safe to enable D19 active probing (see Engine.Handler).
func (e *Engine) siteHandler(site snapshot.Site, managed bool) (http.Handler, []*http.Transport, []upstreamPathGroup, error) {
	if len(site.Upstreams) == 0 {
		return nil, nil, nil, fmt.Errorf("at least one upstream is required")
	}
	router, err := routing.Compile(site.Routes, len(site.Upstreams))
	if err != nil {
		return nil, nil, nil, err
	}
	targets := make([]upstreamTarget, 0, len(site.Upstreams))
	transports := make([]*http.Transport, 0, len(site.Upstreams))
	dialer := e.pathDialer()
	logTunnelEvents := accessLoggingEnabled(site.AccessLog)
	globalActiveProbe := e.pathActiveProbeEnabled()
	for index, upstream := range site.Upstreams {
		target, err := newUpstreamTarget(upstream, dialer, logTunnelEvents, globalActiveProbe, managed)
		if err != nil {
			closeIdle(transports)
			return nil, nil, nil, fmt.Errorf("upstreams[%d]: %w", index, err)
		}
		targets = append(targets, target)
		transports = append(transports, target.transports...)
	}
	var pathGroups []upstreamPathGroup
	for index, target := range targets {
		if target.failover != nil {
			pathGroups = append(pathGroups, upstreamPathGroup{index: index, label: target.label, failover: target.failover})
		}
	}
	return e.observeSite(site.ID, site.AccessLog, routedHandler(router, targets)), transports, pathGroups, nil
}

// newUpstreamTarget builds one upstream's proxy target; globalActiveProbe is the engine-wide D19 default and
// managed reports whether the caller will release the result (see siteHandler).
func newUpstreamTarget(upstream snapshot.Upstream, dialer PathDialer, logTunnelEvents bool, globalActiveProbe, managed bool) (upstreamTarget, error) {
	u, err := url.Parse(upstream.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return upstreamTarget{}, fmt.Errorf("invalid upstream URL")
	}
	transport, err := newTransport(upstream)
	if err != nil {
		return upstreamTarget{}, err
	}
	var roundTripper http.RoundTripper = transport
	transports := []*http.Transport{transport}
	var failover *failoverTransport
	if len(upstream.Paths) > 0 {
		cfg := resolvePathFailoverConfig(upstream.Failover, globalActiveProbe)
		failover, transports = newPathTransports(transport, upstream.Paths, dialer, logTunnelEvents, cfg, managed && cfg.activeProbe)
		roundTripper = failover
	}
	proxy := &httputil.ReverseProxy{
		Transport: roundTripper,
		Rewrite: func(pr *httputil.ProxyRequest) {
			decision, _ := pr.In.Context().Value(routeDecisionKey{}).(routing.Decision)
			routing.Apply(pr, u, decision)
		},
	}
	return upstreamTarget{label: u.Redacted(), proxy: proxy, transports: transports, failover: failover}, nil
}

// routedHandler dispatches each request to the upstream chosen by the site's routes and reports the choice to observeSite.
func routedHandler(router routing.Router, targets []upstreamTarget) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decision := router.Resolve(r)
		target := targets[decision.Upstream]
		if trace, ok := r.Context().Value(routeTraceKey{}).(*routeTrace); ok {
			trace.upstream, trace.route = target.label, decision.Label
		}
		target.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), routeDecisionKey{}, decision)))
	})
}

func closeIdle(transports []*http.Transport) {
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}
