package spool

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/southbound"
)

// fakeController is a minimal southbound logs endpoint for end-to-end uploader tests: it tracks a
// high-water mark like the real controller (D24) and can be told to fail the next few requests.
type fakeController struct {
	mu       sync.Mutex
	hwm      uint64
	fail     int // remaining requests to answer with 500 before accepting again.
	requests int
	received map[uint64][]southbound.LogEnvelope
	// forceAck overrides the acknowledged value the handler returns, for exercising the "controller HWM ahead
	// of what the node just sent" case (D24).
	forceAck uint64
}

func newFakeController() *fakeController {
	return &fakeController{received: make(map[uint64][]southbound.LogEnvelope)}
}

func (f *fakeController) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *fakeController) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		if f.fail > 0 {
			f.fail--
			f.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		f.mu.Unlock()

		if r.URL.Path != southbound.LogsPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("expected Content-Encoding: gzip, got %q", r.Header.Get("Content-Encoding"))
		}
		seq, err := strconv.ParseUint(r.Header.Get(southbound.LogSegmentHeader), 10, 64)
		if err != nil {
			t.Errorf("invalid %s header: %v", southbound.LogSegmentHeader, err)
		}
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Errorf("segment %d did not decompress: %v", seq, err)
		}
		var envelopes []southbound.LogEnvelope
		scanner := bufio.NewScanner(gz)
		for scanner.Scan() {
			var envelope southbound.LogEnvelope
			if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
				t.Errorf("segment %d: invalid envelope line: %v", seq, err)
			}
			envelopes = append(envelopes, envelope)
		}

		f.mu.Lock()
		f.received[seq] = envelopes
		if seq > f.hwm {
			f.hwm = seq
		}
		ack := f.hwm
		if f.forceAck > 0 {
			ack = f.forceAck
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(southbound.LogAck{Ack: ack})
	}
}

func TestUploaderDeliversSegmentsInOrderAndAcksThemAway(t *testing.T) {
	dir := t.TempDir()
	lineSize := envelopeLineSize(t, southbound.LogKindAccess, "adapter-a", testRecord("site-a"))
	s, err := newSpool(Config{Dir: dir, MaxSegmentBytes: lineSize, MaxSegmentAge: time.Hour, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	for i := 0; i < 3; i++ {
		record := testRecord(fmt.Sprintf("site-%d", i))
		if err := s.WriteAccessLog(context.Background(), "adapter-a", record); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pending := s.Pending(); len(pending) != 3 {
		t.Fatalf("expected 3 segments queued for upload, got %+v", pending)
	}

	controller := newFakeController()
	server := httptest.NewServer(controller.handler(t))
	defer server.Close()

	uploader := newUploader(s, UploaderConfig{Endpoint: server.URL + southbound.LogsPath, Client: server.Client, Log: zap.NewNop()}, realClock{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() { uploader.Run(ctx); close(runDone) }()

	waitFor(t, 2*time.Second, func() bool { return s.AckedUpTo() == 3 })
	if pending := s.Pending(); len(pending) != 0 {
		t.Fatalf("expected every acknowledged segment to be deleted locally, still have %+v", pending)
	}
	if controller.requestCount() != 3 {
		t.Fatalf("expected exactly 3 upload requests (one per segment), got %d", controller.requestCount())
	}
	for seq := uint64(1); seq <= 3; seq++ {
		envelopes := controller.received[seq]
		if len(envelopes) != 1 {
			t.Fatalf("segment %d: expected 1 record, got %d", seq, len(envelopes))
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit promptly after ctx was canceled")
	}
}

func TestUploaderSkipsAheadWhenControllerAckIsAheadOfEverySegmentSent(t *testing.T) {
	dir := t.TempDir()
	s, err := newSpool(Config{Dir: dir, MaxSegmentBytes: 1, MaxSegmentAge: time.Hour, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The controller claims a high-water mark far beyond the single segment (1) this freshly reset spool is
	// about to send - the "node reinstalled, controller remembers a much higher HWM" case (D24).
	controller := newFakeController()
	controller.forceAck = 500
	server := httptest.NewServer(controller.handler(t))
	defer server.Close()

	uploader := newUploader(s, UploaderConfig{Endpoint: server.URL + southbound.LogsPath, Client: server.Client, Log: zap.NewNop()}, realClock{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go uploader.Run(ctx)

	waitFor(t, 2*time.Second, func() bool { return s.AckedUpTo() == 500 })
	if pending := s.Pending(); len(pending) != 0 {
		t.Fatalf("expected segment 1 to be deleted once it fell under the new ack floor, got %+v", pending)
	}

	// The next segment created must skip straight past the controller's HWM, not continue at 2.
	if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-b")); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending := s.Pending()
	if len(pending) != 1 || pending[0].Seq != 501 {
		t.Fatalf("expected the next segment to be numbered 501, got %+v", pending)
	}
}

func TestUploaderRetriesWithBackoffAfterFailuresThenSucceeds(t *testing.T) {
	dir := t.TempDir()
	s, err := newSpool(Config{Dir: dir, MaxSegmentBytes: 1, MaxSegmentAge: time.Hour, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err := s.WriteAccessLog(context.Background(), "adapter-a", testRecord("site-a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	controller := newFakeController()
	controller.fail = 2
	server := httptest.NewServer(controller.handler(t))
	defer server.Close()

	clk := newFakeClock(time.Unix(1_700_000_000, 0))
	armed := make(chan struct{}, 4)
	clk.notifyArmed(armed)
	uploader := newUploader(s, UploaderConfig{Endpoint: server.URL + southbound.LogsPath, Client: server.Client, Log: zap.NewNop()}, clk)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go uploader.Run(ctx)

	for expected := 1; expected <= 2; expected++ {
		waitFor(t, time.Second, func() bool { return controller.requestCount() >= expected })
		// Wait for the uploader to have actually registered its next backoff wait with clk before advancing it:
		// polling LastError (or requestCount again) here would race the goroutine, since both can already be
		// true from the *previous* failure and say nothing about whether this one's retry timer is armed yet.
		select {
		case <-armed:
		case <-time.After(time.Second):
			t.Fatal("uploader never armed its retry backoff timer")
		}
		// Safe to check now, unlike before armed fired: setLastErr happens-before the arming select receives
		// from clk.After in Run's own goroutine, and the channel send/receive above carries that ordering over
		// to this goroutine.
		if uploader.LastError() == "" {
			t.Fatalf("LastError still empty right after registering retry #%d's backoff wait", expected)
		}
		// Whatever backoff was chosen is capped at uploadMaxBackoff; advancing well past it always unblocks it.
		clk.Advance(2 * uploadMaxBackoff)
	}

	waitFor(t, time.Second, func() bool { return s.AckedUpTo() == 1 })
	if controller.requestCount() != 3 {
		t.Fatalf("expected 2 failures then 1 success, got %d requests", controller.requestCount())
	}
	if uploader.LastError() != "" {
		t.Fatalf("expected LastError to clear after a successful upload, got %q", uploader.LastError())
	}
}

func TestUploaderExitsPromptlyWhenIdleAndContextIsCanceled(t *testing.T) {
	dir := t.TempDir()
	s, err := newSpool(Config{Dir: dir, Log: zap.NewNop()}, realClock{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	uploader := newUploader(s, UploaderConfig{Endpoint: "http://127.0.0.1:0/unused", Client: func() *http.Client { return http.DefaultClient }, Log: zap.NewNop()}, realClock{})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() { uploader.Run(ctx); close(runDone) }()
	cancel()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit promptly while idle and ctx was canceled")
	}
}
