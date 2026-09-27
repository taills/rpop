package spool

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
)

// readSegmentEnvelopes decompresses a segment file and decodes every NDJSON line into a LogEnvelope, failing
// the test on any read or parse error (a segment on disk is expected to always be one complete gzip member).
func readSegmentEnvelopes(t *testing.T, path string) []southbound.LogEnvelope {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var envelopes []southbound.LogEnvelope
	scanner := bufio.NewScanner(gz)
	scanner.Buffer(make([]byte, 0, 64<<10), 32<<20)
	for scanner.Scan() {
		var envelope southbound.LogEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("parse envelope line %q: %v", scanner.Text(), err)
		}
		envelopes = append(envelopes, envelope)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return envelopes
}

// envelopeLineSize mirrors Spool.append's own marshaling so size-threshold tests can compute an exact byte
// budget instead of guessing at one.
func envelopeLineSize(t *testing.T, kind, adapterID string, record any) int64 {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(southbound.LogEnvelope{Kind: kind, AdapterID: adapterID, Record: raw})
	if err != nil {
		t.Fatal(err)
	}
	return int64(len(line)) + 1 // the trailing '\n' append writes.
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition never became true")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *Spool) hasOpenSegment() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open != nil
}

// hasUnflushedRecords reports whether the open segment (if any) has records written since its last persist;
// tests use it to wait for a batch-limit- or interval-triggered persist to actually run before asserting on it.
func (s *Spool) hasUnflushedRecords() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.open != nil && s.open.unflushedRecords > 0
}

func testRecord(siteID string) accesslog.Record {
	return accesslog.Record{Timestamp: time.Unix(0, 0).UTC(), SiteID: siteID, Method: "GET", Path: "/", Protocol: "HTTP/1.1", Status: 200}
}

func TestSpoolCreatesNoSegmentWithoutEvents(t *testing.T) {
	dir := t.TempDir()
	s, err := newSpool(Config{Dir: dir, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected an empty spool directory, got %v", entries)
	}
}

func TestSpoolClosesSegmentAtSizeThreshold(t *testing.T) {
	dir := t.TempDir()
	lineSize := envelopeLineSize(t, southbound.LogKindAccess, "adapter-a", testRecord("site-a"))
	// A threshold just under twice one line's size means: after one line the segment stays open, after two
	// lines it must close, since the check happens after each write (see Spool.append's doc comment).
	cfg := Config{Dir: dir, MaxSegmentBytes: 2*lineSize - 1, MaxSegmentAge: time.Hour, Log: zap.NewNop()}
	s, err := newSpool(cfg, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	for i := 0; i < 3; i++ {
		if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	pending := s.Pending()
	if len(pending) != 2 {
		t.Fatalf("expected 2 segments (2 records, then 1 flushed early), got %d: %+v", len(pending), pending)
	}
	if pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("expected segments numbered 1 and 2, got %+v", pending)
	}
	first := readSegmentEnvelopes(t, segmentPath(dir, 1))
	if len(first) != 2 {
		t.Fatalf("expected segment 1 to hold 2 records, got %d", len(first))
	}
	second := readSegmentEnvelopes(t, segmentPath(dir, 2))
	if len(second) != 1 {
		t.Fatalf("expected segment 2 (closed only by Flush) to hold 1 record, got %d", len(second))
	}
	for _, envelope := range append(append([]southbound.LogEnvelope{}, first...), second...) {
		if envelope.Kind != southbound.LogKindAccess || envelope.AdapterID != "adapter-a" {
			t.Fatalf("unexpected envelope: %+v", envelope)
		}
		var record accesslog.Record
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			t.Fatal(err)
		}
		if record.SiteID != "site-a" {
			t.Fatalf("record did not round-trip: %+v", record)
		}
	}
}

func TestSpoolClosesSegmentAtMaxAge(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	cfg := Config{Dir: dir, MaxSegmentBytes: 1 << 30, MaxSegmentAge: 30 * time.Second, Log: zap.NewNop()}
	s, err := newSpool(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	// Wait for the write goroutine to actually open the segment before advancing the clock, so open.opened is
	// stamped at the pre-advance time and the age check below is meaningful rather than accidentally trivial.
	waitFor(t, time.Second, s.hasOpenSegment)

	clk.Advance(29 * time.Second)
	time.Sleep(20 * time.Millisecond) // give the loop a chance to wake on its (non-firing) timer; nothing should close yet.
	if len(s.Pending()) != 0 {
		t.Fatalf("segment closed before MaxSegmentAge elapsed: %+v", s.Pending())
	}

	clk.Advance(2 * time.Second)
	waitFor(t, time.Second, func() bool { return len(s.Pending()) == 1 })
}

func TestSpoolSegmentNumberingSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Dir: dir, MaxSegmentBytes: 1, MaxSegmentAge: time.Hour, Log: zap.NewNop()}

	a, err := newSpool(cfg, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	if err := a.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending := a.Pending(); len(pending) != 1 || pending[0].Seq != 1 {
		t.Fatalf("expected segment 1 from the first instance, got %+v", pending)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	b, err := newSpool(cfg, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close(context.Background())
	if err := b.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending := b.Pending()
	if len(pending) != 2 || pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("expected the restarted spool to continue at segment 2, got %+v", pending)
	}
}

func TestSpoolIngestQueueDropsWithoutBlockingWhenFull(t *testing.T) {
	// Constructed directly, bypassing NewSpool, so no goroutine ever drains the channel: this isolates the
	// "queue full" branch deterministically instead of racing a real write goroutine (P8).
	s := &Spool{events: make(chan spoolEvent, 2), log: zap.NewNop()}

	for i := 0; i < 2; i++ {
		if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
			t.Fatalf("unexpected drop while the queue still has room: %v", err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the full queue to report a drop")
		}
	case <-time.After(time.Second):
		t.Fatal("WriteAccessLog blocked on a full queue instead of dropping (P8)")
	}
	if s.droppedAccess.Load() != 1 {
		t.Fatalf("expected 1 dropped access log, got %d", s.droppedAccess.Load())
	}

	// RecordTunnelEvent has no error to report; only the counter reflects the drop.
	s2 := &Spool{events: make(chan spoolEvent, 1), log: zap.NewNop()}
	s2.RecordTunnelEvent(overlay.TunnelEvent{TunnelID: "t1"})
	done2 := make(chan struct{})
	go func() { s2.RecordTunnelEvent(overlay.TunnelEvent{TunnelID: "t2"}); close(done2) }()
	select {
	case <-done2:
	case <-time.After(time.Second):
		t.Fatal("RecordTunnelEvent blocked on a full queue instead of dropping (P8)")
	}
	if s2.droppedTunnel.Load() != 1 {
		t.Fatalf("expected 1 dropped tunnel event, got %d", s2.droppedTunnel.Load())
	}
}

func TestSpoolEnforcesQuotaByDroppingOldestSegments(t *testing.T) {
	dir := t.TempDir()
	lineSize := envelopeLineSize(t, southbound.LogKindAccess, "adapter-a", testRecord("site-a"))
	core, logs := observer.New(zap.WarnLevel)
	// One record per segment; a quota of roughly 2 segments' worth of compressed data forces eviction once a
	// third (or later) segment closes.
	cfg := Config{Dir: dir, MaxSegmentBytes: lineSize, MaxSegmentAge: time.Hour, QuotaBytes: 150, Log: zap.New(core)}
	s, err := newSpool(cfg, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	for i := 0; i < 5; i++ {
		if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	stats := s.Stats()
	if stats.QuotaDroppedSegments == 0 {
		t.Fatal("expected the quota to force at least one segment out")
	}
	var total int64
	for _, seg := range s.Pending() {
		total += seg.Bytes
	}
	if total > cfg.QuotaBytes {
		t.Fatalf("pending bytes %d exceed quota %d after enforcement", total, cfg.QuotaBytes)
	}
	pending := s.Pending()
	for i := 1; i < len(pending); i++ {
		if pending[i-1].Seq >= pending[i].Seq {
			t.Fatalf("expected ascending segment numbers, got %+v", pending)
		}
	}
	// The oldest segment (1) must be the one gone, not a newer one.
	if len(pending) > 0 && pending[0].Seq == 1 {
		t.Fatalf("expected the oldest segment to be evicted first, still have it: %+v", pending)
	}
	if logs.FilterMessage("spool over quota; dropped its oldest unacknowledged segment").Len() == 0 {
		t.Fatal("expected a warning log about the quota eviction")
	}
}

func TestSpoolRecoversTruncatedSegmentKeepingCompleteLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Segment 1: a normal, fully closed segment, present alongside the crashed one.
	writer, err := createSegmentWriter(dir, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	line1 := mustEnvelopeLine(t, southbound.LogKindAccess, "adapter-a", testRecord("site-a"))
	if err := writer.write(line1); err != nil {
		t.Fatal(err)
	}
	if err := writer.close(); err != nil {
		t.Fatal(err)
	}

	// Segment 2: two lines written and persisted (Flush+fsync, as the write goroutine does once per batch),
	// then abandoned without ever calling close, so the gzip stream on disk has no final trailer - exactly what
	// a crash right after a batch's persist, but before the segment closes, leaves behind.
	crashed, err := createSegmentWriter(dir, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	line2a := mustEnvelopeLine(t, southbound.LogKindTunnel, "", overlay.TunnelEvent{TunnelID: "kept-1"})
	line2b := mustEnvelopeLine(t, southbound.LogKindTunnel, "", overlay.TunnelEvent{TunnelID: "kept-2"})
	if err := crashed.write(line2a); err != nil {
		t.Fatal(err)
	}
	if err := crashed.write(line2b); err != nil {
		t.Fatal(err)
	}
	if err := crashed.persist(); err != nil {
		t.Fatal(err)
	}
	// crashed.file is intentionally left open and never closed/finalized: simulating the process dying here.

	if err := saveState(dir, spoolState{NextSegment: 3}); err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zap.WarnLevel)
	s, err := newSpool(Config{Dir: dir, MaxSegmentAge: time.Hour, Log: zap.New(core)}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	_ = logs

	pending := s.Pending()
	if len(pending) != 2 || pending[0].Seq != 1 || pending[1].Seq != 2 {
		t.Fatalf("expected both segment 1 and the salvaged segment 2, got %+v", pending)
	}
	recovered := readSegmentEnvelopes(t, segmentPath(dir, 2))
	if len(recovered) != 2 {
		t.Fatalf("expected the salvaged segment to keep exactly its 2 complete lines, got %d", len(recovered))
	}
	var event1, event2 overlay.TunnelEvent
	if err := json.Unmarshal(recovered[0].Record, &event1); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(recovered[1].Record, &event2); err != nil {
		t.Fatal(err)
	}
	if event1.TunnelID != "kept-1" || event2.TunnelID != "kept-2" {
		t.Fatalf("salvaged lines do not match what was written: %+v %+v", event1, event2)
	}

	// A fresh segment must not reuse number 2, regardless of the crash: the next one allocated is 3.
	if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending = s.Pending()
	if len(pending) != 3 || pending[2].Seq != 3 {
		t.Fatalf("expected the next segment to be numbered 3, got %+v", pending)
	}
}

func TestSpoolDropsSegmentWithNoCompleteLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// A gzip member whose header alone is on disk: it can't even fully decode its first line, so recovery must
	// delete it outright rather than "salvage" nothing.
	path := segmentPath(dir, 1)
	if err := os.WriteFile(path, []byte{0x1f, 0x8b, 0x08, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := saveState(dir, spoolState{NextSegment: 2}); err != nil {
		t.Fatal(err)
	}

	core, logs := observer.New(zap.WarnLevel)
	s, err := newSpool(Config{Dir: dir, MaxSegmentAge: time.Hour, Log: zap.New(core)}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	if len(s.Pending()) != 0 {
		t.Fatalf("expected the unsalvageable segment to be gone, got %+v", s.Pending())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected segment file to be deleted, stat error: %v", err)
	}
	if logs.FilterMessage("dropped an incomplete spool segment left by a crash").Len() == 0 {
		t.Fatal("expected a warning log about the dropped segment")
	}
}

func mustEnvelopeLine(t *testing.T, kind, adapterID string, record any) []byte {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(southbound.LogEnvelope{Kind: kind, AdapterID: adapterID, Record: raw})
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

// TestSpoolBatchedWritesPreserveOrderAndCompleteness writes a run of records - some counts under the batch
// limit, some crossing it one or more times - and checks that batching (drainBatch persisting every
// maxBatchRecords/maxBatchBytes instead of every line) never drops, duplicates, or reorders a record. Each
// record's Path carries its own index so a mismatch anywhere points straight at which record went missing or
// moved.
func TestSpoolBatchedWritesPreserveOrderAndCompleteness(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"singleRecord", 1},
		{"underBatchLimit", 10},
		{"exactlyOneBatchLimit", maxBatchRecords},
		{"justOverOneBatchLimit", maxBatchRecords + 1},
		{"severalBatchLimitsWorth", 3*maxBatchRecords + 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// MaxSegmentBytes/MaxSegmentAge are both large enough that nothing rotates mid-test: every record
			// must land in the single segment Flush closes at the end, so completeness/order is easy to check.
			cfg := Config{Dir: dir, MaxSegmentBytes: 1 << 30, MaxSegmentAge: time.Hour, Log: zap.NewNop()}
			s, err := newSpool(cfg, realClock{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())

			for i := 0; i < tc.count; i++ {
				record := testRecord("site-a")
				record.Path = fmt.Sprintf("/%d", i)
				for s.WriteAccessLog(context.Background(), "adapter-a", record) != nil {
					// The queue is momentarily full while the write goroutine catches up; retry rather than
					// treat this as a real drop, since the test's whole point is that nothing gets dropped.
				}
			}
			if err := s.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}

			pending := s.Pending()
			if len(pending) != 1 {
				t.Fatalf("expected everything in a single segment, got %+v", pending)
			}
			envelopes := readSegmentEnvelopes(t, segmentPath(dir, pending[0].Seq))
			if len(envelopes) != tc.count {
				t.Fatalf("expected %d records, got %d", tc.count, len(envelopes))
			}
			for i, envelope := range envelopes {
				var record accesslog.Record
				if err := json.Unmarshal(envelope.Record, &record); err != nil {
					t.Fatal(err)
				}
				if want := fmt.Sprintf("/%d", i); record.Path != want {
					t.Fatalf("record %d out of order, duplicated, or missing: want path %q, got %q", i, want, record.Path)
				}
			}
		})
	}
}

// TestSpoolPersistIntervalFlushesWithoutClosingSegment checks the second trigger for a persist (alongside the
// batch-size limits): MaxPersistInterval, which bounds how long a slow trickle of records - never enough to
// fill a batch - can sit unflushed. It reads the segment file directly (independently of the Spool, which still
// considers the segment open) to confirm the record actually reached disk, the same way a real crash
// immediately afterwards would leave it: decodable up to that point, with no gzip trailer since the segment
// was never closed.
func TestSpoolPersistIntervalFlushesWithoutClosingSegment(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	cfg := Config{Dir: dir, MaxSegmentBytes: 1 << 30, MaxSegmentAge: time.Hour, MaxPersistInterval: 2 * time.Second, Log: zap.NewNop()}
	s, err := newSpool(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	record := testRecord("site-a")
	if err := s.WriteAccessLog(context.Background(), "adapter-a", record); err != nil {
		t.Fatal(err)
	}
	// Wait for the write goroutine to fully account for the line (not just open the segment - hasOpenSegment
	// can turn true a moment before the record's own byte/record counters are updated) before moving the fake
	// clock forward, so the persist timer below is armed against the correct lastPersist.
	waitFor(t, time.Second, s.hasUnflushedRecords)

	path := segmentPath(dir, 1)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decompressBestEffort(before); err == nil {
		t.Fatal("expected the unpersisted record to not even decode yet")
	}

	clk.Advance(2*time.Second + time.Millisecond)
	waitFor(t, time.Second, func() bool { return !s.hasUnflushedRecords() })

	// Still open (persist does not close), but the bytes must now be durably on disk: decodable up to the one
	// line written, with an error only because the gzip stream has no trailer yet (recoverSegment's normal
	// "truncated" case).
	if len(s.Pending()) != 0 {
		t.Fatalf("expected the persist to not close the segment, got %+v", s.Pending())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data, decodeErr := decompressBestEffort(after)
	if decodeErr == nil {
		t.Fatal("expected a still-open segment to lack a gzip trailer")
	}
	lines := completeLines(data)
	if len(lines) == 0 {
		t.Fatal("expected the interval-triggered persist to have flushed the record's complete line to disk")
	}
	var envelope southbound.LogEnvelope
	if err := json.Unmarshal(bytes.TrimRight(lines, "\n"), &envelope); err != nil {
		t.Fatalf("recovered line does not parse: %v", err)
	}
	if envelope.Kind != southbound.LogKindAccess || envelope.AdapterID != "adapter-a" {
		t.Fatalf("unexpected envelope persisted by the interval timer: %+v", envelope)
	}
}

// TestSpoolRecoversOnlyThePersistedBatchAtCrash simulates a crash that lands between two batches: the first
// batch was written and persisted (Flush+fsync), the second was written but never persisted before the
// process died. Recovery must keep exactly the first batch and lose the second one whole - not a truncated
// remainder of it - since unpersisted bytes never reached disk in the first place (docs/architecture/
// control-data-plane.md §5, "阶段 5 第 2 步").
func TestSpoolRecoversOnlyThePersistedBatchAtCrash(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := createSegmentWriter(dir, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	persistedLines := []string{"persisted-1", "persisted-2", "persisted-3"}
	for _, id := range persistedLines {
		if err := writer.write(mustEnvelopeLine(t, southbound.LogKindTunnel, "", overlay.TunnelEvent{TunnelID: id})); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.persist(); err != nil {
		t.Fatal(err)
	}
	// The crash lands here: a second batch is written to the same still-open segment but never persisted, and
	// the process (this test) abandons the writer without ever calling close.
	lostLines := []string{"lost-1", "lost-2"}
	for _, id := range lostLines {
		if err := writer.write(mustEnvelopeLine(t, southbound.LogKindTunnel, "", overlay.TunnelEvent{TunnelID: id})); err != nil {
			t.Fatal(err)
		}
	}

	if err := saveState(dir, spoolState{NextSegment: 2}); err != nil {
		t.Fatal(err)
	}

	s, err := newSpool(Config{Dir: dir, MaxSegmentAge: time.Hour, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())

	pending := s.Pending()
	if len(pending) != 1 || pending[0].Seq != 1 {
		t.Fatalf("expected the salvaged segment 1, got %+v", pending)
	}
	recovered := readSegmentEnvelopes(t, segmentPath(dir, 1))
	if len(recovered) != len(persistedLines) {
		t.Fatalf("expected exactly the %d persisted lines, got %d", len(persistedLines), len(recovered))
	}
	for i, envelope := range recovered {
		var event overlay.TunnelEvent
		if err := json.Unmarshal(envelope.Record, &event); err != nil {
			t.Fatal(err)
		}
		if event.TunnelID != persistedLines[i] {
			t.Fatalf("recovered line %d = %q, want %q (batch reordered or corrupted)", i, event.TunnelID, persistedLines[i])
		}
	}
}

// BenchmarkSpoolWrite measures sustained WriteAccessLog throughput end to end: enqueue plus the write
// goroutine's actual disk-bound drain (retrying past a momentarily full queue keeps the loop paced by the
// drain rate instead of just measuring how fast records can be dropped once the queue saturates). Compare
// against the pre-batching implementation (fsync per line) to quantify the effect of the batch persistence
// change (docs/architecture/control-data-plane.md §5, "阶段 5 第 2 步").
func BenchmarkSpoolWrite(b *testing.B) {
	dir := b.TempDir()
	cfg := Config{Dir: dir, MaxSegmentBytes: 1 << 30, MaxSegmentAge: time.Hour, Log: zap.NewNop()}
	s, err := newSpool(cfg, realClock{})
	if err != nil {
		b.Fatal(err)
	}
	record := testRecord("site-a")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for s.WriteAccessLog(context.Background(), "adapter-a", record) != nil {
			// The queue is momentarily full; the write goroutine is the bottleneck, so retry instead of
			// counting a dropped attempt as an iteration (P8 never blocks WriteAccessLog itself).
		}
	}
	if err := s.Flush(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.StopTimer()
	if err := s.Close(context.Background()); err != nil {
		b.Fatal(err)
	}
}
