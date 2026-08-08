package workagent

// agent_turn_lock_test.go — pins GetAgentTurnLock's three load-bearing
// properties. The lock serializes (Reserve → Process → Finalize) on
// a single thread; if two concurrent SSE handlers each got a
// DIFFERENT mutex for the same threadID, both Reserve calls would
// race and the time.Now()-fallback idempotency key would double-
// charge. Pin both the happy-path identity and the concurrent
// LoadOrStore race resolution.

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestGetAgentTurnLock_SameThreadSameMutex(t *testing.T) {
	const threadID = "thread-identity-fixture"

	a := GetAgentTurnLock(threadID)
	b := GetAgentTurnLock(threadID)
	if a != b {
		t.Errorf("same threadID must return same mutex pointer (got %p vs %p)", a, b)
	}
}

func TestGetAgentTurnLock_DifferentThreadsDifferentMutexes(t *testing.T) {
	a := GetAgentTurnLock("thread-A-" + t.Name())
	b := GetAgentTurnLock("thread-B-" + t.Name())
	if a == b {
		t.Errorf("different threadIDs must return distinct mutex pointers")
	}
}

// TestGetAgentTurnLock_ConcurrentFirstCallRaces is the load-bearing
// contract: when N goroutines all call GetAgentTurnLock with the same
// previously-unseen threadID at the same time, every goroutine must
// receive the SAME *sync.Mutex pointer. sync.Map's LoadOrStore is the
// primitive that makes this work — if a future refactor uses
// `if !exists { Store(...) }` instead, the race window between
// Load and Store would let two goroutines each Store their own mutex
// before the second's Load happens, and the lock no longer serializes.
//
// Run 64 goroutines × fresh threadID per test invocation; assert
// every returned pointer is identical. Repeat with -race for the
// happy-path concurrency proof.
func TestGetAgentTurnLock_ConcurrentFirstCallRaces(t *testing.T) {
	const N = 64
	threadID := "thread-race-" + t.Name()

	var ready sync.WaitGroup
	var go_ sync.WaitGroup
	ready.Add(N)
	go_.Add(1)

	results := make([]*sync.Mutex, N)
	var done sync.WaitGroup
	done.Add(N)

	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer done.Done()
			ready.Done()
			go_.Wait() // synchronize start
			results[i] = GetAgentTurnLock(threadID)
		}()
	}

	ready.Wait()
	go_.Done() // release all goroutines simultaneously
	done.Wait()

	first := results[0]
	for i, m := range results {
		if m != first {
			t.Errorf("goroutine %d got mutex %p, want %p (concurrent first-call must collapse to one mutex)", i, m, first)
		}
	}
}

// TestGetAgentTurnLock_ActuallySerializes — end-to-end: two goroutines
// hold the same lock concurrently and the second observes the first's
// "I'm inside the critical section" flag. Belt-and-suspenders on the
// identity test, plus exercises the mutex itself (any future swap to
// a non-blocking primitive would fail here).
func TestGetAgentTurnLock_ActuallySerializes(t *testing.T) {
	threadID := "thread-serialize-" + t.Name()
	mu := GetAgentTurnLock(threadID)

	var inCritical atomic.Int32
	var overlap atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again := GetAgentTurnLock(threadID)
			if again != mu {
				t.Errorf("repeat fetch returned different mutex")
			}
			again.Lock()
			if inCritical.Add(1) != 1 {
				overlap.Add(1)
			}
			// brief work
			for j := 0; j < 1000; j++ {
				_ = j
			}
			inCritical.Add(-1)
			again.Unlock()
		}()
	}
	wg.Wait()

	if overlap.Load() != 0 {
		t.Errorf("mutex failed to serialize: %d overlapping critical sections observed", overlap.Load())
	}
}
