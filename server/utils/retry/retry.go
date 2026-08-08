// Package retry provides a single vendor-agnostic retry decorator
// for the project's outbound API calls. Kept tiny + deterministic
// so every caller (TTS, LLM, video provider, storage upload) can
// rely on the same backoff shape + retryability classifier.
//
// Design philosophy:
//   - Exponential backoff with optional full jitter (Amazon's
//     "simpler and often better" recommendation for transient-
//     failure workloads).
//   - Retryability decided by a caller-supplied predicate, with
//     DefaultIsRetryable covering the common HTTP/transport
//     signatures (429, 5xx, timeouts, connection resets). Callers
//     that need a different shape (e.g. TTS provider's JSON error
//     envelope) wrap DefaultIsRetryable with their own extras.
//   - Context-aware: the decorator stops retrying as soon as the
//     caller's context errors, and the final err the caller sees
//     is the last attempt's err (NOT ctx.Err() — that gives the
//     caller a vendor-shaped error even when the deadline was the
//     actual cause).
package retry

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"regexp"
	"strings"
	"time"
)

// Policy configures one invocation of Do. Zero-valued fields get
// sensible defaults; callers typically override MaxAttempts and
// accept the rest.
type Policy struct {
	// MaxAttempts is the total number of tries (NOT retries). 3
	// means "initial call + 2 retries on failure". 0 or 1 means
	// "run once with no retry".
	MaxAttempts int

	// BaseDelay is the first backoff. Subsequent attempts double
	// until MaxDelay. Default: 500ms.
	BaseDelay time.Duration

	// MaxDelay caps the exponential growth. Default: 30s. Set to
	// <= BaseDelay to effectively disable exponentiation.
	MaxDelay time.Duration

	// Jitter = true applies full-jitter (sleep = rand() *
	// capped_exponential). Reduces thundering-herd when many
	// workers retry the same failing target. Default: true.
	// Explicitly set to false via NoJitter option for deterministic
	// tests.
	Jitter bool

	// IsRetryable decides whether a given error gets another try.
	// nil = DefaultIsRetryable. Return false to abort immediately
	// (e.g. authentication / validation errors — no retry will
	// fix them).
	IsRetryable func(err error) bool

	// Sleep is dependency-injected so tests run in zero wall-clock
	// time. nil = time.Sleep.
	Sleep func(time.Duration)

	// Rng is dependency-injected for deterministic jitter tests.
	// nil = a time-seeded rand.Rand (fine for production).
	Rng *rand.Rand
}

// Do executes fn up to Policy.MaxAttempts times, sleeping
// exponentially between failures that pass IsRetryable. Returns
// the last error seen — which is either the last attempt's error
// (success=nil) or the first non-retryable error encountered.
//
// ctx is honoured between attempts: if ctx.Done() fires during a
// backoff sleep, Do returns the last underlying error (preserves
// the vendor-shaped failure the caller wanted to see).
func Do(ctx context.Context, p Policy, fn func() error) error {
	p = applyDefaults(p)

	var lastErr error
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			// Caller cancelled before we even started this attempt.
			// Return the last real error if we have one, otherwise
			// the cancellation itself.
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Classifier: unrecoverable → abort now. Retry budget
		// exhausted → let the loop exit naturally and return err.
		if !p.IsRetryable(err) {
			return err
		}
		if attempt == p.MaxAttempts {
			return err
		}

		// Exponential backoff with optional full jitter. cap at
		// MaxDelay so unbounded exponential doesn't push the next
		// sleep past any reasonable timeout.
		sleep := backoffFor(attempt, p.BaseDelay, p.MaxDelay)
		if p.Jitter {
			sleep = jitterFullSpread(p.Rng, sleep)
		}

		// Interruptible sleep. A ctx cancellation during the sleep
		// doesn't retroactively undo the last attempt — we just
		// stop waiting and return the existing lastErr.
		if !interruptibleSleep(ctx, sleep, p.Sleep) {
			return lastErr
		}
	}
	return lastErr
}

// backoffFor returns the nominal (pre-jitter) backoff for the Nth
// failed attempt. Attempt numbering starts at 1 — the FIRST sleep
// (between attempt 1's failure and attempt 2's start) uses base.
func backoffFor(attempt int, base, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	// 2^(attempt-1) as float so we don't overflow on ridiculous
	// attempt counts; capped by max anyway.
	mul := math.Pow(2, float64(attempt-1))
	d := time.Duration(float64(base) * mul)
	if d <= 0 || d > max {
		return max
	}
	return d
}

// jitterFullSpread picks a random value in [0, capped]. Full
// jitter is what AWS's guidance recommends for high-concurrency
// retry storms.
func jitterFullSpread(rng *rand.Rand, capped time.Duration) time.Duration {
	if capped <= 0 {
		return 0
	}
	return time.Duration(rng.Int63n(int64(capped) + 1))
}

// interruptibleSleep waits d, returning false if ctx cancels
// first. Uses the policy's Sleep function ONLY when the context
// has no deadline / is not done — otherwise we use select+time.After
// so ctx cancellation can wake us. Keeping both paths means tests
// can inject a zero-wall-clock Sleep for deterministic runs.
func interruptibleSleep(ctx context.Context, d time.Duration, sleep func(time.Duration)) bool {
	if d <= 0 {
		return true
	}
	if ctx.Done() == nil {
		sleep(d)
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// applyDefaults fills in the Policy's zero-valued fields.
func applyDefaults(p Policy) Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 1
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 500 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 30 * time.Second
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	if p.IsRetryable == nil {
		p.IsRetryable = DefaultIsRetryable
	}
	if p.Sleep == nil {
		p.Sleep = time.Sleep
	}
	if p.Rng == nil {
		p.Rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	// Jitter defaults to true — most callers want it. Explicit
	// false needs NoJitter (a Policy builder helper below).
	return p
}

// NoJitter returns p with Jitter explicitly disabled. Used by
// tests that want deterministic sleep timings.
func NoJitter(p Policy) Policy {
	p.Jitter = false
	return p
}

// --- Retryability classifier ---

// DefaultIsRetryable covers the common "transient failure" shapes
// we see across vendors:
//   - HTTP 429 (rate limit)
//   - HTTP 5xx (upstream error)
//   - Timeouts, deadline exceeded, context deadline exceeded
//   - TCP connection reset / broken pipe / socket hang up
//   - DNS failures that usually self-resolve
//
// Matches the ERROR TEXT, not a typed hierarchy, because the vendor
// SDKs return a zoo of incompatible error types. Regex matching is
// a reliable lowest-common-denominator.
//
// Authentication, validation, malformed-request, quota-exhausted,
// and any non-5xx 4xx NOT listed here are classified as
// non-retryable — no number of retries will fix a bad API key.
func DefaultIsRetryable(err error) bool {
	if err == nil {
		return false
	}
	// context.Canceled is NOT retryable — the caller explicitly
	// decided to stop. Deadline exceeded at the top-level ctx is
	// also non-retryable because a fresh attempt would hit the same
	// already-expired deadline.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return retryableSignature.MatchString(err.Error())
}

// retryableSignature is the single regex that classifies errors by
// their rendered text. Add new patterns here as new vendors surface
// retryable shapes we haven't seen before. Case-insensitive.
var retryableSignature = regexp.MustCompile(
	`(?i)` + strings.Join([]string{
		`\b429\b`,                          // HTTP 429
		`\b5\d\d\b`,                        // HTTP 5xx
		`rate.?limit`,                      // rate_limit_exceeded, rate-limited
		`too.?many.?requests`,              // wordy variants
		`timeout`,                          // generic timeout
		`timed.?out`,                       // wordy timeout
		`deadline.?exceeded`,               // gRPC deadline (not context.DeadlineExceeded — that's caught above)
		`connection.?reset`,                // ECONNRESET
		`broken.?pipe`,                     // EPIPE
		`connection.?refused`,              // ECONNREFUSED (often transient during deploys)
		`socket.?hang.?up`,                 // axios-style node.js errors
		`EOF`,                              // early connection close
		`i/o.?timeout`,                     // Go net package's net.OpError text
		`temporary.?failure.?in.?name`,     // DNS: "Temporary failure in name resolution"
		`no.?such.?host`,                   // transient DNS
		`service.?unavailable`,             // 503 wording
		`bad.?gateway`,                     // 502 wording
		`gateway.?time`,                    // 504 wording
	}, `|`),
)

// Combine returns an IsRetryable that returns true when ANY of the
// given predicates does. Use when a caller wants the default
// classifier PLUS vendor-specific extras (e.g. a TTS provider
// that signals transient failure via a JSON error.type field).
func Combine(preds ...func(error) bool) func(error) bool {
	return func(err error) bool {
		for _, p := range preds {
			if p(err) {
				return true
			}
		}
		return false
	}
}
