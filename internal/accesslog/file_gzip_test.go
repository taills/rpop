package accesslog

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGzipFileToRenamesIntoPlaceOnlyOnceComplete covers a race the P5 review's file-adapter fix introduced:
// archiveSlot now runs gzipFileTo in the background, outside the sink's lock, so Search (which globs
// "access-*.jsonl*" while holding the lock) can run concurrently with it. If gzipFileTo wrote straight to its
// final ".gz" name, as it once did, a Search landing mid-write would glob a file that is either not yet a
// complete gzip stream (gzip.NewReader/io.ReadAll fails) or, worse, would still exist under a stale, half
// -written name forever if the process crashed mid-compression. A FIFO source lets the test hold gzipFileTo
// open on an incomplete read deterministically, instead of racing a real clock against it.
func TestGzipFileToRenamesIntoPlaceOnlyOnceComplete(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.jsonl")
	if err := syscall.Mkfifo(srcPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	destPath := filepath.Join(dir, "access-20250101.jsonl.gz")

	writerReady := make(chan *os.File, 1)
	writerErr := make(chan error, 1)
	go func() {
		w, err := os.OpenFile(srcPath, os.O_WRONLY, 0)
		if err != nil {
			writerErr <- err
			return
		}
		writerReady <- w
	}()

	done := make(chan error, 1)
	go func() { done <- gzipFileTo(srcPath, destPath) }()

	var writer *os.File
	select {
	case writer = <-writerReady:
	case err := <-writerErr:
		t.Fatalf("open fifo for writing: %v", err)
	}
	payload := bytes.Repeat([]byte("x"), 4096)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write to fifo: %v", err)
	}

	// gzipFileTo is now blocked reading more of the pipe (it has no way to see an EOF yet). Give it plenty of
	// time to have reached that blocked read (it does essentially no work before then), so the follow-up stat
	// reflects its steady state rather than racing the goroutine that just unblocked from opening the fifo.
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(destPath); !os.IsNotExist(err) {
		t.Fatalf("stat dest mid-write: err=%v, want IsNotExist", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close fifo writer: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("gzipFileTo: %v", err)
	}

	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest after gzipFileTo returned: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("gzip.NewReader(dest): %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress dest: %v", err)
	}
	if !bytes.Equal(decompressed, payload) {
		t.Fatalf("decompressed dest = %q, want %q", decompressed, payload)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Fatalf("src still exists after gzipFileTo succeeded: err=%v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), compressingTempPrefix) {
			t.Fatalf("temp compression file %s left behind after gzipFileTo succeeded", entry.Name())
		}
	}
}

// TestResumeInterruptedArchivesRemovesOrphanedCompressionTemp covers a process crashing mid-compression: the
// leftover compressingTempPrefix file is always incomplete garbage (gzipFileTo only renames it into place once
// finished), so a fresh sink must clear it rather than let it block that name forever.
func TestResumeInterruptedArchivesRemovesOrphanedCompressionTemp(t *testing.T) {
	dir := t.TempDir()
	orphan := filepath.Join(dir, compressingTempPrefix+"access-20250101.jsonl.gz")
	if err := os.WriteFile(orphan, []byte("partial"), 0o640); err != nil {
		t.Fatal(err)
	}

	sink, err := newFileSink(dir, FileConfig{Rotation: "day"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned compression temp file survived sink startup: err=%v", err)
	}
}
