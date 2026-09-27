package spool

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// segmentFilePattern matches the zero-padded segment file names spool writes (docs/architecture/
// control-data-plane.md §5): a 20-digit decimal segment number, then the fixed gzip NDJSON suffix.
var segmentFilePattern = regexp.MustCompile(`^(\d{20})\.jsonl\.gz$`)

func segmentFileName(seq uint64) string { return fmt.Sprintf("%020d.jsonl.gz", seq) }

func segmentPath(dir string, seq uint64) string { return filepath.Join(dir, segmentFileName(seq)) }

func parseSegmentFileName(name string) (uint64, bool) {
	m := segmentFilePattern.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	seq, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

// listSegmentFiles returns the segment numbers present in dir, ascending.
func listSegmentFiles(dir string) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []uint64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if seq, ok := parseSegmentFileName(entry.Name()); ok {
			segs = append(segs, seq)
		}
	}
	slices.Sort(segs)
	return segs, nil
}

// segmentWriter is the segment currently being appended to. It streams straight into gzip rather than buffering
// plain NDJSON and compressing on close, so a crash mid-segment leaves a truncated *gzip* file on disk for
// recoverSegment to salvage or drop, matching what the architecture doc calls out as the recovery case.
type segmentWriter struct {
	seq    uint64
	file   *os.File
	gz     *gzip.Writer
	opened time.Time

	// rawBytes, unflushedRecords, and lastPersist are bookkeeping for the segment currently being written.
	// Only Spool.loop's goroutine ever calls write/persist/close below, so it is the only one that ever needs
	// to change these three fields - but Spool.Stats, test helpers, and loop's own next-iteration timer setup
	// all read them from whichever goroutine happens to call them. Spool holds its own mu around every read
	// and write of these three fields (never around the gz.Write/Flush/file.Sync calls themselves, so a slow
	// disk never blocks Pending/Stats/Ack); see Spool.writeRecord and Spool.persistOpen.
	rawBytes         int64
	unflushedRecords int
	lastPersist      time.Time
}

func createSegmentWriter(dir string, seq uint64, opened time.Time) (*segmentWriter, error) {
	file, err := os.OpenFile(segmentPath(dir, seq), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &segmentWriter{seq: seq, file: file, gz: gzip.NewWriter(file), opened: opened, lastPersist: opened}, nil
}

// write appends one already-newline-terminated NDJSON line to the segment's gzip stream. It only buffers the
// line in gzip's own internal buffers (and, once those fill, in the OS's page cache via ordinary unsynced
// Write calls) - it does not flush the gzip stream or fsync the file, so the line is not yet durable. Batching
// many lines between calls to persist is what turns an fsync-per-record write pattern into a bounded number of
// fsyncs per batch or per MaxPersistInterval; see persist and Spool.drainBatch. write does not touch rawBytes
// or unflushedRecords itself - see the segmentWriter doc comment for why the caller (under Spool.mu) does.
func (w *segmentWriter) write(line []byte) error {
	_, err := w.gz.Write(line)
	return err
}

// persist unconditionally flushes every line written since the last persist (or since the segment was opened)
// out of gzip's internal buffers and fsyncs the underlying file, making them durable. The caller (Spool,
// holding mu) is responsible for checking unflushedRecords first and for resetting unflushedRecords/lastPersist
// afterwards - see the segmentWriter doc comment for why persist itself does not touch either field.
func (w *segmentWriter) persist() error {
	if err := w.gz.Flush(); err != nil {
		return err
	}
	return w.file.Sync()
}

// close finalizes the gzip stream (trailer and checksum) and fsyncs it, so what recoverSegment sees after a
// clean shutdown always decompresses without error.
func (w *segmentWriter) close() error {
	if err := w.gz.Close(); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.file.Sync(); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

// recoverSegment validates the gzip NDJSON segment at path, called only for the one segment number that could
// have been open when the process last stopped (see Spool's startup recovery). A segment that decompresses
// cleanly to the end is left untouched. One truncated by a crash (dead mid gzip-write, mid-flush, or mid-fsync)
// is rewritten keeping only the NDJSON lines that were completely flushed before the cut; a segment with no
// complete line at all is deleted outright. kept reports which happened, so the caller can log and count the
// loss (docs/architecture/control-data-plane.md §5).
func recoverSegment(path string) (kept bool, size int64, err error) {
	raw, readErr := os.ReadFile(path)
	if errors.Is(readErr, fs.ErrNotExist) {
		return false, 0, nil
	}
	if readErr != nil {
		return false, 0, readErr
	}
	data, decodeErr := decompressBestEffort(raw)
	if decodeErr == nil {
		return true, int64(len(raw)), nil // a complete gzip member; nothing to salvage.
	}
	lines := completeLines(data)
	if len(lines) == 0 {
		return false, 0, os.Remove(path)
	}
	if err := rewriteSegment(path, lines); err != nil {
		return false, 0, err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return true, 0, statErr
	}
	return true, info.Size(), nil
}

// decompressBestEffort returns everything gzip.Reader could decode from raw, and the error (if any) that ended
// the read; a nil error means raw is a complete, checksum-valid gzip member.
func decompressBestEffort(raw []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

// completeLines keeps only the lines of data that end in '\n'. A trailing partial line means the writer was cut
// off before flushing that record's newline, so its bytes cannot be trusted.
func completeLines(data []byte) []byte {
	idx := bytes.LastIndexByte(data, '\n')
	if idx < 0 {
		return nil
	}
	return data[:idx+1]
}

// rewriteSegment replaces path with a fresh, complete gzip member holding exactly lines.
func rewriteSegment(path string, lines []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".recover-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	gz := gzip.NewWriter(temp)
	if _, err := gz.Write(lines); err != nil {
		_ = gz.Close()
		_ = temp.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// writeFileAtomic replaces a file readable only by its owner, so a crash never leaves it half written. It
// mirrors internal/agent's helper of the same name; spool cannot import that package (agent imports spool to
// wire it up), so the few lines are kept local instead of introducing a shared utility package for them alone.
func writeFileAtomic(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
