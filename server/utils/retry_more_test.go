package utils

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"server/globals"
)

func init() {
	// retry.go logs via globals.GraLog on every non-retryable / exhausted
	// branch. In the test binary the logger is nil (main.go never runs),
	// so calls would nil-panic. Swap in a no-op zap logger once; harmless
	// in the _more file because base retry_test.go never exercises
	// WithRetry and therefore never touches the logger.
	if globals.GraLog == nil {
		globals.GraLog = zap.NewNop()
	}
}

// retry_test.go pins the headline contract (error classification,
// retry allow-list, exponential backoff + jitter + cap, stats math).
// These fill the quieter gate invariants a silent regression would
// slip past:
//
//   • WithRetry's top-level flow is entirely unserved by the base
//     file — only the helper functions are tested. Pin:
//     (a) success on first attempt returns immediately (no wait).
//     (b) success on a LATER attempt returns the last result (not
//         a zero-valued T).
//     (c) non-retryable classification short-circuits: attempts=1,
//         returns the original error VERBATIM (not wrapped).
//     (d) retryable classification but NOT in RetryOnErrors allow-list
//         still short-circuits — the allow-list overrides retryability.
//     (e) MaxRetries exhausted returns the last error (not a
//         synthesised "max retries exceeded"). Callers compare errors
//         against sentinels, so wrapping would silently break them.
//     (f) ctx.Cancel inside the loop aborts with ctx.Err() (not the
//         operation error). Pin the context-wins doctrine.
//     (g) Attempt count is MaxRetries + 1 (initial + retries), NOT
//         MaxRetries. A refactor that counted one-off would miss one
//         attempt or overshoot.
//   • classifyError order precedence overlap:
//     - "connection timeout" matches BOTH network_error (connection*)
//       AND timeout branches. Network branch appears FIRST in the
//       function, so network_error wins. Pin so a refactor that
//       reordered branches would surface as a retry-policy shift.
//     - "service unavailable" overlaps model_overloaded and
//       server_error_5xx. Model_overloaded is earlier → wins.
//     - "gateway timeout" overlaps server_error_5xx and timeout.
//       Timeout branch is earlier → wins (base file already pins
//       this one, so skip here).
//   • classifyError empty-string error message returns unknown_error
//     non-retryable — pin so a refactor that matched empty to any
//     branch would surface. This is a security-adjacent invariant:
//     an un-typed error must never accidentally become retryable.
//   • classifyError preserves ORIGINAL-CASE message in RetryableError.Message
//     even though matching is lowercase. Pin so a refactor that
//     stored strings.ToLower(msg) in the struct would surface
//     (callers log the message in user-visible surfaces).
//   • shouldRetryOnError uses EXACT-string match, not substring.
//     "timeout" is NOT a prefix of "timeout_long" — pin so a refactor
//     to strings.Contains would silently widen the allow-list.
//   • shouldRetryOnError with empty allow-list returns false for
//     every type (including ""). Pin the fail-closed default.
//   • calculateDelay boundary: initialDelay already > maxDelay clamps
//     to maxDelay on attempt=0. Pin so a refactor that applied the
//     cap only on "grown" delays would leak oversized initialDelays.
//   • calculateDelay backoffFactor=1.0 stays within [initial*0.9,
//     initial*1.1] at every attempt — no growth. Pin the flat case
//     so a refactor that hard-coded 2.0 as floor would surface.
//   • calculateDelay cap is the EXACT ceiling after jitter — no
//     +10% overshoot. Pin: attempt=10 with initial=2s/max=30s always
//     returns <= 30s even across many samples.
//   • RetryStats.RecordAttempt keeps LastErrorType empty on a
//     pure-success series — pin so a refactor that clobbered it
//     with "" on success would wipe the last-failure context.
//   • RetryStats.AverageDelay divides by TotalAttempts (not
//     FailedAttempts). Pin so a refactor changing the denominator
//     would shift observability.
//   • RetryStats success+failed invariant: on every RecordAttempt,
//     SuccessAttempts + FailedAttempts == TotalAttempts. Pin so a
//     refactor that double-counted or missed a branch would surface.

func TestWithRetry_SuccessOnFirstAttempt_NoWait(t *testing.T) {
	cfg := &RetryConfig{MaxRetries: 3, InitialDelay: time.Second, MaxDelay: time.Second, BackoffFactor: 2.0, RetryOnErrors: []string{"timeout"}}
	var attempts int32
	start := time.Now()
	got, err := WithRetry(context.Background(), cfg, func() (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "ok", nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "ok" {
		t.Fatalf("got = %q, want ok", got)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (success should not retry)", attempts)
	}
	// Even 100ms is orders of magnitude more than a no-wait happy-path.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("success path slept: elapsed=%v", elapsed)
	}
}

func TestWithRetry_SuccessOnLaterAttempt_ReturnsLatestResult(t *testing.T) {
	cfg := &RetryConfig{
		MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond,
		BackoffFactor: 2.0, RetryOnErrors: []string{"timeout"},
	}
	var attempts int32
	got, err := WithRetry(context.Background(), cfg, func() (int, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return 0, errors.New("request timeout")
		}
		return 42, nil
	})

	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 42 {
		t.Fatalf("got = %d, want 42 (later-attempt result must be surfaced)", got)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (2 fails + 1 success)", attempts)
	}
}

func TestWithRetry_NonRetryableShortCircuits(t *testing.T) {
	cfg := &RetryConfig{MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, BackoffFactor: 2.0, RetryOnErrors: []string{"timeout", "network_error"}}
	var attempts int32
	orig := errors.New("bad request: missing field")
	got, err := WithRetry(context.Background(), cfg, func() (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", orig
	})

	if err == nil || err.Error() != orig.Error() {
		t.Fatalf("err should be the VERBATIM original (not wrapped), got %v", err)
	}
	if got != "" {
		t.Fatalf("zero-value on failure, got %q", got)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (non-retryable must not loop)", attempts)
	}
}

func TestWithRetry_RetryableButNotInAllowListShortCircuits(t *testing.T) {
	// rate_limit_exceeded IS Retryable:true, but allow-list only has
	// timeout — the whitelist overrides.
	cfg := &RetryConfig{MaxRetries: 3, InitialDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, BackoffFactor: 2.0, RetryOnErrors: []string{"timeout"}}
	var attempts int32
	_, err := WithRetry(context.Background(), cfg, func() (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", errors.New("rate limit hit")
	})

	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (allow-list must filter retryable types)", attempts)
	}
}

func TestWithRetry_MaxRetriesExhaustedReturnsLastError(t *testing.T) {
	cfg := &RetryConfig{MaxRetries: 2, InitialDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, BackoffFactor: 2.0, RetryOnErrors: []string{"timeout"}}
	var attempts int32
	// Attempts 1,2,3 will each return a DIFFERENT error; verify the
	// LAST one is the one that surfaces (not the first).
	_, err := WithRetry(context.Background(), cfg, func() (string, error) {
		n := atomic.AddInt32(&attempts, 1)
		return "", errors.New("timeout #" + string(rune('0'+n)))
	})

	if err == nil {
		t.Fatal("expected exhaustion error")
	}
	if err.Error() != "timeout #3" {
		t.Fatalf("err = %q, want %q (last-attempt error must win)", err.Error(), "timeout #3")
	}
	// Attempt count = MaxRetries + 1 (1 initial + 2 retries = 3).
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3 (MaxRetries=2 + 1 initial)", attempts)
	}
}

func TestWithRetry_ContextCancelAbortsDuringDelay(t *testing.T) {
	cfg := &RetryConfig{MaxRetries: 5, InitialDelay: 200 * time.Millisecond, MaxDelay: 500 * time.Millisecond, BackoffFactor: 2.0, RetryOnErrors: []string{"timeout"}}
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int32
	// Cancel 20ms in — well under the 180ms jittered delay, so cancel
	// wins during the wait-select.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := WithRetry(ctx, cfg, func() (string, error) {
		atomic.AddInt32(&attempts, 1)
		return "", errors.New("request timeout")
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// Elapsed must be << full jittered delay (180ms) — cancel aborted.
	if elapsed > 150*time.Millisecond {
		t.Fatalf("cancel did not short-circuit delay: elapsed=%v", elapsed)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (cancelled during first delay)", attempts)
	}
}

func TestClassifyError_OrderPrecedence_NetworkBeatsTimeout(t *testing.T) {
	// "connection timeout" matches both network_error (connection*)
	// AND timeout (timeout). Network branch is earlier → wins.
	got := classifyError(errors.New("connection timeout"))
	if got.ErrorType != "network_error" {
		t.Fatalf("ErrorType = %q, want network_error (earlier branch wins)", got.ErrorType)
	}
}

func TestClassifyError_OrderPrecedence_ModelOverloadedBeatsServerError(t *testing.T) {
	// "service unavailable" appears in BOTH model_overloaded and
	// server_error_5xx branches. Model_overloaded is earlier → wins.
	got := classifyError(errors.New("service unavailable"))
	if got.ErrorType != "model_overloaded" {
		t.Fatalf("ErrorType = %q, want model_overloaded (earlier branch wins)", got.ErrorType)
	}
}

func TestClassifyError_EmptyMessageIsUnknownNonRetryable(t *testing.T) {
	got := classifyError(errors.New(""))
	if got.ErrorType != "unknown_error" {
		t.Fatalf("ErrorType = %q, want unknown_error", got.ErrorType)
	}
	if got.Retryable {
		t.Fatal("empty error must fail-closed (Retryable=false)")
	}
}

func TestClassifyError_PreservesOriginalCaseInMessage(t *testing.T) {
	// Matching is lowercase but Message must preserve the original
	// bytes (callers log to user surfaces).
	orig := "Connection Timeout: Peer Unreachable"
	got := classifyError(errors.New(orig))
	if got.Message != orig {
		t.Fatalf("Message = %q, want %q (must not lowercase-store)", got.Message, orig)
	}
}

func TestShouldRetryOnError_ExactMatch_NotSubstring(t *testing.T) {
	// "timeout" must NOT match "timeout_long" or "gateway_timeout".
	// Pin exact-match semantics.
	allow := []string{"timeout_long", "gateway_timeout"}
	if shouldRetryOnError("timeout", allow) {
		t.Fatal("should be exact match only; substring match forbidden")
	}
}

func TestShouldRetryOnError_EmptyAllowListRejectsEverything(t *testing.T) {
	for _, errType := range []string{"", "timeout", "network_error", "unknown_error"} {
		if shouldRetryOnError(errType, nil) {
			t.Fatalf("empty allow-list should reject %q", errType)
		}
		if shouldRetryOnError(errType, []string{}) {
			t.Fatalf("empty allow-list should reject %q", errType)
		}
	}
}

func TestCalculateDelay_InitialExceedsMaxClampsAtAttemptZero(t *testing.T) {
	// Even attempt=0 must clamp if initial > max (defensive for
	// misconfigured callers). Pin the clamp applies regardless of attempt.
	initial := 60 * time.Second
	maxD := 5 * time.Second
	for i := 0; i < 50; i++ {
		got := calculateDelay(0, initial, maxD, 2.0)
		if got > maxD {
			t.Fatalf("attempt=0 with initial>max didn't clamp: got %v > %v", got, maxD)
		}
	}
}

func TestCalculateDelay_BackoffFactorOneIsFlat(t *testing.T) {
	// backoffFactor=1.0: math.Pow(1,n)==1 → delay stays at initial
	// for every attempt, only jitter varies.
	initial := 500 * time.Millisecond
	maxD := 10 * time.Second
	for attempt := 0; attempt < 10; attempt++ {
		for i := 0; i < 20; i++ {
			got := calculateDelay(attempt, initial, maxD, 1.0)
			if got < 450*time.Millisecond || got > 550*time.Millisecond {
				t.Fatalf("attempt=%d with factor=1.0: got %v outside [450ms,550ms]",
					attempt, got)
			}
		}
	}
}

func TestCalculateDelay_CapIsExactNoOvershoot(t *testing.T) {
	// The cap is applied AFTER jitter, so the returned value never
	// overshoots maxDelay even by the +10% jitter band. Pin so a
	// refactor that reversed the (cap, jitter) order would surface
	// (jitter-after-cap could push past max).
	initial := 2 * time.Second
	maxD := 30 * time.Second
	for i := 0; i < 200; i++ {
		got := calculateDelay(10, initial, maxD, 2.0)
		if got > maxD {
			t.Fatalf("cap overshoot: got %v > %v", got, maxD)
		}
	}
}

func TestRetryStats_LastErrorTypeSticksAcrossLaterSuccesses(t *testing.T) {
	s := NewRetryStats()
	s.RecordAttempt(false, 50*time.Millisecond, "timeout")
	s.RecordAttempt(true, 50*time.Millisecond, "")  // success must NOT wipe
	s.RecordAttempt(true, 50*time.Millisecond, "")  // another success
	if s.LastErrorType != "timeout" {
		t.Fatalf("LastErrorType = %q, want %q (success must not clobber)", s.LastErrorType, "timeout")
	}
}

func TestRetryStats_PureSuccessLeavesLastErrorEmpty(t *testing.T) {
	s := NewRetryStats()
	s.RecordAttempt(true, 10*time.Millisecond, "")
	s.RecordAttempt(true, 10*time.Millisecond, "")
	if s.LastErrorType != "" {
		t.Fatalf("pure-success LastErrorType = %q, want empty", s.LastErrorType)
	}
}

func TestRetryStats_AverageDelayDividesByTotalNotFailed(t *testing.T) {
	s := NewRetryStats()
	// 3 attempts, 1 failed. Total delay = 300ms. Average = 100ms.
	// If the denominator were FailedAttempts, average would be 300ms.
	s.RecordAttempt(true, 100*time.Millisecond, "")
	s.RecordAttempt(true, 100*time.Millisecond, "")
	s.RecordAttempt(false, 100*time.Millisecond, "timeout")
	if got := s.AverageDelay(); got != 100*time.Millisecond {
		t.Fatalf("AverageDelay = %v, want 100ms (must divide by TotalAttempts)", got)
	}
}

func TestRetryStats_TotalInvariantHolds(t *testing.T) {
	// Fuzz-style: every RecordAttempt preserves the invariant
	// Success + Failed == Total.
	s := NewRetryStats()
	pattern := []bool{true, false, true, true, false, false, true}
	for _, ok := range pattern {
		s.RecordAttempt(ok, time.Millisecond, "any")
		if s.SuccessAttempts+s.FailedAttempts != s.TotalAttempts {
			t.Fatalf("invariant violated: %d+%d != %d",
				s.SuccessAttempts, s.FailedAttempts, s.TotalAttempts)
		}
	}
}
