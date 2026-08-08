package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

var (
	ErrEffectNotFound      = errors.New("durable turn effect not found")
	ErrEffectFenced        = errors.New("durable turn effect dispatch lease was superseded")
	ErrEffectTerminal      = errors.New("durable turn effect already reached a terminal delivery state")
	ErrEffectScopeMismatch = errors.New("durable turn effect is outside the dispatcher scope")
	ErrNoClaimableEffects  = errors.New("durable turn effect outbox has no claimable work")
)

const (
	DefaultEffectLeaseTTL      = 30 * time.Second
	MaxEffectLeaseTTL          = 10 * time.Minute
	DefaultMaxDeliveryAttempts = 5
	MaxDeliveryAttemptsLimit   = 64
	DefaultEffectRetryBackoff  = time.Second
	MaxEffectRetryBackoff      = 5 * time.Minute
	DefaultEffectClaimBatch    = 16
	MaxEffectClaimBatch        = 128
	MaxEffectErrorCodeBytes    = 64
)

type EffectStatus string

const (
	EffectStatusPending    EffectStatus = "pending"
	EffectStatusDelivering EffectStatus = "delivering"
	EffectStatusDelivered  EffectStatus = "delivered"
	EffectStatusDeadLetter EffectStatus = "dead_letter"
	// EffectStatusReviewHold is a durable commercial fence. It is neither
	// claimable nor terminal; only a future adjudication transaction may choose
	// to dispatch or suppress it.
	EffectStatusReviewHold EffectStatus = "review_hold"
)

func (status EffectStatus) Valid() bool {
	switch status {
	case EffectStatusPending, EffectStatusDelivering, EffectStatusDelivered, EffectStatusDeadLetter, EffectStatusReviewHold:
		return true
	default:
		return false
	}
}

func (status EffectStatus) Terminal() bool {
	return status == EffectStatusDelivered || status == EffectStatusDeadLetter
}

// EffectDispatchFence identifies one leased delivery attempt. It is
// deliberately independent of the Turn fence: an Effect outlives the epoch
// that produced it, and a Turn that was reclaimed, retired or superseded must
// not invalidate an external side effect already committed to be delivered.
type EffectDispatchFence struct {
	OutboxID      string
	DispatchToken int64
	LeaseOwnerID  string
}

func (fence EffectDispatchFence) Validate() error {
	if err := validatePrintableASCII("outboxId", fence.OutboxID, MaxEffectOutboxIDBytes); err != nil {
		return err
	}
	if fence.DispatchToken < 1 {
		return fmt.Errorf("dispatchToken must be at least 1")
	}
	return validatePrintableASCII("leaseOwnerId", fence.LeaseOwnerID, MaxWorkerIDBytes)
}

// EffectDelivery is one leased unit of external work.
//
// DedupeKey is the provider idempotency key. It is stable for the life of the
// row and must be reused verbatim on every retry: a redelivery that mints a
// new key converts an ambiguous outcome into a duplicate side effect.
type EffectDelivery struct {
	OutboxID         string
	TurnID           agentv1.TurnID
	AttemptID        string
	TurnFencingToken agentv1.Sequence
	OperationID      string
	Ordinal          int
	Topic            string
	DedupeKey        string
	Payload          json.RawMessage
	DeliveryAttempts int64
	Fence            EffectDispatchFence
	LeaseExpiresAt   time.Time
}

type DeliveryOutcome string

const (
	// DeliveryOutcomeDelivered means the provider accepted the effect.
	DeliveryOutcomeDelivered DeliveryOutcome = "delivered"
	// DeliveryOutcomeRetry means the attempt failed in a way that may succeed
	// later and is known not to have taken effect.
	DeliveryOutcomeRetry DeliveryOutcome = "retry"
	// DeliveryOutcomePermanent means no retry can succeed.
	DeliveryOutcomePermanent DeliveryOutcome = "permanent"
	// DeliveryOutcomeUnknown means the provider may or may not have applied
	// the effect: a timeout, a dropped connection, an ambiguous 5xx. This is
	// the outcome that makes at-least-once delivery dangerous, so it is never
	// resolved by guessing.
	DeliveryOutcomeUnknown DeliveryOutcome = "unknown"
)

func (outcome DeliveryOutcome) Valid() bool {
	switch outcome {
	case DeliveryOutcomeDelivered, DeliveryOutcomeRetry, DeliveryOutcomePermanent, DeliveryOutcomeUnknown:
		return true
	default:
		return false
	}
}

// DeliveryReport is what a Deliverer tells the dispatcher.
type DeliveryReport struct {
	Outcome DeliveryOutcome
	// ErrorCode is a bounded, non-PII classification kept for operators. It
	// must never carry provider payloads, credentials or user content.
	ErrorCode string
	// RetryAfter is an optional provider backoff hint. The dispatcher uses the
	// larger of this and its own exponential schedule.
	RetryAfter time.Duration
	// IdempotentRetrySafe must be set for an unknown outcome to be retried.
	// Without it the effect is dead-lettered for explicit resolution rather
	// than re-sent, because re-sending a non-idempotent effect after an
	// ambiguous failure is how duplicates reach real users.
	IdempotentRetrySafe bool
}

func (report DeliveryReport) Validate() error {
	if !report.Outcome.Valid() {
		return fmt.Errorf("unknown delivery outcome %q", report.Outcome)
	}
	if report.ErrorCode != "" {
		if err := validatePrintableASCII("errorCode", report.ErrorCode, MaxEffectErrorCodeBytes); err != nil {
			return err
		}
	}
	if report.Outcome == DeliveryOutcomeDelivered && report.ErrorCode != "" {
		return fmt.Errorf("a delivered effect cannot carry an error code")
	}
	if report.RetryAfter < 0 || report.RetryAfter > MaxEffectRetryBackoff {
		return fmt.Errorf("retryAfter must be between 0 and %s", MaxEffectRetryBackoff)
	}
	return nil
}

// Deliverer performs the external side effect. It is the only part of
// dispatch the kernel does not own; leasing, fencing, retry scheduling,
// attempt budgets and dead-lettering stay here.
//
// A Deliverer must treat DedupeKey as the provider idempotency key and must
// not mint a new one on retry.
type Deliverer interface {
	Deliver(ctx context.Context, delivery EffectDelivery) (DeliveryReport, error)
}

type ClaimEffectsCommand struct {
	LeaseOwnerID string
	// Limit bounds one claim batch. Zero selects DefaultEffectClaimBatch.
	Limit int
	// Topics optionally restricts the claim to specific topics so a
	// dispatcher fleet can be partitioned by provider.
	Topics []string
}

func (command ClaimEffectsCommand) Validate() error {
	if err := validatePrintableASCII("leaseOwnerId", command.LeaseOwnerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	if command.Limit < 0 || command.Limit > MaxEffectClaimBatch {
		return fmt.Errorf("limit must be between 0 and %d", MaxEffectClaimBatch)
	}
	if len(command.Topics) > MaxEffectClaimBatch {
		return fmt.Errorf("topics exceed %d entries", MaxEffectClaimBatch)
	}
	for index, topic := range command.Topics {
		if err := validatePrintableASCII(fmt.Sprintf("topics[%d]", index), topic, MaxEffectTopicBytes); err != nil {
			return err
		}
	}
	return nil
}

func (command ClaimEffectsCommand) limit() int {
	if command.Limit <= 0 {
		return DefaultEffectClaimBatch
	}
	return command.Limit
}

type CompleteEffectCommand struct {
	Fence  EffectDispatchFence
	Report DeliveryReport
}

func (command CompleteEffectCommand) Validate() error {
	if err := command.Fence.Validate(); err != nil {
		return err
	}
	return command.Report.Validate()
}

type CompleteEffectResult struct {
	OutboxID         string
	Status           EffectStatus
	DeliveryAttempts int64
	AvailableAt      time.Time
	LastErrorCode    string
	// Replay is true when the effect had already reached this terminal state,
	// so a dispatcher recovering from an unknown commit outcome can tell a
	// fresh transition from a repeat.
	Replay bool
}

// EffectOutboxStore is the dispatcher's persistence boundary. It performs no
// delivery itself and knows nothing about any provider.
type EffectOutboxStore interface {
	ClaimEffects(context.Context, ClaimEffectsCommand) ([]EffectDelivery, error)
	CompleteEffect(context.Context, CompleteEffectCommand) (CompleteEffectResult, error)
}

var _ EffectOutboxStore = (*SQLStore)(nil)

// ClaimEffects leases a bounded batch of deliverable effects.
//
// Two classes are claimable: `pending` rows whose backoff has elapsed, and
// `delivering` rows whose lease lapsed because their dispatcher died. Each row
// is claimed in its own short transaction, so one contended row never blocks
// the rest of the batch, and the monotonic dispatch fence makes a resurrected
// dispatcher's late completion detectable.
func (store *SQLStore) ClaimEffects(ctx context.Context, command ClaimEffectsCommand) ([]EffectDelivery, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := command.Validate(); err != nil {
		return nil, err
	}
	candidates, err := store.scanClaimableEffects(ctx, command)
	if err != nil {
		return nil, store.normalize("claim-effects", err)
	}

	deliveries := make([]EffectDelivery, 0, len(candidates))
	for _, outboxID := range candidates {
		if err := contextError(ctx); err != nil {
			return deliveries, err
		}
		delivery, claimed, err := store.claimEffect(ctx, outboxID, command.LeaseOwnerID, command.Topics)
		if err != nil {
			return deliveries, store.normalize("claim-effects", err)
		}
		if claimed {
			deliveries = append(deliveries, delivery)
		}
	}
	if len(deliveries) == 0 {
		return nil, ErrNoClaimableEffects
	}
	return deliveries, nil
}

func (store *SQLStore) scanClaimableEffects(ctx context.Context, command ClaimEffectsCommand) ([]string, error) {
	now, err := store.executionNow(ctx, store.db)
	if err != nil {
		return nil, err
	}
	statement := fmt.Sprintf(`
		SELECT outbox_id
		FROM %s
		WHERE (
		        (status = ? AND available_at <= ?)
		     OR (status = ? AND lease_expires_at <= ?)
		      )
		  %s
		ORDER BY available_at ASC, id ASC
		LIMIT ?`, SQLEffectOutboxTable, topicPredicate(command.Topics))

	args := []any{
		string(EffectStatusPending), now,
		string(EffectStatusDelivering), now,
	}
	if len(command.Topics) > 0 {
		for _, topic := range command.Topics {
			args = append(args, topic)
		}
	}
	args = append(args, command.limit())

	var rows []struct {
		OutboxID string `gorm:"column:outbox_id"`
	}
	if err := store.db.WithContext(ctx).Raw(statement, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if err := validatePrintableASCII("outboxId", row.OutboxID, MaxEffectOutboxIDBytes); err != nil {
			return nil, ErrStoreIntegrity
		}
		ids = append(ids, row.OutboxID)
	}
	return ids, nil
}

func topicPredicate(topics []string) string {
	if len(topics) == 0 {
		return ""
	}
	placeholders := make([]byte, 0, len(topics)*2)
	for index := range topics {
		if index > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
	}
	return "AND topic IN (" + string(placeholders) + ")"
}

func (store *SQLStore) claimEffect(
	ctx context.Context,
	outboxID, leaseOwnerID string,
	topics []string,
) (EffectDelivery, bool, error) {
	// Resolve immutable ownership before the transaction, then lock Turn before
	// Effect. Settlement Review opening uses the same order, so either this
	// claim linearizes first or review_hold becomes visible before a lease can
	// be granted.
	var identity struct {
		TurnID string `gorm:"column:turn_id"`
	}
	if err := store.db.WithContext(ctx).Table(SQLEffectOutboxTable).
		Select("turn_id").Where("outbox_id = ?", outboxID).Take(&identity).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return EffectDelivery{}, false, nil
		}
		return EffectDelivery{}, false, err
	}
	if err := validatePathSegment("turnId", identity.TurnID, MaxTurnIDBytes); err != nil {
		return EffectDelivery{}, false, ErrStoreIntegrity
	}
	var delivery EffectDelivery
	claimed := false
	err := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		if _, err := store.lockTurn(tx, "turn_id = ?", identity.TurnID); err != nil {
			return err
		}
		// review_hold is the rolling-deployment fence understood by old
		// dispatchers. New dispatchers additionally verify the orthogonal Review
		// ledger under the same Turn lock, so an out-of-band row repair cannot
		// silently turn held work back into a claimable Effect.
		_, reviewFound, err := store.lookupSettlementReviewTx(tx, agentv1.TurnID(identity.TurnID), false)
		if err != nil {
			return err
		}
		row, err := store.lockEffect(tx, outboxID)
		if err != nil {
			if errors.Is(err, ErrEffectNotFound) {
				return nil
			}
			return err
		}
		if row.TurnID != identity.TurnID {
			return ErrStoreIntegrity
		}
		// The unlocked scan is only discovery. Topic authority is rechecked on
		// the locked row before leasing it, so a repair or anomalous write between
		// scan and claim cannot move work into this dispatcher's partition.
		if !effectTopicScopeAllows(topics, row.Topic) {
			return ErrEffectScopeMismatch
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		status := EffectStatus(row.Status)
		if !status.Valid() {
			return ErrStoreIntegrity
		}
		if reviewFound {
			switch status {
			case EffectStatusReviewHold, EffectStatusDelivered, EffectStatusDeadLetter:
				return nil
			default:
				return ErrStoreIntegrity
			}
		}
		switch {
		case status == EffectStatusPending && !row.AvailableAt.After(now):
		case status == EffectStatusDelivering && row.LeaseExpiresAt != nil && !row.LeaseExpiresAt.After(now):
		default:
			// Another dispatcher won it, or its backoff has not elapsed.
			return nil
		}

		leaseExpiresAt := now.Add(store.effectLeaseTTL())
		next := map[string]any{
			"status":                 string(EffectStatusDelivering),
			"lease_owner_id":         leaseOwnerID,
			"lease_expires_at":       leaseExpiresAt,
			"delivery_attempts":      row.DeliveryAttempts + 1,
			"dispatch_fencing_token": row.DispatchFencingToken + 1,
			"updated_at":             now,
		}
		if err := store.updateEffectColumns(tx, row.ID, next); err != nil {
			return err
		}
		delivery = EffectDelivery{
			OutboxID:         row.OutboxID,
			TurnID:           agentv1.TurnID(row.TurnID),
			AttemptID:        row.AttemptID,
			TurnFencingToken: agentv1.Sequence(row.TurnFencingToken),
			OperationID:      row.OperationID,
			Ordinal:          row.Ordinal,
			Topic:            row.Topic,
			DedupeKey:        row.DedupeKey,
			Payload:          append(json.RawMessage(nil), row.PayloadJSON...),
			DeliveryAttempts: row.DeliveryAttempts + 1,
			Fence: EffectDispatchFence{
				OutboxID:      row.OutboxID,
				DispatchToken: row.DispatchFencingToken + 1,
				LeaseOwnerID:  leaseOwnerID,
			},
			LeaseExpiresAt: leaseExpiresAt,
		}
		claimed = true
		return nil
	})
	if err != nil {
		return EffectDelivery{}, false, err
	}
	return delivery, claimed, nil
}

func effectTopicScopeAllows(topics []string, topic string) bool {
	if len(topics) == 0 {
		return true
	}
	for _, allowed := range topics {
		if allowed == topic {
			return true
		}
	}
	return false
}

// CompleteEffect resolves one leased delivery.
//
// The fence must match exactly. A dispatcher whose lease lapsed and was taken
// over reports against a stale token and is rejected, so a slow dispatcher
// cannot mark delivered an effect that a successor is still working on.
//
// An unknown outcome is never resolved by guessing. It is retried only when
// the Deliverer declared the retry idempotent; otherwise the effect is
// dead-lettered for explicit resolution.
func (store *SQLStore) CompleteEffect(ctx context.Context, command CompleteEffectCommand) (CompleteEffectResult, error) {
	if err := contextError(ctx); err != nil {
		return CompleteEffectResult{}, err
	}
	if err := command.Validate(); err != nil {
		return CompleteEffectResult{}, err
	}

	var result CompleteEffectResult
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		row, err := store.lockEffect(tx, command.Fence.OutboxID)
		if err != nil {
			return err
		}
		status := EffectStatus(row.Status)
		if !status.Valid() {
			return ErrStoreIntegrity
		}
		if row.DispatchFencingToken != command.Fence.DispatchToken {
			if status.Terminal() && row.DispatchFencingToken > command.Fence.DispatchToken {
				return ErrEffectFenced
			}
			return ErrEffectFenced
		}
		if status.Terminal() {
			// Same fence, already terminal: this is a repeat of a completion
			// whose result the caller never observed.
			result = effectResultFromRow(row, true)
			return nil
		}
		if status != EffectStatusDelivering ||
			row.LeaseOwnerID == nil || *row.LeaseOwnerID != command.Fence.LeaseOwnerID {
			return ErrEffectFenced
		}
		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}

		next, err := store.resolveEffectCompletion(row, command.Report, now)
		if err != nil {
			return err
		}
		if err := store.updateEffectColumns(tx, row.ID, next.columns); err != nil {
			return err
		}
		result = next.result
		return nil
	})
	if txErr != nil {
		return CompleteEffectResult{}, store.normalize("complete-effect", txErr)
	}
	return result, nil
}

type effectCompletion struct {
	columns map[string]any
	result  CompleteEffectResult
}

func (store *SQLStore) resolveEffectCompletion(row sqlEffectOutboxRow, report DeliveryReport, now time.Time) (effectCompletion, error) {
	budget := store.maxDeliveryAttemptsBudget()
	errorCode := any(nil)
	if report.ErrorCode != "" {
		errorCode = report.ErrorCode
	}
	base := map[string]any{
		"lease_owner_id":   nil,
		"lease_expires_at": nil,
		"last_error_code":  errorCode,
		"updated_at":       now,
	}
	completion := effectCompletion{columns: base}
	completion.result = CompleteEffectResult{
		OutboxID:         row.OutboxID,
		DeliveryAttempts: row.DeliveryAttempts,
		LastErrorCode:    report.ErrorCode,
	}

	retry := func() {
		delay := effectRetryDelay(row.DeliveryAttempts, report.RetryAfter)
		availableAt := now.Add(delay)
		base["status"] = string(EffectStatusPending)
		base["available_at"] = availableAt
		completion.result.Status = EffectStatusPending
		completion.result.AvailableAt = availableAt
	}
	dead := func() {
		base["status"] = string(EffectStatusDeadLetter)
		base["dead_lettered_at"] = now
		completion.result.Status = EffectStatusDeadLetter
	}

	switch report.Outcome {
	case DeliveryOutcomeDelivered:
		base["status"] = string(EffectStatusDelivered)
		base["delivered_at"] = now
		base["last_error_code"] = nil
		completion.result.Status = EffectStatusDelivered
		completion.result.LastErrorCode = ""
	case DeliveryOutcomePermanent:
		dead()
	case DeliveryOutcomeRetry:
		if row.DeliveryAttempts >= budget {
			dead()
		} else {
			retry()
		}
	case DeliveryOutcomeUnknown:
		// Re-sending after an ambiguous failure is safe only when the provider
		// deduplicates on the key this row already carries.
		if !report.IdempotentRetrySafe || row.DeliveryAttempts >= budget {
			dead()
		} else {
			retry()
		}
	default:
		return effectCompletion{}, fmt.Errorf("unknown delivery outcome %q", report.Outcome)
	}
	return completion, nil
}

// effectRetryDelay grows exponentially with the attempt count and is capped.
// A provider hint may extend the delay but never shorten it: a provider asking
// for a longer pause is honoured, one asking for a shorter one cannot defeat
// the dispatcher's own backoff.
func effectRetryDelay(attempts int64, hint time.Duration) time.Duration {
	delay := DefaultEffectRetryBackoff
	for index := int64(1); index < attempts && delay < MaxEffectRetryBackoff; index++ {
		delay *= 2
	}
	if delay > MaxEffectRetryBackoff {
		delay = MaxEffectRetryBackoff
	}
	if hint > delay {
		delay = hint
	}
	if delay > MaxEffectRetryBackoff {
		delay = MaxEffectRetryBackoff
	}
	return delay
}

func (store *SQLStore) effectLeaseTTL() time.Duration {
	if store.effectLease <= 0 || store.effectLease > MaxEffectLeaseTTL {
		return DefaultEffectLeaseTTL
	}
	return store.effectLease
}

func (store *SQLStore) maxDeliveryAttemptsBudget() int64 {
	if store.maxDeliveryAttempts <= 0 || store.maxDeliveryAttempts > MaxDeliveryAttemptsLimit {
		return DefaultMaxDeliveryAttempts
	}
	return store.maxDeliveryAttempts
}

func effectResultFromRow(row sqlEffectOutboxRow, replay bool) CompleteEffectResult {
	result := CompleteEffectResult{
		OutboxID:         row.OutboxID,
		Status:           EffectStatus(row.Status),
		DeliveryAttempts: row.DeliveryAttempts,
		AvailableAt:      row.AvailableAt.UTC(),
		Replay:           replay,
	}
	if row.LastErrorCode != nil {
		result.LastErrorCode = *row.LastErrorCode
	}
	return result
}

func (store *SQLStore) lockEffect(tx *gorm.DB, outboxID string) (sqlEffectOutboxRow, error) {
	var row sqlEffectOutboxRow
	if store.dialect == "sqlite" {
		result := tx.Table(SQLEffectOutboxTable).Where("outbox_id = ?", outboxID).
			UpdateColumn("dispatch_fencing_token", gorm.Expr("dispatch_fencing_token"))
		if result.Error != nil {
			return row, result.Error
		}
		if result.RowsAffected != 1 {
			return row, ErrEffectNotFound
		}
		if err := tx.Where("outbox_id = ?", outboxID).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return row, ErrEffectNotFound
			}
			return row, err
		}
		return row, nil
	}
	if err := tx.Clauses(lockForUpdate()).Where("outbox_id = ?", outboxID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, ErrEffectNotFound
		}
		return row, err
	}
	return row, nil
}

func (store *SQLStore) updateEffectColumns(tx *gorm.DB, id uint64, columns map[string]any) error {
	result := tx.Table(SQLEffectOutboxTable).Where("id = ?", id).UpdateColumns(columns)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrStoreIntegrity
	}
	return nil
}

func lockForUpdate() clause.Locking { return clause.Locking{Strength: "UPDATE"} }
