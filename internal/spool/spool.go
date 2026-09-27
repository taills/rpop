// Package spool is a node's local durable buffer for access log records and tunnel lifecycle events awaiting
// upload to the controller (D23). dataplane and overlay each drive the spool from their own single
// log-writing goroutine, so WriteAccessLog and RecordTunnelEvent only ever hand the record to a bounded
// in-memory queue and return; a second, dedicated goroutine serializes queued records into gzip NDJSON segment
// files, so a slow disk stalls neither of those callers, let alone request forwarding (P8). See
// docs/architecture/control-data-plane.md §5 ("阶段 5 第 2 步") for the on-disk layout, crash recovery, and
// quota rules this file and segment.go implement.
package spool

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/accesslog"
	"github.com/rpop-project/rpop/internal/overlay"
	"github.com/rpop-project/rpop/internal/southbound"
)

// Defaults for Config, matching docs/architecture/control-data-plane.md §5.
const (
	// DefaultQuotaBytes is the spool's default disk quota (D25, RPOP_LOG_SPOOL_QUOTA_BYTES).
	DefaultQuotaBytes int64 = 2 << 30
	// DefaultMaxSegmentBytes closes a segment once its uncompressed NDJSON content reaches this size.
	DefaultMaxSegmentBytes int64 = 8 << 20
	// DefaultMaxSegmentAge closes a segment once it has been open this long, even under DefaultMaxSegmentBytes.
	DefaultMaxSegmentAge = 30 * time.Second
	// DefaultMaxPersistInterval bounds how long a written-but-unflushed record can sit in the open segment
	// before the write goroutine forces a gzip Flush + fsync on its own, even without a full batch (see
	// maxBatchRecords/maxBatchBytes): a quiet node still gets its buffered records durable within one interval,
	// rather than waiting on a batch that a trickle of traffic may never fill. 1s keeps that worst-case
	// exposure small while still amortizing fsync cost by orders of magnitude once traffic is high enough to
	// hit the batch limits first.
	DefaultMaxPersistInterval = time.Second

	// queueLength is the ingest queue's capacity; it mirrors dataplane.accessLogQueueLength and
	// overlay.tunnelEventQueueLength, the two bounded queues that feed a Spool.
	queueLength = 256
	// maxBatchRecords bounds how many records the write goroutine appends to the open segment between two
	// persist (Flush+fsync) calls. It is set well above queueLength: a single "drain whatever's already
	// queued" pass can never collect more than queueLength records anyway, but a sustained burst keeps
	// refilling the channel while drainBatch is still draining it, so without an independent cap a busy node
	// could go arbitrarily long between fsyncs (unbounded loss on crash, and an ever-growing gzip buffer).
	// 4096 records is small enough to keep that worst case tight while still cutting the fsync rate by roughly
	// three orders of magnitude relative to one fsync per line under sustained load.
	maxBatchRecords = 4096
	// maxBatchBytes bounds the same batch by uncompressed NDJSON bytes instead of record count, so a handful of
	// very large records (access logs with captured bodies can run up to ~22MiB each, see MaxLogSegmentBytes's
	// doc comment) can't hold a persist back far past MaxPersistInterval just because they are individually
	// small in number. Half of DefaultMaxSegmentBytes keeps one batch from ever dominating memory even when a
	// deployment configures larger segments.
	maxBatchBytes = DefaultMaxSegmentBytes / 2
	// quotaWarnInterval rate-limits the "over quota" warning log so a sustained backlog logs once per interval
	// instead of once per evicted segment.
	quotaWarnInterval = 10 * time.Second
)

// Config configures a node's local log spool.
type Config struct {
	// Dir is the spool directory (segment files and state.json live directly under it), typically
	// <log-dir>/spool.
	Dir string
	// QuotaBytes bounds how many bytes of segments the spool keeps on disk; 0 uses DefaultQuotaBytes. Once a
	// newly closed segment pushes the total over this, the oldest segments are deleted first (D25).
	QuotaBytes int64
	// MaxSegmentBytes closes the current segment once its uncompressed NDJSON content reaches this size; 0 uses
	// DefaultMaxSegmentBytes.
	MaxSegmentBytes int64
	// MaxSegmentAge closes the current segment once it has been open this long, even under MaxSegmentBytes, so
	// a quiet node still uploads its buffered logs promptly; 0 uses DefaultMaxSegmentAge.
	MaxSegmentAge time.Duration
	// MaxPersistInterval bounds how long a written record can sit unflushed in the open segment before the
	// write goroutine fsyncs it on its own, even under maxBatchRecords/maxBatchBytes; 0 uses
	// DefaultMaxPersistInterval.
	MaxPersistInterval time.Duration
	// Log receives diagnostics; a nil Log is replaced with zap.NewNop().
	Log *zap.Logger
}

func (c Config) withDefaults() Config {
	if c.QuotaBytes <= 0 {
		c.QuotaBytes = DefaultQuotaBytes
	}
	if c.MaxSegmentBytes <= 0 {
		c.MaxSegmentBytes = DefaultMaxSegmentBytes
	}
	if c.MaxSegmentAge <= 0 {
		c.MaxSegmentAge = DefaultMaxSegmentAge
	}
	if c.MaxPersistInterval <= 0 {
		c.MaxPersistInterval = DefaultMaxPersistInterval
	}
	if c.Log == nil {
		c.Log = zap.NewNop()
	}
	return c
}

// segmentMeta describes one closed segment sitting on disk, pending upload.
type segmentMeta struct {
	seq   uint64
	bytes int64
}

// spoolEvent is one queued record, or a flush request (see Flush).
type spoolEvent struct {
	kind      string // southbound.LogKindAccess or southbound.LogKindTunnel
	adapterID string // only meaningful for LogKindAccess; see southbound.LogEnvelope
	record    any    // accesslog.Record or overlay.TunnelEvent
	flush     chan struct{}
}

// Spool implements dataplane.AccessLogWriter and overlay.TunnelEventSink by durably queuing every record to
// disk for later upload. Use NewSpool to create one.
type Spool struct {
	dir                string
	quotaBytes         int64
	maxSegmentBytes    int64
	maxSegmentAge      time.Duration
	maxPersistInterval time.Duration
	log                *zap.Logger
	clock              clock

	events chan spoolEvent

	droppedAccess atomic.Uint64
	droppedTunnel atomic.Uint64

	mu                   sync.Mutex
	nextSegment          uint64
	ackedUpTo            uint64
	open                 *segmentWriter
	onDisk               []segmentMeta // ascending by seq; closed segments present on disk, pending upload.
	quotaDroppedSegments uint64
	quotaDroppedBytes    uint64
	lastQuotaWarn        time.Time

	changed  chan struct{} // capacity 1: signals the uploader that a segment closed or an ack advanced state.
	stop     chan struct{}
	stopOnce sync.Once
	stopped  chan struct{}
}

// NewSpool opens (or creates) the spool directory at cfg.Dir, recovers from any crash left behind by a previous
// run, and starts its background write goroutine.
func NewSpool(cfg Config) (*Spool, error) {
	return newSpool(cfg, realClock{})
}

func newSpool(cfg Config, clk clock) (*Spool, error) {
	cfg = cfg.withDefaults()
	if cfg.Dir == "" {
		return nil, errors.New("spool directory is required")
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create spool directory: %w", err)
	}
	state, err := loadState(cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("load spool state: %w", err)
	}
	s := &Spool{
		dir: cfg.Dir, quotaBytes: cfg.QuotaBytes, maxSegmentBytes: cfg.MaxSegmentBytes, maxSegmentAge: cfg.MaxSegmentAge,
		maxPersistInterval: cfg.MaxPersistInterval,
		log:                cfg.Log, clock: clk,
		events:      make(chan spoolEvent, queueLength),
		nextSegment: state.NextSegment, ackedUpTo: state.AckedUpTo,
		changed: make(chan struct{}, 1), stop: make(chan struct{}), stopped: make(chan struct{}),
	}
	if err := s.recoverAndScan(); err != nil {
		return nil, fmt.Errorf("recover spool: %w", err)
	}
	go s.loop()
	return s, nil
}

// recoverAndScan runs once at startup. Exactly one on-disk segment number can be ambiguous after a crash: the
// one most recently allocated (nextSegment-1), since state.json is written before that segment's file is even
// created (see openNewSegmentLocked). Every other segment file is either already fully closed, or already
// acknowledged and only waiting to be deleted.
func (s *Spool) recoverAndScan() error {
	segs, err := listSegmentFiles(s.dir)
	if err != nil {
		return err
	}
	ambiguous := s.nextSegment - 1
	for _, seq := range segs {
		path := segmentPath(s.dir, seq)
		if seq == ambiguous {
			kept, size, err := recoverSegment(path)
			if err != nil {
				return err
			}
			if !kept {
				s.log.Warn("dropped an incomplete spool segment left by a crash", zap.Uint64("segment", seq))
				continue
			}
			if seq <= s.ackedUpTo {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				continue
			}
			s.onDisk = append(s.onDisk, segmentMeta{seq: seq, bytes: size})
			continue
		}
		if seq <= s.ackedUpTo {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		s.onDisk = append(s.onDisk, segmentMeta{seq: seq, bytes: info.Size()})
	}
	slices.SortFunc(s.onDisk, func(a, b segmentMeta) int { return cmp.Compare(a.seq, b.seq) })
	return nil
}

// WriteAccessLog implements dataplane.AccessLogWriter: it queues record for the spool's write goroutine and
// returns immediately. A non-nil error means the spool's own ingest queue was full and the record was dropped;
// dataplane counts that against the site the same way it would count a failing writer.
func (s *Spool) WriteAccessLog(_ context.Context, adapterID string, record accesslog.Record) error {
	if s.enqueue(spoolEvent{kind: southbound.LogKindAccess, adapterID: adapterID, record: record}) {
		return nil
	}
	s.droppedAccess.Add(1)
	return fmt.Errorf("spool ingest queue is full")
}

// RecordTunnelEvent implements overlay.TunnelEventSink. Unlike WriteAccessLog it has nothing to report a drop
// to; DroppedTunnelEvents (via Stats) is the only signal.
func (s *Spool) RecordTunnelEvent(event overlay.TunnelEvent) {
	if !s.enqueue(spoolEvent{kind: southbound.LogKindTunnel, record: event}) {
		s.droppedTunnel.Add(1)
	}
}

// enqueue hands e to the write goroutine without blocking; a full queue reports failure instead (P8).
func (s *Spool) enqueue(e spoolEvent) bool {
	select {
	case s.events <- e:
		return true
	default:
		return false
	}
}

// Flush waits until every record enqueued before the call is durably on disk and closes the current segment
// even if it is under the size/age threshold, so tests (and graceful shutdown, via Close) don't have to wait
// out MaxSegmentAge.
func (s *Spool) Flush(ctx context.Context) error {
	done := make(chan struct{})
	select {
	case s.events <- spoolEvent{flush: done}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes and stops the spool's write goroutine. Calls to WriteAccessLog/RecordTunnelEvent after Close
// keep working structurally but are dropped once the (now-unread) ingest queue fills.
func (s *Spool) Close(ctx context.Context) error {
	if err := s.Flush(ctx); err != nil {
		return err
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.stopped:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// loop is the spool's single write goroutine: every mutation of segment/state/quota bookkeeping happens either
// here or in Ack/Pending/Open, all under mu, so the concurrent ingest and upload sides never race.
func (s *Spool) loop() {
	defer close(s.stopped)
	for {
		s.mu.Lock()
		var ageTimer, persistTimer <-chan time.Time
		if s.open != nil {
			ageRemaining := s.maxSegmentAge - s.clock.Now().Sub(s.open.opened)
			if ageRemaining < 0 {
				ageRemaining = 0
			}
			ageTimer = s.clock.After(ageRemaining)
			// Only arm the persist timer while there is actually something unflushed to persist; a segment
			// that just had a batch persisted (or was just opened) has nothing to gain from waking the loop
			// again before the next record arrives.
			if s.open.unflushedRecords > 0 {
				persistRemaining := s.maxPersistInterval - s.clock.Now().Sub(s.open.lastPersist)
				if persistRemaining < 0 {
					persistRemaining = 0
				}
				persistTimer = s.clock.After(persistRemaining)
			}
		}
		s.mu.Unlock()
		select {
		case e := <-s.events:
			s.drainBatch(e)
		case <-persistTimer:
			s.persistOpen()
		case <-ageTimer:
			s.mu.Lock()
			if s.open != nil && s.clock.Now().Sub(s.open.opened) >= s.maxSegmentAge {
				s.closeOpenLocked()
			}
			s.mu.Unlock()
		case <-s.stop:
			s.mu.Lock()
			s.closeOpenLocked()
			s.mu.Unlock()
			return
		}
	}
}

// drainBatch handles first (already received from s.events) and then, without blocking, keeps pulling and
// writing whatever records are already sitting in the channel - "取出队列中已到达的全部记录" - so a burst that
// arrived while the loop was busy elsewhere is written together instead of one persist per record. It persists
// (Flush+fsync) partway through whenever the batch reaches maxBatchRecords or maxBatchBytes, and again if a
// segment happens to close along the way (closing already fsyncs as part of finalizing the segment). It does
// *not* persist just because the channel ran dry: an idle moment with a small, still-unflushed batch is exactly
// what MaxPersistInterval (handled by the caller's timer, not here) is for, so a slow trickle of records isn't
// forced through an fsync for every single one of them either.
func (s *Spool) drainBatch(first spoolEvent) {
	e := first
	var batchRecords int
	var batchBytes int64
	for {
		if e.flush != nil {
			s.mu.Lock()
			s.closeOpenLocked() // Flush's contract: force-close the open segment now, which also fsyncs it.
			s.mu.Unlock()
			close(e.flush)
			return
		}
		n, segmentClosed := s.writeRecord(e)
		if segmentClosed {
			// The old segment's data is already durable via close(); the new segment (if writeRecord opens
			// one for a later record in this batch) starts its own fresh, unpersisted batch.
			batchRecords, batchBytes = 0, 0
		} else {
			batchRecords++
			batchBytes += n
			if batchRecords >= maxBatchRecords || batchBytes >= maxBatchBytes {
				s.persistOpen()
				batchRecords, batchBytes = 0, 0
			}
		}
		select {
		case next := <-s.events:
			e = next
		default:
			return
		}
	}
}

// persistOpen flushes and fsyncs the currently open segment, if any, without closing it, unless nothing has
// been written to it since the last persist. The gz.Flush/file.Sync I/O itself happens without mu held (a slow
// disk must never block Pending/Stats/Ack); only reading the s.open pointer beforehand and updating the
// segment's unflushedRecords/lastPersist bookkeeping afterwards need it (see the segmentWriter doc comment).
func (s *Spool) persistOpen() {
	s.mu.Lock()
	open := s.open
	hasUnflushed := open != nil && open.unflushedRecords > 0
	s.mu.Unlock()
	if !hasUnflushed {
		return
	}
	if err := open.persist(); err != nil {
		s.log.Error("persist spool segment", zap.Uint64("segment", open.seq), zap.Error(err))
		return
	}
	s.mu.Lock()
	open.unflushedRecords = 0
	open.lastPersist = s.clock.Now()
	s.mu.Unlock()
}

// writeRecord serializes one queued record as a southbound.LogEnvelope line, opening a segment first if none is
// open, then closes the segment if that line reached MaxSegmentBytes (reporting that via segmentClosed so
// drainBatch can reset its own batch counters - the just-closed segment is already durable, and the next
// segment, if any, starts a fresh batch). Checking the size after writing (rather than before) means a single
// oversized record - an access log with large captured bodies, for instance - still lands whole in one segment
// instead of splitting a record across two; see MaxLogSegmentBytes's doc comment for the resulting worst case.
// bytesWritten is 0 and segmentClosed is false on any marshal or write error; the record is dropped and logged,
// matching the pre-batching behavior.
func (s *Spool) writeRecord(e spoolEvent) (bytesWritten int64, segmentClosed bool) {
	raw, err := json.Marshal(e.record)
	if err != nil {
		s.log.Error("marshal log record for spool", zap.String("kind", e.kind), zap.Error(err))
		return 0, false
	}
	line, err := json.Marshal(southbound.LogEnvelope{Kind: e.kind, AdapterID: e.adapterID, Record: raw})
	if err != nil {
		s.log.Error("marshal log envelope for spool", zap.String("kind", e.kind), zap.Error(err))
		return 0, false
	}
	line = append(line, '\n')

	s.mu.Lock()
	if s.open == nil {
		if err := s.openNewSegmentLocked(); err != nil {
			s.mu.Unlock()
			s.log.Error("open spool segment", zap.Error(err))
			return 0, false
		}
	}
	open := s.open
	s.mu.Unlock()

	// The write itself (gz.Write, possibly pushing compressed bytes to the OS) happens without mu held; only
	// the resulting bookkeeping does, matching persistOpen's split between I/O and state.
	if err := open.write(line); err != nil {
		s.log.Error("write spool segment", zap.Uint64("segment", open.seq), zap.Error(err))
		return 0, false
	}

	s.mu.Lock()
	open.rawBytes += int64(len(line))
	open.unflushedRecords++
	closed := open.rawBytes >= s.maxSegmentBytes
	if closed {
		s.closeOpenLocked()
	}
	s.mu.Unlock()
	return int64(len(line)), closed
}

// openNewSegmentLocked allocates the next segment number and persists it before ever creating the segment's
// file, so that number is retired the instant it is committed to disk: if creating the file itself then fails,
// the number is *not* reused (a segment number identifies one immutable set of bytes for good), but if
// persisting the state never even succeeded, nothing was committed and the in-memory counter is rolled back.
func (s *Spool) openNewSegmentLocked() error {
	seq := s.nextSegment
	s.nextSegment++
	if err := s.saveStateLocked(); err != nil {
		s.nextSegment = seq
		return err
	}
	writer, err := createSegmentWriter(s.dir, seq, s.clock.Now())
	if err != nil {
		return err
	}
	s.open = writer
	return nil
}

// closeOpenLocked finalizes the open segment (if any), records it as pending upload, and enforces the disk
// quota.
func (s *Spool) closeOpenLocked() {
	open := s.open
	if open == nil {
		return
	}
	s.open = nil
	if err := open.close(); err != nil {
		s.log.Error("close spool segment", zap.Uint64("segment", open.seq), zap.Error(err))
		_ = os.Remove(segmentPath(s.dir, open.seq))
		return
	}
	info, err := os.Stat(segmentPath(s.dir, open.seq))
	var size int64
	if err == nil {
		size = info.Size()
	}
	s.onDisk = append(s.onDisk, segmentMeta{seq: open.seq, bytes: size})
	s.enforceQuotaLocked()
	s.signalChanged()
}

// enforceQuotaLocked deletes the oldest on-disk segments, one at a time, until the total is back under quota
// (D25); new events are never held up for this, only the disk they eventually land on.
func (s *Spool) enforceQuotaLocked() {
	var total int64
	for _, m := range s.onDisk {
		total += m.bytes
	}
	for total > s.quotaBytes && len(s.onDisk) > 0 {
		victim := s.onDisk[0]
		s.onDisk = s.onDisk[1:]
		total -= victim.bytes
		if err := os.Remove(segmentPath(s.dir, victim.seq)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			s.log.Error("remove spool segment over quota", zap.Uint64("segment", victim.seq), zap.Error(err))
		}
		s.quotaDroppedSegments++
		s.quotaDroppedBytes += uint64(victim.bytes)
		s.warnQuotaLocked(victim.seq)
	}
}

func (s *Spool) warnQuotaLocked(seq uint64) {
	now := s.clock.Now()
	if !s.lastQuotaWarn.IsZero() && now.Sub(s.lastQuotaWarn) < quotaWarnInterval {
		return
	}
	s.lastQuotaWarn = now
	s.log.Warn("spool over quota; dropped its oldest unacknowledged segment",
		zap.Uint64("segment", seq), zap.Int64("quota_bytes", s.quotaBytes), zap.Uint64("dropped_segments_total", s.quotaDroppedSegments))
}

func (s *Spool) signalChanged() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func (s *Spool) saveStateLocked() error {
	return saveState(s.dir, spoolState{NextSegment: s.nextSegment, AckedUpTo: s.ackedUpTo})
}

// SegmentInfo describes one segment on disk, for the uploader.
type SegmentInfo struct {
	Seq   uint64
	Bytes int64
}

// Pending returns the segments currently on disk, ascending by segment number, whether or not they have been
// acknowledged yet (a segment already covered by AckedUpTo can briefly still be here; see Ack).
func (s *Spool) Pending() []SegmentInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SegmentInfo, len(s.onDisk))
	for i, m := range s.onDisk {
		out[i] = SegmentInfo{Seq: m.seq, Bytes: m.bytes}
	}
	return out
}

// AckedUpTo returns the highest segment number this node has locally recorded as acknowledged by the
// controller.
func (s *Spool) AckedUpTo() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ackedUpTo
}

// Changed signals once (dropping further signals until the receiver catches up) whenever a segment closes or
// Ack advances state; the uploader waits on it instead of polling.
func (s *Spool) Changed() <-chan struct{} { return s.changed }

// Open opens a closed segment for reading: its raw gzip bytes, ready to POST to the controller as-is.
func (s *Spool) Open(seq uint64) (*os.File, int64, error) {
	file, err := os.Open(segmentPath(s.dir, seq))
	if err != nil {
		return nil, 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, err
	}
	return file, info.Size(), nil
}

// Ack records that the controller has durably applied every segment numbered at or below ack (D24): matching
// on-disk segments are deleted, and if this node has not even allocated a segment number beyond ack yet, the
// next one it creates skips straight past it. That second part matters after a node reinstalls with an empty
// spool but the controller still remembers a much higher high-water mark for its node ID (state.json gone,
// segment numbering restarts at 1, but the controller's HWM might be, say, 500): without skipping ahead, every
// new segment the reinstalled node produces would again come in at or under that old HWM, be treated as a
// duplicate retransmission, and be acknowledged without ever actually being written by the controller - forever,
// not just for the one segment lost at the moment of reinstall. Skipping nextSegment to ack+1 the first time
// such an ack is seen bounds the loss to that one moment instead.
func (s *Spool) Ack(ack uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ack > s.ackedUpTo {
		s.ackedUpTo = ack
	}
	if ack+1 > s.nextSegment {
		s.nextSegment = ack + 1
	}
	if err := s.saveStateLocked(); err != nil {
		return err
	}
	kept := make([]segmentMeta, 0, len(s.onDisk))
	for _, m := range s.onDisk {
		if m.seq <= ack {
			if err := os.Remove(segmentPath(s.dir, m.seq)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				s.log.Warn("remove acknowledged spool segment", zap.Uint64("segment", m.seq), zap.Error(err))
			}
			continue
		}
		kept = append(kept, m)
	}
	s.onDisk = kept
	s.signalChanged()
	return nil
}

// Stats is a snapshot of the spool's own health; Uploader.LastError() covers the upload side. agent.go combines
// both into southbound.LogStats.
type Stats struct {
	AccessLogQueueDropped   uint64
	TunnelEventQueueDropped uint64
	QuotaDroppedSegments    uint64
	QuotaDroppedBytes       uint64
	PendingSegments         uint64
	PendingBytes            uint64
	AckedSegment            uint64
}

func (s *Spool) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	var bytes int64
	for _, m := range s.onDisk {
		bytes += m.bytes
	}
	return Stats{
		AccessLogQueueDropped:   s.droppedAccess.Load(),
		TunnelEventQueueDropped: s.droppedTunnel.Load(),
		QuotaDroppedSegments:    s.quotaDroppedSegments,
		QuotaDroppedBytes:       s.quotaDroppedBytes,
		PendingSegments:         uint64(len(s.onDisk)),
		PendingBytes:            uint64(bytes),
		AckedSegment:            s.ackedUpTo,
	}
}
