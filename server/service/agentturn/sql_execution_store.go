package agentturn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

const (
	SQLTurnAttemptTable   = "w_agent_turn_attempt"
	SQLTurnOperationTable = "w_agent_turn_operation"
	SQLEffectOutboxTable  = "w_agent_effect_outbox"
	MaxAttemptLeaseTTL    = 10 * time.Minute
)

var _ ExecutionStore = (*SQLStore)(nil)

// The row mappings below mirror migration 20260666. They are mappings only:
// SQLStore never creates, migrates or repairs these tables.
type sqlTurnAttemptRow struct {
	ID                uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	AttemptID         string     `gorm:"column:attempt_id"`
	TurnID            string     `gorm:"column:turn_id"`
	FencingToken      int64      `gorm:"column:fencing_token"`
	Status            string     `gorm:"column:status"`
	WorkerID          string     `gorm:"column:worker_id"`
	WorkerBuildDigest string     `gorm:"column:worker_build_digest"`
	ClaimedAt         time.Time  `gorm:"column:claimed_at"`
	LastHeartbeatAt   time.Time  `gorm:"column:last_heartbeat_at"`
	LeaseExpiresAt    time.Time  `gorm:"column:lease_expires_at"`
	FinishedAt        *time.Time `gorm:"column:finished_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (sqlTurnAttemptRow) TableName() string { return SQLTurnAttemptTable }

type sqlTurnOperationRow struct {
	ID              uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	TurnID          string    `gorm:"column:turn_id"`
	OperationID     string    `gorm:"column:operation_id"`
	OperationDigest string    `gorm:"column:operation_digest"`
	AttemptID       string    `gorm:"column:attempt_id"`
	FencingToken    int64     `gorm:"column:fencing_token"`
	EventSequence   int64     `gorm:"column:event_sequence"`
	TurnStatus      string    `gorm:"column:turn_status"`
	EffectCount     int       `gorm:"column:effect_count"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

func (sqlTurnOperationRow) TableName() string { return SQLTurnOperationTable }

type sqlEffectOutboxRow struct {
	ID                   uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	OutboxID             string     `gorm:"column:outbox_id"`
	TurnID               string     `gorm:"column:turn_id"`
	AttemptID            string     `gorm:"column:attempt_id"`
	TurnFencingToken     int64      `gorm:"column:turn_fencing_token"`
	OperationID          string     `gorm:"column:operation_id"`
	Ordinal              int        `gorm:"column:ordinal"`
	Topic                string     `gorm:"column:topic"`
	DedupeKey            string     `gorm:"column:dedupe_key"`
	PayloadJSON          []byte     `gorm:"column:payload_json"`
	Status               string     `gorm:"column:status"`
	AvailableAt          time.Time  `gorm:"column:available_at"`
	DeliveryAttempts     int64      `gorm:"column:delivery_attempts"`
	DispatchFencingToken int64      `gorm:"column:dispatch_fencing_token"`
	LeaseOwnerID         *string    `gorm:"column:lease_owner_id"`
	LeaseExpiresAt       *time.Time `gorm:"column:lease_expires_at"`
	LastErrorCode        *string    `gorm:"column:last_error_code"`
	DeliveredAt          *time.Time `gorm:"column:delivered_at"`
	DeadLetteredAt       *time.Time `gorm:"column:dead_lettered_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (sqlEffectOutboxRow) TableName() string { return SQLEffectOutboxTable }

// ClaimAttempt gives exactly one worker a live database-arbitrated execution
// epoch for a Turn. A queue notification may choose the Turn, but this method
// is the ownership authority. The first claim moves queued -> running and
// appends the corresponding durable status event in the same transaction.
func (store *SQLStore) ClaimAttempt(ctx context.Context, command ClaimAttemptCommand) (ClaimAttemptResult, error) {
	if err := contextError(ctx); err != nil {
		return ClaimAttemptResult{}, err
	}
	if err := command.Validate(); err != nil {
		return ClaimAttemptResult{}, err
	}

	var result ClaimAttemptResult
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockTurn(tx, "turn_id = ?", string(command.TurnID))
		if err != nil {
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		// ClaimNext's unlocked scan is only a discovery optimization. Exact
		// Plugin ownership is rechecked under the authoritative Turn lock before
		// either replaying or creating an Attempt.
		if !claimPluginScopeAllows(command.PluginScope, turn.Plugin) {
			return ErrPluginScopeMismatch
		}
		if existing, found, err := store.lookupAttemptByIDTx(tx, command.AttemptID, true); err != nil {
			return err
		} else if found {
			attempt, err := existing.toAttempt()
			if err != nil {
				return ErrStoreIntegrity
			}
			if !attemptMatchesClaim(attempt, command) {
				return ErrAttemptConflict
			}
			if !turnOwnsAttempt(row, attempt) || attempt.Status != AttemptStatusRunning {
				return ErrAttemptFenced
			}
			now, err := store.executionNow(ctx, tx)
			if err != nil {
				return err
			}
			if !attempt.LeaseExpiresAt.After(now) {
				return ErrAttemptLeaseExpired
			}
			result = ClaimAttemptResult{Turn: turn, Attempt: attempt, Replay: true}
			return nil
		}

		// Read the lease clock only after FOR UPDATE has been acquired. Reading
		// it before a contended lock can make a losing claimant compare stale
		// time with the winner's newly committed timestamps.
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}

		if turn.Status.Terminal() {
			return ErrTurnTerminal
		}
		if turn.Status != agentv1.TurnStatusQueued && turn.Status != agentv1.TurnStatusRunning {
			return ErrStoreIntegrity
		}
		// A recorded cancellation intent stops new execution epochs. The
		// idempotent replay branch above still returns a live Attempt so its
		// owner can commit the `stopped` terminal; only fresh claims and
		// expired-Attempt reclaims are refused here.
		if turn.CancelRequestedAt != nil {
			return ErrAttemptCancelled
		}
		if agentv1.Sequence(row.FencingToken) == MaxDurableSequence {
			return ErrAttemptFenceExhausted
		}
		// The absolute schema bound above protects the column; the budget
		// below is the reliability policy. A Turn that consumed every allowed
		// epoch must be retired by a Reconciler, not handed to another worker
		// that will die the same way.
		budget, err := store.turnAttemptBudget()
		if err != nil {
			return err
		}
		if row.FencingToken >= budget {
			return ErrAttemptBudgetExhausted
		}
		if row.ActiveAttemptID == nil && row.FencingToken > 0 {
			return ErrStoreIntegrity
		}
		if row.ActiveAttemptID != nil && turn.Status != agentv1.TurnStatusRunning {
			return ErrStoreIntegrity
		}
		var expiredAttempt *sqlTurnAttemptRow
		if row.ActiveAttemptID != nil {
			current, err := store.lockAttempt(tx, row.TurnID, *row.ActiveAttemptID, row.FencingToken)
			if err != nil {
				return err
			}
			attempt, err := current.toAttempt()
			if err != nil || !turnOwnsAttempt(row, attempt) || attempt.Status != AttemptStatusRunning {
				return ErrStoreIntegrity
			}
			if attempt.LeaseExpiresAt.After(now) {
				return ErrAttemptBusy
			}
			expiredAttempt = &current
		}

		// Replay returned above because that epoch was authorized when it was
		// created. Every fresh epoch, including a reclaim, must prove commercial
		// execution authority before the old Attempt or Turn is mutated.
		if err := store.authorizeTurnReservationExecution(tx, turn); err != nil {
			return err
		}
		reclaimed := expiredAttempt != nil
		if expiredAttempt != nil {
			if err := store.updateAttemptColumns(tx, expiredAttempt.ID, map[string]any{
				"status":      string(AttemptStatusExpired),
				"finished_at": now,
				"updated_at":  now,
			}); err != nil {
				return err
			}
		}

		fence, err := nextExecutionFence(row.FencingToken)
		if err != nil {
			return err
		}
		leaseExpiresAt, err := store.executionLeaseExpiry(now)
		if err != nil {
			return err
		}
		attempt := TurnAttempt{
			ID:                command.AttemptID,
			TurnID:            command.TurnID,
			FencingToken:      fence,
			Status:            AttemptStatusRunning,
			WorkerID:          command.WorkerID,
			WorkerBuildDigest: command.WorkerBuildDigest,
			LeaseExpiresAt:    leaseExpiresAt,
			ClaimedAt:         now,
			LastHeartbeatAt:   now,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		attemptRow, err := attemptToSQLRow(attempt)
		if err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&attemptRow).Error; err != nil {
			return err
		}

		nextTurn := cloneTurn(turn)
		nextTurn.UpdatedAt = now
		columns := map[string]any{
			"active_attempt_id": attempt.ID,
			"fencing_token":     int64(fence),
			"updated_at":        now,
		}
		if turn.Status == agentv1.TurnStatusQueued {
			nextTurn.Status = agentv1.TurnStatusRunning
			nextTurn.StartedAt = timePointer(now)
			columns["status"] = string(nextTurn.Status)
			columns["started_at"] = nextTurn.StartedAt

			sequence, err := nextSQLSequence(row.LastEventSequence)
			if err != nil {
				return err
			}
			draft, err := statusEvent(agentv1.TurnStatusRunning, false)
			if err != nil {
				return err
			}
			event, err := buildEvent(nextTurn, sequence, draft)
			if err != nil {
				return err
			}
			if err := store.insertEvent(tx, event, now); err != nil {
				return err
			}
			columns["last_event_sequence"] = int64(sequence)
		}
		if err := nextTurn.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		if err := store.updateTurnColumns(tx, command.TurnID, columns); err != nil {
			return err
		}
		result = ClaimAttemptResult{
			Turn: nextTurn, Attempt: attempt, Claimed: true, Reclaimed: reclaimed,
		}
		return nil
	})
	if txErr == nil {
		return cloneClaimAttemptResult(result), nil
	}
	if err := contextError(ctx); err == nil {
		if replay, found, resolveErr := store.resolveClaim(ctx, command); resolveErr != nil {
			return ClaimAttemptResult{}, store.normalize("claim-attempt", resolveErr)
		} else if found {
			return replay, nil
		}
	} else {
		return ClaimAttemptResult{}, err
	}
	return ClaimAttemptResult{}, store.normalize("claim-attempt", txErr)
}

// HeartbeatAttempt extends only the exact, still-live Attempt named by the
// current Turn pointer and fencing token. Expiry equality is expired: a late
// heartbeat can never revive ownership after another worker may claim it.
func (store *SQLStore) HeartbeatAttempt(ctx context.Context, command HeartbeatAttemptCommand) (HeartbeatAttemptResult, error) {
	if err := contextError(ctx); err != nil {
		return HeartbeatAttemptResult{}, err
	}
	if err := command.Validate(); err != nil {
		return HeartbeatAttemptResult{}, err
	}
	var result HeartbeatAttemptResult
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		turnRow, err := store.lockTurn(tx, "turn_id = ?", string(command.Fence.TurnID))
		if err != nil {
			return err
		}
		turn, err := turnRow.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if !rowOwnsFence(turnRow, command.Fence) || turn.Status.Terminal() {
			return ErrAttemptFenced
		}
		attemptRow, err := store.lockAttempt(tx, string(command.Fence.TurnID), command.Fence.AttemptID, int64(command.Fence.FencingToken))
		if err != nil {
			if errors.Is(err, ErrAttemptNotFound) {
				return ErrAttemptFenced
			}
			return err
		}
		attempt, err := attemptRow.toAttempt()
		if err != nil {
			return ErrStoreIntegrity
		}
		if !attemptMatchesFence(attempt, command.Fence) || attempt.Status != AttemptStatusRunning {
			return ErrAttemptFenced
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		if !attempt.LeaseExpiresAt.After(now) {
			return ErrAttemptLeaseExpired
		}
		if now.Before(attempt.LastHeartbeatAt) {
			return ErrStoreIntegrity
		}
		leaseExpiresAt, err := store.executionLeaseExpiry(now)
		if err != nil {
			return err
		}
		if err := store.updateAttemptColumns(tx, attemptRow.ID, map[string]any{
			"last_heartbeat_at": now,
			"lease_expires_at":  leaseExpiresAt,
			"updated_at":        now,
		}); err != nil {
			return err
		}
		attempt.LastHeartbeatAt = now
		attempt.LeaseExpiresAt = leaseExpiresAt
		attempt.UpdatedAt = now
		result = HeartbeatAttemptResult{
			Attempt: attempt, CancelRequestedAt: cloneTimePointer(turn.CancelRequestedAt),
		}
		return nil
	})
	if err != nil {
		return HeartbeatAttemptResult{}, store.normalize("heartbeat-attempt", err)
	}
	result.Attempt = cloneTurnAttempt(result.Attempt)
	return result, nil
}

// CommitAttempt is the only worker mutation port in this candidate. It
// verifies the live fence, then commits one durable Event, an immutable
// operation receipt, optional external Effect Outbox rows and any terminal
// Turn/Attempt state in one short transaction. Stable operation identity makes
// a later retry a primary-database read instead of a blind side-effect replay.
func (store *SQLStore) CommitAttempt(ctx context.Context, command CommitAttemptCommand) (CommitAttemptResult, error) {
	if err := contextError(ctx); err != nil {
		return CommitAttemptResult{}, err
	}
	if err := command.Validate(); err != nil {
		return CommitAttemptResult{}, err
	}
	legacyNormalized, legacyDigest, err := normalizeCommitCommand(command)
	if err != nil {
		return CommitAttemptResult{}, err
	}
	legacy := commitOperationCandidate{
		Command: legacyNormalized, Digest: legacyDigest, Mode: operationTerminalizationLegacy,
	}
	candidates := []commitOperationCandidate{legacy}
	providerUsageMode := false
	completedUsageMode := false
	if command.TerminalStatus != "" {
		terminalization, enabled, terminalizationErr := store.providerUsageTerminalization(command.TerminalStatus)
		if terminalizationErr != nil {
			return CommitAttemptResult{}, terminalizationErr
		}
		if enabled {
			normalized, digest, normalizeErr := normalizeProviderUsageCommitCommand(command, terminalization)
			if normalizeErr != nil {
				return CommitAttemptResult{}, normalizeErr
			}
			candidates = []commitOperationCandidate{{
				Command: normalized, Digest: digest, Mode: operationTerminalizationProviderUsageReview,
			}}
			// P0-044 v3 receipts exist only for completed Turns. They remain a
			// read-only fallback after a deployment upgrades to the Provider
			// Journal binding; failed/stopped/timeout have no v3 interpretation.
			if command.TerminalStatus == agentv1.TurnStatusCompleted {
				v3Terminalization := newCompletedUsageTerminalization()
				v3Command, v3Digest, v3Err := normalizeCompletedUsageCommitCommand(command, v3Terminalization)
				if v3Err != nil {
					return CommitAttemptResult{}, v3Err
				}
				candidates = append(candidates, commitOperationCandidate{
					Command: v3Command, Digest: v3Digest, Mode: operationTerminalizationCompletedUsageReview,
				})
			}
			candidates = append(candidates, legacy)
			providerUsageMode = true
		}
	}
	if !providerUsageMode && command.TerminalStatus == agentv1.TurnStatusCompleted {
		terminalization, enabled, terminalizationErr := store.completedUsageTerminalization()
		if terminalizationErr != nil {
			return CommitAttemptResult{}, terminalizationErr
		}
		if enabled {
			normalized, digest, normalizeErr := normalizeCompletedUsageCommitCommand(command, terminalization)
			if normalizeErr != nil {
				return CommitAttemptResult{}, normalizeErr
			}
			candidates = []commitOperationCandidate{{
				Command: normalized, Digest: digest, Mode: operationTerminalizationCompletedUsageReview,
			}, legacy}
			completedUsageMode = true
		}
	}
	primary := candidates[0]
	normalized, digest := primary.Command, primary.Digest
	untrustedCompletedUsageAssertion :=
		(completedUsageMode && completedUsageCallerAssertionUntrusted(command)) ||
			(providerUsageMode && providerUsageCallerAssertionUntrusted(command))

	var result CommitAttemptResult
	var providerReviewAuthority SettlementReviewProviderUsageAuthority
	providerBindingLocked := false
	unlockProviderBinding := func() {
		if providerBindingLocked {
			providerBindingLocked = false
			store.settlementMu.RUnlock()
		}
	}
	// A callback panic must not strand the immutable production binding. The
	// explicit call immediately after writeTransaction narrows the hold to the
	// outer commit/rollback; this defer is the panic-safe backstop.
	defer unlockProviderBinding()
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		turnRow, err := store.lockTurn(tx, "turn_id = ?", string(normalized.Fence.TurnID))
		if err != nil {
			return err
		}
		turn, err := turnRow.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if providerUsageMode {
			// Lock order is Turn -> settlement binding. Keep this read lock after
			// HoldForReview returns until GORM has committed or rolled back, so a
			// compatibility mutator cannot invalidate the binding in that gap.
			store.settlementMu.RLock()
			providerBindingLocked = true
			authority, _, bindingErr := store.settlementReviewProviderUsageAuthorityLocked()
			if bindingErr != nil {
				return bindingErr
			}
			providerReviewAuthority = authority
		}

		// Lock order is Turn -> Attempt -> Operation. Receipt lookup still
		// precedes lease and active-pointer validation, so an unknown successful
		// commit remains replayable after its lease expires or the Turn finishes.
		attemptRow, attemptErr := store.lockAttempt(tx, string(normalized.Fence.TurnID), normalized.Fence.AttemptID, int64(normalized.Fence.FencingToken))
		if attemptErr != nil && !errors.Is(attemptErr, ErrAttemptNotFound) {
			return attemptErr
		}
		if operation, found, err := store.lookupOperationTx(tx, normalized.Fence.TurnID, normalized.OperationID, true); err != nil {
			return err
		} else if found {
			candidate, matched := selectOperationReplayCandidate(
				operation, candidates, untrustedCompletedUsageAssertion,
			)
			if !matched {
				if untrustedCompletedUsageAssertion {
					return ErrSettlementCompletedUsageUntrusted
				}
				return ErrOperationConflict
			}
			replayed, err := store.hydrateOperationResult(
				tx, turnRow, turn, operation, candidate.Command, candidate.Mode,
			)
			if err != nil {
				return err
			}
			if !attemptMatchesFence(replayed.Attempt, candidate.Command.Fence) {
				return ErrOperationConflict
			}
			replayed.Replay = true
			result = replayed
			return nil
		}
		if untrustedCompletedUsageAssertion {
			return ErrSettlementCompletedUsageUntrusted
		}
		if attemptErr != nil {
			return ErrAttemptFenced
		}

		if !rowOwnsFence(turnRow, normalized.Fence) || turn.Status.Terminal() {
			return ErrAttemptFenced
		}
		attempt, err := attemptRow.toAttempt()
		if err != nil {
			return ErrStoreIntegrity
		}
		if !attemptMatchesFence(attempt, normalized.Fence) || attempt.Status != AttemptStatusRunning {
			return ErrAttemptFenced
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		if !attempt.LeaseExpiresAt.After(now) {
			return ErrAttemptLeaseExpired
		}
		if now.Before(attempt.LastHeartbeatAt) {
			return ErrStoreIntegrity
		}
		if turn.CancelRequestedAt != nil && normalized.TerminalStatus != agentv1.TurnStatusStopped {
			return ErrAttemptCancelled
		}
		var reviewEvidence *SettlementUsageEvidence
		reviewSource := SettlementReviewSource("")
		if providerUsageMode {
			// Every terminal Turn under the exact Provider Journal binding waits
			// for kernel-verified usage. Counts are frozen while the Turn lock is
			// held, so an AppendAttested call can only land wholly before this
			// snapshot or after the pending Review becomes durable.
			evidence, err := inspectAmbiguousRelease(tx, turn.ID, len(normalized.Effects))
			if err != nil {
				return err
			}
			providerCount, err := countProviderUsageJournalTx(tx, turn.ID)
			if err != nil {
				return err
			}
			evidence.PriorProviderUsageCount = providerCount
			reviewEvidence = &evidence
			if normalized.TerminalStatus == agentv1.TurnStatusCompleted {
				reviewSource = SettlementReviewSourceExecutorCompletion
			} else {
				reviewSource = SettlementReviewSourceExecutorTerminal
			}
		} else if completedUsageMode {
			// A completed Turn under the exact Usage binding always waits for the
			// trusted meter. An empty evidence snapshot is meaningful here: the
			// worker's lack of durable output is not proof of zero billable usage.
			evidence, err := inspectAmbiguousRelease(tx, turn.ID, len(normalized.Effects))
			if err != nil {
				return err
			}
			reviewEvidence = &evidence
			reviewSource = SettlementReviewSourceExecutorCompletion
		} else if store.hasSettlementAuthority() && normalized.TerminalStatus != "" && normalized.Settlement != nil &&
			normalized.Settlement.Intent == SettlementIntentRelease {
			// This is still before the terminal Event, receipt, Turn/Attempt
			// mutation and Settlement call. Because the Turn row is locked, an
			// Emit cannot appear between this proof and the terminal commit.
			evidence, err := inspectAmbiguousRelease(tx, turn.ID, len(normalized.Effects))
			if err != nil {
				return err
			}
			if evidence.ambiguous() {
				if _, reviewCapable := store.reviewAuthority(); !reviewCapable {
					return ErrSettlementUsageUnknown
				}
				reviewEvidence = &evidence
				reviewSource = SettlementReviewSourceExecutor
			}
		}

		nextTurn := cloneTurn(turn)
		nextAttempt := cloneTurnAttempt(attempt)
		nextAttempt.UpdatedAt = now
		var pendingReview *SettlementReviewRecord
		var draft EventDraft
		if normalized.TerminalStatus == "" {
			draft = *normalized.Event
		} else {
			if !CanTransition(turn.Status, normalized.TerminalStatus) {
				return transitionError(turn.Status, normalized.TerminalStatus)
			}
			if normalized.TerminalStatus == agentv1.TurnStatusStopped && turn.CancelRequestedAt == nil {
				return ErrCancellationNotRequested
			}
			nextTurn.Status = normalized.TerminalStatus
			nextTurn.UpdatedAt = now
			nextTurn.FinishedAt = timePointer(now)
			nextAttempt.Status = attemptStatusForTurn(normalized.TerminalStatus)
			nextAttempt.FinishedAt = timePointer(now)
			if reviewEvidence != nil {
				var review SettlementReviewRecord
				var err error
				if providerUsageMode {
					review, err = buildProviderSettlementReviewRecord(
						nextTurn, nextTurn.Status, reviewSource,
						nextAttempt.ID, nextAttempt.FencingToken, normalized.OperationID,
						settlementKey(nextTurn.ID, normalized.OperationID), *reviewEvidence, now,
					)
				} else {
					review, err = buildSettlementReviewRecord(
						nextTurn, nextTurn.Status, reviewSource,
						nextAttempt.ID, nextAttempt.FencingToken, normalized.OperationID,
						settlementKey(nextTurn.ID, normalized.OperationID), *reviewEvidence, now,
					)
				}
				if err != nil {
					return err
				}
				pendingReview = &review
			}
			draft, err = statusEventWithSettlementReview(
				normalized.TerminalStatus,
				normalized.TerminalStatus == agentv1.TurnStatusStopped,
				pendingReview,
			)
			if err != nil {
				return err
			}
		}
		if err := nextTurn.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		if err := nextAttempt.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		sequence, err := nextSQLSequence(turnRow.LastEventSequence)
		if err != nil {
			return err
		}
		event, err := buildEvent(nextTurn, sequence, draft)
		if err != nil {
			return err
		}
		if err := store.insertEvent(tx, event, now); err != nil {
			return err
		}

		operation := sqlTurnOperationRow{
			TurnID:          string(nextTurn.ID),
			OperationID:     normalized.OperationID,
			OperationDigest: digest,
			AttemptID:       nextAttempt.ID,
			FencingToken:    int64(nextAttempt.FencingToken),
			EventSequence:   int64(event.Sequence),
			TurnStatus:      string(nextTurn.Status),
			EffectCount:     len(normalized.Effects),
			CreatedAt:       now,
		}
		if err := validateOperationRow(operation); err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&operation).Error; err != nil {
			return err
		}

		effects := make([]EffectOutboxRecord, 0, len(normalized.Effects))
		for ordinal, effect := range normalized.Effects {
			outboxRow, record, err := buildEffectOutboxRow(nextAttempt, normalized.OperationID, ordinal, effect, now)
			if err != nil {
				return ErrStoreIntegrity
			}
			if err := tx.Create(&outboxRow).Error; err != nil {
				return err
			}
			effects = append(effects, record)
		}
		reviewOpenedAt := now
		if pendingReview != nil {
			if err := holdTurnEffectsForReview(tx, nextTurn.ID, now); err != nil {
				return err
			}
			reviewOpenedAt, err = store.executionNow(ctx, tx)
			if err != nil {
				return err
			}
			for index := range effects {
				effects[index].Status = string(EffectStatusReviewHold)
			}
		}

		turnColumns := map[string]any{"last_event_sequence": int64(event.Sequence)}
		attemptColumns := map[string]any{"updated_at": now}
		if normalized.TerminalStatus != "" {
			turnColumns["status"] = string(nextTurn.Status)
			turnColumns["finished_at"] = nextTurn.FinishedAt
			turnColumns["updated_at"] = now
			turnColumns["active_attempt_id"] = nil
			attemptColumns["status"] = string(nextAttempt.Status)
			attemptColumns["finished_at"] = nextAttempt.FinishedAt
		}
		if err := store.updateAttemptColumns(tx, attemptRow.ID, attemptColumns); err != nil {
			return err
		}
		if err := store.updateTurnColumns(tx, nextTurn.ID, turnColumns); err != nil {
			return err
		}

		// Settlement or its durable Review hold rides the same transaction as
		// terminal state and the immutable Operation receipt. That is the
		// exactly-once boundary: no finished Turn can exist without either a
		// commercial outcome or an explicit pending adjudication. Any authority
		// failure rolls the whole terminal commit back.
		var settlementReview *SettlementReviewRecord
		if normalized.TerminalStatus != "" {
			request := SettlementRequest{}
			if normalized.Settlement != nil {
				request = *normalized.Settlement
			}
			intent := request.resolve(nextTurn.Status)
			units := request.UsedUnits
			if intent == SettlementIntentRelease {
				units = 0
			}
			key := settlementKey(nextTurn.ID, normalized.OperationID)
			if pendingReview != nil {
				pendingReview.CreatedAt = reviewOpenedAt
				pendingReview.UpdatedAt = reviewOpenedAt
				var persistErr error
				if providerUsageMode {
					persistErr = persistSettlementReviewWithAuthority(
						tx, nextTurn, *pendingReview, providerReviewAuthority,
					)
				} else {
					persistErr = store.persistSettlementReview(tx, nextTurn, *pendingReview)
				}
				if persistErr != nil {
					return persistErr
				}
				review := *pendingReview
				settlementReview = &review
			} else if err := store.settle(tx, SettlementCommand{
				TurnID: nextTurn.ID, PrincipalID: nextTurn.PrincipalID,
				SettlementKey: key, AuthorizationKind: SettlementAuthorizationOperation,
				AttemptID: normalized.Fence.AttemptID, FencingToken: normalized.Fence.FencingToken,
				OperationID: normalized.OperationID,
				Intent:      intent, TerminalStatus: nextTurn.Status, UsedUnits: units,
			}); err != nil {
				return err
			}
		}

		result = CommitAttemptResult{
			OperationID: normalized.OperationID, OperationDigest: digest,
			Event: event, Turn: nextTurn, Attempt: nextAttempt,
			TurnStatus: nextTurn.Status, Effects: effects, SettlementReview: settlementReview,
		}
		return nil
	})
	unlockProviderBinding()
	if txErr == nil {
		return cloneCommitAttemptResult(result), nil
	}
	if err := contextError(ctx); err == nil {
		if replay, found, resolveErr := store.resolveOperation(
			ctx, candidates, untrustedCompletedUsageAssertion,
		); resolveErr != nil {
			return CommitAttemptResult{}, store.normalize("commit-attempt", resolveErr)
		} else if found {
			return replay, nil
		}
		classified := store.normalize("commit-attempt", txErr)
		// Effect identity probing exists only to classify an otherwise opaque
		// unique-index failure. It must never replace an authoritative fence,
		// lease, cancellation, transition, settlement or integrity error merely
		// because an unrelated Effect key already exists.
		if !errors.Is(classified, ErrStoreUnavailable) {
			return CommitAttemptResult{}, classified
		}
		if conflict, detectErr := store.detectEffectConflict(ctx, normalized); detectErr != nil {
			return CommitAttemptResult{}, store.normalize("commit-attempt", detectErr)
		} else if conflict {
			return CommitAttemptResult{}, ErrEffectConflict
		}
		return CommitAttemptResult{}, classified
	} else {
		return CommitAttemptResult{}, err
	}
}

func databaseExecutionClock(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	if tx == nil || tx.Config == nil || tx.Dialector == nil {
		return time.Time{}, ErrStoreUnavailable
	}
	// Format the database timestamp as text so clock reads do not depend on a
	// driver's parseTime/session-time-zone settings. Both expressions are UTC.
	var query string
	switch tx.Dialector.Name() {
	case "mysql":
		query = "SELECT DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%d %H:%i:%s.%f') AS now"
	case "sqlite":
		query = "SELECT strftime('%Y-%m-%d %H:%M:%f', 'now') AS now"
	default:
		return time.Time{}, ErrStoreUnavailable
	}
	var result struct {
		Now string `gorm:"column:now"`
	}
	if err := tx.WithContext(ctx).Raw(query).Scan(&result).Error; err != nil {
		return time.Time{}, err
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", result.Now, time.UTC)
	if err != nil {
		return time.Time{}, err
	}
	return canonicalExecutionTime(now)
}

func (store *SQLStore) executionNow(ctx context.Context, tx *gorm.DB) (time.Time, error) {
	if store.executionClock == nil {
		return time.Time{}, ErrStoreIntegrity
	}
	now, err := store.executionClock(ctx, tx)
	if err != nil {
		return time.Time{}, err
	}
	return canonicalExecutionTime(now)
}

func canonicalExecutionTime(value time.Time) (time.Time, error) {
	if value.IsZero() {
		return time.Time{}, fmt.Errorf("execution clock returned zero time")
	}
	value = value.UTC().Truncate(time.Microsecond)
	if value.Year() < 1000 || value.Year() > 9999 {
		return time.Time{}, fmt.Errorf("execution time is outside SQL datetime range")
	}
	return value, nil
}

func (store *SQLStore) executionLeaseExpiry(now time.Time) (time.Time, error) {
	if store.attemptLeaseTTL <= 0 || store.attemptLeaseTTL > MaxAttemptLeaseTTL {
		return time.Time{}, ErrStoreIntegrity
	}
	expiresAt, err := canonicalExecutionTime(now.Add(store.attemptLeaseTTL))
	if err != nil || !expiresAt.After(now) {
		return time.Time{}, ErrStoreIntegrity
	}
	return expiresAt, nil
}

func nextExecutionFence(last int64) (agentv1.Sequence, error) {
	if last < 0 || agentv1.Sequence(last) > MaxDurableSequence {
		return 0, ErrStoreIntegrity
	}
	if agentv1.Sequence(last) == MaxDurableSequence {
		return 0, ErrAttemptFenceExhausted
	}
	return agentv1.Sequence(last + 1), nil
}

func attemptToSQLRow(attempt TurnAttempt) (sqlTurnAttemptRow, error) {
	if err := attempt.Validate(); err != nil {
		return sqlTurnAttemptRow{}, err
	}
	return sqlTurnAttemptRow{
		AttemptID:         attempt.ID,
		TurnID:            string(attempt.TurnID),
		FencingToken:      int64(attempt.FencingToken),
		Status:            string(attempt.Status),
		WorkerID:          attempt.WorkerID,
		WorkerBuildDigest: attempt.WorkerBuildDigest,
		ClaimedAt:         attempt.ClaimedAt.UTC(),
		LastHeartbeatAt:   attempt.LastHeartbeatAt.UTC(),
		LeaseExpiresAt:    attempt.LeaseExpiresAt.UTC(),
		FinishedAt:        utcTimePointer(attempt.FinishedAt),
		CreatedAt:         attempt.CreatedAt.UTC(),
		UpdatedAt:         attempt.UpdatedAt.UTC(),
	}, nil
}

func (row sqlTurnAttemptRow) toAttempt() (TurnAttempt, error) {
	if row.FencingToken < 1 || agentv1.Sequence(row.FencingToken) > MaxDurableSequence {
		return TurnAttempt{}, ErrStoreIntegrity
	}
	attempt := TurnAttempt{
		ID:                row.AttemptID,
		TurnID:            agentv1.TurnID(row.TurnID),
		FencingToken:      agentv1.Sequence(row.FencingToken),
		Status:            AttemptStatus(row.Status),
		WorkerID:          row.WorkerID,
		WorkerBuildDigest: row.WorkerBuildDigest,
		LeaseExpiresAt:    row.LeaseExpiresAt.UTC(),
		ClaimedAt:         row.ClaimedAt.UTC(),
		LastHeartbeatAt:   row.LastHeartbeatAt.UTC(),
		FinishedAt:        utcTimePointer(row.FinishedAt),
		CreatedAt:         row.CreatedAt.UTC(),
		UpdatedAt:         row.UpdatedAt.UTC(),
	}
	if err := attempt.Validate(); err != nil {
		return TurnAttempt{}, ErrStoreIntegrity
	}
	return attempt, nil
}

func (store *SQLStore) lookupAttemptByIDTx(tx *gorm.DB, attemptID string, lock bool) (sqlTurnAttemptRow, bool, error) {
	var row sqlTurnAttemptRow
	query := tx.Where("attempt_id = ?", attemptID)
	if lock && store.dialect == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sqlTurnAttemptRow{}, false, nil
	}
	return row, err == nil, err
}

func (store *SQLStore) lockAttempt(tx *gorm.DB, turnID, attemptID string, fence int64) (sqlTurnAttemptRow, error) {
	var row sqlTurnAttemptRow
	query := tx.Where("turn_id = ? AND attempt_id = ? AND fencing_token = ?", turnID, attemptID, fence)
	if store.dialect == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sqlTurnAttemptRow{}, ErrAttemptNotFound
		}
		return sqlTurnAttemptRow{}, err
	}
	return row, nil
}

func (store *SQLStore) updateAttemptColumns(tx *gorm.DB, id uint64, columns map[string]any) error {
	// Every caller holds the Turn lock and has already locked/read this exact
	// Attempt row. MySQL reports RowsAffected=0 for an update whose values are
	// unchanged (for example, two heartbeats in one database microsecond), so
	// row count cannot distinguish a safe no-op from absence here.
	result := tx.Table(SQLTurnAttemptTable).Where("id = ?", id).UpdateColumns(columns)
	return result.Error
}

func attemptMatchesClaim(attempt TurnAttempt, command ClaimAttemptCommand) bool {
	return attempt.ID == command.AttemptID && attempt.TurnID == command.TurnID &&
		attempt.WorkerID == command.WorkerID && attempt.WorkerBuildDigest == command.WorkerBuildDigest
}

func attemptMatchesFence(attempt TurnAttempt, fence AttemptFence) bool {
	return attempt.TurnID == fence.TurnID && attempt.ID == fence.AttemptID &&
		attempt.FencingToken == fence.FencingToken && attempt.WorkerID == fence.WorkerID &&
		attempt.WorkerBuildDigest == fence.WorkerBuildDigest
}

func turnOwnsAttempt(row sqlTurnRow, attempt TurnAttempt) bool {
	return row.ActiveAttemptID != nil && row.TurnID == string(attempt.TurnID) &&
		*row.ActiveAttemptID == attempt.ID && row.FencingToken == int64(attempt.FencingToken)
}

func rowOwnsFence(row sqlTurnRow, fence AttemptFence) bool {
	return row.ActiveAttemptID != nil && row.TurnID == string(fence.TurnID) &&
		*row.ActiveAttemptID == fence.AttemptID && row.FencingToken == int64(fence.FencingToken)
}

func (store *SQLStore) resolveClaim(ctx context.Context, command ClaimAttemptCommand) (ClaimAttemptResult, bool, error) {
	var attemptRow sqlTurnAttemptRow
	err := store.db.WithContext(ctx).Where("attempt_id = ?", command.AttemptID).Take(&attemptRow).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ClaimAttemptResult{}, false, nil
	}
	if err != nil {
		return ClaimAttemptResult{}, false, err
	}
	attempt, err := attemptRow.toAttempt()
	if err != nil {
		return ClaimAttemptResult{}, false, ErrStoreIntegrity
	}
	if !attemptMatchesClaim(attempt, command) {
		return ClaimAttemptResult{}, false, ErrAttemptConflict
	}
	var turnRow sqlTurnRow
	if err := store.db.WithContext(ctx).Where("turn_id = ?", string(command.TurnID)).Take(&turnRow).Error; err != nil {
		return ClaimAttemptResult{}, false, err
	}
	turn, err := turnRow.toTurn()
	if err != nil {
		return ClaimAttemptResult{}, false, ErrStoreIntegrity
	}
	if !claimPluginScopeAllows(command.PluginScope, turn.Plugin) {
		return ClaimAttemptResult{}, false, ErrPluginScopeMismatch
	}
	if !turnOwnsAttempt(turnRow, attempt) || attempt.Status != AttemptStatusRunning {
		return ClaimAttemptResult{}, false, ErrAttemptFenced
	}
	now, err := store.executionNow(ctx, store.db.WithContext(ctx))
	if err != nil {
		return ClaimAttemptResult{}, false, err
	}
	if !attempt.LeaseExpiresAt.After(now) {
		return ClaimAttemptResult{}, false, ErrAttemptLeaseExpired
	}
	return ClaimAttemptResult{Turn: turn, Attempt: attempt, Replay: true}, true, nil
}

type commitDigestEvent struct {
	Type         agentv1.EventType `json:"type"`
	ResourceRefs []string          `json:"resourceRefs,omitempty"`
	Data         json.RawMessage   `json:"data"`
}

type commitDigestEffect struct {
	OutboxID    string          `json:"outboxId"`
	Topic       string          `json:"topic"`
	DedupeKey   string          `json:"dedupeKey"`
	Payload     json.RawMessage `json:"payload"`
	AvailableAt time.Time       `json:"availableAt"`
}

type commitDigestSettlement struct {
	Intent    SettlementIntent `json:"intent"`
	UsedUnits int64            `json:"usedUnits"`
}

const (
	completedUsageTerminalizationMode = "trusted_usage_review_v1"
	providerUsageTerminalizationMode  = "provider_usage_review_v1"
	operationDigestVersionV2          = "workmax.agentturn.operation/v2"
	operationDigestVersionV3          = "workmax.agentturn.operation/v3"
	operationDigestVersionV4          = "workmax.agentturn.operation/v4"
)

// commitDigestTerminalization is present in v3 completed-Usage and v4
// Provider-Journal Operation receipts. It makes a retry bind not merely to the
// caller's normalized settlement request, but to the exact kernel policy that
// replaced caller-reported usage with a durable Review.
type commitDigestTerminalization struct {
	Mode         string                 `json:"mode"`
	Source       SettlementReviewSource `json:"source"`
	Reason       string                 `json:"reason"`
	PolicyDigest string                 `json:"policyDigest"`
}

func newCompletedUsageTerminalization() commitDigestTerminalization {
	return commitDigestTerminalization{
		Mode: completedUsageTerminalizationMode, Source: SettlementReviewSourceExecutorCompletion,
		Reason:       SettlementReviewReasonCompletedUsageUnmeasured,
		PolicyDigest: completedUsageTerminalizationPolicyDigest(),
	}
}

func completedUsageTerminalizationPolicyDigest() string {
	hash := sha256.New()
	for _, part := range []string{
		"workmax.agentturn.completed-usage-policy/v1",
		"completed terminal usage must come from the sealed trusted meter",
		"nil/default/finalize-zero opens a durable review",
		"zero durable evidence is permitted",
		"all effects remain review_hold",
		"settle is forbidden until evidence-backed resolution",
	} {
		_, _ = fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func newProviderUsageTerminalization(
	terminal agentv1.TurnStatus,
) (commitDigestTerminalization, error) {
	if !terminal.Valid() || !terminal.Terminal() {
		return commitDigestTerminalization{}, ErrStoreIntegrity
	}
	terminalization := commitDigestTerminalization{
		Mode: providerUsageTerminalizationMode, Source: SettlementReviewSourceExecutorTerminal,
		Reason:       SettlementReviewReasonTerminalUsageUnmeasured,
		PolicyDigest: providerUsageTerminalizationPolicyDigest(),
	}
	if terminal == agentv1.TurnStatusCompleted {
		terminalization.Source = SettlementReviewSourceExecutorCompletion
		terminalization.Reason = SettlementReviewReasonCompletedUsageUnmeasured
	}
	return terminalization, nil
}

func providerUsageTerminalizationPolicyDigest() string {
	hash := sha256.New()
	for _, part := range []string{
		"workmax.agentturn.provider-usage-policy/v1",
		"every terminal status opens a durable provider usage review",
		"completed uses executor_completion and other terminals use executor_terminal",
		"nil/default/finalize-zero asks the kernel meter to decide usage",
		"release and positive caller units are untrusted",
		"journal receipts are counted under the turn lock and may arrive while review is pending",
		"all effects remain review_hold and direct settlement is forbidden",
	} {
		_, _ = fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// providerUsageTerminalization returns v4 policy only for the exact sealed
// Provider Journal binding. It deliberately does not treat an older Usage or
// Review authority as Provider-aware merely because its dynamic type happens
// to expose additional methods.
func (store *SQLStore) providerUsageTerminalization(
	terminal agentv1.TurnStatus,
) (commitDigestTerminalization, bool, error) {
	if store == nil {
		return commitDigestTerminalization{}, false, ErrSettlementBindingInvalid
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	binding := store.settlementBinding
	if binding == nil || !binding.providerUsageAware {
		return commitDigestTerminalization{}, false, nil
	}
	if _, _, err := store.settlementReviewProviderUsageAuthorityLocked(); err != nil {
		return commitDigestTerminalization{}, false, err
	}
	terminalization, err := newProviderUsageTerminalization(terminal)
	if err != nil {
		return commitDigestTerminalization{}, false, err
	}
	return terminalization, true, nil
}

func (terminalization commitDigestTerminalization) validate() error {
	if err := validatePrintableASCII("terminalizationMode", terminalization.Mode, MaxCommandDigestBytes); err != nil {
		return err
	}
	if !terminalization.Source.Valid() {
		return ErrStoreIntegrity
	}
	if err := validatePrintableASCII("terminalizationReason", terminalization.Reason, MaxSettlementReviewUsageFieldBytes); err != nil {
		return err
	}
	return validateSettlementReviewSHA256Digest("terminalizationPolicyDigest", terminalization.PolicyDigest)
}

func normalizeCommitCommand(command CommitAttemptCommand) (CommitAttemptCommand, string, error) {
	return normalizeCommitCommandWithTerminalization(command, nil)
}

func normalizeCompletedUsageCommitCommand(
	command CommitAttemptCommand,
	terminalization commitDigestTerminalization,
) (CommitAttemptCommand, string, error) {
	if command.TerminalStatus != agentv1.TurnStatusCompleted {
		return CommitAttemptCommand{}, "", ErrStoreIntegrity
	}
	if err := terminalization.validate(); err != nil {
		return CommitAttemptCommand{}, "", err
	}
	if terminalization != newCompletedUsageTerminalization() {
		return CommitAttemptCommand{}, "", ErrStoreIntegrity
	}
	return normalizeCommitCommandWithTerminalization(command, &terminalization)
}

func normalizeProviderUsageCommitCommand(
	command CommitAttemptCommand,
	terminalization commitDigestTerminalization,
) (CommitAttemptCommand, string, error) {
	if !command.TerminalStatus.Valid() || !command.TerminalStatus.Terminal() {
		return CommitAttemptCommand{}, "", ErrStoreIntegrity
	}
	expected, err := newProviderUsageTerminalization(command.TerminalStatus)
	if err != nil || terminalization != expected {
		return CommitAttemptCommand{}, "", ErrStoreIntegrity
	}
	if err := terminalization.validate(); err != nil {
		return CommitAttemptCommand{}, "", err
	}
	// In v4 all caller forms that merely request measurement have one identity.
	// In particular, a failed/stopped/timeout default must not be encoded as a
	// release: nil, default and explicit Finalize(0) all normalize to the same
	// kernel-meter request. The untrusted gate still examines the original
	// command, so explicit Release or positive units cannot create/replay v4.
	providerCommand := command
	providerCommand.Settlement = &SettlementRequest{Intent: SettlementIntentFinalize}
	return normalizeCommitCommandWithVersion(providerCommand, &terminalization, operationDigestVersionV4)
}

func normalizeCommitCommandWithTerminalization(
	command CommitAttemptCommand,
	terminalization *commitDigestTerminalization,
) (CommitAttemptCommand, string, error) {
	version := operationDigestVersionV2
	if terminalization != nil {
		version = operationDigestVersionV3
	}
	return normalizeCommitCommandWithVersion(command, terminalization, version)
}

func normalizeCommitCommandWithVersion(
	command CommitAttemptCommand,
	terminalization *commitDigestTerminalization,
	version string,
) (CommitAttemptCommand, string, error) {
	if version != operationDigestVersionV2 && version != operationDigestVersionV3 &&
		version != operationDigestVersionV4 {
		return CommitAttemptCommand{}, "", ErrStoreIntegrity
	}
	switch version {
	case operationDigestVersionV2:
		if terminalization != nil {
			return CommitAttemptCommand{}, "", ErrStoreIntegrity
		}
	case operationDigestVersionV3:
		if terminalization == nil || command.TerminalStatus != agentv1.TurnStatusCompleted ||
			*terminalization != newCompletedUsageTerminalization() {
			return CommitAttemptCommand{}, "", ErrStoreIntegrity
		}
	case operationDigestVersionV4:
		if terminalization == nil {
			return CommitAttemptCommand{}, "", ErrStoreIntegrity
		}
		expected, err := newProviderUsageTerminalization(command.TerminalStatus)
		if err != nil || *terminalization != expected {
			return CommitAttemptCommand{}, "", ErrStoreIntegrity
		}
	}
	normalized := command
	if command.Event != nil {
		event := *command.Event
		event.ResourceRefs = append([]string(nil), command.Event.ResourceRefs...)
		event.Data = append(json.RawMessage(nil), command.Event.Data...)
		normalized.Event = &event
	}
	normalized.Effects = make([]EffectOutboxDraft, len(command.Effects))
	digestEffects := make([]commitDigestEffect, len(command.Effects))
	for index, effect := range command.Effects {
		availableAt, err := canonicalExecutionTime(effect.AvailableAt)
		if err != nil {
			return CommitAttemptCommand{}, "", fmt.Errorf("effect %d availableAt: %w", index, err)
		}
		normalized.Effects[index] = effect
		normalized.Effects[index].Payload = append(json.RawMessage(nil), effect.Payload...)
		normalized.Effects[index].AvailableAt = availableAt
		digestEffects[index] = commitDigestEffect{
			OutboxID: effect.OutboxID, Topic: effect.Topic, DedupeKey: effect.DedupeKey,
			Payload: normalized.Effects[index].Payload, AvailableAt: availableAt,
		}
	}
	var digestEvent *commitDigestEvent
	if normalized.Event != nil {
		digestEvent = &commitDigestEvent{
			Type: normalized.Event.Type, ResourceRefs: normalized.Event.ResourceRefs, Data: normalized.Event.Data,
		}
	}
	var digestSettlement *commitDigestSettlement
	if normalized.TerminalStatus != "" {
		request := SettlementRequest{}
		if command.Settlement != nil {
			request = *command.Settlement
		}
		request.Intent = request.resolve(normalized.TerminalStatus)
		if request.Intent == SettlementIntentRelease {
			request.UsedUnits = 0
		}
		normalized.Settlement = &request
		digestSettlement = &commitDigestSettlement{Intent: request.Intent, UsedUnits: request.UsedUnits}
	}
	payload := struct {
		Version         string                       `json:"version"`
		TerminalStatus  agentv1.TurnStatus           `json:"terminalStatus,omitempty"`
		Event           *commitDigestEvent           `json:"event,omitempty"`
		Effects         []commitDigestEffect         `json:"effects"`
		Settlement      *commitDigestSettlement      `json:"settlement,omitempty"`
		Terminalization *commitDigestTerminalization `json:"terminalization,omitempty"`
	}{
		Version: version, TerminalStatus: normalized.TerminalStatus,
		Event: digestEvent, Effects: digestEffects, Settlement: digestSettlement,
		Terminalization: terminalization,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return CommitAttemptCommand{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return normalized, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (store *SQLStore) lookupOperationTx(tx *gorm.DB, turnID agentv1.TurnID, operationID string, lock bool) (sqlTurnOperationRow, bool, error) {
	var row sqlTurnOperationRow
	query := tx.Where("turn_id = ? AND operation_id = ?", string(turnID), operationID)
	if lock && store.dialect == "mysql" {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sqlTurnOperationRow{}, false, nil
	}
	return row, err == nil, err
}

func validateOperationRow(row sqlTurnOperationRow) error {
	if err := validatePathSegment("turnId", row.TurnID, MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("operationId", row.OperationID, MaxOperationIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("operationDigest", row.OperationDigest, MaxCommandDigestBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("attemptId", row.AttemptID, MaxAttemptIDBytes); err != nil {
		return err
	}
	if row.FencingToken < 1 || agentv1.Sequence(row.FencingToken) > MaxDurableSequence ||
		row.EventSequence < 1 || agentv1.Sequence(row.EventSequence) > MaxDurableSequence {
		return ErrStoreIntegrity
	}
	if !agentv1.TurnStatus(row.TurnStatus).Valid() || row.EffectCount < 0 || row.EffectCount > MaxEffectsPerOperation {
		return ErrStoreIntegrity
	}
	if row.CreatedAt.IsZero() {
		return ErrStoreIntegrity
	}
	return nil
}

type operationTerminalizationMode uint8

const (
	operationTerminalizationLegacy operationTerminalizationMode = iota
	operationTerminalizationCompletedUsageReview
	operationTerminalizationProviderUsageReview
)

type commitOperationCandidate struct {
	Command CommitAttemptCommand
	Digest  string
	Mode    operationTerminalizationMode
}

// completedUsageCallerAssertionUntrusted identifies commercial claims that a
// worker must not make when the exact trusted-meter mode is active. Empty,
// default and explicit Finalize(0) all mean "measure this Review". Release and
// any positive unit count instead assert a financial fact without evidence.
func completedUsageCallerAssertionUntrusted(command CommitAttemptCommand) bool {
	if command.TerminalStatus != agentv1.TurnStatusCompleted || command.Settlement == nil {
		return false
	}
	return command.Settlement.Intent == SettlementIntentRelease || command.Settlement.UsedUnits > 0
}

// providerUsageCallerAssertionUntrusted applies to every terminal status.
// The Plugin may ask for measurement with nil/default/Finalize(0), but it may
// neither assert a free release nor supply a positive commercial quantity.
func providerUsageCallerAssertionUntrusted(command CommitAttemptCommand) bool {
	if command.TerminalStatus == "" || command.Settlement == nil {
		return false
	}
	return command.Settlement.Intent == SettlementIntentRelease || command.Settlement.UsedUnits > 0
}

// selectOperationReplayCandidate walks the caller's immutable compatibility
// chain in order: v4, completed-only v3, then v2 under a Provider binding;
// v3 then v2 under the older Usage binding; and only v2 otherwise. For an
// untrusted caller assertion, v2 remains the sole historical read path.
func selectOperationReplayCandidate(
	row sqlTurnOperationRow,
	candidates []commitOperationCandidate,
	requireLegacy bool,
) (commitOperationCandidate, bool) {
	for _, candidate := range candidates {
		if requireLegacy && candidate.Mode != operationTerminalizationLegacy {
			continue
		}
		if operationMatchesCommand(row, candidate.Command, candidate.Digest) {
			return candidate, true
		}
	}
	return commitOperationCandidate{}, false
}

func operationMatchesCommand(row sqlTurnOperationRow, command CommitAttemptCommand, digest string) bool {
	return row.TurnID == string(command.Fence.TurnID) && row.OperationID == command.OperationID &&
		row.OperationDigest == digest && row.AttemptID == command.Fence.AttemptID &&
		row.FencingToken == int64(command.Fence.FencingToken)
}

func countProviderUsageJournalTx(tx *gorm.DB, turnID agentv1.TurnID) (int64, error) {
	if tx == nil {
		return 0, ErrStoreIntegrity
	}
	var count int64
	if err := tx.Table(SQLProviderUsageJournalTable).
		Where("turn_id = ?", string(turnID)).Count(&count).Error; err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, ErrStoreIntegrity
	}
	return count, nil
}

func (store *SQLStore) hydrateOperationResult(
	tx *gorm.DB,
	turnRow sqlTurnRow,
	turn Turn,
	operation sqlTurnOperationRow,
	command CommitAttemptCommand,
	terminalizationMode operationTerminalizationMode,
) (CommitAttemptResult, error) {
	if err := validateOperationRow(operation); err != nil || operation.TurnID != turnRow.TurnID || operation.EventSequence > turnRow.LastEventSequence {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	attemptRow, err := store.lockAttempt(tx, operation.TurnID, operation.AttemptID, operation.FencingToken)
	if err != nil {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	attempt, err := attemptRow.toAttempt()
	if err != nil {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	var eventRow sqlTurnEventRow
	if err := tx.Where("turn_id = ? AND sequence = ?", operation.TurnID, operation.EventSequence).Take(&eventRow).Error; err != nil {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	event, err := eventRow.toEnvelope(turn)
	if err != nil || !operationEventMatchesStatus(event, agentv1.TurnStatus(operation.TurnStatus)) {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	var effectRows []sqlEffectOutboxRow
	if err := tx.Where("turn_id = ? AND operation_id = ?", operation.TurnID, operation.OperationID).
		Order("ordinal ASC").Find(&effectRows).Error; err != nil {
		return CommitAttemptResult{}, err
	}
	if len(effectRows) != operation.EffectCount {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	effects := make([]EffectOutboxRecord, 0, len(effectRows))
	for ordinal, effectRow := range effectRows {
		record, err := effectRow.toRecord()
		if err != nil || effectRow.Ordinal != ordinal || effectRow.TurnID != operation.TurnID ||
			effectRow.AttemptID != operation.AttemptID || effectRow.TurnFencingToken != operation.FencingToken ||
			effectRow.OperationID != operation.OperationID {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
		effects = append(effects, record)
	}
	review, reviewFound, err := store.lookupSettlementReviewTx(tx, turn.ID, false)
	if err != nil {
		return CommitAttemptResult{}, err
	}
	var operationReview *SettlementReviewRecord
	switch terminalizationMode {
	case operationTerminalizationLegacy:
		if reviewFound && review.Source == SettlementReviewSourceExecutor &&
			review.OperationID == operation.OperationID {
			operationReview = &review
		}
	case operationTerminalizationCompletedUsageReview:
		if !reviewFound || review.Source != SettlementReviewSourceExecutorCompletion ||
			review.OperationID != operation.OperationID {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
		operationReview = &review
	case operationTerminalizationProviderUsageReview:
		if !reviewFound || review.OperationID != operation.OperationID ||
			!settlementReviewProviderUsageAware(review) {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
		operationReview = &review
	default:
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	if !operationContentMatchesCommand(event, effects, command, operationReview) {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	result := CommitAttemptResult{
		OperationID: operation.OperationID, OperationDigest: operation.OperationDigest,
		Event: event, Turn: turn, Attempt: attempt,
		TurnStatus: agentv1.TurnStatus(operation.TurnStatus), Effects: effects,
	}
	if terminalizationMode == operationTerminalizationCompletedUsageReview {
		evidence, err := inspectReplayReleaseEvidence(tx, turn.ID, operation.OperationID, operation.EffectCount)
		if err != nil {
			return CommitAttemptResult{}, err
		}
		if review.AttemptID != operation.AttemptID ||
			review.FencingToken != agentv1.Sequence(operation.FencingToken) ||
			review.TerminalStatus != agentv1.TurnStatusCompleted ||
			agentv1.TurnStatus(operation.TurnStatus) != agentv1.TurnStatusCompleted ||
			review.SettlementKey != settlementKey(turn.ID, operation.OperationID) ||
			review.Reason != SettlementReviewReasonCompletedUsageUnmeasured ||
			review.RequestDigest != settlementReviewRequestDigestV1(review) ||
			review.Evidence != evidence {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return CommitAttemptResult{}, err
		}
		result.SettlementReview = &review
		return result, nil
	}
	if terminalizationMode == operationTerminalizationProviderUsageReview {
		evidence, err := inspectReplayReleaseEvidence(tx, turn.ID, operation.OperationID, operation.EffectCount)
		if err != nil {
			return CommitAttemptResult{}, err
		}
		wantSource := SettlementReviewSourceExecutorTerminal
		wantReason := SettlementReviewReasonTerminalUsageUnmeasured
		if command.TerminalStatus == agentv1.TurnStatusCompleted {
			wantSource = SettlementReviewSourceExecutorCompletion
			wantReason = SettlementReviewReasonCompletedUsageUnmeasured
		}
		providerCount, err := countProviderUsageJournalTx(tx, turn.ID)
		if err != nil {
			return CommitAttemptResult{}, err
		}
		if review.Source != wantSource || review.Reason != wantReason ||
			review.TerminalStatus != command.TerminalStatus ||
			agentv1.TurnStatus(operation.TurnStatus) != command.TerminalStatus ||
			review.OperationID != operation.OperationID || review.AttemptID != operation.AttemptID ||
			review.FencingToken != agentv1.Sequence(operation.FencingToken) ||
			review.SettlementKey != settlementKey(turn.ID, operation.OperationID) ||
			review.RequestDigest != settlementReviewRequestDigestV2(review) ||
			review.Evidence.PriorOperationCount != evidence.PriorOperationCount ||
			review.Evidence.PriorEffectCount != evidence.PriorEffectCount ||
			review.Evidence.CurrentEffectCount != evidence.CurrentEffectCount ||
			providerCount < review.Evidence.PriorProviderUsageCount {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return CommitAttemptResult{}, err
		}
		result.SettlementReview = &review
		return result, nil
	}

	isRelease := command.TerminalStatus != "" && command.Settlement != nil &&
		command.Settlement.Intent == SettlementIntentRelease
	if isRelease && (store.hasSettlementAuthority() || reviewFound) {
		evidence, err := inspectReplayReleaseEvidence(tx, turn.ID, operation.OperationID, operation.EffectCount)
		if err != nil {
			return CommitAttemptResult{}, err
		}
		if evidence.ambiguous() {
			if !reviewFound || review.Source != SettlementReviewSourceExecutor ||
				review.OperationID != operation.OperationID || review.AttemptID != operation.AttemptID ||
				review.FencingToken != agentv1.Sequence(operation.FencingToken) ||
				review.TerminalStatus != agentv1.TurnStatus(operation.TurnStatus) ||
				review.SettlementKey != settlementKey(turn.ID, operation.OperationID) ||
				review.Evidence != evidence {
				return CommitAttemptResult{}, ErrStoreIntegrity
			}
			if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
				return CommitAttemptResult{}, err
			}
			result.SettlementReview = &review
		} else if reviewFound {
			return CommitAttemptResult{}, ErrStoreIntegrity
		}
	} else if reviewFound {
		return CommitAttemptResult{}, ErrStoreIntegrity
	}
	return result, nil
}

// operationContentMatchesCommand protects unknown-commit recovery from a
// partially repaired or manually mutated database. The immutable Operation
// digest binds the retry command, while this comparison proves that the Event
// and Effect rows referenced by the receipt still contain that command's
// content. Mutable delivery state, including retry-adjusted AvailableAt, is
// deliberately excluded.
func operationContentMatchesCommand(
	event agentv1.EventEnvelope,
	effects []EffectOutboxRecord,
	command CommitAttemptCommand,
	review *SettlementReviewRecord,
) bool {
	expectedEvent, err := expectedOperationEvent(command, review)
	if err != nil || event.Type != expectedEvent.Type ||
		!sameStrings(event.ResourceRefs, expectedEvent.ResourceRefs) ||
		!sameJSONContent(event.Data, expectedEvent.Data) || len(effects) != len(command.Effects) {
		return false
	}
	for index, effect := range effects {
		expected := command.Effects[index]
		if effect.Ordinal != index || effect.OutboxID != expected.OutboxID ||
			effect.Topic != expected.Topic || effect.DedupeKey != expected.DedupeKey ||
			!sameJSONContent(effect.Payload, expected.Payload) {
			return false
		}
	}
	return true
}

func expectedOperationEvent(command CommitAttemptCommand, review *SettlementReviewRecord) (EventDraft, error) {
	if command.TerminalStatus == "" {
		if command.Event == nil {
			return EventDraft{}, ErrStoreIntegrity
		}
		return *command.Event, nil
	}
	return statusEventWithSettlementReview(
		command.TerminalStatus,
		command.TerminalStatus == agentv1.TurnStatusStopped,
		review,
	)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameJSONContent(left, right []byte) bool {
	leftCanonical, err := canonicalJSONContent(left)
	if err != nil {
		return false
	}
	rightCanonical, err := canonicalJSONContent(right)
	return err == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func canonicalJSONContent(raw []byte) ([]byte, error) {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, ErrStoreIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func operationEventMatchesStatus(event agentv1.EventEnvelope, status agentv1.TurnStatus) bool {
	if status == agentv1.TurnStatusRunning {
		return !stringsHasCorePrefix(event.Type)
	}
	if !status.Terminal() || event.Type != agentv1.EventCoreTurnStatus {
		return false
	}
	var payload struct {
		Status agentv1.TurnStatus `json:"status"`
	}
	return json.Unmarshal(event.Data, &payload) == nil && payload.Status == status
}

func stringsHasCorePrefix(eventType agentv1.EventType) bool {
	value := string(eventType)
	return len(value) >= len("core.") && value[:len("core.")] == "core."
}

func (store *SQLStore) resolveOperation(
	ctx context.Context,
	candidates []commitOperationCandidate,
	requireLegacy bool,
) (CommitAttemptResult, bool, error) {
	if len(candidates) == 0 {
		return CommitAttemptResult{}, false, ErrStoreIntegrity
	}
	command := candidates[0].Command
	var result CommitAttemptResult
	found := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var turnRow sqlTurnRow
		if err := tx.Where("turn_id = ?", string(command.Fence.TurnID)).Take(&turnRow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTurnNotFound
			}
			return err
		}
		turn, err := turnRow.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		operation, exists, err := store.lookupOperationTx(tx, command.Fence.TurnID, command.OperationID, false)
		if err != nil || !exists {
			return err
		}
		found = true
		candidate, matched := selectOperationReplayCandidate(operation, candidates, requireLegacy)
		if !matched {
			if requireLegacy {
				return ErrSettlementCompletedUsageUntrusted
			}
			return ErrOperationConflict
		}
		result, err = store.hydrateOperationResult(
			tx, turnRow, turn, operation, candidate.Command, candidate.Mode,
		)
		if err != nil {
			return err
		}
		if !attemptMatchesFence(result.Attempt, candidate.Command.Fence) {
			return ErrOperationConflict
		}
		result.Replay = true
		return nil
	})
	if err != nil {
		return CommitAttemptResult{}, found, err
	}
	return cloneCommitAttemptResult(result), found, nil
}

func (store *SQLStore) detectEffectConflict(ctx context.Context, command CommitAttemptCommand) (bool, error) {
	for _, effect := range command.Effects {
		var row sqlEffectOutboxRow
		err := store.db.WithContext(ctx).
			Where("outbox_id = ? OR (topic = ? AND dedupe_key = ?)", effect.OutboxID, effect.Topic, effect.DedupeKey).
			Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func buildEffectOutboxRow(attempt TurnAttempt, operationID string, ordinal int, draft EffectOutboxDraft, now time.Time) (sqlEffectOutboxRow, EffectOutboxRecord, error) {
	if err := draft.Validate(); err != nil || ordinal < 0 || ordinal >= MaxEffectsPerOperation {
		return sqlEffectOutboxRow{}, EffectOutboxRecord{}, ErrStoreIntegrity
	}
	availableAt, err := canonicalExecutionTime(draft.AvailableAt)
	if err != nil {
		return sqlEffectOutboxRow{}, EffectOutboxRecord{}, err
	}
	payload := append([]byte(nil), draft.Payload...)
	row := sqlEffectOutboxRow{
		OutboxID: draft.OutboxID, TurnID: string(attempt.TurnID), AttemptID: attempt.ID,
		TurnFencingToken: int64(attempt.FencingToken), OperationID: operationID, Ordinal: ordinal,
		Topic: draft.Topic, DedupeKey: draft.DedupeKey, PayloadJSON: payload,
		Status: "pending", AvailableAt: availableAt, CreatedAt: now, UpdatedAt: now,
	}
	record, err := row.toRecord()
	return row, record, err
}

func (row sqlEffectOutboxRow) toRecord() (EffectOutboxRecord, error) {
	if err := validatePrintableASCII("outboxId", row.OutboxID, MaxEffectOutboxIDBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if err := validatePathSegment("turnId", row.TurnID, MaxTurnIDBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if err := validatePrintableASCII("attemptId", row.AttemptID, MaxAttemptIDBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if err := validatePrintableASCII("operationId", row.OperationID, MaxOperationIDBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if err := validatePrintableASCII("topic", row.Topic, MaxEffectTopicBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if err := validateBoundedText("dedupeKey", row.DedupeKey, MaxEffectDedupeKeyBytes); err != nil {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if row.TurnFencingToken < 1 || agentv1.Sequence(row.TurnFencingToken) > MaxDurableSequence ||
		row.Ordinal < 0 || row.Ordinal >= MaxEffectsPerOperation || len(row.PayloadJSON) == 0 ||
		len(row.PayloadJSON) > MaxEffectPayloadBytes || !json.Valid(row.PayloadJSON) || row.AvailableAt.IsZero() {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	if !validEffectOutboxState(row) {
		return EffectOutboxRecord{}, ErrStoreIntegrity
	}
	return EffectOutboxRecord{
		OutboxID: row.OutboxID, TurnID: agentv1.TurnID(row.TurnID), AttemptID: row.AttemptID,
		FencingToken: agentv1.Sequence(row.TurnFencingToken), OperationID: row.OperationID,
		Ordinal: row.Ordinal, Topic: row.Topic, DedupeKey: row.DedupeKey,
		Payload: append(json.RawMessage(nil), row.PayloadJSON...), Status: row.Status,
		AvailableAt: row.AvailableAt.UTC(),
	}, nil
}

func validEffectOutboxState(row sqlEffectOutboxRow) bool {
	if row.DeliveryAttempts < 0 || row.DispatchFencingToken < 0 || row.CreatedAt.IsZero() ||
		row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return false
	}
	if row.LeaseOwnerID != nil && validatePrintableASCII("leaseOwnerId", *row.LeaseOwnerID, MaxWorkerIDBytes) != nil {
		return false
	}
	if row.LastErrorCode != nil && validatePrintableASCII("lastErrorCode", *row.LastErrorCode, MaxEffectErrorCodeBytes) != nil {
		return false
	}
	if row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(row.UpdatedAt) {
		return false
	}
	if row.DeliveredAt != nil && row.DeliveredAt.Before(row.CreatedAt) {
		return false
	}
	if row.DeadLetteredAt != nil && row.DeadLetteredAt.Before(row.CreatedAt) {
		return false
	}
	switch row.Status {
	case "pending":
		return row.LeaseOwnerID == nil && row.LeaseExpiresAt == nil && row.DeliveredAt == nil && row.DeadLetteredAt == nil
	case "delivering":
		return row.LeaseOwnerID != nil && row.LeaseExpiresAt != nil && row.DeliveryAttempts >= 1 &&
			row.DispatchFencingToken >= 1 && row.DeliveredAt == nil && row.DeadLetteredAt == nil
	case "delivered":
		return row.LeaseOwnerID == nil && row.LeaseExpiresAt == nil && row.DeliveryAttempts >= 1 &&
			row.DispatchFencingToken >= 1 && row.DeliveredAt != nil && row.DeadLetteredAt == nil
	case "dead_letter":
		return row.LeaseOwnerID == nil && row.LeaseExpiresAt == nil && row.DeliveryAttempts >= 1 &&
			row.DispatchFencingToken >= 1 && row.DeliveredAt == nil && row.DeadLetteredAt != nil
	case "review_hold":
		return row.LeaseOwnerID == nil && row.LeaseExpiresAt == nil && row.DeliveredAt == nil && row.DeadLetteredAt == nil
	default:
		return false
	}
}

func attemptStatusForTurn(status agentv1.TurnStatus) AttemptStatus {
	switch status {
	case agentv1.TurnStatusCompleted:
		return AttemptStatusCompleted
	case agentv1.TurnStatusStopped:
		return AttemptStatusStopped
	case agentv1.TurnStatusFailed:
		return AttemptStatusFailed
	case agentv1.TurnStatusTimeout:
		return AttemptStatusTimeout
	default:
		return ""
	}
}

func cloneTurnAttempt(attempt TurnAttempt) TurnAttempt {
	attempt.FinishedAt = cloneTimePointer(attempt.FinishedAt)
	return attempt
}

func cloneClaimAttemptResult(result ClaimAttemptResult) ClaimAttemptResult {
	result.Turn = cloneTurn(result.Turn)
	result.Attempt = cloneTurnAttempt(result.Attempt)
	return result
}

func cloneCommitAttemptResult(result CommitAttemptResult) CommitAttemptResult {
	result.Event = cloneEvent(result.Event)
	result.Turn = cloneTurn(result.Turn)
	result.Attempt = cloneTurnAttempt(result.Attempt)
	result.Effects = append([]EffectOutboxRecord(nil), result.Effects...)
	for index := range result.Effects {
		result.Effects[index].Payload = append(json.RawMessage(nil), result.Effects[index].Payload...)
	}
	if result.SettlementReview != nil {
		review := *result.SettlementReview
		result.SettlementReview = &review
	}
	return result
}
