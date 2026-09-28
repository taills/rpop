package routing

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"sort"
	"strings"
)

// HeaderMatch is one request-header condition of a Route, following Caddy's header matcher:
// each value may be exact, "prefix*", "*suffix" or "*substring*"; values are ORed.
// No values means the header must be present; Absent means it must not be present.
type HeaderMatch struct {
	Name   string   `json:"name"`
	Values []string `json:"values,omitempty"`
	Absent bool     `json:"absent,omitempty"`
}

// Route sends matching requests to Upstreams[Upstream]. Path is exact unless it ends in "*"
// (prefix match) and is compared case-insensitively; an empty Path matches every path.
// All header conditions must hold. StripPrefix removes the matched path prefix before proxying.
type Route struct {
	Path        string        `json:"path,omitempty"`
	Headers     []HeaderMatch `json:"headers,omitempty"`
	StripPrefix bool          `json:"stripPrefix,omitempty"`
	Upstream    int           `json:"upstream"`
}

// Route matching follows Caddy's handle/handle_path semantics: paths are exact unless they end in "*",
// compare case-insensitively against the cleaned request path, and the most specific rule wins.
const (
	MaxRoutes          = 100
	maxRouteHeaders    = 16
	maxRoutePathLength = 1024
	catchAllPath       = "/*"
)

// Route check results reported by Explain.
const (
	ResultMatched      = "matched"
	ResultPathMismatch = "path"
	ResultHeaderFailed = "header"
	ResultSkipped      = "skipped"
	DefaultRoute       = -1
)

type headerCondition struct {
	name   string // canonical header name
	values []string
	absent bool
}

type compiledRoute struct {
	index   int // position in the configured route list
	route   Route
	label   string
	base    string // path pattern without the trailing "*"
	prefix  bool
	headers []headerCondition
}

type Router struct {
	routes []compiledRoute // in effective (evaluation) order
}

// Decision is the outcome of routing one request; index is -1 (and label empty) when no rule matched
// and the first upstream is used.
type Decision struct {
	Index       int
	Upstream    int
	Label       string
	StripPrefix string
}

// Check describes how one rule fared against a request, in evaluation order.
type Check struct {
	Index    int    `json:"index"`
	Label    string `json:"label"`
	Upstream int    `json:"upstream"`
	Result   string `json:"result"`
	Header   string `json:"header,omitempty"`
}

func Compile(routes []Route, upstreams int) (Router, error) {
	if len(routes) > MaxRoutes {
		return Router{}, fmt.Errorf("at most %d routes are allowed", MaxRoutes)
	}
	compiled := make([]compiledRoute, 0, len(routes))
	seen := make(map[string]int, len(routes))
	for index, route := range routes {
		item, err := compileRoute(index, route, upstreams)
		if err != nil {
			return Router{}, err
		}
		key := routeKey(item)
		if previous, exists := seen[key]; exists {
			return Router{}, fmt.Errorf("routes[%d] duplicates routes[%d]", index, previous)
		}
		seen[key] = index
		compiled = append(compiled, item)
	}
	sort.SliceStable(compiled, func(i, j int) bool { return moreSpecific(compiled[i], compiled[j]) })
	return Router{routes: compiled}, nil
}

func compileRoute(index int, route Route, upstreams int) (compiledRoute, error) {
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
	return compiledRoute{index: index, route: route, label: Label(route), base: base, prefix: base != pattern, headers: headers}, nil
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

func compileHeaderConditions(matches []HeaderMatch) ([]headerCondition, error) {
	if len(matches) > maxRouteHeaders {
		return nil, fmt.Errorf("at most %d header conditions are allowed", maxRouteHeaders)
	}
	out := make([]headerCondition, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		if !ValidHeaderName(match.Name) {
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

// ValidHeaderName reports whether name is an RFC 9110 token.
func ValidHeaderName(name string) bool {
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

func Label(route Route) string {
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

// Resolve picks the upstream for a request; requests matching no rule go to the first upstream.
func (sr Router) Resolve(r *http.Request) Decision {
	cleaned := CleanPath(r.URL.Path)
	for _, route := range sr.routes {
		if result, _ := route.check(r, cleaned); result == ResultMatched {
			return route.decision()
		}
	}
	return Decision{Index: DefaultRoute, Upstream: 0}
}

// Explain reports every rule in evaluation order; rules after the first match are marked skipped.
func (sr Router) Explain(r *http.Request) []Check {
	cleaned := CleanPath(r.URL.Path)
	checks := make([]Check, 0, len(sr.routes))
	matched := false
	for _, route := range sr.routes {
		check := Check{Index: route.index, Label: route.label, Upstream: route.route.Upstream, Result: ResultSkipped}
		if !matched {
			check.Result, check.Header = route.check(r, cleaned)
			matched = check.Result == ResultMatched
		}
		checks = append(checks, check)
	}
	return checks
}

func (route compiledRoute) decision() Decision {
	decision := Decision{Index: route.index, Upstream: route.route.Upstream, Label: route.label}
	if route.route.StripPrefix {
		decision.StripPrefix = strings.TrimSuffix(route.base, "/")
	}
	return decision
}

// check returns ResultMatched, ResultPathMismatch, or ResultHeaderFailed together with the failing header name.
func (route compiledRoute) check(r *http.Request, cleanedPath string) (string, string) {
	if !route.matchesPath(cleanedPath) {
		return ResultPathMismatch, ""
	}
	for _, header := range route.headers {
		if !header.matches(r) {
			return ResultHeaderFailed, header.name
		}
	}
	return ResultMatched, ""
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

// CleanPath resolves "." / ".." segments and repeated slashes, keeping a trailing slash.
func CleanPath(requestPath string) string {
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

// Apply rewrites the outbound request for the chosen upstream: it strips the matched prefix
// (from the cleaned path, preserving escaping such as %2F) and then joins the upstream base path.
func Apply(pr *httputil.ProxyRequest, target *url.URL, decision Decision) {
	if decision.StripPrefix != "" {
		StripPathPrefix(pr.Out.URL, decision.StripPrefix)
	}
	pr.SetURL(target)
	pr.Out.Host = target.Host
}

func StripPathPrefix(u *url.URL, prefix string) {
	escaped := CleanPath(u.EscapedPath())
	if rest, ok := trimPrefixFold(escaped, prefix); ok {
		rest = ensureLeadingSlash(rest)
		if decoded, err := url.PathUnescape(rest); err == nil {
			u.Path, u.RawPath = decoded, rest
			return
		}
	}
	rest, _ := trimPrefixFold(CleanPath(u.Path), prefix)
	u.Path, u.RawPath = ensureLeadingSlash(rest), ""
}

func ensureLeadingSlash(value string) string {
	if strings.HasPrefix(value, "/") {
		return value
	}
	return "/" + value
}
