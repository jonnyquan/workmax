package commerce

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"server/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	MaxProviderPayloadBytes = 64 * 1024
	MaxOutboxPayloadBytes   = 64 * 1024

	// Preparation may require bounded provider reads. Two minutes keeps those
	// reads outside the database transaction without letting the reconciler
	// reclaim an ordinary in-flight Stripe request too aggressively.
	DefaultProviderEventLeaseTTL                 = 2 * time.Minute
	MaxProviderEventLeaseTTL                     = 10 * time.Minute
	DefaultProviderEventPrepareTimeout           = 90 * time.Second
	DefaultProviderEventCompleteTimeout          = 10 * time.Second
	DefaultProviderEventFailurePersistTimeout    = 10 * time.Second
	maxProviderEventLeaseSafetyReserve           = 10 * time.Second
	DefaultProviderEventMaxAttempts              = 5
	MaxProviderEventAttempts                     = 64
	DefaultProviderEventRetryBackoff             = time.Second
	MaxProviderEventRetryBackoff                 = 15 * time.Minute
	DefaultProviderEventBatch                    = 32
	MaxProviderEventBatch                        = 256
	providerEventAttemptBudgetExhaustedErrorCode = "attempt_budget_exhausted"
)

var (
	ErrProviderEventBusy           = errors.New("commerce provider event is already being processed")
	ErrProviderEventNotReady       = errors.New("commerce provider event retry is not ready")
	ErrProviderEventRetryScheduled = errors.New("commerce provider event retry was scheduled")
	ErrProviderEventManualReview   = errors.New("commerce provider event requires manual review")
	ErrProviderEventFenced         = errors.New("commerce provider event processing lease was superseded")
	ErrProviderEventConflict       = errors.New("commerce provider event identity conflicts with durable inbox")
	ErrCommerceStoreIntegrity      = errors.New("commerce event store integrity violation")
)

// ProviderEventInput is created only after the provider signature has been
// verified. VerificationKeyDigest identifies the accepted signing-key epoch
// without persisting the secret or signature header.
type ProviderEventInput struct {
	Provider              string
	ProviderAccountID     string
	ProviderAPIVersion    string
	EventID               string
	EventType             string
	ObjectID              string
	LiveMode              bool
	ProviderCreatedAt     *time.Time
	VerificationKeyDigest string
	Payload               []byte
}

type ProviderEventSnapshot struct {
	ID                    uint
	Provider              string
	ProviderAccountID     string
	ProviderAPIVersion    string
	EventID               string
	EventType             string
	ObjectID              string
	LiveMode              bool
	ProviderCreatedAt     *time.Time
	VerificationKeyDigest string
	PayloadDigest         string
	Payload               []byte
	AttemptCount          int
	ProcessingVersion     int64
}

type OutboxDraft struct {
	Topic     string
	DedupeKey string
	Payload   json.RawMessage
}

// EventOutcome is applied atomically with the business mutation. AfterCommit
// is deliberately outside the durable digest; it is only a compatibility hook
// for existing best-effort notifications. Durable consumers use Outbox.
type EventOutcome struct {
	Status      string
	Kind        string
	Outbox      []OutboxDraft
	AfterCommit func()
}

type PreparedEvent struct {
	Apply func(context.Context, *gorm.DB, time.Time) (EventOutcome, error)
}

type ProviderEventProcessor interface {
	Prepare(context.Context, ProviderEventSnapshot) (PreparedEvent, error)
}

// ProcessingError carries only a closed, non-PII code into durable state.
// Err remains available to logs/callers but is never copied into a row.
type ProcessingError struct {
	Code      string
	Retryable bool
	Err       error
}

func (err *ProcessingError) Error() string {
	if err == nil || err.Err == nil {
		return "commerce provider event processing failed"
	}
	return err.Err.Error()
}

func (err *ProcessingError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func RetryableError(code string, err error) error {
	return &ProcessingError{Code: code, Retryable: true, Err: err}
}

func ManualReviewError(code string, err error) error {
	return &ProcessingError{Code: code, Retryable: false, Err: err}
}

type ProviderEventServiceOptions struct {
	WorkerID              string
	LeaseTTL              time.Duration
	PrepareTimeout        time.Duration
	CompleteTimeout       time.Duration
	FailurePersistTimeout time.Duration
	MaxAttempts           int
	BaseBackoff           time.Duration
	MaxBackoff            time.Duration
}

func (options *ProviderEventServiceOptions) applyDefaults() {
	if options.WorkerID == "" {
		options.WorkerID = "commerce-event-worker"
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = DefaultProviderEventLeaseTTL
	}
	usableLease := options.LeaseTTL - providerEventLeaseSafetyReserve(options.LeaseTTL)
	if options.PrepareTimeout <= 0 {
		options.PrepareTimeout = DefaultProviderEventPrepareTimeout
		if options.PrepareTimeout >= usableLease {
			options.PrepareTimeout = usableLease * 3 / 4
		}
	}
	if options.CompleteTimeout <= 0 {
		options.CompleteTimeout = DefaultProviderEventCompleteTimeout
		remaining := usableLease - options.PrepareTimeout
		if options.CompleteTimeout >= remaining {
			options.CompleteTimeout = remaining / 3
		}
	}
	if options.FailurePersistTimeout <= 0 {
		options.FailurePersistTimeout = DefaultProviderEventFailurePersistTimeout
		remaining := usableLease - options.PrepareTimeout - options.CompleteTimeout
		if options.FailurePersistTimeout >= remaining {
			options.FailurePersistTimeout = remaining / 2
		}
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = DefaultProviderEventMaxAttempts
	}
	if options.BaseBackoff <= 0 {
		options.BaseBackoff = DefaultProviderEventRetryBackoff
	}
	if options.MaxBackoff <= 0 {
		options.MaxBackoff = MaxProviderEventRetryBackoff
	}
}

func (options ProviderEventServiceOptions) validate() error {
	if err := validateASCII("workerID", options.WorkerID, 128); err != nil {
		return err
	}
	if options.LeaseTTL <= 0 || options.LeaseTTL > MaxProviderEventLeaseTTL {
		return fmt.Errorf("leaseTTL must be between 1ns and %s", MaxProviderEventLeaseTTL)
	}
	if options.PrepareTimeout <= 0 || options.PrepareTimeout >= options.LeaseTTL {
		return fmt.Errorf("prepareTimeout must be positive and strictly shorter than leaseTTL")
	}
	remainingLease := options.LeaseTTL - options.PrepareTimeout
	if options.CompleteTimeout <= 0 || options.CompleteTimeout >= remainingLease {
		return fmt.Errorf("completeTimeout must be positive and leave lease time after prepareTimeout")
	}
	remainingLease -= options.CompleteTimeout
	leaseReserve := providerEventLeaseSafetyReserve(options.LeaseTTL)
	if options.FailurePersistTimeout <= 0 || options.FailurePersistTimeout > remainingLease-leaseReserve {
		return fmt.Errorf(
			"failurePersistTimeout must be positive and leave a lease safety reserve of at least %s",
			leaseReserve,
		)
	}
	if options.MaxAttempts < 1 || options.MaxAttempts > MaxProviderEventAttempts {
		return fmt.Errorf("maxAttempts must be between 1 and %d", MaxProviderEventAttempts)
	}
	if options.BaseBackoff <= 0 || options.BaseBackoff > MaxProviderEventRetryBackoff {
		return fmt.Errorf("baseBackoff must be between 1ns and %s", MaxProviderEventRetryBackoff)
	}
	if options.MaxBackoff < options.BaseBackoff || options.MaxBackoff > MaxProviderEventRetryBackoff {
		return fmt.Errorf("maxBackoff must be between baseBackoff and %s", MaxProviderEventRetryBackoff)
	}
	return nil
}

func providerEventLeaseSafetyReserve(leaseTTL time.Duration) time.Duration {
	reserve := leaseTTL / 20
	if reserve > maxProviderEventLeaseSafetyReserve {
		return maxProviderEventLeaseSafetyReserve
	}
	if reserve <= 0 {
		return time.Nanosecond
	}
	return reserve
}

type ProviderEventService struct {
	db      *gorm.DB
	options ProviderEventServiceOptions
}

func NewProviderEventService(db *gorm.DB, options ProviderEventServiceOptions) (*ProviderEventService, error) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return nil, fmt.Errorf("commerce provider event service requires a database")
	}
	options.applyDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}
	if dialect := db.Dialector.Name(); dialect != "mysql" && dialect != "sqlite" {
		return nil, fmt.Errorf("unsupported commerce event database dialect %q", dialect)
	}
	return &ProviderEventService{db: db, options: options}, nil
}

type IngestResult struct {
	Event  model.CommerceProviderEvent
	Replay bool
}

func (service *ProviderEventService) Ingest(ctx context.Context, input ProviderEventInput) (IngestResult, error) {
	if err := contextError(ctx); err != nil {
		return IngestResult{}, err
	}
	if err := validateProviderEventInput(input); err != nil {
		return IngestResult{}, err
	}
	now, err := databaseCommerceNow(ctx, service.db)
	if err != nil {
		return IngestResult{}, err
	}
	payload := append([]byte(nil), input.Payload...)
	digest := sha256Hex(payload)
	providerCreatedAt := canonicalProviderInstant(input.ProviderCreatedAt)
	event := model.CommerceProviderEvent{
		Provider: input.Provider, ProviderAccountID: input.ProviderAccountID,
		ProviderAPIVersion: input.ProviderAPIVersion,
		EventID:            input.EventID, EventType: input.EventType, ObjectID: input.ObjectID,
		LiveMode: input.LiveMode, ProviderCreatedAt: providerCreatedAt,
		VerificationKeyDigest: input.VerificationKeyDigest,
		PayloadDigest:         digest, PayloadJSON: payload,
		Status: model.CommerceProviderEventStatusReceived,
	}
	event.CreatedAt = now
	event.UpdatedAt = now
	if err := service.db.WithContext(ctx).Create(&event).Error; err == nil {
		return IngestResult{Event: event}, nil
	} else {
		var existing model.CommerceProviderEvent
		readErr := service.db.WithContext(ctx).Where(
			"provider = ? AND provider_account_id = ? AND live_mode = ? AND event_id = ?",
			input.Provider, input.ProviderAccountID, input.LiveMode, input.EventID,
		).First(&existing).Error
		if readErr != nil {
			return IngestResult{}, fmt.Errorf("insert commerce provider event: %w", err)
		}
		if existing.ProviderAPIVersion != input.ProviderAPIVersion ||
			existing.EventType != input.EventType || existing.ObjectID != input.ObjectID ||
			existing.LiveMode != input.LiveMode || !sameOptionalInstant(existing.ProviderCreatedAt, providerCreatedAt) ||
			existing.PayloadDigest != digest ||
			!bytes.Equal(existing.PayloadJSON, payload) {
			return IngestResult{}, ErrProviderEventConflict
		}
		if err := validateStoredEvent(existing); err != nil {
			return IngestResult{}, err
		}
		return IngestResult{Event: existing, Replay: true}, nil
	}
}

type ProcessResult struct {
	EventID      string
	Status       string
	OutcomeKind  string
	Replay       bool
	AttemptCount int
}

func (service *ProviderEventService) IngestAndProcess(
	ctx context.Context,
	input ProviderEventInput,
	processor ProviderEventProcessor,
) (ProcessResult, error) {
	ingested, err := service.Ingest(ctx, input)
	if err != nil {
		return ProcessResult{}, err
	}
	result, err := service.ProcessEvent(ctx, ingested.Event.Id, processor)
	if ingested.Replay && err == nil {
		result.Replay = true
	}
	return result, err
}

type eventClaim struct {
	snapshot ProviderEventSnapshot
	ownerID  string
	fence    int64
}

func (service *ProviderEventService) ProcessEvent(
	ctx context.Context,
	eventRowID uint,
	processor ProviderEventProcessor,
) (ProcessResult, error) {
	if err := contextError(ctx); err != nil {
		return ProcessResult{}, err
	}
	if processor == nil {
		return ProcessResult{}, fmt.Errorf("commerce provider event processor is required")
	}
	claim, terminal, err := service.claimEvent(ctx, eventRowID)
	if err != nil {
		return terminal, err
	}
	if terminal.Status != "" {
		terminal.Replay = true
		return terminal, nil
	}

	prepareContext, cancelPrepare := context.WithTimeout(ctx, service.options.PrepareTimeout)
	prepared, err := prepareSafely(prepareContext, processor, claim.snapshot)
	if err == nil {
		err = prepareContext.Err()
	}
	cancelPrepare()
	if err != nil {
		return service.failClaimPersistently(ctx, claim, err)
	}
	if prepared.Apply == nil {
		return service.failClaimPersistently(ctx, claim, ManualReviewError(
			"invalid_processor", errors.New("processor returned no apply function"),
		))
	}

	completeContext, cancelComplete := context.WithTimeout(ctx, service.options.CompleteTimeout)
	result, outcome, err := service.completeClaim(completeContext, claim, prepared)
	cancelComplete()
	if err != nil {
		failureContext, cancelFailure := service.failurePersistenceContext(ctx)
		defer cancelFailure()
		// A commit result may be ambiguous. Read the durable owner before changing
		// the claim: if the transaction committed, terminal state is authority.
		if recovered, ok := service.readTerminalResult(failureContext, eventRowID); ok {
			recovered.Replay = true
			return recovered, nil
		}
		return service.failClaim(failureContext, claim, err)
	}
	invokeAfterCommit(outcome.AfterCommit)
	return result, nil
}

func (service *ProviderEventService) claimEvent(
	ctx context.Context,
	eventRowID uint,
) (eventClaim, ProcessResult, error) {
	if eventRowID == 0 {
		return eventClaim{}, ProcessResult{}, ErrCommerceStoreIntegrity
	}
	var claim eventClaim
	var terminal ProcessResult
	budgetExhausted := false
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var event model.CommerceProviderEvent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, "id = ?", eventRowID).Error; err != nil {
			return err
		}
		if err := validateStoredEvent(event); err != nil {
			return err
		}
		if event.IsTerminal() {
			terminal = resultFromEvent(event)
			return nil
		}
		if event.Status == model.CommerceProviderEventStatusManualReview {
			terminal = resultFromEvent(event)
			return ErrProviderEventManualReview
		}
		now, err := databaseCommerceNow(ctx, tx)
		if err != nil {
			return err
		}
		switch event.Status {
		case model.CommerceProviderEventStatusReceived:
		case model.CommerceProviderEventStatusRetryWait:
			if event.NextAttemptAt == nil || event.NextAttemptAt.After(now) {
				return ErrProviderEventNotReady
			}
		case model.CommerceProviderEventStatusProcessing:
			if event.LeaseExpiresAt == nil {
				return ErrCommerceStoreIntegrity
			}
			if event.LeaseExpiresAt.After(now) {
				return ErrProviderEventBusy
			}
		default:
			return ErrCommerceStoreIntegrity
		}
		if event.AttemptCount >= service.options.MaxAttempts {
			updates := map[string]any{
				"status":           model.CommerceProviderEventStatusManualReview,
				"lease_owner_id":   "",
				"lease_expires_at": nil,
				"next_attempt_at":  nil,
				"last_error_code":  providerEventAttemptBudgetExhaustedErrorCode,
				"updated_at":       now,
			}
			updated := tx.Model(&model.CommerceProviderEvent{}).
				Where("id = ? AND status = ? AND processing_version = ?",
					event.Id, event.Status, event.ProcessingVersion).
				Updates(updates)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrProviderEventFenced
			}
			terminal = ProcessResult{
				EventID: event.EventID, Status: model.CommerceProviderEventStatusManualReview,
				AttemptCount: event.AttemptCount,
			}
			budgetExhausted = true
			return nil
		}

		leaseExpiresAt := now.Add(service.options.LeaseTTL)
		fence := event.ProcessingVersion + 1
		attempt := event.AttemptCount + 1
		updates := map[string]any{
			"status":             model.CommerceProviderEventStatusProcessing,
			"attempt_count":      attempt,
			"processing_version": fence,
			"lease_owner_id":     service.options.WorkerID,
			"lease_expires_at":   leaseExpiresAt,
			"next_attempt_at":    nil,
			"last_error_code":    "",
			"updated_at":         now,
		}
		result := tx.Model(&model.CommerceProviderEvent{}).
			Where("id = ? AND processing_version = ?", event.Id, event.ProcessingVersion).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrProviderEventFenced
		}
		claim = eventClaim{
			ownerID: service.options.WorkerID,
			fence:   fence,
			snapshot: ProviderEventSnapshot{
				ID: event.Id, Provider: event.Provider, ProviderAccountID: event.ProviderAccountID,
				ProviderAPIVersion: event.ProviderAPIVersion,
				EventID:            event.EventID, EventType: event.EventType, ObjectID: event.ObjectID,
				LiveMode: event.LiveMode, ProviderCreatedAt: copyTime(event.ProviderCreatedAt),
				VerificationKeyDigest: event.VerificationKeyDigest,
				PayloadDigest:         event.PayloadDigest, Payload: append([]byte(nil), event.PayloadJSON...),
				AttemptCount: attempt, ProcessingVersion: fence,
			},
		}
		return nil
	})
	if err != nil {
		return eventClaim{}, ProcessResult{}, err
	}
	if budgetExhausted {
		return eventClaim{}, terminal, ErrProviderEventManualReview
	}
	return claim, terminal, nil
}

func (service *ProviderEventService) failurePersistenceContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), service.options.FailurePersistTimeout)
}

func (service *ProviderEventService) failClaimPersistently(
	parent context.Context,
	claim eventClaim,
	processingErr error,
) (ProcessResult, error) {
	failureContext, cancelFailure := service.failurePersistenceContext(parent)
	defer cancelFailure()
	return service.failClaim(failureContext, claim, processingErr)
}

func (service *ProviderEventService) completeClaim(
	ctx context.Context,
	claim eventClaim,
	prepared PreparedEvent,
) (ProcessResult, EventOutcome, error) {
	var outcome EventOutcome
	var result ProcessResult
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		event, now, err := service.lockClaim(ctx, tx, claim)
		if err != nil {
			return err
		}
		outcome, err = applySafely(ctx, prepared, tx, now)
		if err != nil {
			return err
		}
		if err := validateOutcome(outcome); err != nil {
			return ManualReviewError("invalid_outcome", err)
		}
		resultDigest := digestOutcome(outcome)
		if err := persistOutboxDrafts(tx, event, outcome.Outbox, now); err != nil {
			return err
		}
		updates := map[string]any{
			"status":           outcome.Status,
			"lease_owner_id":   "",
			"lease_expires_at": nil,
			"next_attempt_at":  nil,
			"processed_at":     now,
			"outcome_kind":     outcome.Kind,
			"result_digest":    resultDigest,
			"last_error_code":  "",
			"updated_at":       now,
		}
		updated := tx.Model(&model.CommerceProviderEvent{}).
			Where("id = ? AND status = ? AND processing_version = ? AND lease_owner_id = ?",
				event.Id, model.CommerceProviderEventStatusProcessing, claim.fence, claim.ownerID).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProviderEventFenced
		}
		result = ProcessResult{
			EventID: event.EventID, Status: outcome.Status, OutcomeKind: outcome.Kind,
			AttemptCount: event.AttemptCount,
		}
		return nil
	})
	return result, outcome, err
}

func (service *ProviderEventService) lockClaim(
	ctx context.Context,
	tx *gorm.DB,
	claim eventClaim,
) (model.CommerceProviderEvent, time.Time, error) {
	var event model.CommerceProviderEvent
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&event, "id = ?", claim.snapshot.ID).Error; err != nil {
		return event, time.Time{}, err
	}
	if err := validateStoredEvent(event); err != nil {
		return event, time.Time{}, err
	}
	if event.Status != model.CommerceProviderEventStatusProcessing ||
		event.ProcessingVersion != claim.fence || event.LeaseOwnerID != claim.ownerID {
		return event, time.Time{}, ErrProviderEventFenced
	}
	now, err := databaseCommerceNow(ctx, tx)
	if err != nil {
		return event, time.Time{}, err
	}
	if event.LeaseExpiresAt == nil || !event.LeaseExpiresAt.After(now) {
		return event, time.Time{}, ErrProviderEventFenced
	}
	return event, now, nil
}

func (service *ProviderEventService) failClaim(
	ctx context.Context,
	claim eventClaim,
	processingErr error,
) (ProcessResult, error) {
	code, retryable := classifyProcessingError(processingErr)
	var event model.CommerceProviderEvent
	err := service.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		locked, now, err := service.lockClaim(ctx, tx, claim)
		if err != nil {
			return err
		}
		event = locked
		status := model.CommerceProviderEventStatusRetryWait
		var nextAttemptAt *time.Time
		if !retryable || event.AttemptCount >= service.options.MaxAttempts {
			status = model.CommerceProviderEventStatusManualReview
		} else {
			next := now.Add(service.retryDelay(event.AttemptCount))
			nextAttemptAt = &next
		}
		updates := map[string]any{
			"status":           status,
			"lease_owner_id":   "",
			"lease_expires_at": nil,
			"next_attempt_at":  nextAttemptAt,
			"last_error_code":  code,
			"updated_at":       now,
		}
		updated := tx.Model(&model.CommerceProviderEvent{}).
			Where("id = ? AND status = ? AND processing_version = ? AND lease_owner_id = ?",
				event.Id, model.CommerceProviderEventStatusProcessing, claim.fence, claim.ownerID).
			Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrProviderEventFenced
		}
		event.Status = status
		return nil
	})
	if err != nil {
		return ProcessResult{}, fmt.Errorf("record commerce provider event failure: %w", err)
	}
	result := ProcessResult{
		EventID: event.EventID, Status: event.Status, AttemptCount: event.AttemptCount,
	}
	if event.Status == model.CommerceProviderEventStatusManualReview {
		return result, fmt.Errorf("%w: %v", ErrProviderEventManualReview, processingErr)
	}
	return result, fmt.Errorf("%w: %v", ErrProviderEventRetryScheduled, processingErr)
}

func (service *ProviderEventService) retryDelay(attempt int) time.Duration {
	delay := service.options.BaseBackoff
	for current := 1; current < attempt && delay < service.options.MaxBackoff; current++ {
		if delay > service.options.MaxBackoff/2 {
			return service.options.MaxBackoff
		}
		delay *= 2
	}
	if delay > service.options.MaxBackoff {
		return service.options.MaxBackoff
	}
	return delay
}

func (service *ProviderEventService) readTerminalResult(ctx context.Context, eventRowID uint) (ProcessResult, bool) {
	var event model.CommerceProviderEvent
	if err := service.db.WithContext(ctx).First(&event, "id = ?", eventRowID).Error; err != nil {
		return ProcessResult{}, false
	}
	if validateStoredEvent(event) != nil || !event.IsTerminal() {
		return ProcessResult{}, false
	}
	return resultFromEvent(event), true
}

type ReconcileReport struct {
	Scanned       int
	Processed     int
	Ignored       int
	Retried       int
	ManualReviews int
	Busy          int
	Failures      []ReconcileFailure
}

type ReconcileFailure struct {
	EventRowID uint
	Err        error
}

func (service *ProviderEventService) ReconcileOnce(
	ctx context.Context,
	processor ProviderEventProcessor,
	limit int,
) (ReconcileReport, error) {
	if processor == nil {
		return ReconcileReport{}, fmt.Errorf("commerce provider event processor is required")
	}
	if limit <= 0 {
		limit = DefaultProviderEventBatch
	}
	if limit > MaxProviderEventBatch {
		return ReconcileReport{}, fmt.Errorf("reconcile limit exceeds %d", MaxProviderEventBatch)
	}
	now, err := databaseCommerceNow(ctx, service.db)
	if err != nil {
		return ReconcileReport{}, err
	}
	var rows []struct {
		ID uint `gorm:"column:id"`
	}
	err = service.db.WithContext(ctx).Model(&model.CommerceProviderEvent{}).
		Select("id").
		Where(
			"status = ? OR (status = ? AND next_attempt_at <= ?) OR (status = ? AND lease_expires_at <= ?)",
			model.CommerceProviderEventStatusReceived,
			model.CommerceProviderEventStatusRetryWait, now,
			model.CommerceProviderEventStatusProcessing, now,
		).
		Order("id ASC").Limit(limit).Scan(&rows).Error
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Scanned: len(rows)}
	for _, row := range rows {
		if err := contextError(ctx); err != nil {
			return report, err
		}
		result, processErr := service.ProcessEvent(ctx, row.ID, processor)
		switch {
		case processErr == nil && result.Status == model.CommerceProviderEventStatusProcessed:
			report.Processed++
		case processErr == nil && result.Status == model.CommerceProviderEventStatusIgnored:
			report.Ignored++
		case errors.Is(processErr, ErrProviderEventRetryScheduled):
			report.Retried++
		case errors.Is(processErr, ErrProviderEventManualReview):
			report.ManualReviews++
		case errors.Is(processErr, ErrProviderEventBusy), errors.Is(processErr, ErrProviderEventNotReady):
			report.Busy++
		default:
			report.Failures = append(report.Failures, ReconcileFailure{EventRowID: row.ID, Err: processErr})
		}
	}
	return report, nil
}

func persistOutboxDrafts(
	tx *gorm.DB,
	event model.CommerceProviderEvent,
	drafts []OutboxDraft,
	now time.Time,
) error {
	for ordinal, draft := range drafts {
		payload := append([]byte(nil), draft.Payload...)
		row := model.CommerceOutbox{
			ProviderEventID: event.Id, Ordinal: ordinal, Topic: draft.Topic,
			DedupeKey: draft.DedupeKey, PayloadDigest: sha256Hex(payload), PayloadJSON: payload,
			Status: model.CommerceOutboxStatusPending, AvailableAt: now,
		}
		row.CreatedAt = now
		row.UpdatedAt = now
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("create commerce outbox ordinal %d: %w", ordinal, err)
		}
	}
	return nil
}

func validateProviderEventInput(input ProviderEventInput) error {
	if err := validateASCII("provider", input.Provider, 32); err != nil {
		return err
	}
	if err := validateASCII("providerAccountID", input.ProviderAccountID, 255); err != nil {
		return err
	}
	if err := validateOptionalASCII("providerAPIVersion", input.ProviderAPIVersion, 32); err != nil {
		return err
	}
	if err := validateASCII("eventID", input.EventID, 255); err != nil {
		return err
	}
	if err := validateASCII("eventType", input.EventType, 128); err != nil {
		return err
	}
	if err := validateASCII("objectID", input.ObjectID, 255); err != nil {
		return err
	}
	if err := validateProviderInstant(input.ProviderCreatedAt); err != nil {
		return err
	}
	if len(input.VerificationKeyDigest) != 71 || !strings.HasPrefix(input.VerificationKeyDigest, "sha256:") ||
		!isLowerHex(input.VerificationKeyDigest[7:]) {
		return fmt.Errorf("verificationKeyDigest must be canonical sha256")
	}
	if len(input.Payload) == 0 || len(input.Payload) > MaxProviderPayloadBytes ||
		!utf8.Valid(input.Payload) || !json.Valid(input.Payload) {
		return fmt.Errorf("provider payload must be valid UTF-8 JSON within %d bytes", MaxProviderPayloadBytes)
	}
	return nil
}

func validateStoredEvent(event model.CommerceProviderEvent) error {
	if event.Id == 0 || validateASCII("provider", event.Provider, 32) != nil ||
		validateASCII("providerAccountID", event.ProviderAccountID, 255) != nil ||
		validateOptionalASCII("providerAPIVersion", event.ProviderAPIVersion, 32) != nil ||
		validateASCII("eventID", event.EventID, 255) != nil ||
		validateASCII("eventType", event.EventType, 128) != nil ||
		validateASCII("objectID", event.ObjectID, 255) != nil ||
		validateStoredProviderInstant(event.ProviderCreatedAt) != nil ||
		len(event.PayloadDigest) != 64 || !isLowerHex(event.PayloadDigest) ||
		sha256Hex(event.PayloadJSON) != event.PayloadDigest ||
		len(event.PayloadJSON) == 0 || len(event.PayloadJSON) > MaxProviderPayloadBytes ||
		!utf8.Valid(event.PayloadJSON) || !json.Valid(event.PayloadJSON) ||
		len(event.VerificationKeyDigest) != 71 || !strings.HasPrefix(event.VerificationKeyDigest, "sha256:") ||
		!isLowerHex(event.VerificationKeyDigest[7:]) ||
		event.AttemptCount < 0 || event.ProcessingVersion < 0 {
		return ErrCommerceStoreIntegrity
	}
	switch event.Status {
	case model.CommerceProviderEventStatusReceived:
		if event.AttemptCount != 0 || event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil ||
			event.NextAttemptAt != nil || event.ProcessedAt != nil || event.OutcomeKind != "" ||
			event.ResultDigest != "" || event.LastErrorCode != "" {
			return ErrCommerceStoreIntegrity
		}
	case model.CommerceProviderEventStatusProcessing:
		if event.AttemptCount < 1 || event.ProcessingVersion < 1 || event.LeaseOwnerID == "" ||
			event.LeaseExpiresAt == nil || event.NextAttemptAt != nil || event.ProcessedAt != nil ||
			event.OutcomeKind != "" || event.ResultDigest != "" || event.LastErrorCode != "" {
			return ErrCommerceStoreIntegrity
		}
	case model.CommerceProviderEventStatusRetryWait:
		if event.AttemptCount < 1 || event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil ||
			event.NextAttemptAt == nil || event.ProcessedAt != nil || event.OutcomeKind != "" ||
			event.ResultDigest != "" || validateErrorCode(event.LastErrorCode) != nil {
			return ErrCommerceStoreIntegrity
		}
	case model.CommerceProviderEventStatusProcessed, model.CommerceProviderEventStatusIgnored:
		if event.AttemptCount < 1 || event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil ||
			event.NextAttemptAt != nil || event.ProcessedAt == nil ||
			validateASCII("outcomeKind", event.OutcomeKind, 64) != nil ||
			len(event.ResultDigest) != 64 || !isLowerHex(event.ResultDigest) || event.LastErrorCode != "" {
			return ErrCommerceStoreIntegrity
		}
	case model.CommerceProviderEventStatusManualReview:
		if event.AttemptCount < 1 || event.LeaseOwnerID != "" || event.LeaseExpiresAt != nil ||
			event.NextAttemptAt != nil || event.ProcessedAt != nil || event.OutcomeKind != "" ||
			event.ResultDigest != "" || validateErrorCode(event.LastErrorCode) != nil {
			return ErrCommerceStoreIntegrity
		}
	default:
		return ErrCommerceStoreIntegrity
	}
	return nil
}

func validateOutcome(outcome EventOutcome) error {
	if outcome.Status != model.CommerceProviderEventStatusProcessed &&
		outcome.Status != model.CommerceProviderEventStatusIgnored {
		return fmt.Errorf("outcome status %q is not terminal", outcome.Status)
	}
	if err := validateASCII("outcomeKind", outcome.Kind, 64); err != nil {
		return err
	}
	if outcome.Status == model.CommerceProviderEventStatusIgnored && len(outcome.Outbox) != 0 {
		return fmt.Errorf("ignored event cannot publish an outbox effect")
	}
	if len(outcome.Outbox) > 16 {
		return fmt.Errorf("outbox draft count exceeds 16")
	}
	seen := make(map[string]struct{}, len(outcome.Outbox))
	for index, draft := range outcome.Outbox {
		if err := validateASCII(fmt.Sprintf("outbox[%d].topic", index), draft.Topic, 128); err != nil {
			return err
		}
		if len(draft.DedupeKey) != 64 || !isLowerHex(draft.DedupeKey) {
			return fmt.Errorf("outbox[%d].dedupeKey must be lowercase sha256 hex", index)
		}
		if _, ok := seen[draft.DedupeKey]; ok {
			return fmt.Errorf("outbox dedupe key is repeated")
		}
		seen[draft.DedupeKey] = struct{}{}
		if len(draft.Payload) == 0 || len(draft.Payload) > MaxOutboxPayloadBytes ||
			!utf8.Valid(draft.Payload) || !json.Valid(draft.Payload) {
			return fmt.Errorf("outbox[%d] payload is not bounded UTF-8 JSON", index)
		}
	}
	return nil
}

func classifyProcessingError(err error) (string, bool) {
	var processing *ProcessingError
	if errors.As(err, &processing) {
		if validateErrorCode(processing.Code) == nil {
			return processing.Code, processing.Retryable
		}
		return "invalid_error_code", false
	}
	return "processing_error", true
}

func validateErrorCode(code string) error {
	return validateASCII("errorCode", code, 64)
}

func validateASCII(name, value string, max int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > max {
		return fmt.Errorf("%s must contain 1..%d canonical ASCII bytes", name, max)
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return fmt.Errorf("%s must contain printable ASCII", name)
		}
	}
	return nil
}

func validateOptionalASCII(name, value string, max int) error {
	if value == "" {
		return nil
	}
	return validateASCII(name, value, max)
}

func resultFromEvent(event model.CommerceProviderEvent) ProcessResult {
	return ProcessResult{
		EventID: event.EventID, Status: event.Status, OutcomeKind: event.OutcomeKind,
		AttemptCount: event.AttemptCount,
	}
}

func digestOutcome(outcome EventOutcome) string {
	hash := sha256.New()
	writeDigestPart(hash, outcome.Status)
	writeDigestPart(hash, outcome.Kind)
	for _, draft := range outcome.Outbox {
		writeDigestPart(hash, draft.Topic)
		writeDigestPart(hash, draft.DedupeKey)
		writeDigestPart(hash, sha256Hex(draft.Payload))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestPart(writer digestWriter, value string) {
	_, _ = fmt.Fprintf(writer, "%d:%s", len(value), value)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func SHA256Key(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		writeDigestPart(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func VerificationKeyDigest(secret string) string {
	return "sha256:" + SHA256Key(secret)
}

func isLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func canonicalProviderInstant(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := value.UTC().Truncate(time.Microsecond)
	return &canonical
}

func validateProviderInstant(value *time.Time) error {
	if value == nil {
		return nil
	}
	_, offset := value.Zone()
	if offset != 0 || value.Nanosecond()%int(time.Microsecond) != 0 {
		return fmt.Errorf("providerCreatedAt must be UTC with canonical microsecond precision")
	}
	return nil
}

func validateStoredProviderInstant(value *time.Time) error {
	if value == nil {
		return nil
	}
	// MySQL DATETIME(6) is zone-less. With parseTime and loc=Local the driver
	// scans the same instant in the configured local Location, so stored rows
	// must not be rejected merely for carrying a non-UTC offset. Precision is
	// still enforced because DATETIME(6) cannot round-trip nanoseconds.
	if value.Nanosecond()%int(time.Microsecond) != 0 {
		return fmt.Errorf("stored providerCreatedAt exceeds microsecond precision")
	}
	return nil
}

func sameOptionalInstant(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("context is required")
	}
	return ctx.Err()
}

func prepareSafely(
	ctx context.Context,
	processor ProviderEventProcessor,
	snapshot ProviderEventSnapshot,
) (prepared PreparedEvent, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			prepared = PreparedEvent{}
			err = ManualReviewError("processor_panic", fmt.Errorf("provider event processor panic"))
		}
	}()
	return processor.Prepare(ctx, snapshot)
}

func applySafely(
	ctx context.Context,
	prepared PreparedEvent,
	tx *gorm.DB,
	now time.Time,
) (outcome EventOutcome, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = EventOutcome{}
			err = ManualReviewError("processor_panic", fmt.Errorf("provider event apply panic"))
		}
	}()
	return prepared.Apply(ctx, tx, now)
}

func invokeAfterCommit(after func()) {
	if after == nil {
		return
	}
	defer func() { _ = recover() }()
	after()
}

func databaseCommerceNow(ctx context.Context, db *gorm.DB) (time.Time, error) {
	if db == nil || db.Config == nil || db.Dialector == nil {
		return time.Time{}, fmt.Errorf("commerce database is unavailable")
	}
	var query string
	switch db.Dialector.Name() {
	case "mysql":
		query = "SELECT DATE_FORMAT(UTC_TIMESTAMP(6), '%Y-%m-%d %H:%i:%s.%f') AS now"
	case "sqlite":
		query = "SELECT strftime('%Y-%m-%d %H:%M:%f', 'now') AS now"
	default:
		return time.Time{}, fmt.Errorf("unsupported commerce database dialect")
	}
	var result struct {
		Now string `gorm:"column:now"`
	}
	if err := db.WithContext(ctx).Raw(query).Scan(&result).Error; err != nil {
		return time.Time{}, fmt.Errorf("read commerce database time: %w", err)
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", result.Now, time.UTC)
	if err != nil || now.IsZero() {
		return time.Time{}, fmt.Errorf("commerce database returned invalid time")
	}
	return now, nil
}
