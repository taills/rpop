package traceid

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewMatchesUUIDv7Format(t *testing.T) {
	id := New()
	if !uuidPattern.MatchString(id) {
		t.Fatalf("New() = %q, does not look like a UUIDv7", id)
	}
}

func TestNewIsMonotonicUnderSustainedLoad(t *testing.T) {
	const count = 20000
	previous := ""
	for i := 0; i < count; i++ {
		id := New()
		if !uuidPattern.MatchString(id) {
			t.Fatalf("iteration %d: %q does not look like a UUIDv7", i, id)
		}
		if previous != "" && id <= previous {
			t.Fatalf("iteration %d: %q did not sort after %q", i, id, previous)
		}
		previous = id
	}
}

func TestTimeExtractsTheEncodedMillisecond(t *testing.T) {
	// New truncates the wall clock to millisecond precision before encoding it, so bracket the id's minting
	// between two truncated reads rather than asserting exact equality with a third, later time.Now() call.
	before := time.Now().UTC().Truncate(time.Millisecond)
	id := New()
	after := time.Now().UTC()

	got, ok := Time(id)
	if !ok {
		t.Fatalf("Time(%q) ok = false, want true", id)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("Time(%q) = %v, want within [%v, %v]", id, got, before, after)
	}
}

// TestTimeMatchesTheDocumentedByteLayout builds the raw bytes by hand, independently of New's own bit-packing,
// so a mirrored bug in encode and decode (e.g. both shifting the wrong way) could not cancel out and still pass.
func TestTimeMatchesTheDocumentedByteLayout(t *testing.T) {
	const msConst = 1735689600123 // 2025-01-01T00:00:00.123Z; the exact value is arbitrary, just easy to eyeball
	ms := int64(msConst)          // shifted at runtime below, so byte() truncates instead of a compile-time overflow
	var b [16]byte
	b[0], b[1], b[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	b[3], b[4], b[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	b[6], b[8] = 0x70, 0x80 // version 7, variant 10; the counter/random tail is irrelevant to Time
	id := format(b)

	got, ok := Time(id)
	if !ok {
		t.Fatalf("Time(%q) ok = false, want true", id)
	}
	if want := time.UnixMilli(ms).UTC(); !got.Equal(want) {
		t.Fatalf("Time(%q) = %v, want %v", id, got, want)
	}
}

func TestTimeRejectsIDsItCannotTrust(t *testing.T) {
	v7 := func(mutate func(b *[16]byte)) string {
		var b [16]byte
		b[6], b[8] = 0x70, 0x80
		if mutate != nil {
			mutate(&b)
		}
		return format(b)
	}
	tests := []struct {
		name string
		id   string
	}{
		{"empty string", ""},
		{"too short", "1234"},
		{"missing dashes", strings.ReplaceAll(v7(nil), "-", "")},
		{"non-hex characters", "zzzzzzzz-zzzz-7zzz-8zzz-zzzzzzzzzzzz"},
		{"version 4, not 7", v7(func(b *[16]byte) { b[6] = 0x40 })},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := Time(tc.id); ok {
				t.Fatalf("Time(%q) = %v, true; want ok = false", tc.id, got)
			}
		})
	}
}

// TestValidAcceptsAnyWellFormedUUIDRegardlessOfVersion covers stage 5 security review item 4: Valid checks only
// the shape New's own IDs have, not the version 7 marker Time additionally requires, so an ID from an untrusted
// source (a node's tunnel event, a relay header) that this package never minted is still accepted as a
// well-formed identifier.
func TestValidAcceptsAnyWellFormedUUIDRegardlessOfVersion(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"a freshly minted id", New(), true},
		{"a random version 4 UUID", "3fa85f64-5717-4562-b3fc-2c963f66afa6", true},
		{"empty string", "", false},
		{"too short", "1234", false},
		{"missing dashes", strings.ReplaceAll(New(), "-", ""), false},
		{"non-hex characters", "zzzzzzzz-zzzz-7zzz-8zzz-zzzzzzzzzzzz", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.id); got != tc.want {
				t.Fatalf("Valid(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func TestNewNeverRepeatsUnderConcurrentUse(t *testing.T) {
	const goroutines, perGoroutine = 50, 500
	ids := make(chan string, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				ids <- New()
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]bool, goroutines*perGoroutine)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
