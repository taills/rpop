package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
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

// runHealthCheck probes the local control API and returns an error unless it answers 200 OK.
func runHealthCheck(addr string) error {
	url, err := healthCheckURL(addr)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: healthCheckTimeout}
	response, err := client.Get(url)
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
