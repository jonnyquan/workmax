package agentbilling

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
	"server/model"
	"server/service/account"
	"server/service/agentturn"
)

// CreditSettlementAuthority is the transaction-local adapter shared by Turn
// admission, execution authorization, terminal settlement and Review
// resolution. It performs no network I/O and never resolves a Reservation by
// a mutable request field: every path starts from the immutable binding row.
type CreditSettlementAuthority struct {
	db                     *gorm.DB
	reservations           *account.CreditReservationService
	pendingReconcileMu     sync.Mutex
	pendingReconcileCursor uint64
	pendingReconcileHigh   uint64
}

func NewCreditSettlementAuthority(
	db *gorm.DB,
	reservations *account.CreditReservationService,
) (*CreditSettlementAuthority, error) {
	if db == nil || db.Config == nil || db.Dialector == nil || reservations == nil {
		return nil, ErrLedgerUnavailable
	}
	if dialect := db.Dialector.Name(); dialect != "mysql" && dialect != "sqlite" {
		return nil, fmt.Errorf("%w: unsupported dialect %q", ErrLedgerUnavailable, dialect)
	}
	return &CreditSettlementAuthority{db: db, reservations: reservations}, nil
}

// ReservationAdmission is server-owned commercial input. PrincipalID comes
// from the authenticated credential adapter; Reservation and pricing facts
// come from policy/quote resolution and must not be decoded from Desktop JSON.
type ReservationAdmission struct {
	PrincipalID           agentturn.PrincipalID
	Reservation           account.ReservationRequest
	PricingSnapshotDigest string
}

func (admission ReservationAdmission) Validate() error {
	if admission.PrincipalID == "" || len(admission.PrincipalID) > agentturn.MaxPrincipalIDBytes ||
		!sha256Digest(admission.PricingSnapshotDigest) {
		return ErrBindingConflict
	}
	if _, err := account.CanonicalReservationRequestDigest(admission.Reservation); err != nil {
		return ErrBindingConflict
	}
	if err := validateASCII("reservationTool", admission.Reservation.Tool, maxReservationToolBytes); err != nil {
		return err
	}
	return nil
}

// Admission returns the per-request sealed hook consumed by
// Service.StartWithReservationAuthority. Capturing the exact spec prevents a
// replay from substituting another Reservation after a Turn ID is known.
func (authority *CreditSettlementAuthority) Admission(
	spec ReservationAdmission,
) (agentturn.TurnReservationAdmissionAuthority, error) {
	if authority == nil || authority.reservations == nil {
		return nil, ErrLedgerUnavailable
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	requestDigest, err := account.CanonicalReservationRequestDigest(spec.Reservation)
	if err != nil {
		return nil, ErrBindingConflict
	}
	return &reservationAdmissionAuthority{
		ledger: authority, spec: spec, reservationRequestDigest: requestDigest,
	}, nil
}

type reservationAdmissionAuthority struct {
	ledger                   *CreditSettlementAuthority
	spec                     ReservationAdmission
	reservationRequestDigest string
}

func (authority *reservationAdmissionAuthority) ReserveAndBindTurn(tx *gorm.DB, turn agentturn.Turn) error {
	if authority == nil || authority.ledger == nil || tx == nil || turn.PrincipalID != authority.spec.PrincipalID ||
		turn.Status != agentv1.TurnStatusQueued {
		return ErrBindingConflict
	}
	if _, found, err := lockBindingByTurn(tx, turn.ID); err != nil {
		return err
	} else if found {
		return ErrBindingConflict
	}
	result, err := authority.ledger.reservations.Reserve(tx, authority.spec.Reservation)
	if err != nil {
		return err
	}
	// A pre-existing Reservation cannot be adopted by a newly-created Turn.
	// Exact Turn admission replay uses VerifyTurnBinding instead.
	if !result.Created || result.Reservation == nil {
		return ErrBindingConflict
	}
	reservation := result.Reservation
	if reservation.RequestDigest != authority.reservationRequestDigest {
		return ErrBindingConflict
	}
	record := BindingRecord{
		BindingID: bindingID(turn.ID), TurnID: turn.ID, PrincipalID: turn.PrincipalID,
		TurnCommandDigest: turn.CommandDigest, ReservationID: uint64(reservation.Id),
		ReservationUID: reservation.UID, ReservationRequestDigest: reservation.RequestDigest,
		ReservationTool: reservation.Tool, ReservedUnits: int64(reservation.Reserved),
		ProjectID: uint64(reservation.ProjectID), PricingSnapshotDigest: authority.spec.PricingSnapshotDigest,
		CreatedAt: reservation.CreatedAt.UTC(),
	}
	record.BindingDigest = bindingRecordDigest(record)
	row, err := bindingSQLRow(record)
	if err != nil {
		return err
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	return authority.ledger.verifyBindingReservation(tx, record, false)
}

func (authority *reservationAdmissionAuthority) VerifyTurnBinding(tx *gorm.DB, turn agentturn.Turn) error {
	if authority == nil || authority.ledger == nil || tx == nil || turn.PrincipalID != authority.spec.PrincipalID {
		return ErrBindingConflict
	}
	binding, found, err := lockBindingByTurn(tx, turn.ID)
	if err != nil {
		return err
	}
	if !found {
		return ErrBindingNotFound
	}
	if binding.PrincipalID != turn.PrincipalID || binding.TurnCommandDigest != turn.CommandDigest ||
		binding.ReservationUID != authority.spec.Reservation.UID ||
		binding.ReservationRequestDigest != authority.reservationRequestDigest ||
		binding.ReservationTool != authority.spec.Reservation.Tool ||
		binding.ReservedUnits != int64(authority.spec.Reservation.Reserved) ||
		binding.ProjectID != uint64(authority.spec.Reservation.ProjectID) ||
		binding.PricingSnapshotDigest != authority.spec.PricingSnapshotDigest {
		return ErrBindingConflict
	}
	return authority.ledger.verifyBindingReservation(tx, binding, false)
}

// Settle implements agentturn.SettlementAuthority. The Turn row has already
// been terminalized and locked by the kernel; re-locking it documents and
// defends the required owner-first order for direct/recovery callers too.
func (authority *CreditSettlementAuthority) Settle(tx *gorm.DB, command agentturn.SettlementCommand) error {
	if authority == nil || tx == nil || authority.reservations == nil {
		return ErrLedgerUnavailable
	}
	if err := command.Validate(); err != nil {
		return err
	}
	turn, err := lockTurnOwner(tx, command.TurnID)
	if err != nil {
		return err
	}
	if turn.PrincipalID != string(command.PrincipalID) || turn.Status != string(command.TerminalStatus) {
		return ErrBindingConflict
	}
	if _, found, err := lockReviewOwnerByTurn(tx, command.TurnID); err != nil {
		return err
	} else if found {
		// A Review owns the only commercial decision for its Turn. An ordinary
		// settlement must never publish a second outcome beside it.
		return ErrOutcomeConflict
	}
	binding, err := authority.lockExactBinding(tx, command.TurnID, command.PrincipalID)
	if err != nil {
		return err
	}
	requestDigest := ordinaryRequestDigest(binding, command)
	existing, found, err := lockOutcomeByTurn(tx, command.TurnID)
	if err != nil {
		return err
	}
	if found {
		if !ordinaryOutcomeMatches(existing, binding, command, requestDigest) {
			return ErrOutcomeConflict
		}
		if existing.Status != OutcomeStatusRefundPending {
			snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
			if err != nil {
				return err
			}
			return verifyOutcomeSnapshot(existing, binding, snapshot)
		}
	}

	before, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
	if err != nil {
		return err
	}
	if err := snapshotMatchesBinding(binding, before); err != nil {
		return err
	}
	if !found && before.Status != model.CreditReservationStatusReserved {
		return ErrReservationStateDrift
	}
	used, err := settlementUnits(command)
	if err != nil {
		return err
	}
	var snapshot account.ReservationSettlementSnapshot
	switch command.Intent {
	case agentturn.SettlementIntentFinalize:
		snapshot, err = authority.reservations.FinalizeAfterTurnAuthorizationWithResult(
			tx, uint(binding.ReservationID), used,
		)
	case agentturn.SettlementIntentRelease:
		snapshot, err = authority.reservations.ReleaseWithResult(tx, uint(binding.ReservationID))
	default:
		return ErrOutcomeConflict
	}
	if err != nil {
		return err
	}
	outcome, err := ordinaryOutcome(binding, command, requestDigest, snapshot, existing, found)
	if err != nil {
		return err
	}
	return persistOutcome(tx, outcome, existing, found)
}

// AuthorizeTurnExecution is the fresh-claim guard. It verifies the exact
// immutable binding and an unexpired reserved hold while the kernel owns the
// Turn lock. It never extends TTL: admission must freeze a TTL compatible with
// queue and execution policy, and an expired hold requires reconciliation,
// not a worker-side commercial rewrite.
func (authority *CreditSettlementAuthority) AuthorizeTurnExecution(tx *gorm.DB, turn agentturn.Turn) error {
	if authority == nil || tx == nil || authority.reservations == nil {
		return ErrLedgerUnavailable
	}
	if err := turn.Validate(); err != nil || turn.Status.Terminal() {
		return ErrReservationIneligible
	}
	owner, err := lockTurnOwner(tx, turn.ID)
	if err != nil {
		return err
	}
	if owner.PrincipalID != string(turn.PrincipalID) || owner.CommandDigest != turn.CommandDigest ||
		owner.Status != string(turn.Status) {
		return ErrBindingConflict
	}
	binding, err := authority.lockExactBinding(tx, turn.ID, turn.PrincipalID)
	if err != nil {
		return err
	}
	snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
	if err != nil {
		return err
	}
	if err := snapshotMatchesBinding(binding, snapshot); err != nil {
		return err
	}
	if snapshot.Status != model.CreditReservationStatusReserved {
		return ErrReservationIneligible
	}
	now, err := billingDatabaseNow(tx)
	if err != nil {
		return err
	}
	if !snapshot.ExpiresAt.After(now) {
		// This sentinel is intentionally emitted only after the complete
		// Turn/Binding/Reservation tuple and reserved state were proven under
		// lock. The kernel may therefore treat it as candidate-local expiry;
		// every missing/drifted/non-reserved case remains opaque Unauthorized.
		return agentturn.ErrTurnReservationExecutionExpired
	}
	return nil
}

// VerifyExpiredTurnReservation implements agentturn.TurnReservationExpiryAuthority.
// Reconciliation enters with the Turn (and any expired active Attempt) locked.
// This method completes the owner-first commercial lock order and proves that
// no earlier Review/Outcome owns a decision before observing the exact bound
// Reservation as reserved and expired on the database clock.
func (authority *CreditSettlementAuthority) VerifyExpiredTurnReservation(
	tx *gorm.DB,
	turn agentturn.Turn,
) error {
	if authority == nil || tx == nil || authority.reservations == nil {
		return ErrLedgerUnavailable
	}
	if err := turn.Validate(); err != nil || turn.Status.Terminal() {
		return agentturn.ErrReconcilePrecondition
	}
	owner, err := lockTurnOwner(tx, turn.ID)
	if err != nil {
		return err
	}
	if owner.PrincipalID != string(turn.PrincipalID) || owner.CommandDigest != turn.CommandDigest ||
		owner.Status != string(turn.Status) {
		return ErrBindingConflict
	}
	if _, found, err := lockReviewOwnerByTurn(tx, turn.ID); err != nil {
		return err
	} else if found {
		return ErrOutcomeConflict
	}
	binding, err := authority.lockExactBinding(tx, turn.ID, turn.PrincipalID)
	if err != nil {
		return err
	}
	if _, found, err := lockOutcomeByTurn(tx, turn.ID); err != nil {
		return err
	} else if found {
		return ErrOutcomeConflict
	}
	snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
	if err != nil {
		return err
	}
	if err := snapshotMatchesBinding(binding, snapshot); err != nil {
		return err
	}
	if snapshot.Status != model.CreditReservationStatusReserved {
		return ErrReservationIneligible
	}
	now, err := billingDatabaseNow(tx)
	if err != nil {
		return err
	}
	if snapshot.ExpiresAt.After(now) {
		return agentturn.ErrReconcilePrecondition
	}
	return nil
}

// HoldForReview implements agentturn.SettlementReviewAuthority. The Review
// row is already inserted by the kernel; this method verifies it before moving
// the exact Reservation into review_hold and publishing the held projection.
func (authority *CreditSettlementAuthority) HoldForReview(
	tx *gorm.DB,
	command agentturn.SettlementReviewHoldCommand,
) error {
	if authority == nil || tx == nil || authority.reservations == nil {
		return ErrLedgerUnavailable
	}
	if err := command.Validate(); err != nil {
		return err
	}
	turn, err := lockTurnOwner(tx, command.Review.TurnID)
	if err != nil {
		return err
	}
	if turn.PrincipalID != string(command.PrincipalID) || turn.Status != string(command.Review.TerminalStatus) {
		return ErrBindingConflict
	}
	if err := verifyReviewOwner(tx, command.Review, agentturn.SettlementReviewStatusPending); err != nil {
		return err
	}
	binding, err := authority.lockExactBinding(tx, command.Review.TurnID, command.PrincipalID)
	if err != nil {
		return err
	}
	requestDigest := reviewRequestDigest(binding, command)
	existing, found, err := lockOutcomeByTurn(tx, command.Review.TurnID)
	if err != nil {
		return err
	}
	if found && !reviewOutcomeMatches(existing, binding, command, requestDigest) {
		return ErrOutcomeConflict
	}
	before, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
	if err != nil {
		return err
	}
	if err := snapshotMatchesBinding(binding, before); err != nil {
		return err
	}
	if !found && before.Status != model.CreditReservationStatusReserved {
		return ErrReservationStateDrift
	}
	hold := account.ReservationReviewHold{
		ReviewID: command.Review.ReviewID, SettlementKey: command.Review.SettlementKey,
		RequestDigest: command.Review.RequestDigest,
	}
	snapshot, err := authority.reservations.HoldForReviewAfterTurnAuthorizationWithResult(
		tx, uint(binding.ReservationID), hold,
	)
	if err != nil {
		return err
	}
	outcome, err := heldOutcome(binding, command, requestDigest, snapshot, existing, found)
	if err != nil {
		return err
	}
	return persistOutcome(tx, outcome, existing, found)
}

// ResolveReview implements the positive, metered Review resolution boundary.
// refund_pending is a real successful commercial result: the immutable
// resolution decision commits while the outcome projection stays pending
// until the exact refund target is recovered.
func (authority *CreditSettlementAuthority) ResolveReview(
	tx *gorm.DB,
	command agentturn.SettlementReviewResolutionAuthorityCommand,
) (agentturn.SettlementReviewResolutionAuthorityReceipt, error) {
	if authority == nil || tx == nil || authority.reservations == nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, ErrLedgerUnavailable
	}
	if err := command.Validate(); err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	turn, err := lockTurnOwner(tx, command.Review.TurnID)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	if turn.PrincipalID != string(command.PrincipalID) || turn.Status != string(command.Review.TerminalStatus) {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, ErrBindingConflict
	}
	if err := verifyReviewOwner(tx, command.Review, agentturn.SettlementReviewStatusMeteredHeld); err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	binding, err := authority.lockExactBinding(tx, command.Review.TurnID, command.PrincipalID)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	if binding.PricingSnapshotDigest != command.PricingSnapshotDigest {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, ErrPricingSnapshotConflict
	}
	outcome, found, err := lockOutcomeByTurn(tx, command.Review.TurnID)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	holdCommand := agentturn.SettlementReviewHoldCommand{
		Review: command.Review, PrincipalID: command.PrincipalID,
	}
	if !found || !reviewOutcomeMatches(
		outcome, binding, holdCommand, reviewRequestDigest(binding, holdCommand),
	) {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, ErrOutcomeConflict
	}
	if outcome.ResolutionID != nil && (*outcome.ResolutionID != command.ResolutionID ||
		outcome.ResolutionRequestDigest == nil || *outcome.ResolutionRequestDigest != command.DecisionDigest) {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, ErrOutcomeConflict
	}
	used, err := positiveUnits(command.UsedUnits)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	hold := account.ReservationReviewHold{
		ReviewID: command.Review.ReviewID, SettlementKey: command.Review.SettlementKey,
		RequestDigest: command.Review.RequestDigest,
	}
	snapshot, err := authority.reservations.FinalizeReviewWithResult(
		tx, uint(binding.ReservationID), hold, used,
	)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	updated, err := resolvedReviewOutcome(binding, outcome, command, snapshot)
	if err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	if err := persistOutcome(tx, updated, outcome, true); err != nil {
		return agentturn.SettlementReviewResolutionAuthorityReceipt{}, err
	}
	receipt := agentturn.SettlementReviewResolutionAuthorityReceipt{
		ResolutionID: command.ResolutionID, DecisionDigest: command.DecisionDigest,
		EvidenceID: command.Evidence.EvidenceID, EvidenceDigest: command.Evidence.EvidenceDigest,
		PricingSnapshotDigest: command.PricingSnapshotDigest, UsedUnits: command.UsedUnits,
		ReservedUnits: binding.ReservedUnits,
	}
	receipt.ReceiptDigest = resolutionAuthorityReceiptDigest(binding, command)
	return receipt, nil
}

// GetOutcome is the read-only unknown-commit recovery surface. Ownership is
// mandatory; no caller can probe a SettlementKey or Reservation by opaque ID.
func (authority *CreditSettlementAuthority) GetOutcome(
	ctx context.Context,
	principalID agentturn.PrincipalID,
	turnID agentv1.TurnID,
) (OutcomeRecord, error) {
	if authority == nil || authority.db == nil || ctx == nil {
		return OutcomeRecord{}, ErrLedgerUnavailable
	}
	var result OutcomeRecord
	err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn, err := lockTurnOwner(tx, turnID)
		if err != nil {
			return err
		}
		if turn.PrincipalID != string(principalID) {
			return ErrBindingNotFound
		}
		review, reviewFound, err := lockReviewOwnerByTurn(tx, turnID)
		if err != nil {
			return err
		}
		binding, err := authority.lockExactBinding(tx, turnID, principalID)
		if err != nil {
			return err
		}
		outcome, found, err := lockOutcomeByTurn(tx, turnID)
		if err != nil {
			return err
		}
		if !found {
			return ErrOutcomeNotFound
		}
		if err := verifyOutcomeReviewOwner(review, reviewFound, outcome); err != nil {
			return err
		}
		if err := verifyOutcomeResolutionOwner(tx, binding, review, reviewFound, outcome); err != nil {
			return err
		}
		snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
		if err != nil {
			return err
		}
		if err := verifyOutcomeSnapshot(outcome, binding, snapshot); err != nil {
			return err
		}
		result = outcome
		return nil
	})
	return result, err
}

// ReconcilePending replays only the target already frozen in a refund_pending
// outcome. It takes the Turn owner lock first and can therefore replace the
// legacy Reservation sweeper for Agent-bound rows without reversing locks.
func (authority *CreditSettlementAuthority) ReconcilePending(
	ctx context.Context,
	principalID agentturn.PrincipalID,
	turnID agentv1.TurnID,
) (OutcomeRecord, error) {
	if authority == nil || authority.db == nil || ctx == nil {
		return OutcomeRecord{}, ErrLedgerUnavailable
	}
	var result OutcomeRecord
	err := authority.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		turn, err := lockTurnOwner(tx, turnID)
		if err != nil {
			return err
		}
		if turn.PrincipalID != string(principalID) {
			return ErrBindingNotFound
		}
		review, reviewFound, err := lockReviewOwnerByTurn(tx, turnID)
		if err != nil {
			return err
		}
		binding, err := authority.lockExactBinding(tx, turnID, principalID)
		if err != nil {
			return err
		}
		outcome, found, err := lockOutcomeByTurn(tx, turnID)
		if err != nil {
			return err
		}
		if !found {
			return ErrOutcomeNotFound
		}
		if err := verifyOutcomeReviewOwner(review, reviewFound, outcome); err != nil {
			return err
		}
		if err := verifyOutcomeResolutionOwner(tx, binding, review, reviewFound, outcome); err != nil {
			return err
		}
		if outcome.Status != OutcomeStatusRefundPending {
			snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
			if err != nil {
				return err
			}
			if err := verifyOutcomeSnapshot(outcome, binding, snapshot); err != nil {
				return err
			}
			result = outcome
			return nil
		}
		var snapshot account.ReservationSettlementSnapshot
		switch outcome.RequestedIntent {
		case RequestedIntentFinalize:
			if outcome.UsedUnits == nil {
				return ErrOutcomeConflict
			}
			used, err := positiveUnits(*outcome.UsedUnits)
			if err != nil {
				return err
			}
			snapshot, err = authority.reservations.FinalizeAfterTurnAuthorizationWithResult(
				tx, uint(binding.ReservationID), used,
			)
			if err != nil {
				return err
			}
		case RequestedIntentRelease:
			snapshot, err = authority.reservations.ReleaseWithResult(tx, uint(binding.ReservationID))
			if err != nil {
				return err
			}
		case RequestedIntentReview:
			if outcome.UsedUnits == nil || outcome.ReviewID == nil || outcome.ReviewRequestDigest == nil {
				return ErrOutcomeConflict
			}
			used, err := positiveUnits(*outcome.UsedUnits)
			if err != nil {
				return err
			}
			hold := account.ReservationReviewHold{
				ReviewID: *outcome.ReviewID, SettlementKey: outcome.SettlementKey,
				RequestDigest: *outcome.ReviewRequestDigest,
			}
			snapshot, err = authority.reservations.FinalizeReviewWithResult(tx, uint(binding.ReservationID), hold, used)
			if err != nil {
				return err
			}
		default:
			return ErrOutcomeConflict
		}
		updated, err := outcomeFromSnapshot(binding, outcome, snapshot)
		if err != nil {
			return err
		}
		if err := persistOutcome(tx, updated, outcome, true); err != nil {
			return err
		}
		result = updated
		return nil
	})
	return result, err
}

func (authority *CreditSettlementAuthority) lockExactBinding(
	tx *gorm.DB,
	turnID agentv1.TurnID,
	principalID agentturn.PrincipalID,
) (BindingRecord, error) {
	binding, found, err := lockBindingByTurn(tx, turnID)
	if err != nil {
		return BindingRecord{}, err
	}
	if !found {
		return BindingRecord{}, ErrBindingNotFound
	}
	if binding.PrincipalID != principalID {
		return BindingRecord{}, ErrBindingConflict
	}
	return binding, nil
}

func (authority *CreditSettlementAuthority) verifyBindingReservation(
	tx *gorm.DB,
	binding BindingRecord,
	requireActive bool,
) error {
	snapshot, err := authority.reservations.LockSettlementSnapshot(tx, uint(binding.ReservationID))
	if err != nil {
		return err
	}
	if err := snapshotMatchesBinding(binding, snapshot); err != nil {
		return err
	}
	if requireActive && snapshot.Status != model.CreditReservationStatusReserved {
		return ErrReservationIneligible
	}
	return nil
}

type turnOwnerRow struct {
	TurnID        string `gorm:"column:turn_id"`
	PrincipalID   string `gorm:"column:principal_id"`
	CommandDigest string `gorm:"column:command_digest"`
	Status        string `gorm:"column:status"`
}

func lockTurnOwner(tx *gorm.DB, turnID agentv1.TurnID) (turnOwnerRow, error) {
	if tx == nil {
		return turnOwnerRow{}, ErrLedgerUnavailable
	}
	var row turnOwnerRow
	if err := tx.Table(agentturn.SQLTurnTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("turn_id = ?", string(turnID)).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return turnOwnerRow{}, ErrBindingNotFound
	} else if err != nil {
		return turnOwnerRow{}, err
	}
	return row, nil
}

type reviewOwnerRow struct {
	ReviewID                string    `gorm:"column:review_id"`
	TurnID                  string    `gorm:"column:turn_id"`
	SettlementKey           string    `gorm:"column:settlement_key"`
	RequestDigest           string    `gorm:"column:request_digest"`
	Reason                  string    `gorm:"column:reason"`
	Source                  string    `gorm:"column:source"`
	TerminalStatus          string    `gorm:"column:terminal_status"`
	AttemptID               *string   `gorm:"column:attempt_id"`
	FencingToken            int64     `gorm:"column:fencing_token"`
	OperationID             *string   `gorm:"column:operation_id"`
	PriorOperationCount     int64     `gorm:"column:prior_operation_count"`
	PriorEffectCount        int64     `gorm:"column:prior_effect_count"`
	PriorProviderUsageCount int64     `gorm:"column:prior_provider_usage_count"`
	CurrentEffectCount      int       `gorm:"column:current_effect_count"`
	Status                  string    `gorm:"column:status"`
	CreatedAt               time.Time `gorm:"column:created_at"`
	UpdatedAt               time.Time `gorm:"column:updated_at"`
}

func (row reviewOwnerRow) record() (agentturn.SettlementReviewRecord, error) {
	review := agentturn.SettlementReviewRecord{
		ReviewID: row.ReviewID, TurnID: agentv1.TurnID(row.TurnID), SettlementKey: row.SettlementKey,
		RequestDigest: row.RequestDigest, Reason: row.Reason,
		Source: agentturn.SettlementReviewSource(row.Source), TerminalStatus: agentv1.TurnStatus(row.TerminalStatus),
		AttemptID: optionalSQLString(row.AttemptID), FencingToken: agentv1.Sequence(row.FencingToken),
		OperationID: optionalSQLString(row.OperationID),
		Evidence: agentturn.SettlementUsageEvidence{
			PriorOperationCount: row.PriorOperationCount, PriorEffectCount: row.PriorEffectCount,
			PriorProviderUsageCount: row.PriorProviderUsageCount, CurrentEffectCount: row.CurrentEffectCount,
		},
		Status: row.Status, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if err := review.Validate(); err != nil {
		return agentturn.SettlementReviewRecord{}, ErrOutcomeConflict
	}
	return review, nil
}

func verifyReviewOwner(tx *gorm.DB, review agentturn.SettlementReviewRecord, status string) error {
	var row reviewOwnerRow
	if err := tx.Table(agentturn.SQLSettlementReviewTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("review_id = ?", review.ReviewID).Take(&row).Error; err != nil {
		return err
	}
	owner, err := row.record()
	if err != nil {
		return err
	}
	if owner.ReviewID != review.ReviewID || owner.TurnID != review.TurnID ||
		owner.SettlementKey != review.SettlementKey || owner.RequestDigest != review.RequestDigest ||
		owner.Status != status {
		return ErrOutcomeConflict
	}
	return nil
}

func lockReviewOwnerByTurn(
	tx *gorm.DB,
	turnID agentv1.TurnID,
) (agentturn.SettlementReviewRecord, bool, error) {
	if tx == nil {
		return agentturn.SettlementReviewRecord{}, false, ErrLedgerUnavailable
	}
	var row reviewOwnerRow
	err := tx.Table(agentturn.SQLSettlementReviewTable).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("turn_id = ?", string(turnID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return agentturn.SettlementReviewRecord{}, false, nil
	}
	if err != nil {
		return agentturn.SettlementReviewRecord{}, false, err
	}
	review, err := row.record()
	return review, err == nil, err
}

func verifyOutcomeReviewOwner(row agentturn.SettlementReviewRecord, found bool, outcome OutcomeRecord) error {
	if outcome.RequestedIntent != RequestedIntentReview {
		if found || outcome.ReviewID != nil || outcome.ReviewRequestDigest != nil ||
			outcome.ResolutionID != nil || outcome.ResolutionRequestDigest != nil {
			return ErrOutcomeConflict
		}
		return nil
	}
	if !found || outcome.ReviewID == nil || outcome.ReviewRequestDigest == nil ||
		row.ReviewID != *outcome.ReviewID || row.TurnID != outcome.TurnID ||
		row.SettlementKey != outcome.SettlementKey || row.RequestDigest != *outcome.ReviewRequestDigest ||
		row.TerminalStatus != outcome.TerminalStatus || row.FencingToken != outcome.FencingToken {
		return ErrOutcomeConflict
	}
	switch outcome.AuthorizationKind {
	case AuthorizationKindOperation:
		if row.AttemptID == "" || row.OperationID == "" || outcome.AttemptID == nil || outcome.OperationID == nil ||
			row.AttemptID != *outcome.AttemptID || row.OperationID != *outcome.OperationID {
			return ErrOutcomeConflict
		}
	case AuthorizationKindReconcile:
		if row.AttemptID != "" || row.OperationID != "" || outcome.AttemptID != nil || outcome.OperationID != nil {
			return ErrOutcomeConflict
		}
	default:
		return ErrOutcomeConflict
	}
	switch outcome.Status {
	case OutcomeStatusReviewHeld:
		if row.Status != agentturn.SettlementReviewStatusPending &&
			row.Status != agentturn.SettlementReviewStatusMeteredHeld {
			return ErrOutcomeConflict
		}
	case OutcomeStatusRefundPending, OutcomeStatusFinalized:
		if row.Status != agentturn.SettlementReviewStatusFinalizedHeld {
			return ErrOutcomeConflict
		}
	default:
		return ErrOutcomeConflict
	}
	return nil
}

type resolutionOwnerRow struct {
	ResolutionID           string    `gorm:"column:resolution_id"`
	ReviewID               string    `gorm:"column:review_id"`
	TurnID                 string    `gorm:"column:turn_id"`
	SettlementKey          string    `gorm:"column:settlement_key"`
	ReviewRequestDigest    string    `gorm:"column:review_request_digest"`
	EvidenceID             string    `gorm:"column:evidence_id"`
	PricingSnapshotDigest  string    `gorm:"column:pricing_snapshot_digest"`
	DecisionDigest         string    `gorm:"column:decision_digest"`
	ResolutionDigest       string    `gorm:"column:resolution_digest"`
	Intent                 string    `gorm:"column:intent"`
	UsedUnits              int64     `gorm:"column:used_units"`
	ReservedUnits          int64     `gorm:"column:reserved_units"`
	ActorID                string    `gorm:"column:actor_id"`
	Reason                 string    `gorm:"column:reason"`
	EvidenceDigest         string    `gorm:"column:evidence_digest"`
	AuthorityReceiptDigest string    `gorm:"column:authority_receipt_digest"`
	CreatedAt              time.Time `gorm:"column:created_at"`
}

func (row resolutionOwnerRow) record() (agentturn.SettlementReviewResolutionRecord, error) {
	resolution := agentturn.SettlementReviewResolutionRecord{
		ResolutionID: row.ResolutionID, ReviewID: row.ReviewID, TurnID: agentv1.TurnID(row.TurnID),
		SettlementKey: row.SettlementKey, ReviewRequestDigest: row.ReviewRequestDigest,
		EvidenceID: row.EvidenceID, PricingSnapshotDigest: row.PricingSnapshotDigest,
		DecisionDigest: row.DecisionDigest, ResolutionDigest: row.ResolutionDigest,
		Intent: agentturn.SettlementIntent(row.Intent), UsedUnits: row.UsedUnits, ReservedUnits: row.ReservedUnits,
		ActorID: row.ActorID, Reason: row.Reason, EvidenceDigest: row.EvidenceDigest,
		AuthorityReceiptDigest: row.AuthorityReceiptDigest, CreatedAt: row.CreatedAt.UTC(),
	}
	if err := resolution.Validate(); err != nil {
		return agentturn.SettlementReviewResolutionRecord{}, ErrOutcomeConflict
	}
	return resolution, nil
}

func verifyOutcomeResolutionOwner(
	tx *gorm.DB,
	binding BindingRecord,
	review agentturn.SettlementReviewRecord,
	reviewFound bool,
	outcome OutcomeRecord,
) error {
	if outcome.ResolutionID == nil && outcome.ResolutionRequestDigest == nil {
		if tx == nil {
			return ErrLedgerUnavailable
		}
		// A Review without a projected Resolution must not already have an
		// immutable child receipt. Checking by the parent key closes the inverse
		// side of the one-to-one projection instead of trusting nullable Outcome
		// columns alone.
		if outcome.ReviewID != nil {
			var child struct {
				ResolutionID string `gorm:"column:resolution_id"`
			}
			err := tx.Table(agentturn.SQLSettlementReviewResolutionTable).
				Select("resolution_id").
				Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("review_id = ?", *outcome.ReviewID).Take(&child).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			return ErrOutcomeConflict
		}
		return nil
	}
	if tx == nil || outcome.ResolutionID == nil || outcome.ResolutionRequestDigest == nil ||
		outcome.ReviewID == nil || outcome.ReviewRequestDigest == nil || outcome.UsedUnits == nil {
		return ErrOutcomeConflict
	}
	var row resolutionOwnerRow
	if err := tx.Table(agentturn.SQLSettlementReviewResolutionTable).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("resolution_id = ?", *outcome.ResolutionID).Take(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrOutcomeConflict
	} else if err != nil {
		return err
	}
	resolution, err := row.record()
	if err != nil {
		return err
	}
	if !reviewFound || review.ReviewID != resolution.ReviewID || review.TurnID != resolution.TurnID ||
		review.SettlementKey != resolution.SettlementKey || review.RequestDigest != resolution.ReviewRequestDigest ||
		!resolution.CreatedAt.Equal(review.UpdatedAt) {
		return ErrOutcomeConflict
	}
	return verifyResolutionProjection(binding, outcome, resolution)
}

func verifyResolutionProjection(
	binding BindingRecord,
	outcome OutcomeRecord,
	resolution agentturn.SettlementReviewResolutionRecord,
) error {
	if outcome.ResolutionID == nil || outcome.ResolutionRequestDigest == nil ||
		outcome.ReviewID == nil || outcome.ReviewRequestDigest == nil || outcome.UsedUnits == nil ||
		resolution.ResolutionID != *outcome.ResolutionID || resolution.ReviewID != *outcome.ReviewID ||
		resolution.TurnID != outcome.TurnID || resolution.SettlementKey != outcome.SettlementKey ||
		resolution.ReviewRequestDigest != *outcome.ReviewRequestDigest ||
		resolution.DecisionDigest != *outcome.ResolutionRequestDigest || resolution.UsedUnits != *outcome.UsedUnits ||
		resolution.ReservedUnits != outcome.ReservedUnits || resolution.ReservedUnits != binding.ReservedUnits ||
		resolution.PricingSnapshotDigest != binding.PricingSnapshotDigest ||
		resolution.AuthorityReceiptDigest != resolutionAuthorityReceiptDigestFromRecord(binding, resolution) {
		return ErrOutcomeConflict
	}
	return nil
}

func optionalSQLString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func snapshotMatchesBinding(binding BindingRecord, snapshot account.ReservationSettlementSnapshot) error {
	if snapshot.ReservationID == 0 || uint64(snapshot.ReservationID) != binding.ReservationID ||
		snapshot.UID != binding.ReservationUID || snapshot.RequestDigest != binding.ReservationRequestDigest ||
		snapshot.Tool != binding.ReservationTool || int64(snapshot.Reserved) != binding.ReservedUnits ||
		uint64(snapshot.ProjectID) != binding.ProjectID || snapshot.StateVersion == 0 {
		return ErrReservationStateDrift
	}
	return nil
}

func settlementUnits(command agentturn.SettlementCommand) (int, error) {
	if command.Intent == agentturn.SettlementIntentRelease {
		if command.UsedUnits != 0 {
			return 0, ErrOutcomeConflict
		}
		return 0, nil
	}
	return positiveUnits(command.UsedUnits)
}

func positiveUnits(units int64) (int, error) {
	if units <= 0 || units > int64(^uint32(0)>>1) {
		return 0, ErrReservationIneligible
	}
	return int(units), nil
}

func ordinaryRequestDigest(binding BindingRecord, command agentturn.SettlementCommand) string {
	return digest(
		"agent-turn-settlement-ledger-request-v1", binding.BindingDigest, command.SettlementKey,
		string(command.AuthorizationKind), command.AttemptID,
		strconv.FormatInt(int64(command.FencingToken), 10), command.OperationID,
		string(command.TerminalStatus), string(command.Intent), strconv.FormatInt(command.UsedUnits, 10),
	)
}

func reviewRequestDigest(binding BindingRecord, command agentturn.SettlementReviewHoldCommand) string {
	return digest(
		"agent-turn-settlement-review-ledger-request-v1", binding.BindingDigest,
		command.Review.SettlementKey, command.Review.ReviewID, command.Review.RequestDigest,
		command.Review.AttemptID, strconv.FormatInt(int64(command.Review.FencingToken), 10),
		command.Review.OperationID,
		string(command.Review.TerminalStatus),
	)
}

func ordinaryOutcomeMatches(
	outcome OutcomeRecord,
	binding BindingRecord,
	command agentturn.SettlementCommand,
	requestDigest string,
) bool {
	wantIntent := RequestedIntentFinalize
	if command.Intent == agentturn.SettlementIntentRelease {
		wantIntent = RequestedIntentRelease
	}
	return outcome.BindingID == binding.BindingID && outcome.BindingDigest == binding.BindingDigest &&
		outcome.ReservationID == binding.ReservationID && outcome.SettlementKey == command.SettlementKey &&
		outcome.LedgerRequestDigest == requestDigest && outcome.TerminalStatus == command.TerminalStatus &&
		outcome.AuthorizationKind == AuthorizationKind(command.AuthorizationKind) &&
		optionalString(outcome.AttemptID) == command.AttemptID && outcome.FencingToken == command.FencingToken &&
		optionalString(outcome.OperationID) == command.OperationID &&
		outcome.RequestedIntent == wantIntent && outcome.ReviewID == nil && outcome.ResolutionID == nil
}

func reviewOutcomeMatches(
	outcome OutcomeRecord,
	binding BindingRecord,
	command agentturn.SettlementReviewHoldCommand,
	requestDigest string,
) bool {
	kind, attemptID, operationID := reviewAuthorization(command.Review)
	return outcome.BindingID == binding.BindingID && outcome.BindingDigest == binding.BindingDigest &&
		outcome.ReservationID == binding.ReservationID && outcome.SettlementKey == command.Review.SettlementKey &&
		outcome.LedgerRequestDigest == requestDigest && outcome.TerminalStatus == command.Review.TerminalStatus &&
		outcome.AuthorizationKind == kind && optionalString(outcome.AttemptID) == attemptID &&
		outcome.FencingToken == command.Review.FencingToken && optionalString(outcome.OperationID) == operationID &&
		outcome.RequestedIntent == RequestedIntentReview && outcome.ReviewID != nil &&
		*outcome.ReviewID == command.Review.ReviewID && outcome.ReviewRequestDigest != nil &&
		*outcome.ReviewRequestDigest == command.Review.RequestDigest
}

func ordinaryOutcome(
	binding BindingRecord,
	command agentturn.SettlementCommand,
	requestDigest string,
	snapshot account.ReservationSettlementSnapshot,
	existing OutcomeRecord,
	found bool,
) (OutcomeRecord, error) {
	intent := RequestedIntentFinalize
	used := command.UsedUnits
	if command.Intent == agentturn.SettlementIntentRelease {
		intent = RequestedIntentRelease
		used = 0
	}
	outcome := OutcomeRecord{
		OutcomeID: outcomeID(command.SettlementKey), BindingID: binding.BindingID, TurnID: binding.TurnID,
		ReservationID: binding.ReservationID, BindingDigest: binding.BindingDigest,
		SettlementKey: command.SettlementKey, LedgerRequestDigest: requestDigest,
		AuthorizationKind: AuthorizationKind(command.AuthorizationKind), FencingToken: command.FencingToken,
		TerminalStatus: command.TerminalStatus, RequestedIntent: intent, UsedUnits: int64Pointer(used),
		ReservedUnits: binding.ReservedUnits,
	}
	if command.AttemptID != "" {
		outcome.AttemptID = stringPointer(command.AttemptID)
	}
	if command.OperationID != "" {
		outcome.OperationID = stringPointer(command.OperationID)
	}
	if found {
		outcome.CreatedAt = existing.CreatedAt
	}
	return outcomeFromSnapshot(binding, outcome, snapshot)
}

func heldOutcome(
	binding BindingRecord,
	command agentturn.SettlementReviewHoldCommand,
	requestDigest string,
	snapshot account.ReservationSettlementSnapshot,
	existing OutcomeRecord,
	found bool,
) (OutcomeRecord, error) {
	if snapshot.Status != model.CreditReservationStatusReviewHold ||
		snapshot.HoldReviewID != command.Review.ReviewID ||
		snapshot.HoldSettlementKey != command.Review.SettlementKey ||
		snapshot.HoldRequestDigest != command.Review.RequestDigest {
		return OutcomeRecord{}, ErrReservationStateDrift
	}
	timestamp, err := snapshotTime(snapshot)
	if err != nil {
		return OutcomeRecord{}, err
	}
	outcome := OutcomeRecord{
		OutcomeID: outcomeID(command.Review.SettlementKey), BindingID: binding.BindingID,
		TurnID: binding.TurnID, ReservationID: binding.ReservationID, BindingDigest: binding.BindingDigest,
		SettlementKey: command.Review.SettlementKey, LedgerRequestDigest: requestDigest,
		FencingToken:   command.Review.FencingToken,
		TerminalStatus: command.Review.TerminalStatus, RequestedIntent: RequestedIntentReview,
		ReservedUnits: binding.ReservedUnits, Status: OutcomeStatusReviewHeld,
		ReservationStateVersion: snapshot.StateVersion,
		ReviewID:                stringPointer(command.Review.ReviewID),
		ReviewRequestDigest:     stringPointer(command.Review.RequestDigest),
		CreatedAt:               timestamp, UpdatedAt: timestamp,
	}
	outcome.AuthorizationKind, outcome.AttemptID, outcome.OperationID = reviewAuthorizationPointers(command.Review)
	if found {
		outcome.CreatedAt = existing.CreatedAt
	}
	outcome.OutcomeDigest = outcomeRecordDigest(outcome)
	if err := outcome.Validate(); err != nil {
		return OutcomeRecord{}, err
	}
	return outcome, nil
}

func reviewAuthorization(review agentturn.SettlementReviewRecord) (AuthorizationKind, string, string) {
	if review.AttemptID == "" && review.OperationID == "" {
		return AuthorizationKindReconcile, "", ""
	}
	return AuthorizationKindOperation, review.AttemptID, review.OperationID
}

func reviewAuthorizationPointers(
	review agentturn.SettlementReviewRecord,
) (AuthorizationKind, *string, *string) {
	kind, attemptID, operationID := reviewAuthorization(review)
	if kind == AuthorizationKindReconcile {
		return kind, nil, nil
	}
	return kind, stringPointer(attemptID), stringPointer(operationID)
}

func resolvedReviewOutcome(
	binding BindingRecord,
	outcome OutcomeRecord,
	command agentturn.SettlementReviewResolutionAuthorityCommand,
	snapshot account.ReservationSettlementSnapshot,
) (OutcomeRecord, error) {
	outcome.UsedUnits = int64Pointer(command.UsedUnits)
	outcome.ResolutionID = stringPointer(command.ResolutionID)
	outcome.ResolutionRequestDigest = stringPointer(command.DecisionDigest)
	return outcomeFromSnapshot(binding, outcome, snapshot)
}

func outcomeFromSnapshot(
	binding BindingRecord,
	outcome OutcomeRecord,
	snapshot account.ReservationSettlementSnapshot,
) (OutcomeRecord, error) {
	if err := snapshotMatchesBinding(binding, snapshot); err != nil {
		return OutcomeRecord{}, err
	}
	if outcome.UsedUnits == nil {
		return OutcomeRecord{}, ErrOutcomeConflict
	}
	used := *outcome.UsedUnits
	switch snapshot.Status {
	case model.CreditReservationStatusFinalized:
		if int64(snapshot.Used) != used {
			return OutcomeRecord{}, ErrReservationStateDrift
		}
		outcome.Status = OutcomeStatusFinalized
		outcome.RefundTarget = nil
		outcome.RefundDue = 0
	case model.CreditReservationStatusReleased:
		if used != 0 || snapshot.Used != 0 {
			return OutcomeRecord{}, ErrReservationStateDrift
		}
		outcome.Status = OutcomeStatusReleased
		outcome.RefundTarget = nil
		outcome.RefundDue = 0
	case model.CreditReservationStatusRefundPending:
		if snapshot.RefundTargetUsed == nil || int64(*snapshot.RefundTargetUsed) != used ||
			(snapshot.RefundTargetStatus != model.CreditReservationStatusFinalized &&
				snapshot.RefundTargetStatus != model.CreditReservationStatusReleased) ||
			(snapshot.RefundTargetStatus == model.CreditReservationStatusReleased && used != 0) {
			return OutcomeRecord{}, ErrReservationStateDrift
		}
		outcome.Status = OutcomeStatusRefundPending
		outcome.RefundTarget = stringPointer(snapshot.RefundTargetStatus)
		outcome.RefundDue = int64(snapshot.RefundDue)
	default:
		return OutcomeRecord{}, ErrReservationStateDrift
	}
	timestamp, err := snapshotTime(snapshot)
	if err != nil {
		return OutcomeRecord{}, err
	}
	if outcome.CreatedAt.IsZero() {
		outcome.CreatedAt = timestamp
	}
	outcome.UpdatedAt = timestamp
	outcome.ReservationStateVersion = snapshot.StateVersion
	outcome.OutcomeDigest = outcomeRecordDigest(outcome)
	if err := outcome.Validate(); err != nil {
		return OutcomeRecord{}, err
	}
	return outcome, nil
}

func snapshotTime(snapshot account.ReservationSettlementSnapshot) (time.Time, error) {
	if snapshot.StateChangedAt == nil || snapshot.StateChangedAt.IsZero() {
		return time.Time{}, ErrReservationStateDrift
	}
	return snapshot.StateChangedAt.UTC(), nil
}

func billingDatabaseNow(tx *gorm.DB) (time.Time, error) {
	if tx == nil || tx.Dialector == nil {
		return time.Time{}, ErrLedgerUnavailable
	}
	switch tx.Dialector.Name() {
	case "mysql":
		// Keep the result textual even when the production DSN enables
		// parseTime. Scanning a DATETIME into string otherwise depends on the
		// driver's RFC3339 conversion and the session location.
		var row struct {
			Now string `gorm:"column:billing_now"`
		}
		query := "SELECT DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%d %H:%i:%s.%f') AS billing_now"
		if err := tx.Raw(query).Scan(&row).Error; err != nil {
			return time.Time{}, err
		}
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05.999999", row.Now, time.UTC)
		if err != nil || parsed.IsZero() {
			return time.Time{}, ErrLedgerUnavailable
		}
		return parsed.UTC().Truncate(time.Microsecond), nil
	case "sqlite":
		var row struct {
			UnixSeconds int64 `gorm:"column:unix_seconds"`
		}
		if err := tx.Raw("SELECT CAST(strftime('%s', 'now') AS INTEGER) AS unix_seconds").Scan(&row).Error; err != nil {
			return time.Time{}, err
		}
		if row.UnixSeconds <= 0 {
			return time.Time{}, ErrLedgerUnavailable
		}
		return time.Unix(row.UnixSeconds, 0).In(time.Local), nil
	default:
		return time.Time{}, ErrLedgerUnavailable
	}
}

func verifyOutcomeSnapshot(
	outcome OutcomeRecord,
	binding BindingRecord,
	snapshot account.ReservationSettlementSnapshot,
) error {
	if outcome.Status == OutcomeStatusReviewHeld {
		if err := snapshotMatchesBinding(binding, snapshot); err != nil {
			return err
		}
		if snapshot.Status != model.CreditReservationStatusReviewHold || outcome.ReviewID == nil ||
			outcome.ReviewRequestDigest == nil || snapshot.HoldReviewID != *outcome.ReviewID ||
			snapshot.HoldSettlementKey != outcome.SettlementKey ||
			snapshot.HoldRequestDigest != *outcome.ReviewRequestDigest || snapshot.ReviewHeldAt == nil ||
			snapshot.StateChangedAt == nil || !snapshot.ReviewHeldAt.UTC().Equal(snapshot.StateChangedAt.UTC()) ||
			!snapshot.StateChangedAt.UTC().Equal(outcome.UpdatedAt.UTC()) ||
			snapshot.StateVersion != outcome.ReservationStateVersion {
			return ErrReservationStateDrift
		}
		return nil
	}
	want, err := outcomeFromSnapshot(binding, outcome, snapshot)
	if err != nil {
		return err
	}
	if want.Status != outcome.Status || optionalInt64(want.UsedUnits) != optionalInt64(outcome.UsedUnits) ||
		optionalString(want.RefundTarget) != optionalString(outcome.RefundTarget) ||
		want.RefundDue != outcome.RefundDue || want.ReservationStateVersion != outcome.ReservationStateVersion ||
		want.OutcomeDigest != outcome.OutcomeDigest || snapshot.StateChangedAt == nil ||
		!snapshot.StateChangedAt.UTC().Equal(outcome.UpdatedAt.UTC()) {
		return ErrReservationStateDrift
	}
	switch snapshot.Status {
	case model.CreditReservationStatusFinalized:
		if snapshot.FinalizedAt == nil || snapshot.ReleasedAt != nil || snapshot.StateChangedAt == nil ||
			!snapshot.FinalizedAt.UTC().Equal(snapshot.StateChangedAt.UTC()) {
			return ErrReservationStateDrift
		}
	case model.CreditReservationStatusReleased:
		if snapshot.ReleasedAt == nil || snapshot.FinalizedAt != nil || snapshot.StateChangedAt == nil ||
			!snapshot.ReleasedAt.UTC().Equal(snapshot.StateChangedAt.UTC()) {
			return ErrReservationStateDrift
		}
	case model.CreditReservationStatusRefundPending:
		if snapshot.FinalizedAt != nil || snapshot.ReleasedAt != nil {
			return ErrReservationStateDrift
		}
	}
	return nil
}

func persistOutcome(tx *gorm.DB, next, previous OutcomeRecord, exists bool) error {
	row, err := outcomeSQLRow(next)
	if err != nil {
		return err
	}
	if !exists {
		return tx.Create(&row).Error
	}
	// A retry can observe the same refund_pending snapshot while Credits'
	// database-clock backoff is still in force. Treat an exact durable result as
	// success instead of issuing a no-op UPDATE: MySQL normally reports zero
	// affected rows for unchanged values, which must not turn a valid
	// SettlementKey replay into a false conflict.
	if next.OutcomeID == previous.OutcomeID && next.OutcomeDigest == previous.OutcomeDigest {
		return nil
	}
	updated := tx.Table(OutcomeTable).
		Where("outcome_id = ? AND outcome_digest = ?", previous.OutcomeID, previous.OutcomeDigest).
		UpdateColumns(map[string]any{
			"used_units": next.UsedUnits, "status": string(next.Status),
			"refund_target": next.RefundTarget, "refund_due": next.RefundDue,
			"reservation_state_version": next.ReservationStateVersion,
			"resolution_id":             next.ResolutionID,
			"resolution_request_digest": next.ResolutionRequestDigest,
			"outcome_digest":            next.OutcomeDigest, "updated_at": next.UpdatedAt,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrOutcomeConflict
	}
	return nil
}

func resolutionAuthorityReceiptDigest(
	binding BindingRecord,
	command agentturn.SettlementReviewResolutionAuthorityCommand,
) string {
	return resolutionAuthorityReceiptDigestFields(
		binding, command.Review.SettlementKey, command.Review.ReviewID, command.Review.RequestDigest,
		command.ResolutionID, command.DecisionDigest, command.Evidence.EvidenceID,
		command.Evidence.EvidenceDigest, command.PricingSnapshotDigest, command.UsedUnits,
	)
}

func resolutionAuthorityReceiptDigestFromRecord(
	binding BindingRecord,
	resolution agentturn.SettlementReviewResolutionRecord,
) string {
	return resolutionAuthorityReceiptDigestFields(
		binding, resolution.SettlementKey, resolution.ReviewID, resolution.ReviewRequestDigest,
		resolution.ResolutionID, resolution.DecisionDigest, resolution.EvidenceID,
		resolution.EvidenceDigest, resolution.PricingSnapshotDigest, resolution.UsedUnits,
	)
}

func resolutionAuthorityReceiptDigestFields(
	binding BindingRecord,
	settlementKey string,
	reviewID string,
	reviewRequestDigest string,
	resolutionID string,
	decisionDigest string,
	evidenceID string,
	evidenceDigest string,
	pricingSnapshotDigest string,
	usedUnits int64,
) string {
	return digest(
		"agent-credit-settlement-authority-receipt-v1", binding.BindingDigest,
		settlementKey, reviewID, reviewRequestDigest, resolutionID, decisionDigest, evidenceID,
		evidenceDigest, pricingSnapshotDigest, strconv.FormatInt(usedUnits, 10),
		strconv.FormatInt(binding.ReservedUnits, 10),
	)
}

var (
	_ agentturn.SettlementReviewResolutionAuthority = (*CreditSettlementAuthority)(nil)
	_ agentturn.TurnReservationExecutionAuthority   = (*CreditSettlementAuthority)(nil)
	_ agentturn.TurnReservationAdmissionAuthority   = (*reservationAdmissionAuthority)(nil)
)
