package dataplane

import (
	"context"
	"sync/atomic"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
)

const (
	defaultBodyLogLimit     int64 = 1 << 20
	maxBodyLogLimit         int64 = 8 << 20
	maxQueuedAccessLogBytes int64 = 32 << 20
	accessLogQueueLength          = 256
)

// AccessLogWriter stores one access log record in the destination named by adapterID.
type AccessLogWriter interface {
	WriteAccessLog(ctx context.Context, adapterID string, record accesslog.Record) error
}

type accessLogEvent struct {
	record                    accesslog.Record
	adapterID                 string
	requestBody, responseBody *bodyCapture
	includeBodies             bool
	bytes                     int64
	barrier                   chan struct{}
}

type writerHolder struct{ writer AccessLogWriter }

// logQueue moves access logs off the request path: producers never block, and a full queue drops records.
type logQueue struct {
	events      chan accessLogEvent
	queuedBytes atomic.Int64
	writer      atomic.Pointer[writerHolder]
	log         *zap.Logger
	dropped     func(siteID string)
}

func newLogQueue(log *zap.Logger, dropped func(siteID string)) *logQueue {
	q := &logQueue{events: make(chan accessLogEvent, accessLogQueueLength), log: log, dropped: dropped}
	go q.loop()
	return q
}

func (q *logQueue) setWriter(writer AccessLogWriter) {
	q.writer.Store(&writerHolder{writer: writer})
}

// reserve admits size bytes; an empty queue always admits one event so a single oversized body still logs.
func (q *logQueue) reserve(size int64) bool {
	if size < 0 {
		return false
	}
	for {
		current := q.queuedBytes.Load()
		if current > 0 && current+size > maxQueuedAccessLogBytes {
			return false
		}
		if q.queuedBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

// enqueue hands a reserved event to the writer loop without blocking.
func (q *logQueue) enqueue(event accessLogEvent) bool {
	select {
	case q.events <- event:
		return true
	default:
		q.queuedBytes.Add(-event.bytes)
		return false
	}
}

func (q *logQueue) loop() {
	for event := range q.events {
		if event.barrier != nil {
			close(event.barrier)
			continue
		}
		record := event.record
		if event.includeBodies {
			record.RequestBody, record.RequestBodyEncoding, record.RequestBodyTotalBytes, record.RequestBodyTruncated = capturedBody(event.requestBody)
			record.ResponseBody, record.ResponseBodyEncoding, record.ResponseBodyTotalBytes, record.ResponseBodyTruncated = capturedBody(event.responseBody)
		}
		if holder := q.writer.Load(); holder != nil && holder.writer != nil {
			if err := holder.writer.WriteAccessLog(context.Background(), event.adapterID, record); err != nil {
				q.log.Error("write access log", zap.String("site_id", record.SiteID), zap.Error(err))
				q.dropped(record.SiteID)
			}
		} else {
			q.log.Info("site HTTP access", accessLogFields(record)...)
		}
		q.queuedBytes.Add(-event.bytes)
	}
}

// drain waits until every event queued before the call has been written.
func (q *logQueue) drain(ctx context.Context) error {
	barrier := make(chan struct{})
	select {
	case q.events <- accessLogEvent{barrier: barrier}:
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
