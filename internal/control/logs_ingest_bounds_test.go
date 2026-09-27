package control

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/southbound"
)

// zeroReader produces an endless stream of the same byte; combined with ingestLogSegment's own
// io.LimitReader(gz, maxDecompressedLogSegmentBytes+1), it stops after exactly that many bytes without ever
// needing a real gzip stream that decompresses to tens of megabytes (a genuine zip bomb is covered end to end,
// over real gzip, by TestSouthboundLogsRejectsOversizedSegmentBody's wire-size cap; this test is the
// decompressed-size cap that protects against a small compressed body expanding far past it).
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestIngestLogSegmentRejectsExcessiveDecompressedSize(t *testing.T) {
	c := newTestControl(t)
	_, err := c.ingestLogSegment(context.Background(), "edge-1", zeroReader{})
	if err == nil {
		t.Fatal("expected an error once the decompressed segment exceeds maxDecompressedLogSegmentBytes")
	}
}

func TestIngestLogSegmentDropsAnOversizedLineWithoutFailingTheSegment(t *testing.T) {
	c := newTestControl(t)
	oversizedLine := bytes.Repeat([]byte("a"), maxLogRecordLineBytes+1)
	validLine := accessEnvelope(t, "default", accesslog.Record{SiteID: "site-a"})
	body := io.MultiReader(bytes.NewReader(oversizedLine), strings.NewReader("\n"+validLine+"\n"))

	result, err := c.ingestLogSegment(context.Background(), "edge-1", body)
	if err != nil {
		t.Fatalf("an oversized line must be dropped, not fail the segment: %v", err)
	}
	if result.undecodableLines != 1 {
		t.Fatalf("undecodableLines = %d, want 1 (the oversized line)", result.undecodableLines)
	}
	// The node has no site selecting "default" in this test, so the otherwise-valid second line is rejected on
	// adapter placement, not decoding — proof that ingestLogSegment kept reading past the oversized line.
	if result.rejectedAdapterLines != 1 {
		t.Fatalf("rejectedAdapterLines = %d, want 1 (the line after the oversized one was still processed)", result.rejectedAdapterLines)
	}
}

func TestMaxLogRecordLineBytesMatchesTheSouthboundSegmentBound(t *testing.T) {
	// A single record can legitimately approach southbound.MaxLogSegmentBytes (see its doc comment); the
	// per-line cap must not be stricter than that, or a legitimate oversized record would always be dropped.
	if maxLogRecordLineBytes != southbound.MaxLogSegmentBytes {
		t.Fatalf("maxLogRecordLineBytes = %d, want %d", maxLogRecordLineBytes, southbound.MaxLogSegmentBytes)
	}
}
