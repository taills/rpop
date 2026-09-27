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
}

type routeDecisionKey struct{}

// siteHandler builds the routed, observed proxy of a site together with the transports it owns.
func (e *Engine) siteHandler(site snapshot.Site) (http.Handler, []*http.Transport, error) {
	if len(site.Upstreams) == 0 {
		return nil, nil, fmt.Errorf("at least one upstream is required")
	}
	router, err := routing.Compile(site.Routes, len(site.Upstreams))
	if err != nil {
		return nil, nil, err
	}
	targets := make([]upstreamTarget, 0, len(site.Upstreams))
	transports := make([]*http.Transport, 0, len(site.Upstreams))
	dialer := e.pathDialer()
	logTunnelEvents := accessLoggingEnabled(site.AccessLog)
	for index, upstream := range site.Upstreams {
		target, err := newUpstreamTarget(upstream, dialer, logTunnelEvents)
		if err != nil {
			closeIdle(transports)
			return nil, nil, fmt.Errorf("upstreams[%d]: %w", index, err)
		}
		targets = append(targets, target)
		transports = append(transports, target.transports...)
	}
	return e.observeSite(site.ID, site.AccessLog, routedHandler(router, targets)), transports, nil
}

func newUpstreamTarget(upstream snapshot.Upstream, dialer PathDialer, logTunnelEvents bool) (upstreamTarget, error) {
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
	if len(upstream.Paths) > 0 {
		roundTripper, transports = newPathTransports(transport, upstream.Paths, dialer, logTunnelEvents)
	}
	proxy := &httputil.ReverseProxy{
		Transport: roundTripper,
		Rewrite: func(pr *httputil.ProxyRequest) {
			decision, _ := pr.In.Context().Value(routeDecisionKey{}).(routing.Decision)
			routing.Apply(pr, u, decision)
		},
	}
	return upstreamTarget{label: u.Redacted(), proxy: proxy, transports: transports}, nil
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
