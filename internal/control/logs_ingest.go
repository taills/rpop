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
	"github.com/rpop-project/rpop/internal/store"
	"github.com/rpop-project/rpop/internal/traceid"
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
	// traceQueryWindow bounds the [from, to] window loggingTrace builds around a trackId's own UUIDv7 timestamp.
	// The entry node mints the track ID and timestamps its access log record from the same time.Now() call (see
	// dataplane.observeSite and requestRecord), so in practice the two are the same instant; a full minute on
	// each side is far more margin than that ever needs while still letting a time-partitioned adapter
	// (ClickHouse, S3, Elasticsearch) skip almost all of its history instead of scanning it end to end.
	traceQueryWindow = time.Minute
	// maxLogSegmentJump bounds how far one request may advance a segment number past the node's current
	// high-water mark (stage 5 security review item 2). A legitimate jump happens when the node's spool drops
	// its oldest unacked segment under disk quota pressure while offline (D25); segments close at least every
	// spool.DefaultMaxSegmentAge (30s) or spool.DefaultMaxSegmentBytes (8MiB), whichever comes first, so even a
	// node that somehow closed one every single second, continuously, for ten years offline would jump under
	// 3.2*10^8 segments — comfortably under this bound. A jump past it is rejected outright: the node cannot
	// have a legitimate reason to skip this many segments in one request, and accepting it would let a node (or
	// an attacker holding its certificate) push the high-water mark far enough ahead that the controller can
	// never again tell a genuine gap from one manufactured to make it permanently blind to that node's future
	// segments.
	maxLogSegmentJump = 1 << 32
	// warnLogSegmentJumpThreshold is the smaller point past which an accepted jump is still logged at Warn
	// instead of Info, so an operator notices an unusual gap even though the request was accepted. A node
	// closing a segment every 30 seconds continuously for a full year offline accumulates roughly 1,051,200
	// segments (a little over 2^20); this threshold sits at exactly 2^20, so routine quota-drop gaps (typically
	// tens to low thousands of segments) never trigger it.
	warnLogSegmentJumpThreshold = 1 << 20
)

// traceIDPattern validates a trackId/tunnelId path parameter: both are UUIDs minted by internal/traceid.New()
// (D22). It checks shape and length only, not the UUIDv7 version/variant bits, so it stays valid if that
// generator ever changes.
var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// keyedMutex serializes operations that share a key without a single lock across every key: log segment uploads
// from two different nodes proceed concurrently, but two uploads from the same node never race (D24). Each
// entry is reference-counted and removed from the map the instant its last caller releases it, rather than
// waiting for an explicit forget(key) once the node it belongs to is deleted (stage 5 low-priority finding item
// 3): forget(key) and lock(key) racing each other for the same key — a node deleted and immediately
// re-registered under the same ID, for instance — could otherwise create a second, independent *sync.Mutex for
// key while a caller already held (or was still waiting on) the first one, so the two entries would never
// exclude each other and two "serialized" operations on the same key could run concurrently after all.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*refCountedMutex
}

// refCountedMutex is one keyedMutex entry. refs is guarded by keyedMutex.mu, not mu itself: every read or write
// of it happens while holding that lock, in lock and the func lock returns.
type refCountedMutex struct {
	mu   sync.Mutex
	refs int
}

func newKeyedMutex() *keyedMutex { return &keyedMutex{locks: make(map[string]*refCountedMutex)} }

// lock blocks until key is free and returns a function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	entry, ok := k.locks[key]
	if !ok {
		entry = &refCountedMutex{}
		k.locks[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// logIngestResult counts the lines a segment could not place, for a single summary warning per segment (see
// southboundLogs) rather than one log line per bad record.
type logIngestResult struct {
	// undecodableLines counts lines that were not valid JSON, decoded to an unknown envelope kind, or whose
	// inner record failed to decode; they are dropped and never retried (D24 does not distinguish "bad line"
	// from "line the node never resends", since a byte-identical resend would fail the same way).
	undecodableLines int
	// rejectedAdapterLines counts access records whose AdapterID does not exist, or exists but is not used by
	// any site currently placed on the reporting node; see nodePlacement's doc comment for the rationale.
	rejectedAdapterLines int
	// rejectedSiteLines counts access records whose SiteID does not name a site currently placed on the
	// reporting node: a node authenticates itself, not the sites it serves, so nothing stops it from claiming an
	// arbitrary SiteID otherwise (stage 5 security review item 3). This is deliberately about the node's
	// placement, not a delayed record's own site-to-adapter selection at the time it was produced; see
	// nodePlacement's doc comment.
	rejectedSiteLines int
}

func (r logIngestResult) hasDrops() bool {
	return r.undecodableLines > 0 || r.rejectedAdapterLines > 0 || r.rejectedSiteLines > 0
}

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
	if segment < 1 || segment > store.MaxLogHWM {
		// Reject before any write happens (stage 5 security review item 1): a segment this large could never be
		// persisted as this node's high-water mark (see store.MaxLogHWM's doc comment for why), and segment
		// numbers are 1-based (the node's spool never emits 0), so that end of the range is invalid too. Checking
		// this immediately after parsing the header, before touching the store or decompressing anything, means
		// the controller can never durably write a segment's records and then fail to acknowledge them: without
		// this check, a node (or an attacker holding its certificate) could force exactly that by claiming a
		// segment number of 1<<63 or above, permanently wedging its own upload retries and, since each attempt
		// still fully decompresses and writes the segment before failing, amplifying its resource cost on the
		// controller.
		writeJSON(w, http.StatusBadRequest, apiError{fmt.Sprintf("%s must be between 1 and %d", southbound.LogSegmentHeader, uint64(store.MaxLogHWM))})
		return
	}
	if r.Header.Get("Content-Encoding") != "gzip" {
		writeJSON(w, http.StatusBadRequest, apiError{"log segments must be gzip-encoded"})
		return
	}

	// Bound how many segments the controller processes at once, across every node, before doing any of a
	// segment's real work: each one may briefly hold tens of megabytes of decompressed NDJSON in memory (stage 5
	// security review item 5). A full semaphore answers immediately rather than queuing the request, so a node
	// backs off instead of piling up blocked connections.
	select {
	case c.logIngestSemaphore <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusServiceUnavailable, apiError{"the controller is processing the maximum number of concurrent log segments; retry shortly"})
		return
	}
	defer func() { <-c.logIngestSemaphore }()

	// Bound how fast this node specifically may upload, independent of the concurrency cap above (item 5). The
	// node always sets Content-Length to the segment's exact size (see internal/spool/upload.go); a request that
	// somehow arrives without one is charged for the full southbound.MaxLogSegmentBytes, the worst case, rather
	// than let an unknown size bypass the limiter.
	segmentBytes := r.ContentLength
	if segmentBytes <= 0 {
		segmentBytes = southbound.MaxLogSegmentBytes
	}
	if !c.logIngestRate.allow(node.ID, segmentBytes) {
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, apiError{"upload rate exceeded for this node; retry shortly"})
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
		jump := segment - current.LogHWM
		if jump > maxLogSegmentJump {
			// Reject before any write happens, the same as the range check above: a jump this large can never be
			// a legitimate quota-drop gap (see maxLogSegmentJump's doc comment), so there is nothing to accept.
			writeJSON(w, http.StatusBadRequest, apiError{"log segment number jumped too far ahead of the node's high-water mark"})
			return
		}
		// A legitimate gap: e.g. the node's spool dropped its oldest segment under quota pressure (D25). Accept
		// and note it; the controller has no way to recover the skipped segment's records. A jump large enough to
		// be unusual (see warnLogSegmentJumpThreshold) is still accepted, but logged at Warn instead of Info so an
		// operator notices it.
		logJump := c.log.Info
		if jump > warnLogSegmentJumpThreshold {
			logJump = c.log.Warn
		}
		logJump("log segment sequence jumped ahead of the node's high-water mark",
			zap.String("node", node.ID), zap.Uint64("previous_hwm", current.LogHWM), zap.Uint64("segment", segment), zap.Uint64("jump", jump))
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
			zap.Int("undecodable_lines", result.undecodableLines), zap.Int("rejected_adapter_lines", result.rejectedAdapterLines),
			zap.Int("rejected_site_lines", result.rejectedSiteLines))
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
	placement, err := c.nodePlacement(ctx, nodeID)
	if err != nil {
		return result, fmt.Errorf("resolve node's site placement: %w", err)
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
			} else if err := c.ingestLogLine(ctx, nodeID, placement, trimmed, &result); err != nil {
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
func (c *Control) ingestLogLine(ctx context.Context, nodeID string, placement nodePlacementInfo, line []byte, result *logIngestResult) error {
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
		if adapterID == "" || !placement.allowedAdapters[adapterID] {
			// Covers both an AdapterID that no longer exists and one that exists but belongs to a site this node
			// does not (or no longer) host: see nodePlacement's doc comment for why this stays lenient about
			// exactly which site, not just which node.
			result.rejectedAdapterLines++
			return nil
		}
		if !placement.siteIDs[record.SiteID] {
			// The node authenticated itself, not the SiteID it claims: without this, an AdapterID any site on
			// this node may write to (checked above) is all a node needs to attribute a record to a site placed
			// on a different node, or one that no longer exists (stage 5 security review item 3). A record for a
			// site that used to be on this node but has since moved off it looks the same as this from here, and
			// is refused the same way; see nodePlacement's doc comment.
			result.rejectedSiteLines++
			return nil
		}
		// The reporting node authenticated itself over mTLS; trust that identity over whatever reportedBy the
		// record claims, the same reasoning as event.NodeID below.
		record.ReportedBy = nodeID
		if err := c.accessLogs.Write(ctx, adapterID, record); err != nil {
			return fmt.Errorf("write access log to adapter %q: %w", adapterID, err)
		}
	case southbound.LogKindTunnel:
		var event overlay.TunnelEvent
		if err := json.Unmarshal(envelope.Record, &event); err != nil {
			result.undecodableLines++
			return nil
		}
		if !traceIDPattern.MatchString(event.TunnelID) {
			// A node controls every field of an uploaded record, TunnelID included; without this it could write
			// to an arbitrary key of the tunnel event store instead of one of internal/traceid.New()'s own IDs
			// (stage 5 security review item 4). Dropped like any other undecodable line, not failed outright: one
			// bad tunnel ID must not block the rest of the segment.
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

// nodePlacementInfo is what nodePlacement resolves once per segment (item 3): the access log adapters any site
// currently placed on the node selects, and the exact set of site IDs currently placed on it.
type nodePlacementInfo struct {
	// allowedAdapters is the set of adapter IDs any site placed on the node currently selects. An uploaded access
	// record's AdapterID only needs to be one of these, not specifically the adapter of the record's own SiteID:
	// the AdapterID travels with the record from whenever the node first observed it (see
	// southbound.LogEnvelope's doc comment), and by the time a delayed segment arrives the site may have moved to
	// a different adapter, or off this node entirely. Checking the coarser "does this node have any business
	// writing to this adapter at all" tolerates that drift instead of dropping records the moment a site is
	// reconfigured, while still refusing an adapter no site on this node has ever selected.
	allowedAdapters map[string]bool
	// siteIDs is the set of site IDs currently placed on the node, checked against a record's own claimed SiteID
	// (item 3): unlike allowedAdapters, this is not given the same "used to be true" leniency, since it is the
	// one check standing between a compromised node and attributing traffic to a site it has nothing to do with.
	siteIDs map[string]bool
}

// nodePlacement resolves nodeID's current site placement: see nodePlacementInfo's doc comment for what each of
// its two sets means and why they tolerate different amounts of drift.
func (c *Control) nodePlacement(ctx context.Context, nodeID string) (nodePlacementInfo, error) {
	sites, err := c.store.List(ctx)
	if err != nil {
		return nodePlacementInfo{}, err
	}
	placement := nodePlacementInfo{allowedAdapters: make(map[string]bool), siteIDs: make(map[string]bool)}
	for _, site := range sites {
		if !slices.Contains(siteNodes(site.Config), nodeID) {
			continue
		}
		placement.siteIDs[site.ID] = true
		if site.Config.AccessLog.AdapterID != "" {
			placement.allowedAdapters[site.Config.AccessLog.AdapterID] = true
		}
	}
	return placement, nil
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
	query := accesslog.Query{TrackID: trackID, Page: 1, PageSize: 1}
	// A trackId that fails to parse as a UUIDv7 (traceIDPattern above does not check the version bits, see its
	// doc comment) leaves query.From/To zero, which every adapter treats as "no bound" — fall back to searching
	// each adapter's full history rather than rejecting the request outright, the same as any other ID this
	// generator never actually minted.
	if generated, ok := traceid.Time(trackID); ok {
		query.From, query.To = generated.Add(-traceQueryWindow), generated.Add(traceQueryWindow)
	}
	var searchErr error
	for _, adapter := range c.accessLogs.List() {
		result, err := c.accessLogs.Search(ctx, adapter.ID, query)
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
	// A tunnelId that fails to parse as a UUIDv7 leaves start zero, which Query treats as "scan every partition"
	// (see its doc comment) — the same fallback loggingTrace uses above, for the same reason.
	var start time.Time
	if generated, ok := traceid.Time(tunnelID); ok {
		start = generated
	}
	events, err := c.tunnelEvents.Query(ctx, tunnelID, start)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}
