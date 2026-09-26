package control

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/rpop-project/rpop/internal/store"
)

const maxSimulationHeaders = 64

type simulationHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// routeSimulationRequest carries unsaved routing settings so the editor can debug rules before saving.
type routeSimulationRequest struct {
	Config  store.Config       `json:"config"`
	Method  string             `json:"method,omitempty"`
	URL     string             `json:"url"`
	Headers []simulationHeader `json:"headers,omitempty"`
}

// routeSimulationResult shows which rule matched and how the path changes on its way to the upstream.
type routeSimulationResult struct {
	Matched      bool         `json:"matched"`
	RouteIndex   int          `json:"routeIndex"`
	Route        string       `json:"route,omitempty"`
	Upstream     int          `json:"upstream"`
	UpstreamURL  string       `json:"upstreamUrl"`
	StripPrefix  string       `json:"stripPrefix,omitempty"`
	RequestURI   string       `json:"requestUri"`
	MatchPath    string       `json:"matchPath"`
	StrippedPath string       `json:"strippedPath"`
	UpstreamURI  string       `json:"upstreamUri"`
	FinalURL     string       `json:"finalUrl"`
	Checks       []routeCheck `json:"checks"`
}

func (c *Control) simulateRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	var input routeSimulationRequest
	if !decode(w, r, &input) {
		return
	}
	result, err := simulateRoute(input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// simulateRoute runs the same resolve and rewrite code as the live proxy against a synthetic request.
func simulateRoute(input routeSimulationRequest) (routeSimulationResult, error) {
	if len(input.Config.Upstreams) == 0 {
		return routeSimulationResult{}, fmt.Errorf("at least one upstream required")
	}
	router, err := compileRoutes(input.Config.Routes, len(input.Config.Upstreams))
	if err != nil {
		return routeSimulationResult{}, err
	}
	request, err := simulationRequest(input)
	if err != nil {
		return routeSimulationResult{}, err
	}
	decision := router.resolve(request)
	target, err := url.Parse(input.Config.Upstreams[decision.upstream].URL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return routeSimulationResult{}, fmt.Errorf("upstreams[%d] URL is invalid", decision.upstream)
	}
	stripped := *request.URL
	if decision.stripPrefix != "" {
		stripPathPrefix(&stripped, decision.stripPrefix)
	}
	pr := &httputil.ProxyRequest{In: request, Out: request.Clone(request.Context())}
	applyRoute(pr, target, decision)
	return routeSimulationResult{
		Matched: decision.index >= 0, RouteIndex: decision.index, Route: decision.label,
		Upstream: decision.upstream, UpstreamURL: target.Redacted(), StripPrefix: decision.stripPrefix,
		RequestURI: request.URL.RequestURI(), MatchPath: cleanRequestPath(request.URL.Path),
		StrippedPath: stripped.EscapedPath(), UpstreamURI: pr.Out.URL.RequestURI(), FinalURL: pr.Out.URL.Redacted(),
		Checks: router.explain(request),
	}, nil
}

// simulationRequest accepts a full URL, a host-relative "/path?query", or "host/path" (assumed http).
func simulationRequest(input routeSimulationRequest) (*http.Request, error) {
	raw := strings.TrimSpace(input.URL)
	if raw == "" {
		return nil, fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(raw, "/") && !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !validHeaderName(method) {
		return nil, fmt.Errorf("invalid method %q", input.Method)
	}
	request, err := http.NewRequest(method, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if request.URL.Scheme != "" && request.URL.Scheme != "http" && request.URL.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https")
	}
	if len(input.Headers) > maxSimulationHeaders {
		return nil, fmt.Errorf("at most %d headers are allowed", maxSimulationHeaders)
	}
	for _, header := range input.Headers {
		if !validHeaderName(header.Name) {
			return nil, fmt.Errorf("invalid header name %q", header.Name)
		}
		if strings.EqualFold(header.Name, "Host") {
			request.Host = header.Value
			continue
		}
		request.Header.Add(header.Name, header.Value)
	}
	return request, nil
}
