package dataplane

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestRequestRecordParsesTLSAndIPv6Client(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://secure.example/a?b=c", nil)
	req.RemoteAddr = "[2001:db8::1]:443"
	record := requestRecord(req, "site-tls", "https://backend.example", time.Unix(0, 0))
	if record.ClientIP != "2001:db8::1" || record.ClientPort != 443 || record.Scheme != "https" || record.TLSVersion == "" || record.Host != "secure.example" || record.Path != "/a?b=c" {
		t.Fatalf("unexpected TLS record: %#v", record)
	}
	req.RemoteAddr = "@unix"
	if ip, port := splitRemoteAddr(req.RemoteAddr); ip != "@unix" || port != 0 {
		t.Fatalf("unparsable remote address = %q, %d", ip, port)
	}
}

func TestUnlimitedBodyCaptureDoesNotTruncate(t *testing.T) {
	capture := newBodyCapture(-1)
	chunk := bytes.Repeat([]byte{'x'}, 32<<10)
	target := maxBodyLogLimit + 1
	var written int64
	for written < target {
		n := int64(len(chunk))
		if n > target-written {
			n = target - written
		}
		if _, err := capture.Write(chunk[:n]); err != nil {
			t.Fatal(err)
		}
		written += n
	}
	data, total, truncated := capture.snapshot()
	if total != target || int64(len(data)) != target || truncated {
		t.Fatalf("unlimited capture truncated body: total=%d stored=%d truncated=%v", total, len(data), truncated)
	}
}

func TestAccessLogQueueAllowsSingleOversizedEvent(t *testing.T) {
	q := &logQueue{events: make(chan accessLogEvent, 1), log: zap.NewNop(), dropped: func(string) {}}
	size := maxQueuedAccessLogBytes + 1
	if !q.reserve(size) {
		t.Fatal("expected empty queue to admit one oversized full-body log")
	}
	if q.reserve(1) {
		t.Fatal("expected oversized queued body to prevent unbounded additional queueing")
	}
	q.queuedBytes.Add(-size)
}
