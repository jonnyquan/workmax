package workagent

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// Run with -race to catch the regression: LastError/LastErrorTime
// used to be plain field reads/writes against concurrent Get/Reset.
// time.Time has an internal monotonic-clock pointer so a torn read
// could surface as a garbage instant in monitoring; this test
// hammers the API from many goroutines so a missing mutex would
// trip the race detector.
func TestSessionMetrics_ConcurrentRecordAndReadIsRaceFree(t *testing.T) {
	ResetSessionMetrics()
	t.Cleanup(ResetSessionMetrics)

	const writers = 8
	const readers = 8
	const itersPerGoroutine = 200

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < itersPerGoroutine; j++ {
				RecordSessionAttempt()
				if j%3 == 0 {
					RecordSessionNotFound(errors.New("session not found"))
				}
				if j%5 == 0 {
					RecordSessionRecovered()
				}
				if j%7 == 0 {
					RecordRecoveryFailed()
				}
			}
		}()
	}
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < itersPerGoroutine; j++ {
				m := GetSessionMetrics()
				// Touch the time so the race detector sees the
				// read; without this the optimizer might elide it.
				if t, ok := m["last_error_time"].(time.Time); ok {
					_ = t.UnixNano()
				}
			}
		}()
	}
	wg.Wait()

	// Counter sanity — TotalAttempts increments unconditionally
	// once per writer iteration. Catch obvious atomic regression.
	m := GetSessionMetrics()
	if got, _ := m["total_attempts"].(int64); got != int64(writers*itersPerGoroutine) {
		t.Errorf("total_attempts = %d; want %d", got, writers*itersPerGoroutine)
	}
}

// GetSessionMetrics must return a coherent snapshot of the
// last-error pair (string + time were set together; reader sees
// either both unset or the most recent pair, never a mix).
func TestSessionMetrics_LastErrorPairIsCoherent(t *testing.T) {
	ResetSessionMetrics()
	t.Cleanup(ResetSessionMetrics)

	before := GetSessionMetrics()
	if before["last_error"] != "" {
		t.Errorf("expected empty last_error after reset, got %q", before["last_error"])
	}

	RecordSessionNotFound(errors.New("boom"))

	after := GetSessionMetrics()
	if after["last_error"] != "boom" {
		t.Errorf("last_error = %v; want boom", after["last_error"])
	}
	ts, ok := after["last_error_time"].(time.Time)
	if !ok || ts.IsZero() {
		t.Errorf("last_error_time = %v; want a real time", after["last_error_time"])
	}
}
