package accesslog

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"os"
)

// These tests cover the review's HIGH finding (problem 2): gzipFileTo renames a period's ".gz" into place
// before removing its ".archiving" source (see its doc comment), so Search's Glob can land in the window where
// both names exist and count that period's records twice. The companion ENOENT race (problem 1) has its own
// tests in file_search_race_test.go.

// TestFileSink_SearchDoesNotDoubleCountDuringArchiveWindow covers problem 2 directly: both names for the same
// period's records exist at once, and Search must count that period's records once, not twice.
func TestFileSink_SearchDoesNotDoubleCountDuringArchiveWindow(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	old := time.Now().UTC().AddDate(0, 0, -6)
	period := rotationPeriod("day", old, time.UTC)
	primary := periodSlotName(period, 0)
	line := recordLine(t, old, "/archive-window")

	if err := os.WriteFile(filepath.Join(dir, primary+".archiving"), line, 0640); err != nil {
		t.Fatal(err)
	}
	writeGzipFile(t, filepath.Join(dir, primary+".gz"), line)

	result, err := manager.Search(context.Background(), Query{From: old.Add(-time.Hour), To: old.Add(time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 {
		t.Fatalf("Total = %d, want 1 (the .archiving copy must be skipped in favor of the complete .gz)", result.Total)
	}
}

// TestFileSink_SearchNeitherLosesNorDuplicatesRecordsUnderConcurrentArchiving is a concurrent stress regression
// test for both races together: many goroutines write records for many distinct historical periods (each
// triggering a background archiveSlot once its handle goes idle) while other goroutines repeatedly call Search.
// Every Search call must succeed, and once everything settles, a final Search must return exactly the number of
// records written — neither fewer (a file caught mid rename/remove) nor more (a period briefly double-counted
// while its ".gz" and ".archiving" coexist). Compression is on and retention is off (KeepFiles=0) so nothing is
// legitimately pruned, keeping the expected count exact. Run with -race.
func TestFileSink_SearchNeitherLosesNorDuplicatesRecordsUnderConcurrentArchiving(t *testing.T) {
	sink, dir := newDayFileSink(t, 0, true)
	manager := &Manager{dir: dir, sink: sink, location: time.UTC}
	now := time.Now().UTC()

	const numPeriods = 10
	const perPeriod = 12
	total := numPeriods * perPeriod

	var writeWG sync.WaitGroup
	for p := 1; p <= numPeriods; p++ {
		at := now.AddDate(0, 0, -p)
		writeWG.Add(1)
		go func(at time.Time) {
			defer writeWG.Done()
			for i := 0; i < perPeriod; i++ {
				writeRecord(t, sink, at, "/stress")
			}
		}(at)
	}

	query := Query{From: now.AddDate(0, 0, -numPeriods-1), To: now.Add(time.Hour), Page: 1, PageSize: 100}
	stopSearch := make(chan struct{})
	var searchErrs int32
	var searchWG sync.WaitGroup
	for i := 0; i < 4; i++ {
		searchWG.Add(1)
		go func() {
			defer searchWG.Done()
			for {
				select {
				case <-stopSearch:
					return
				default:
				}
				if _, err := manager.Search(context.Background(), query); err != nil {
					atomic.AddInt32(&searchErrs, 1)
				}
			}
		}()
	}

	writeWG.Wait()
	sink.sweepWG.Wait()
	close(stopSearch)
	searchWG.Wait()

	if searchErrs != 0 {
		t.Fatalf("%d concurrent Search calls failed while background archiving raced them", searchErrs)
	}

	result, err := manager.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != total {
		t.Fatalf("Total = %d, want %d (no record should be lost or double-counted to the archive race)", result.Total, total)
	}
}
