package control

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/store"
)

const (
	defaultBodyLogLimit     int64 = 1 << 20
	maxBodyLogLimit         int64 = 8 << 20
	maxQueuedAccessLogBytes int64 = 32 << 20
)

type accessLogEvent struct {
	fields                    []zap.Field
	requestBody, responseBody *bodyCapture
	includeBodies             bool
	bytes                     int64
	barrier                   chan struct{}
}

func (c *Control) reserveAccessLog(size int64) bool {
	if size < 0 || size > maxQueuedAccessLogBytes {
		return false
	}
	for {
		current := c.queuedLogBytes.Load()
		if current+size > maxQueuedAccessLogBytes {
			return false
		}
		if c.queuedLogBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func (c *Control) accessLogLoop() {
	for event := range c.accessLogQueue {
		if event.barrier != nil {
			close(event.barrier)
			continue
		}
		fields := event.fields
		if event.includeBodies {
			fields = append(fields, bodyFields("request", event.requestBody)...)
			fields = append(fields, bodyFields("response", event.responseBody)...)
		}
		c.log.Info("site HTTP access", fields...)
		c.queuedLogBytes.Add(-event.bytes)
	}
}

func (c *Control) DrainAccessLogs(ctx context.Context) error {
	barrier := make(chan struct{})
	select {
	case c.accessLogQueue <- accessLogEvent{barrier: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type MetricsSnapshot struct {
	RequestCount          uint64         `json:"requestCount"`
	ErrorCount            uint64         `json:"errorCount"`
	DroppedAccessLogCount uint64         `json:"droppedAccessLogCount"`
	InFlight              uint64         `json:"inFlight"`
	BytesReceived         uint64         `json:"bytesReceived"`
	BytesSent             uint64         `json:"bytesSent"`
	StatusCodes           map[int]uint64 `json:"statusCodes"`
	AverageTTFBMillis     float64        `json:"averageTtfbMillis"`
	AverageResponseMillis float64        `json:"averageResponseMillis"`
	P95ResponseMillis     float64        `json:"p95ResponseMillis"`
	MaxResponseMillis     float64        `json:"maxResponseMillis"`
}

type siteMetrics struct {
	mu                                                         sync.RWMutex
	requests, errors, inFlight, bytesIn, bytesOut, droppedLogs uint64
	ttfbCount                                                  uint64
	ttfbTotal, durationTotal, maxDuration                      time.Duration
	statusCodes                                                map[int]uint64
	durationBuckets                                            [8]uint64
}

var responseDurationBuckets = [...]time.Duration{10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 3 * time.Second}

func (m *siteMetrics) dropLog() { m.mu.Lock(); m.droppedLogs++; m.mu.Unlock() }
func (m *siteMetrics) begin()   { m.mu.Lock(); m.inFlight++; m.mu.Unlock() }
func (m *siteMetrics) finish(status int, bytesIn, bytesOut uint64, ttfb, elapsed time.Duration, hasTTFB bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	if status >= 500 {
		m.errors++
	}
	if m.inFlight > 0 {
		m.inFlight--
	}
	m.bytesIn += bytesIn
	m.bytesOut += bytesOut
	if m.statusCodes == nil {
		m.statusCodes = make(map[int]uint64)
	}
	m.statusCodes[status]++
	m.durationTotal += elapsed
	if elapsed > m.maxDuration {
		m.maxDuration = elapsed
	}
	bucket := len(responseDurationBuckets)
	for i, bound := range responseDurationBuckets {
		if elapsed <= bound {
			bucket = i
			break
		}
	}
	m.durationBuckets[bucket]++
	if hasTTFB {
		m.ttfbCount++
		m.ttfbTotal += ttfb
	}
}
func (m *siteMetrics) snapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := MetricsSnapshot{RequestCount: m.requests, ErrorCount: m.errors, DroppedAccessLogCount: m.droppedLogs, InFlight: m.inFlight, BytesReceived: m.bytesIn, BytesSent: m.bytesOut, StatusCodes: make(map[int]uint64, len(m.statusCodes)), MaxResponseMillis: float64(m.maxDuration) / float64(time.Millisecond)}
	for code, count := range m.statusCodes {
		s.StatusCodes[code] = count
	}
	if m.requests > 0 {
		s.AverageResponseMillis = float64(m.durationTotal) / float64(m.requests) / float64(time.Millisecond)
	}
	if m.ttfbCount > 0 {
		s.AverageTTFBMillis = float64(m.ttfbTotal) / float64(m.ttfbCount) / float64(time.Millisecond)
	}
	if m.requests > 0 {
		target := (m.requests*95 + 99) / 100
		var count uint64
		for i, n := range m.durationBuckets {
			count += n
			if count >= target {
				if i < len(responseDurationBuckets) {
					s.P95ResponseMillis = float64(responseDurationBuckets[i]) / float64(time.Millisecond)
				} else {
					s.P95ResponseMillis = s.MaxResponseMillis
				}
				break
			}
		}
	}
	return s
}

type bodyCapture struct {
	mu    sync.Mutex
	data  []byte
	total int64
	limit int64
}

func newBodyCapture(limit int64) *bodyCapture { return &bodyCapture{limit: limit} }
func (b *bodyCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if remain := b.limit - int64(len(b.data)); remain > 0 {
		n := int64(len(p))
		if n > remain {
			n = remain
		}
		b.data = append(b.data, p[:n]...)
	}
	return len(p), nil
}
func (b *bodyCapture) totalBytes() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.total }

func (b *bodyCapture) storedBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.data))
}

func (b *bodyCapture) snapshot() ([]byte, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...), b.total, b.total > int64(len(b.data))
}

type observedResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  uint64
	body   *bodyCapture
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedResponseWriter) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *observedResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.bytes += uint64(n)
		if w.body != nil {
			_, _ = w.body.Write(p[:n])
		}
	}
	return n, err
}
func (w *observedResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *observedResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	if w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}
func (w *observedResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func (c *Control) metricsForSite(siteID string) *siteMetrics {
	value, _ := c.metrics.LoadOrStore(siteID, &siteMetrics{})
	return value.(*siteMetrics)
}

func (c *Control) observeSite(siteID string, settings store.AccessLogConfig, next http.Handler) http.Handler {
	limit := settings.MaxBodyBytes
	if limit <= 0 {
		limit = defaultBodyLogLimit
	}
	if limit > maxBodyLogLimit {
		limit = maxBodyLogLimit
	}
	if !settings.IncludeBodies {
		limit = 0
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		m := c.metricsForSite(siteID)
		m.begin()
		var requestBody *bodyCapture
		var requestBytes atomic.Uint64
		if settings.IncludeBodies {
			requestBody = newBodyCapture(limit)
			if r.Body != nil {
				r.Body = &teeReadCloser{ReadCloser: r.Body, tee: requestBody}
			}
		} else if r.Body != nil {
			r.Body = &countingReadCloser{ReadCloser: r.Body, bytes: &requestBytes}
		}
		var responseBody *bodyCapture
		if settings.IncludeBodies {
			responseBody = newBodyCapture(limit)
		}
		rw := &observedResponseWriter{ResponseWriter: w, body: responseBody}
		var firstByte time.Duration
		var hasTTFB bool
		var firstMu sync.Mutex
		trace := &httptrace.ClientTrace{GotFirstResponseByte: func() {
			firstMu.Lock()
			if !hasTTFB {
				firstByte = time.Since(started)
				hasTTFB = true
			}
			firstMu.Unlock()
		}}
		ctx := httptrace.WithClientTrace(r.Context(), trace)
		next.ServeHTTP(rw, r.WithContext(ctx))
		elapsed := time.Since(started)
		if rw.status == 0 {
			rw.status = http.StatusOK
		}
		firstMu.Lock()
		ttfb, gotTTFB := firstByte, hasTTFB
		firstMu.Unlock()
		requestBytesCount := requestBytes.Load()
		if requestBody != nil {
			requestBytesCount = uint64(requestBody.totalBytes())
		}
		requestBytesInt64 := int64(requestBytesCount)
		m.finish(rw.status, requestBytesCount, rw.bytes, ttfb, elapsed, gotTTFB)
		requestHeaders := loggedHeaders(r.Header, settings.IncludeSensitiveHeaders)
		responseHeaders := loggedHeaders(rw.Header(), settings.IncludeSensitiveHeaders)
		fields := []zap.Field{zap.String("site_id", siteID), zap.String("method", r.Method), zap.String("path", r.URL.RequestURI()), zap.String("protocol", r.Proto), zap.Any("request_headers", requestHeaders), zap.Int("status", rw.status), zap.Any("response_headers", responseHeaders), zap.Int64("request_body_bytes", requestBytesInt64), zap.Uint64("response_body_bytes", rw.bytes), zap.Duration("ttfb", ttfb), zap.Duration("response_time", elapsed)}
		eventBytes := int64(2048 + headerBytes(requestHeaders) + headerBytes(responseHeaders))
		if settings.IncludeBodies {
			eventBytes += requestBody.storedBytes() + responseBody.storedBytes()
		}
		if !c.reserveAccessLog(eventBytes) {
			m.dropLog()
			return
		}
		event := accessLogEvent{fields: fields, requestBody: requestBody, responseBody: responseBody, includeBodies: settings.IncludeBodies, bytes: eventBytes}
		select {
		case c.accessLogQueue <- event:
		default:
			c.queuedLogBytes.Add(-eventBytes)
			m.dropLog()
		}
	})
}

type teeReadCloser struct {
	io.ReadCloser
	tee io.Writer
}

func (r *teeReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		_, _ = r.tee.Write(p[:n])
	}
	return n, err
}

type countingReadCloser struct {
	io.ReadCloser
	bytes *atomic.Uint64
}

func (r *countingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if n > 0 {
		r.bytes.Add(uint64(n))
	}
	return n, err
}

func loggedHeaders(h http.Header, includeSensitive bool) map[string][]string {
	out := make(map[string][]string, len(h))
	for key, values := range h {
		if !includeSensitive && sensitiveHeader(key) {
			out[key] = []string{"[REDACTED]"}
		} else {
			out[key] = append([]string(nil), values...)
		}
	}
	return out
}
func headerBytes(headers map[string][]string) int64 {
	var total int64
	for name, values := range headers {
		total += int64(len(name))
		for _, value := range values {
			total += int64(len(value))
		}
	}
	return total
}

func sensitiveHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-api-key":
		return true
	}
	return false
}
func bodyFields(prefix string, b *bodyCapture) []zap.Field {
	data, total, truncated := b.snapshot()
	encoding := "utf-8"
	value := string(data)
	if !utf8.Valid(data) {
		encoding = "base64"
		value = base64.StdEncoding.EncodeToString(data)
	}
	return []zap.Field{zap.String(prefix+"_body", value), zap.String(prefix+"_body_encoding", encoding), zap.Int64(prefix+"_body_total_bytes", total), zap.Bool(prefix+"_body_truncated", truncated)}
}

var _ http.Flusher = (*observedResponseWriter)(nil)
var _ http.Hijacker = (*observedResponseWriter)(nil)
var _ http.Pusher = (*observedResponseWriter)(nil)
