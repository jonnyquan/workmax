package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

var _ ReconcileStore = (*SQLStore)(nil)

// ReconcileTerminal retires one Turn that has no live executor and cannot
// resume on its own.
//
// It is not an execution path. It takes no lease, writes no Operation receipt
// and produces no Effects, because nothing ran. It does bump the Turn fence:
// an executor that was merely partitioned rather than dead must not be able to
// commit against the epoch this pass just retired.
//
// The caller's Reason is treated as a precondition, never an instruction. The
// Turn's state is re-derived under lock from the same predicates
// ListReclaimableTurns used, so a Reconciler that raced a recovering worker
// fails with ErrReconcilePrecondition instead of terminating live work.
//
// The compatibility path releases the reservation in the same transaction only
// when no durable Operation/Effect evidence exists. An exact Provider Usage
// binding always opens a durable Settlement Review, including when no receipt
// has arrived yet. Other ambiguous partial work either opens a Review through a
// capable authority or fails closed with ErrSettlementUsageUnknown before
// mutating lifecycle state.
func (store *SQLStore) ReconcileTerminal(ctx context.Context, command ReconcileCommand) (ReconcileResult, error) {
	if err := contextError(ctx); err != nil {
		return ReconcileResult{}, err
	}
	if err := command.Validate(); err != nil {
		return ReconcileResult{}, err
	}
	terminal, ok := command.Reason.TerminalStatus()
	if !ok {
		return ReconcileResult{}, fmt.Errorf("reconcile reason %q has no terminal status", command.Reason)
	}
	_, providerUsageMode, err := store.providerUsageTerminalization(terminal)
	if err != nil {
		return ReconcileResult{}, err
	}

	var result ReconcileResult
	var providerReviewAuthority SettlementReviewProviderUsageAuthority
	providerBindingLocked := false
	unlockProviderBinding := func() {
		if providerBindingLocked {
			providerBindingLocked = false
			store.settlementMu.RUnlock()
		}
	}
	defer unlockProviderBinding()
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockTurn(tx, "turn_id = ?", string(command.TurnID))
		if err != nil {
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if providerUsageMode {
			// Retain the exact Provider binding from the authoritative Turn lock
			// through the enclosing database commit. persistSettlementReview's
			// ordinary helper lock would end before commit and cannot be nested
			// here when a compatibility writer is already waiting.
			store.settlementMu.RLock()
			providerBindingLocked = true
			authority, _, bindingErr := store.settlementReviewProviderUsageAuthorityLocked()
			if bindingErr != nil {
				return bindingErr
			}
			providerReviewAuthority = authority
		}
		if turn.Status.Terminal() {
			// Another pass, or the Turn's own executor, already finished it.
			result = ReconcileResult{Turn: turn, TerminalStatus: turn.Status}
			reconciledEvent, markerReviewID, markerDigest, err := store.terminalSettlementReviewMarkerTx(tx, row, turn)
			if err != nil {
				return err
			}
			if review, found, err := store.lookupSettlementReviewTx(tx, turn.ID, false); err != nil {
				return err
			} else if found {
				if err := store.validateTerminalSettlementReviewTx(tx, row, turn, review); err != nil {
					return err
				}
				switch review.Source {
				case SettlementReviewSourceExecutor, SettlementReviewSourceExecutorCompletion,
					SettlementReviewSourceExecutorTerminal:
					if reconciledEvent || markerReviewID != review.ReviewID || markerDigest != review.RequestDigest {
						return ErrStoreIntegrity
					}
				case SettlementReviewSourceReconcile, SettlementReviewSourceReconcileTerminal:
					if !reconciledEvent || markerReviewID != review.ReviewID || markerDigest != review.RequestDigest {
						return ErrStoreIntegrity
					}
				default:
					return ErrStoreIntegrity
				}
				result.SettlementReview = &review
			} else {
				if markerReviewID != "" || markerDigest != "" {
					return ErrStoreIntegrity
				}
				var orphanedHolds int64
				if err := tx.Table(SQLEffectOutboxTable).
					Where("turn_id = ? AND status = ?", string(turn.ID), string(EffectStatusReviewHold)).
					Count(&orphanedHolds).Error; err != nil {
					return err
				}
				if orphanedHolds != 0 {
					return ErrStoreIntegrity
				}
			}
			return nil
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		budget, err := store.turnAttemptBudget()
		if err != nil {
			return err
		}

		expired, err := store.expiredActiveAttempt(tx, row, now)
		if err != nil {
			return err
		}
		if err := verifyReconcilePrecondition(command.Reason, row, expired != nil, budget); err != nil {
			return err
		}
		if command.Reason == ReclaimReasonReservationExpired {
			// The unlocked commercial scan is only discovery. Re-prove expiry
			// after Turn/Attempt ownership is locked and before any lifecycle,
			// Event, settlement or Reservation mutation.
			var expiryErr error
			if providerBindingLocked {
				expiryErr = store.verifyExpiredTurnReservationLocked(tx, turn)
			} else {
				expiryErr = store.verifyExpiredTurnReservation(tx, turn)
			}
			if expiryErr != nil {
				return expiryErr
			}
		}
		if !CanTransition(turn.Status, terminal) {
			return transitionError(turn.Status, terminal)
		}
		if terminal == agentv1.TurnStatusStopped && turn.CancelRequestedAt == nil {
			return ErrCancellationNotRequested
		}
		// Reconciliation has no authoritative usage measurement. Existing
		// Operation/Effect evidence means "nothing ran" is not provable, so do
		// not mutate the fence, lifecycle rows or reservation.
		var reviewEvidence *SettlementUsageEvidence
		if providerUsageMode {
			evidence, err := inspectAmbiguousRelease(tx, turn.ID, 0)
			if err != nil {
				return err
			}
			providerCount, err := countProviderUsageJournalTx(tx, turn.ID)
			if err != nil {
				return err
			}
			evidence.PriorProviderUsageCount = providerCount
			reviewEvidence = &evidence
		} else if store.hasSettlementAuthority() {
			evidence, err := inspectAmbiguousRelease(tx, turn.ID, 0)
			if err != nil {
				return err
			}
			if evidence.ambiguous() {
				if _, reviewCapable := store.reviewAuthority(); !reviewCapable {
					return ErrSettlementUsageUnknown
				}
				reviewEvidence = &evidence
			}
		}

		fence, err := nextExecutionFence(row.FencingToken)
		if err != nil {
			return err
		}
		next := cloneTurn(turn)
		next.Status = terminal
		next.UpdatedAt = now
		next.FinishedAt = timePointer(now)
		if next.StartedAt == nil && turn.Status == agentv1.TurnStatusRunning {
			// A running Turn without startedAt is schema drift, not something
			// reconciliation should paper over with an invented start time.
			return ErrStoreIntegrity
		}
		if err := next.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		key := reconcileSettlementKey(next.ID, fence)
		var pendingReview *SettlementReviewRecord
		if reviewEvidence != nil {
			var review SettlementReviewRecord
			var err error
			if providerUsageMode {
				review, err = buildProviderSettlementReviewRecord(
					next, terminal, SettlementReviewSourceReconcileTerminal,
					"", fence, "", key, *reviewEvidence, now,
				)
			} else {
				review, err = buildSettlementReviewRecord(
					next, terminal, SettlementReviewSourceReconcile,
					"", fence, "", key, *reviewEvidence, now,
				)
			}
			if err != nil {
				return err
			}
			pendingReview = &review
		}

		columns := map[string]any{
			"status":            string(next.Status),
			"updated_at":        next.UpdatedAt,
			"finished_at":       next.FinishedAt,
			"fencing_token":     int64(fence),
			"active_attempt_id": nil,
		}
		fencedAttemptID := ""
		if expired != nil {
			if err := store.updateAttemptColumns(tx, expired.ID, map[string]any{
				"status":      string(AttemptStatusExpired),
				"finished_at": now,
				"updated_at":  now,
			}); err != nil {
				return err
			}
			fencedAttemptID = expired.AttemptID
		}

		sequence, err := nextSQLSequence(row.LastEventSequence)
		if err != nil {
			return err
		}
		draft, err := reconcileEvent(command.Reason, terminal, pendingReview)
		if err != nil {
			return err
		}
		event, err := buildEvent(next, sequence, draft)
		if err != nil {
			return err
		}
		if err := store.insertEvent(tx, event, now); err != nil {
			return err
		}
		columns["last_event_sequence"] = int64(sequence)
		if err := store.updateTurnColumns(tx, next.ID, columns); err != nil {
			return err
		}
		reviewOpenedAt := now
		if reviewEvidence != nil {
			if err := holdTurnEffectsForReview(tx, next.ID, now); err != nil {
				return err
			}
			reviewOpenedAt, err = store.executionNow(ctx, tx)
			if err != nil {
				return err
			}
		}

		// The compatibility path releases a provably empty retirement
		// immediately. A Provider-aware path always opens Review; other durable
		// ambiguity does so when supported. The stable key uses the retiring
		// fence because reconciliation itself creates no Operation receipt.
		var settlementReview *SettlementReviewRecord
		if pendingReview != nil {
			pendingReview.CreatedAt = reviewOpenedAt
			pendingReview.UpdatedAt = reviewOpenedAt
			var persistErr error
			if providerUsageMode {
				persistErr = persistSettlementReviewWithAuthority(
					tx, next, *pendingReview, providerReviewAuthority,
				)
			} else {
				persistErr = store.persistSettlementReview(tx, next, *pendingReview)
			}
			if persistErr != nil {
				return persistErr
			}
			review := *pendingReview
			settlementReview = &review
		} else if err := store.settle(tx, SettlementCommand{
			TurnID: next.ID, PrincipalID: next.PrincipalID, SettlementKey: key,
			AuthorizationKind: SettlementAuthorizationReconcile, FencingToken: fence,
			Intent: SettlementIntentRelease, TerminalStatus: terminal,
		}); err != nil {
			return err
		}

		result = ReconcileResult{
			Turn:             next,
			Event:            event,
			TerminalStatus:   terminal,
			Changed:          true,
			FencedAttemptID:  fencedAttemptID,
			SettlementReview: settlementReview,
		}
		return nil
	})
	unlockProviderBinding()
	if txErr != nil {
		return ReconcileResult{}, store.normalize("reconcile-terminal", txErr)
	}
	return result, nil
}

// verifyReconcilePrecondition re-checks, under lock, the exact condition the
// unlocked scan reported. Each branch fails closed: if the Turn recovered, was
// claimed by a live worker, or no longer matches the claimed reason, the pass
// aborts rather than guessing a terminal state.
func verifyReconcilePrecondition(reason ReclaimReason, row sqlTurnRow, hasExpiredAttempt bool, budget int64) error {
	if row.ActiveAttemptID != nil && !hasExpiredAttempt {
		// A live lease means an executor still owns this Turn.
		return ErrReconcilePrecondition
	}
	switch reason {
	case ReclaimReasonCancellationPending:
		if row.CancelRequestedAt == nil {
			return ErrReconcilePrecondition
		}
	case ReclaimReasonAttemptsExhausted:
		if row.CancelRequestedAt != nil {
			// Cancellation is the more specific outcome; re-issue the command
			// with that reason so the Turn lands on `stopped`, not `timeout`.
			return ErrReconcilePrecondition
		}
		if row.FencingToken < budget {
			return ErrReconcilePrecondition
		}
	case ReclaimReasonReservationExpired:
		if row.CancelRequestedAt != nil {
			// Cancellation remains the more specific user intent and must land on
			// stopped through cancellation_pending, never timeout here.
			return ErrReconcilePrecondition
		}
		switch row.Status {
		case string(agentv1.TurnStatusQueued):
			// A queued expiry is the never-started shape. Any prior fence or
			// active/start timestamp is lifecycle drift, not an expiry candidate.
			if row.ActiveAttemptID != nil || row.FencingToken != 0 || row.StartedAt != nil {
				return ErrStoreIntegrity
			}
		case string(agentv1.TurnStatusRunning):
			// Running work may be retired only after its execution lease lapses.
			// Reservation TTL by itself never fences a live authorized Attempt.
			if row.ActiveAttemptID == nil || !hasExpiredAttempt || row.FencingToken < 1 || row.StartedAt == nil {
				return ErrReconcilePrecondition
			}
		default:
			return ErrReconcilePrecondition
		}
	default:
		return ErrReconcilePrecondition
	}
	return nil
}

type reconcileEventData struct {
	Status                 agentv1.TurnStatus `json:"status"`
	CancellationRequested  bool               `json:"cancellationRequested,omitempty"`
	Reconciled             bool               `json:"reconciled"`
	Reason                 ReclaimReason      `json:"reason,omitempty"`
	SettlementReviewID     string             `json:"settlementReviewId,omitempty"`
	SettlementReviewDigest string             `json:"settlementReviewDigest,omitempty"`
}

// reconcileEvent carries the retirement reason in the terminal event so
// observers and audit can distinguish a Turn its executor finished from one an
// operator pass retired. A Review marker is an immutable recovery receipt: it
// lets a later pass detect a missing Reconcile Review even when no Effect row
// exists to retain review_hold.
func reconcileEvent(
	reason ReclaimReason,
	terminal agentv1.TurnStatus,
	review *SettlementReviewRecord,
) (EventDraft, error) {
	payload := reconcileEventData{
		Status: terminal, CancellationRequested: terminal == agentv1.TurnStatusStopped,
		Reconciled: true, Reason: reason,
	}
	if review != nil {
		if (review.Source != SettlementReviewSourceReconcile &&
			review.Source != SettlementReviewSourceReconcileTerminal) ||
			review.TerminalStatus != terminal {
			return EventDraft{}, ErrStoreIntegrity
		}
		payload.SettlementReviewID = review.ReviewID
		payload.SettlementReviewDigest = review.RequestDigest
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return EventDraft{}, err
	}
	draft := EventDraft{Type: agentv1.EventCoreTurnStatus, Data: data}
	if err := draft.Validate(); err != nil {
		return EventDraft{}, err
	}
	return draft, nil
}

func (store *SQLStore) terminalSettlementReviewMarkerTx(
	tx *gorm.DB,
	turnRow sqlTurnRow,
	turn Turn,
) (bool, string, string, error) {
	if tx == nil || !turn.Status.Terminal() || turnRow.LastEventSequence < 1 {
		return false, "", "", ErrStoreIntegrity
	}
	var row sqlTurnEventRow
	if err := tx.Where("turn_id = ? AND sequence = ?", turnRow.TurnID, turnRow.LastEventSequence).
		Take(&row).Error; err != nil {
		return false, "", "", ErrStoreIntegrity
	}
	event, err := row.toEnvelope(turn)
	if err != nil || event.Type != agentv1.EventCoreTurnStatus {
		return false, "", "", ErrStoreIntegrity
	}
	var payload reconcileEventData
	if err := json.Unmarshal(event.Data, &payload); err != nil || payload.Status != turn.Status {
		return false, "", "", ErrStoreIntegrity
	}
	if !payload.Reconciled {
		if payload.Reason != "" ||
			(payload.SettlementReviewID == "") != (payload.SettlementReviewDigest == "") {
			return false, "", "", ErrStoreIntegrity
		}
		if payload.SettlementReviewID != "" &&
			(validatePrintableASCII("settlementReviewId", payload.SettlementReviewID, MaxSettlementReviewIDBytes) != nil ||
				validatePrintableASCII("settlementReviewDigest", payload.SettlementReviewDigest, MaxSettlementReviewDigestBytes) != nil) {
			return false, "", "", ErrStoreIntegrity
		}
		return false, payload.SettlementReviewID, payload.SettlementReviewDigest, nil
	}
	status, ok := payload.Reason.TerminalStatus()
	if !ok || status != turn.Status ||
		(payload.SettlementReviewID == "") != (payload.SettlementReviewDigest == "") {
		return false, "", "", ErrStoreIntegrity
	}
	if payload.SettlementReviewID != "" {
		if validatePrintableASCII("settlementReviewId", payload.SettlementReviewID, MaxSettlementReviewIDBytes) != nil ||
			validatePrintableASCII("settlementReviewDigest", payload.SettlementReviewDigest, MaxSettlementReviewDigestBytes) != nil {
			return false, "", "", ErrStoreIntegrity
		}
	}
	return true, payload.SettlementReviewID, payload.SettlementReviewDigest, nil
}

// expiredActiveAttempt returns the Turn's active Attempt only when its lease
// has lapsed. A live Attempt yields nil so the caller rejects the precondition
// rather than fencing a working executor.
func (store *SQLStore) expiredActiveAttempt(tx *gorm.DB, row sqlTurnRow, now time.Time) (*sqlTurnAttemptRow, error) {
	if row.ActiveAttemptID == nil {
		return nil, nil
	}
	attempt, err := store.lockAttempt(tx, row.TurnID, *row.ActiveAttemptID, row.FencingToken)
	if err != nil {
		return nil, err
	}
	if attempt.Status != string(AttemptStatusRunning) {
		return nil, ErrStoreIntegrity
	}
	if attempt.LeaseExpiresAt.After(now) {
		return nil, nil
	}
	return &attempt, nil
}

const (
	DefaultReconcileInterval       = 30 * time.Second
	MaxReconcileInterval           = time.Hour
	DefaultReconcileJitterFraction = 0.2
)

// Reconciler retires Turns that cannot make progress on their own.
//
// ReconcileOnce is one bounded synchronous pass and stays the unit of
// behaviour; Run only schedules it. The Reconciler owns no leader election and
// no partitioning, so several instances may scan the same rows — that is safe
// because ReconcileTerminal arbitrates under lock, but it is not efficient,
// and a production deployment still needs one of the two.
type Reconciler struct {
	scanner   ReclaimScanner
	store     ReconcileStore
	options   ReconcilerOptions
	admission *AdmissionGate
}

type ReconcilerOptions struct {
	ReconcilerID          string
	ReconcilerBuildDigest string
	// AdmissionGate is the shared, one-way authority boundary for starting a
	// ReconcileTerminal mutation. Nil preserves legacy candidate behaviour.
	AdmissionGate *AdmissionGate
	// BatchLimit bounds one pass. Zero selects DefaultReclaimScanLimit.
	BatchLimit int
	// Interval is the pause between passes that found nothing more to do.
	// Zero selects DefaultReconcileInterval. Only Run uses it.
	Interval time.Duration
	// JitterFraction spreads passes across instances so several Reconcilers
	// started by the same deploy do not scan in lockstep and contend on the
	// same rows every tick. Zero selects DefaultReconcileJitterFraction.
	JitterFraction float64
	// Rand supplies jitter in [0,1). Injectable so tests are deterministic.
	Rand func() float64
}

func (options *ReconcilerOptions) applyDefaults() {
	if options.Interval <= 0 {
		options.Interval = DefaultReconcileInterval
	}
	if options.JitterFraction == 0 {
		options.JitterFraction = DefaultReconcileJitterFraction
	}
	if options.Rand == nil {
		options.Rand = rand.Float64
	}
}

func (options ReconcilerOptions) Validate() error {
	if err := validatePrintableASCII("reconcilerId", options.ReconcilerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("reconcilerBuildDigest", options.ReconcilerBuildDigest, MaxWorkerBuildDigestBytes); err != nil {
		return err
	}
	if options.Interval <= 0 || options.Interval > MaxReconcileInterval {
		return fmt.Errorf("interval must be between 1ns and %s", MaxReconcileInterval)
	}
	if options.JitterFraction < 0 || options.JitterFraction >= 1 {
		return fmt.Errorf("jitterFraction must be in [0,1)")
	}
	return ReclaimQuery{Limit: options.BatchLimit}.Validate()
}

func NewReconciler(scanner ReclaimScanner, store ReconcileStore, options ReconcilerOptions) (*Reconciler, error) {
	if scanner == nil || store == nil {
		return nil, fmt.Errorf("reconciler requires a scanner and a reconcile store")
	}
	options.applyDefaults()
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &Reconciler{
		scanner: scanner, store: store, options: options,
		admission: options.AdmissionGate,
	}, nil
}

// MatchesAdmissionGate proves the exact gate identity captured at
// construction.
func (reconciler *Reconciler) MatchesAdmissionGate(expected *AdmissionGate) bool {
	return reconciler != nil && reconciler.admission == expected
}

// ReconcileReport is the outcome of one pass. Skipped counts Turns that
// recovered between the scan and the locked retirement; that is the expected
// benign race, not an error.
type ReconcileReport struct {
	Scanned  int
	Retired  []ReconcileResult
	Skipped  int
	Failures []ReconcileFailure
}

type ReconcileFailure struct {
	TurnID agentv1.TurnID
	Reason ReclaimReason
	Err    error
}

// ReconcileOnce retires every actionable Turn in one bounded page.
//
// A single Turn's failure never aborts the pass: one poisoned row must not
// stall reconciliation for every other stuck Turn. Context cancellation does
// stop it, so a shutting-down process exits promptly.
func (reconciler *Reconciler) ReconcileOnce(ctx context.Context) (ReconcileReport, error) {
	if err := contextError(ctx); err != nil {
		return ReconcileReport{}, err
	}
	candidates, err := reconciler.scanner.ListReclaimableTurns(ctx, ReclaimQuery{
		Limit:          reconciler.options.BatchLimit,
		ActionableOnly: true,
	})
	if err != nil {
		return ReconcileReport{}, err
	}

	report := ReconcileReport{Scanned: len(candidates)}
	for _, candidate := range candidates {
		if err := contextError(ctx); err != nil {
			return report, err
		}
		if err := reconciler.admission.Acquire(); err != nil {
			return report, err
		}
		result, err := reconciler.store.ReconcileTerminal(ctx, ReconcileCommand{
			TurnID:                candidate.TurnID,
			Reason:                candidate.Reason,
			ReconcilerID:          reconciler.options.ReconcilerID,
			ReconcilerBuildDigest: reconciler.options.ReconcilerBuildDigest,
		})
		switch {
		case err == nil && result.Changed:
			report.Retired = append(report.Retired, result)
		case err == nil:
			// Already terminal: another pass or the executor itself won.
			report.Skipped++
		case errors.Is(err, ErrReconcilePrecondition), errors.Is(err, ErrTurnTerminal),
			errors.Is(err, ErrTurnNotFound), errors.Is(err, ErrCancellationNotRequested):
			report.Skipped++
		default:
			report.Failures = append(report.Failures, ReconcileFailure{
				TurnID: candidate.TurnID, Reason: candidate.Reason, Err: err,
			})
		}
	}
	return report, nil
}

// Run schedules ReconcileOnce until ctx is cancelled.
//
// A pass that filled its batch is followed immediately by another: a backlog
// larger than one page must drain at scan speed rather than one page per
// interval, or a burst of crashed Turns would take hours to retire.
//
// There is no drain window. A pass is idempotent and re-runnable, so stopping
// mid-backlog loses nothing; the next process to run picks the rest up.
func (reconciler *Reconciler) Run(ctx context.Context, observe func(ReconcileReport, error)) error {
	return reconciler.RunWithPulse(ctx, observe, nil)
}

// RunWithPulse is Run plus a scheduler-progress pulse. An empty or benign
// reconcile pass still counts: health must not depend on business backlog.
func (reconciler *Reconciler) RunWithPulse(ctx context.Context, observe func(ReconcileReport, error), pulse func()) error {
	for {
		if err := contextError(ctx); err != nil {
			return err
		}
		observeLoopPulse(pulse)
		report, err := reconciler.ReconcileOnce(ctx)
		if observe != nil && !errors.Is(err, ErrAdmissionClosed) {
			observe(report, err)
		}
		if err != nil {
			if errors.Is(err, ErrAdmissionClosed) {
				return waitForAdmissionShutdown(ctx)
			}
			if ctxErr := contextError(ctx); ctxErr != nil {
				return ctxErr
			}
			// A scan failure backs off rather than spinning on a sick store.
			if sleepErr := sleepContext(ctx, reconciler.nextDelay()); sleepErr != nil {
				return sleepErr
			}
			continue
		}
		if report.Scanned >= reconciler.batchLimit() && report.Scanned > 0 {
			continue
		}
		if sleepErr := sleepContext(ctx, reconciler.nextDelay()); sleepErr != nil {
			return sleepErr
		}
	}
}

func (reconciler *Reconciler) batchLimit() int {
	return ReclaimQuery{Limit: reconciler.options.BatchLimit}.limit()
}

// nextDelay spreads the interval over [interval*(1-f), interval*(1+f)].
func (reconciler *Reconciler) nextDelay() time.Duration {
	fraction := reconciler.options.JitterFraction
	if fraction <= 0 {
		return reconciler.options.Interval
	}
	offset := (reconciler.options.Rand()*2 - 1) * fraction
	delay := time.Duration(float64(reconciler.options.Interval) * (1 + offset))
	if delay <= 0 {
		return time.Nanosecond
	}
	return delay
}
