package agentbilling

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/agentturn"
)

const (
	MaxExpiredReservationBatchSize      = 200
	MaxExpiredReservationFailureDetails = 32
)

var (
	ErrExpiredReservationReconcileLimit = errors.New("agent expired reservation reconcile limit is invalid")
	ErrExpiredReservationCursor         = errors.New("agent expired reservation reconcile cursor is invalid")
)

// ExpiredReservationFailureCode is a closed, driver-independent diagnostic.
// The scanner never returns persisted values or raw database errors in a pass
// report. LiveAttempt and NotExpired are benign deferrals; the remaining codes
// identify durable integrity or reconciliation failures.
type ExpiredReservationFailureCode string

const (
	ExpiredReservationFailureOwnerNotFound       ExpiredReservationFailureCode = "owner_not_found"
	ExpiredReservationFailureOwnerTupleDrift     ExpiredReservationFailureCode = "owner_tuple_drift"
	ExpiredReservationFailureReservationState    ExpiredReservationFailureCode = "reservation_state_drift"
	ExpiredReservationFailureNotExpired          ExpiredReservationFailureCode = "not_expired"
	ExpiredReservationFailureLiveAttempt         ExpiredReservationFailureCode = "live_attempt"
	ExpiredReservationFailureDurableConflict     ExpiredReservationFailureCode = "durable_conflict"
	ExpiredReservationFailurePreconditionChanged ExpiredReservationFailureCode = "precondition_changed"
	ExpiredReservationFailureReconcileFailed     ExpiredReservationFailureCode = "reconcile_failed"
)

type ExpiredReservationCursor struct {
	BindingRowID       uint64
	CycleHighWatermark uint64
}

// ExpiredReservationCandidate is discovery output, never mutation authority.
// A zero FailureCode means the row was an exact queued expiry or a running
// expiry whose active execution lease also elapsed at the discovery instant.
type ExpiredReservationCandidate struct {
	BindingRowID   uint64
	TurnID         agentv1.TurnID
	PrincipalID    agentturn.PrincipalID
	ReservationID  uint64
	TurnStatus     agentv1.TurnStatus
	AttemptID      string
	ExpiresAt      time.Time
	LeaseExpiresAt *time.Time
	FailureCode    ExpiredReservationFailureCode
}

type ExpiredReservationFailure struct {
	BindingRowID  uint64
	TurnID        agentv1.TurnID
	ReservationID uint64
	Code          ExpiredReservationFailureCode
}

type ExpiredReservationFailureDetails struct {
	Items   []ExpiredReservationFailure
	Omitted int
}

type ExpiredReservationPassResult struct {
	Discovered int
	Attempted  int
	Retired    []agentturn.ReconcileResult
	Deferred   int
	Skipped    int
	Failed     int
	NextCursor ExpiredReservationCursor
	Details    *ExpiredReservationFailureDetails
}

type ExpiredReservationReconcilerOptions struct {
	ReconcilerID          string
	ReconcilerBuildDigest string
	// SettlementBinding is the opaque Store-issued proof that the authority is
	// sealed. It is mandatory: a compatibility-installed authority can be
	// replaced between discovery and mutation and is therefore insufficient.
	SettlementBinding *agentturn.SettlementAuthorityBinding
	// ExpiryAuthority is the exact capability installed on Store. Nil selects
	// the CreditSettlementAuthority passed to the constructor. Provider-aware
	// composition supplies its ProviderUsageCreditAuthority wrapper here; the
	// constructor also proves that wrapper delegates to the same Credits ledger.
	ExpiryAuthority agentturn.TurnReservationExpiryAuthority
}

func (options ExpiredReservationReconcilerOptions) Validate() error {
	if err := validateASCII("reconcilerId", options.ReconcilerID, agentturn.MaxWorkerIDBytes); err != nil {
		return err
	}
	return validateASCII("reconcilerBuildDigest", options.ReconcilerBuildDigest, agentturn.MaxWorkerBuildDigestBytes)
}

// ExpiredReservationReconciler owns no scheduler or production wiring. One
// instance serializes its process-local cursor; an external scheduler may use
// the explicit-cursor API under its own single-writer lease and persist the
// returned cursor across restarts.
type ExpiredReservationReconciler struct {
	authority *CreditSettlementAuthority
	store     agentturn.ReconcileStore
	expiry    agentturn.TurnReservationExpiryAuthority
	matcher   turnReservationExpiryStoreMatcher
	binding   *agentturn.SettlementAuthorityBinding
	options   ExpiredReservationReconcilerOptions

	mu     sync.Mutex
	cursor ExpiredReservationCursor
}

type turnReservationExpiryStoreMatcher interface {
	MatchesTurnReservationExpiryAuthority(agentturn.TurnReservationExpiryAuthority, *gorm.DB) bool
	MatchesSettlementAuthorityBinding(*agentturn.SettlementAuthorityBinding) bool
}

func NewExpiredReservationReconciler(
	authority *CreditSettlementAuthority,
	store agentturn.ReconcileStore,
	options ExpiredReservationReconcilerOptions,
) (*ExpiredReservationReconciler, error) {
	if authority == nil || authority.db == nil || authority.reservations == nil || store == nil {
		return nil, ErrLedgerUnavailable
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	expiry := options.ExpiryAuthority
	if turnReservationExpiryAuthorityNil(expiry) {
		expiry = authority
	}
	if !expiryAuthorityUsesCreditLedger(expiry, authority) {
		return nil, ErrLedgerUnavailable
	}
	matcher, ok := store.(turnReservationExpiryStoreMatcher)
	if !ok || options.SettlementBinding == nil ||
		!matcher.MatchesSettlementAuthorityBinding(options.SettlementBinding) ||
		!matcher.MatchesTurnReservationExpiryAuthority(expiry, authority.db) {
		return nil, ErrLedgerUnavailable
	}
	return &ExpiredReservationReconciler{
		authority: authority, store: store, expiry: expiry, matcher: matcher,
		binding: options.SettlementBinding, options: options,
	}, nil
}

func turnReservationExpiryAuthorityNil(authority agentturn.TurnReservationExpiryAuthority) bool {
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

func expiryAuthorityUsesCreditLedger(
	expiry agentturn.TurnReservationExpiryAuthority,
	credits *CreditSettlementAuthority,
) bool {
	switch authority := expiry.(type) {
	case *CreditSettlementAuthority:
		return authority == credits
	case *ProviderUsageCreditAuthority:
		return authority != nil && authority.credits == credits
	default:
		return false
	}
}

type expiredReservationCandidateRow struct {
	BindingRowID             uint64     `gorm:"column:binding_row_id"`
	BindingID                string     `gorm:"column:binding_id"`
	BindingTurnID            string     `gorm:"column:binding_turn_id"`
	BindingPrincipalID       string     `gorm:"column:binding_principal_id"`
	BindingTurnCommandDigest string     `gorm:"column:binding_turn_command_digest"`
	BindingReservationID     uint64     `gorm:"column:binding_reservation_id"`
	BindingReservationUID    int        `gorm:"column:binding_reservation_uid"`
	BindingReservationDigest string     `gorm:"column:binding_reservation_digest"`
	BindingReservationTool   string     `gorm:"column:binding_reservation_tool"`
	BindingReservedUnits     int64      `gorm:"column:binding_reserved_units"`
	BindingProjectID         uint64     `gorm:"column:binding_project_id"`
	BindingPricingDigest     string     `gorm:"column:binding_pricing_digest"`
	BindingDigest            string     `gorm:"column:binding_digest"`
	BindingCreatedAt         time.Time  `gorm:"column:binding_created_at"`
	TurnRowID                *uint64    `gorm:"column:turn_row_id"`
	TurnID                   *string    `gorm:"column:turn_id"`
	TurnPrincipalID          *string    `gorm:"column:turn_principal_id"`
	TurnCommandDigest        *string    `gorm:"column:turn_command_digest"`
	TurnStatus               *string    `gorm:"column:turn_status"`
	TurnActiveAttemptID      *string    `gorm:"column:turn_active_attempt_id"`
	TurnFencingToken         *int64     `gorm:"column:turn_fencing_token"`
	TurnCancelRequestedAt    *time.Time `gorm:"column:turn_cancel_requested_at"`
	TurnStartedAt            *time.Time `gorm:"column:turn_started_at"`
	ReservationRowID         *uint64    `gorm:"column:reservation_row_id"`
	ReservationUID           *int       `gorm:"column:reservation_uid"`
	ReservationRequestDigest *string    `gorm:"column:reservation_request_digest"`
	ReservationTool          *string    `gorm:"column:reservation_tool"`
	ReservationReservedUnits *int64     `gorm:"column:reservation_reserved_units"`
	ReservationProjectID     *uint64    `gorm:"column:reservation_project_id"`
	ReservationStatus        *string    `gorm:"column:reservation_status"`
	ReservationExpiresAt     *time.Time `gorm:"column:reservation_expires_at"`
	AttemptRowID             *uint64    `gorm:"column:attempt_row_id"`
	AttemptID                *string    `gorm:"column:attempt_id"`
	AttemptTurnID            *string    `gorm:"column:attempt_turn_id"`
	AttemptFencingToken      *int64     `gorm:"column:attempt_fencing_token"`
	AttemptStatus            *string    `gorm:"column:attempt_status"`
	AttemptLeaseExpiresAt    *time.Time `gorm:"column:attempt_lease_expires_at"`
	ReviewRowID              *uint64    `gorm:"column:review_row_id"`
	OutcomeRowID             *uint64    `gorm:"column:outcome_row_id"`
}

func (reconciler *ExpiredReservationReconciler) DiscoverExpiredReservations(
	ctx context.Context,
	cursor ExpiredReservationCursor,
	limit int,
) ([]ExpiredReservationCandidate, ExpiredReservationCursor, bool, error) {
	if reconciler == nil || reconciler.authority == nil || reconciler.authority.db == nil || ctx == nil {
		return nil, cursor, false, ErrLedgerUnavailable
	}
	if reconciler.matcher == nil || reconciler.binding == nil ||
		!reconciler.matcher.MatchesSettlementAuthorityBinding(reconciler.binding) ||
		!reconciler.matcher.MatchesTurnReservationExpiryAuthority(reconciler.expiry, reconciler.authority.db) {
		return nil, cursor, false, ErrLedgerUnavailable
	}
	if limit < 1 || limit > MaxExpiredReservationBatchSize {
		return nil, cursor, false, fmt.Errorf(
			"%w: got %d, want 1..%d", ErrExpiredReservationReconcileLimit, limit, MaxExpiredReservationBatchSize,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, cursor, false, err
	}
	db := reconciler.authority.db.WithContext(ctx)
	now, err := billingDatabaseNow(db)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, cursor, false, ctxErr
		}
		return nil, cursor, false, err
	}
	if err := validateExpiredReservationCursor(db, cursor); err != nil {
		return nil, cursor, false, err
	}
	return reconciler.discoverExpiredPage(ctx, db, now, cursor, limit)
}

func validateExpiredReservationCursor(db *gorm.DB, cursor ExpiredReservationCursor) error {
	if db == nil {
		return ErrLedgerUnavailable
	}
	if cursor.CycleHighWatermark == 0 {
		if cursor.BindingRowID != 0 {
			return ErrExpiredReservationCursor
		}
		return nil
	}
	if cursor.BindingRowID > cursor.CycleHighWatermark {
		return ErrExpiredReservationCursor
	}
	var ownerHigh uint64
	if err := db.Table(BindingTable).Select("COALESCE(MAX(id), 0)").Scan(&ownerHigh).Error; err != nil {
		return err
	}
	if cursor.CycleHighWatermark > ownerHigh {
		return ErrExpiredReservationCursor
	}
	return nil
}

func (reconciler *ExpiredReservationReconciler) discoverExpiredPage(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	cursor ExpiredReservationCursor,
	limit int,
) ([]ExpiredReservationCandidate, ExpiredReservationCursor, bool, error) {
	if cursor.CycleHighWatermark == 0 {
		var high uint64
		if err := reconciler.expiredReservationBaseQuery(db, now).
			Select("COALESCE(MAX(turn_binding.id), 0)").Scan(&high).Error; err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, cursor, false, ctxErr
			}
			return nil, cursor, false, err
		}
		cursor.CycleHighWatermark = high
		if cursor.BindingRowID >= high {
			cursor.BindingRowID = 0
		}
	}
	if cursor.CycleHighWatermark == 0 {
		return nil, ExpiredReservationCursor{}, true, nil
	}

	query := reconciler.expiredReservationBaseQuery(db, now).
		Select(`turn_binding.id AS binding_row_id,
			turn_binding.binding_id AS binding_id,
			turn_binding.turn_id AS binding_turn_id,
			turn_binding.principal_id AS binding_principal_id,
			turn_binding.turn_command_digest AS binding_turn_command_digest,
			turn_binding.reservation_id AS binding_reservation_id,
			turn_binding.reservation_uid AS binding_reservation_uid,
			turn_binding.reservation_request_digest AS binding_reservation_digest,
			turn_binding.reservation_tool AS binding_reservation_tool,
			turn_binding.reserved_units AS binding_reserved_units,
			turn_binding.project_id AS binding_project_id,
			turn_binding.pricing_snapshot_digest AS binding_pricing_digest,
			turn_binding.binding_digest AS binding_digest,
			turn_binding.created_at AS binding_created_at,
			turn_owner.id AS turn_row_id,
			turn_owner.turn_id AS turn_id,
			turn_owner.principal_id AS turn_principal_id,
			turn_owner.command_digest AS turn_command_digest,
			turn_owner.status AS turn_status,
			turn_owner.active_attempt_id AS turn_active_attempt_id,
			turn_owner.fencing_token AS turn_fencing_token,
			turn_owner.cancel_requested_at AS turn_cancel_requested_at,
			turn_owner.started_at AS turn_started_at,
			credit_reservation.id AS reservation_row_id,
			credit_reservation.uid AS reservation_uid,
			credit_reservation.request_digest AS reservation_request_digest,
			credit_reservation.tool AS reservation_tool,
			credit_reservation.reserved AS reservation_reserved_units,
			credit_reservation.project_id AS reservation_project_id,
			credit_reservation.status AS reservation_status,
			credit_reservation.expires_at AS reservation_expires_at,
			active_attempt.id AS attempt_row_id,
			active_attempt.attempt_id AS attempt_id,
			active_attempt.turn_id AS attempt_turn_id,
			active_attempt.fencing_token AS attempt_fencing_token,
			active_attempt.status AS attempt_status,
			active_attempt.lease_expires_at AS attempt_lease_expires_at,
			settlement_review.id AS review_row_id,
			settlement_outcome.id AS outcome_row_id`).
		Where("turn_binding.id > ? AND turn_binding.id <= ?", cursor.BindingRowID, cursor.CycleHighWatermark).
		Order("turn_binding.id ASC").Limit(limit)

	var rows []expiredReservationCandidateRow
	if err := query.Scan(&rows).Error; err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, cursor, false, ctxErr
		}
		return nil, cursor, false, err
	}
	candidates := make([]ExpiredReservationCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, row.candidate(now))
	}
	exhausted := len(candidates) < limit
	if len(candidates) > 0 && candidates[len(candidates)-1].BindingRowID >= cursor.CycleHighWatermark {
		exhausted = true
	}
	return candidates, cursor, exhausted, nil
}

func (reconciler *ExpiredReservationReconciler) expiredReservationBaseQuery(
	db *gorm.DB,
	now time.Time,
) *gorm.DB {
	query := db.Table(BindingTable + " AS turn_binding").
		Joins(`LEFT JOIN ` + agentturn.SQLTurnTable + ` AS turn_owner
			ON turn_owner.turn_id = turn_binding.turn_id`).
		Joins(`LEFT JOIN w_credit_reservation AS credit_reservation
			ON credit_reservation.id = turn_binding.reservation_id`).
		Joins(`LEFT JOIN ` + agentturn.SQLTurnAttemptTable + ` AS active_attempt
			ON active_attempt.turn_id = turn_owner.turn_id
			AND active_attempt.attempt_id = turn_owner.active_attempt_id`).
		Joins(`LEFT JOIN ` + agentturn.SQLSettlementReviewTable + ` AS settlement_review
			ON settlement_review.turn_id = turn_binding.turn_id`).
		Joins(`LEFT JOIN ` + OutcomeTable + ` AS settlement_outcome
			ON settlement_outcome.turn_id = turn_binding.turn_id`)

	// Missing owners are always diagnostics. Otherwise only a non-terminal
	// owner with a non-reserved commercial state, or a due reserved hold, enters
	// the finite generation. Normal terminal history is deliberately excluded.
	predicate := `(
		turn_owner.id IS NULL
		OR credit_reservation.id IS NULL
		OR (
			turn_owner.status IN (?, ?)
			AND (
				credit_reservation.status IS NULL
				OR credit_reservation.status <> ?
				OR %s
			)
		)
		OR (
			credit_reservation.status = ?
			AND %s
		)
	)`
	if db.Dialector.Name() == "sqlite" {
		return query.Where(fmt.Sprintf(predicate,
			"julianday(credit_reservation.expires_at) <= julianday(?)",
			"julianday(credit_reservation.expires_at) <= julianday(?)",
		), string(agentv1.TurnStatusQueued), string(agentv1.TurnStatusRunning),
			model.CreditReservationStatusReserved, now, model.CreditReservationStatusReserved, now)
	}
	return query.Where(fmt.Sprintf(predicate,
		"credit_reservation.expires_at <= ?", "credit_reservation.expires_at <= ?",
	), string(agentv1.TurnStatusQueued), string(agentv1.TurnStatusRunning),
		model.CreditReservationStatusReserved, now, model.CreditReservationStatusReserved, now)
}

func (row expiredReservationCandidateRow) candidate(now time.Time) ExpiredReservationCandidate {
	candidate := ExpiredReservationCandidate{
		BindingRowID: row.BindingRowID, TurnID: agentv1.TurnID(row.BindingTurnID),
		PrincipalID: agentturn.PrincipalID(row.BindingPrincipalID), ReservationID: row.BindingReservationID,
	}
	if row.TurnStatus != nil {
		candidate.TurnStatus = agentv1.TurnStatus(*row.TurnStatus)
	}
	if row.AttemptID != nil {
		candidate.AttemptID = *row.AttemptID
	}
	if row.ReservationExpiresAt != nil {
		candidate.ExpiresAt = row.ReservationExpiresAt.UTC()
	}
	if row.AttemptLeaseExpiresAt != nil {
		lease := row.AttemptLeaseExpiresAt.UTC()
		candidate.LeaseExpiresAt = &lease
	}

	if row.TurnRowID == nil || row.ReservationRowID == nil {
		candidate.FailureCode = ExpiredReservationFailureOwnerNotFound
		return candidate
	}
	binding := BindingRecord{
		BindingID: row.BindingID, TurnID: agentv1.TurnID(row.BindingTurnID),
		PrincipalID: agentturn.PrincipalID(row.BindingPrincipalID), TurnCommandDigest: row.BindingTurnCommandDigest,
		ReservationID: row.BindingReservationID, ReservationUID: row.BindingReservationUID,
		ReservationRequestDigest: row.BindingReservationDigest, ReservationTool: row.BindingReservationTool,
		ReservedUnits: row.BindingReservedUnits, ProjectID: row.BindingProjectID,
		PricingSnapshotDigest: row.BindingPricingDigest, BindingDigest: row.BindingDigest,
		CreatedAt: row.BindingCreatedAt.UTC(),
	}
	if binding.Validate() != nil || row.TurnID == nil || row.TurnPrincipalID == nil ||
		row.TurnCommandDigest == nil || row.TurnStatus == nil || row.TurnFencingToken == nil ||
		row.ReservationUID == nil || row.ReservationRequestDigest == nil || row.ReservationTool == nil ||
		row.ReservationReservedUnits == nil || row.ReservationProjectID == nil || row.ReservationStatus == nil ||
		row.ReservationExpiresAt == nil || *row.TurnID != row.BindingTurnID ||
		*row.TurnPrincipalID != row.BindingPrincipalID || *row.TurnCommandDigest != row.BindingTurnCommandDigest ||
		*row.ReservationRowID != row.BindingReservationID || *row.ReservationUID != row.BindingReservationUID ||
		*row.ReservationRequestDigest != row.BindingReservationDigest ||
		*row.ReservationTool != row.BindingReservationTool ||
		*row.ReservationReservedUnits != row.BindingReservedUnits ||
		*row.ReservationProjectID != row.BindingProjectID {
		candidate.FailureCode = ExpiredReservationFailureOwnerTupleDrift
		return candidate
	}
	if row.ReviewRowID != nil || row.OutcomeRowID != nil || row.TurnCancelRequestedAt != nil {
		candidate.FailureCode = ExpiredReservationFailureDurableConflict
		return candidate
	}
	if *row.ReservationStatus != model.CreditReservationStatusReserved {
		candidate.FailureCode = ExpiredReservationFailureReservationState
		return candidate
	}
	if row.ReservationExpiresAt.After(now) {
		candidate.FailureCode = ExpiredReservationFailureNotExpired
		return candidate
	}

	switch agentv1.TurnStatus(*row.TurnStatus) {
	case agentv1.TurnStatusQueued:
		if row.TurnActiveAttemptID != nil || *row.TurnFencingToken != 0 || row.TurnStartedAt != nil ||
			row.AttemptRowID != nil {
			candidate.FailureCode = ExpiredReservationFailureOwnerTupleDrift
		}
	case agentv1.TurnStatusRunning:
		if row.TurnActiveAttemptID == nil || row.AttemptRowID == nil || row.AttemptID == nil ||
			row.AttemptTurnID == nil || row.AttemptFencingToken == nil || row.AttemptStatus == nil ||
			row.AttemptLeaseExpiresAt == nil || row.TurnStartedAt == nil ||
			*row.TurnActiveAttemptID != *row.AttemptID || *row.AttemptTurnID != row.BindingTurnID ||
			*row.AttemptFencingToken != *row.TurnFencingToken ||
			*row.AttemptStatus != string(agentturn.AttemptStatusRunning) {
			candidate.FailureCode = ExpiredReservationFailureOwnerTupleDrift
			return candidate
		}
		if row.AttemptLeaseExpiresAt.After(now) {
			candidate.FailureCode = ExpiredReservationFailureLiveAttempt
		}
	default:
		candidate.FailureCode = ExpiredReservationFailureReservationState
	}
	return candidate
}

func (reconciler *ExpiredReservationReconciler) ReconcileExpiredReservationPass(
	ctx context.Context,
	limit int,
) (ExpiredReservationPassResult, error) {
	if reconciler == nil {
		return ExpiredReservationPassResult{}, ErrLedgerUnavailable
	}
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	result, err := reconciler.ReconcileExpiredReservationPassAfter(ctx, reconciler.cursor, limit)
	reconciler.cursor = result.NextCursor
	return result, err
}

func (reconciler *ExpiredReservationReconciler) ReconcileExpiredReservationPassAfter(
	ctx context.Context,
	cursor ExpiredReservationCursor,
	limit int,
) (ExpiredReservationPassResult, error) {
	result := ExpiredReservationPassResult{NextCursor: cursor}
	candidates, prepared, exhausted, err := reconciler.DiscoverExpiredReservations(ctx, cursor, limit)
	if err != nil {
		return result, err
	}
	result.NextCursor = prepared
	result.Discovered = len(candidates)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.NextCursor.BindingRowID = candidate.BindingRowID
		switch candidate.FailureCode {
		case ExpiredReservationFailureLiveAttempt:
			result.Deferred++
			result.addDetail(candidate, candidate.FailureCode)
			continue
		case ExpiredReservationFailureNotExpired:
			result.Skipped++
			result.addDetail(candidate, candidate.FailureCode)
			continue
		case "":
			// Exact discovery remains non-authoritative; mutation below rechecks
			// Turn, Attempt, Binding, Outcome and Reservation under one tx.
		default:
			result.Failed++
			result.addDetail(candidate, candidate.FailureCode)
			continue
		}

		result.Attempted++
		if !reconciler.matcher.MatchesSettlementAuthorityBinding(reconciler.binding) ||
			!reconciler.matcher.MatchesTurnReservationExpiryAuthority(
				reconciler.expiry, reconciler.authority.db,
			) {
			return result, ErrLedgerUnavailable
		}
		reconciled, reconcileErr := reconciler.store.ReconcileTerminal(ctx, agentturn.ReconcileCommand{
			TurnID: candidate.TurnID, Reason: agentturn.ReclaimReasonReservationExpired,
			ReconcilerID:          reconciler.options.ReconcilerID,
			ReconcilerBuildDigest: reconciler.options.ReconcilerBuildDigest,
		})
		switch {
		case reconcileErr == nil && reconciled.Changed:
			result.Retired = append(result.Retired, reconciled)
		case reconcileErr == nil:
			result.Skipped++
		case errors.Is(reconcileErr, agentturn.ErrReconcilePrecondition),
			errors.Is(reconcileErr, agentturn.ErrTurnTerminal):
			result.Skipped++
			result.addDetail(candidate, ExpiredReservationFailurePreconditionChanged)
		case errors.Is(reconcileErr, agentturn.ErrTurnNotFound):
			result.Failed++
			result.addDetail(candidate, ExpiredReservationFailureOwnerNotFound)
		case errors.Is(reconcileErr, agentturn.ErrStoreIntegrity),
			errors.Is(reconcileErr, agentturn.ErrSettlementBindingInvalid):
			result.Failed++
			result.addDetail(candidate, ExpiredReservationFailureDurableConflict)
		default:
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Failed++
			result.addDetail(candidate, ExpiredReservationFailureReconcileFailed)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if exhausted {
		result.NextCursor = ExpiredReservationCursor{}
	}
	return result, nil
}

func (result *ExpiredReservationPassResult) addDetail(
	candidate ExpiredReservationCandidate,
	code ExpiredReservationFailureCode,
) {
	if result.Details == nil {
		result.Details = &ExpiredReservationFailureDetails{}
	}
	if len(result.Details.Items) >= MaxExpiredReservationFailureDetails {
		result.Details.Omitted++
		return
	}
	result.Details.Items = append(result.Details.Items, ExpiredReservationFailure{
		BindingRowID: candidate.BindingRowID, TurnID: candidate.TurnID,
		ReservationID: candidate.ReservationID, Code: code,
	})
}
