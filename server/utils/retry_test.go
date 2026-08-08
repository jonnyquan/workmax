package utils

import (
	"errors"
	"testing"
	"time"
)

// retry.go is the shared retry/backoff/classification helper used by
// workagent, canvasagent, the provider layer and error_handler middleware
// (6 callsites). A silent drift in classifyError's keyword families or in
// calculateDelay's cap would quietly change retry behaviour for every
// agent submission and every LLM provider call, so the branches are
// pinned here rather than re-derived from the constants they test.

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		wantType     string
		wantRetry    bool
		caseVariants bool // also verify case-insensitive match
	}{
		// Keyword order matters: "service unavailable" appears in BOTH the
		// model_overloaded branch and the server_error_5xx branch. The
		// first-match wins rule pins it to model_overloaded.
		{"service unavailable goes to model_overloaded (first match)", errors.New("Service Unavailable"), "model_overloaded", true, true},

		{"connection refused → network_error", errors.New("connection refused by peer"), "network_error", true, true},
		{"connection reset → network_error", errors.New("connection reset"), "network_error", true, false},
		{"connection timeout → network_error (matches network branch first)", errors.New("connection timeout"), "network_error", true, false},
		{"bare network keyword → network_error", errors.New("network is unreachable"), "network_error", true, false},

		{"rate limit → rate_limit_exceeded", errors.New("rate limit hit"), "rate_limit_exceeded", true, true},
		{"too many requests → rate_limit_exceeded", errors.New("too many requests"), "rate_limit_exceeded", true, false},
		{"quota exceeded → rate_limit_exceeded", errors.New("quota exceeded for today"), "rate_limit_exceeded", true, false},

		{"model overloaded → model_overloaded", errors.New("Model Overloaded"), "model_overloaded", true, true},
		{"temporarily unavailable → model_overloaded", errors.New("temporarily unavailable"), "model_overloaded", true, false},

		{"timeout → timeout", errors.New("request timeout"), "timeout", true, true},
		{"deadline exceeded → timeout", errors.New("context deadline exceeded"), "timeout", true, false},

		{"internal server error → server_error_5xx", errors.New("Internal Server Error"), "server_error_5xx", true, true},
		{"bad gateway → server_error_5xx", errors.New("bad gateway"), "server_error_5xx", true, false},
		{"gateway timeout → timeout (timeout branch wins)", errors.New("gateway timeout"), "timeout", true, false},

		// 4xx client errors are non-retryable — this is the contract that
		// prevents workagent from looping on bad-request or auth failures.
		{"bad request → client_error_4xx, non-retryable", errors.New("bad request: missing field"), "client_error_4xx", false, true},
		{"unauthorized → client_error_4xx, non-retryable", errors.New("unauthorized"), "client_error_4xx", false, false},
		{"forbidden → client_error_4xx, non-retryable", errors.New("forbidden"), "client_error_4xx", false, false},
		{"not found → client_error_4xx, non-retryable", errors.New("not found"), "client_error_4xx", false, false},

		// Unknown defaults to non-retryable — fail-closed rather than loop forever.
		{"unknown → unknown_error, non-retryable", errors.New("something completely new"), "unknown_error", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyError(tc.err)
			if got.ErrorType != tc.wantType {
				t.Fatalf("ErrorType = %q, want %q", got.ErrorType, tc.wantType)
			}
			if got.Retryable != tc.wantRetry {
				t.Fatalf("Retryable = %v, want %v", got.Retryable, tc.wantRetry)
			}
			if got.Message != tc.err.Error() {
				t.Fatalf("Message not preserved: got %q, want %q", got.Message, tc.err.Error())
			}

			// Case-insensitive matching is load-bearing because upstream
			// providers return mixed-case messages. If strings.ToLower
			// were ever removed from classifyError, this would fail.
			if tc.caseVariants {
				upper := errors.New(upperCase(tc.err.Error()))
				g2 := classifyError(upper)
				if g2.ErrorType != tc.wantType {
					t.Fatalf("uppercase variant: ErrorType = %q, want %q", g2.ErrorType, tc.wantType)
				}
			}
		})
	}
}

// upperCase avoids importing strings just for a helper — retry.go already
// pulls in strings.ToLower, and the test package reuses that via classifyError.
func upperCase(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}

func TestShouldRetryOnError(t *testing.T) {
	// Whitelist semantics: only errorTypes listed in retryOnErrors
	// trigger a retry. A classification can be Retryable:true but still
	// skipped if the caller's config didn't opt into that type.
	allow := []string{"network_error", "timeout"}

	if !shouldRetryOnError("timeout", allow) {
		t.Fatalf("timeout in allow-list should retry")
	}
	if !shouldRetryOnError("network_error", allow) {
		t.Fatalf("network_error in allow-list should retry")
	}
	if shouldRetryOnError("rate_limit_exceeded", allow) {
		t.Fatalf("rate_limit_exceeded NOT in allow-list, must not retry")
	}
	if shouldRetryOnError("network_error", nil) {
		t.Fatalf("empty allow-list must reject")
	}
}

// calculateDelay applies exponential backoff with ±10% jitter and a hard cap.
// Without the cap, attempt=10 with 2× backoff would blow into ~17 minutes;
// with jitter but no cap, a single jittered value could still overshoot.
// Pin: every returned delay stays within [0, maxDelay + 10% of uncapped delay].
func TestCalculateDelay_RespectsMaxCap(t *testing.T) {
	initial := 2 * time.Second
	max := 30 * time.Second

	// Attempt 10 → uncapped base is 2s * 2^10 = ~34 minutes, so the cap must
	// engage. Run many times to cover jitter variance.
	for i := 0; i < 100; i++ {
		got := calculateDelay(10, initial, max, 2.0)
		if got > max {
			t.Fatalf("attempt=10: delay %v exceeded cap %v", got, max)
		}
		if got < 0 {
			t.Fatalf("attempt=10: delay %v must not be negative", got)
		}
	}
}

// For small attempts the cap does not engage, so the delay should fall
// near the computed base ± 10%. This pins the exponential schedule itself.
func TestCalculateDelay_ExponentialShape(t *testing.T) {
	initial := 100 * time.Millisecond
	max := 10 * time.Second

	// attempt=0: base = 100ms, jitter range ±10ms → [90ms, 110ms]
	for i := 0; i < 50; i++ {
		got := calculateDelay(0, initial, max, 2.0)
		if got < 90*time.Millisecond || got > 110*time.Millisecond {
			t.Fatalf("attempt=0: delay %v outside [90ms, 110ms]", got)
		}
	}
	// attempt=3: base = 100ms * 2^3 = 800ms, jitter ±80ms → [720ms, 880ms]
	for i := 0; i < 50; i++ {
		got := calculateDelay(3, initial, max, 2.0)
		if got < 720*time.Millisecond || got > 880*time.Millisecond {
			t.Fatalf("attempt=3: delay %v outside [720ms, 880ms]", got)
		}
	}
}

func TestRetryStats_SuccessRateMath(t *testing.T) {
	s := NewRetryStats()

	// Empty stats → 0, not NaN. A division-by-zero bug here would poison
	// any telemetry consumer doing arithmetic on the rate.
	if got := s.SuccessRate(); got != 0 {
		t.Fatalf("empty SuccessRate = %v, want 0", got)
	}
	if got := s.AverageDelay(); got != 0 {
		t.Fatalf("empty AverageDelay = %v, want 0", got)
	}

	s.RecordAttempt(true, 100*time.Millisecond, "")
	s.RecordAttempt(false, 200*time.Millisecond, "timeout")
	s.RecordAttempt(false, 300*time.Millisecond, "network_error")
	s.RecordAttempt(true, 400*time.Millisecond, "")

	if s.TotalAttempts != 4 {
		t.Fatalf("TotalAttempts = %d, want 4", s.TotalAttempts)
	}
	if s.SuccessAttempts != 2 {
		t.Fatalf("SuccessAttempts = %d, want 2", s.SuccessAttempts)
	}
	if s.FailedAttempts != 2 {
		t.Fatalf("FailedAttempts = %d, want 2", s.FailedAttempts)
	}
	// LastErrorType is only set on failures; success must not clobber it.
	if s.LastErrorType != "network_error" {
		t.Fatalf("LastErrorType = %q, want %q (last failure wins, successes don't clobber)", s.LastErrorType, "network_error")
	}
	if got := s.SuccessRate(); got != 50.0 {
		t.Fatalf("SuccessRate = %v, want 50.0", got)
	}
	// (100+200+300+400)/4 = 250ms
	if got := s.AverageDelay(); got != 250*time.Millisecond {
		t.Fatalf("AverageDelay = %v, want 250ms", got)
	}
}
