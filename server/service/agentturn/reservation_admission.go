package agentturn

import (
	"context"
	"errors"
	"reflect"

	"gorm.io/gorm"
)

var (
	// ErrTurnReservationAdmissionAuthorityUnavailable means a caller selected
	// commercial admission without providing a transaction-local authority, or
	// the configured Store cannot expose that stronger admission boundary.
	ErrTurnReservationAdmissionAuthorityUnavailable = errors.New(
		"durable turn reservation admission authority is unavailable",
	)
	// ErrTurnReservationAdmissionFailed is deliberately opaque.
	// ReserveAndBindTurn runs while the Turn row exists inside an uncommitted
	// transaction; exposing an implementation error could leak ledger or
	// database details.
	ErrTurnReservationAdmissionFailed = errors.New("durable turn reservation admission failed")
	// ErrTurnReservationBindingInvalid means an idempotent admission winner
	// exists, but its exact commercial binding could not be proven under lock.
	ErrTurnReservationBindingInvalid = errors.New("durable turn reservation binding is invalid")
	// ErrTurnReservationExecutionUnauthorized is deliberately opaque. It means
	// the installed commercial authority could not prove that a fresh execution
	// epoch may consume the Turn's reservation.
	ErrTurnReservationExecutionUnauthorized = errors.New(
		"durable turn reservation does not authorize execution",
	)
	// ErrTurnReservationExecutionExpired is the one non-opaque execution-gate
	// outcome. It may be returned only after the installed commercial authority
	// proves the exact Turn -> Binding -> Reservation tuple and observes that
	// exact reserved hold at or beyond its database-clock expiry. ClaimNext may
	// skip this candidate so one expired hold cannot hide healthy work behind it;
	// every other denial remains ExecutionUnauthorized and stops discovery.
	ErrTurnReservationExecutionExpired = errors.New(
		"durable turn reservation execution authorization expired",
	)
)

// TurnReservationAdmissionAuthority is the opt-in commercial admission
// boundary. Implementations are request-scoped: they may carry trusted UID,
// quote, pricing and reservation inputs without adding those fields to the
// execution kernel's Turn model.
//
// Both methods run inside SQLStore's admission transaction and must use tx for
// every read and write. They must not commit, roll back or perform network I/O.
// ReserveAndBindTurn is called only after the new Turn row exists and before
// its first Event is inserted. VerifyTurnBinding is called for an exact
// idempotent winner while that winner is locked; it must fail closed on a
// missing or conflicting binding.
type TurnReservationAdmissionAuthority interface {
	ReserveAndBindTurn(tx *gorm.DB, turn Turn) error
	VerifyTurnBinding(tx *gorm.DB, turn Turn) error
}

// TurnReservationExecutionAuthority is an optional capability of the Store's
// installed SettlementAuthority. It runs under the Turn lock before a fresh
// Attempt or reclaim mutates lifecycle state. Exact Attempt replay never calls
// it because replay resumes an epoch that was already authorized.
//
// Implementations must use tx for every read, must not commit or roll back the
// transaction, and must not perform network I/O.
type TurnReservationExecutionAuthority interface {
	AuthorizeTurnExecution(tx *gorm.DB, turn Turn) error
}

// TurnReservationExpiryAuthority is the opt-in reconciliation capability for
// an Agent-bound Reservation whose TTL elapsed before a new execution epoch
// could begin. It runs after the kernel has locked the Turn and, for a running
// Turn, its expired active Attempt. Implementations must prove the immutable
// commercial owner tuple under the same transaction. A live Attempt is never
// presented: Reservation TTL revokes only a fresh epoch, not one that was
// already authorized when claimed.
type TurnReservationExpiryAuthority interface {
	VerifyExpiredTurnReservation(tx *gorm.DB, turn Turn) error
}

// TurnReservationAdmissionStore is intentionally separate from Store so the
// ordinary Admit/Start contract remains source- and behavior-compatible.
type TurnReservationAdmissionStore interface {
	AdmitWithReservationAuthority(
		ctx context.Context,
		candidate Turn,
		initial EventDraft,
		authority TurnReservationAdmissionAuthority,
	) (AdmissionRecord, error)
}

var _ TurnReservationAdmissionStore = (*SQLStore)(nil)

func turnReservationAdmissionAuthorityMissing(authority TurnReservationAdmissionAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func turnReservationExecutionAuthorityMissing(authority TurnReservationExecutionAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (store *SQLStore) authorizeTurnReservationExecution(tx *gorm.DB, turn Turn) error {
	if tx == nil {
		return ErrStoreIntegrity
	}
	settlement, intact := store.settlementAuthority()
	authority, capable := settlement.(TurnReservationExecutionAuthority)
	if !capable || turnReservationExecutionAuthorityMissing(authority) {
		// Nil and legacy SettlementAuthority implementations retain their existing
		// claim behavior. Installing the stronger capability is the opt-in gate.
		return nil
	}
	if !intact {
		return ErrSettlementBindingInvalid
	}
	if err := authority.AuthorizeTurnExecution(tx, cloneTurn(turn)); err != nil {
		if errors.Is(err, ErrTurnReservationExecutionExpired) {
			return ErrTurnReservationExecutionExpired
		}
		return ErrTurnReservationExecutionUnauthorized
	}
	return nil
}

func (store *SQLStore) verifyExpiredTurnReservation(tx *gorm.DB, turn Turn) error {
	if tx == nil {
		return ErrStoreIntegrity
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	return store.verifyExpiredTurnReservationLocked(tx, turn)
}

// verifyExpiredTurnReservationLocked is used by Provider-aware reconciliation,
// which already retains settlementMu.RLock through commit. Taking a nested
// RLock there could self-deadlock when a compatibility writer is waiting.
func (store *SQLStore) verifyExpiredTurnReservationLocked(tx *gorm.DB, turn Turn) error {
	if tx == nil {
		return ErrStoreIntegrity
	}
	settlement, intact := store.settlement, !store.settlementViolated
	authority, capable := settlement.(TurnReservationExpiryAuthority)
	if !capable || turnReservationExpiryAuthorityMissing(authority) {
		return ErrReconcilePrecondition
	}
	if !intact {
		return ErrSettlementBindingInvalid
	}
	return authority.VerifyExpiredTurnReservation(tx, cloneTurn(turn))
}

func turnReservationExpiryAuthorityMissing(authority TurnReservationExpiryAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// MatchesTurnReservationExpiryAuthority proves that expected is the exact
// capability currently installed as this Store's settlement authority and
// expectedDB is the exact GORM handle from which discovery will run. It is
// intentionally narrow: commercial scanners use it to prevent discovering on
// one database/ledger and reconciling same-shaped IDs through another Store.
func (store *SQLStore) MatchesTurnReservationExpiryAuthority(
	expected TurnReservationExpiryAuthority,
	expectedDB *gorm.DB,
) bool {
	if store == nil || expectedDB == nil || store.db != expectedDB ||
		turnReservationExpiryAuthorityMissing(expected) {
		return false
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	if store.settlementViolated {
		return false
	}
	installed, ok := store.settlement.(TurnReservationExpiryAuthority)
	return ok && sameTurnReservationExpiryAuthority(installed, expected)
}

func sameTurnReservationExpiryAuthority(
	left TurnReservationExpiryAuthority,
	right TurnReservationExpiryAuthority,
) bool {
	if turnReservationExpiryAuthorityMissing(left) || turnReservationExpiryAuthorityMissing(right) {
		return false
	}
	leftValue, rightValue := reflect.ValueOf(left), reflect.ValueOf(right)
	if leftValue.Type() != rightValue.Type() {
		return false
	}
	switch leftValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return leftValue.Comparable() && leftValue.Interface() == rightValue.Interface()
	}
}
