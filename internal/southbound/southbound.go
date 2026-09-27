// Package southbound defines the protocol between the controller and data-plane nodes. Nodes always dial the
// controller over HTTP/2 with mutual TLS; the controller streams snapshots down a long-lived watch response.
package southbound

import (
	"encoding/json"
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
	LogsPath     = "/southbound/v1/logs"
)

// PingInterval is how often an idle watch stream carries a ping frame; nodes treat a stream silent for
// several intervals as dead and reconnect.
const PingInterval = 15 * time.Second

// ProtocolVersionHeader and ProtocolVersion mirror overlay's (D27): the whole rpop wire protocol, southbound
// control channel and overlay data-plane channel alike, shares one version number, carried on this one header.
// The canonical values live in package overlay, which southbound already imports for LinkStatus; overlay cannot
// import southbound back without a cycle, so it cannot alias these instead. Every southbound request
// (register/renew/watch/status/logs) carries this header, and the controller echoes it on every response, so a
// single round trip reveals a mismatch in either direction. Bump ProtocolVersion only for a breaking change; see
// overlay.ProtocolVersion's doc comment.
const (
	ProtocolVersionHeader = overlay.ProtocolVersionHeader
	ProtocolVersion       = overlay.ProtocolVersion
)

// MinSupportedProtocolVersion is the oldest node protocol version this controller still accepts (D27); a node
// below it is refused with a prompt to upgrade the node, and one above ProtocolVersion is refused with a prompt
// to upgrade the controller first (rolling upgrades go control-plane before nodes, mirroring how Kubernetes
// bounds its own node/control-plane version skew). Only the southbound control channel enforces this window —
// overlay CONNECT tunnel handshakes never reject over a version mismatch, only log one (see
// Overlay.checkTunnelProtocolVersion).
const MinSupportedProtocolVersion = 1

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
	// Paths is the node's per-upstream candidate-path failover state (D18/D19/D20); empty on a node currently
	// running no paths-based upstream.
	Paths []dataplane.UpstreamPathHealth `json:"paths,omitempty"`
	// Logs summarizes the node's log spool and upload pipeline (D23/D24/D25); nil on nodes that have not
	// initialized a spool (the embedded local node writes access logs directly and never sets this).
	Logs *LogStats `json:"logs,omitempty"`
}

// LogSegmentHeader names the header carrying the segment number (decimal uint64) of a POST LogsPath request;
// the request body is the segment file's bytes, unpacked from disk as-is (already gzip-compressed).
const LogSegmentHeader = "Rpop-Log-Segment"

// MaxLogSegmentBytes bounds how much of a POST LogsPath request body the controller reads. A segment closes at
// spool.DefaultMaxSegmentBytes (8MiB) of uncompressed NDJSON, but one record can be larger by itself: an access
// log record with request and response bodies captured at a site's maximum body-log limit (8MiB each, base64
// encoded) reaches roughly 22MiB on its own and still becomes one (oversized) segment. Gzip never expands data
// by more than a negligible margin, so the same bound also safely covers the compressed bytes actually read off
// the wire, and guards the controller against inflating an oversized or hostile body without a limit.
const MaxLogSegmentBytes = 32 << 20

// Kinds of record multiplexed onto one log segment; LogEnvelope.Kind names which follows in Record.
const (
	LogKindAccess = "access"
	LogKindTunnel = "tunnel"
)

// LogEnvelope is one line of a segment's NDJSON body (D23). For LogKindAccess, Record decodes as an
// accesslog.Record and AdapterID is the adapter the site had selected when the node observed it: the node
// carries this along instead of the controller re-resolving it from the site's current configuration, so a
// record relayed after a delay (or after the site's adapter selection later changes) still lands in the
// destination that was actually in effect when the record was produced. For LogKindTunnel, Record decodes as an
// overlay.TunnelEvent and AdapterID is unused, since tunnel events have no per-site adapter (P7: a relay route
// can be shared by several sites).
type LogEnvelope struct {
	Kind      string          `json:"kind"`
	AdapterID string          `json:"adapterId,omitempty"`
	Record    json.RawMessage `json:"record"`
}

// LogAck answers a segment upload with the highest segment number the controller has durably written and
// persisted as this node's high-water mark (D24). A node treats every segment numbered at or below Ack as
// delivered, whether or not it originally sent that exact segment: the controller's HWM can be ahead of what a
// freshly reset spool would send after the node reinstalls, and the node must skip ahead rather than treat the
// gap as still pending.
type LogAck struct {
	Ack uint64 `json:"ack"`
}

// LogStats summarizes a node's log spool and upload pipeline health, reported alongside Status.
type LogStats struct {
	// AccessLogQueueDropped/TunnelEventQueueDropped count records the spool's own bounded ingest queue could
	// not accept and dropped (P8): dataplane's access log queue and overlay's tunnel event queue each call into
	// the spool from a single dedicated goroutine, and a slow disk must not stall either of them.
	AccessLogQueueDropped   uint64 `json:"accessLogQueueDropped"`
	TunnelEventQueueDropped uint64 `json:"tunnelEventQueueDropped"`
	// QuotaDroppedSegments/QuotaDroppedBytes count segments the spool deleted, oldest first, to stay under its
	// disk quota (D25) before the controller acknowledged them.
	QuotaDroppedSegments uint64 `json:"quotaDroppedSegments"`
	QuotaDroppedBytes    uint64 `json:"quotaDroppedBytes"`
	// PendingSegments/PendingBytes are what is on disk right now, acknowledged or not, including the segment
	// still being written.
	PendingSegments uint64 `json:"pendingSegments"`
	PendingBytes    uint64 `json:"pendingBytes"`
	// AckedSegment is the highest segment number the controller has acknowledged so far; 0 before any upload
	// succeeds.
	AckedSegment uint64 `json:"ackedSegment"`
	// LastUploadError is the most recent upload failure the node saw; empty once an upload succeeds again.
	LastUploadError string `json:"lastUploadError,omitempty"`
}
