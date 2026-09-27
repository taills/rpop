package accesslog

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	activeFileName = "access.jsonl"
	// maxHistoricalHandles bounds how many distinct out-of-period files this sink keeps open at once (see
	// writeHistoricalLocked): a node resuming after a long outage can replay records for many stale periods in a
	// burst, and without a cap each one would hold a file descriptor open indefinitely.
	maxHistoricalHandles = 8
	// maxSlotShards bounds how many same-period shard files (see periodSlotName) chooseSlotLocked will look for
	// before giving up: only ever grows past 1 if a period's file gets archived and then written to again, which
	// a real deployment does at most a handful of times per period.
	maxSlotShards = 1000
	// compressingTempPrefix names gzipFileTo's staging file while it writes a compressed archive: it never
	// starts with "access-", so Search/sweepOnce/pruneArchives (which all glob or filter by that prefix) never
	// see it, even while it is only partially written.
	compressingTempPrefix = ".compressing-"
)

// fileSink is the NDJSON access log adapter. Records for the sink's current rotation period (day or hour, in
// s.location) go to the always-open activeFileName; rotating it out to a period-named archive is driven purely
// by the wall clock (see writePeriodicLocked and rotateActiveLocked), never by comparing one record's timestamp
// against another's. That distinction matters because ingest can interleave a node's real-time records with far
// older ones replayed from its spool (D-series delayed delivery): a record whose own period does not match the
// active one is instead routed straight to that period's own file (writeHistoricalLocked), so a late record can
// never cause the active file to be renamed under the wrong name, or a same-period archive to be misdated (see
// docs/architecture/control-data-plane.md §5's stage 5 review notes for the incident this replaced).
type fileSink struct {
	mu       sync.Mutex
	dir      string
	config   FileConfig
	location *time.Location

	// file/size/sequence/period back the always-open active file. period is the rotation label (day/hour mode)
	// its current content belongs to; size/sequence only matter for "size" rotation, which keeps the original
	// sequential-rename behavior (see rotateSizedLocked) since it has no per-record period to get out of order.
	file     *os.File
	size     int64
	period   string
	sequence uint64

	// historical holds handles opened directly by period name for records whose own period is not the active
	// one, keyed by period; historicalLRU tracks recency for maxHistoricalHandles eviction (evicted handles are
	// simply closed, not deleted, and reopened on the next write for that period).
	historical    map[string]*historicalHandle
	historicalLRU []string

	// sweepWG tracks in-flight background archive/prune jobs (see launchArchive) so Close can wait for them
	// instead of leaving a goroutine racing the directory after the sink is gone.
	sweepWG sync.WaitGroup
}

type historicalHandle struct {
	file     *os.File
	slot     string
	lastUsed time.Time
}

// historicalIdleGrace bounds how long a historical handle (see writeHistoricalLocked) stays open, unused,
// before a sweep closes it and makes its file eligible for archiving/pruning. Long enough that one segment's
// worth of same-period backfill lines (a node replaying its spool writes many records for the same stale period
// in a row; see internal/control/logs_ingest.go's per-line loop) keeps reusing the same handle instead of
// repeatedly closing and reopening it; short enough that a period stops blocking retention soon after the
// backfill that touched it ends. Without this, a handle that is merely under the maxHistoricalHandles cap would
// stay open — and so unarchivable and unprunable — for as long as the sink lives.
const historicalIdleGrace = 2 * time.Second

func newFileSink(dir string, config FileConfig) (*fileSink, error) {
	if dir == "" {
		dir = "logs"
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0750); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, activeFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0640); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	period := ""
	if info.Size() > 0 {
		period = rotationPeriod(config.Rotation, info.ModTime(), time.UTC)
	}
	sink := &fileSink{
		dir:        dir,
		config:     config,
		file:       file,
		location:   time.UTC,
		size:       info.Size(),
		period:     period,
		historical: make(map[string]*historicalHandle),
	}
	// Resume any archive left mid-compression by a previous process that died before it finished (see
	// archiveSlot): put it back under its plain name so a later sweep retries it, and pick up any stale
	// out-of-period file a previous run never got around to archiving.
	sink.resumeInterruptedArchives()
	sink.triggerSweep()
	return sink, nil
}

func (s *fileSink) resumeInterruptedArchives() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".archiving"):
			original := strings.TrimSuffix(name, ".archiving")
			_ = os.Rename(filepath.Join(s.dir, name), filepath.Join(s.dir, original))
		case strings.HasPrefix(name, compressingTempPrefix):
			// gzipFileTo writes a compressed archive under this name and renames it into place only once it
			// is complete (see its doc comment), so one left behind by a process that died mid-compression is
			// always incomplete garbage; the source it was compressing is handled by the case above (or was
			// never renamed away to begin with), so the next sweep just redoes the work.
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

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

func (s *fileSink) Write(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalizeRecordTime(&record)
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return fmt.Errorf("access log file is not open")
	}
	if s.config.Rotation == "day" || s.config.Rotation == "hour" {
		return s.writePeriodicLocked(record, line)
	}
	return s.writeSizedLocked(record, line)
}

// writeSizedLocked implements "size" rotation (and the no-op default): a single sequential file, rotated by
// accumulated size, exactly as before this review (it has no record-ordering hazard: rotation is triggered by
// this sink's own write volume, not by any record's timestamp, so out-of-order timestamps cannot mis-rotate it).
func (s *fileSink) writeSizedLocked(record Record, line []byte) error {
	if s.config.Rotation == "size" && s.size > 0 && s.size+int64(len(line)) > s.config.MaxSizeBytes {
		if err := s.rotateSizedLocked(record.Timestamp); err != nil {
			return err
		}
	}
	if _, err := s.file.Write(line); err != nil {
		return err
	}
	s.size += int64(len(line))
	return nil
}

// writePeriodicLocked implements "day"/"hour" rotation. It first rotates the active file if the wall clock has
// moved into a new period since it was opened (never based on record.Timestamp: that is what let one delayed
// record rename a file full of unrelated, current records in the bug this replaced). It then routes record by
// its own period: matching the active period, or empty (no rotation configured) goes to the active file; a
// mismatch — almost always a delayed record — goes to that period's own file instead, so it can never perturb
// the active file's name or contents.
func (s *fileSink) writePeriodicLocked(record Record, line []byte) error {
	wallPeriod := rotationPeriod(s.config.Rotation, time.Now(), s.location)
	switch {
	case s.period == "":
		s.period = wallPeriod
	case wallPeriod != "" && wallPeriod != s.period:
		if err := s.rotateActiveLocked(wallPeriod); err != nil {
			return err
		}
	}
	recordPeriod := rotationPeriod(s.config.Rotation, record.Timestamp, s.location)
	if recordPeriod == "" || recordPeriod == s.period {
		if _, err := s.file.Write(line); err != nil {
			return err
		}
		s.size += int64(len(line))
		return nil
	}
	return s.writeHistoricalLocked(recordPeriod, line)
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

// rotateSizedLocked is the original size-rotation implementation: rename is synchronous (it is what makes the
// active path available again immediately), compression and pruning happen in the background (see
// rotateActiveLocked's doc comment for why).
func (s *fileSink) rotateSizedLocked(at time.Time) error {
	if err := s.file.Close(); err != nil {
		return err
	}
	s.sequence++
	rawName := fmt.Sprintf("access-%s-%06d.jsonl", at.UTC().Format("20060102T150405.000000000Z"), s.sequence)
	rawPath := filepath.Join(s.dir, rawName)
	activePath := filepath.Join(s.dir, activeFileName)
	if err := os.Rename(activePath, rawPath); err != nil {
		file, openErr := os.OpenFile(activePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
		if openErr == nil {
			s.file = file
		}
		return err
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
	compress := s.config.Compress
	s.launchArchive(func() {
		if compress {
			_ = gzipFile(rawPath)
		}
		_ = s.pruneArchives()
	})
	return nil
}

// launchArchive runs job in the background, tracked by sweepWG so Close can wait for it. Compression and
// pruning are the only work ever handed to it; both tolerate running concurrently with fresh writes (see
// archiveSlot and pruneArchives).
func (s *fileSink) launchArchive(job func()) {
	s.sweepWG.Add(1)
	go func() {
		defer s.sweepWG.Done()
		job()
	}()
}

// triggerSweep schedules a background pass over every non-active period file: archiving (if configured) any
// that is not currently open for writing, then pruning by the result (see sweepOnce). Called after every active
// rotation and after opening a new historical handle, so a burst of delayed records from a long-disconnected
// node gets swept promptly rather than waiting for the active period to itself roll over.
func (s *fileSink) triggerSweep() {
	if s.config.Rotation != "day" && s.config.Rotation != "hour" {
		return
	}
	s.launchArchive(s.sweepOnce)
}

func (s *fileSink) sweepOnce() {
	s.mu.Lock()
	s.closeIdleHistoricalLocked(time.Now().Add(-historicalIdleGrace))
	s.mu.Unlock()
	if s.config.Compress {
		entries, err := os.ReadDir(s.dir)
		if err == nil {
			for _, entry := range entries {
				name := entry.Name()
				if name == activeFileName || !strings.HasPrefix(name, "access-") || !strings.HasSuffix(name, ".jsonl") {
					continue
				}
				s.archiveSlot(name)
			}
		}
	}
	_ = s.pruneArchives()
}

// archiveSlot compresses name (a period's primary or shard file) to name+".gz", unless a handle for it is
// currently open (still receiving writes) or it has already been archived. The rename to ".archiving" happens
// under s.mu so it is atomic with respect to writeHistoricalLocked's own "is this slot open" check: once
// renamed, a concurrent late write for the same period finds the primary name free again and opens a fresh
// shard (chooseSlotLocked) rather than racing this compression. Compression itself (the slow part) runs without
// the lock held.
func (s *fileSink) archiveSlot(name string) {
	s.mu.Lock()
	if s.isHistoricalSlotOpenLocked(name) {
		s.mu.Unlock()
		return
	}
	stagedPath := filepath.Join(s.dir, name+".archiving")
	if err := os.Rename(filepath.Join(s.dir, name), stagedPath); err != nil {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	if err := gzipFileTo(stagedPath, filepath.Join(s.dir, name+".gz")); err != nil {
		// Best-effort recovery: put it back under its plain name so the next sweep retries it.
		_ = os.Rename(stagedPath, filepath.Join(s.dir, name))
	}
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

// gzipFileTo compresses srcPath to destPath, making destPath appear only once compression has fully succeeded.
// It writes under a temporary, compressingTempPrefix-named path in the same directory and renames that into
// destPath as the last step (a same-directory rename is atomic), rather than creating destPath itself up front
// and filling it in: archiveSlot calls this in the background, outside the sink's lock, while Search (and
// sweepOnce/pruneArchives) can run concurrently, and any of them could otherwise glob or open destPath's final
// name while it still held only a partial, undecodable gzip stream.
func gzipFileTo(srcPath, destPath string) error {
	input, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer input.Close()
	tmpPath := filepath.Join(filepath.Dir(destPath), compressingTempPrefix+filepath.Base(destPath))
	output, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return err
	}
	writer := gzip.NewWriter(output)
	_, copyErr := io.Copy(writer, input)
	closeErr := writer.Close()
	fileErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return closeErr
	}
	if fileErr != nil {
		_ = os.Remove(tmpPath)
		return fileErr
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Remove(srcPath)
}

func gzipFile(path string) error {
	return gzipFileTo(path, path+".gz")
}

// pruneArchives enforces FileConfig.KeepFiles. Under "day"/"hour" rotation it keeps the newest KeepFiles
// *periods* (prunePeriodicArchives): a period's primary file plus any late shards and its compressed form all
// count once, so a period is never partially pruned. Under "size" rotation, files already sort chronologically
// by construction (timestamp-sequence), so keeping the newest KeepFiles by name (pruneSequentialArchives, the
// original algorithm) is exact.
func (s *fileSink) pruneArchives() error {
	if s.config.KeepFiles <= 0 {
		return nil
	}
	if s.config.Rotation == "day" || s.config.Rotation == "hour" {
		return s.prunePeriodicArchives()
	}
	return s.pruneSequentialArchives()
}

func (s *fileSink) prunePeriodicArchives() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	// prunePeriodicArchives runs from the background sweep (sweepOnce/rotateActiveLocked's launchArchive job),
	// outside s.mu, while SetTimeZone can concurrently replace s.location under lock; take a snapshot instead of
	// reading the field directly so the two never race.
	location := s.locationSnapshot()
	type candidate struct {
		name   string
		period string
	}
	var candidates []candidate
	periods := make(map[string]struct{})
	for _, entry := range entries {
		name := entry.Name()
		if name == activeFileName || strings.HasSuffix(name, ".archiving") {
			continue
		}
		period, ok := parseFilePeriod(name, s.config.Rotation, location)
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{name, period})
		periods[period] = struct{}{}
	}
	if len(periods) <= s.config.KeepFiles {
		return nil
	}
	ordered := make([]string, 0, len(periods))
	for p := range periods {
		ordered = append(ordered, p)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ordered)))
	keep := make(map[string]struct{}, s.config.KeepFiles)
	for _, p := range ordered[:s.config.KeepFiles] {
		keep[p] = struct{}{}
	}
	openSlots := s.openHistoricalSlotNames()
	var errs []error
	for _, c := range candidates {
		if _, ok := keep[c.period]; ok {
			continue
		}
		if openSlots[c.name] {
			// Still being written; leave it for a later sweep once it is idle rather than yank the file out
			// from under an open handle.
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, c.name)); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *fileSink) openHistoricalSlotNames() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make(map[string]bool, len(s.historical))
	for _, h := range s.historical {
		names[h.slot] = true
	}
	return names
}

func (s *fileSink) pruneSequentialArchives() error {
	files, err := filepath.Glob(filepath.Join(s.dir, "access-*.jsonl*"))
	if err != nil {
		return err
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	if len(files) <= s.config.KeepFiles {
		return nil
	}
	var errs []error
	for _, path := range files[s.config.KeepFiles:] {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *fileSink) Search(ctx context.Context, query Query) (SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, err := filepath.Glob(filepath.Join(s.dir, "access-*.jsonl*"))
	if err != nil {
		return SearchResult{}, err
	}
	active := filepath.Join(s.dir, activeFileName)
	if _, err := os.Stat(active); err == nil {
		paths = append(paths, active)
	}
	var records []Record
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		entries, err := readRecords(ctx, path, query)
		if err != nil {
			return SearchResult{}, err
		}
		records = append(records, entries...)
	}
	return paginate(records, query), nil
}

func readRecords(ctx context.Context, path string, query Query) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".gz") {
		gzipReader, err := gzip.NewReader(file)
		if err != nil {
			return nil, err
		}
		defer gzipReader.Close()
		reader = gzipReader
	}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	var found []Record
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > 0 {
			var record Record
			if err := json.Unmarshal(line, &record); err != nil {
				return nil, fmt.Errorf("parse access log %s: %w", filepath.Base(path), err)
			}
			if matches(record, query) {
				found = append(found, record)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return found, nil
}

func (s *fileSink) Close() error {
	s.mu.Lock()
	var errs []error
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			errs = append(errs, err)
		}
		s.file = nil
	}
	for period, h := range s.historical {
		if err := h.file.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(s.historical, period)
	}
	s.historicalLRU = nil
	s.mu.Unlock()
	s.sweepWG.Wait()
	return errors.Join(errs...)
}
