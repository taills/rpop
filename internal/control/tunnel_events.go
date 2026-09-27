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
	// tunnelEventRetentionDays bounds how long tunnel events stay queryable: old partitions are pruned once a
	// new one opens, so the store cannot grow without bound (D22's trace query only ever needs recent history to
	// debug a path, not a permanent record). It mirrors accesslog's file adapter default retention
	// (DefaultConfig().File.KeepFiles = 30 daily files) at roughly half that, since tunnel events are
	// diagnostic, not an audit trail.
	tunnelEventRetentionDays = 14
	// maxTunnelEventLineBytes bounds one stored line. Unlike an access log record, a TunnelEvent never carries
	// request/response bodies, so a generous static ceiling (rather than a configurable limit) is enough to
	// reject a corrupt or truncated line without failing the whole query.
	maxTunnelEventLineBytes = 64 << 10
	// tunnelQueryWindowDays bounds how many UTC day partitions Query scans once it has a starting day to work
	// from (loggingTunnelEvents derives it from the tunnel ID's own UUIDv7 timestamp, D22): the day the tunnel
	// started, plus one more to cover a tunnel that keeps running past midnight or is simply long-lived. A tunnel
	// whose events span more than that still has its earlier events found (they are in the first partition
	// scanned); only events reported more than a day after the tunnel opened would be missed. That is preferred
	// over scanning the full tunnelEventRetentionDays window on every query; widen this constant if it proves too
	// tight in practice.
	tunnelQueryWindowDays = 2
	// tunnelEventStoreDefaultMaxBytes bounds the total size of every day partition the store keeps on disk,
	// independent of tunnelEventRetentionDays' age-based limit (stage 5 security review item 5): a burst of
	// tunnel activity within the retention window could otherwise still grow the store without bound. 10GiB is
	// generous for a diagnostic store, not an audit trail (see the doc comment below on why this is a bespoke
	// store at all), while still being a real ceiling instead of none.
	tunnelEventStoreDefaultMaxBytes int64 = 10 << 30
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
	mu   sync.Mutex
	dir  string
	log  *zap.Logger
	file *os.File
	day  string
	// maxBytes bounds the total size of every day partition on disk (item 5); pruneLocked evicts the oldest ones
	// once it is exceeded, on top of the age-based cutoff it already applies.
	maxBytes int64
}

func newTunnelEventStore(logDir string, log *zap.Logger) (*tunnelEventStore, error) {
	dir := filepath.Join(logDir, tunnelEventsDirName)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("create tunnel event directory: %w", err)
	}
	return &tunnelEventStore{dir: dir, log: log, maxBytes: tunnelEventStoreDefaultMaxBytes}, nil
}

func tunnelEventPath(dir, day string) string {
	return filepath.Join(dir, "events-"+day+".jsonl")
}

// Write appends one event to today's partition, opening or rotating to it as needed.
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
	if s.file == nil || day != s.day {
		if err := s.rotateLocked(day); err != nil {
			return err
		}
	}
	_, err = s.file.Write(line)
	return err
}

// rotateLocked switches the active file to day's partition, pruning partitions the retention window has aged
// out. Events are timestamped by the hop that produced them (D22), so a burst of late-arriving events from a
// slow spool can still target yesterday's file; opening files by name rather than keeping only "the current
// one" handles that without any special-casing here.
func (s *tunnelEventStore) rotateLocked(day string) error {
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(tunnelEventPath(s.dir, day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		s.file, s.day = nil, ""
		return err
	}
	s.file, s.day = file, day
	s.pruneLocked()
	return nil
}

// pruneLocked removes partitions the age-based retention window has aged out, then, if the store's total size
// still exceeds maxBytes, evicts the oldest remaining ones (by day, ascending) until it fits (item 5) — never
// the partition just opened for writing, even if that leaves the store over the cap: there is nothing older
// left to remove instead, and the file mid-write must survive regardless.
func (s *tunnelEventStore) pruneLocked() {
	cutoff := time.Now().UTC().AddDate(0, 0, -tunnelEventRetentionDays).Format("20060102")
	paths, err := filepath.Glob(filepath.Join(s.dir, "events-*.jsonl"))
	if err != nil {
		return
	}
	sort.Strings(paths) // oldest day first, so the size-based pass below evicts in the right order
	var total int64
	kept := make([]string, 0, len(paths))
	for _, path := range paths {
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		// Never prune the file just opened for writing, even if its own day label is already outside the
		// retention window (a late event, or a clock far behind): a file must survive at least the write that
		// just created or reopened it.
		if day == s.day || len(day) != 8 || day >= cutoff {
			if info, statErr := os.Stat(path); statErr == nil {
				total += info.Size()
			}
			kept = append(kept, path)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.log.Warn("prune expired tunnel event partition", zap.String("path", path), zap.Error(err))
		}
	}
	for _, path := range kept {
		if total <= s.maxBytes {
			break
		}
		day := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		if day == s.day {
			continue
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			continue
		}
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
// not a UUIDv7).
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
	return events, nil
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

func (s *tunnelEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
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
