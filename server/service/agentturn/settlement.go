package agentturn

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

var (
	ErrSettlementFailed                      = errors.New("durable turn settlement failed")
	ErrSettlementReviewFailed                = errors.New("durable turn settlement review hold failed")
	ErrSettlementBindingInvalid              = errors.New("durable turn settlement authority binding is invalid")
	ErrSettlementReviewNotFound              = errors.New("durable turn settlement review was not found")
	ErrSettlementReviewResolutionUnavailable = errors.New(
		"durable turn settlement review resolution authority is unavailable",
	)
	ErrSettlementReviewResolutionConflict = errors.New(
		"durable turn settlement review resolution conflicts with the durable receipt",
	)
	ErrSettlementReviewResolutionFailed = errors.New(
		"durable turn settlement review resolution failed",
	)
	ErrSettlementReviewUsageUnavailable = errors.New(
		"durable turn settlement review usage authority is unavailable",
	)
	ErrSettlementReviewUsageConflict = errors.New(
		"durable turn settlement review usage conflicts with the durable evidence",
	)
	ErrSettlementReviewUsageFailed = errors.New(
		"durable turn settlement review usage measurement failed",
	)
	ErrSettlementReviewUsagePending = errors.New(
		"durable turn settlement review is awaiting provider usage receipts",
	)
	ErrSettlementReviewUsageOverflow = errors.New(
		"durable turn settlement review has more provider usage receipts than one evidence can bind",
	)
	ErrSettlementCompletedUsageUntrusted = errors.New(
		"durable turn completed settlement requires trusted usage evidence",
	)
	ErrSettlementReviewUnitsExceedReserved = errors.New(
		"durable turn settlement review used units exceed the reservation",
	)
	// ErrSettlementUsageUnknown prevents a release from becoming a commercial
	// assertion that no billable work happened when durable Operation/Effect
	// evidence says otherwise. It is returned before lifecycle mutation when the
	// installed authority cannot open a durable review hold.
	ErrSettlementUsageUnknown = errors.New("durable turn settlement usage is unknown")
)

type SettlementIntent string

const (
	// SettlementIntentFinalize charges the reservation for what was used.
	SettlementIntentFinalize SettlementIntent = "finalize"
	// SettlementIntentRelease returns the whole reservation.
	SettlementIntentRelease SettlementIntent = "release"
)

func (intent SettlementIntent) Valid() bool {
	return intent == SettlementIntentFinalize || intent == SettlementIntentRelease
}

// SettlementAuthorizationKind identifies the immutable kernel receipt that
// authorized a terminal commercial decision. It carries no Reservation or
// pricing data; the external ledger uses it only to bind its outcome back to
// the exact Operation or reconciliation fence.
type SettlementAuthorizationKind string

const (
	SettlementAuthorizationOperation SettlementAuthorizationKind = "operation"
	SettlementAuthorizationReconcile SettlementAuthorizationKind = "reconcile"
)

func (kind SettlementAuthorizationKind) Valid() bool {
	return kind == SettlementAuthorizationOperation || kind == SettlementAuthorizationReconcile
}

// DefaultSettlementIntent is the kernel's policy when a caller states none:
// only a Turn that completed is charged. A crash, timeout or cancellation may
// release the reservation only when the kernel has no durable evidence of
// partial work. When a SettlementAuthority is installed, CommitAttempt and
// ReconcileTerminal either place an ambiguous release under a durable Review
// hold or reject it before mutation instead of producing free output.
func DefaultSettlementIntent(status agentv1.TurnStatus) SettlementIntent {
	if status == agentv1.TurnStatusCompleted {
		return SettlementIntentFinalize
	}
	return SettlementIntentRelease
}

// SettlementCommand is what the kernel asks the commercial authority to do.
//
// The kernel deliberately carries no reservation identity, ledger reference or
// pricing. Resolving a Turn to its reservation is the authority's job, which
// keeps billing schema out of the execution kernel and stops a Turn's terminal
// path from becoming a second opinion about money. The authorization tuple is
// kernel-owned execution evidence, not commercial identity.
type SettlementCommand struct {
	TurnID      agentv1.TurnID
	PrincipalID PrincipalID
	// SettlementKey is stable, unique and derived from the immutable Operation
	// receipt that authorised this settlement. It is the exactly-once anchor.
	SettlementKey     string
	AuthorizationKind SettlementAuthorizationKind
	AttemptID         string
	FencingToken      agentv1.Sequence
	OperationID       string
	Intent            SettlementIntent
	TerminalStatus    agentv1.TurnStatus
	// UsedUnits is metered usage for a finalize. It is ignored for a release.
	UsedUnits int64
}

func (command SettlementCommand) Validate() error {
	if err := validatePathSegment("turnId", string(command.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validateBoundedText("principalId", string(command.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("settlementKey", command.SettlementKey, MaxSettlementKeyBytes); err != nil {
		return err
	}
	if !command.AuthorizationKind.Valid() || command.FencingToken < 1 || command.FencingToken > MaxDurableSequence {
		return fmt.Errorf("settlement authorization is invalid")
	}
	switch command.AuthorizationKind {
	case SettlementAuthorizationOperation:
		if err := validatePrintableASCII("attemptId", command.AttemptID, MaxAttemptIDBytes); err != nil {
			return err
		}
		if err := validatePrintableASCII("operationId", command.OperationID, MaxOperationIDBytes); err != nil {
			return err
		}
		if command.SettlementKey != settlementKey(command.TurnID, command.OperationID) {
			return fmt.Errorf("settlement key does not match operation authorization")
		}
	case SettlementAuthorizationReconcile:
		if command.AttemptID != "" || command.OperationID != "" ||
			command.SettlementKey != reconcileSettlementKey(command.TurnID, command.FencingToken) {
			return fmt.Errorf("settlement key does not match reconciliation authorization")
		}
	}
	if !command.Intent.Valid() {
		return fmt.Errorf("unknown settlement intent %q", command.Intent)
	}
	if !command.TerminalStatus.Valid() || !command.TerminalStatus.Terminal() {
		return fmt.Errorf("settlement requires a terminal turn status, got %q", command.TerminalStatus)
	}
	if command.UsedUnits < 0 {
		return fmt.Errorf("usedUnits must not be negative")
	}
	if command.Intent == SettlementIntentRelease && command.UsedUnits != 0 {
		return fmt.Errorf("a released reservation cannot report used units")
	}
	return nil
}

const MaxSettlementKeyBytes = 256

// SettlementAuthority is the commercial boundary.
//
// It runs inside the kernel's terminal transaction, which is what makes
// settlement exactly-once: the Turn's terminal state, its immutable Operation
// receipt and the commercial outcome commit or roll back together. There is no
// window in which a Turn is finished but unbilled, and no second process that
// could bill it twice.
//
// Implementations must still be idempotent on SettlementKey. The kernel
// guarantees at most one *committed* call per key, but a caller resolving an
// unknown commit outcome may present the same key again.
//
// An implementation must not perform network I/O: it holds a database
// transaction that is also holding a Turn row lock.
type SettlementAuthority interface {
	Settle(tx *gorm.DB, command SettlementCommand) error
}

// SettlementRequest is the optional commercial part of a terminal commit.
type SettlementRequest struct {
	// Intent overrides DefaultSettlementIntent. Empty selects the default.
	Intent SettlementIntent
	// UsedUnits is metered usage for a finalize.
	UsedUnits int64
}

func (request SettlementRequest) resolve(status agentv1.TurnStatus) SettlementIntent {
	if request.Intent.Valid() {
		return request.Intent
	}
	return DefaultSettlementIntent(status)
}

// settlementKey binds a settlement to the exact Operation receipt that
// authorised it. The receipt is unique per (turn, operation) and immutable, so
// a retried terminal commit resolves from the receipt and never reaches
// settlement a second time.
//
// The key is a digest rather than a concatenation: Turn and Operation IDs are
// caller-supplied, so joining them with any separator risks two different
// pairs producing the same key. A domain tag keeps an executor's settlement
// and an operator's retirement in separate key spaces even for the same Turn.
func settlementKey(turnID agentv1.TurnID, operationID string) string {
	return settlementKeyDigest("operation", string(turnID), operationID)
}

// reconcileSettlementKey distinguishes an operator retirement from an
// executor's terminal commit. A reconciled Turn has no Operation receipt,
// because nothing ran; the retiring fence is its unique authorisation.
func reconcileSettlementKey(turnID agentv1.TurnID, fence agentv1.Sequence) string {
	return settlementKeyDigest("reconcile", string(turnID), strconv.FormatInt(int64(fence), 10))
}

func settlementKeyDigest(domain, first, second string) string {
	hash := sha256.New()
	// Length-prefixing removes any chance of two different inputs hashing the
	// same way through separator collision.
	for _, part := range []string{domain, first, second} {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "wm:turn-settlement:v1:" + hex.EncodeToString(hash.Sum(nil))
}

// WithSettlementAuthority installs the commercial boundary for candidate and
// compatibility compositions.
//
// The zero value is nil, and a nil authority settles nothing. That is the
// default on purpose: composing this store must not silently start moving
// money, and every existing candidate path keeps its current behaviour until a
// caller opts in explicitly. An exact production composition seals its
// authority through BindSettlementReviewAuthority instead. Any later call to
// this compatibility mutator leaves the sealed authority installed but marks
// the binding invalid, so readiness and terminal writes fail closed.
func (store *SQLStore) WithSettlementAuthority(authority SettlementAuthority) *SQLStore {
	if store == nil {
		return store
	}
	store.settlementMu.Lock()
	defer store.settlementMu.Unlock()
	if store.settlementBinding != nil {
		store.settlementViolated = true
		return store
	}
	if settlementAuthorityMissing(authority) {
		authority = nil
	}
	store.settlement = authority
	return store
}

func settlementAuthorityMissing(authority any) bool {
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

func (store *SQLStore) settlementAuthority() (SettlementAuthority, bool) {
	if store == nil {
		return nil, false
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	return store.settlement, !store.settlementViolated
}

func (store *SQLStore) hasSettlementAuthority() bool {
	authority, _ := store.settlementAuthority()
	return authority != nil
}

// settle runs the commercial boundary inside the caller's transaction. A
// settlement failure fails the whole terminal commit: a Turn that cannot be
// billed correctly must not be recorded as finished.
func (store *SQLStore) settle(tx *gorm.DB, command SettlementCommand) error {
	authority, intact := store.settlementAuthority()
	if !intact {
		return ErrSettlementBindingInvalid
	}
	if authority == nil {
		return nil
	}
	if err := command.Validate(); err != nil {
		return err
	}
	if err := authority.Settle(tx, command); err != nil {
		return ErrSettlementFailed
	}
	return nil
}

// inspectAmbiguousRelease records, while the caller holds the Turn row lock and
// an installed SettlementAuthority can actually move money, that a release
// cannot discard usage represented by an earlier Operation or Effect. Events
// are deliberately excluded: core.running is lifecycle state, not evidence of
// domain output.
//
// The Effect query is an integrity defence. A valid Effect has an Operation
// parent, but checking both tables keeps an inconsistent database from turning
// a missing parent into a free release. currentEffects covers Effects created
// by the terminal command itself, before those rows have been inserted.
func inspectAmbiguousRelease(tx *gorm.DB, turnID agentv1.TurnID, currentEffects int) (SettlementUsageEvidence, error) {
	if tx == nil {
		return SettlementUsageEvidence{}, ErrStoreIntegrity
	}
	if currentEffects < 0 || currentEffects > MaxEffectsPerOperation {
		return SettlementUsageEvidence{}, ErrStoreIntegrity
	}
	evidence := SettlementUsageEvidence{CurrentEffectCount: currentEffects}
	for index, table := range []string{SQLTurnOperationTable, SQLEffectOutboxTable} {
		var count int64
		if err := tx.Table(table).Where("turn_id = ?", string(turnID)).Count(&count).Error; err != nil {
			return SettlementUsageEvidence{}, err
		}
		if count < 0 {
			return SettlementUsageEvidence{}, ErrStoreIntegrity
		}
		if index == 0 {
			evidence.PriorOperationCount = count
		} else {
			evidence.PriorEffectCount = count
		}
	}
	return evidence, nil
}
