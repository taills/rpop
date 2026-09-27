// Package southbound defines the protocol between the controller and data-plane nodes. Nodes always dial the
// controller over HTTP/2 with mutual TLS; the controller streams snapshots down a long-lived watch response.
package southbound

import (
	"time"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/snapshot"
)

// Endpoints served on the controller's southbound listener.
const (
	RegisterPath = "/southbound/v1/register"
	RenewPath    = "/southbound/v1/renew"
	WatchPath    = "/southbound/v1/watch"
	StatusPath   = "/southbound/v1/status"
)

// PingInterval is how often an idle watch stream carries a ping frame; nodes treat a stream silent for
// several intervals as dead and reconnect.
const PingInterval = 15 * time.Second

// RegisterRequest exchanges a single-use join token and a CSR for a node certificate.
type RegisterRequest struct {
	Token  string `json:"token"`
	CSRPEM string `json:"csrPem"`
}

// RegisterResponse carries the node certificate and the CA every peer trusts.
type RegisterResponse struct {
	CertificatePEM string `json:"certificatePem"`
	CAPEM          string `json:"caPem"`
}

// RenewRequest asks for a fresh certificate for the key the node already holds.
type RenewRequest struct {
	CSRPEM string `json:"csrPem"`
}

// RenewResponse carries the renewed certificate.
type RenewResponse struct {
	CertificatePEM string `json:"certificatePem"`
}

// Frame types of the watch stream, which is newline-delimited JSON.
const (
	FrameSnapshot = "snapshot"
	FramePing     = "ping"
)

// Frame is one line of the watch stream.
type Frame struct {
	Type     string             `json:"type"`
	Snapshot *snapshot.Snapshot `json:"snapshot,omitempty"`
}

// Status is what a node reports about itself after applying a snapshot and periodically.
type Status struct {
	Version  string `json:"version"`
	Revision int64  `json:"revision"`
	// Errors holds the sites of the applied snapshot that could not be applied; they keep their previous runtime.
	Errors    map[string]string                    `json:"errors,omitempty"`
	Running   []string                             `json:"running"`
	Metrics   map[string]dataplane.MetricsSnapshot `json:"metrics,omitempty"`
	StartedAt time.Time                            `json:"startedAt"`
	// Links are the node's overlay links to its peers; RelayError explains a relay port that could not bind.
	Links      []overlay.LinkStatus `json:"links,omitempty"`
	RelayError string               `json:"relayError,omitempty"`
}
