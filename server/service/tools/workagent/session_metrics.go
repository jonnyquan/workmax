package workagent

import (
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// SessionMetrics tracks session-related metrics.
//
// Counters use sync/atomic for hot-path increments. The "last error"
// pair (string + time.Time) lives behind lastErrMu — they used to be
// plain field reads/writes which is a data race against the
// concurrent Get/Reset paths. time.Time in particular is a struct
// with an internal monotonic-clock pointer, so a torn read can
// surface as a garbage instant in monitoring dashboards or, worse,
// a wall-clock that goes backwards.
type SessionMetrics struct {
	// Counters
	TotalAttempts    int64 // Total attempt count
	SessionNotFound  int64 // Session not found error count
	SessionRecovered int64 // Successful recovery count
	RecoveryFailed   int64 // Recovery failure count

	lastErrMu     sync.RWMutex
	lastError     string
	lastErrorTime time.Time
}

var sessionMetrics = &SessionMetrics{}

// RecordSessionAttempt records a session attempt
func RecordSessionAttempt() {
	atomic.AddInt64(&sessionMetrics.TotalAttempts, 1)
}

// RecordSessionNotFound records a session not found error.
//
// err.Error() is capped at recordFailureMaxErrLen before storage —
// upstream model-gateway errors during an outage routinely include
// multi-KB stack traces or response bodies, and lastError sits in
// process memory until the next session-not-found event (could be
// minutes during a cold spell). GetSessionMetrics exposes this
// string to the admin UI / monitoring; a bounded preview is enough
// to identify the failure mode. Same cap and shape as the DB-write
// paths in fa2141f4 (RecordFailure) and 136dff3d (recordTestFailure).
func RecordSessionNotFound(err error) {
	atomic.AddInt64(&sessionMetrics.SessionNotFound, 1)
	msg := err.Error()
	// Byte-cap with a rune-boundary walk-back, matching the production
	// RecordFailure (cb8f207c) and recordTestFailure (68e83818) fixes.
	// The previous shape mixed metrics: outer byte-length check, inner
	// rune-count trim. A 1500-byte CJK error (~500 runes at 3 bytes/rune)
	// passed the outer gate and failed the inner — no truncation, full
	// multi-KB error pinned in lastError until the next session-not-
	// found event (could be many minutes), and exposed verbatim through
	// GetSessionMetrics to the admin UI / monitoring dashboards.
	if len(msg) > recordFailureMaxErrLen {
		cut := recordFailureMaxErrLen
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "…(truncated)"
	}
	sessionMetrics.lastErrMu.Lock()
	sessionMetrics.lastError = msg
	sessionMetrics.lastErrorTime = time.Now()
	sessionMetrics.lastErrMu.Unlock()
}

// RecordSessionRecovered records a successful session recovery
func RecordSessionRecovered() {
	atomic.AddInt64(&sessionMetrics.SessionRecovered, 1)
}

// RecordRecoveryFailed records a failed recovery attempt
func RecordRecoveryFailed() {
	atomic.AddInt64(&sessionMetrics.RecoveryFailed, 1)
}

// GetSessionMetrics returns a snapshot of current metrics.
// Reads the last-error pair under RLock so it never observes a
// half-written time.Time.
func GetSessionMetrics() map[string]interface{} {
	sessionMetrics.lastErrMu.RLock()
	lastErr := sessionMetrics.lastError
	lastErrTime := sessionMetrics.lastErrorTime
	sessionMetrics.lastErrMu.RUnlock()
	return map[string]interface{}{
		"total_attempts":    atomic.LoadInt64(&sessionMetrics.TotalAttempts),
		"session_not_found": atomic.LoadInt64(&sessionMetrics.SessionNotFound),
		"session_recovered": atomic.LoadInt64(&sessionMetrics.SessionRecovered),
		"recovery_failed":   atomic.LoadInt64(&sessionMetrics.RecoveryFailed),
		"last_error":        lastErr,
		"last_error_time":   lastErrTime,
		"recovery_rate":     calculateRecoveryRate(),
	}
}

// calculateRecoveryRate calculates the recovery success rate
func calculateRecoveryRate() float64 {
	notFound := atomic.LoadInt64(&sessionMetrics.SessionNotFound)
	if notFound == 0 {
		return 100.0
	}

	recovered := atomic.LoadInt64(&sessionMetrics.SessionRecovered)
	return float64(recovered) / float64(notFound) * 100.0
}

// ResetSessionMetrics resets all metrics (useful for testing)
func ResetSessionMetrics() {
	atomic.StoreInt64(&sessionMetrics.TotalAttempts, 0)
	atomic.StoreInt64(&sessionMetrics.SessionNotFound, 0)
	atomic.StoreInt64(&sessionMetrics.SessionRecovered, 0)
	atomic.StoreInt64(&sessionMetrics.RecoveryFailed, 0)
	sessionMetrics.lastErrMu.Lock()
	sessionMetrics.lastError = ""
	sessionMetrics.lastErrorTime = time.Time{}
	sessionMetrics.lastErrMu.Unlock()
}
