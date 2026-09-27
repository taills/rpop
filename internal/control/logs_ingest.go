package control

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
)

const (
	// maxDecompressedLogSegmentBytes bounds the NDJSON a segment may expand to once ungzipped, independently of
	// the compressed size southbound.MaxLogSegmentBytes already bounds on the wire: gzip can expand a small,
	// highly repetitive payload by orders of magnitude, so the wire-size cap alone does not stop a zip bomb from
	// a node (D23's nodes are semi-trusted, not fully trusted). A segment closes at spool.DefaultMaxSegmentBytes
	// (8MiB) uncompressed in normal operation, and one record can by itself reach close to
	// southbound.MaxLogSegmentBytes (32MiB, see its doc comment); this leaves headroom for that outlier while
	// still refusing an unbounded stream.
	maxDecompressedLogSegmentBytes = 64 << 20
	// maxLogRecordLineBytes bounds one NDJSON line. A single record can legitimately approach
	// southbound.MaxLogSegmentBytes (see its doc comment), so the per-line cap matches it; a longer line is
	// treated as corrupt or hostile and the line is dropped rather than failing the whole segment.
	maxLogRecordLineBytes = southbound.MaxLogSegmentBytes
)

// traceIDPattern validates a trackId/tunnelId path parameter: both are UUIDs minted by internal/traceid.New()
// (D22). It checks shape and length only, not the UUIDv7 version/variant bits, so it stays valid if that
// generator ever changes.
var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// keyedMutex serializes operations that share a key without a single lock across every key: log segment uploads
// from two different nodes proceed concurrently, but two uploads from the same node never race (D24).
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{locks: make(map[string]*sync.Mutex)} }

// lock blocks until key is free and returns a function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	l, ok := k.locks[key]
	if !ok {
		l = &sync.Mutex{}
		k.locks[key] = l
	}
	k.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// forget drops a key's lock once its node is deleted, so a long-lived controller does not keep one mutex per
// node ID that ever existed.
func (k *keyedMutex) forget(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.locks, key)
}

// logIngestResult counts the lines a segment could not place, for a single summary warning per segment (see
// southboundLogs) rather than one log line per bad record.
type logIngestResult struct {
	// undecodableLines counts lines that were not valid JSON, decoded to an unknown envelope kind, or whose
	// inner record failed to decode; they are dropped and never retried (D24 does not distinguish "bad line"
	// from "line the node never resends", since a byte-identical resend would fail the same way).
	undecodableLines int
	// rejectedAdapterLines counts access records whose AdapterID does not exist, or exists but is not used by
	// any site currently placed on the reporting node; see nodeAdapterSet's doc comment for the rationale.
	rejectedAdapterLines int
}

func (r logIngestResult) hasDrops() bool { return r.undecodableLines > 0 || r.rejectedAdapterLines > 0 }

// warnOnLogStatsRegressions logs once when a node's self-reported LogStats (Status.Logs) shows its quota or
// bounded-queue drop counters moved forward since the last report (D25's "count and warn" requirement). previous
// is nil on a node's first report; current is nil on a node that has not initialized a spool (the embedded node
// never sets it). Comparing against the last report, rather than logging on every non-zero count, is what keeps
// this rate-limited: a node stuck dropping records logs once per new increase, not once per status interval.
func (c *Control) warnOnLogStatsRegressions(nodeID string, previous, current *southbound.LogStats) {
	if current == nil {
		return
	}
	var previousQuotaDrops, previousQueueDrops uint64
	if previous != nil {
		previousQuotaDrops = previous.QuotaDroppedSegments
		previousQueueDrops = previous.AccessLogQueueDropped + previous.TunnelEventQueueDropped
	}
	if current.QuotaDroppedSegments > previousQuotaDrops {
		c.log.Warn("node dropped spooled log segments to stay under its disk quota",
			zap.String("node", nodeID), zap.Uint64("dropped_segments_delta", current.QuotaDroppedSegments-previousQuotaDrops),
			zap.Uint64("dropped_bytes_total", current.QuotaDroppedBytes))
	}
	if currentQueueDrops := current.AccessLogQueueDropped + current.TunnelEventQueueDropped; currentQueueDrops > previousQueueDrops {
		c.log.Warn("node dropped log records from a full bounded ingest queue",
			zap.String("node", nodeID), zap.Uint64("dropped_delta", currentQueueDrops-previousQueueDrops))
	}
}

// southboundLogs ingests one log segment (D23/D24). Authentication and generation checks mirror the other
// southbound endpoints (see authenticateNode); segment framing and idempotency follow the contract in
// docs/architecture/control-data-plane.md §5.
func (c *Control) southboundLogs(w http.ResponseWriter, r *http.Request) {
	node, err := c.authenticateNode(r)
	if err != nil {
		writeNodeAuthError(w, err)
		return
	}
	if node.ID == LocalNodeID {
		// In practice authenticateNode above already refuses this: the embedded node has no row in the nodes
		// table, so its certificate never resolves to one. This is a defense-in-depth backstop in case that ever
		// changes — the embedded node writes tunnel events and access logs directly in-process (item 4 of stage
		// 5 step 3) and must never upload logs over southbound.
		writeJSON(w, http.StatusBadRequest, apiError{"the embedded node does not upload logs over southbound"})
		return
	}
	if c.accessLogs == nil || c.tunnelEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"log ingest is not initialized"})
		return
	}
	segment, err := strconv.ParseUint(r.Header.Get(southbound.LogSegmentHeader), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"missing or invalid " + southbound.LogSegmentHeader + " header"})
		return
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		writeJSON(w, http.StatusBadRequest, apiError{"log segments must be gzip-encoded"})
		return
	}

	// Concurrent uploads from the same node must not race to read-then-advance its high-water mark; uploads from
	// other nodes are unaffected (D24).
	unlock := c.logIngestLocks.lock(node.ID)
	defer unlock()

	current, err := c.store.GetNode(r.Context(), node.ID)
	if err != nil {
		writeNodeAuthError(w, errNodeUnauthenticated)
		return
	}
	if segment <= current.LogHWM {
		// Idempotent replay: the node already got this far, whether or not this exact segment was the one that
		// advanced the mark (see southbound.LogAck's doc comment).
		writeJSON(w, http.StatusOK, southbound.LogAck{Ack: current.LogHWM})
		return
	}
	if segment > current.LogHWM+1 {
		// A legitimate gap: e.g. the node's spool dropped its oldest segment under quota pressure (D25). Accept
		// and note it; the controller has no way to recover the skipped segment's records.
		c.log.Info("log segment sequence jumped ahead of the node's high-water mark",
			zap.String("node", node.ID), zap.Uint64("previous_hwm", current.LogHWM), zap.Uint64("segment", segment))
	}

	body := http.MaxBytesReader(w, r.Body, southbound.MaxLogSegmentBytes)
	defer r.Body.Close()
	gzipReader, err := gzip.NewReader(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"invalid gzip body"})
		return
	}
	defer gzipReader.Close()

	result, err := c.ingestLogSegment(r.Context(), node.ID, gzipReader)
	if err != nil {
		c.log.Error("ingest log segment", zap.String("node", node.ID), zap.Uint64("segment", segment), zap.Error(err))
		writeJSON(w, http.StatusBadGateway, apiError{"could not ingest log segment"})
		return
	}
	if result.hasDrops() {
		c.log.Warn("log segment contained records the controller could not place",
			zap.String("node", node.ID), zap.Uint64("segment", segment),
			zap.Int("undecodable_lines", result.undecodableLines), zap.Int("rejected_adapter_lines", result.rejectedAdapterLines))
	}
	// Every record in the segment is durably written at this point; only now may the high-water mark advance
	// (D24's "write, then persist HWM, then ACK" order).
	if err := c.store.UpdateNodeLogHWM(r.Context(), node.ID, segment); err != nil {
		c.log.Error("persist log high-water mark", zap.String("node", node.ID), zap.Uint64("segment", segment), zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, apiError{"could not persist the log high-water mark"})
		return
	}
	writeJSON(w, http.StatusOK, southbound.LogAck{Ack: segment})
}

// ingestLogSegment decompresses and decodes one segment's NDJSON body, writing each record to its destination.
// It returns as soon as a write to a destination fails (as opposed to a merely undecodable or unplaceable
// line): the segment is not acknowledged, so the node resends the whole thing, which can duplicate records this
// call already wrote successfully before the failure. That is an accepted limitation of segment-level (not
// record-level) idempotency; see docs/architecture/control-data-plane.md §5.
func (c *Control) ingestLogSegment(ctx context.Context, nodeID string, gz io.Reader) (logIngestResult, error) {
	var result logIngestResult
	allowedAdapters, err := c.nodeAdapterSet(ctx, nodeID)
	if err != nil {
		return result, fmt.Errorf("resolve node's access log adapters: %w", err)
	}
	limited := io.LimitReader(gz, maxDecompressedLogSegmentBytes+1)
	reader := bufio.NewReaderSize(limited, 64<<10)
	var total int64
	for {
		line, readErr := reader.ReadBytes('\n')
		total += int64(len(line))
		if total > maxDecompressedLogSegmentBytes {
			return result, fmt.Errorf("segment exceeds %d bytes once decompressed", maxDecompressedLogSegmentBytes)
		}
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			if len(trimmed) > maxLogRecordLineBytes {
				result.undecodableLines++
			} else if err := c.ingestLogLine(ctx, nodeID, allowedAdapters, trimmed, &result); err != nil {
				return result, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
	}
	return result, nil
}

// ingestLogLine decodes one envelope and writes it to its destination. Decode failures and placement rejections
// are counted on result and otherwise ignored (a bad line must never block the rest of the segment); only an
// actual write failure to a destination is returned as an error.
func (c *Control) ingestLogLine(ctx context.Context, nodeID string, allowedAdapters map[string]bool, line []byte, result *logIngestResult) error {
	var envelope southbound.LogEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		result.undecodableLines++
		return nil
	}
	switch envelope.Kind {
	case southbound.LogKindAccess:
		var record accesslog.Record
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			result.undecodableLines++
			return nil
		}
		adapterID := strings.TrimSpace(envelope.AdapterID)
		if adapterID == "" || !allowedAdapters[adapterID] {
			// Covers both an AdapterID that no longer exists and one that exists but belongs to a site this node
			// does not (or no longer) host: see nodeAdapterSet's doc comment for why this stays lenient about
			// exactly which site, not just which node.
			result.rejectedAdapterLines++
			return nil
		}
		if err := c.accessLogs.Write(ctx, adapterID, record); err != nil {
			return fmt.Errorf("write access log to adapter %q: %w", adapterID, err)
		}
	case southbound.LogKindTunnel:
		var event overlay.TunnelEvent
		if err := json.Unmarshal(envelope.Record, &event); err != nil {
			result.undecodableLines++
			return nil
		}
		// The reporting node authenticated itself over mTLS; trust that identity over whatever nodeId the
		// record claims, so a node cannot attribute an event to a hop it is not.
		event.NodeID = nodeID
		if err := c.tunnelEvents.Write(ctx, event); err != nil {
			return fmt.Errorf("write tunnel event: %w", err)
		}
	default:
		result.undecodableLines++
	}
	return nil
}

// nodeAdapterSet lists the access log adapters that any site currently placed on nodeID selects. An uploaded
// access record's AdapterID only needs to be one of these, not specifically the adapter of the record's own
// SiteID: the AdapterID travels with the record from whenever the node first observed it (see
// southbound.LogEnvelope's doc comment), and by the time a delayed segment arrives the site may have moved to a
// different adapter, or off this node entirely. Rejecting on the coarser "does this node have any business
// writing to this adapter at all" check tolerates that drift instead of dropping records the moment a site is
// reconfigured, while still refusing an adapter no site on this node has ever selected.
func (c *Control) nodeAdapterSet(ctx context.Context, nodeID string) (map[string]bool, error) {
	sites, err := c.store.List(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool)
	for _, site := range sites {
		if site.Config.AccessLog.AdapterID == "" {
			continue
		}
		if slices.Contains(siteNodes(site.Config), nodeID) {
			allowed[site.Config.AccessLog.AdapterID] = true
		}
	}
	return allowed, nil
}

// loggingTrace serves GET /api/logging/trace/{trackId}: the one access log record that carries this track ID,
// plus the tunnelId already on it (D22). It searches every configured adapter, since a track ID does not name
// which one the request landed in.
func (c *Control) loggingTrace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	trackID := strings.TrimPrefix(r.URL.Path, "/api/logging/trace/")
	if !traceIDPattern.MatchString(trackID) {
		writeJSON(w, http.StatusBadRequest, apiError{"trackId must be a UUID"})
		return
	}
	if c.accessLogs == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"access log adapter registry is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var searchErr error
	for _, adapter := range c.accessLogs.List() {
		result, err := c.accessLogs.Search(ctx, adapter.ID, accesslog.Query{TrackID: trackID, Page: 1, PageSize: 1})
		if err != nil {
			searchErr = err
			c.log.Warn("search access log adapter for a trace", zap.String("adapter_id", adapter.ID), zap.Error(err))
			continue
		}
		if len(result.Records) > 0 {
			writeJSON(w, http.StatusOK, result.Records[0])
			return
		}
	}
	if searchErr != nil {
		writeJSON(w, http.StatusBadGateway, apiError{"could not search every access log adapter: " + searchErr.Error()})
		return
	}
	writeJSON(w, http.StatusNotFound, apiError{"no access log record found for this track id"})
}

// loggingTunnelEvents serves GET /api/logging/tunnels/{tunnelId}: every overlay.TunnelEvent any node reported
// for this tunnel, sorted by timestamp — the tunnel's full path timeline (D22).
func (c *Control) loggingTunnelEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, apiError{"method not allowed"})
		return
	}
	tunnelID := strings.TrimPrefix(r.URL.Path, "/api/logging/tunnels/")
	if !traceIDPattern.MatchString(tunnelID) {
		writeJSON(w, http.StatusBadRequest, apiError{"tunnelId must be a UUID"})
		return
	}
	if c.tunnelEvents == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{"tunnel event store is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	events, err := c.tunnelEvents.Query(ctx, tunnelID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}
