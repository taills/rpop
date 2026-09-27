package accesslog

import (
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

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
