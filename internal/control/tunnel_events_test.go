package control

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/store"
)

func TestTunnelEventStoreWriteAndQuery(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	events := []overlay.TunnelEvent{
		{Timestamp: base.Add(2 * time.Second), TunnelID: "tunnel-a", NodeID: "exit-1", Role: overlay.RoleExit, Stage: overlay.StageEnded},
		{Timestamp: base, TunnelID: "tunnel-a", NodeID: "entry-1", Role: overlay.RoleEntry, Stage: overlay.StageArrived},
		{Timestamp: base.Add(time.Second), TunnelID: "tunnel-a", NodeID: "relay-1", Role: overlay.RoleRelay, Stage: overlay.StageEstablished},
		{Timestamp: base, TunnelID: "tunnel-b", NodeID: "entry-1", Role: overlay.RoleEntry, Stage: overlay.StageArrived},
	}
	for _, event := range events {
		if err := store.Write(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Query(ctx, "tunnel-a", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("Query(tunnel-a) returned %d events, want 3: %#v", len(got), got)
	}
	wantOrder := []string{"entry-1", "relay-1", "exit-1"}
	for i, event := range got {
		if event.NodeID != wantOrder[i] {
			t.Fatalf("event %d node = %q, want %q (events must sort by timestamp): %#v", i, event.NodeID, wantOrder[i], got)
		}
	}

	got, err = store.Query(ctx, "tunnel-missing", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Query(tunnel-missing) = %#v, want empty", got)
	}
}

func TestTunnelEventStorePartitionsByDayAndPrunesOldOnes(t *testing.T) {
	dir := t.TempDir()
	store, err := newTunnelEventStore(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -(tunnelEventRetentionDays + 5))
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: old, TunnelID: "ancient", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	oldPath := tunnelEventPath(filepath.Join(dir, tunnelEventsDirName), old.Format("20060102"))
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected old partition file to exist right after writing: %v", err)
	}

	// Writing today's event opens a new partition and, in doing so, prunes the one beyond the retention window.
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "fresh", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected the expired partition to be pruned, stat err = %v", err)
	}

	got, err := store.Query(ctx, "ancient", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pruned tunnel events must not be queryable, got %#v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestTunnelEventStoreQueryWithAStartLimitsToItsWindow covers the query-narrowing this step adds: with a
// non-zero start, Query must only look at tunnelQueryWindowDays partitions from start's UTC day, even though the
// tunnel ID is the same in every partition and an unbounded scan would find all of them.
func TestTunnelEventStoreQueryWithAStartLimitsToItsWindow(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	// Far enough in the future that none of these partitions can ever fall inside the retention window's
	// "older than tunnelEventRetentionDays" cutoff and get pruned out from under the test, regardless of when it
	// actually runs.
	start := time.Date(2035, 6, 1, 0, 0, 0, 0, time.UTC)
	inWindow := []time.Time{start, start.AddDate(0, 0, tunnelQueryWindowDays-1).Add(2 * time.Hour)}
	outOfWindow := start.AddDate(0, 0, tunnelQueryWindowDays).Add(2 * time.Hour)
	for _, ts := range inWindow {
		if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: ts, TunnelID: "tunnel-window", NodeID: "in-window"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: outOfWindow, TunnelID: "tunnel-window", NodeID: "out-of-window"}); err != nil {
		t.Fatal(err)
	}

	windowed, err := store.Query(ctx, "tunnel-window", start)
	if err != nil {
		t.Fatal(err)
	}
	if len(windowed) != len(inWindow) {
		t.Fatalf("windowed Query returned %d events, want %d (the partition beyond tunnelQueryWindowDays must not be scanned): %#v",
			len(windowed), len(inWindow), windowed)
	}
	for _, event := range windowed {
		if event.NodeID == "out-of-window" {
			t.Fatalf("windowed Query must not return an event from beyond tunnelQueryWindowDays: %#v", windowed)
		}
	}

	full, err := store.Query(ctx, "tunnel-window", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != len(inWindow)+1 {
		t.Fatalf("full scan (zero start) returned %d events, want %d (every partition)", len(full), len(inWindow)+1)
	}
}

func TestReadTunnelEventsSkipsOversizedAndCorruptLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	oversized := make([]byte, maxTunnelEventLineBytes+10)
	for i := range oversized {
		oversized[i] = 'a'
	}
	content := `{"tunnelId":"t1","nodeId":"n1"}` + "\n" +
		"not json\n" +
		string(oversized) + "\n" +
		`{"tunnelId":"t1","nodeId":"n2"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
	events, err := readTunnelEvents(path, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].NodeID != "n1" || events[1].NodeID != "n2" {
		t.Fatalf("events = %#v, want two events from n1 and n2 with the bad lines skipped", events)
	}
}

// TestTunnelEventStoreEvictsOldestPartitionsOverItsSizeCap covers stage 5 security review item 5: on top of
// tunnelEventRetentionDays' age-based limit, the store must not grow without bound within that window either.
// Capping it to a little over one day's partition forces the next day's rotation to evict the oldest one.
func TestTunnelEventStoreEvictsOldestPartitionsOverItsSizeCap(t *testing.T) {
	dir := t.TempDir()
	store, err := newTunnelEventStore(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Far enough in the future that neither partition can ever fall inside the retention window's "older than
	// tunnelEventRetentionDays" cutoff and get age-pruned instead, regardless of when this test actually runs.
	base := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	write := func(day int, tunnelID string) {
		t.Helper()
		if err := store.Write(context.Background(), overlay.TunnelEvent{Timestamp: base.AddDate(0, 0, day), TunnelID: tunnelID, NodeID: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	write(0, "day-0")
	firstPath := tunnelEventPath(filepath.Join(dir, tunnelEventsDirName), base.Format("20060102"))
	info, err := os.Stat(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	// Below day 0's own size: pruneLocked runs right after day 1's (still-empty) file is opened but before its
	// line is written, so the cap must already exclude day 0 alone to force its eviction on this rotation.
	store.mu.Lock()
	store.maxBytes = info.Size() - 1
	store.mu.Unlock()

	write(1, "day-1")
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("expected day 0's partition to be evicted over the size cap, stat err = %v", err)
	}
	got, err := store.Query(context.Background(), "day-0", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("evicted tunnel events must not be queryable, got %#v", got)
	}
	got, err = store.Query(context.Background(), "day-1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("day 1's own partition must survive (it is the one just opened for writing), got %#v", got)
	}
}

// TestSetTunnelEventStoreCapacityOverridesTheDefault covers the Control-level knob for item 5's size cap: a
// positive override replaces the default and sticks, a non-positive one is a no-op that leaves whatever is
// already set in place, and calling it on a Control with no tunnel event store (the plain New() constructor)
// must not panic.
func TestSetTunnelEventStoreCapacityOverridesTheDefault(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	c, err := NewWithLogDir(store.New(db), zap.NewNop(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseAccessLogs(context.Background())

	c.SetTunnelEventStoreCapacity(12345)
	if c.tunnelEvents.maxBytes != 12345 {
		t.Fatalf("maxBytes = %d, want 12345", c.tunnelEvents.maxBytes)
	}
	c.SetTunnelEventStoreCapacity(0)
	if c.tunnelEvents.maxBytes != 12345 {
		t.Fatalf("maxBytes after a non-positive override = %d, want unchanged at 12345", c.tunnelEvents.maxBytes)
	}

	plain := New(store.New(db), zap.NewNop())
	plain.SetTunnelEventStoreCapacity(999)
}

func TestLocalTunnelEventWriterWritesThrough(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := localTunnelEventWriter{store: store, log: zap.NewNop()}
	writer.RecordTunnelEvent(overlay.TunnelEvent{Timestamp: time.Now(), TunnelID: "local-tunnel", NodeID: "local"})
	got, err := store.Query(context.Background(), "local-tunnel", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %#v, want one event", got)
	}
}
