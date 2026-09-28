package sharedport

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeHostnames lower-cases, validates, and de-duplicates site hostnames; "*.example.com" wildcards are
// allowed. Ported unchanged from the dataplane package that owned this logic before shared ports existed, so
// existing site configurations keep exactly the same validation and routing behavior.
func NormalizeHostnames(hostnames []string) ([]string, error) {
	out := make([]string, 0, len(hostnames))
	seen := make(map[string]bool, len(hostnames))
	for _, name := range hostnames {
		name = normalizeHost(name)
		if !validHostname(name) {
			return nil, fmt.Errorf("invalid hostname %q", name)
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	return strings.TrimSuffix(host, ".")
}

func validHostname(host string) bool {
	if host == "" || strings.ContainsAny(host, "/\\ \t\r\n") {
		return false
	}
	if strings.HasPrefix(host, "*.") {
		host = host[2:]
	} else if strings.Contains(host, "*") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.Contains(host, ":") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func hostPatternsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	aWildcard, bWildcard := strings.HasPrefix(a, "*."), strings.HasPrefix(b, "*.")
	if aWildcard && !bWildcard {
		return hostPatternMatches(a, b)
	}
	if bWildcard && !aWildcard {
		return hostPatternMatches(b, a)
	}
	if aWildcard && bWildcard {
		return strings.HasSuffix(a[1:], b[1:]) || strings.HasSuffix(b[1:], a[1:])
	}
	return false
}

func hostPatternMatches(pattern, host string) bool {
	if !strings.HasPrefix(pattern, "*.") {
		return pattern == host
	}
	suffix := pattern[1:]
	return len(host) > len(suffix) && strings.HasSuffix(host, suffix)
}
