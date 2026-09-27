package control

import (
	"sync"
	"testing"
	"time"
)

// TestKeyedMutexSerializesTheSameKeyButNotDifferentOnes covers keyedMutex's basic contract (D24): two callers
// for the same key never run concurrently, but two callers for different keys are never blocked by each other.
func TestKeyedMutexSerializesTheSameKeyButNotDifferentOnes(t *testing.T) {
	k := newKeyedMutex()

	unlockA := k.lock("a")
	unlocked := make(chan struct{})
	go func() {
		unlock := k.lock("a")
		defer unlock()
		close(unlocked)
	}()
	select {
	case <-unlocked:
		t.Fatal("a second lock(\"a\") must block while the first caller still holds it")
	case <-time.After(20 * time.Millisecond):
	}
	unlockA()
	select {
	case <-unlocked:
	case <-time.After(time.Second):
		t.Fatal("expected the second lock(\"a\") to proceed once the first was released")
	}

	unlockB := k.lock("b") // a different key must never block on "a"'s lock
	unlockB()
}

// TestKeyedMutexDropsAKeysEntryOnceItIsUnused covers stage 5 low-priority finding item 3's cleanup goal: a
// key's entry is removed as soon as its last caller releases it, without ever calling a separate forget, so a
// long-lived controller does not keep one mutex per key that ever existed.
func TestKeyedMutexDropsAKeysEntryOnceItIsUnused(t *testing.T) {
	k := newKeyedMutex()
	unlock := k.lock("edge-1")
	k.mu.Lock()
	_, present := k.locks["edge-1"]
	k.mu.Unlock()
	if !present {
		t.Fatal("expected an entry for \"edge-1\" while its lock is held")
	}
	unlock()
	k.mu.Lock()
	_, present = k.locks["edge-1"]
	k.mu.Unlock()
	if present {
		t.Fatal("expected the entry for \"edge-1\" to be gone once its last caller released it")
	}
}

// TestKeyedMutexNeverGrantsTwoDifferentLocksForOneKeyAtOnce is a regression test for item 3: replacing the
// entry with a fresh, independent *sync.Mutex the instant it looks unused — the bug an explicit forget(key)
// racing lock(key) for the same, just-deleted-and-recreated key used to risk — would let two callers that both
// believe they serialize on the same key run their critical sections concurrently. Many goroutines hammering a
// handful of keys, each mutating a per-key counter (a distinct counters slot per key, so different keys never
// touch the same memory and only a same-key race could ever corrupt one) without its own synchronization, would
// show that as either a wrong final count or a failure under -race; both must never happen.
func TestKeyedMutexNeverGrantsTwoDifferentLocksForOneKeyAtOnce(t *testing.T) {
	k := newKeyedMutex()
	const keys = 3
	const workers = 20
	const perKeyIncrements = 500
	counters := make([]int, keys) // counters[i] belongs to key string(rune('a'+i)) alone.

	var wg sync.WaitGroup
	for g := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range perKeyIncrements {
				idx := (worker + i) % keys
				unlock := k.lock(string(rune('a' + idx)))
				counters[idx]++ // unsynchronized but for keyedMutex: a same-key race corrupts this or the count.
				unlock()
			}
		}(g)
	}
	wg.Wait()

	var total int
	for _, n := range counters {
		total += n
	}
	if total != workers*perKeyIncrements {
		t.Fatalf("counters sum to %d, want %d: a key's lock let two callers in at once", total, workers*perKeyIncrements)
	}
	k.mu.Lock()
	remaining := len(k.locks)
	k.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected every key's entry to be gone once idle, %d remain", remaining)
	}
}
