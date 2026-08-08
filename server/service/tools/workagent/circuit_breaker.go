package workagent

import (
	"fmt"
	"sync"
	"time"

	"server/globals"
)

// CircuitBreaker tracks the failure count for one account so the pool
// can stop routing traffic to a model gateway that's currently broken.
//
// State machine:
//
//	closed → open       on N consecutive failures
//	open → half-open    after recoveryTimeout elapses (probe allowed)
//	half-open → closed  on a successful probe
//	half-open → open    on a failed probe (handled by RecordFailure
//	                    bumping the counter back over threshold)
//
// All state transitions are logged so the pool's failover decisions
// stay diagnosable in production.
type CircuitBreaker struct {
	mu                  sync.RWMutex
	accountID           uint64
	consecutiveFailures int
	lastFailureTime     time.Time
	halfOpenSince       time.Time
	state               string // "closed", "open", "half-open"
	failureThreshold    int
	recoveryTimeout     time.Duration
	halfOpenProbeTTL    time.Duration
}

// NewCircuitBreaker constructs a breaker for accountID with the
// production thresholds: trip after 5 consecutive failures, probe
// again after 5 minutes. halfOpenProbeTTL caps how long a single
// probe is allowed to stay in flight before we treat it as failed
// and reopen — without it, a probe whose result never reports back
// (network partition, caller crash before RecordSuccess/Failure)
// pins the breaker in half-open forever and the pool can never
// route to this account again.
func NewCircuitBreaker(accountID uint64) *CircuitBreaker {
	return &CircuitBreaker{
		accountID:        accountID,
		failureThreshold: 5,
		recoveryTimeout:  5 * time.Minute,
		halfOpenProbeTTL: 5 * time.Minute,
		state:            "closed",
	}
}

// RecordFailure increments the failure counter and trips the breaker
// to "open" once the threshold is crossed.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures++
	cb.lastFailureTime = time.Now()

	if cb.consecutiveFailures >= cb.failureThreshold && cb.state != "open" {
		cb.state = "open"
		cb.halfOpenSince = time.Time{}
		globals.Warn(fmt.Sprintf("[CircuitBreaker] Account %d opened (failures: %d)",
			cb.accountID, cb.consecutiveFailures))
	}
}

// RecordSuccess resets the failure counter and closes the breaker.
// A success during the half-open probe is what closes the breaker
// fully.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.consecutiveFailures = 0
	if cb.state != "closed" {
		oldState := cb.state
		cb.state = "closed"
		cb.halfOpenSince = time.Time{}
		globals.Info(fmt.Sprintf("[CircuitBreaker] Account %d closed (from %s)", cb.accountID, oldState))
	}
}

// CanAttempt reports whether the pool should route a request to this
// account. Side effect: an "open" breaker past its recovery timeout
// transitions to "half-open" here so the next request acts as the
// probe. Subsequent CanAttempt calls in half-open return false until
// the probe resolves — without that, every concurrent caller during
// recovery would be granted a probe slot, defeating the half-open
// "single probe" semantic and slamming a still-broken upstream with
// a thundering herd.
//
// **Iteration contract**: every existing caller iterates accounts and
// short-circuits on the first CanAttempt=true (see
// agent_account_pool.go:GetAccountForModelTier and
// agent_account_pool_switch.go:selectBestAvailableAccount). That
// guarantees at most ONE breaker per call transitions open→half-open,
// matching the "one probe in flight" semantic. If a future caller
// needs to scan state without potentially flipping it (admin UI,
// metrics), use GetState() — it's read-only and ignores the recovery
// timeout. Don't add a non-short-circuiting iteration that calls
// CanAttempt.
func (cb *CircuitBreaker) CanAttempt() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case "closed":
		return true
	case "open":
		if time.Since(cb.lastFailureTime) >= cb.recoveryTimeout {
			cb.state = "half-open"
			cb.halfOpenSince = time.Now()
			globals.Info(fmt.Sprintf("[CircuitBreaker] Account %d entering half-open state", cb.accountID))
			return true
		}
		return false
	case "half-open":
		// Probe already in flight — one of RecordSuccess /
		// RecordFailure will resolve it back to closed/open and
		// release the gate. If the probe never resolves (caller
		// crashed before reporting, upstream hung past timeout),
		// halfOpenProbeTTL forces us back to open with a fresh
		// recovery window so the breaker isn't pinned forever.
		if !cb.halfOpenSince.IsZero() && time.Since(cb.halfOpenSince) >= cb.halfOpenProbeTTL {
			cb.state = "open"
			cb.lastFailureTime = time.Now()
			cb.halfOpenSince = time.Time{}
			globals.Warn(fmt.Sprintf("[CircuitBreaker] Account %d probe TTL expired (>%s), reopening", cb.accountID, cb.halfOpenProbeTTL))
		}
		return false
	}
	return false
}

// GetState returns the current breaker state ("closed" / "open" /
// "half-open") for monitoring + admin UI display.
func (cb *CircuitBreaker) GetState() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// ConsecutiveFailures returns the current run of failures since the
// last success. Used by the pool's failure handler to decide whether
// to trip auto-switch — reading this is O(1) and lock-free at the SQL
// layer, unlike the previous Pluck against w_workagent_account that
// fired on every error path during a model outage.
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.consecutiveFailures
}
