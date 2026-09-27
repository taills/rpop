package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/dataplane"
	"github.com/rpop-project/rpop/internal/southbound"
)

// TestClockRoundTimestampsOffsetAndRTT covers D28's NTP formulas directly: symmetric delay with the node's clock
// running fast (offset alone reveals the skew, with the correct sign), symmetric delay with the node's clock
// running slow, and asymmetric delay (RTT still comes out right even though neither leg alone would). Every case's
// timestamps are derived from an explicit physical scenario (one-way delays plus a real clock error ε) so the
// expected offset is checked against ε directly, not against whatever offsetAndRTT happens to compute — see the
// stage 7 review's clock-skew sign fix (docs/architecture/control-data-plane.md §5).
func TestClockRoundTimestampsOffsetAndRTT(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		round      clockRoundTimestamps
		wantOffset time.Duration
		wantRTT    time.Duration
	}{
		{
			// Node's clock runs ε=+200ms fast; symmetric one-way delay d=50ms each way; controller processes
			// instantly. Modeling the controller's clock as the reference (error 0) and the node's as reading
			// true_time+ε: t0=sentAt=true_send+ε=0+200=200ms; t1=receivedAt=true_send+d=0+50=50ms (controller's
			// clock, no error); t2=respondedAt=true_send+d=50ms (instant processing); t3=gotResponseAt=
			// true_send+2d+ε=0+100+200=300ms. offset=((t0−t1)+(t3−t2))/2=((200−50)+(300−50))/2=200ms=ε; rtt=2d=100ms.
			name: "symmetric delay reveals a positive offset when the node's clock runs fast",
			round: clockRoundTimestamps{
				sentAt: base.Add(200 * time.Millisecond), gotResponseAt: base.Add(300 * time.Millisecond),
				receivedAt: base.Add(50 * time.Millisecond), respondedAt: base.Add(50 * time.Millisecond),
			},
			wantOffset: 200 * time.Millisecond,
			wantRTT:    100 * time.Millisecond,
		},
		{
			// Node's clock runs ε=−150ms slow; symmetric one-way delay d=30ms each way. t0=true_send+ε=0−150=
			// −150ms; t1=true_send+d=30ms; t2=true_send+d=30ms; t3=true_send+2d+ε=0+60−150=−90ms.
			// offset=((−150−30)+(−90−30))/2=−150ms=ε; rtt=2d=60ms.
			name: "symmetric delay reveals a negative offset when the node's clock runs slow",
			round: clockRoundTimestamps{
				sentAt: base.Add(-150 * time.Millisecond), gotResponseAt: base.Add(-90 * time.Millisecond),
				receivedAt: base.Add(30 * time.Millisecond), respondedAt: base.Add(30 * time.Millisecond),
			},
			wantOffset: -150 * time.Millisecond,
			wantRTT:    60 * time.Millisecond,
		},
		{
			// Asymmetric delay (request leg 80ms, response leg 20ms) with zero real clock offset (ε=0): NTP's own
			// well-known limitation is that the offset formula assumes a symmetric round trip, so an asymmetric one
			// biases the estimate away from the true value (here: ((0−80)+(100−80))/2 = −30ms) even though RTT
			// itself comes out exactly right regardless of the asymmetry.
			name: "asymmetric delay biases the offset but not the RTT",
			round: clockRoundTimestamps{
				sentAt: base, gotResponseAt: base.Add(100 * time.Millisecond),
				receivedAt: base.Add(80 * time.Millisecond), respondedAt: base.Add(80 * time.Millisecond),
			},
			wantOffset: -30 * time.Millisecond,
			wantRTT:    100 * time.Millisecond,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offset, rtt, ok := tt.round.offsetAndRTT()
			if !ok {
				t.Fatal("offsetAndRTT() ok = false, want true for a complete round")
			}
			if offset != tt.wantOffset {
				t.Errorf("offset = %v, want %v", offset, tt.wantOffset)
			}
			if rtt != tt.wantRTT {
				t.Errorf("rtt = %v, want %v", rtt, tt.wantRTT)
			}
		})
	}
}

// TestClockRoundTimestampsIncompleteRoundIsNotOK covers the zero value and every partially-filled round: none of
// them may report ok, since status() must omit ClockOffsetMillis/ClockRTTMillis entirely rather than reporting a
// bogus zero.
func TestClockRoundTimestampsIncompleteRoundIsNotOK(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		round clockRoundTimestamps
	}{
		{"zero value (first heartbeat, or a controller that predates D28)", clockRoundTimestamps{}},
		{"missing sentAt", clockRoundTimestamps{gotResponseAt: now, receivedAt: now, respondedAt: now}},
		{"missing gotResponseAt", clockRoundTimestamps{sentAt: now, receivedAt: now, respondedAt: now}},
		{"missing receivedAt", clockRoundTimestamps{sentAt: now, gotResponseAt: now, respondedAt: now}},
		{"missing respondedAt", clockRoundTimestamps{sentAt: now, gotResponseAt: now, receivedAt: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := tt.round.offsetAndRTT(); ok {
				t.Fatal("offsetAndRTT() ok = true, want false for an incomplete round")
			}
		})
	}
}

// TestPostJSONTreats204AsNoBodyEvenWithANonNilOut covers D28's compatibility half for a new node talking to a
// controller that predates D28 (or any other endpoint that legitimately answers 204): sendStatus always passes a
// non-nil *southbound.StatusResponse, and a 204 must not surface as a decode error, only as "this round carried
// no data" (the zero value).
func TestPostJSONTreats204AsNoBodyEvenWithANonNilOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var out southbound.StatusResponse
	err := postJSON(context.Background(), server.Client(), server.URL, southbound.Status{Version: "v"}, &out)
	if err != nil {
		t.Fatalf("postJSON() error = %v, want nil (a 204 must not surface as a decode error)", err)
	}
	if !out.ReceivedAt.IsZero() || !out.RespondedAt.IsZero() {
		t.Fatalf("out = %#v, want the zero value on a 204", out)
	}
}

// TestSendStatusHandlesAControllerThatAnswers204 covers sendStatus end to end against exactly that stand-in for
// a pre-D28 controller: it must not return an error, and must leave lastClockRound at the zero value so the next
// status() omits ClockOffsetMillis/ClockRTTMillis rather than reporting stale or fabricated data.
func TestSendStatusHandlesAControllerThatAnswers204(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{Version: "test"}, base: base, engine: dataplane.New(zap.NewNop()), kick: make(chan struct{}, 1)}
	a.current.Store(&session{client: server.Client()})
	if err := a.sendStatus(context.Background()); err != nil {
		t.Fatalf("sendStatus() error = %v", err)
	}
	if _, _, ok := a.lastClockRound.offsetAndRTT(); ok {
		t.Fatal("lastClockRound reported a complete round after a 204 answer")
	}
}

// TestStatusReportsThePreviousRoundsOffsetNotTheCurrentOnes covers the "next heartbeat" shape of D28 end to end:
// a round's own answer necessarily arrives after status() already built the request that produced it, so the
// offset a status report carries must be the previous round's, never one derived from data status() could not
// yet have. The stand-in controller always answers with the same fixed (ReceivedAt, RespondedAt), so the second
// sendStatus's request is the first place an offset can legitimately appear at all.
func TestStatusReportsThePreviousRoundsOffsetNotTheCurrentOnes(t *testing.T) {
	receivedAt := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	respondedAt := receivedAt.Add(10 * time.Millisecond)
	var (
		mu              sync.Mutex
		reportedOffsets []*int64
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var status southbound.Status
		if err := json.NewDecoder(r.Body).Decode(&status); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		reportedOffsets = append(reportedOffsets, status.ClockOffsetMillis)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(southbound.StatusResponse{ReceivedAt: receivedAt, RespondedAt: respondedAt})
	}))
	defer server.Close()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{cfg: Config{Version: "test"}, base: base, engine: dataplane.New(zap.NewNop()), kick: make(chan struct{}, 1)}
	a.current.Store(&session{client: server.Client()})

	if err := a.sendStatus(context.Background()); err != nil {
		t.Fatalf("first sendStatus() error = %v", err)
	}
	if err := a.sendStatus(context.Background()); err != nil {
		t.Fatalf("second sendStatus() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reportedOffsets) != 2 {
		t.Fatalf("controller saw %d requests, want 2", len(reportedOffsets))
	}
	if reportedOffsets[0] != nil {
		t.Fatalf("first request's ClockOffsetMillis = %v, want nil (no previous round exists yet)", *reportedOffsets[0])
	}
	if reportedOffsets[1] == nil {
		t.Fatal("second request's ClockOffsetMillis = nil, want the first round's computed offset")
	}
}
