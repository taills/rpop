package spool

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
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

	// Segment 2: two lines flushed to disk (each write fsyncs), then abandoned without ever calling close, so
	// the gzip stream on disk has no final trailer - exactly what a crash mid-segment leaves behind.
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
