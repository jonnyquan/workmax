package provider

import (
	"context"
	"fmt"
	"server/globals"
	"time"
)

// pollMaxAttempts caps the number of consecutive transient-failure
// retries on a single poll round before bubbling the last error.
//
// Three is empirical: most hiccups (DNS blip, single TCP reset, brief
// 502 on a CDN edge, momentary auth-proxy 401 during key rotation)
// clear within 1–2 attempts. If it's still failing on attempt 3,
// something structural is wrong (gateway down, key revoked) and waiting
// longer in this round won't help — the OUTER poll loop's next tick
// will try again anyway, so a persistent transient error doesn't kill
// the task immediately.
const pollMaxAttempts = 3

// pollBackoffStep grows the wait between inner retries linearly:
// 300ms, 600ms, 900ms. Linear (not exponential) because we want a
// quick recovery on transient blips; exponential backoff is what the
// outer poll-tick interval already provides for sustained issues.
const pollBackoffStep = 300 * time.Millisecond

// PollWithInnerRetry retries `fn` on transient failures with linear
// backoff. Returns the first successful result, or the last error if
// all attempts fail.
//
// Use this around any single status-query call that talks to an
// upstream that has *already* accepted a paid job: the helper exists
// because a flaky GET on /predictions/{id}, /v1/videos/{id}, etc. must
// not abandon the in-flight job. Status reads are free; we should
// retry them aggressively before letting the outer poll loop give up.
//
// The label is used in log lines so a single grep can join inner-retry
// events with the rest of the provider's lifecycle.
//
// Caller invariants:
//   - `fn` must be idempotent (it's just reading status).
//   - `fn` must respect the context — pass it through to its HTTP client.
//   - Any error returned by `fn` is treated as transient. Providers
//     that want to distinguish "transient" from "fatal" should classify
//     before returning to the helper (e.g. wrap fatals in a sentinel
//     and bail manually).
func PollWithInnerRetry[T any](ctx context.Context, label string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= pollMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result, err := fn(ctx)
		if err == nil {
			if attempt > 1 {
				globals.Info(fmt.Sprintf("[%s] poll recovered on attempt %d", label, attempt))
			}
			return result, nil
		}
		lastErr = err
		if attempt < pollMaxAttempts {
			backoff := time.Duration(attempt) * pollBackoffStep
			globals.Warn(fmt.Sprintf("[%s] poll attempt %d/%d failed (%v), retrying in %v", label, attempt, pollMaxAttempts, err, backoff))
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return zero, lastErr
}
