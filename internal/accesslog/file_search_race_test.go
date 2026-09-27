package accesslog

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests cover the review's HIGH finding (problem 1) that Search can fail outright when it races the
// background archive sweep (archiveSlot/gzipFileTo) or retention (pruneArchives): both touch files outside s.mu
// so a slow gzip or prune never blocks the write path (see rotateActiveLocked). searchGlobHook lets these tests
// inject a filesystem change at the exact point Search has listed its candidate files but not yet opened them,
// reproducing races that are only a few instructions wide. The companion duplicate-counting race (problem 2) has
// its own tests in file_search_dedup_test.go.

// writeGzipFile writes content to path compressed with gzip, matching the shape gzipFileTo produces, so
// readRecords' ".gz" branch can decode it.
func writeGzipFile(t *testing.T, path string, content []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := gzip.NewWriter(f)
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func recordLine(t *testing.T, at time.Time, path string) []byte {
	t.Helper()
	record := Record{Timestamp: at, SiteID: "site-a", Method: "GET", Path: path, Status: 200, RequestHeaders: map[string][]string{}, ResponseHeaders: map[string][]string{}}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(line, '\n')
}

// TestFileSink_SearchFallsBackToGzWhenArchivingFileFinishesMidQuery reproduces the review's repro shape for
// problem 1: Search's Glob captures a period's ".archiving" file, but a concurrent gzipFileTo (run outside s.mu
// by archiveSlot) finishes compressing it — renaming the ".gz" into place and then removing the ".archiving"
// source — before Search gets to Open it. Before the fix, Open returned ENOENT and the whole query failed;
// afterward it must fall back to the now-complete ".gz" form and still find the record.
func TestFileSink_SearchFallsBackToGzWhenArchivingFileFinishesMidQuery(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	old := time.Now().UTC().AddDate(0, 0, -5)
	period := rotationPeriod("day", old, time.UTC)
	primary := periodSlotName(period, 0)
	line := recordLine(t, old, "/gz-fallback")

	archivingPath := filepath.Join(dir, primary+".archiving")
	if err := os.WriteFile(archivingPath, line, 0640); err != nil {
		t.Fatal(err)
	}

	defer func() { searchGlobHook = nil }()
	searchGlobHook = func(paths []string) {
		// Simulate gzipFileTo's last two steps racing this Search call: the compressed form appears...
		writeGzipFile(t, filepath.Join(dir, primary+".gz"), line)
		// ...and then its source is removed, exactly like gzipFileTo's final os.Remove(srcPath).
		if err := os.Remove(archivingPath); err != nil {
			t.Fatal(err)
		}
	}

	result, err := manager.Search(context.Background(), Query{From: old.Add(-time.Hour), To: old.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search failed instead of falling back to the finished .gz: %v", err)
	}
	if result.Total != 1 || result.Records[0].Path != "/gz-fallback" {
		t.Fatalf("unexpected result after archiving finished mid-query: %#v", result)
	}
}

// TestFileSink_SearchFallsBackToPlainWhenArchiveIsRolledBackMidQuery covers archiveSlot's recovery branch: if
// gzipFileTo fails, archiveSlot renames the ".archiving" file back to its plain name. If that rollback races
// Search the same way, there is no ".gz" to fall back to either — Search must fall further back to the plain
// name rather than fail or skip the record.
func TestFileSink_SearchFallsBackToPlainWhenArchiveIsRolledBackMidQuery(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	old := time.Now().UTC().AddDate(0, 0, -5)
	period := rotationPeriod("day", old, time.UTC)
	primary := periodSlotName(period, 0)
	line := recordLine(t, old, "/rollback-fallback")

	archivingPath := filepath.Join(dir, primary+".archiving")
	if err := os.WriteFile(archivingPath, line, 0640); err != nil {
		t.Fatal(err)
	}

	defer func() { searchGlobHook = nil }()
	searchGlobHook = func(paths []string) {
		// Simulate archiveSlot's failure-recovery rename: no ".gz" ever appears, the ".archiving" file just
		// moves back under its original plain name.
		if err := os.Rename(archivingPath, filepath.Join(dir, primary)); err != nil {
			t.Fatal(err)
		}
	}

	result, err := manager.Search(context.Background(), Query{From: old.Add(-time.Hour), To: old.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search failed instead of falling back to the rolled-back plain file: %v", err)
	}
	if result.Total != 1 || result.Records[0].Path != "/rollback-fallback" {
		t.Fatalf("unexpected result after archive rollback mid-query: %#v", result)
	}
}

// TestFileSink_SearchSkipsFilesPrunedMidQuery covers a file (not ".archiving") that pruneArchives (also run
// outside s.mu; see prunePeriodicArchives) removes between Search's Glob and Open: unlike a vanished ".archiving"
// file, there is nowhere to fall back to — it was legitimately deleted by retention — so Search must simply
// leave it out of the result instead of failing the whole query.
func TestFileSink_SearchSkipsFilesPrunedMidQuery(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	// newFileSink unconditionally launches one background sweep (triggerSweep, via launchArchive) at construction
	// time, tracked by sweepWG; on the empty directory it sees at that instant, it is a no-op, but its os.ReadDir
	// is not synchronized with anything below. Without this Wait, that goroutine can still be pending when the
	// plain "access-*.jsonl" fixtures below land on disk, and — being indistinguishable from a real leftover
	// period file — race archiveSlot into renaming/compressing prunedPath before the hook below ever runs,
	// making its hardcoded os.Remove(prunedPath) fail with ENOENT for a reason that has nothing to do with what
	// this test is actually injecting. Draining it first (this sink never calls sink.Write, the only other thing
	// that triggers a sweep) removes that race entirely rather than just narrowing it.
	sink.sweepWG.Wait()
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	prunedAt := time.Now().UTC().AddDate(0, 0, -9)
	survivingAt := time.Now().UTC().AddDate(0, 0, -8)
	prunedPeriod := rotationPeriod("day", prunedAt, time.UTC)
	prunedPath := filepath.Join(dir, periodSlotName(prunedPeriod, 0))
	if err := os.WriteFile(prunedPath, recordLine(t, prunedAt, "/pruned"), 0640); err != nil {
		t.Fatal(err)
	}
	survivingPeriod := rotationPeriod("day", survivingAt, time.UTC)
	survivingPath := filepath.Join(dir, periodSlotName(survivingPeriod, 0))
	if err := os.WriteFile(survivingPath, recordLine(t, survivingAt, "/surviving"), 0640); err != nil {
		t.Fatal(err)
	}

	defer func() { searchGlobHook = nil }()
	searchGlobHook = func(paths []string) {
		if err := os.Remove(prunedPath); err != nil {
			t.Fatal(err)
		}
	}

	result, err := manager.Search(context.Background(), Query{From: prunedAt.Add(-time.Hour), To: survivingAt.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("Search failed instead of skipping the file retention removed mid-query: %v", err)
	}
	if result.Total != 1 || result.Records[0].Path != "/surviving" {
		t.Fatalf("unexpected result after a concurrent prune: %#v", result)
	}
}
