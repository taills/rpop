package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
)

const (
	tunnelEventsDirName = "tunnel-events"
	// DefaultTunnelEventRetentionDays bounds how long tunnel events stay queryable: pruneLoop removes partitions
	// older than this on its own schedule (see tunnelEventPruneInterval), so the store cannot grow without bound
	// (D22's trace query only ever needs recent history to debug a path, not a permanent record). It mirrors
	// accesslog's file adapter default retention (DefaultConfig().File.KeepFiles = 30 daily files) at roughly
	// half that, since tunnel events are diagnostic, not an audit trail. Configurable since D31
	// (-tunnel-event-retention-days; see SetTunnelEventRetention).
	DefaultTunnelEventRetentionDays = 14
	// maxTunnelEventLineBytes bounds one stored line. Unlike an access log record, a TunnelEvent never carries
	// request/response bodies, so a generous static ceiling (rather than a configurable limit) is enough to
	// reject a corrupt or truncated line without failing the whole query.
	maxTunnelEventLineBytes = 64 << 10
	// tunnelQueryWindowDays bounds how many UTC day partitions Query scans once it has a starting day to work
	// from (loggingTunnelEvents derives it from the tunnel ID's own UUIDv7 timestamp, D22): the day the tunnel
	// started, plus one more to cover a tunnel that keeps running past midnight or is simply long-lived. A tunnel
	// whose events span more than that still has its earlier events found (they are in the first partition
	// scanned); only events reported more than a day after the tunnel opened would be missed. That is preferred
	// over scanning the full retention window on every query; widen this constant if it proves too tight in
	// practice. This is a query performance constant, not an operator-facing knob (D31): misconfiguring it would
	// silently drop events from a query with no error, unlike the size- and age-based limits above.
	tunnelQueryWindowDays = 2
	// DefaultTunnelEventStoreMaxBytes bounds the total size of every day partition the store keeps on disk,
	// independent of the age-based retention limit (stage 5 security review item 5): a burst of tunnel activity
	// within the retention window could otherwise still grow the store without bound. 10GiB is generous for a
	// diagnostic store, not an audit trail (see the doc comment below on why this is a bespoke store at all),
	// while still being a real ceiling instead of none. Configurable since D31 (-tunnel-event-store-max-bytes;
	// see Control.SetTunnelEventStoreCapacity).
	DefaultTunnelEventStoreMaxBytes int64 = 10 << 30
	// maxOpenTunnelEventFiles bounds how many day partitions' file handles the store keeps open at once (stage 5
	// low-priority finding item 6): out-of-order or delayed events crossing a day boundary can make consecutive
	// writes alternate between a couple of days, and a store that only ever kept the single most recent one open
	// would Close+Open on every alternation. A small LRU absorbs that without letting an unbounded number of
	// handles accumulate; 3 covers "today and yesterday" with one more to spare for a third day appearing
	// briefly, without needing to be configurable for what is purely an implementation detail.
	maxOpenTunnelEventFiles = 3
	// tunnelEventPruneInterval controls how often the background loop that enforces retentionDays and maxBytes
	// runs (item 6). Pruning used to run inline, every time Write opened a new day's partition; decoupling it
	// from the write path means a node cannot force a directory Glob plus a Stat of every partition on every
	// write just by alternating which day its reported event timestamps fall on. An hour is frequent enough that
	// neither limit is ever meaningfully exceeded in practice.
	tunnelEventPruneInterval = time.Hour
	// tunnelEventDedupCapacity bounds tunnelEventStore's write-time dedup cache (D29): how many recent
	// overlay.TunnelEvent.DedupKey values Write remembers before evicting the oldest to make room for a new one.
	// 4096 comfortably covers "the same segment redelivered right after its own successful upload" (D24's main
	// retransmission scenario), for however many tunnels are active at once, without growing without bound.
	// Resetting to empty on every restart is an accepted residual risk (a redelivery landing exactly across a
	// restart could double-count once) — Query's own dedup pass (see dedupTunnelEvents) is the backstop for that.
	tunnelEventDedupCapacity = 4096
)

// tunnelEventStore persists overlay.TunnelEvent records so GET /api/logging/tunnels/{tunnelId} (D22) can
// reconstruct a tunnel's full path after the fact. It reuses the technique the access log file adapter uses
// (see internal/accesslog/file.go): one NDJSON file per UTC day, scanned by Query — every partition still on
// disk when the caller has no starting day to work from, or just the few nearest one when it does (see Query and
// tunnelQueryWindowDays). A bespoke store, rather than plugging into the accesslog.Registry adapters, because
// tunnel events have no per-site adapter selection to begin with (P7: a relay route can be shared by many sites,
// so "which site's adapter" is not a meaningful question for them) — there is nothing for an operator to
// configure, so a self-contained store with a fixed retention is simpler than threading a new record type
// through every accesslog Sink implementation.
type tunnelEventStore struct {
	mu  sync.Mutex
	dir string
	log *zap.Logger
	// handles caches up to maxOpenTunnelEventFiles day partitions' open file handles (item 6), keyed by day; lru
	// holds those same keys ordered least- to most-recently-used, so alternating writes between a handful of
	// recent days reuse an already-open handle instead of a Close+Open every time.
	handles map[string]*os.File
	lru     []string
	// opens counts actual cache misses (a day this store had to open, or reopen, a handle for): tests use it to
	// prove alternating writes across a small set of days no longer reopen a handle per write.
	opens int
	// pruneRuns counts every pruneLocked call, including pruneLoop's own periodic ones: tests use it to observe
	// the background loop running (and, after Close, having actually stopped) without an exported hook.
	pruneRuns int
	// maxBytes bounds the total size of every day partition on disk (item 5); pruneLocked evicts the oldest ones
	// once it is exceeded, on top of the age-based cutoff it already applies.
	maxBytes int64
	// retentionDays bounds how long tunnel events stay queryable (D31, see DefaultTunnelEventRetentionDays and
	// Control.SetTunnelEventRetention); pruneLocked removes partitions older than this.
	retentionDays int
	// pruneInterval is tunnelEventPruneInterval in production; tests inject a short one (see
	// newTunnelEventStoreWithPruneInterval) to observe pruneLoop without waiting for it.
	pruneInterval time.Duration
	stop          chan struct{}
	stopOnce      sync.Once
	stopped       chan struct{} // closed by pruneLoop right before it returns.
	// dedupSeen holds the up to tunnelEventDedupCapacity most recently written DedupKey values (D29), for O(1)
	// membership tests; dedupOrder holds the same keys in insertion order, oldest first, so Write can evict the
	// oldest once the cache is full. Both guarded by mu, same as everything else Write touches.
	dedupSeen  map[string]struct{}
	dedupOrder []string
}

func newTunnelEventStore(logDir string, log *zap.Logger) (*tunnelEventStore, error) {
	return newTunnelEventStoreWithPruneInterval(logDir, log, tunnelEventPruneInterval)
}

// newTunnelEventStoreWithPruneInterval is newTunnelEventStore with an injectable background prune cadence, so
// tests can observe pruneLoop (item 6) run repeatedly without waiting tunnelEventPruneInterval for real.
func newTunnelEventStoreWithPruneInterval(logDir string, log *zap.Logger, pruneInterval time.Duration) (*tunnelEventStore, error) {
	dir := filepath.Join(logDir, tunnelEventsDirName)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create tunnel event directory: %w", err)
	}
	s := &tunnelEventStore{
		dir:           dir,
		log:           log,
		handles:       make(map[string]*os.File),
		maxBytes:      DefaultTunnelEventStoreMaxBytes,
		retentionDays: DefaultTunnelEventRetentionDays,
		pruneInterval: pruneInterval,
		stop:          make(chan struct{}),
		stopped:       make(chan struct{}),
		dedupSeen:     make(map[string]struct{}),
	}
	go s.pruneLoop()
	return s, nil
}

func tunnelEventPath(dir, day string) string {
	return filepath.Join(dir, "events-"+day+".jsonl")
}

// Write appends one event to day's partition, opening it (or reusing an already-cached handle, see handles) as
// needed. A retransmitted event whose DedupKey (D29) is still in the recent-write cache is silently dropped
// instead: the store's other client, GET /api/logging/tunnels/{tunnelId}, would otherwise see the same lifecycle
// point twice for a redelivered segment (D24).
func (s *tunnelEventStore) Write(ctx context.Context, event overlay.TunnelEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	day := event.Timestamp.Format("20060102")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rememberDedupKeyLocked(event.DedupKey()) {
		return nil
	}
	file, err := s.openLocked(day)
	if err != nil {
		return err
	}
	_, err = file.Write(line)
	return err
}

// rememberDedupKeyLocked reports whether key has already been written recently — D29's main "retransmit right
// after the last successful upload" scenario — and, if not, adds it to the bounded recency cache before
// returning false, so an immediately following duplicate is caught too. An empty key (see
// overlay.TunnelEvent.DedupKey) is never remembered or matched: the store cannot tell such events apart, so it
// must not risk merging unrelated ones. Called with mu held.
func (s *tunnelEventStore) rememberDedupKeyLocked(key string) bool {
	if key == "" {
		return false
	}
	if _, ok := s.dedupSeen[key]; ok {
		return true
	}
	if len(s.dedupOrder) >= tunnelEventDedupCapacity {
		oldest := s.dedupOrder[0]
		s.dedupOrder = s.dedupOrder[1:]
		delete(s.dedupSeen, oldest)
	}
	s.dedupSeen[key] = struct{}{}
	s.dedupOrder = append(s.dedupOrder, key)
	return false
}

// openLocked returns day's file handle, from the LRU cache if already open, so alternating writes between a
// handful of recent days do not Close+Open on every one (item 6). A genuine cache miss opens the file, evicting
// the least recently used handle first if the cache is now over capacity. It never prunes; see pruneLoop.
func (s *tunnelEventStore) openLocked(day string) (*os.File, error) {
	if file, ok := s.handles[day]; ok {
		s.touchLocked(day)
		return file, nil
	}
	file, err := os.OpenFile(tunnelEventPath(s.dir, day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	s.opens++
	s.handles[day] = file
	s.lru = append(s.lru, day)
	if len(s.lru) > maxOpenTunnelEventFiles {
		s.closeHandleLocked(s.lru[0])
	}
	return file, nil
}

// touchLocked moves day to the most-recently-used end of the LRU order.
func (s *tunnelEventStore) touchLocked(day string) {
	for i, d := range s.lru {
		if d == day {
			s.lru = append(s.lru[:i], s.lru[i+1:]...)
			break
		}
	}
	s.lru = append(s.lru, day)
}

// closeHandleLocked closes and evicts day's cached file handle, if one is open, so a later write for that same
// day reopens it fresh rather than reusing a handle pruneLocked may since have removed the file out from under.
// A no-op if day has no cached handle.
func (s *tunnelEventStore) closeHandleLocked(day string) {
	file, ok := s.handles[day]
	if !ok {
		return
	}
	_ = file.Close()
	delete(s.handles, day)
	for i, d := range s.lru {
		if d == day {
			s.lru = append(s.lru[:i], s.lru[i+1:]...)
			break
		}
	}
}

// pruneLoop runs pruneLocked once immediately and then every pruneInterval, until Close stops it (item 6): a
// background cadence, rather than inline with every Write, so a node cannot force a directory Glob plus a Stat
// of every partition on every write just by alternating which day its reported event timestamps fall on.
func (s *tunnelEventStore) pruneLoop() {
	defer close(s.stopped)
	s.pruneNow()
	ticker := time.NewTicker(s.pruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.pruneNow()
		}
	}
}

func (s *tunnelEventStore) pruneNow() {
	s.mu.Lock()
	s.pruneLocked()
	s.mu.Unlock()
}

// pruneLocked removes partitions the age-based retention window has aged out, then, if the store's total size
// still exceeds maxBytes, evicts the oldest remaining ones (by day, ascending) until it fits (item 5) — except
// the partition most recently written to (mostRecentDay below), even if that leaves the store over the cap or
// past the retention window: a late or backdated event, or a clock far behind, must not make the store delete
// the very partition a write just landed in (this generalizes the single *os.File store this replaced, which
// protected "the currently open file" the same way). Any other day's cached handle (see handles) never protects
// its partition, in either pass: closeHandleLocked closes it first, so a handle can never be left pointing at a
// file this store just unlinked, and a later write for that day reopens (and thereby recreates) it.
func (s *tunnelEventStore) pruneLocked() {
	s.pruneRuns++
	var mostRecentDay string
	if len(s.lru) > 0 {
		mostRecentDay = s.lru[len(s.lru)-1]
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.retentionDays).Format("20060102")
	paths, err := filepath.Glob(filepath.Join(s.dir, "events-*.jsonl"))
	if err != nil {
		return
	}
	sort.Strings(paths) // oldest day first, so the size-based pass below evicts in the right order
	var total int64
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		if day != mostRecentDay && len(day) == 8 && day < cutoff {
			s.closeHandleLocked(day)
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				s.log.Warn("prune expired tunnel event partition", zap.String("path", path), zap.Error(err))
			}
			continue
		}
		if info, statErr := os.Stat(path); statErr == nil {
			total += info.Size()
		}
		kept = append(kept, path)
	}
	for _, path := range kept {
		if total <= s.maxBytes {
			break
		}
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		if day == mostRecentDay {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
		s.closeHandleLocked(day)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.log.Warn("evict tunnel event partition over the size cap", zap.String("path", path), zap.Int64("max_bytes", s.maxBytes), zap.Error(err))
			continue
		}
		total -= info.Size()
	}
}

// Query returns every stored event for tunnelID, across every node that reported one, sorted by timestamp: that
// is the tunnel's timeline from the hop that opened it through to the hop that closed it (D22). A non-zero start
// narrows the scan to its UTC day partition plus tunnelQueryWindowDays-1 more (see that constant); the zero
// value scans every partition still on disk, for callers that could not derive a starting day (e.g. tunnelID is
// not a UUIDv7). The result is deduplicated by DedupKey (D29) as a backstop for whatever Write's bounded, and
// restart-reset, recency cache let through onto disk more than once.
func (s *tunnelEventStore) Query(ctx context.Context, tunnelID string, start time.Time) ([]overlay.TunnelEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := s.candidatePathsLocked(start)
	if err != nil {
		return nil, err
	}
	events := []overlay.TunnelEvent{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		found, err := readTunnelEvents(path, tunnelID)
		if err != nil {
			return nil, err
		}
		events = append(events, found...)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.Before(events[j].Timestamp) })
	return dedupTunnelEvents(events), nil
}

// dedupTunnelEvents is Query's fallback deduplication pass (D29): it collapses events sharing the same non-empty
// DedupKey down to their first occurrence in events' already-sorted order, catching whatever Write's bounded,
// restart-reset recency cache let through — most notably a retransmit landing after the cache's window has moved
// on, or after a restart cleared it. An event with an empty DedupKey (see overlay.TunnelEvent.DedupKey) is never
// collapsed with anything, including another empty-keyed one.
func dedupTunnelEvents(events []overlay.TunnelEvent) []overlay.TunnelEvent {
	seen := make(map[string]bool, len(events))
	deduped := make([]overlay.TunnelEvent, 0, len(events))
	for _, event := range events {
		key := event.DedupKey()
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		deduped = append(deduped, event)
	}
	return deduped
}

// candidatePathsLocked lists the partition files Query should read: every one on disk for a zero start, or
// exactly the tunnelQueryWindowDays day-partition paths starting at start's UTC day otherwise — computed by name
// rather than another glob, since most of those files will not exist for a tunnel that only ran on one of them
// (readTunnelEvents already treats a missing file as "no events" rather than an error).
func (s *tunnelEventStore) candidatePathsLocked(start time.Time) ([]string, error) {
	if start.IsZero() {
		paths, err := filepath.Glob(filepath.Join(s.dir, "events-*.jsonl"))
		if err != nil {
			return nil, err
		}
		sort.Strings(paths)
		return paths, nil
	}
	paths := make([]string, tunnelQueryWindowDays)
	for i := range paths {
		day := start.UTC().AddDate(0, 0, i).Format("20060102")
		paths[i] = tunnelEventPath(s.dir, day)
	}
	return paths, nil
}

func readTunnelEvents(path, tunnelID string) ([]overlay.TunnelEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var found []overlay.TunnelEvent
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && len(line) <= maxTunnelEventLineBytes {
			var event overlay.TunnelEvent
			if err := json.Unmarshal(line, &event); err == nil && event.TunnelID == tunnelID {
				found = append(found, event)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return found, nil
}

// Close stops pruneLoop, waiting for it to actually exit (item 6: a prune already in flight must finish before
// Close starts closing the handles it might otherwise still be examining), then closes every cached handle.
func (s *tunnelEventStore) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.stopped
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for day, file := range s.handles {
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.handles, day)
	}
	s.lru = nil
	return firstErr
}

// SetTunnelEventRetention overrides the default age-based retention window for the tunnel event store (D31, see
// DefaultTunnelEventRetentionDays). Call before serving southbound traffic; days <= 0 leaves the default in
// place. A no-op if the controller was not built with NewWithLogDir (c.tunnelEvents is nil, as it is for the
// plain New() constructor tests commonly use), mirroring SetTunnelEventStoreCapacity.
func (c *Control) SetTunnelEventRetention(days int) {
	if c.tunnelEvents == nil || days <= 0 {
		return
	}
	c.tunnelEvents.mu.Lock()
	defer c.tunnelEvents.mu.Unlock()
	c.tunnelEvents.retentionDays = days
}

// localTunnelEventWriter adapts a tunnelEventStore to overlay.TunnelEventSink for the embedded "local" node
// (item 4 of stage 5 step 3): overlay's own eventQueue already decouples this from the forwarding path with a
// bounded channel that drops and counts on overflow (P8), the same property spool gives a registered node's
// tunnel events, so this can write straight through without a second queue.
type localTunnelEventWriter struct {
	store *tunnelEventStore
	log   *zap.Logger
}

func (w localTunnelEventWriter) RecordTunnelEvent(event overlay.TunnelEvent) {
	if err := w.store.Write(context.Background(), event); err != nil {
		w.log.Error("write tunnel event", zap.String("tunnel_id", event.TunnelID), zap.Error(err))
	}
}
