package salt

import (
	"sync"
	"testing"
)

// Same host returns the same mutex across calls (so Lock on one caller
// blocks Lock on another).
func TestHostLockFor_SameHostSameMutex(t *testing.T) {
	a := HostLockFor("10.0.0.1")
	b := HostLockFor("10.0.0.1")
	if a != b {
		t.Fatalf("expected same *Mutex for same host, got %p and %p", a, b)
	}
}

// Different hosts get distinct mutexes (so two hosts don't serialize
// against each other).
func TestHostLockFor_DifferentHostsDistinctMutex(t *testing.T) {
	a := HostLockFor("10.0.0.1")
	b := HostLockFor("10.0.0.2")
	if a == b {
		t.Fatalf("expected distinct *Mutex for different hosts, got same %p", a)
	}
}

// Concurrent LoadOrStore calls for the same host all converge on one mutex
// — guards against a future refactor that introduces a race in the
// per-host registry.
func TestHostLockFor_ConcurrentSameHost(t *testing.T) {
	const goroutines = 32
	var wg sync.WaitGroup
	results := make([]*sync.Mutex, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i] = HostLockFor("10.0.0.42")
		}(i)
	}
	wg.Wait()
	first := results[0]
	for i, m := range results {
		if m != first {
			t.Fatalf("goroutine %d got different mutex %p vs first %p", i, m, first)
		}
	}
}
