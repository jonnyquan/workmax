package workagent

import (
	"sync"
	"testing"
	"time"
)

// CircuitBreaker tests pin the three-state transition contract:
// closed → open (after threshold failures) → half-open (after recovery
// timeout) → closed (on next success). The pool's auto-switch trigger
// reads ConsecutiveFailures() in-memory now, so any breakage in the
// counter or transition logic surfaces here instead of in production
// during a live model outage.

func TestCircuitBreaker_StartsClosed(t *testing.T) {
	cb := NewCircuitBreaker(1)
	if cb.GetState() != "closed" {
		t.Errorf("new breaker state = %q, want closed", cb.GetState())
	}
	if !cb.CanAttempt() {
		t.Error("closed breaker must allow attempts")
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(1)

	// Default threshold is 5. Failures 1..4 must keep it closed.
	for i := 0; i < 4; i++ {
		cb.RecordFailure()
		if cb.GetState() != "closed" {
			t.Errorf("after %d failures state = %q, want closed", i+1, cb.GetState())
		}
		if cb.ConsecutiveFailures() != i+1 {
			t.Errorf("after %d failures count = %d", i+1, cb.ConsecutiveFailures())
		}
	}

	// 5th failure trips it.
	cb.RecordFailure()
	if cb.GetState() != "open" {
		t.Errorf("after 5 failures state = %q, want open", cb.GetState())
	}
	if cb.CanAttempt() {
		t.Error("open breaker must reject attempts")
	}
}

func TestCircuitBreaker_RecordSuccessClosesAndResetsCount(t *testing.T) {
	cb := NewCircuitBreaker(1)
	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != "open" {
		t.Fatalf("setup: expected open, got %q", cb.GetState())
	}

	cb.RecordSuccess()
	if cb.GetState() != "closed" {
		t.Errorf("after success state = %q, want closed", cb.GetState())
	}
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("after success count = %d, want 0", cb.ConsecutiveFailures())
	}
}

func TestCircuitBreaker_RecoveryTimeoutEntersHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(1)
	// Compress the recovery window so the test stays fast.
	cb.recoveryTimeout = 10 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != "open" {
		t.Fatalf("setup: expected open, got %q", cb.GetState())
	}

	if cb.CanAttempt() {
		t.Error("CanAttempt during recovery window should be false")
	}

	time.Sleep(15 * time.Millisecond)

	if !cb.CanAttempt() {
		t.Error("CanAttempt after recovery should be true (half-open probe)")
	}
	if cb.GetState() != "half-open" {
		t.Errorf("post-recovery state = %q, want half-open", cb.GetState())
	}
}

func TestCircuitBreaker_HalfOpenSuccessClosesAgain(t *testing.T) {
	cb := NewCircuitBreaker(1)
	cb.recoveryTimeout = 10 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(15 * time.Millisecond)
	cb.CanAttempt() // transitions to half-open

	cb.RecordSuccess()
	if cb.GetState() != "closed" {
		t.Errorf("half-open + success → state = %q, want closed", cb.GetState())
	}
}

func TestCircuitBreaker_ConcurrentFailuresStillTrip(t *testing.T) {
	// 10 goroutines fail in parallel; the breaker still trips at >=5
	// without losing failure events to the lock.
	cb := NewCircuitBreaker(1)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cb.RecordFailure()
		}()
	}
	wg.Wait()

	if cb.GetState() != "open" {
		t.Errorf("after 10 concurrent failures state = %q, want open", cb.GetState())
	}
	if cb.ConsecutiveFailures() != 10 {
		t.Errorf("count = %d, want 10", cb.ConsecutiveFailures())
	}
}

// Regression: half-open must allow exactly ONE in-flight probe.
// The original implementation returned true for every CanAttempt
// while in half-open, so 100 concurrent callers during recovery
// all got promoted to "probes" and slammed the still-broken upstream
// in a thundering herd. The fix: first transition open→half-open
// returns true, subsequent calls return false until RecordSuccess
// or RecordFailure resolves the probe.
func TestCircuitBreaker_HalfOpenAllowsSingleProbeAtATime(t *testing.T) {
	cb := NewCircuitBreaker(1)
	cb.recoveryTimeout = 10 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(15 * time.Millisecond)

	// First CanAttempt after the recovery window opens the probe
	// slot.
	if !cb.CanAttempt() {
		t.Fatal("first CanAttempt after recovery should grant the probe")
	}
	if cb.GetState() != "half-open" {
		t.Fatalf("state after probe grant = %q, want half-open", cb.GetState())
	}

	// Subsequent calls during the probe MUST be denied — otherwise
	// 100 concurrent requests all become probes.
	for i := 0; i < 5; i++ {
		if cb.CanAttempt() {
			t.Errorf("CanAttempt #%d during in-flight probe returned true; want false", i+2)
		}
	}

	// Probe success closes the breaker; CanAttempt is allowed again.
	cb.RecordSuccess()
	if !cb.CanAttempt() {
		t.Error("CanAttempt after successful probe should allow")
	}
}

// Regression: probe failure should re-open the breaker (with a
// fresh recovery window) so the next probe waits the full
// recoveryTimeout — not the residual time from the previous open.
func TestCircuitBreaker_HalfOpenFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(1)
	cb.recoveryTimeout = 10 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(15 * time.Millisecond)
	if !cb.CanAttempt() {
		t.Fatal("setup: probe grant failed")
	}
	if cb.GetState() != "half-open" {
		t.Fatalf("setup: state = %q, want half-open", cb.GetState())
	}

	// Probe fails — RecordFailure bumps count over threshold and
	// state goes back to "open".
	cb.RecordFailure()
	if cb.GetState() != "open" {
		t.Errorf("state after failed probe = %q, want open", cb.GetState())
	}
	// Within the new recovery window, no probe granted.
	if cb.CanAttempt() {
		t.Error("CanAttempt during fresh recovery window should be false")
	}
}

// Regression: a half-open probe that never reports back (upstream hung,
// caller crashed before RecordSuccess/Failure) used to pin the breaker
// in half-open forever — CanAttempt returned false on every subsequent
// call and the pool could not route to this account again until process
// restart. halfOpenProbeTTL elapses → next CanAttempt re-opens with a
// fresh recovery window so the system self-heals.
func TestCircuitBreaker_HalfOpenProbeTTLReopens(t *testing.T) {
	cb := NewCircuitBreaker(1)
	cb.recoveryTimeout = 10 * time.Millisecond
	cb.halfOpenProbeTTL = 20 * time.Millisecond

	for i := 0; i < 5; i++ {
		cb.RecordFailure()
	}
	time.Sleep(15 * time.Millisecond)

	if !cb.CanAttempt() {
		t.Fatal("setup: probe grant failed")
	}
	if cb.GetState() != "half-open" {
		t.Fatalf("setup: state = %q, want half-open", cb.GetState())
	}

	// Probe never reports back. Wait past halfOpenProbeTTL and the
	// next CanAttempt should detect the stuck probe and reopen.
	time.Sleep(25 * time.Millisecond)

	if cb.CanAttempt() {
		t.Error("CanAttempt during the fresh recovery window should be false")
	}
	if cb.GetState() != "open" {
		t.Errorf("after probe TTL expiry state = %q, want open", cb.GetState())
	}

	// Once the new recovery window passes, a fresh probe is granted.
	time.Sleep(15 * time.Millisecond)
	if !cb.CanAttempt() {
		t.Error("CanAttempt after fresh recovery should grant new probe")
	}
}

func TestCircuitBreaker_RecordFailureThenSuccessSequence(t *testing.T) {
	cb := NewCircuitBreaker(1)

	// 3 failures, 1 success, 3 more failures — should stay closed
	// because RecordSuccess in the middle resets the counter.
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.ConsecutiveFailures() != 3 {
		t.Errorf("after 3 failures count = %d", cb.ConsecutiveFailures())
	}

	cb.RecordSuccess()
	if cb.ConsecutiveFailures() != 0 {
		t.Errorf("after intervening success count = %d, want 0", cb.ConsecutiveFailures())
	}

	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}
	if cb.GetState() != "closed" {
		t.Errorf("3 + reset + 3 should stay closed, got %q", cb.GetState())
	}
}
