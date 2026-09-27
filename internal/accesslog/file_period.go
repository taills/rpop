package accesslog

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// historicalIdleGrace bounds how long a historical handle (see writeHistoricalLocked) stays open, unused,
// before a sweep closes it and makes its file eligible for archiving/pruning. Long enough that one segment's
// worth of same-period backfill lines (a node replaying its spool writes many records for the same stale period
// in a row; see internal/control/logs_ingest.go's per-line loop) keeps reusing the same handle instead of
// repeatedly closing and reopening it; short enough that a period stops blocking retention soon after the
// backfill that touched it ends. Without this, a handle that is merely under the maxHistoricalHandles cap would
// stay open — and so unarchivable and unprunable — for as long as the sink lives.
const historicalIdleGrace = 2 * time.Second

func (s *fileSink) SetTimeZone(location *time.Location) {
	if location == nil {
		location = time.UTC
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.location = location
	if s.file != nil && s.size > 0 {
		if info, err := s.file.Stat(); err == nil {
			s.period = rotationPeriod(s.config.Rotation, info.ModTime(), location)
		}
	}
}

// locationSnapshot returns the sink's current time zone under lock. Background work that runs outside s.mu
// (pruneArchives and anything it calls) must go through this instead of reading s.location directly, since
// SetTimeZone can replace it concurrently.
func (s *fileSink) locationSnapshot() *time.Location {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.location
}

// rotateActiveLocked retires the active file under newPeriod's predecessor and opens a fresh one for newPeriod.
// The rename is fast (same-directory metadata operation); compressing and pruning the retired file happen in
// the background (see launchArchive) so a slow gzip of a big period never blocks the write path — the P5 review
// found synchronous compression under this same lock stalling every node's ingest ACKs.
func (s *fileSink) rotateActiveLocked(newPeriod string) error {
	oldPeriod := s.period
	if err := s.file.Close(); err != nil {
		return err
	}
	activePath := filepath.Join(s.dir, activeFileName)
	archived := true
	slot, err := s.chooseSlotLocked(oldPeriod)
	if err != nil {
		return err
	}
	archivedPath := filepath.Join(s.dir, slot)
	if err := os.Rename(activePath, archivedPath); err != nil {
		archived = false
	}
	file, err := os.OpenFile(activePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return err
	}
	s.file = file
	s.size = 0
	s.period = newPeriod
	if archived {
		s.launchArchive(func() {
			if s.config.Compress {
				s.archiveSlot(slot)
			}
			_ = s.pruneArchives()
		})
	}
	return nil
}

// writeHistoricalLocked appends line to recordPeriod's own file, opening (or reusing a cached) handle for it.
// chooseSlotLocked picks a fresh shard if that period's primary file has already been archived, so a late
// record can never be lost by landing in a file a background sweep is compressing or has already compressed.
func (s *fileSink) writeHistoricalLocked(recordPeriod string, line []byte) error {
	h, ok := s.historical[recordPeriod]
	if !ok {
		slot, err := s.chooseSlotLocked(recordPeriod)
		if err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(s.dir, slot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if err != nil {
			return err
		}
		if err := file.Chmod(0640); err != nil {
			_ = file.Close()
			return err
		}
		h = &historicalHandle{file: file, slot: slot}
		s.historical[recordPeriod] = h
		s.evictHistoricalLocked()
		s.triggerSweep()
	}
	h.lastUsed = time.Now()
	s.touchHistoricalLRU(recordPeriod)
	if _, err := h.file.Write(line); err != nil {
		return err
	}
	return nil
}

func (s *fileSink) touchHistoricalLRU(period string) {
	s.removeFromLRULocked(period)
	s.historicalLRU = append(s.historicalLRU, period)
}

func (s *fileSink) removeFromLRULocked(period string) {
	for i, p := range s.historicalLRU {
		if p == period {
			s.historicalLRU = append(s.historicalLRU[:i], s.historicalLRU[i+1:]...)
			return
		}
	}
}

func (s *fileSink) evictHistoricalLocked() {
	for len(s.historical) > maxHistoricalHandles && len(s.historicalLRU) > 0 {
		oldest := s.historicalLRU[0]
		s.historicalLRU = s.historicalLRU[1:]
		if h, ok := s.historical[oldest]; ok {
			_ = h.file.Close()
			delete(s.historical, oldest)
		}
	}
}

// closeIdleHistoricalLocked closes (but does not delete from disk) every historical handle last written before
// cutoff, so its file is no longer "open" from archiveSlot/pruneArchives's point of view. Called by sweepOnce
// ahead of archiving so a handle merely sitting under the maxHistoricalHandles cap does not block its file from
// ever being archived or pruned (see historicalIdleGrace).
func (s *fileSink) closeIdleHistoricalLocked(cutoff time.Time) {
	for period, h := range s.historical {
		if h.lastUsed.After(cutoff) {
			continue
		}
		_ = h.file.Close()
		delete(s.historical, period)
		s.removeFromLRULocked(period)
	}
}

func (s *fileSink) isHistoricalSlotOpenLocked(name string) bool {
	for _, h := range s.historical {
		if h.slot == name {
			return true
		}
	}
	return false
}

// periodSlotName names the n'th shard of period's file: n==0 is the primary name a fresh period always starts
// with, n>0 is a fallback used only once the previous shard has been archived (see chooseSlotLocked).
func periodSlotName(period string, n int) string {
	if n == 0 {
		return fmt.Sprintf("access-%s.jsonl", period)
	}
	return fmt.Sprintf("access-%s.late%d.jsonl", period, n)
}

// chooseSlotLocked returns the file period should be written to: its primary name if that is still a plain,
// writable file (whether empty/nonexistent or already holding earlier records for period), or the next shard
// once the primary (and any earlier shard) has been archived to ".gz" or is mid-archive (".archiving"). This is
// what lets a delayed record for an already-compressed period still be written without overwriting or losing
// the compressed data.
func (s *fileSink) chooseSlotLocked(period string) (string, error) {
	for n := 0; n < maxSlotShards; n++ {
		name := periodSlotName(period, n)
		base := filepath.Join(s.dir, name)
		taken := false
		for _, suffix := range []string{".gz", ".archiving"} {
			if _, err := os.Stat(base + suffix); err == nil {
				taken = true
				break
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		if !taken {
			return name, nil
		}
	}
	return "", fmt.Errorf("access log period %s exhausted %d shard files", period, maxSlotShards)
}

func rotationPeriod(mode string, at time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	at = at.In(location)
	switch mode {
	case "day":
		return at.Format("20060102")
	case "hour":
		return at.Format("2006010215")
	default:
		return ""
	}
}

var (
	legacyArchivePattern = regexp.MustCompile(`^access-(\d{8}T\d{6}\.\d{9}Z)-\d+\.jsonl(?:\.gz)?$`)
	periodArchivePattern = regexp.MustCompile(`^access-(\d{8}|\d{10})(?:\.late\d+)?\.jsonl(?:\.gz)?$`)
)

// parseFilePeriod extracts the rotation period an on-disk access log file's content belongs to, for the two
// name shapes this sink can produce under "day"/"hour" rotation:
//   - access-<period>.jsonl(.gz)?        the primary file for a period
//   - access-<period>.lateN.jsonl(.gz)?  an extra shard opened after the primary was archived (chooseSlotLocked)
//
// and the legacy name every rotation mode used before this review: access-<timestamp>-<seq>.jsonl(.gz)?. A
// period-shaped name is trusted only if its digit width matches rotation's (8 for day, 10 for hour); a mismatch
// means it was written under a different rotation mode and its window boundaries are not comparable, so it is
// left out of prunePeriodicArchives's grouping rather than risk mis-sorting it. The legacy timestamp is always
// UTC (rotateSizedLocked writes it with at.UTC()), so it can be reinterpreted under the current rotation/location
// safely.
func parseFilePeriod(name, rotation string, location *time.Location) (string, bool) {
	wantWidth := 0
	switch rotation {
	case "day":
		wantWidth = 8
	case "hour":
		wantWidth = 10
	default:
		return "", false
	}
	if m := periodArchivePattern.FindStringSubmatch(name); m != nil {
		if len(m[1]) != wantWidth {
			return "", false
		}
		return m[1], true
	}
	if m := legacyArchivePattern.FindStringSubmatch(name); m != nil {
		t, err := time.Parse("20060102T150405.000000000Z", m[1])
		if err != nil {
			return "", false
		}
		return rotationPeriod(rotation, t, location), true
	}
	return "", false
}
