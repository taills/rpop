package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rpop-project/rpop/internal/sharedport"
)

const healthCheckTimeout = 5 * time.Second

// envDefault returns the environment variable key, or fallback when it is unset or empty.
// Flags still override it, so container images can configure rpop through ENV.
func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// envDefaultInt64 is envDefault for an integer setting (log spool quota, upload rate); an unset, empty, or
// unparsable value falls back the same way.
func envDefaultInt64(key string, fallback int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// envDefaultInt is envDefaultInt64 for a plain int setting (D31's window sizes and concurrency limits, well
// within the platform int range); an unset, empty, or unparsable value falls back the same way.
func envDefaultInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// envDefaultBool is envDefault for a boolean setting (D30's -path-active-probe switch); strconv.ParseBool accepts
// "1"/"t"/"T"/"true"/... and their "0"/"f"/"false"/... counterparts. An unset, empty, or unparsable value falls
// back the same way as envDefault's own string case.
func envDefaultBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// healthCheckURL maps the control API listen address to a URL reachable from the same host:
// wildcard hosts (empty, 0.0.0.0, ::) are probed on loopback.
func healthCheckURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if port == "" {
		return "", fmt.Errorf("invalid listen address %q: missing port", addr)
	}
	switch ip := net.ParseIP(host); {
	case host == "" || ip != nil && ip.IsUnspecified() && ip.To4() != nil:
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified():
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/health", nil
}

// runHealthCheck probes the local control API and returns an error unless it answers 200 OK. hostHeader, when
// non-empty, is sent as the request's Host header — required once -console-hostnames restricts the console to
// specific hostnames, since the console then answers /api/health only on one of them (see parseConsoleHostnames
// and cmd/rpop's main, which passes the first configured hostname).
func runHealthCheck(addr, hostHeader string) error {
	url, err := healthCheckURL(addr)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if hostHeader != "" {
		req.Host = hostHeader
	}
	client := &http.Client{Timeout: healthCheckTimeout}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health check %s: %s", url, response.Status)
	}
	return nil
}

// Process modes. All-in-one runs the controller and an embedded data-plane node; the other two split them.
const (
	modeAllInOne   = "all-in-one"
	modeController = "controller"
	modeNode       = "node"

	defaultSouthboundAddr = ":7443"
)

// southboundAddress is where the controller serves nodes. A dedicated controller always serves them; an
// all-in-one process only when an address is configured.
func southboundAddress(mode, configured string) (string, error) {
	switch mode {
	case modeAllInOne:
		return configured, nil
	case modeController:
		if configured == "" {
			return defaultSouthboundAddr, nil
		}
		return configured, nil
	case modeNode:
		return "", nil
	default:
		return "", fmt.Errorf("unknown mode %q; use %s, %s, or %s", mode, modeAllInOne, modeController, modeNode)
	}
}

// parseConsoleHostnames splits the -console-hostnames flag on commas and validates each entry with
// sharedport.NormalizeHostnames — the same validation Registry.PutSite admits a site's own hostnames under, so
// an invalid entry fails fast at startup instead of silently becoming a literal Host string nothing can ever
// match. An empty (or whitespace-only) flag returns nil, meaning "no restriction" (see PutPlaintextOwner).
func parseConsoleHostnames(raw string) ([]string, error) {
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	return sharedport.NormalizeHostnames(names)
}
