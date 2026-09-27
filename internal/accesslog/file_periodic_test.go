package accesslog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests cover the P5 review's CRITICAL/HIGH findings against the file adapter's day/hour rotation: a
// record's own (possibly out-of-order) timestamp must never rename or mis-prune the active file (see
// rotateActiveLocked/writeHistoricalLocked in file.go), and archiving/pruning must not run synchronously on the
// write path for every alternating period (see launchArchive).

func newDayFileSink(t *testing.T, keepFiles int, compress bool) (*fileSink, string) {
	t.Helper()
	dir := t.TempDir()
	sink, err := newFileSink(dir, FileConfig{Rotation: "day", MaxSizeBytes: 1 << 30, Compress: compress, KeepFiles: keepFiles})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	return sink, dir
}

// forceSweep drains any sweep already triggered by a preceding write, then forces one more pass that closes
// every historical handle regardless of historicalIdleGrace before archiving/pruning — giving tests
// deterministic behavior without sleeping past the grace period.
func forceSweep(t *testing.T, sink *fileSink) {
	t.Helper()
	sink.sweepWG.Wait()
	sink.mu.Lock()
	sink.closeIdleHistoricalLocked(time.Now())
	sink.mu.Unlock()
	sink.sweepOnce()
}

func writeRecord(t *testing.T, sink *fileSink, at time.Time, path string) {
	t.Helper()
	record := Record{
		Timestamp:       at,
		SiteID:          "site-a",
		Method:          "GET",
		Path:            path,
		Status:          200,
		RequestHeaders:  map[string][]string{},
		ResponseHeaders: map[string][]string{},
	}
	if err := sink.Write(context.Background(), record); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestFileSink_OutOfOrderWritesNoDataLoss reproduces the review's repro shape (50 "today" records, one delayed
// record from a week ago, two more "today" records) and asserts every record survives and is searchable when
// nothing is pruned (KeepFiles=0 disables retention), across a period boundary crossed entirely out of order.
func TestFileSink_OutOfOrderWritesNoDataLoss(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	now := time.Now().UTC()
	weekAgo := now.AddDate(0, 0, -7)
	for i := 0; i < 50; i++ {
		writeRecord(t, sink, now, "/today")
	}
	writeRecord(t, sink, weekAgo, "/week-ago")
	for i := 0; i < 2; i++ {
		writeRecord(t, sink, now, "/today")
	}
	sink.sweepWG.Wait()
	result, err := manager.Search(context.Background(), Query{From: weekAgo.Add(-time.Hour), To: now.Add(time.Hour), Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 53 {
		t.Fatalf("Total = %d, want 53 (no record should be lost to misnaming)", result.Total)
	}
	todayCount, weekAgoCount := 0, 0
	for _, r := range result.Records {
		switch r.Path {
		case "/today":
			todayCount++
		case "/week-ago":
			weekAgoCount++
		default:
			t.Fatalf("unexpected record path %q", r.Path)
		}
	}
	if todayCount != 52 || weekAgoCount != 1 {
		t.Fatalf("today=%d weekAgo=%d, want 52 and 1", todayCount, weekAgoCount)
	}
}

// TestFileSink_OutOfOrderWriteDoesNotRenameActiveFile is the direct regression test for the CRITICAL finding: a
// delayed record for a past period must be written to that period's own file, never trigger a rename of the
// active file (which would mis-date it and mix its unrelated, current records into an archive named for the
// delayed record's period).
func TestFileSink_OutOfOrderWriteDoesNotRenameActiveFile(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	now := time.Now().UTC()
	weekAgo := now.AddDate(0, 0, -7)
	for i := 0; i < 5; i++ {
		writeRecord(t, sink, now, "/today")
	}
	writeRecord(t, sink, weekAgo, "/week-ago")
	for i := 0; i < 5; i++ {
		writeRecord(t, sink, now, "/today")
	}
	sink.sweepWG.Wait()

	activePath := filepath.Join(dir, activeFileName)
	activeContent, err := os.ReadFile(activePath)
	if err != nil {
		t.Fatalf("active file missing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(activeContent)), "\n")
	if len(lines) != 10 {
		t.Fatalf("active file has %d lines, want 10 (all current-period writes, none diverted or lost)", len(lines))
	}
	for _, line := range lines {
		if !strings.Contains(line, "/today") {
			t.Fatalf("active file contains a non-current record: %s", line)
		}
	}

	weekAgoPeriod := rotationPeriod("day", weekAgo, time.UTC)
	weekAgoPath := filepath.Join(dir, periodSlotName(weekAgoPeriod, 0))
	weekAgoContent, err := os.ReadFile(weekAgoPath)
	if err != nil {
		t.Fatalf("expected the delayed record's own period file to exist: %v", err)
	}
	if !strings.Contains(string(weekAgoContent), "/week-ago") || strings.Contains(string(weekAgoContent), "/today") {
		t.Fatalf("delayed record landed in the wrong file: %s", weekAgoContent)
	}
}

// TestFileSink_AlternatingPeriodsDoNotRotatePerLine is the regression test for the HIGH finding: rotate must not
// run on every write when records for two periods interleave. If it did, each alternation would synchronously
// rename+reopen the active file, leaving many legacy-shaped archive files behind instead of the one file per
// distinct historical period this design produces.
func TestFileSink_AlternatingPeriodsDoNotRotatePerLine(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -3)
	for i := 0; i < 40; i++ {
		if i%5 == 0 {
			writeRecord(t, sink, old, "/old")
		} else {
			writeRecord(t, sink, now, "/today")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var historicalFiles []string
	for _, entry := range entries {
		if entry.Name() != activeFileName {
			historicalFiles = append(historicalFiles, entry.Name())
		}
	}
	if len(historicalFiles) != 1 {
		t.Fatalf("expected exactly one historical file (the old period's primary, opened once and reused for every alternation), got %v", historicalFiles)
	}
	oldPeriod := rotationPeriod("day", old, time.UTC)
	if want := periodSlotName(oldPeriod, 0); historicalFiles[0] != want {
		t.Fatalf("historical file = %q, want %q", historicalFiles[0], want)
	}
}

// TestFileSink_KeepFilesPrunesOldestPeriodOnly asserts retention groups by period, not by file: two distinct
// historical periods, KeepFiles=1, only the newer historical period should survive pruning, and the active
// (current) period is never subject to retention at all.
func TestFileSink_KeepFilesPrunesOldestPeriodOnly(t *testing.T) {
	sink, dir := newDayFileSink(t, 1, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	now := time.Now().UTC()
	older := now.AddDate(0, 0, -8)
	newer := now.AddDate(0, 0, -7)
	for i := 0; i < 5; i++ {
		writeRecord(t, sink, now, "/today")
	}
	writeRecord(t, sink, older, "/older")
	writeRecord(t, sink, newer, "/newer")
	writeRecord(t, sink, newer, "/newer")
	forceSweep(t, sink)

	result, err := manager.Search(context.Background(), Query{From: older.Add(-time.Hour), To: now.Add(time.Hour), Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	paths := make(map[string]int)
	for _, r := range result.Records {
		paths[r.Path]++
	}
	if paths["/older"] != 0 {
		t.Fatalf("the oldest historical period should have been pruned, found %d /older records", paths["/older"])
	}
	if paths["/newer"] != 2 {
		t.Fatalf("the newer historical period should survive retention, found %d /newer records, want 2", paths["/newer"])
	}
	if paths["/today"] != 5 {
		t.Fatalf("the active period is never subject to retention, found %d /today records, want 5", paths["/today"])
	}
}

// TestFileSink_LateWriteAfterArchiveUsesShard covers the review's "delayed record targets an already-compressed
// period" scenario: once a period's primary file has been archived to .gz, a further write for that period must
// not be lost or silently dropped — it goes to a fresh shard, and both remain searchable.
func TestFileSink_LateWriteAfterArchiveUsesShard(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	old := time.Now().UTC().AddDate(0, 0, -10)
	writeRecord(t, sink, old, "/first")

	oldPeriod := rotationPeriod("day", old, time.UTC)
	primary := periodSlotName(oldPeriod, 0)
	forceSweep(t, sink)
	if _, err := os.Stat(filepath.Join(dir, primary+".gz")); err != nil {
		t.Fatalf("expected the primary period file to be archived: %v", err)
	}

	writeRecord(t, sink, old, "/second")
	shard := periodSlotName(oldPeriod, 1)
	if _, err := os.Stat(filepath.Join(dir, shard)); err != nil {
		t.Fatalf("expected the delayed write to land in a late shard %q: %v", shard, err)
	}

	result, err := manager.Search(context.Background(), Query{From: old.Add(-time.Hour), To: old.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 {
		t.Fatalf("Total = %d, want 2 (one archived, one in the late shard)", result.Total)
	}
}

// TestFileSink_CompressDisabledKeepsPlainHistoricalFilesAndStillPrunes verifies retention still works with
// compression turned off: old periods are left as plain .jsonl (never renamed to .gz) but are still pruned by
// KeepFiles once a newer historical period exists.
func TestFileSink_CompressDisabledKeepsPlainHistoricalFilesAndStillPrunes(t *testing.T) {
	sink, dir := newDayFileSink(t, 1, false)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	now := time.Now().UTC()
	older := now.AddDate(0, 0, -8)
	newer := now.AddDate(0, 0, -7)
	writeRecord(t, sink, older, "/older")
	writeRecord(t, sink, newer, "/newer")
	forceSweep(t, sink)

	if matches, _ := filepath.Glob(filepath.Join(dir, "*.gz")); len(matches) != 0 {
		t.Fatalf("compress is disabled, expected no .gz files, got %v", matches)
	}
	result, err := manager.Search(context.Background(), Query{From: older.Add(-time.Hour), To: now, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].Path != "/newer" {
		t.Fatalf("unexpected search result after pruning without compression: %#v", result)
	}
}

// TestFileSink_ConcurrentWritesRace exercises concurrent writers spanning the active period and several
// historical ones (run with -race): no write should be lost, and the historical handle cache/LRU and active
// rotation must not race with each other.
func TestFileSink_ConcurrentWritesRace(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	now := time.Now().UTC()
	periods := []time.Time{
		now,
		now.AddDate(0, 0, -1),
		now.AddDate(0, 0, -2),
		now.AddDate(0, 0, -3),
		now.AddDate(0, 0, -4),
		now.AddDate(0, 0, -5),
	}
	const perGoroutine = 30
	var wg sync.WaitGroup
	for g := 0; g < len(periods); g++ {
		wg.Add(1)
		go func(at time.Time) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				record := Record{Timestamp: at, SiteID: "site-a", Method: "GET", Path: "/race", Status: 200, RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
				if err := sink.Write(context.Background(), record); err != nil {
					t.Errorf("concurrent write: %v", err)
				}
			}
		}(periods[g])
	}
	wg.Wait()
	sink.sweepWG.Wait()

	result, err := manager.Search(context.Background(), Query{From: now.AddDate(0, 0, -6), To: now.Add(time.Hour), Page: 1, PageSize: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := len(periods) * perGoroutine
	if result.Total != want {
		t.Fatalf("Total = %d, want %d", result.Total, want)
	}
}

// TestParseFilePeriod_RecognizesLegacyAndCurrentNames covers the naming formats prunePeriodicArchives must keep
// recognizing: the current period-named form (with and without a late shard suffix) and the pre-review
// timestamp+sequence form every rotation mode used to produce.
func TestParseFilePeriod_RecognizesLegacyAndCurrentNames(t *testing.T) {
	cases := []struct {
		name     string
		rotation string
		want     string
		ok       bool
	}{
		{name: "access-20260927.jsonl", rotation: "day", want: "20260927", ok: true},
		{name: "access-20260927.jsonl.gz", rotation: "day", want: "20260927", ok: true},
		{name: "access-20260927.late1.jsonl", rotation: "day", want: "20260927", ok: true},
		{name: "access-2026092714.jsonl", rotation: "hour", want: "2026092714", ok: true},
		{name: "access-2026092714.jsonl", rotation: "day", ok: false},
		{name: "access-20260920T101112.123456789Z-000001.jsonl", rotation: "day", want: "20260920", ok: true},
		{name: "access-20260920T101112.123456789Z-000001.jsonl.gz", rotation: "day", want: "20260920", ok: true},
		{name: "access.jsonl", rotation: "day", ok: false},
		{name: "not-an-access-file.jsonl", rotation: "day", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseFilePeriod(tc.name, tc.rotation, time.UTC)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("period = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestFileSink_ResumesInterruptedArchiveOnRestart verifies a ".archiving" file left behind by a process that
// died mid-compression (archiveSlot renamed it but never finished gzipping) is restored to a plain, writable
// file on the next startup rather than left orphaned or its data stuck unreadable.
func TestFileSink_ResumesInterruptedArchiveOnRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -1)
	oldPeriod := rotationPeriod("day", old, time.UTC)
	primary := periodSlotName(oldPeriod, 0)
	record := Record{Timestamp: old, SiteID: "site-a", Method: "GET", Path: "/interrupted", Status: 200, RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, primary+".archiving")
	if err := os.WriteFile(staged, append(line, '\n'), 0640); err != nil {
		t.Fatal(err)
	}

	sink, err := newFileSink(dir, FileConfig{Rotation: "day", MaxSizeBytes: 1 << 30, Compress: true, KeepFiles: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	sink.sweepWG.Wait()
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("expected the interrupted archive to be resumed, .archiving file still present (err=%v)", err)
	}

	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	result, err := manager.Search(context.Background(), Query{From: old.Add(-time.Hour), To: old.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Records[0].Path != "/interrupted" {
		t.Fatalf("resumed record not found: %#v", result)
	}
}

// TestFileSink_SetTimeZoneRaceWithBackgroundSweep is the regression test for a data race between SetTimeZone
// (called with s.mu held, e.g. right after accesslog.NewRegistry/NewManager construction) and the background
// sweep triggerSweep launches: sweepOnce's call chain (pruneArchives -> prunePeriodicArchives) used to read
// s.location without holding s.mu. Seeding real archived periods here makes prunePeriodicArchives actually reach
// its parseFilePeriod call (the read that raced) on every pass rather than short-circuiting on an empty
// directory. Run with -race; before the fix this fails on essentially every run.
func TestFileSink_SetTimeZoneRaceWithBackgroundSweep(t *testing.T) {
	sink, dir := newDayFileSink(t, 5, true)
	old := time.Now().UTC().AddDate(0, 0, -30)
	for i := 0; i < 8; i++ {
		period := rotationPeriod("day", old.AddDate(0, 0, -i), time.UTC)
		name := periodSlotName(period, 0)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}\n"), 0640); err != nil {
			t.Fatal(err)
		}
	}

	zones := []*time.Location{time.UTC, time.FixedZone("east", 9*3600), time.FixedZone("west", -5*3600)}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			sink.SetTimeZone(zones[i%len(zones)])
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			sink.sweepOnce()
		}
		close(stop)
	}()
	wg.Wait()
}
