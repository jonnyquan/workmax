package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"
)

// --- DefaultIsRetryable classifier ---

func TestDefaultIsRetryable_Retryable(t *testing.T) {
	// Each of these error strings should classify as retryable. If
	// a new vendor shape shows up in the wild, add it here FIRST,
	// then extend retryableSignature to match.
	retryable := []string{
		"HTTP 429 Too Many Requests",
		"rate_limit_exceeded: slow down",
		"rate-limited by upstream",
		"too many requests",
		"openai tts status 503: service unavailable",
		"openai tts status 500: internal server error",
		"request timeout after 30s",
		"context timed out waiting for response",
		"rpc error: code = DeadlineExceeded desc = timeout",
		"read tcp 10.0.0.1:443: connection reset by peer",
		"write: broken pipe",
		"dial tcp: connection refused",
		"socket hang up",
		"unexpected EOF while reading response body",
		"read: i/o timeout",
		"lookup api.provider.com: Temporary failure in name resolution",
		"lookup foo.example.com: no such host",
		"Bad Gateway",
		"Gateway Time-out",
	}
	for _, msg := range retryable {
		if !DefaultIsRetryable(errors.New(msg)) {
			t.Errorf("should be retryable: %q", msg)
		}
	}
}

func TestDefaultIsRetryable_NotRetryable(t *testing.T) {
	notRetryable := []string{
		"HTTP 400 Bad Request",
		"HTTP 401 Unauthorized",
		"HTTP 403 Forbidden",
		"HTTP 404 Not Found",
		"invalid api key",
		"invalid_voice: voice 'does-not-exist' is not valid",
		"prompt violates content policy",
		"quota exceeded for this billing period",
		"malformed JSON in request body",
	}
	for _, msg := range notRetryable {
		if DefaultIsRetryable(errors.New(msg)) {
			t.Errorf("should NOT be retryable: %q", msg)
		}
	}
}

func TestDefaultIsRetryable_NilAndContextErrors(t *testing.T) {
	if DefaultIsRetryable(nil) {
		t.Error("nil error must not be retryable")
	}
	if DefaultIsRetryable(context.Canceled) {
		t.Error("context.Canceled must not be retryable — caller chose to stop")
	}
	if DefaultIsRetryable(context.DeadlineExceeded) {
		t.Error("context.DeadlineExceeded must not be retryable — fresh attempt sees same expired deadline")
	}
}

func TestDefaultIsRetryable_CaseInsensitive(t *testing.T) {
	cases := []string{
		"TIMEOUT after 30s",
		"Rate-Limit hit",
		"CONNECTION RESET",
	}
	for _, msg := range cases {
		if !DefaultIsRetryable(errors.New(msg)) {
			t.Errorf("case-insensitive match failed for %q", msg)
		}
	}
}

// --- Combine ---

func TestCombine_AnyTruePredicate(t *testing.T) {
	alwaysFalse := func(err error) bool { return false }
	alwaysTrue := func(err error) bool { return true }
	p := Combine(alwaysFalse, alwaysTrue)
	if !p(errors.New("x")) {
		t.Error("combined predicate should return true when any inner does")
	}
}

func TestCombine_AllFalse(t *testing.T) {
	alwaysFalse := func(err error) bool { return false }
	p := Combine(alwaysFalse, alwaysFalse)
	if p(errors.New("x")) {
		t.Error("combined predicate should return false when all inner do")
	}
}

// --- Do: exponential backoff + retry counting ---

// fakeClock captures requested sleeps instead of waiting. Lets
// tests assert the backoff schedule deterministically.
type fakeClock struct {
	sleeps []time.Duration
}

func (c *fakeClock) Sleep(d time.Duration) { c.sleeps = append(c.sleeps, d) }

func newDeterministicPolicy(attempts int, clock *fakeClock) Policy {
	return NoJitter(Policy{
		MaxAttempts: attempts,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    10 * time.Second,
		Sleep:       clock.Sleep,
		// Deterministic rng in case jitter gets re-enabled by accident —
		// tests should still produce stable results.
		Rng: rand.New(rand.NewSource(1)),
	})
}

func TestDo_SucceedsOnFirstAttempt(t *testing.T) {
	clock := &fakeClock{}
	calls := 0
	err := Do(context.Background(), newDeterministicPolicy(3, clock), func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls=%d, want 1", calls)
	}
	if len(clock.sleeps) != 0 {
		t.Errorf("no sleep on success, got %v", clock.sleeps)
	}
}

func TestDo_RetriesTransientThenSucceeds(t *testing.T) {
	clock := &fakeClock{}
	calls := 0
	err := Do(context.Background(), newDeterministicPolicy(4, clock), func() error {
		calls++
		if calls < 3 {
			return errors.New("HTTP 429 rate_limit_exceeded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3 (2 retries + success)", calls)
	}
	// Two backoffs: 100ms and 200ms (exponential base×2^(n-1)).
	wantSleeps := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(clock.sleeps) != 2 {
		t.Fatalf("sleeps=%v, want 2 backoffs", clock.sleeps)
	}
	for i, want := range wantSleeps {
		if clock.sleeps[i] != want {
			t.Errorf("sleep[%d]=%v, want %v", i, clock.sleeps[i], want)
		}
	}
}

func TestDo_StopsAtMaxAttempts(t *testing.T) {
	clock := &fakeClock{}
	calls := 0
	err := Do(context.Background(), newDeterministicPolicy(3, clock), func() error {
		calls++
		return errors.New("HTTP 503 service unavailable")
	})
	if err == nil {
		t.Fatal("expected err after exhausting retries")
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3 (MaxAttempts)", calls)
	}
	// 2 sleeps between 3 attempts.
	if len(clock.sleeps) != 2 {
		t.Errorf("sleeps=%v, want 2", clock.sleeps)
	}
}

func TestDo_StopsImmediatelyOnNonRetryable(t *testing.T) {
	clock := &fakeClock{}
	calls := 0
	err := Do(context.Background(), newDeterministicPolicy(5, clock), func() error {
		calls++
		return errors.New("HTTP 401 unauthorized")
	})
	if err == nil {
		t.Fatal("expected err")
	}
	if calls != 1 {
		t.Errorf("non-retryable should abort after 1 call, got %d", calls)
	}
	if len(clock.sleeps) != 0 {
		t.Errorf("no sleeps on non-retryable, got %v", clock.sleeps)
	}
}

func TestDo_SingleAttemptNoRetry(t *testing.T) {
	// MaxAttempts=1 means "try once, no retry". Useful when a
	// caller wants the classifier off for a particular call.
	clock := &fakeClock{}
	calls := 0
	err := Do(context.Background(), newDeterministicPolicy(1, clock), func() error {
		calls++
		return errors.New("HTTP 429")
	})
	if err == nil {
		t.Fatal("expected err")
	}
	if calls != 1 {
		t.Errorf("MaxAttempts=1: got %d calls", calls)
	}
}

func TestDo_PreservesLastError(t *testing.T) {
	// Caller should see the vendor-shaped error the final attempt
	// produced — not a wrapped "retries exhausted" sentinel. Lets
	// upstream code grep / classify / display the real error.
	clock := &fakeClock{}
	final := errors.New("sentinel-final-error HTTP 503")
	err := Do(context.Background(), newDeterministicPolicy(2, clock), func() error {
		return final
	})
	if err != final {
		t.Errorf("expected last error pass-through, got %v", err)
	}
}

func TestDo_BackoffCapsAtMaxDelay(t *testing.T) {
	// With base=100ms max=300ms, attempts 1..5 should give
	// sleeps: 100, 200, 300, 300, 300 (capped).
	clock := &fakeClock{}
	p := NoJitter(Policy{
		MaxAttempts: 6,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    300 * time.Millisecond,
		Sleep:       clock.Sleep,
		IsRetryable: func(err error) bool { return true },
	})
	_ = Do(context.Background(), p, func() error { return errors.New("x") })
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		300 * time.Millisecond,
		300 * time.Millisecond,
		300 * time.Millisecond,
	}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("sleeps=%v", clock.sleeps)
	}
	for i, w := range want {
		if clock.sleeps[i] != w {
			t.Errorf("sleep[%d]=%v, want %v", i, clock.sleeps[i], w)
		}
	}
}

func TestDo_AbortsOnContextCancelDuringAttempt(t *testing.T) {
	// Cancellation BEFORE the first attempt runs returns ctx.Err
	// (or the last recorded error if any).
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	clock := &fakeClock{}
	calls := 0
	err := Do(ctx, newDeterministicPolicy(5, clock), func() error {
		calls++
		return errors.New("shouldn't run")
	})
	if err == nil {
		t.Fatal("expected err")
	}
	if calls != 0 {
		t.Errorf("calls=%d, want 0 (ctx cancelled before first attempt)", calls)
	}
}

func TestDo_AbortsDuringBackoffSleep(t *testing.T) {
	// Use real time.Sleep (tiny durations) + real ctx with deadline
	// so the select in interruptibleSleep fires. Keeps the test <50ms.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	p := Policy{
		MaxAttempts: 10,
		BaseDelay:   500 * time.Millisecond, // way more than ctx deadline
		MaxDelay:    1 * time.Second,
		IsRetryable: func(err error) bool { return true },
	}
	calls := 0
	err := Do(ctx, p, func() error {
		calls++
		return errors.New("transient")
	})
	if err == nil {
		t.Fatal("expected err")
	}
	// Should have made at least one attempt but not all 10.
	if calls < 1 || calls >= 10 {
		t.Errorf("calls=%d, want 1..9 (ctx cut backoff short)", calls)
	}
}

func TestDo_ZeroPolicyUsesDefaults(t *testing.T) {
	// Zero-valued Policy should still work — tries once, no retry
	// (MaxAttempts defaults to 1).
	calls := 0
	err := Do(context.Background(), Policy{}, func() error {
		calls++
		return fmt.Errorf("HTTP 429")
	})
	if err == nil || calls != 1 {
		t.Errorf("zero policy: err=%v calls=%d", err, calls)
	}
}

// --- backoffFor (direct) ---

func TestBackoffFor_ExponentialBeforeCap(t *testing.T) {
	base := 100 * time.Millisecond
	max := 10 * time.Second
	wants := map[int]time.Duration{
		1: 100 * time.Millisecond,
		2: 200 * time.Millisecond,
		3: 400 * time.Millisecond,
		4: 800 * time.Millisecond,
	}
	for attempt, want := range wants {
		if got := backoffFor(attempt, base, max); got != want {
			t.Errorf("backoffFor(%d)=%v, want %v", attempt, got, want)
		}
	}
}

func TestBackoffFor_CapAfterExponential(t *testing.T) {
	base := 100 * time.Millisecond
	max := 500 * time.Millisecond
	if got := backoffFor(10, base, max); got != max {
		t.Errorf("huge attempt count should cap at max, got %v", got)
	}
}

// --- Jitter spread ---

func TestJitterFullSpread_BoundedByCapped(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 100; i++ {
		got := jitterFullSpread(rng, time.Second)
		if got < 0 || got > time.Second {
			t.Fatalf("jitter outside [0, capped]: got %v", got)
		}
	}
}

func TestJitterFullSpread_ZeroCappedReturnsZero(t *testing.T) {
	if got := jitterFullSpread(rand.New(rand.NewSource(1)), 0); got != 0 {
		t.Errorf("zero capped should yield 0, got %v", got)
	}
}
