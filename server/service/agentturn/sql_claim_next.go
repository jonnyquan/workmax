package agentturn

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
)

var _ ReclaimScanner = (*SQLStore)(nil)

// ClaimNext turns the explicit ClaimAttempt primitive into work discovery.
//
// It is a queue over the authoritative Turn table rather than a second store:
// candidates are read without locks, and every candidate is then contended
// through ClaimAttempt, which remains the only ownership authority. A losing
// scanner therefore observes ErrAttemptBusy and moves on instead of producing
// a second live executor.
//
// Discovery deliberately refuses two classes that the schema still lists as
// non-terminal: Turns with a recorded cancellation intent and Turns whose
// fence is exhausted. Both need a Reconciler decision, not a fresh execution
// epoch; ListReclaimableTurns surfaces the first class.
//
// This is a persistence primitive. It does not poll, notify, back off, run a
// worker loop, heartbeat, reconcile leases, dispatch effects or settle
// credits, and no production router or worker composes it.
func (store *SQLStore) ClaimNext(ctx context.Context, command ClaimNextCommand) (ClaimAttemptResult, error) {
	if err := contextError(ctx); err != nil {
		return ClaimAttemptResult{}, err
	}
	if err := command.Validate(); err != nil {
		return ClaimAttemptResult{}, err
	}

	// Resolve an already-bound Attempt before discovering new work. Without
	// this, a worker retrying after an unknown outcome would scan for a
	// different Turn while its first Attempt still holds a live lease, and
	// ClaimAttempt would reject the reused ID as an identity conflict.
	if existing, found, err := store.lookupAttemptByID(ctx, command.AttemptID); err != nil {
		return ClaimAttemptResult{}, store.normalize("claim-next", err)
	} else if found {
		return store.ClaimAttempt(ctx, command.claimFor(agentv1.TurnID(existing.TurnID)))
	}

	candidates, err := store.scanClaimableTurns(ctx, command.scanLimit(), command.PluginScope)
	if err != nil {
		return ClaimAttemptResult{}, store.normalize("claim-next", err)
	}
	for _, turnID := range candidates {
		result, claimErr := store.ClaimAttempt(ctx, command.claimFor(turnID))
		if claimErr == nil {
			return result, nil
		}
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ClaimAttemptResult{}, ctxErr
		}
		if claimNextSkips(claimErr) {
			continue
		}
		return ClaimAttemptResult{}, claimErr
	}
	return ClaimAttemptResult{}, ErrNoClaimableTurn
}

// claimNextSkips separates "this candidate is no longer ours to take" from
// real failures. Every skipped condition is one another actor legitimately
// caused between the unlocked scan and the locked claim. Identity conflicts
// and integrity errors are never skipped: they mean the caller reused an
// Attempt ID or the schema disagrees with the store, and silently scanning
// past either would hide the defect behind an empty queue.
func claimNextSkips(err error) bool {
	for _, skippable := range []error{
		ErrAttemptBusy,
		ErrTurnTerminal,
		ErrTurnNotFound,
		ErrAttemptCancelled,
		ErrAttemptFenceExhausted,
		ErrAttemptBudgetExhausted,
		// Only exact commercial expiry is a candidate-local condition. The
		// opaque Unauthorized sentinel may represent missing bindings, drift or
		// infrastructure failure and must stop the scan.
		ErrTurnReservationExecutionExpired,
	} {
		if errors.Is(err, skippable) {
			return true
		}
	}
	return false
}

// scanClaimableTurns reads candidate Turn IDs oldest-first. FIFO ordering is
// the starvation guard: a Turn that repeatedly loses a race keeps its position
// ahead of later admissions instead of drifting to the back of every scan.
func (store *SQLStore) scanClaimableTurns(
	ctx context.Context,
	limit int,
	pluginScope []agentv1.EventPluginRef,
) ([]agentv1.TurnID, error) {
	now, err := store.executionNow(ctx, store.db)
	if err != nil {
		return nil, err
	}
	budget, err := store.turnAttemptBudget()
	if err != nil {
		return nil, err
	}
	pluginPredicate, pluginArgs, err := claimPluginScopePredicate(store.dialect, pluginScope)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf(`
		SELECT t.turn_id AS turn_id
		FROM %s AS t
		LEFT JOIN %s AS a
		  ON a.turn_id = t.turn_id AND a.attempt_id = t.active_attempt_id
		WHERE t.cancel_requested_at IS NULL
		  AND t.fencing_token < ?
		  AND (
		        (t.status = ? AND t.active_attempt_id IS NULL)
		     OR (t.status = ? AND t.active_attempt_id IS NOT NULL
		         AND a.status = ? AND a.lease_expires_at <= ?)
		      )%s
		ORDER BY t.created_at ASC, t.id ASC
		LIMIT ?`, SQLTurnTable, SQLTurnAttemptTable, pluginPredicate)

	var rows []struct {
		TurnID string `gorm:"column:turn_id"`
	}
	queryArgs := []any{
		budget,
		string(agentv1.TurnStatusQueued),
		string(agentv1.TurnStatusRunning),
		string(AttemptStatusRunning),
		now,
	}
	queryArgs = append(queryArgs, pluginArgs...)
	queryArgs = append(queryArgs, limit)
	if err := store.db.WithContext(ctx).Raw(statement, queryArgs...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	candidates := make([]agentv1.TurnID, 0, len(rows))
	for _, row := range rows {
		if err := validatePathSegment("turnId", row.TurnID, MaxTurnIDBytes); err != nil {
			return nil, ErrStoreIntegrity
		}
		candidates = append(candidates, agentv1.TurnID(row.TurnID))
	}
	return candidates, nil
}

func claimPluginScopePredicate(dialect string, scope []agentv1.EventPluginRef) (string, []any, error) {
	if len(scope) == 0 {
		return "", nil, nil
	}
	if err := validateClaimPluginScope(scope); err != nil {
		return "", nil, err
	}
	var idExpression, versionExpression, digestExpression string
	switch dialect {
	case "mysql":
		// MySQL string equality inherits the column/session collation. Explicit
		// binary casts keep release identity case-sensitive even when the schema
		// default is case-insensitive.
		idExpression = "CAST(JSON_UNQUOTE(JSON_EXTRACT(t.plugin_snapshot_json, '$.id')) AS BINARY) = CAST(? AS BINARY)"
		versionExpression = "CAST(JSON_UNQUOTE(JSON_EXTRACT(t.plugin_snapshot_json, '$.version')) AS BINARY) = CAST(? AS BINARY)"
		digestExpression = "CAST(JSON_UNQUOTE(JSON_EXTRACT(t.plugin_snapshot_json, '$.releaseDigest')) AS BINARY) = CAST(? AS BINARY)"
	case "sqlite":
		idExpression = "json_extract(t.plugin_snapshot_json, '$.id') = ? COLLATE BINARY"
		versionExpression = "json_extract(t.plugin_snapshot_json, '$.version') = ? COLLATE BINARY"
		digestExpression = "json_extract(t.plugin_snapshot_json, '$.releaseDigest') = ? COLLATE BINARY"
	default:
		return "", nil, ErrStoreUnavailable
	}
	clauses := make([]string, 0, len(scope))
	args := make([]any, 0, len(scope)*3)
	for _, plugin := range scope {
		clauses = append(clauses, fmt.Sprintf("(%s AND %s AND %s)",
			idExpression, versionExpression, digestExpression))
		args = append(args, plugin.ID, plugin.Version, plugin.ReleaseDigest)
	}
	return "\n\t\t  AND (" + strings.Join(clauses, " OR ") + ")", args, nil
}

// ListReclaimableTurns reports non-terminal Turns that currently have no live
// executor. It is a bounded read: it takes no lock, mutates nothing and
// confers no execution rights, so two Reconcilers may observe the same row and
// must still contend through ReconcileTerminal.
//
// A Turn with a live Attempt is always excluded, including a cancelled one:
// its owner learns of the intent through HeartbeatAttempt and is expected to
// commit the `stopped` terminal itself. A Turn that is merely waiting for its
// first claim is also excluded — it is queue work, not stuck work.
//
// Rows are classified by why the Turn cannot resume, most decisive first:
// a recorded cancellation, then an exhausted attempt budget, then a lapsed
// lease that still has budget left. Only the first two are Actionable; the
// third is retry traffic reported for observability.
func (store *SQLStore) ListReclaimableTurns(ctx context.Context, query ReclaimQuery) ([]ReclaimableTurn, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	now, err := store.executionNow(ctx, store.db)
	if err != nil {
		return nil, store.normalize("list-reclaimable", err)
	}
	budget, err := store.turnAttemptBudget()
	if err != nil {
		return nil, store.normalize("list-reclaimable", err)
	}
	statement := fmt.Sprintf(`
		SELECT t.turn_id AS turn_id,
		       t.status AS turn_status,
		       t.fencing_token AS fencing_token,
		       t.cancel_requested_at AS cancel_requested_at,
		       a.attempt_id AS attempt_id,
		       a.worker_id AS worker_id,
		       a.lease_expires_at AS lease_expires_at
		FROM %s AS t
		LEFT JOIN %s AS a
		  ON a.turn_id = t.turn_id AND a.attempt_id = t.active_attempt_id
		WHERE t.status IN (?, ?)
		  AND (t.active_attempt_id IS NULL
		       OR (a.status = ? AND a.lease_expires_at <= ?))
		  AND (t.cancel_requested_at IS NOT NULL
		       OR t.fencing_token >= ?
		       OR t.active_attempt_id IS NOT NULL)
		ORDER BY t.created_at ASC, t.id ASC
		LIMIT ?`, SQLTurnTable, SQLTurnAttemptTable)

	var rows []sqlReclaimableRow
	if err := store.db.WithContext(ctx).Raw(statement,
		string(agentv1.TurnStatusQueued),
		string(agentv1.TurnStatusRunning),
		string(AttemptStatusRunning),
		now,
		budget,
		query.limit(),
	).Scan(&rows).Error; err != nil {
		return nil, store.normalize("list-reclaimable", err)
	}

	reclaimable := make([]ReclaimableTurn, 0, len(rows))
	for _, row := range rows {
		entry, err := row.toReclaimableTurn(budget)
		if err != nil {
			return nil, store.integrity("list-reclaimable")
		}
		if query.ActionableOnly && !entry.Reason.Actionable() {
			continue
		}
		reclaimable = append(reclaimable, entry)
	}
	return reclaimable, nil
}

type sqlReclaimableRow struct {
	TurnID            string     `gorm:"column:turn_id"`
	TurnStatus        string     `gorm:"column:turn_status"`
	FencingToken      int64      `gorm:"column:fencing_token"`
	CancelRequestedAt *time.Time `gorm:"column:cancel_requested_at"`
	AttemptID         *string    `gorm:"column:attempt_id"`
	WorkerID          *string    `gorm:"column:worker_id"`
	LeaseExpiresAt    *time.Time `gorm:"column:lease_expires_at"`
}

func (row sqlReclaimableRow) toReclaimableTurn(budget int64) (ReclaimableTurn, error) {
	if err := validatePathSegment("turnId", row.TurnID, MaxTurnIDBytes); err != nil {
		return ReclaimableTurn{}, ErrStoreIntegrity
	}
	status := agentv1.TurnStatus(row.TurnStatus)
	if !status.Valid() || status.Terminal() {
		return ReclaimableTurn{}, ErrStoreIntegrity
	}
	if row.FencingToken < 0 || agentv1.Sequence(row.FencingToken) > MaxDurableSequence {
		return ReclaimableTurn{}, ErrStoreIntegrity
	}
	entry := ReclaimableTurn{
		TurnID:            agentv1.TurnID(row.TurnID),
		Status:            status,
		FencingToken:      agentv1.Sequence(row.FencingToken),
		CancelRequestedAt: utcTimePointer(row.CancelRequestedAt),
	}
	// Attempt details describe the dead epoch and are attached whatever the
	// reason, so an operator can see which worker stopped heartbeating even on
	// a row classified by cancellation.
	if row.AttemptID != nil {
		if row.LeaseExpiresAt == nil {
			return ReclaimableTurn{}, ErrStoreIntegrity
		}
		if err := validatePrintableASCII("attemptId", *row.AttemptID, MaxAttemptIDBytes); err != nil {
			return ReclaimableTurn{}, ErrStoreIntegrity
		}
		entry.AttemptID = *row.AttemptID
		entry.LeaseExpiresAt = utcTimePointer(row.LeaseExpiresAt)
		if row.WorkerID != nil {
			entry.WorkerID = *row.WorkerID
		}
	}
	switch {
	case entry.CancelRequestedAt != nil:
		entry.Reason = ReclaimReasonCancellationPending
	case row.FencingToken >= budget:
		entry.Reason = ReclaimReasonAttemptsExhausted
	case row.AttemptID != nil:
		entry.Reason = ReclaimReasonLeaseExpired
	default:
		return ReclaimableTurn{}, ErrStoreIntegrity
	}
	if !entry.Reason.Valid() {
		return ReclaimableTurn{}, ErrStoreIntegrity
	}
	return entry, nil
}

func (store *SQLStore) lookupAttemptByID(ctx context.Context, attemptID string) (sqlTurnAttemptRow, bool, error) {
	var row sqlTurnAttemptRow
	err := store.db.WithContext(ctx).Where("attempt_id = ?", attemptID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return sqlTurnAttemptRow{}, false, nil
	}
	if err != nil {
		return sqlTurnAttemptRow{}, false, err
	}
	return row, true, nil
}
