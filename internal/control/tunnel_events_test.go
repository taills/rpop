package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/overlay"
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

	got, err := store.Query(ctx, "tunnel-a")
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

	got, err = store.Query(ctx, "tunnel-missing")
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

	got, err := store.Query(ctx, "ancient")
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

func TestLocalTunnelEventWriterWritesThrough(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writer := localTunnelEventWriter{store: store, log: zap.NewNop()}
	writer.RecordTunnelEvent(overlay.TunnelEvent{Timestamp: time.Now(), TunnelID: "local-tunnel", NodeID: "local"})
	got, err := store.Query(context.Background(), "local-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %#v, want one event", got)
	}
}
