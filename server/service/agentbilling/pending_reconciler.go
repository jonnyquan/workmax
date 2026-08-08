package agentbilling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/agentturn"
)

const MaxPendingReconcileBatchSize = 200

// MaxPendingReconcileFailureDetails keeps one poisoned batch from producing an
// unbounded scheduler/log payload. Failed remains the total failure count;
// FailureDetailsOmitted reports the identities beyond this diagnostic bound.
const MaxPendingReconcileFailureDetails = 32

var (
	ErrPendingReconcileLimit  = errors.New("agent pending settlement reconcile limit is invalid")
	ErrPendingReconcileCursor = errors.New("agent pending settlement reconcile cursor is invalid")
)

type PendingReconcileFailureCode string

const (
	PendingReconcileFailureOwnerTupleDrift  PendingReconcileFailureCode = "owner_tuple_drift"
	PendingReconcileFailureOwnerNotFound    PendingReconcileFailureCode = "owner_not_found"
	PendingReconcileFailureDurableConflict  PendingReconcileFailureCode = "durable_conflict"
	PendingReconcileFailureReconcileFailed  PendingReconcileFailureCode = "reconcile_failed"
	PendingReconcileFailureUnexpectedStatus PendingReconcileFailureCode = "unexpected_outcome_status"
)

// PendingReconcileCursor is the last Outcome row considered by a recovery
// pass. Persisting this value lets an external scheduler retain fairness across
// process restarts; ReconcileDuePendingPass also maintains a process-local,
// concurrency-safe cursor for callers of the original API.
type PendingReconcileCursor struct {
	OutcomeRowID       uint64
	CycleHighWatermark uint64
}

// PendingReconcileCandidate contains only the immutable owner identity needed
// to enter ReconcilePending. Discovery never returns a mutable Reservation
// request key and never takes a Reservation lock.
type PendingReconcileCandidate struct {
	OutcomeRowID  uint64
	TurnID        agentv1.TurnID
	PrincipalID   agentturn.PrincipalID
	ReservationID uint64
	SettlementKey string
	NextRefundAt  time.Time

	// FailureCode is populated when the Outcome-led LEFT JOIN discovers a
	// broken owner tuple. Such a candidate must be reported, not reconciled
	// through a Reservation-first path.
	FailureCode PendingReconcileFailureCode
}

// PendingReconcileFailure is a stable, bounded diagnostic identity. It never
// includes a database-driver error string.
type PendingReconcileFailure struct {
	OutcomeRowID  uint64
	TurnID        agentv1.TurnID
	ReservationID uint64
	SettlementKey string
	Code          PendingReconcileFailureCode
}

// PendingReconcileFailureDetails is present only when a pass has failures.
// Items is capped by MaxPendingReconcileFailureDetails.
type PendingReconcileFailureDetails struct {
	Items   []PendingReconcileFailure
	Omitted int
}

// PendingReconcilePassResult makes partial progress explicit. Attempted is
// always Converged + StillPending + Failed. A retry that another worker has
// already converged is counted as Converged; a retry whose backoff was moved
// forward concurrently is counted as StillPending.
type PendingReconcilePassResult struct {
	Discovered     int
	Attempted      int
	Converged      int
	StillPending   int
	Failed         int
	NextCursor     PendingReconcileCursor
	FailureDetails *PendingReconcileFailureDetails
}

type pendingReconcileCandidateRow struct {
	OutcomeRowID             uint64     `gorm:"column:outcome_row_id"`
	OutcomeBindingID         string     `gorm:"column:outcome_binding_id"`
	TurnID                   string     `gorm:"column:turn_id"`
	OutcomeReservationID     uint64     `gorm:"column:outcome_reservation_id"`
	OutcomeBindingDigest     string     `gorm:"column:outcome_binding_digest"`
	OutcomeReservedUnits     int64      `gorm:"column:outcome_reserved_units"`
	SettlementKey            string     `gorm:"column:settlement_key"`
	BindingRowID             *uint64    `gorm:"column:binding_row_id"`
	BindingTurnID            *string    `gorm:"column:binding_turn_id"`
	PrincipalID              *string    `gorm:"column:principal_id"`
	BindingReservationID     *uint64    `gorm:"column:binding_reservation_id"`
	BindingReservationUID    *int       `gorm:"column:binding_reservation_uid"`
	BindingReservationDigest *string    `gorm:"column:binding_reservation_digest"`
	BindingReservationTool   *string    `gorm:"column:binding_reservation_tool"`
	BindingReservedUnits     *int64     `gorm:"column:binding_reserved_units"`
	BindingProjectID         *uint64    `gorm:"column:binding_project_id"`
	BindingDigest            *string    `gorm:"column:binding_digest"`
	ReservationRowID         *uint64    `gorm:"column:reservation_row_id"`
	ReservationUID           *int       `gorm:"column:reservation_uid"`
	ReservationRequestDigest *string    `gorm:"column:reservation_request_digest"`
	ReservationTool          *string    `gorm:"column:reservation_tool"`
	ReservationReservedUnits *int64     `gorm:"column:reservation_reserved_units"`
	ReservationProjectID     *uint64    `gorm:"column:reservation_project_id"`
	ReservationStatus        *string    `gorm:"column:reservation_status"`
	ReservationNextRefundAt  *time.Time `gorm:"column:next_refund_at"`
}

// DiscoverDuePending performs a bounded, read-only candidate scan. The scan
// verifies both durable exact tuples:
//
//	Outcome -> immutable Turn/Reservation binding -> Credit Reservation.
//
// It deliberately has no FOR UPDATE clause. Each candidate is later processed
// by ReconcilePending, whose transaction starts with the Turn owner lock.
func (authority *CreditSettlementAuthority) DiscoverDuePending(
	ctx context.Context,
	limit int,
) ([]PendingReconcileCandidate, error) {
	candidates, _, _, err := authority.DiscoverDuePendingAfter(ctx, PendingReconcileCursor{}, limit)
	return candidates, err
}

// DiscoverDuePendingAfter scans one finite generation by Outcome primary key,
// strictly after cursor and no later than its cycle high-watermark. A zero
// high-watermark captures the current maximum eligible Outcome. The Outcome is
// the scan anchor; LEFT JOINs make a missing or mismatched Binding/Reservation
// visible as a failed candidate instead of silently excluding it. The returned
// prepared cursor carries a newly captured high watermark; callers performing
// read-only pagination must persist it rather than reconstructing a cursor from
// the last candidate alone.
func (authority *CreditSettlementAuthority) DiscoverDuePendingAfter(
	ctx context.Context,
	cursor PendingReconcileCursor,
	limit int,
) ([]PendingReconcileCandidate, PendingReconcileCursor, bool, error) {
	if authority == nil || authority.db == nil || ctx == nil {
		return nil, cursor, false, ErrLedgerUnavailable
	}
	if limit < 1 || limit > MaxPendingReconcileBatchSize {
		return nil, cursor, false, fmt.Errorf(
			"%w: got %d, want 1..%d", ErrPendingReconcileLimit, limit, MaxPendingReconcileBatchSize,
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, cursor, false, err
	}

	db := authority.db.WithContext(ctx)
	now, err := billingDatabaseNow(db)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, cursor, false, ctxErr
		}
		return nil, cursor, false, err
	}

	prepared, candidates, exhausted, err := authority.discoverDuePendingPage(ctx, db, now, cursor, limit)
	return candidates, prepared, exhausted, err
}

func (authority *CreditSettlementAuthority) discoverDuePendingPage(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	cursor PendingReconcileCursor,
	limit int,
) (PendingReconcileCursor, []PendingReconcileCandidate, bool, error) {
	if err := validatePendingReconcileCursor(db, cursor); err != nil {
		return cursor, nil, false, err
	}
	if cursor.CycleHighWatermark == 0 {
		high, err := authority.discoverDuePendingHighWatermark(ctx, db, now)
		if err != nil {
			return cursor, nil, false, err
		}
		cursor.CycleHighWatermark = high
		if cursor.OutcomeRowID >= high {
			cursor.OutcomeRowID = 0
		}
	}
	if cursor.CycleHighWatermark == 0 {
		return PendingReconcileCursor{}, nil, true, nil
	}

	candidates, err := authority.discoverDuePendingRange(
		ctx, db, now, cursor.OutcomeRowID, cursor.CycleHighWatermark, limit,
	)
	if err != nil {
		return cursor, nil, false, err
	}
	exhausted := len(candidates) < limit
	if len(candidates) > 0 && candidates[len(candidates)-1].OutcomeRowID >= cursor.CycleHighWatermark {
		exhausted = true
	}
	return cursor, candidates, exhausted, nil
}

func validatePendingReconcileCursor(db *gorm.DB, cursor PendingReconcileCursor) error {
	if db == nil || (cursor.CycleHighWatermark == 0 && cursor.OutcomeRowID != 0) ||
		(cursor.CycleHighWatermark != 0 && cursor.OutcomeRowID > cursor.CycleHighWatermark) {
		return ErrPendingReconcileCursor
	}
	if cursor.CycleHighWatermark == 0 {
		return nil
	}
	var ownerHigh uint64
	if err := db.Table(OutcomeTable).Select("COALESCE(MAX(id), 0)").Scan(&ownerHigh).Error; err != nil {
		return err
	}
	if cursor.CycleHighWatermark > ownerHigh {
		return ErrPendingReconcileCursor
	}
	return nil
}

func (authority *CreditSettlementAuthority) discoverDuePendingHighWatermark(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
) (uint64, error) {
	var high uint64
	if err := authority.pendingReconcileBaseQuery(db, now).
		Select("COALESCE(MAX(settlement_outcome.id), 0)").Scan(&high).Error; err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, ctxErr
		}
		return 0, err
	}
	return high, nil
}

func (authority *CreditSettlementAuthority) discoverDuePendingRange(
	ctx context.Context,
	db *gorm.DB,
	now time.Time,
	cursor uint64,
	high uint64,
	limit int,
) ([]PendingReconcileCandidate, error) {
	query := authority.pendingReconcileBaseQuery(db, now).
		Select(`settlement_outcome.id AS outcome_row_id,
			settlement_outcome.binding_id AS outcome_binding_id,
			settlement_outcome.turn_id AS turn_id,
			settlement_outcome.reservation_id AS outcome_reservation_id,
			settlement_outcome.binding_digest AS outcome_binding_digest,
			settlement_outcome.reserved_units AS outcome_reserved_units,
			settlement_outcome.settlement_key AS settlement_key,
			turn_binding.id AS binding_row_id,
			turn_binding.turn_id AS binding_turn_id,
			turn_binding.principal_id AS principal_id,
			turn_binding.reservation_id AS binding_reservation_id,
			turn_binding.reservation_uid AS binding_reservation_uid,
			turn_binding.reservation_request_digest AS binding_reservation_digest,
			turn_binding.reservation_tool AS binding_reservation_tool,
			turn_binding.reserved_units AS binding_reserved_units,
			turn_binding.project_id AS binding_project_id,
			turn_binding.binding_digest AS binding_digest,
			credit_reservation.id AS reservation_row_id,
			credit_reservation.uid AS reservation_uid,
			credit_reservation.request_digest AS reservation_request_digest,
			credit_reservation.tool AS reservation_tool,
			credit_reservation.reserved AS reservation_reserved_units,
			credit_reservation.project_id AS reservation_project_id,
			credit_reservation.status AS reservation_status,
			credit_reservation.next_refund_at AS next_refund_at`).
		Where("settlement_outcome.id > ? AND settlement_outcome.id <= ?", cursor, high)

	var rows []pendingReconcileCandidateRow
	if err := query.Order("settlement_outcome.id ASC").Limit(limit).Scan(&rows).Error; err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	candidates := make([]PendingReconcileCandidate, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, row.candidate())
	}
	return candidates, nil
}

func (authority *CreditSettlementAuthority) pendingReconcileBaseQuery(
	db *gorm.DB,
	now time.Time,
) *gorm.DB {
	query := db.Table(OutcomeTable+" AS settlement_outcome").
		Joins(`LEFT JOIN `+BindingTable+` AS turn_binding
			ON turn_binding.binding_id = settlement_outcome.binding_id`).
		Joins(`LEFT JOIN w_credit_reservation AS credit_reservation
			ON credit_reservation.id = settlement_outcome.reservation_id`).
		Where("settlement_outcome.status = ?", OutcomeStatusRefundPending)

	integrityOrDue := `(
		turn_binding.id IS NULL
		OR credit_reservation.id IS NULL
		OR turn_binding.turn_id <> settlement_outcome.turn_id
		OR turn_binding.reservation_id <> settlement_outcome.reservation_id
		OR turn_binding.binding_digest <> settlement_outcome.binding_digest
		OR turn_binding.reserved_units <> settlement_outcome.reserved_units
		OR credit_reservation.uid <> turn_binding.reservation_uid
		OR credit_reservation.request_digest IS NULL
		OR credit_reservation.request_digest <> turn_binding.reservation_request_digest
		OR credit_reservation.tool <> turn_binding.reservation_tool
		OR credit_reservation.reserved <> turn_binding.reserved_units
		OR credit_reservation.project_id <> turn_binding.project_id
		OR credit_reservation.status IS NULL
		OR credit_reservation.status <> ?
		OR credit_reservation.next_refund_at IS NULL
		OR %s
	)`
	if db.Dialector.Name() == "sqlite" {
		// SQLite fixtures can contain both UTC and local-offset DATETIME text.
		// julianday compares instants instead of their serialized wall clocks.
		query = query.Where(
			fmt.Sprintf(integrityOrDue, "julianday(credit_reservation.next_refund_at) <= julianday('now')"),
			model.CreditReservationStatusRefundPending,
		)
	} else {
		query = query.Where(
			fmt.Sprintf(integrityOrDue, "credit_reservation.next_refund_at <= ?"),
			model.CreditReservationStatusRefundPending, now,
		)
	}
	return query
}

func (row pendingReconcileCandidateRow) candidate() PendingReconcileCandidate {
	candidate := PendingReconcileCandidate{
		OutcomeRowID: row.OutcomeRowID, TurnID: agentv1.TurnID(row.TurnID),
		ReservationID: row.OutcomeReservationID, SettlementKey: row.SettlementKey,
	}
	if row.PrincipalID != nil {
		candidate.PrincipalID = agentturn.PrincipalID(*row.PrincipalID)
	}
	if row.ReservationNextRefundAt != nil {
		candidate.NextRefundAt = row.ReservationNextRefundAt.UTC()
	}
	if row.BindingRowID == nil || row.BindingTurnID == nil || row.PrincipalID == nil ||
		row.BindingReservationID == nil || row.BindingReservationUID == nil ||
		row.BindingReservationDigest == nil || row.BindingReservationTool == nil ||
		row.BindingReservedUnits == nil || row.BindingProjectID == nil || row.BindingDigest == nil ||
		row.ReservationRowID == nil || row.ReservationUID == nil ||
		row.ReservationRequestDigest == nil || row.ReservationTool == nil ||
		row.ReservationReservedUnits == nil || row.ReservationProjectID == nil ||
		row.ReservationStatus == nil || row.ReservationNextRefundAt == nil ||
		*row.BindingTurnID != row.TurnID ||
		*row.BindingReservationID != row.OutcomeReservationID ||
		*row.BindingDigest != row.OutcomeBindingDigest ||
		*row.BindingReservedUnits != row.OutcomeReservedUnits ||
		*row.ReservationRowID != row.OutcomeReservationID ||
		*row.ReservationUID != *row.BindingReservationUID ||
		*row.ReservationRequestDigest != *row.BindingReservationDigest ||
		*row.ReservationTool != *row.BindingReservationTool ||
		*row.ReservationReservedUnits != *row.BindingReservedUnits ||
		*row.ReservationProjectID != *row.BindingProjectID ||
		*row.ReservationStatus != model.CreditReservationStatusRefundPending {
		candidate.FailureCode = PendingReconcileFailureOwnerTupleDrift
	}
	return candidate
}

// ReconcileDuePendingPass runs at most limit owner-first reconciliation
// transactions. Discovery failure and context cancellation stop the pass;
// individual durable-row failures are isolated and counted so one bad Turn
// cannot starve later candidates in the same bounded batch.
func (authority *CreditSettlementAuthority) ReconcileDuePendingPass(
	ctx context.Context,
	limit int,
) (PendingReconcilePassResult, error) {
	if authority == nil {
		return PendingReconcilePassResult{}, ErrLedgerUnavailable
	}
	authority.pendingReconcileMu.Lock()
	defer authority.pendingReconcileMu.Unlock()

	result, err := authority.ReconcileDuePendingPassAfter(
		ctx, PendingReconcileCursor{
			OutcomeRowID:       authority.pendingReconcileCursor,
			CycleHighWatermark: authority.pendingReconcileHigh,
		}, limit,
	)
	authority.pendingReconcileCursor = result.NextCursor.OutcomeRowID
	authority.pendingReconcileHigh = result.NextCursor.CycleHighWatermark
	return result, err
}

// ReconcileDuePendingPassAfter is the explicit-cursor form for schedulers that
// persist and serialize recovery progress under a single scheduler lease. A
// tuple-drift candidate advances immediately because no mutation is attempted.
// A mutation candidate advances only after its isolated owner-first attempt has
// resolved to a durable result or a non-context failure. Cancellation leaves
// that candidate unconsumed, so the returned statistics remain balanced and a
// persisted cursor retries an outcome whose commit was not observed. The
// finite captured high watermark prevents continuous inserts from postponing
// the next ring forever.
func (authority *CreditSettlementAuthority) ReconcileDuePendingPassAfter(
	ctx context.Context,
	cursor PendingReconcileCursor,
	limit int,
) (PendingReconcilePassResult, error) {
	result := PendingReconcilePassResult{NextCursor: cursor}
	if authority == nil || authority.db == nil || ctx == nil {
		return result, ErrLedgerUnavailable
	}
	if limit < 1 || limit > MaxPendingReconcileBatchSize {
		return result, fmt.Errorf("%w: got %d, want 1..%d", ErrPendingReconcileLimit, limit, MaxPendingReconcileBatchSize)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	db := authority.db.WithContext(ctx)
	now, err := billingDatabaseNow(db)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		return result, err
	}
	preparedCursor, candidates, cycleExhausted, err := authority.discoverDuePendingPage(
		ctx, db, now, cursor, limit,
	)
	if err != nil {
		return result, err
	}
	result.NextCursor = preparedCursor
	result.Discovered = len(candidates)

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if candidate.FailureCode != "" {
			result.Attempted++
			result.NextCursor.OutcomeRowID = candidate.OutcomeRowID
			result.addFailure(candidate, candidate.FailureCode)
			continue
		}
		outcome, reconcileErr := authority.ReconcilePending(ctx, candidate.PrincipalID, candidate.TurnID)
		if reconcileErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			result.Attempted++
			result.NextCursor.OutcomeRowID = candidate.OutcomeRowID
			result.addFailure(candidate, pendingReconcileFailureCode(reconcileErr))
			continue
		}
		result.Attempted++
		result.NextCursor.OutcomeRowID = candidate.OutcomeRowID
		switch outcome.Status {
		case OutcomeStatusFinalized, OutcomeStatusReleased:
			result.Converged++
		case OutcomeStatusRefundPending:
			result.StillPending++
		default:
			// Discovery admits only refund_pending rows and the ledger is
			// monotonic. Any other post-state is therefore durable drift.
			result.addFailure(candidate, PendingReconcileFailureUnexpectedStatus)
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if cycleExhausted {
		result.NextCursor = PendingReconcileCursor{}
	}
	return result, nil
}

func pendingReconcileFailureCode(err error) PendingReconcileFailureCode {
	switch {
	case errors.Is(err, ErrBindingNotFound), errors.Is(err, ErrOutcomeNotFound):
		return PendingReconcileFailureOwnerNotFound
	case errors.Is(err, ErrBindingConflict), errors.Is(err, ErrOutcomeConflict):
		return PendingReconcileFailureDurableConflict
	default:
		return PendingReconcileFailureReconcileFailed
	}
}

func (result *PendingReconcilePassResult) addFailure(
	candidate PendingReconcileCandidate,
	code PendingReconcileFailureCode,
) {
	result.Failed++
	if result.FailureDetails == nil {
		result.FailureDetails = &PendingReconcileFailureDetails{}
	}
	if len(result.FailureDetails.Items) >= MaxPendingReconcileFailureDetails {
		result.FailureDetails.Omitted++
		return
	}
	result.FailureDetails.Items = append(result.FailureDetails.Items, PendingReconcileFailure{
		OutcomeRowID: candidate.OutcomeRowID, TurnID: candidate.TurnID,
		ReservationID: candidate.ReservationID, SettlementKey: candidate.SettlementKey,
		Code: code,
	})
}
