package control

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/rpop-project/rpop/internal/store"
)

// Route matching follows Caddy's handle/handle_path semantics: paths are exact unless they end in "*",
// compare case-insensitively against the cleaned request path, and the most specific rule wins.
const (
	maxRoutes          = 100
	maxRouteHeaders    = 16
	maxRoutePathLength = 1024
	catchAllPath       = "/*"
)

// Route check results reported by explain.
const (
	routeMatched       = "matched"
	routePathMismatch  = "path"
	routeHeaderFailed  = "header"
	routeNotEvaluated  = "skipped"
	defaultRouteResult = -1
)

type headerCondition struct {
	name   string // canonical header name
	values []string
	absent bool
}

type compiledRoute struct {
	index   int // position in the configured route list
	route   store.Route
	label   string
	base    string // path pattern without the trailing "*"
	prefix  bool
	headers []headerCondition
}

type siteRouter struct {
	routes []compiledRoute // in effective (evaluation) order
}

// routeDecision is the outcome of routing one request; index is -1 (and label empty) when no rule matched
// and the first upstream is used.
type routeDecision struct {
	index       int
	upstream    int
	label       string
	stripPrefix string
}

// routeCheck describes how one rule fared against a request, in evaluation order.
type routeCheck struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Upstream int    `json:"upstream"`
	Result   string `json:"result"`
	Header   string `json:"header,omitempty"`
}

func compileRoutes(routes []store.Route, upstreams int) (siteRouter, error) {
	if len(routes) > maxRoutes {
		return siteRouter{}, fmt.Errorf("at most %d routes are allowed", maxRoutes)
	}
	compiled := make([]compiledRoute, 0, len(routes))
	seen := make(map[string]int, len(routes))
	for index, route := range routes {
		item, err := compileRoute(index, route, upstreams)
		if err != nil {
			return siteRouter{}, err
		}
		key := routeKey(item)
		if previous, exists := seen[key]; exists {
			return siteRouter{}, fmt.Errorf("routes[%d] duplicates routes[%d]", index, previous)
		}
		seen[key] = index
		compiled = append(compiled, item)
	}
	sort.SliceStable(compiled, func(i, j int) bool { return moreSpecific(compiled[i], compiled[j]) })
	return siteRouter{routes: compiled}, nil
}

func compileRoute(index int, route store.Route, upstreams int) (compiledRoute, error) {
	if route.Upstream < 0 || route.Upstream >= upstreams {
		return compiledRoute{}, fmt.Errorf("routes[%d] upstream %d is out of range", index, route.Upstream)
	}
	if route.Path == "" && len(route.Headers) == 0 {
		return compiledRoute{}, fmt.Errorf("routes[%d] needs a path or a header condition", index)
	}
	if route.StripPrefix && route.Path == "" {
		return compiledRoute{}, fmt.Errorf("routes[%d] stripPrefix requires a path", index)
	}
	pattern := route.Path
	if pattern == "" {
		pattern = catchAllPath
	}
	if err := validateRoutePath(pattern); err != nil {
		return compiledRoute{}, fmt.Errorf("routes[%d] %w", index, err)
	}
	headers, err := compileHeaderConditions(route.Headers)
	if err != nil {
		return compiledRoute{}, fmt.Errorf("routes[%d] %w", index, err)
	}
	base := strings.TrimSuffix(pattern, "*")
	return compiledRoute{index: index, route: route, label: routeLabel(route), base: base, prefix: base != pattern, headers: headers}, nil
}

func validateRoutePath(pattern string) error {
	if !strings.HasPrefix(pattern, "/") {
		return fmt.Errorf("path must start with /")
	}
	if len(pattern) > maxRoutePathLength {
		return fmt.Errorf("path exceeds %d bytes", maxRoutePathLength)
	}
	if strings.Contains(strings.TrimSuffix(pattern, "*"), "*") {
		return fmt.Errorf("path may contain * only at the end")
	}
	for _, ch := range pattern {
		if ch <= ' ' || ch == 0x7f || ch == '?' || ch == '#' {
			return fmt.Errorf("path contains an invalid character")
		}
	}
	return nil
}

func compileHeaderConditions(matches []store.HeaderMatch) ([]headerCondition, error) {
	if len(matches) > maxRouteHeaders {
		return nil, fmt.Errorf("at most %d header conditions are allowed", maxRouteHeaders)
	}
	out := make([]headerCondition, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		if !validHeaderName(match.Name) {
			return nil, fmt.Errorf("invalid header name %q", match.Name)
		}
		name := http.CanonicalHeaderKey(match.Name)
		if seen[name] {
			return nil, fmt.Errorf("header %q is listed more than once", name)
		}
		seen[name] = true
		if match.Absent && len(match.Values) > 0 {
			return nil, fmt.Errorf("header %q: absent cannot be combined with values", name)
		}
		for _, value := range match.Values {
			if value == "" {
				return nil, fmt.Errorf("header %q has an empty value", name)
			}
		}
		out = append(out, headerCondition{name: name, values: append([]string(nil), match.Values...), absent: match.Absent})
	}
	return out, nil
}

// validHeaderName reports whether name is an RFC 9110 token.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", ch)) {
			return false
		}
	}
	return true
}

// routeKey identifies rules that would always match the same requests, so duplicates can be rejected.
func routeKey(route compiledRoute) string {
	parts := make([]string, 0, len(route.headers))
	for _, header := range route.headers {
		values := append([]string(nil), header.values...)
		sort.Strings(values)
		parts = append(parts, fmt.Sprintf("%s|%t|%s", header.name, header.absent, strings.Join(values, "\x00")))
	}
	sort.Strings(parts)
	pattern := route.base
	if route.prefix {
		pattern += "*"
	}
	return strings.ToLower(pattern) + "\x01" + strings.Join(parts, "\x01")
}

// moreSpecific orders longer paths first, exact before prefix at equal length, then rules with more header conditions.
func moreSpecific(a, b compiledRoute) bool {
	if len(a.base) != len(b.base) {
		return len(a.base) > len(b.base)
	}
	if a.prefix != b.prefix {
		return !a.prefix
	}
	return len(a.headers) > len(b.headers)
}

func routeLabel(route store.Route) string {
	var label strings.Builder
	if route.Path == "" {
		label.WriteString("*")
	} else {
		label.WriteString(route.Path)
	}
	for _, header := range route.Headers {
		name := http.CanonicalHeaderKey(header.Name)
		switch {
		case header.Absent:
			fmt.Fprintf(&label, " [!%s]", name)
		case len(header.Values) == 0:
			fmt.Fprintf(&label, " [%s]", name)
		default:
			fmt.Fprintf(&label, " [%s: %s]", name, strings.Join(header.Values, "|"))
		}
	}
	return label.String()
}

// resolve picks the upstream for a request; requests matching no rule go to the first upstream.
func (sr siteRouter) resolve(r *http.Request) routeDecision {
	cleaned := cleanRequestPath(r.URL.Path)
	for _, route := range sr.routes {
		if result, _ := route.check(r, cleaned); result == routeMatched {
			return route.decision()
		}
	}
	return routeDecision{index: defaultRouteResult, upstream: 0}
}

// explain reports every rule in evaluation order; rules after the first match are marked skipped.
func (sr siteRouter) explain(r *http.Request) []routeCheck {
	cleaned := cleanRequestPath(r.URL.Path)
	checks := make([]routeCheck, 0, len(sr.routes))
	matched := false
	for _, route := range sr.routes {
		check := routeCheck{Index: route.index, Label: route.label, Upstream: route.route.Upstream, Result: routeNotEvaluated}
		if !matched {
			check.Result, check.Header = route.check(r, cleaned)
			matched = check.Result == routeMatched
		}
		checks = append(checks, check)
	}
	return checks
}

func (route compiledRoute) decision() routeDecision {
	decision := routeDecision{index: route.index, upstream: route.route.Upstream, label: route.label}
	if route.route.StripPrefix {
		decision.stripPrefix = strings.TrimSuffix(route.base, "/")
	}
	return decision
}

// check returns routeMatched, routePathMismatch, or routeHeaderFailed together with the failing header name.
func (route compiledRoute) check(r *http.Request, cleanedPath string) (string, string) {
	if !route.matchesPath(cleanedPath) {
		return routePathMismatch, ""
	}
	for _, header := range route.headers {
		if !header.matches(r) {
			return routeHeaderFailed, header.name
		}
	}
	return routeMatched, ""
}

func (route compiledRoute) matchesPath(cleanedPath string) bool {
	if route.prefix {
		_, ok := trimPrefixFold(cleanedPath, route.base)
		return ok
	}
	return strings.EqualFold(cleanedPath, route.base)
}

func (header headerCondition) matches(r *http.Request) bool {
	actual, present := r.Header[header.name]
	if header.name == "Host" {
		actual, present = []string{r.Host}, r.Host != ""
	}
	if header.absent || !present {
		return header.absent != present
	}
	if len(header.values) == 0 {
		return true
	}
	for _, want := range header.values {
		for _, value := range actual {
			if headerValueMatches(want, value) {
				return true
			}
		}
	}
	return false
}

// headerValueMatches supports Caddy's exact, "prefix*", "*suffix" and "*substring*" forms; values are case-sensitive.
func headerValueMatches(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	leading, trailing := strings.HasPrefix(pattern, "*"), strings.HasSuffix(pattern, "*")
	switch {
	case leading && trailing:
		return strings.Contains(value, pattern[1:len(pattern)-1])
	case leading:
		return strings.HasSuffix(value, pattern[1:])
	case trailing:
		return strings.HasPrefix(value, pattern[:len(pattern)-1])
	default:
		return value == pattern
	}
}

// cleanRequestPath resolves "." / ".." segments and repeated slashes, keeping a trailing slash.
func cleanRequestPath(requestPath string) string {
	if requestPath == "" {
		return "/"
	}
	cleaned := path.Clean("/" + requestPath)
	if strings.HasSuffix(requestPath, "/") && cleaned != "/" {
		cleaned += "/"
	}
	return cleaned
}

func trimPrefixFold(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", false
	}
	return value[len(prefix):], true
}

// applyRoute rewrites the outbound request for the chosen upstream: it strips the matched prefix
// (from the cleaned path, preserving escaping such as %2F) and then joins the upstream base path.
func applyRoute(pr *httputil.ProxyRequest, target *url.URL, decision routeDecision) {
	if decision.stripPrefix != "" {
		stripPathPrefix(pr.Out.URL, decision.stripPrefix)
	}
	pr.SetURL(target)
	pr.Out.Host = target.Host
}

func stripPathPrefix(u *url.URL, prefix string) {
	escaped := cleanRequestPath(u.EscapedPath())
	if rest, ok := trimPrefixFold(escaped, prefix); ok {
		rest = ensureLeadingSlash(rest)
		if decoded, err := url.PathUnescape(rest); err == nil {
			u.Path, u.RawPath = decoded, rest
			return
		}
	}
	rest, _ := trimPrefixFold(cleanRequestPath(u.Path), prefix)
	u.Path, u.RawPath = ensureLeadingSlash(rest), ""
}

func ensureLeadingSlash(value string) string {
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}
