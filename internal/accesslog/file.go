package accesslog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
