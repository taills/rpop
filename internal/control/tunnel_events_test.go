package control

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// TestTunnelEventStoreWriteDedupesRetransmittedEvents covers D29's main scenario directly: the same event
// (identical tunnelId+nodeId+stage) written twice, as a retransmitted segment would (D24), must be stored once.
func TestTunnelEventStoreWriteDedupesRetransmittedEvents(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	event := overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "tunnel-retransmit", NodeID: "entry-1", Role: overlay.RoleEntry, Stage: overlay.StageArrived}
	if err := store.Write(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, event); err != nil {
		t.Fatal(err)
	}
	got, err := store.Query(ctx, "tunnel-retransmit", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d events, want 1 (the retransmitted duplicate must be dropped): %#v", len(got), got)
	}
}

// TestTunnelEventStoreWriteDoesNotDedupeDifferentStages sanity-checks that DedupKey's stage component actually
// matters: two events for the same tunnel/node but different lifecycle stages are not duplicates and must both
// be stored.
func TestTunnelEventStoreWriteDoesNotDedupeDifferentStages(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "tunnel-stages", NodeID: "entry-1", Role: overlay.RoleEntry}
	arrived, ended := base, base
	arrived.Stage, ended.Stage = overlay.StageArrived, overlay.StageEnded
	if err := store.Write(ctx, arrived); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, ended); err != nil {
		t.Fatal(err)
	}
	got, err := store.Query(ctx, "tunnel-stages", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Query returned %d events, want 2 (different stages are different events): %#v", len(got), got)
	}
}

// TestTunnelEventStoreWriteNeverDedupesEmptyDedupKeys covers the defensive edge case: an event with no TunnelID
// (DedupKey returns "") must never be collapsed with another such event, since an empty key does not identify
// anything.
func TestTunnelEventStoreWriteNeverDedupesEmptyDedupKeys(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	at := time.Now().UTC()
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: at, NodeID: "n", Stage: overlay.StageArrived}); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: at, NodeID: "n", Stage: overlay.StageArrived}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	dedupSeenSize := len(store.dedupSeen)
	store.mu.Unlock()
	if dedupSeenSize != 0 {
		t.Fatalf("dedupSeen tracked %d empty-key events, want 0 (an empty key must never be remembered)", dedupSeenSize)
	}
	// Both must still land in the same day's partition even though neither was deduplicated by the store itself;
	// readTunnelEvents (and so Query) can only find them by tunnelID, which is empty here, so read the file
	// directly instead.
	path := tunnelEventPath(store.dir, at.Format("20060102"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Fatalf("partition file has %d lines, want 2 (neither empty-tunnelId write may be dropped)", lines)
	}
}

// TestTunnelEventStoreDedupCacheIsBounded covers the LRU's capacity limit: once more than
// tunnelEventDedupCapacity distinct dedup keys have been written, the oldest one falls out of the cache, so a
// later "retransmit" of it is (correctly) treated as new rather than being dropped forever.
func TestTunnelEventStoreDedupCacheIsBounded(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	at := time.Now().UTC()
	first := overlay.TunnelEvent{Timestamp: at, TunnelID: "tunnel-0", NodeID: "n", Stage: overlay.StageArrived}
	if err := store.Write(ctx, first); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= tunnelEventDedupCapacity; i++ {
		filler := overlay.TunnelEvent{Timestamp: at, TunnelID: "tunnel-" + strconv.Itoa(i), NodeID: "n", Stage: overlay.StageArrived}
		if err := store.Write(ctx, filler); err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	cacheSize := len(store.dedupSeen)
	_, firstStillCached := store.dedupSeen[first.DedupKey()]
	store.mu.Unlock()
	if cacheSize != tunnelEventDedupCapacity {
		t.Fatalf("dedup cache holds %d keys, want the bounded capacity of %d", cacheSize, tunnelEventDedupCapacity)
	}
	if firstStillCached {
		t.Fatal("expected the very first dedup key to have been evicted once the cache filled beyond its capacity")
	}
	// Now that it fell out of the cache, "retransmitting" it must be accepted at write time as a new event —
	// checked against the raw partition file, not Query: Query's own fallback dedup pass (see
	// TestTunnelEventStoreQueryDedupesEvenAfterTheLRUForgetsIt) would collapse two same-key events regardless of
	// whether the write-time cache let both through, so it cannot distinguish "the LRU dropped it silently" from
	// "the LRU wrote it and Query merged it afterward".
	if err := store.Write(ctx, first); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tunnelEventPath(store.dir, at.Format("20060102")))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"tunnelId":"tunnel-0"`); got != 2 {
		t.Fatalf("partition file contains %d lines for tunnel-0, want 2 (the evicted key must reach disk again, not be silently dropped at write time)", got)
	}
}

// TestTunnelEventStoreQueryDedupesEvenAfterTheLRUForgetsIt covers Query's fallback deduplication pass: a restart
// (or simply the LRU's capacity being exceeded) resets the write-time cache, so a retransmit that lands afterward
// reaches disk as a genuine second line — Query must still collapse it to one event.
func TestTunnelEventStoreQueryDedupesEvenAfterTheLRUForgetsIt(t *testing.T) {
	store, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	event := overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "tunnel-restart", NodeID: "entry-1", Stage: overlay.StageArrived}
	if err := store.Write(ctx, event); err != nil {
		t.Fatal(err)
	}

	// Simulate the write-time cache being reset (a restart, D29's accepted residual risk) and the identical event
	// being retransmitted afterward: it is no longer recognized as a duplicate at write time, so it really does
	// land on disk a second time.
	store.mu.Lock()
	store.dedupSeen = make(map[string]struct{})
	store.dedupOrder = nil
	store.mu.Unlock()
	if err := store.Write(ctx, event); err != nil {
		t.Fatal(err)
	}

	got, err := store.Query(ctx, "tunnel-restart", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("Query returned %d events, want 1 (the query-time dedup pass must still collapse the duplicate): %#v", len(got), got)
	}
}

func TestTunnelEventStorePartitionsByDayAndPrunesOldOnes(t *testing.T) {
	dir := t.TempDir()
	store, err := newTunnelEventStore(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -(DefaultTunnelEventRetentionDays + 5))
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: old, TunnelID: "ancient", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	oldPath := tunnelEventPath(filepath.Join(dir, tunnelEventsDirName), old.Format("20060102"))
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected old partition file to exist right after writing: %v", err)
	}

	// Writing today's event opens a new partition. Pruning itself now runs on its own periodic schedule (item
	// 6), off the write path, so trigger one directly to observe its effect deterministically.
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "fresh", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.pruneLocked()
	store.mu.Unlock()
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
	// "older than DefaultTunnelEventRetentionDays" cutoff and get pruned out from under the test, regardless of when it
	// actually runs.
	start := time.Date(2035, 6, 1, 0, 0, 0, 0, time.UTC)
	inWindow := []time.Time{start, start.AddDate(0, 0, tunnelQueryWindowDays-1).Add(2 * time.Hour)}
	outOfWindow := start.AddDate(0, 0, tunnelQueryWindowDays).Add(2 * time.Hour)
	// Distinct stages, not just distinct timestamps: two otherwise-identical events would collide under the D29
	// dedup key (tunnelId+nodeId+stage) and collapse to one, which is not what this test is exercising.
	for i, ts := range inWindow {
		stage := []string{overlay.StageArrived, overlay.StageEstablished}[i]
		if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: ts, TunnelID: "tunnel-window", NodeID: "in-window", Stage: stage}); err != nil {
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
// DefaultTunnelEventRetentionDays' age-based limit, the store must not grow without bound within that window either.
// Capping it to a little over one day's partition forces the next prune to evict the oldest one.
func TestTunnelEventStoreEvictsOldestPartitionsOverItsSizeCap(t *testing.T) {
	dir := t.TempDir()
	store, err := newTunnelEventStore(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Far enough in the future that neither partition can ever fall inside the retention window's "older than
	// DefaultTunnelEventRetentionDays" cutoff and get age-pruned instead, regardless of when this test actually runs.
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
	// Below day 0's own size, so the cap must already exclude day 0 alone to force its eviction.
	store.mu.Lock()
	store.maxBytes = info.Size() - 1
	store.mu.Unlock()

	write(1, "day-1")
	// Pruning now runs on its own periodic schedule (item 6), off the write path, so trigger one directly to
	// observe its effect deterministically instead of relying on it firing inline with the write above.
	store.mu.Lock()
	store.pruneLocked()
	store.mu.Unlock()
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
		t.Fatalf("day 1's own partition must survive (it is the most recent one), got %#v", got)
	}
}

// TestTunnelEventStorePruneClosesTheEvictedPartitionsCachedHandle covers the correctness half of item 6: a
// cached handle protects its partition from pruneLocked only while it is the most recently written-to one
// (mirroring the single *os.File store this replaced, which only ever protected "the currently open file" the
// same way); any other day's cached handle must not, or a node could keep alternating writes across a couple of
// fresh days plus one stale one, always within the LRU's capacity, to keep that stale day permanently exempt
// from DefaultTunnelEventRetentionDays.
func TestTunnelEventStorePruneClosesTheEvictedPartitionsCachedHandle(t *testing.T) {
	dir := t.TempDir()
	store, err := newTunnelEventStore(dir, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	stale := time.Now().UTC().AddDate(0, 0, -(DefaultTunnelEventRetentionDays + 1))
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: stale, TunnelID: "stale", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	// A second, more recent write makes stale's day no longer the most-recently-written one (well within
	// maxOpenTunnelEventFiles, so its handle stays cached rather than being LRU-evicted), so pruneLocked's
	// protection for the most-recently-written partition no longer covers it.
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "fresh", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	stalePath := tunnelEventPath(filepath.Join(dir, tunnelEventsDirName), stale.Format("20060102"))

	store.mu.Lock()
	if _, cached := store.handles[stale.Format("20060102")]; !cached {
		store.mu.Unlock()
		t.Fatal("expected the stale day's handle to still be cached (well within maxOpenTunnelEventFiles)")
	}
	store.pruneLocked()
	_, stillCached := store.handles[stale.Format("20060102")]
	store.mu.Unlock()
	if stillCached {
		t.Fatal("pruneLocked must evict a pruned (non-most-recent) partition's cached handle along with its file")
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected the stale, non-most-recent partition to be pruned, stat err = %v", err)
	}

	// A later write for the same (still stale) day must reopen the file rather than reuse a stale reference to
	// the one pruneLocked already removed; it lands in a fresh file, immediately queryable again. A distinct
	// Stage from the first "stale" write, not just a re-lands-later timing difference, so this write is not
	// itself collapsed by the D29 dedup key (tunnelId+nodeId+stage) into the very handle-recreation path this
	// test is exercising.
	if err := store.Write(ctx, overlay.TunnelEvent{Timestamp: stale, TunnelID: "stale", NodeID: "n", Stage: overlay.StageEnded}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Query(ctx, "stale", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %#v, want the one event written after the file was recreated", got)
	}
}

// TestTunnelEventStoreCachesOpenFileHandlesAcrossDaySwitches covers stage 5 low-priority finding item 6:
// out-of-order or delayed events crossing a day boundary can make consecutive writes alternate between two (or
// a few) days. Without a small handle cache, every alternation would force a Close+Open, even though the same
// handful of days keep recurring. opens (incremented only on an actual cache miss) must equal the number of
// distinct days touched, not the number of writes.
func TestTunnelEventStoreCachesOpenFileHandlesAcrossDaySwitches(t *testing.T) {
	s, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	dayA := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	dayB := dayA.AddDate(0, 0, 1)
	const writes = 20
	for i := range writes {
		ts := dayA
		if i%2 == 1 {
			ts = dayB
		}
		// Stage carries the write index so no two of these 20 writes share a D29 dedup key
		// (tunnelId+nodeId+stage): this test is about handle caching across day switches, not deduplication, and
		// every write must independently reach openLocked to be counted.
		if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: ts, TunnelID: "t", NodeID: "n", Stage: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	opens := s.opens
	s.mu.Unlock()
	if opens != 2 {
		t.Fatalf("opens = %d, want 2 (one per distinct day), even though %d writes alternated between them", opens, writes)
	}

	got, err := s.Query(context.Background(), "t", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != writes {
		t.Fatalf("stored events = %d, want %d (every write must still have landed in its own day's partition)", len(got), writes)
	}
}

// TestTunnelEventStoreEvictsTheLeastRecentlyUsedHandleOverCapacity covers the small-LRU part of item 6: only a
// bounded number of file handles stay open at once, and the least recently touched one is evicted first, not
// whichever happens to be oldest by day.
func TestTunnelEventStoreEvictsTheLeastRecentlyUsedHandleOverCapacity(t *testing.T) {
	s, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	seq := 0
	write := func(day int) {
		t.Helper()
		// Stage carries a call-sequence number, not the day, so even the two calls for day 0 (below) get distinct
		// D29 dedup keys: this test is about which file handle gets touched/evicted, and a deduplicated write
		// would never reach openLocked to touch anything.
		seq++
		if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: base.AddDate(0, 0, day), TunnelID: "t", NodeID: "n", Stage: strconv.Itoa(seq)}); err != nil {
			t.Fatal(err)
		}
	}
	write(0)
	write(1)
	write(2)
	s.mu.Lock()
	openHandles := len(s.handles)
	s.mu.Unlock()
	if openHandles != maxOpenTunnelEventFiles {
		t.Fatalf("open handles = %d, want the cap of %d", openHandles, maxOpenTunnelEventFiles)
	}
	write(0) // touches day 0 again, so it is no longer the least recently used
	write(3) // one more distinct day pushes the cache over its cap; day 1 (untouched since) must be evicted

	s.mu.Lock()
	_, day1Open := s.handles[base.AddDate(0, 0, 1).Format("20060102")]
	_, day0Open := s.handles[base.Format("20060102")]
	s.mu.Unlock()
	if day1Open {
		t.Fatal("day 1's handle should have been evicted as the least recently used")
	}
	if !day0Open {
		t.Fatal("day 0's handle should still be cached: it was touched again before the eviction")
	}
}

// TestTunnelEventStorePruneLoopRunsInTheBackground covers item 6's own periodic cadence directly: retention is
// enforced by a loop that runs on its own schedule, not by anything tied to Write, so an expired partition must
// eventually disappear on its own, with no explicit trigger from the test at all.
func TestTunnelEventStorePruneLoopRunsInTheBackground(t *testing.T) {
	dir := t.TempDir()
	const pruneInterval = 10 * time.Millisecond
	s, err := newTunnelEventStoreWithPruneInterval(dir, zap.NewNop(), pruneInterval)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	stale := time.Now().UTC().AddDate(0, 0, -(DefaultTunnelEventRetentionDays + 1))
	if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: stale, TunnelID: "stale", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	// A second, more recent write makes stale's day no longer the most-recently-written one, so pruneLocked's
	// protection for that single partition (see pruneLocked) no longer applies to it.
	if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: time.Now().UTC(), TunnelID: "fresh", NodeID: "n"}); err != nil {
		t.Fatal(err)
	}
	stalePath := tunnelEventPath(filepath.Join(dir, tunnelEventsDirName), stale.Format("20060102"))

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(stalePath); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("expected the background prune loop to remove the stale partition without an explicit trigger")
		}
		time.Sleep(pruneInterval)
	}
}

// TestTunnelEventStoreCloseStopsThePruneLoop covers the shutdown half of item 6: Close must wait for the
// background loop to actually exit, not just signal it and return, so it never keeps running past Close.
func TestTunnelEventStoreCloseStopsThePruneLoop(t *testing.T) {
	const pruneInterval = 5 * time.Millisecond
	s, err := newTunnelEventStoreWithPruneInterval(t.TempDir(), zap.NewNop(), pruneInterval)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * pruneInterval) // let the loop tick a few times, so there is something to actually stop

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	runsAtClose := s.pruneRuns
	s.mu.Unlock()

	time.Sleep(10 * pruneInterval)
	s.mu.Lock()
	runsAfterClose := s.pruneRuns
	s.mu.Unlock()
	if runsAfterClose != runsAtClose {
		t.Fatalf("pruneRuns advanced from %d to %d after Close returned: the background loop did not actually stop",
			runsAtClose, runsAfterClose)
	}
}

// TestTunnelEventStoreConcurrentWriteAndQueryIsRaceFree exercises Write and Query from several goroutines at
// once, across more distinct days than the handle cache holds (so both the LRU eviction path and the
// concurrently running background prune loop are exercised too), to be run with -race.
func TestTunnelEventStoreConcurrentWriteAndQueryIsRaceFree(t *testing.T) {
	s, err := newTunnelEventStoreWithPruneInterval(t.TempDir(), zap.NewNop(), 2*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	base := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range 50 {
				day := (worker + i) % 5 // more distinct days than maxOpenTunnelEventFiles, to force eviction too
				ts := base.AddDate(0, 0, day)
				// NodeID is unique per call so this stress test actually keeps writing through to openLocked
				// under the D29 dedup check, instead of every call after the first becoming a no-op duplicate.
				nodeID := "n-" + strconv.Itoa(worker) + "-" + strconv.Itoa(i)
				if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: ts, TunnelID: "concurrent", NodeID: nodeID}); err != nil {
					t.Error(err)
				}
				if _, err := s.Query(context.Background(), "concurrent", time.Time{}); err != nil {
					t.Error(err)
				}
			}
		}(worker)
	}
	wg.Wait()
}

func BenchmarkTunnelEventStoreAlternatingDayWrites(b *testing.B) {
	s, err := newTunnelEventStore(b.TempDir(), zap.NewNop())
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	dayA := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	dayB := dayA.AddDate(0, 0, 1)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		ts := dayA
		if i%2 == 1 {
			ts = dayB
		}
		// A unique Stage per iteration keeps every write past the D29 dedup check (see the equivalent fix in
		// TestTunnelEventStoreCachesOpenFileHandlesAcrossDaySwitches), so this still benchmarks the day-switching
		// write path itself rather than the dedup short-circuit.
		if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: ts, TunnelID: "t", NodeID: "n", Stage: strconv.Itoa(i)}); err != nil {
			b.Fatal(err)
		}
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

// TestSetTunnelEventRetentionOverridesTheDefault covers the Control-level knob for D31's retention window,
// mirroring TestSetTunnelEventStoreCapacityOverridesTheDefault above: a positive override replaces the default
// and sticks, a non-positive one is a no-op that leaves whatever is already set in place, and calling it on a
// Control with no tunnel event store (the plain New() constructor) must not panic.
func TestSetTunnelEventRetentionOverridesTheDefault(t *testing.T) {
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

	if c.tunnelEvents.retentionDays != DefaultTunnelEventRetentionDays {
		t.Fatalf("retentionDays before any override = %d, want %d", c.tunnelEvents.retentionDays, DefaultTunnelEventRetentionDays)
	}
	c.SetTunnelEventRetention(30)
	if c.tunnelEvents.retentionDays != 30 {
		t.Fatalf("retentionDays = %d, want 30", c.tunnelEvents.retentionDays)
	}
	c.SetTunnelEventRetention(0)
	if c.tunnelEvents.retentionDays != 30 {
		t.Fatalf("retentionDays after a non-positive override = %d, want unchanged at 30", c.tunnelEvents.retentionDays)
	}

	plain := New(store.New(db), zap.NewNop())
	plain.SetTunnelEventRetention(7)
}

// TestSetTunnelEventRetentionAffectsPruning covers that the override actually changes which partitions
// pruneLocked keeps, not just the stored field: a partition older than the shortened window is removed, and one
// that would only have aged out under the old default survives.
func TestSetTunnelEventRetentionAffectsPruning(t *testing.T) {
	s, err := newTunnelEventStore(t.TempDir(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.mu.Lock()
	s.retentionDays = 1
	s.mu.Unlock()

	old := time.Now().UTC().AddDate(0, 0, -5)
	recent := time.Now().UTC()
	if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: old, TunnelID: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), overlay.TunnelEvent{Timestamp: recent, TunnelID: "recent"}); err != nil {
		t.Fatal(err)
	}
	s.pruneNow()

	if _, err := os.Stat(tunnelEventPath(s.dir, old.Format("20060102"))); !os.IsNotExist(err) {
		t.Fatalf("old partition still exists after pruning with a 1-day retention override: %v", err)
	}
	if _, err := os.Stat(tunnelEventPath(s.dir, recent.Format("20060102"))); err != nil {
		t.Fatalf("recent partition was pruned away: %v", err)
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
	got, err := store.Query(context.Background(), "local-tunnel", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %#v, want one event", got)
	}
}
