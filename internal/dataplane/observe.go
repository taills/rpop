package dataplane

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/snapshot"
)

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
	if b.limit < 0 {
		b.data = append(b.data, p...)
	} else if remain := b.limit - int64(len(b.data)); remain > 0 {
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

func (e *Engine) metricsFor(siteID string) *siteMetrics {
	value, _ := e.metrics.LoadOrStore(siteID, &siteMetrics{})
	return value.(*siteMetrics)
}

// Metrics reports the counters of one site; they survive restarts and reloads of the site.
func (e *Engine) Metrics(siteID string) MetricsSnapshot {
	return e.metricsFor(siteID).snapshot()
}

// ForgetMetrics discards the counters of a deleted site.
func (e *Engine) ForgetMetrics(siteID string) {
	e.metrics.Delete(siteID)
}

// routeTrace lets the routing handler report the chosen upstream (redacted URL) and route label back to observeSite.
type routeTrace struct {
	upstream, route string
}

type routeTraceKey struct{}

// observeSite records metrics and access logs for a site.
func (e *Engine) observeSite(siteID string, settings snapshot.AccessLog, next http.Handler) http.Handler {
	loggingEnabled := strings.TrimSpace(settings.AdapterID) != ""
	includeBodies := loggingEnabled && settings.IncludeBodies
	limit := settings.MaxBodyBytes
	if limit == 0 || limit < -1 {
		limit = defaultBodyLogLimit
	}
	if limit > maxBodyLogLimit {
		limit = maxBodyLogLimit
	}
	if !includeBodies {
		limit = 0
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		m := e.metricsFor(siteID)
		m.begin()
		var requestBody *bodyCapture
		var requestBytes atomic.Uint64
		if includeBodies {
			requestBody = newBodyCapture(limit)
			if r.Body != nil {
				r.Body = &teeReadCloser{ReadCloser: r.Body, tee: requestBody}
			}
		} else if r.Body != nil {
			r.Body = &countingReadCloser{ReadCloser: r.Body, bytes: &requestBytes}
		}
		var responseBody *bodyCapture
		if includeBodies {
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
		route := &routeTrace{}
		ctx := httptrace.WithClientTrace(context.WithValue(r.Context(), routeTraceKey{}, route), trace)
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
		m.finish(rw.status, requestBytesCount, rw.bytes, ttfb, elapsed, gotTTFB)
		if !loggingEnabled {
			return
		}
		requestHeaders := loggedHeaders(r.Header, settings.IncludeSensitiveHeaders)
		responseHeaders := loggedHeaders(rw.Header(), settings.IncludeSensitiveHeaders)
		record := requestRecord(r, siteID, route.upstream, started)
		record.Route = route.route
		record.RequestHeaders, record.ResponseHeaders = requestHeaders, responseHeaders
		record.Status, record.RequestBytes, record.ResponseBytes = rw.status, requestBytesCount, rw.bytes
		record.TTFBMillis, record.ResponseMillis = float64(ttfb)/float64(time.Millisecond), float64(elapsed)/float64(time.Millisecond)
		eventBytes := int64(2048 + headerBytes(requestHeaders) + headerBytes(responseHeaders))
		if includeBodies {
			eventBytes += requestBody.storedBytes() + responseBody.storedBytes()
		}
		if !e.logs.reserve(eventBytes) {
			m.dropLog()
			return
		}
		event := accessLogEvent{record: record, adapterID: settings.AdapterID, requestBody: requestBody, responseBody: responseBody, includeBodies: includeBodies, bytes: eventBytes}
		if !e.logs.enqueue(event) {
			m.dropLog()
		}
	})
}

// requestRecord fills the client, connection, and request-line fields of a standard (combined-format) access log.
// ClientIP is the TCP peer; X-Forwarded-For is kept verbatim because it is client-controlled unless a trusted proxy sets it.
func requestRecord(r *http.Request, siteID, upstream string, started time.Time) accesslog.Record {
	clientIP, clientPort := splitRemoteAddr(r.RemoteAddr)
	scheme, tlsVersion := "http", ""
	if r.TLS != nil {
		scheme, tlsVersion = "https", tls.VersionName(r.TLS.Version)
	}
	return accesslog.Record{
		Timestamp: started.UTC(), SiteID: siteID,
		ClientIP: clientIP, ClientPort: clientPort, ForwardedFor: strings.Join(r.Header.Values("X-Forwarded-For"), ", "),
		Scheme: scheme, TLSVersion: tlsVersion, Host: r.Host,
		Method: r.Method, Path: r.URL.RequestURI(), Protocol: r.Proto,
		Referer: r.Referer(), UserAgent: r.UserAgent(), Upstream: upstream,
	}
}

func splitRemoteAddr(addr string) (string, int) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		return host, 0
	}
	return host, portNumber
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
func capturedBody(b *bodyCapture) (string, string, int64, bool) {
	if b == nil {
		return "", "", 0, false
	}
	data, total, truncated := b.snapshot()
	encoding := "utf-8"
	value := string(data)
	if !utf8.Valid(data) {
		encoding = "base64"
		value = base64.StdEncoding.EncodeToString(data)
	}
	return value, encoding, total, truncated
}

func accessLogFields(record accesslog.Record) []zap.Field {
	fields := []zap.Field{
		zap.String("site_id", record.SiteID), zap.String("client_ip", record.ClientIP), zap.Int("client_port", record.ClientPort), zap.String("forwarded_for", record.ForwardedFor),
		zap.String("scheme", record.Scheme), zap.String("tls_version", record.TLSVersion), zap.String("host", record.Host),
		zap.String("method", record.Method), zap.String("path", record.Path), zap.String("protocol", record.Protocol),
		zap.String("referer", record.Referer), zap.String("user_agent", record.UserAgent), zap.String("upstream", record.Upstream), zap.String("route", record.Route),
		zap.Any("request_headers", record.RequestHeaders), zap.Int("status", record.Status), zap.Any("response_headers", record.ResponseHeaders),
		zap.Int64("request_body_bytes", int64(record.RequestBytes)), zap.Uint64("response_body_bytes", record.ResponseBytes),
		zap.Duration("ttfb", time.Duration(record.TTFBMillis*float64(time.Millisecond))), zap.Duration("response_time", time.Duration(record.ResponseMillis*float64(time.Millisecond))),
	}
	if record.RequestBodyEncoding != "" || record.ResponseBodyEncoding != "" {
		fields = append(fields,
			zap.String("request_body", record.RequestBody), zap.String("request_body_encoding", record.RequestBodyEncoding), zap.Int64("request_body_total_bytes", record.RequestBodyTotalBytes), zap.Bool("request_body_truncated", record.RequestBodyTruncated),
			zap.String("response_body", record.ResponseBody), zap.String("response_body_encoding", record.ResponseBodyEncoding), zap.Int64("response_body_total_bytes", record.ResponseBodyTotalBytes), zap.Bool("response_body_truncated", record.ResponseBodyTruncated),
		)
	}
	return fields
}

var _ http.Flusher = (*observedResponseWriter)(nil)
var _ http.Hijacker = (*observedResponseWriter)(nil)
var _ http.Pusher = (*observedResponseWriter)(nil)
