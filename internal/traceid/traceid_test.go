package traceid

import (
	"regexp"
	"sync"
	"testing"
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
