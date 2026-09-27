package spool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/rpop-project/rpop/internal/southbound"
)

const (
	// DefaultUploadRateBytesPerSecond throttles reads of segment files while uploading (D25,
	// RPOP_LOG_UPLOAD_RATE_BYTES); forwarding traffic is never rate-limited by this.
	DefaultUploadRateBytesPerSecond int64 = 4 << 20

	uploadMinBackoff     = time.Second
	uploadMaxBackoff     = 30 * time.Second
	uploadRequestTimeout = 30 * time.Second
)

// UploaderConfig configures the upload loop.
type UploaderConfig struct {
	// Endpoint is the controller's full logs URL, e.g. https://controller:7443/southbound/v1/logs.
	Endpoint string
	// Client returns the node's current southbound mTLS client (the same one watch/status use). It is a func
	// rather than a fixed value because the agent replaces its client wholesale on re-registration; Uploader
	// always dials with whichever one is current when it is about to send a request.
	Client func() *http.Client
	// RateBytesPerSecond throttles how fast segments are read and sent; 0 uses DefaultUploadRateBytesPerSecond.
	RateBytesPerSecond int64
	// Log receives diagnostics; a nil Log is replaced with zap.NewNop().
	Log *zap.Logger
}

// Uploader drains a Spool's pending segments to the controller strictly in ascending segment order, waiting for
// each segment's acknowledgement before sending the next (D24: the protocol is deliberately stop-and-wait,
// since uploads are already rate-limited and pipelining would not help throughput).
type Uploader struct {
	spool    *Spool
	endpoint string
	client   func() *http.Client
	log      *zap.Logger
	clock    clock
	limiter  *tokenBucket

	lastErr atomic.Pointer[string]
}

// NewUploader creates an Uploader for spool; call Run to start draining it.
func NewUploader(spool *Spool, cfg UploaderConfig) *Uploader {
	return newUploader(spool, cfg, realClock{})
}

func newUploader(spool *Spool, cfg UploaderConfig, clk clock) *Uploader {
	rate := cfg.RateBytesPerSecond
	if rate <= 0 {
		rate = DefaultUploadRateBytesPerSecond
	}
	log := cfg.Log
	if log == nil {
		log = zap.NewNop()
	}
	u := &Uploader{spool: spool, endpoint: cfg.Endpoint, client: cfg.Client, log: log, clock: clk, limiter: newTokenBucket(rate, clk)}
	empty := ""
	u.lastErr.Store(&empty)
	return u
}

// Run drains pending segments until ctx ends. A failed upload retries the same segment with exponential
// backoff (capped) rather than skipping ahead, since segments must be delivered in order (D24); Run returns as
// soon as ctx is done, whether idle, backing off, or mid-request.
func (u *Uploader) Run(ctx context.Context) {
	backoff := uploadMinBackoff
	for {
		seq, ok := u.next(ctx)
		if !ok {
			return
		}
		if err := u.uploadOne(ctx, seq); err != nil {
			if ctx.Err() != nil {
				return
			}
			u.setLastErr(err)
			u.log.Warn("log segment upload failed; retrying", zap.Uint64("segment", seq), zap.Error(err), zap.Duration("retry_in", backoff))
			if !sleepClock(ctx, u.clock, backoff) {
				return
			}
			backoff = min(backoff*2, uploadMaxBackoff)
			continue
		}
		backoff = uploadMinBackoff
	}
}

// next returns the smallest pending segment number still above AckedUpTo, blocking until one exists or ctx
// ends. Pending() never actually holds a segment at or below AckedUpTo for long (Ack deletes those immediately),
// so the filter below is a defensive no-op in the common case, not a substitute for calling Ack.
func (u *Uploader) next(ctx context.Context) (uint64, bool) {
	for {
		acked := u.spool.AckedUpTo()
		for _, seg := range u.spool.Pending() {
			if seg.Seq > acked {
				return seg.Seq, true
			}
		}
		select {
		case <-u.spool.Changed():
		case <-ctx.Done():
			return 0, false
		}
	}
}

// uploadOne sends segment seq's raw (already gzip-compressed) bytes as-is and processes the controller's
// acknowledgement.
func (u *Uploader) uploadOne(ctx context.Context, seq uint64) error {
	file, size, err := u.spool.Open(seq)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Gone already: quota eviction, or an Ack for a later segment that covered this one too.
			return nil
		}
		return fmt.Errorf("open segment %d: %w", seq, err)
	}
	defer file.Close()
	if err := u.limiter.wait(ctx, size); err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, uploadRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, u.endpoint, file)
	if err != nil {
		return err
	}
	request.Header.Set(southbound.LogSegmentHeader, strconv.FormatUint(seq, 10))
	request.Header.Set(southbound.ProtocolVersionHeader, strconv.Itoa(southbound.ProtocolVersion))
	request.Header.Set("Content-Encoding", "gzip")
	request.Header.Set("Content-Type", "application/x-ndjson")
	request.ContentLength = size
	response, err := u.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := checkResponse(response); err != nil {
		return err
	}
	var ack southbound.LogAck
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&ack); err != nil {
		return fmt.Errorf("decode log ack: %w", err)
	}
	// A 2xx response for seq always confirms at least seq itself, whatever the controller's own HWM says (see
	// Spool.Ack's doc comment on why the controller's value can be even higher, and never lower, in practice).
	advance := ack.Ack
	if seq > advance {
		advance = seq
	}
	if err := u.spool.Ack(advance); err != nil {
		return fmt.Errorf("persist log ack: %w", err)
	}
	u.clearLastErr()
	return nil
}

func checkResponse(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	return fmt.Errorf("controller answered %d: %s", response.StatusCode, bytes.TrimSpace(body))
}

func (u *Uploader) setLastErr(err error) {
	msg := err.Error()
	u.lastErr.Store(&msg)
}

func (u *Uploader) clearLastErr() {
	empty := ""
	u.lastErr.Store(&empty)
}

// LastError is the most recent upload failure, empty once an upload has succeeded since.
func (u *Uploader) LastError() string {
	if p := u.lastErr.Load(); p != nil {
		return *p
	}
	return ""
}
