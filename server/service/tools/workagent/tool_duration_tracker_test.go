package workagent

import (
	"sync"
	"testing"
	"time"
)

func TestToolDurationTracker_RecordThenConsume(t *testing.T) {
	tracker := newToolDurationTracker()
	id := "tool_use_abc123"

	tracker.record(&id)
	if tracker.inflightCount() != 1 {
		t.Fatalf("inflightCount = %d, want 1", tracker.inflightCount())
	}

	// Brief sleep so the elapsed duration is non-zero — the actual
	// value is timing-dependent so we just assert "non-negative" + "consumed".
	time.Sleep(2 * time.Millisecond)

	dur, ok := tracker.consume(&id)
	if !ok {
		t.Fatal("consume returned ok=false for a recorded id")
	}
	if dur < 0 {
		t.Errorf("consume returned negative duration: %v", dur)
	}
	if tracker.inflightCount() != 0 {
		t.Errorf("inflightCount after consume = %d, want 0", tracker.inflightCount())
	}
}

func TestToolDurationTracker_ConsumeWithoutRecord(t *testing.T) {
	tracker := newToolDurationTracker()
	id := "never_recorded"

	dur, ok := tracker.consume(&id)
	if ok {
		t.Errorf("consume returned ok=true without record; dur=%v", dur)
	}
}

func TestToolDurationTracker_NilAndEmptyIDs(t *testing.T) {
	tracker := newToolDurationTracker()

	// nil pointer — must not panic, must be a no-op
	tracker.record(nil)
	if tracker.inflightCount() != 0 {
		t.Errorf("nil record stored an entry: count=%d", tracker.inflightCount())
	}
	if _, ok := tracker.consume(nil); ok {
		t.Error("consume(nil) returned ok=true")
	}

	// empty string — same contract
	empty := ""
	tracker.record(&empty)
	if tracker.inflightCount() != 0 {
		t.Errorf("empty-string record stored an entry: count=%d", tracker.inflightCount())
	}
	if _, ok := tracker.consume(&empty); ok {
		t.Error("consume(\"\") returned ok=true")
	}
}

// Double-consume must return ok=false the second time. Prevents a
// hook firing twice (or a buggy SDK retry) from reporting two
// durations for the same tool call.
func TestToolDurationTracker_DoubleConsume(t *testing.T) {
	tracker := newToolDurationTracker()
	id := "tool_use_double"

	tracker.record(&id)

	if _, ok := tracker.consume(&id); !ok {
		t.Fatal("first consume returned ok=false")
	}
	if dur, ok := tracker.consume(&id); ok {
		t.Errorf("second consume returned ok=true; dur=%v", dur)
	}
}

// TestToolDurationTracker_Concurrent stress-tests the mutex —
// concurrent record+consume on different ids must not race.
func TestToolDurationTracker_Concurrent(t *testing.T) {
	tracker := newToolDurationTracker()

	var wg sync.WaitGroup
	const workers = 16
	const opsPerWorker = 100

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				id := makeID(workerID, i)
				tracker.record(&id)
				if _, ok := tracker.consume(&id); !ok {
					t.Errorf("worker %d op %d: consume failed", workerID, i)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	if tracker.inflightCount() != 0 {
		t.Errorf("post-concurrent inflightCount = %d, want 0", tracker.inflightCount())
	}
}

func makeID(worker, op int) string {
	// "w0_op_0", "w15_op_99", etc. Fast and unique per (worker, op) pair.
	out := make([]byte, 0, 16)
	out = append(out, 'w')
	out = appendInt(out, worker)
	out = append(out, '_', 'o', 'p', '_')
	out = appendInt(out, op)
	return string(out)
}

func appendInt(b []byte, n int) []byte {
	if n == 0 {
		return append(b, '0')
	}
	if n < 0 {
		b = append(b, '-')
		n = -n
	}
	var digits [10]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return append(b, digits[i:]...)
}
