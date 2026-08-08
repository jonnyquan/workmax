package workagent

import (
	"math"
	"time"
)

// agentReservationSettlementMargin keeps the financial hold alive while the
// handler persists the provider result and commits the terminal settlement.
// Generator reservations use the same five-minute policy.
const agentReservationSettlementMargin = 5 * time.Minute

// ResolveAgentExecutionTimeout converts a configured seconds value into one
// immutable timeout snapshot for the whole request. Callers must reuse the
// returned value for both their execution context and Reservation TTL so a
// config reload cannot make those clocks disagree mid-turn.
func ResolveAgentExecutionTimeout(configuredSeconds int, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = time.Second
	}
	if configuredSeconds <= 0 {
		return fallback
	}
	maxSeconds := int64(math.MaxInt64 / int64(time.Second))
	if uint64(configuredSeconds) > uint64(maxSeconds) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(configuredSeconds) * time.Second
}

// ReservationTTLForExecution adds a bounded post-execution settlement window.
// Saturation prevents a hostile or corrupted timeout value from wrapping into
// a short/negative TTL and allowing the sweeper to refund an active turn.
func ReservationTTLForExecution(executionTimeout time.Duration) time.Duration {
	if executionTimeout <= 0 {
		executionTimeout = time.Second
	}
	if executionTimeout > time.Duration(math.MaxInt64)-agentReservationSettlementMargin {
		return time.Duration(math.MaxInt64)
	}
	return executionTimeout + agentReservationSettlementMargin
}
