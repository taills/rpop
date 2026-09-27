// Package traceid generates the identifiers the data plane attaches to requests and tunnels so their path
// across nodes can be reconstructed later: Rpop-Track-Id per request and the tunnel ID per cross-node tunnel
// (see docs/architecture/control-data-plane.md, D22). Generation sits on the request hot path (P8), so it uses
// math/rand/v2, a non-cryptographic, syscall-free random source; IDs only need to avoid collisions, not resist
// prediction.
package traceid

import (
	"encoding/hex"
	"math/rand/v2"
	"sync"
	"time"
)

// counterBits is the width of the monotonic counter packed into a UUIDv7's rand_a field (RFC 9562 §6.2 method
// 3): 12 bits let 4096 IDs be generated within the same millisecond while still sorting in order.
const counterBits = 12
const counterMask = 1<<counterBits - 1

var state struct {
	mu      sync.Mutex
	lastMS  int64
	counter uint32
}

// New returns a version 7 UUID: a 48-bit Unix millisecond timestamp followed by a monotonic counter and random
// bits, so IDs sort in generation order even when many are minted within the same millisecond.
func New() string {
	ms, counter := nextTimestampAndCounter()
	random := rand.Uint64()

	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = 0x70 | byte(counter>>8) // version 7, top 4 bits of the 12-bit counter
	b[7] = byte(counter)
	b[8] = 0x80 | byte(random>>58) // variant 10, top 6 bits of the 62-bit random tail
	b[9] = byte(random >> 50)
	b[10] = byte(random >> 42)
	b[11] = byte(random >> 34)
	b[12] = byte(random >> 26)
	b[13] = byte(random >> 18)
	b[14] = byte(random >> 10)
	b[15] = byte(random >> 2)
	return format(b)
}

// nextTimestampAndCounter advances the shared monotonic state. Within one millisecond it increments the
// counter; when the counter would wrap, it borrows the next millisecond instead of repeating a value.
func nextTimestampAndCounter() (int64, uint32) {
	state.mu.Lock()
	defer state.mu.Unlock()
	ms := time.Now().UnixMilli()
	switch {
	case ms > state.lastMS:
		state.lastMS = ms
		state.counter = uint32(rand.Uint32()) & counterMask
	default:
		state.counter = (state.counter + 1) & counterMask
		if state.counter == 0 {
			// Exhausted this millisecond's counter space faster than the clock advanced; keep IDs increasing by
			// borrowing the next one.
			state.lastMS++
		}
		ms = state.lastMS
	}
	return ms, state.counter
}

func format(b [16]byte) string {
	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}

// Time extracts the millisecond timestamp a UUIDv7 minted by New encodes in its leading 48 bits (RFC 9562 §5.7),
// truncated to millisecond precision like the ID itself. It reports ok=false for anything that is not shaped
// like a UUID (wrong length, missing dashes, non-hex digits) or does not carry New's version 7 marker, so
// callers with an ID from an untrusted or unknown source (a path parameter someone typed by hand, a future
// generator that stops using UUIDv7) can fall back to treating it as opaque instead of deriving a meaningless
// timestamp from bytes New never put one in.
func Time(id string) (time.Time, bool) {
	b, ok := decode(id)
	if !ok || b[6]&0xf0 != 0x70 {
		return time.Time{}, false
	}
	ms := int64(b[0])<<40 | int64(b[1])<<32 | int64(b[2])<<24 | int64(b[3])<<16 | int64(b[4])<<8 | int64(b[5])
	return time.UnixMilli(ms).UTC(), true
}

// decode parses a canonical 8-4-4-4-12 hex UUID string (the shape format produces) into its 16 raw bytes.
func decode(id string) ([16]byte, bool) {
	var b [16]byte
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return b, false
	}
	var packed [32]byte
	copy(packed[0:8], id[0:8])
	copy(packed[8:12], id[9:13])
	copy(packed[12:16], id[14:18])
	copy(packed[16:20], id[19:23])
	copy(packed[20:32], id[24:36])
	if _, err := hex.Decode(b[:], packed[:]); err != nil {
		return [16]byte{}, false
	}
	return b, true
}
