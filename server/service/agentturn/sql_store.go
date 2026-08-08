package agentturn

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

const (
	SQLTurnTable      = "w_agent_turn"
	SQLTurnEventTable = "w_agent_turn_event"
)

// SQLStoreError is a sanitized persistence failure. It deliberately does not
// retain or unwrap the driver error, SQL text, DSN or persisted row values.
type SQLStoreError struct {
	Operation string
	Kind      error
}

func (err *SQLStoreError) Error() string {
	return fmt.Sprintf("durable turn SQL store %s failed: %v", err.Operation, err.Kind)
}

func (err *SQLStoreError) Unwrap() error { return err.Kind }

// SQLStore implements Store over schema created by the repository migration.
// It never migrates, creates or repairs tables and never falls back to an
// in-memory implementation.
type SQLStore struct {
	db              *gorm.DB
	dialect         string
	executionClock  func(context.Context, *gorm.DB) (time.Time, error)
	attemptLeaseTTL time.Duration
	maxTurnAttempts int64

	effectLease         time.Duration
	maxDeliveryAttempts int64
	settlementMu        sync.RWMutex
	settlement          SettlementAuthority
	settlementBinding   *SettlementAuthorityBinding
	settlementViolated  bool

	sqliteWriteMu sync.Mutex
}

var _ Store = (*SQLStore)(nil)

func NewSQLStore(db *gorm.DB) (*SQLStore, error) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return nil, fmt.Errorf("durable turn SQL store requires a configured database")
	}
	dialect := db.Dialector.Name()
	if dialect != "mysql" && dialect != "sqlite" {
		return nil, fmt.Errorf("durable turn SQL store does not support dialect %q", dialect)
	}
	return &SQLStore{
		db:              db,
		dialect:         dialect,
		executionClock:  databaseExecutionClock,
		attemptLeaseTTL: DefaultAttemptLeaseTTL,
		maxTurnAttempts: DefaultMaxTurnAttempts,

		effectLease:         DefaultEffectLeaseTTL,
		maxDeliveryAttempts: DefaultMaxDeliveryAttempts,
	}, nil
}

// turnAttemptBudget returns the configured attempt bound. The Turn fence is
// the attempt counter, so a Turn is out of budget once its fence reaches this
// value.
func (store *SQLStore) turnAttemptBudget() (int64, error) {
	if store.maxTurnAttempts <= 0 || store.maxTurnAttempts > MaxTurnAttemptsLimit {
		return 0, ErrStoreIntegrity
	}
	return store.maxTurnAttempts, nil
}

// These rows intentionally mirror migrations/20260665_create_agent_turn.sql.
// GORM tags document mapping only; SQLStore never invokes AutoMigrate.
type sqlTurnRow struct {
	ID                 uint64     `gorm:"column:id;primaryKey;autoIncrement"`
	TurnID             string     `gorm:"column:turn_id"`
	PrincipalID        string     `gorm:"column:principal_id"`
	ThreadID           string     `gorm:"column:thread_id"`
	IdempotencyKey     string     `gorm:"column:idempotency_key"`
	CommandDigest      string     `gorm:"column:command_digest"`
	PluginSnapshotJSON []byte     `gorm:"column:plugin_snapshot_json"`
	Status             string     `gorm:"column:status"`
	LastEventSequence  int64      `gorm:"column:last_event_sequence"`
	ActiveAttemptID    *string    `gorm:"column:active_attempt_id"`
	FencingToken       int64      `gorm:"column:fencing_token"`
	CancelRequestedAt  *time.Time `gorm:"column:cancel_requested_at"`
	StartedAt          *time.Time `gorm:"column:started_at"`
	FinishedAt         *time.Time `gorm:"column:finished_at"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	UpdatedAt          time.Time  `gorm:"column:updated_at"`
}

func (sqlTurnRow) TableName() string { return SQLTurnTable }

type sqlTurnEventRow struct {
	ID            uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	TurnID        string    `gorm:"column:turn_id"`
	Sequence      int64     `gorm:"column:sequence"`
	EventID       string    `gorm:"column:event_id"`
	SchemaVersion int       `gorm:"column:schema_version"`
	EventType     string    `gorm:"column:event_type"`
	EventJSON     []byte    `gorm:"column:event_json"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

func (sqlTurnEventRow) TableName() string { return SQLTurnEventTable }

func (store *SQLStore) Admit(ctx context.Context, candidate Turn, initial EventDraft) (AdmissionRecord, error) {
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}
	if err := candidate.Validate(); err != nil {
		return AdmissionRecord{}, err
	}
	if candidate.Status != agentv1.TurnStatusQueued || candidate.CancelRequestedAt != nil || candidate.StartedAt != nil || candidate.FinishedAt != nil {
		return AdmissionRecord{}, ErrInvalidTransition
	}
	if err := initial.Validate(); err != nil {
		return AdmissionRecord{}, err
	}

	if existing, found, err := store.lookupIdempotency(ctx, candidate); err != nil {
		return AdmissionRecord{}, store.normalize("admit", err)
	} else if found {
		return classifyIdempotentAdmission(existing, candidate)
	}

	first, err := buildEvent(candidate, 1, initial)
	if err != nil {
		return AdmissionRecord{}, err
	}
	admitted := cloneTurn(candidate)
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		// SQLStore owns durable lifecycle time. Caller-provided timestamps are
		// validated at the service boundary but never become database authority.
		admitted.CreatedAt = now
		admitted.UpdatedAt = now
		if err := admitted.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		turnRow, err := turnToSQLRow(admitted, 1)
		if err != nil {
			return ErrStoreIntegrity
		}
		eventRow, err := eventToSQLRow(first, now)
		if err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&turnRow).Error; err != nil {
			return err
		}
		return tx.Create(&eventRow).Error
	})
	if txErr == nil {
		return AdmissionRecord{Turn: cloneTurn(admitted), Created: true}, nil
	}
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}

	// Unique constraints arbitrate concurrent admission. Resolve the winner by
	// exact scope after rollback without parsing a driver-specific error.
	if existing, found, err := store.lookupIdempotency(ctx, candidate); err != nil {
		return AdmissionRecord{}, store.normalize("admit", err)
	} else if found {
		return classifyIdempotentAdmission(existing, candidate)
	}
	if _, found, err := store.lookupTurnByID(ctx, candidate.ID); err != nil {
		return AdmissionRecord{}, store.normalize("admit", err)
	} else if found {
		return AdmissionRecord{}, ErrTurnIDConflict
	}
	return AdmissionRecord{}, store.normalize("admit", txErr)
}

// AdmitWithReservationAuthority is the opt-in commercial admission path. A
// new Turn, its reservation binding and its initial Event commit atomically.
// Existing exact idempotency winners are locked and commercially verified
// before they are returned; this makes a retry after an unknown commit outcome
// a proof of both lifecycle and reservation admission, not merely a Turn read.
//
// Admit intentionally remains separate and unchanged. Calling the ordinary
// path can never invoke a commercial authority implicitly.
func (store *SQLStore) AdmitWithReservationAuthority(
	ctx context.Context,
	candidate Turn,
	initial EventDraft,
	authority TurnReservationAdmissionAuthority,
) (AdmissionRecord, error) {
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}
	if err := candidate.Validate(); err != nil {
		return AdmissionRecord{}, err
	}
	if candidate.Status != agentv1.TurnStatusQueued || candidate.CancelRequestedAt != nil ||
		candidate.StartedAt != nil || candidate.FinishedAt != nil {
		return AdmissionRecord{}, ErrInvalidTransition
	}
	if err := initial.Validate(); err != nil {
		return AdmissionRecord{}, err
	}
	if turnReservationAdmissionAuthorityMissing(authority) {
		return AdmissionRecord{}, ErrTurnReservationAdmissionAuthorityUnavailable
	}

	var result AdmissionRecord
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		if winner, found, err := store.verifyReservationAdmissionWinnerTx(tx, candidate, authority); err != nil {
			return err
		} else if found {
			result = winner
			return nil
		}

		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		admitted := cloneTurn(candidate)
		admitted.CreatedAt = now
		admitted.UpdatedAt = now
		if err := admitted.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		first, err := buildEvent(admitted, 1, initial)
		if err != nil {
			return err
		}
		turnRow, err := turnToSQLRow(admitted, 1)
		if err != nil {
			return ErrStoreIntegrity
		}
		eventRow, err := eventToSQLRow(first, now)
		if err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&turnRow).Error; err != nil {
			return err
		}
		// The Turn row is the owner/lock-order anchor. The authority must bind
		// its reservation now, before sequence 1 becomes durable.
		if err := authority.ReserveAndBindTurn(tx, cloneTurn(admitted)); err != nil {
			return ErrTurnReservationAdmissionFailed
		}
		if err := tx.Create(&eventRow).Error; err != nil {
			return err
		}
		result = AdmissionRecord{Turn: admitted, Created: true}
		return nil
	})
	if txErr == nil {
		result.Turn = cloneTurn(result.Turn)
		return result, nil
	}
	if err := contextError(ctx); err != nil {
		return AdmissionRecord{}, err
	}
	classified := store.normalize("admit-with-reservation", txErr)
	if !errors.Is(classified, ErrStoreUnavailable) {
		return AdmissionRecord{}, classified
	}

	// A duplicate-key race or failed COMMIT can hide a committed winner. Resolve
	// it in a fresh transaction and verify the binding under the winner's lock.
	if winner, found, err := store.resolveReservationAdmissionWinner(ctx, candidate, authority); err != nil {
		return AdmissionRecord{}, store.normalize("admit-with-reservation", err)
	} else if found {
		return winner, nil
	}
	if _, found, err := store.lookupTurnByID(ctx, candidate.ID); err != nil {
		return AdmissionRecord{}, store.normalize("admit-with-reservation", err)
	} else if found {
		return AdmissionRecord{}, ErrTurnIDConflict
	}
	return AdmissionRecord{}, classified
}

func (store *SQLStore) resolveReservationAdmissionWinner(
	ctx context.Context,
	candidate Turn,
	authority TurnReservationAdmissionAuthority,
) (AdmissionRecord, bool, error) {
	var result AdmissionRecord
	found := false
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		winner, exists, err := store.verifyReservationAdmissionWinnerTx(tx, candidate, authority)
		if err != nil {
			return err
		}
		if exists {
			result = winner
			found = true
		}
		return nil
	})
	if err != nil {
		return AdmissionRecord{}, found, err
	}
	result.Turn = cloneTurn(result.Turn)
	return result, found, nil
}

func (store *SQLStore) verifyReservationAdmissionWinnerTx(
	tx *gorm.DB,
	candidate Turn,
	authority TurnReservationAdmissionAuthority,
) (AdmissionRecord, bool, error) {
	row, err := store.lockTurn(
		tx,
		"principal_id = ? AND thread_id = ? AND idempotency_key = ?",
		string(candidate.PrincipalID), string(candidate.ThreadID), string(candidate.IdempotencyKey),
	)
	if errors.Is(err, ErrTurnNotFound) {
		return AdmissionRecord{}, false, nil
	}
	if err != nil {
		return AdmissionRecord{}, false, err
	}
	winner, err := row.toTurn()
	if err != nil {
		return AdmissionRecord{}, true, ErrStoreIntegrity
	}
	record, err := classifyIdempotentAdmission(winner, candidate)
	if err != nil {
		return AdmissionRecord{}, true, err
	}
	if err := authority.VerifyTurnBinding(tx, cloneTurn(winner)); err != nil {
		return AdmissionRecord{}, true, ErrTurnReservationBindingInvalid
	}
	record.Turn = cloneTurn(record.Turn)
	return record, true, nil
}

func (store *SQLStore) GetOwned(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID) (Turn, error) {
	if err := contextError(ctx); err != nil {
		return Turn{}, err
	}
	if err := validateOwnedStorageLookup(principalID, threadID, turnID); err != nil {
		return Turn{}, err
	}
	var row sqlTurnRow
	err := store.db.WithContext(ctx).
		Where("turn_id = ? AND principal_id = ? AND thread_id = ?", string(turnID), string(principalID), string(threadID)).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Turn{}, ErrTurnNotFound
	}
	if err != nil {
		return Turn{}, store.normalize("get-owned", err)
	}
	turn, err := row.toTurn()
	if err != nil {
		return Turn{}, store.integrity("get-owned")
	}
	return turn, nil
}

func (store *SQLStore) AppendEvent(ctx context.Context, turnID agentv1.TurnID, draft EventDraft) (agentv1.EventEnvelope, error) {
	if err := contextError(ctx); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	if err := validatePathSegment("turnId", string(turnID), MaxTurnIDBytes); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	if err := draft.Validate(); err != nil {
		return agentv1.EventEnvelope{}, err
	}
	var result agentv1.EventEnvelope
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockTurn(tx, "turn_id = ?", string(turnID))
		if err != nil {
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if row.FencingToken > 0 {
			return ErrExecutionFenceRequired
		}
		if turn.Status.Terminal() {
			return ErrTurnTerminal
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		sequence, err := nextSQLSequence(row.LastEventSequence)
		if err != nil {
			return err
		}
		event, err := buildEvent(turn, sequence, draft)
		if err != nil {
			return err
		}
		if err := store.insertEvent(tx, event, now); err != nil {
			return err
		}
		if err := store.updateTurnColumns(tx, turn.ID, map[string]any{"last_event_sequence": int64(sequence)}); err != nil {
			return err
		}
		result = event
		return nil
	})
	if err != nil {
		return agentv1.EventEnvelope{}, store.normalize("append-event", err)
	}
	return cloneEvent(result), nil
}

func (store *SQLStore) Transition(ctx context.Context, turnID agentv1.TurnID, to agentv1.TurnStatus, at time.Time, draft EventDraft) (TransitionResult, error) {
	if err := contextError(ctx); err != nil {
		return TransitionResult{}, err
	}
	if err := validatePathSegment("turnId", string(turnID), MaxTurnIDBytes); err != nil {
		return TransitionResult{}, err
	}
	if !to.Valid() {
		return TransitionResult{}, ErrInvalidTransition
	}
	if at.IsZero() {
		return TransitionResult{}, fmt.Errorf("transition time is required")
	}
	if err := draft.Validate(); err != nil {
		return TransitionResult{}, err
	}
	var result TransitionResult
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockTurn(tx, "turn_id = ?", string(turnID))
		if err != nil {
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if row.FencingToken > 0 {
			return ErrExecutionFenceRequired
		}
		if turn.Status == to {
			result = TransitionResult{Turn: turn}
			return nil
		}
		if !CanTransition(turn.Status, to) {
			return transitionError(turn.Status, to)
		}
		if to == agentv1.TurnStatusStopped && turn.CancelRequestedAt == nil {
			return ErrCancellationNotRequested
		}

		// SQLStore owns durable lifecycle time. The caller-supplied `at` is a
		// validated intent timestamp; only the storage clock that produced
		// created_at may advance the lifecycle columns, otherwise an admission
		// written from database time and a transition written from process time
		// can order backwards.
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		next := cloneTurn(turn)
		next.Status = to
		next.UpdatedAt = now
		if to == agentv1.TurnStatusRunning && next.StartedAt == nil {
			next.StartedAt = timePointer(now)
		}
		if to.Terminal() {
			next.FinishedAt = timePointer(now)
		}
		if err := next.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		sequence, err := nextSQLSequence(row.LastEventSequence)
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
		if err := store.updateTurnColumns(tx, next.ID, map[string]any{
			"status":              string(next.Status),
			"last_event_sequence": int64(sequence),
			"updated_at":          next.UpdatedAt,
			"started_at":          next.StartedAt,
			"finished_at":         next.FinishedAt,
		}); err != nil {
			return err
		}
		result = TransitionResult{Turn: next, Changed: true, Event: eventPointer(event)}
		return nil
	})
	if err != nil {
		return TransitionResult{}, store.normalize("transition", err)
	}
	return result, nil
}

func (store *SQLStore) RequestCancel(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, at time.Time, draft EventDraft) (CancelResult, error) {
	if err := contextError(ctx); err != nil {
		return CancelResult{}, err
	}
	if err := validateOwnedStorageLookup(principalID, threadID, turnID); err != nil {
		return CancelResult{}, err
	}
	if at.IsZero() {
		return CancelResult{}, fmt.Errorf("cancellation time is required")
	}
	if err := draft.Validate(); err != nil {
		return CancelResult{}, err
	}
	var result CancelResult
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockTurn(tx, "turn_id = ? AND principal_id = ? AND thread_id = ?", string(turnID), string(principalID), string(threadID))
		if err != nil {
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if turn.Status.Terminal() || turn.CancelRequestedAt != nil {
			result = CancelResult{Turn: turn}
			return nil
		}

		// Cancellation intent is recorded on the storage clock for the same
		// reason as Transition: `at` is validated caller intent, not authority.
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		next := cloneTurn(turn)
		next.CancelRequestedAt = timePointer(now)
		next.UpdatedAt = now
		if err := next.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		sequence, err := nextSQLSequence(row.LastEventSequence)
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
		if err := store.updateTurnColumns(tx, next.ID, map[string]any{
			"cancel_requested_at": next.CancelRequestedAt,
			"last_event_sequence": int64(sequence),
			"updated_at":          next.UpdatedAt,
		}); err != nil {
			return err
		}
		result = CancelResult{Turn: next, NewlyRequested: true, Event: eventPointer(event)}
		return nil
	})
	if err != nil {
		return CancelResult{}, store.normalize("request-cancel", err)
	}
	return result, nil
}

func (store *SQLStore) Replay(ctx context.Context, principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID, query ReplayQuery) (ReplayRecord, error) {
	if err := contextError(ctx); err != nil {
		return ReplayRecord{}, err
	}
	if err := validateOwnedStorageLookup(principalID, threadID, turnID); err != nil {
		return ReplayRecord{}, err
	}
	if err := query.Validate(); err != nil {
		return ReplayRecord{}, err
	}
	var result ReplayRecord
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row sqlTurnRow
		if err := tx.Where("turn_id = ? AND principal_id = ? AND thread_id = ?", string(turnID), string(principalID), string(threadID)).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrTurnNotFound
			}
			return err
		}
		turn, err := row.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		window, err := store.loadReplayWindow(tx, row)
		if err != nil {
			return err
		}

		resolvedAfter := agentv1.Sequence(0)
		hasExplicitCursor := false
		if query.Cursor.AfterSequence != nil {
			hasExplicitCursor = true
			resolvedAfter = *query.Cursor.AfterSequence
		}
		if query.Cursor.LastEventID != "" {
			hasExplicitCursor = true
			var cursorRow sqlTurnEventRow
			err := tx.Where("turn_id = ? AND event_id = ? AND sequence <= ?", string(turnID), query.Cursor.LastEventID, row.LastEventSequence).Take(&cursorRow).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if sequence, ok := parseDeterministicEventID(turnID, query.Cursor.LastEventID); ok {
					// A deterministic ID outside the retained window still
					// resolves to its sequence for boundary classification and
					// for furthest-of-two cursor semantics. A missing ID inside
					// the retained window is not a valid stored-event cursor.
					if sequence < window.OldestSequence || sequence > window.LatestSequence {
						if sequence > resolvedAfter {
							resolvedAfter = sequence
						}
					} else {
						return ErrReplayCursorNotFound
					}
				} else {
					return ErrReplayCursorNotFound
				}
			} else if err != nil {
				return err
			} else {
				cursorEvent, err := cursorRow.toEnvelope(turn)
				if err != nil || cursorEvent.Sequence < window.OldestSequence || cursorEvent.Sequence > window.LatestSequence {
					return ErrStoreIntegrity
				}
				if cursorEvent.Sequence > resolvedAfter {
					resolvedAfter = cursorEvent.Sequence
				}
			}
		}
		if hasExplicitCursor {
			if resolvedAfter > window.LatestSequence {
				return &ReplayBoundaryError{Kind: ErrReplayCursorAhead, Cursor: resolvedAfter, Window: window}
			}
			if resolvedAfter < window.OldestSequence-1 {
				return &ReplayBoundaryError{Kind: ErrReplayGap, Cursor: resolvedAfter, Window: window}
			}
		}

		lower := resolvedAfter
		if !hasExplicitCursor {
			lower = window.OldestSequence - 1
		}
		var rows []sqlTurnEventRow
		if err := tx.Where("turn_id = ? AND sequence > ? AND sequence <= ?", string(turnID), int64(lower), row.LastEventSequence).
			Order("sequence ASC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
			return err
		}
		hasMore := len(rows) > query.Limit
		if hasMore {
			rows = rows[:query.Limit]
		}
		events := make([]agentv1.EventEnvelope, 0, len(rows))
		seenEventIDs := make(map[string]struct{}, len(rows))
		previous := lower
		for _, eventRow := range rows {
			event, err := eventRow.toEnvelope(turn)
			if err != nil || event.Sequence <= previous || event.Sequence > window.LatestSequence {
				return ErrStoreIntegrity
			}
			if _, exists := seenEventIDs[event.EventID]; exists {
				return ErrStoreIntegrity
			}
			seenEventIDs[event.EventID] = struct{}{}
			events = append(events, event)
			previous = event.Sequence
		}
		result = ReplayRecord{
			Turn:          turn,
			AfterSequence: resolvedAfter,
			Window:        window,
			Events:        events,
			HasMore:       hasMore,
		}
		return nil
	})
	if err != nil {
		return ReplayRecord{}, store.normalize("replay", err)
	}
	return result, nil
}

func (store *SQLStore) loadReplayWindow(tx *gorm.DB, row sqlTurnRow) (agentv1.ReplayWindow, error) {
	if row.LastEventSequence < 1 || agentv1.Sequence(row.LastEventSequence) > MaxDurableSequence {
		return agentv1.ReplayWindow{}, ErrStoreIntegrity
	}
	var bounds struct {
		OldestSequence sql.NullInt64 `gorm:"column:oldest_sequence"`
	}
	if err := tx.Table(SQLTurnEventTable).
		Select("MIN(sequence) AS oldest_sequence").
		Where("turn_id = ? AND sequence <= ?", row.TurnID, row.LastEventSequence).
		Scan(&bounds).Error; err != nil {
		return agentv1.ReplayWindow{}, err
	}
	if !bounds.OldestSequence.Valid || bounds.OldestSequence.Int64 < 1 || bounds.OldestSequence.Int64 > row.LastEventSequence {
		return agentv1.ReplayWindow{}, ErrStoreIntegrity
	}
	window := agentv1.ReplayWindow{
		OldestSequence: agentv1.Sequence(bounds.OldestSequence.Int64),
		LatestSequence: agentv1.Sequence(row.LastEventSequence),
	}
	if window.LatestSequence > MaxDurableSequence || window.Validate() != nil {
		return agentv1.ReplayWindow{}, ErrStoreIntegrity
	}
	return window, nil
}

func (store *SQLStore) lockTurn(tx *gorm.DB, predicate string, args ...any) (sqlTurnRow, error) {
	var row sqlTurnRow
	if store.dialect == "sqlite" {
		result := tx.Table(SQLTurnTable).Where(predicate, args...).UpdateColumn("last_event_sequence", gorm.Expr("last_event_sequence"))
		if result.Error != nil {
			return row, result.Error
		}
		if result.RowsAffected != 1 {
			return row, ErrTurnNotFound
		}
		if err := tx.Where(predicate, args...).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return row, ErrTurnNotFound
			}
			return row, err
		}
		return row, nil
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(predicate, args...).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, ErrTurnNotFound
		}
		return row, err
	}
	return row, nil
}

func (store *SQLStore) insertEvent(tx *gorm.DB, event agentv1.EventEnvelope, now time.Time) error {
	row, err := eventToSQLRow(event, now)
	if err != nil {
		return ErrStoreIntegrity
	}
	return tx.Create(&row).Error
}

func (store *SQLStore) updateTurnColumns(tx *gorm.DB, turnID agentv1.TurnID, columns map[string]any) error {
	result := tx.Table(SQLTurnTable).Where("turn_id = ?", string(turnID)).UpdateColumns(columns)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStoreIntegrity
	}
	return nil
}

func (store *SQLStore) lookupIdempotency(ctx context.Context, candidate Turn) (Turn, bool, error) {
	var row sqlTurnRow
	err := store.db.WithContext(ctx).Where(
		"principal_id = ? AND thread_id = ? AND idempotency_key = ?",
		string(candidate.PrincipalID), string(candidate.ThreadID), string(candidate.IdempotencyKey),
	).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Turn{}, false, nil
	}
	if err != nil {
		return Turn{}, false, err
	}
	turn, err := row.toTurn()
	if err != nil {
		return Turn{}, false, ErrStoreIntegrity
	}
	if turn.PrincipalID != candidate.PrincipalID || turn.ThreadID != candidate.ThreadID || turn.IdempotencyKey != candidate.IdempotencyKey {
		return Turn{}, false, ErrStoreIntegrity
	}
	return turn, true, nil
}

func (store *SQLStore) lookupTurnByID(ctx context.Context, turnID agentv1.TurnID) (Turn, bool, error) {
	var row sqlTurnRow
	err := store.db.WithContext(ctx).Where("turn_id = ?", string(turnID)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Turn{}, false, nil
	}
	if err != nil {
		return Turn{}, false, err
	}
	turn, err := row.toTurn()
	if err != nil {
		return Turn{}, false, ErrStoreIntegrity
	}
	return turn, true, nil
}

// SQLite write operations are serialized per Store instance. This avoids a
// whole-transaction retry after an ambiguous commit; busy_timeout remains the
// database-level policy for contention with other processes/connections.
func (store *SQLStore) writeTransaction(ctx context.Context, transaction func(*gorm.DB) error) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if store.dialect == "sqlite" {
		store.sqliteWriteMu.Lock()
		defer store.sqliteWriteMu.Unlock()
		if err := contextError(ctx); err != nil {
			return err
		}
	}
	return store.db.WithContext(ctx).Transaction(transaction)
}

func (store *SQLStore) normalize(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	for _, known := range []error{
		ErrTurnNotFound, ErrTurnIDConflict, ErrIdempotencyConflict,
		ErrInvalidTransition, ErrTurnTerminal, ErrCancellationNotRequested,
		ErrReplayCursorNotFound, ErrReplayCursorAhead, ErrReplayGap,
		ErrSequenceExhausted, ErrAttemptNotFound, ErrAttemptConflict,
		ErrAttemptBusy, ErrAttemptFenced, ErrAttemptLeaseExpired,
		ErrAttemptCancelled, ErrAttemptFenceExhausted, ErrOperationConflict,
		ErrEffectConflict, ErrExecutionFenceRequired,
		ErrAttemptBudgetExhausted, ErrPluginScopeMismatch, ErrReconcilePrecondition,
		ErrEffectNotFound, ErrEffectFenced, ErrEffectTerminal, ErrEffectScopeMismatch,
		ErrSettlementFailed, ErrSettlementUsageUnknown, ErrSettlementReviewFailed,
		ErrSettlementBindingInvalid, ErrSettlementReviewNotFound,
		ErrSettlementReviewResolutionUnavailable, ErrSettlementReviewResolutionConflict,
		ErrSettlementReviewResolutionFailed, ErrSettlementReviewUnitsExceedReserved,
		ErrSettlementReviewUsageUnavailable, ErrSettlementReviewUsageConflict,
		ErrSettlementReviewUsageFailed, ErrSettlementReviewUsagePending,
		ErrSettlementReviewUsageOverflow,
		ErrSettlementCompletedUsageUntrusted,
		ErrTurnReservationAdmissionAuthorityUnavailable,
		ErrTurnReservationAdmissionFailed, ErrTurnReservationBindingInvalid,
		ErrTurnReservationExecutionUnauthorized, ErrTurnReservationExecutionExpired,
	} {
		if errors.Is(err, known) {
			return err
		}
	}
	if errors.Is(err, ErrStoreIntegrity) {
		return store.integrity(operation)
	}
	return &SQLStoreError{Operation: operation, Kind: ErrStoreUnavailable}
}

func (store *SQLStore) integrity(operation string) error {
	return &SQLStoreError{Operation: operation, Kind: ErrStoreIntegrity}
}

func classifyIdempotentAdmission(existing, candidate Turn) (AdmissionRecord, error) {
	if existing.CommandDigest != candidate.CommandDigest {
		return AdmissionRecord{}, ErrIdempotencyConflict
	}
	return AdmissionRecord{Turn: existing, Created: false}, nil
}

func validateOwnedStorageLookup(principalID PrincipalID, threadID agentv1.ThreadID, turnID agentv1.TurnID) error {
	if err := validateBoundedText("principalId", string(principalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(threadID), MaxThreadIDBytes); err != nil {
		return err
	}
	return validatePathSegment("turnId", string(turnID), MaxTurnIDBytes)
}

func nextSQLSequence(last int64) (agentv1.Sequence, error) {
	if last < 1 {
		return 0, ErrStoreIntegrity
	}
	if agentv1.Sequence(last) >= MaxDurableSequence {
		return 0, ErrSequenceExhausted
	}
	return agentv1.Sequence(last + 1), nil
}

func turnToSQLRow(turn Turn, last agentv1.Sequence) (sqlTurnRow, error) {
	pluginJSON, err := json.Marshal(turn.Plugin)
	if err != nil {
		return sqlTurnRow{}, err
	}
	return sqlTurnRow{
		TurnID:             string(turn.ID),
		PrincipalID:        string(turn.PrincipalID),
		ThreadID:           string(turn.ThreadID),
		IdempotencyKey:     string(turn.IdempotencyKey),
		CommandDigest:      turn.CommandDigest,
		PluginSnapshotJSON: pluginJSON,
		Status:             string(turn.Status),
		LastEventSequence:  int64(last),
		CancelRequestedAt:  utcTimePointer(turn.CancelRequestedAt),
		StartedAt:          utcTimePointer(turn.StartedAt),
		FinishedAt:         utcTimePointer(turn.FinishedAt),
		CreatedAt:          turn.CreatedAt.UTC(),
		UpdatedAt:          turn.UpdatedAt.UTC(),
	}, nil
}

func (row sqlTurnRow) toTurn() (Turn, error) {
	if row.LastEventSequence < 1 || agentv1.Sequence(row.LastEventSequence) > MaxDurableSequence {
		return Turn{}, ErrStoreIntegrity
	}
	if row.FencingToken < 0 || agentv1.Sequence(row.FencingToken) > MaxDurableSequence {
		return Turn{}, ErrStoreIntegrity
	}
	if row.ActiveAttemptID != nil {
		if row.FencingToken < 1 || validatePrintableASCII("activeAttemptId", *row.ActiveAttemptID, MaxAttemptIDBytes) != nil {
			return Turn{}, ErrStoreIntegrity
		}
	}
	var plugin agentv1.EventPluginRef
	if err := decodeStrictJSON(row.PluginSnapshotJSON, &plugin); err != nil {
		return Turn{}, ErrStoreIntegrity
	}
	turn := Turn{
		ID:                agentv1.TurnID(row.TurnID),
		PrincipalID:       PrincipalID(row.PrincipalID),
		ThreadID:          agentv1.ThreadID(row.ThreadID),
		IdempotencyKey:    agentv1.IdempotencyKey(row.IdempotencyKey),
		CommandDigest:     row.CommandDigest,
		Plugin:            plugin,
		Status:            agentv1.TurnStatus(row.Status),
		CancelRequestedAt: utcTimePointer(row.CancelRequestedAt),
		StartedAt:         utcTimePointer(row.StartedAt),
		FinishedAt:        utcTimePointer(row.FinishedAt),
		CreatedAt:         row.CreatedAt.UTC(),
		UpdatedAt:         row.UpdatedAt.UTC(),
	}
	if err := turn.Validate(); err != nil {
		return Turn{}, ErrStoreIntegrity
	}
	return turn, nil
}

// eventToSQLRow requires the durable insert time supplied by the enclosing
// transaction. Append order is arbitrated by the monotonic per-Turn Sequence;
// created_at is durable audit metadata and must come from the same database
// clock as the Turn/Attempt columns written in that transaction, never from
// the process wall clock.
func eventToSQLRow(event agentv1.EventEnvelope, now time.Time) (sqlTurnEventRow, error) {
	if err := validateStoredEnvelope(event); err != nil {
		return sqlTurnEventRow{}, err
	}
	canonicalNow, err := canonicalExecutionTime(now)
	if err != nil {
		return sqlTurnEventRow{}, ErrStoreIntegrity
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return sqlTurnEventRow{}, err
	}
	return sqlTurnEventRow{
		TurnID:        string(event.TurnID),
		Sequence:      int64(event.Sequence),
		EventID:       event.EventID,
		SchemaVersion: event.SchemaVersion,
		EventType:     string(event.Type),
		EventJSON:     eventJSON,
		CreatedAt:     canonicalNow,
	}, nil
}

func (row sqlTurnEventRow) toEnvelope(turn Turn) (agentv1.EventEnvelope, error) {
	if row.Sequence < 1 || agentv1.Sequence(row.Sequence) > MaxDurableSequence {
		return agentv1.EventEnvelope{}, ErrStoreIntegrity
	}
	var event agentv1.EventEnvelope
	if err := decodeStrictJSON(row.EventJSON, &event); err != nil {
		return agentv1.EventEnvelope{}, ErrStoreIntegrity
	}
	if event.TurnID != turn.ID || event.Plugin != turn.Plugin ||
		event.Sequence != agentv1.Sequence(row.Sequence) || event.EventID != row.EventID ||
		event.SchemaVersion != row.SchemaVersion || string(event.Type) != row.EventType ||
		event.EventID != deterministicEventID(turn.ID, event.Sequence) {
		return agentv1.EventEnvelope{}, ErrStoreIntegrity
	}
	if err := validateStoredEnvelope(event); err != nil {
		return agentv1.EventEnvelope{}, ErrStoreIntegrity
	}
	return event, nil
}

func validateStoredEnvelope(event agentv1.EventEnvelope) error {
	if err := event.Validate(); err != nil || event.Sequence > MaxDurableSequence {
		return ErrStoreIntegrity
	}
	if err := validatePluginRef(event.Plugin); err != nil {
		return ErrStoreIntegrity
	}
	if err := (EventDraft{Type: event.Type, ResourceRefs: event.ResourceRefs, Data: event.Data}).Validate(); err != nil {
		return ErrStoreIntegrity
	}
	return nil
}

func decodeStrictJSON(raw []byte, target any) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return ErrStoreIntegrity
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrStoreIntegrity
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrStoreIntegrity
	}
	return nil
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func deterministicEventID(turnID agentv1.TurnID, sequence agentv1.Sequence) string {
	return fmt.Sprintf("%s:%d", turnID, sequence)
}

func parseDeterministicEventID(turnID agentv1.TurnID, eventID string) (agentv1.Sequence, bool) {
	prefix := string(turnID) + ":"
	if !strings.HasPrefix(eventID, prefix) {
		return 0, false
	}
	raw := strings.TrimPrefix(eventID, prefix)
	if raw == "" || (len(raw) > 1 && raw[0] == '0') {
		return 0, false
	}
	parsed, err := strconv.ParseUint(raw, 10, 63)
	if err != nil || parsed == 0 {
		return 0, false
	}
	return agentv1.Sequence(parsed), true
}
