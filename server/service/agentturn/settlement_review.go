package agentturn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

const (
	SQLSettlementReviewTable         = "w_agent_turn_settlement_review"
	DefaultSettlementReviewListLimit = 64
	MaxSettlementReviewListLimit     = 512
	MaxSettlementReviewIDBytes       = 64
	MaxSettlementReviewDigestBytes   = 128
)

type SettlementReviewSource string

const (
	SettlementReviewSourceExecutor                 SettlementReviewSource = "executor_release"
	SettlementReviewSourceExecutorCompletion       SettlementReviewSource = "executor_completion"
	SettlementReviewSourceExecutorTerminal         SettlementReviewSource = "executor_terminal"
	SettlementReviewSourceReconcile                SettlementReviewSource = "reconcile_release"
	SettlementReviewSourceReconcileTerminal        SettlementReviewSource = "reconcile_terminal"
	SettlementReviewReasonUsageUnknown                                    = "usage_unknown"
	SettlementReviewReasonCompletedUsageUnmeasured                        = "completed_usage_unmeasured"
	SettlementReviewReasonTerminalUsageUnmeasured                         = "terminal_usage_unmeasured"
	SettlementReviewStatusPending                                         = "pending"
	SettlementReviewStatusMeteredHeld                                     = "metered_held"
	SettlementReviewStatusFinalizedHeld                                   = "finalized_held"
)

func (source SettlementReviewSource) Valid() bool {
	return source == SettlementReviewSourceExecutor ||
		source == SettlementReviewSourceExecutorCompletion ||
		source == SettlementReviewSourceExecutorTerminal ||
		source == SettlementReviewSourceReconcile ||
		source == SettlementReviewSourceReconcileTerminal
}

func (source SettlementReviewSource) executor() bool {
	return source == SettlementReviewSourceExecutor ||
		source == SettlementReviewSourceExecutorCompletion ||
		source == SettlementReviewSourceExecutorTerminal
}

// SettlementUsageEvidence is the immutable evidence snapshot that made a
// release unsafe. Prior counts are read while holding the Turn row lock;
// CurrentEffectCount covers Effects supplied by the terminal command but not
// inserted yet.
type SettlementUsageEvidence struct {
	PriorOperationCount     int64
	PriorEffectCount        int64
	PriorProviderUsageCount int64
	CurrentEffectCount      int
}

func (evidence SettlementUsageEvidence) ambiguous() bool {
	return evidence.PriorOperationCount > 0 || evidence.PriorEffectCount > 0 || evidence.CurrentEffectCount > 0
}

func (evidence SettlementUsageEvidence) validate() error {
	if evidence.PriorOperationCount < 0 || evidence.PriorEffectCount < 0 ||
		evidence.PriorProviderUsageCount < 0 ||
		evidence.CurrentEffectCount < 0 || evidence.CurrentEffectCount > MaxEffectsPerOperation {
		return ErrStoreIntegrity
	}
	return nil
}

// SettlementReviewRecord is a durable, read-only summary of a commercial
// decision that still needs trusted metering or manual adjudication. Opening a
// review terminalizes the Turn, so this state remains orthogonal to execution
// lifecycle and old Workers cannot reclaim the work.
type SettlementReviewRecord struct {
	ReviewID       string
	TurnID         agentv1.TurnID
	SettlementKey  string
	RequestDigest  string
	Reason         string
	Source         SettlementReviewSource
	TerminalStatus agentv1.TurnStatus
	AttemptID      string
	FencingToken   agentv1.Sequence
	OperationID    string
	Evidence       SettlementUsageEvidence
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (review SettlementReviewRecord) Validate() error {
	if err := validatePrintableASCII("reviewId", review.ReviewID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("turnId", string(review.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("settlementKey", review.SettlementKey, MaxSettlementKeyBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("requestDigest", review.RequestDigest, MaxSettlementReviewDigestBytes); err != nil {
		return err
	}
	if !review.Source.Valid() ||
		(review.Status != SettlementReviewStatusPending && review.Status != SettlementReviewStatusMeteredHeld &&
			review.Status != SettlementReviewStatusFinalizedHeld) {
		return ErrStoreIntegrity
	}
	if !review.TerminalStatus.Valid() || !review.TerminalStatus.Terminal() {
		return ErrStoreIntegrity
	}
	if review.FencingToken < 1 || review.FencingToken > MaxDurableSequence {
		return ErrStoreIntegrity
	}
	if err := review.Evidence.validate(); err != nil {
		return err
	}
	switch review.Source {
	case SettlementReviewSourceExecutor:
		if review.Reason != SettlementReviewReasonUsageUnknown || !review.Evidence.ambiguous() ||
			review.Evidence.PriorProviderUsageCount != 0 {
			return ErrStoreIntegrity
		}
		fallthrough
	case SettlementReviewSourceExecutorCompletion:
		if review.Source == SettlementReviewSourceExecutorCompletion &&
			(review.Reason != SettlementReviewReasonCompletedUsageUnmeasured ||
				review.TerminalStatus != agentv1.TurnStatusCompleted) {
			return ErrStoreIntegrity
		}
		if err := validatePrintableASCII("attemptId", review.AttemptID, MaxAttemptIDBytes); err != nil {
			return err
		}
		if err := validatePrintableASCII("operationId", review.OperationID, MaxOperationIDBytes); err != nil {
			return err
		}
		if review.SettlementKey != settlementKey(review.TurnID, review.OperationID) {
			return ErrStoreIntegrity
		}
	case SettlementReviewSourceExecutorTerminal:
		if review.Reason != SettlementReviewReasonTerminalUsageUnmeasured ||
			review.TerminalStatus == agentv1.TurnStatusCompleted {
			return ErrStoreIntegrity
		}
		if err := validatePrintableASCII("attemptId", review.AttemptID, MaxAttemptIDBytes); err != nil {
			return err
		}
		if err := validatePrintableASCII("operationId", review.OperationID, MaxOperationIDBytes); err != nil {
			return err
		}
		if review.SettlementKey != settlementKey(review.TurnID, review.OperationID) {
			return ErrStoreIntegrity
		}
	case SettlementReviewSourceReconcile:
		if review.Reason != SettlementReviewReasonUsageUnknown ||
			review.TerminalStatus == agentv1.TurnStatusCompleted || !review.Evidence.ambiguous() ||
			review.AttemptID != "" || review.OperationID != "" || review.Evidence.CurrentEffectCount != 0 ||
			review.Evidence.PriorProviderUsageCount != 0 {
			return ErrStoreIntegrity
		}
		if review.SettlementKey != reconcileSettlementKey(review.TurnID, review.FencingToken) {
			return ErrStoreIntegrity
		}
	case SettlementReviewSourceReconcileTerminal:
		if review.Reason != SettlementReviewReasonTerminalUsageUnmeasured ||
			review.TerminalStatus == agentv1.TurnStatusCompleted || review.AttemptID != "" ||
			review.OperationID != "" || review.Evidence.CurrentEffectCount != 0 {
			return ErrStoreIntegrity
		}
		if review.SettlementKey != reconcileSettlementKey(review.TurnID, review.FencingToken) {
			return ErrStoreIntegrity
		}
	}
	if review.CreatedAt.IsZero() || review.UpdatedAt.IsZero() || review.UpdatedAt.Before(review.CreatedAt) {
		return ErrStoreIntegrity
	}
	if review.ReviewID != settlementReviewID(review.SettlementKey) ||
		!settlementReviewRequestDigestValid(review) {
		return ErrStoreIntegrity
	}
	return nil
}

// SettlementReviewHoldCommand asks the commercial authority to move the
// Turn's reservation out of ordinary TTL/release handling and into review. It
// runs in the same transaction as the terminal Turn and Review row.
type SettlementReviewHoldCommand struct {
	Review      SettlementReviewRecord
	PrincipalID PrincipalID
}

func (command SettlementReviewHoldCommand) Validate() error {
	if err := command.Review.Validate(); err != nil {
		return err
	}
	if command.Review.Status != SettlementReviewStatusPending {
		return ErrStoreIntegrity
	}
	return validateBoundedText("principalId", string(command.PrincipalID), MaxPrincipalIDBytes)
}

// SettlementReviewAuthority is an optional stronger commercial boundary. A
// plain SettlementAuthority retains the P0-040 zero-mutation rejection for
// ambiguous releases. Only this capability authorizes terminalization with a
// durable pending review.
type SettlementReviewAuthority interface {
	SettlementAuthority
	HoldForReview(tx *gorm.DB, command SettlementReviewHoldCommand) error
}

// SettlementAuthorityBinding is an opaque, one-store proof that the exact
// production composition installed a review-capable commercial authority.
// Its fields are private so callers cannot forge or transplant a binding.
type SettlementAuthorityBinding struct {
	store              *SQLStore
	marker             *byte
	usageAware         bool
	providerUsageAware bool
	providerJournal    *ProviderUsageJournal
}

// BindSettlementReviewAuthority installs and seals the exact production
// commercial boundary. A store that was already configured or bound is
// rejected instead of guessing whether two interface values name the same
// provider instance.
func (store *SQLStore) BindSettlementReviewAuthority(authority SettlementReviewAuthority) (*SettlementAuthorityBinding, error) {
	if store == nil || settlementAuthorityMissing(authority) {
		return nil, ErrSettlementBindingInvalid
	}
	store.settlementMu.Lock()
	defer store.settlementMu.Unlock()
	if store.settlement != nil || store.settlementBinding != nil || store.settlementViolated {
		return nil, ErrSettlementBindingInvalid
	}
	marker := byte(1)
	binding := &SettlementAuthorityBinding{store: store, marker: &marker}
	store.settlement = authority
	store.settlementBinding = binding
	return binding, nil
}

// BindSettlementReviewUsageAuthority is the explicit sealed capability for
// trusted Review metering and meter-aware completed terminalization. Merely
// implementing SettlementReviewUsageAuthority behind the older Review bind
// does not enable these semantics.
func (store *SQLStore) BindSettlementReviewUsageAuthority(
	authority SettlementReviewUsageAuthority,
) (*SettlementAuthorityBinding, error) {
	if store == nil || settlementAuthorityMissing(authority) {
		return nil, ErrSettlementBindingInvalid
	}
	store.settlementMu.Lock()
	defer store.settlementMu.Unlock()
	if store.settlement != nil || store.settlementBinding != nil || store.settlementViolated {
		return nil, ErrSettlementBindingInvalid
	}
	marker := byte(1)
	binding := &SettlementAuthorityBinding{store: store, marker: &marker, usageAware: true}
	store.settlement = authority
	store.settlementBinding = binding
	return binding, nil
}

// BindSettlementReviewProviderUsageAuthority installs the only capability
// allowed to turn durable Provider journal receipts into new usage Evidence.
// Both the Journal and Authority are sealed to this exact Store; an older
// Usage binding remains capable of v3 terminalization and resolution, but it
// cannot manufacture provider provenance.
func (store *SQLStore) BindSettlementReviewProviderUsageAuthority(
	journal *ProviderUsageJournal,
	authority SettlementReviewProviderUsageAuthority,
) (*SettlementAuthorityBinding, error) {
	if store == nil || journal == nil || !journal.MatchesStore(store) ||
		settlementAuthorityMissing(authority) {
		return nil, ErrSettlementBindingInvalid
	}
	store.settlementMu.Lock()
	defer store.settlementMu.Unlock()
	if store.settlement != nil || store.settlementBinding != nil || store.settlementViolated {
		return nil, ErrSettlementBindingInvalid
	}
	marker := byte(1)
	binding := &SettlementAuthorityBinding{
		store: store, marker: &marker, providerUsageAware: true, providerJournal: journal,
	}
	store.settlement = authority
	store.settlementBinding = binding
	return binding, nil
}

// MatchesSettlementAuthorityBinding verifies both origin and integrity. A
// post-bind call to the compatibility mutator makes this return false even
// though the original authority remains installed for commercial safety.
func (store *SQLStore) MatchesSettlementAuthorityBinding(binding *SettlementAuthorityBinding) bool {
	if store == nil || binding == nil {
		return false
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	return store.settlementBinding == binding && binding.store == store &&
		binding.marker != nil && *binding.marker == 1 && store.settlement != nil &&
		!store.settlementViolated &&
		(!binding.providerUsageAware ||
			(binding.providerJournal != nil && binding.providerJournal.MatchesStore(store)))
}

type ListSettlementReviewsQuery struct {
	Limit int
}

func (query ListSettlementReviewsQuery) limit() int {
	if query.Limit <= 0 {
		return DefaultSettlementReviewListLimit
	}
	return query.Limit
}

func (query ListSettlementReviewsQuery) Validate() error {
	if query.Limit < 0 || query.Limit > MaxSettlementReviewListLimit {
		return fmt.Errorf("limit must be between 0 and %d", MaxSettlementReviewListLimit)
	}
	return nil
}

type sqlSettlementReviewRow struct {
	ID                      uint64    `gorm:"column:id;primaryKey;autoIncrement"`
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

func (sqlSettlementReviewRow) TableName() string { return SQLSettlementReviewTable }

func (row sqlSettlementReviewRow) toRecord() (SettlementReviewRecord, error) {
	review := SettlementReviewRecord{
		ReviewID: row.ReviewID, TurnID: agentv1.TurnID(row.TurnID),
		SettlementKey: row.SettlementKey, RequestDigest: row.RequestDigest,
		Reason: row.Reason, Source: SettlementReviewSource(row.Source),
		TerminalStatus: agentv1.TurnStatus(row.TerminalStatus),
		FencingToken:   agentv1.Sequence(row.FencingToken),
		Evidence: SettlementUsageEvidence{
			PriorOperationCount:     row.PriorOperationCount,
			PriorEffectCount:        row.PriorEffectCount,
			PriorProviderUsageCount: row.PriorProviderUsageCount,
			CurrentEffectCount:      row.CurrentEffectCount,
		},
		Status: row.Status, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
	}
	if row.AttemptID != nil {
		review.AttemptID = *row.AttemptID
	}
	if row.OperationID != nil {
		review.OperationID = *row.OperationID
	}
	if err := review.Validate(); err != nil {
		return SettlementReviewRecord{}, ErrStoreIntegrity
	}
	return review, nil
}

func settlementReviewToSQLRow(review SettlementReviewRecord) (sqlSettlementReviewRow, error) {
	if err := review.Validate(); err != nil {
		return sqlSettlementReviewRow{}, err
	}
	row := sqlSettlementReviewRow{
		ReviewID: review.ReviewID, TurnID: string(review.TurnID),
		SettlementKey: review.SettlementKey, RequestDigest: review.RequestDigest,
		Reason: review.Reason, Source: string(review.Source),
		TerminalStatus: string(review.TerminalStatus), FencingToken: int64(review.FencingToken),
		PriorOperationCount:     review.Evidence.PriorOperationCount,
		PriorEffectCount:        review.Evidence.PriorEffectCount,
		PriorProviderUsageCount: review.Evidence.PriorProviderUsageCount,
		CurrentEffectCount:      review.Evidence.CurrentEffectCount,
		Status:                  review.Status, CreatedAt: review.CreatedAt.UTC(), UpdatedAt: review.UpdatedAt.UTC(),
	}
	if review.AttemptID != "" {
		attemptID := review.AttemptID
		row.AttemptID = &attemptID
	}
	if review.OperationID != "" {
		operationID := review.OperationID
		row.OperationID = &operationID
	}
	return row, nil
}

func (store *SQLStore) reviewAuthority() (SettlementReviewAuthority, bool) {
	base, intact := store.settlementAuthority()
	authority, ok := base.(SettlementReviewAuthority)
	return authority, intact && ok && !settlementAuthorityMissing(authority)
}

// completedUsageTerminalization returns the immutable digest policy only for
// the exact sealed Usage binding. Compatibility composition remains on the v2
// path even when its dynamic authority happens to implement the stronger
// interface.
func (store *SQLStore) completedUsageTerminalization() (commitDigestTerminalization, bool, error) {
	if store == nil {
		return commitDigestTerminalization{}, false, ErrSettlementBindingInvalid
	}
	store.settlementMu.RLock()
	defer store.settlementMu.RUnlock()
	binding := store.settlementBinding
	if binding == nil || !binding.usageAware {
		return commitDigestTerminalization{}, false, nil
	}
	if store.settlementViolated || binding.store != store || binding.marker == nil ||
		*binding.marker != 1 || store.settlement == nil {
		return commitDigestTerminalization{}, false, ErrSettlementBindingInvalid
	}
	authority, ok := store.settlement.(SettlementReviewUsageAuthority)
	if !ok || settlementAuthorityMissing(authority) {
		return commitDigestTerminalization{}, false, ErrSettlementBindingInvalid
	}
	return newCompletedUsageTerminalization(), true, nil
}

func buildSettlementReviewRecord(
	turn Turn,
	terminal agentv1.TurnStatus,
	source SettlementReviewSource,
	attemptID string,
	fence agentv1.Sequence,
	operationID string,
	key string,
	evidence SettlementUsageEvidence,
	now time.Time,
) (SettlementReviewRecord, error) {
	review := SettlementReviewRecord{
		ReviewID: settlementReviewID(key), TurnID: turn.ID, SettlementKey: key,
		Reason: SettlementReviewReasonUsageUnknown, Source: source,
		TerminalStatus: terminal, AttemptID: attemptID, FencingToken: fence,
		OperationID: operationID, Evidence: evidence,
		Status: SettlementReviewStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if source == SettlementReviewSourceExecutorCompletion {
		review.Reason = SettlementReviewReasonCompletedUsageUnmeasured
	}
	review.RequestDigest = settlementReviewRequestDigest(review)
	if err := review.Validate(); err != nil {
		return SettlementReviewRecord{}, ErrStoreIntegrity
	}
	return review, nil
}

func buildProviderSettlementReviewRecord(
	turn Turn,
	terminal agentv1.TurnStatus,
	source SettlementReviewSource,
	attemptID string,
	fence agentv1.Sequence,
	operationID string,
	key string,
	evidence SettlementUsageEvidence,
	now time.Time,
) (SettlementReviewRecord, error) {
	if source != SettlementReviewSourceExecutorCompletion &&
		source != SettlementReviewSourceExecutorTerminal &&
		source != SettlementReviewSourceReconcileTerminal {
		return SettlementReviewRecord{}, ErrStoreIntegrity
	}
	review := SettlementReviewRecord{
		ReviewID: settlementReviewID(key), TurnID: turn.ID, SettlementKey: key,
		Reason: SettlementReviewReasonTerminalUsageUnmeasured, Source: source,
		TerminalStatus: terminal, AttemptID: attemptID, FencingToken: fence,
		OperationID: operationID, Evidence: evidence,
		Status: SettlementReviewStatusPending, CreatedAt: now, UpdatedAt: now,
	}
	if source == SettlementReviewSourceExecutorCompletion {
		review.Reason = SettlementReviewReasonCompletedUsageUnmeasured
	}
	review.RequestDigest = settlementReviewRequestDigestV2(review)
	if err := review.Validate(); err != nil {
		return SettlementReviewRecord{}, ErrStoreIntegrity
	}
	return review, nil
}

func (store *SQLStore) persistSettlementReview(
	tx *gorm.DB,
	turn Turn,
	review SettlementReviewRecord,
) error {
	if tx == nil || review.Status != SettlementReviewStatusPending {
		return ErrStoreIntegrity
	}
	var authority SettlementReviewAuthority
	if settlementReviewProviderUsageAware(review) {
		store.settlementMu.RLock()
		defer store.settlementMu.RUnlock()
		providerAuthority, _, err := store.settlementReviewProviderUsageAuthorityLocked()
		if err != nil {
			return err
		}
		authority = providerAuthority
	} else if review.Source == SettlementReviewSourceExecutorCompletion {
		// Keep the exact binding immutable across validation, receipt insertion
		// and the transaction-local hold call. Entry points that must cover the
		// outer commit use persistSettlementReviewWithAuthority under their own
		// function-scoped binding lock.
		store.settlementMu.RLock()
		defer store.settlementMu.RUnlock()
		usageAuthority, err := store.settlementReviewUsageAuthorityLocked()
		if err != nil {
			return err
		}
		authority = usageAuthority
	} else {
		var ok bool
		authority, ok = store.reviewAuthority()
		if !ok {
			return ErrSettlementUsageUnknown
		}
	}
	return persistSettlementReviewWithAuthority(tx, turn, review, authority)
}

// persistSettlementReviewWithAuthority is the lock-free persistence core for
// callers that already hold the exact settlement binding read lock through
// the enclosing database transaction's commit or rollback. Other callers
// must use persistSettlementReview, which acquires and validates its own
// binding before entering this helper.
func persistSettlementReviewWithAuthority(
	tx *gorm.DB,
	turn Turn,
	review SettlementReviewRecord,
	authority SettlementReviewAuthority,
) error {
	if tx == nil || review.Status != SettlementReviewStatusPending ||
		settlementAuthorityMissing(authority) {
		return ErrStoreIntegrity
	}
	row, err := settlementReviewToSQLRow(review)
	if err != nil {
		return ErrStoreIntegrity
	}
	if err := tx.Create(&row).Error; err != nil {
		return err
	}
	hold := SettlementReviewHoldCommand{Review: review, PrincipalID: turn.PrincipalID}
	if err := hold.Validate(); err != nil {
		return ErrStoreIntegrity
	}
	if err := authority.HoldForReview(tx, hold); err != nil {
		return fmt.Errorf("%w: hold pending review", ErrSettlementReviewFailed)
	}
	return nil
}

func settlementReviewID(settlementKey string) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%d:%s%d:%s", len("review"), "review", len(settlementKey), settlementKey)
	return hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewRequestDigest(review SettlementReviewRecord) string {
	return settlementReviewRequestDigestV1(review)
}

func settlementReviewRequestDigestV1(review SettlementReviewRecord) string {
	hash := sha256.New()
	parts := []string{
		"settlement-review-v1", review.ReviewID, string(review.TurnID), review.SettlementKey,
		review.Reason, string(review.Source), string(review.TerminalStatus), review.AttemptID,
		strconv.FormatInt(int64(review.FencingToken), 10), review.OperationID,
		strconv.FormatInt(review.Evidence.PriorOperationCount, 10),
		strconv.FormatInt(review.Evidence.PriorEffectCount, 10),
		strconv.Itoa(review.Evidence.CurrentEffectCount),
	}
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewRequestDigestV2(review SettlementReviewRecord) string {
	hash := sha256.New()
	parts := []string{
		"settlement-review-v2", review.ReviewID, string(review.TurnID), review.SettlementKey,
		review.Reason, string(review.Source), string(review.TerminalStatus), review.AttemptID,
		strconv.FormatInt(int64(review.FencingToken), 10), review.OperationID,
		strconv.FormatInt(review.Evidence.PriorOperationCount, 10),
		strconv.FormatInt(review.Evidence.PriorEffectCount, 10),
		strconv.FormatInt(review.Evidence.PriorProviderUsageCount, 10),
		strconv.Itoa(review.Evidence.CurrentEffectCount),
	}
	for _, part := range parts {
		fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewRequestDigestValid(review SettlementReviewRecord) bool {
	switch review.Source {
	case SettlementReviewSourceExecutor, SettlementReviewSourceReconcile:
		return review.Evidence.PriorProviderUsageCount == 0 &&
			review.RequestDigest == settlementReviewRequestDigestV1(review)
	case SettlementReviewSourceExecutorCompletion:
		if review.RequestDigest == settlementReviewRequestDigestV2(review) {
			return true
		}
		return review.Evidence.PriorProviderUsageCount == 0 &&
			review.RequestDigest == settlementReviewRequestDigestV1(review)
	case SettlementReviewSourceExecutorTerminal, SettlementReviewSourceReconcileTerminal:
		return review.RequestDigest == settlementReviewRequestDigestV2(review)
	default:
		return false
	}
}

func settlementReviewProviderUsageAware(review SettlementReviewRecord) bool {
	return review.Validate() == nil && review.RequestDigest == settlementReviewRequestDigestV2(review) &&
		(review.Source == SettlementReviewSourceExecutorCompletion ||
			review.Source == SettlementReviewSourceExecutorTerminal ||
			review.Source == SettlementReviewSourceReconcileTerminal)
}

func (store *SQLStore) lookupSettlementReviewTx(tx *gorm.DB, turnID agentv1.TurnID, lock bool) (SettlementReviewRecord, bool, error) {
	if tx == nil {
		return SettlementReviewRecord{}, false, ErrStoreIntegrity
	}
	query := tx.Table(SQLSettlementReviewTable).Where("turn_id = ?", string(turnID))
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sqlSettlementReviewRow
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SettlementReviewRecord{}, false, nil
	}
	if err != nil {
		return SettlementReviewRecord{}, false, err
	}
	review, err := row.toRecord()
	if err != nil {
		return SettlementReviewRecord{}, false, err
	}
	return review, true, nil
}

func inspectReplayReleaseEvidence(
	tx *gorm.DB,
	turnID agentv1.TurnID,
	operationID string,
	currentEffects int,
) (SettlementUsageEvidence, error) {
	if tx == nil || currentEffects < 0 || currentEffects > MaxEffectsPerOperation {
		return SettlementUsageEvidence{}, ErrStoreIntegrity
	}
	evidence := SettlementUsageEvidence{CurrentEffectCount: currentEffects}
	for index, table := range []string{SQLTurnOperationTable, SQLEffectOutboxTable} {
		var count int64
		if err := tx.Table(table).
			Where("turn_id = ? AND operation_id <> ?", string(turnID), operationID).
			Count(&count).Error; err != nil {
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

// validateTerminalSettlementReviewTx proves that a pending or financially
// finalized-held Review still names the immutable terminal authorization and
// that no nonterminal Effect escaped its durable hold. The Turn row must
// already be locked by the caller.
func (store *SQLStore) validateTerminalSettlementReviewTx(
	tx *gorm.DB,
	turnRow sqlTurnRow,
	turn Turn,
	review SettlementReviewRecord,
) error {
	if tx == nil || !turn.Status.Terminal() || review.TurnID != turn.ID ||
		review.TerminalStatus != turn.Status ||
		review.FencingToken != agentv1.Sequence(turnRow.FencingToken) {
		return ErrStoreIntegrity
	}
	var escaped int64
	if err := tx.Table(SQLEffectOutboxTable).
		Where("turn_id = ? AND status IN ?", string(turn.ID), []string{
			string(EffectStatusPending), string(EffectStatusDelivering),
		}).Count(&escaped).Error; err != nil {
		return err
	}
	if escaped != 0 {
		return ErrStoreIntegrity
	}

	var evidence SettlementUsageEvidence
	switch review.Source {
	case SettlementReviewSourceExecutor, SettlementReviewSourceExecutorCompletion,
		SettlementReviewSourceExecutorTerminal:
		operation, found, err := store.lookupOperationTx(tx, turn.ID, review.OperationID, false)
		if err != nil {
			return err
		}
		if !found || validateOperationRow(operation) != nil ||
			operation.AttemptID != review.AttemptID ||
			operation.FencingToken != int64(review.FencingToken) ||
			agentv1.TurnStatus(operation.TurnStatus) != turn.Status ||
			operation.EffectCount != review.Evidence.CurrentEffectCount {
			return ErrStoreIntegrity
		}
		var effectRows []sqlEffectOutboxRow
		if err := tx.Where("turn_id = ? AND operation_id = ?", operation.TurnID, operation.OperationID).
			Order("ordinal ASC").Find(&effectRows).Error; err != nil {
			return err
		}
		if len(effectRows) != operation.EffectCount {
			return ErrStoreIntegrity
		}
		for ordinal, effectRow := range effectRows {
			if _, err := effectRow.toRecord(); err != nil || effectRow.Ordinal != ordinal ||
				effectRow.TurnID != operation.TurnID || effectRow.AttemptID != operation.AttemptID ||
				effectRow.TurnFencingToken != operation.FencingToken ||
				effectRow.OperationID != operation.OperationID ||
				EffectStatus(effectRow.Status) != EffectStatusReviewHold {
				return ErrStoreIntegrity
			}
		}
		attemptRow, found, err := store.lookupAttemptByIDTx(tx, review.AttemptID, false)
		if err != nil {
			return err
		}
		if !found {
			return ErrStoreIntegrity
		}
		attempt, err := attemptRow.toAttempt()
		if err != nil || attempt.TurnID != turn.ID ||
			attempt.FencingToken != review.FencingToken ||
			attempt.Status != attemptStatusForTurn(turn.Status) {
			return ErrStoreIntegrity
		}
		evidence, err = inspectReplayReleaseEvidence(
			tx, turn.ID, operation.OperationID, operation.EffectCount,
		)
		if err != nil {
			return err
		}
	case SettlementReviewSourceReconcile, SettlementReviewSourceReconcileTerminal:
		if review.SettlementKey != reconcileSettlementKey(turn.ID, review.FencingToken) {
			return ErrStoreIntegrity
		}
		var err error
		evidence, err = inspectAmbiguousRelease(tx, turn.ID, 0)
		if err != nil {
			return err
		}
	default:
		return ErrStoreIntegrity
	}
	if evidence.PriorOperationCount != review.Evidence.PriorOperationCount ||
		evidence.PriorEffectCount != review.Evidence.PriorEffectCount ||
		evidence.CurrentEffectCount != review.Evidence.CurrentEffectCount {
		return ErrStoreIntegrity
	}
	if settlementReviewProviderUsageAware(review) {
		providerUsage, err := store.lookupSettlementReviewProviderUsageTx(tx, review, false)
		if err != nil {
			return err
		}
		if int64(len(providerUsage)) < review.Evidence.PriorProviderUsageCount {
			return ErrStoreIntegrity
		}
	} else {
		var providerUsageCount int64
		if err := tx.Table(SQLProviderUsageJournalTable).
			Where("turn_id = ?", string(turn.ID)).Count(&providerUsageCount).Error; err != nil {
			return err
		}
		if review.Evidence.PriorProviderUsageCount != 0 || providerUsageCount != 0 {
			return ErrStoreIntegrity
		}
	}
	if _, err := store.validateSettlementReviewResolutionStateTx(tx, review); err != nil {
		return err
	}
	if review.Status != SettlementReviewStatusPending {
		usage, found, err := store.lookupSettlementReviewUsageEvidenceTx(tx, review.ReviewID, false)
		if err != nil {
			return err
		}
		if !found || usage.Plugin != turn.Plugin {
			return ErrStoreIntegrity
		}
	}
	return nil
}

// ListSettlementReviews is an internal, bounded read model for a future
// protected operator surface. It grants no settlement or Effect authority.
func (store *SQLStore) ListSettlementReviews(ctx context.Context, query ListSettlementReviewsQuery) ([]SettlementReviewRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	var rows []sqlSettlementReviewRow
	if err := store.db.WithContext(ctx).Table(SQLSettlementReviewTable).
		Where("status IN ?", []string{SettlementReviewStatusPending, SettlementReviewStatusMeteredHeld}).
		Order("created_at ASC, id ASC").Limit(query.limit()).Find(&rows).Error; err != nil {
		return nil, store.normalize("list-settlement-reviews", err)
	}
	reviews := make([]SettlementReviewRecord, 0, len(rows))
	for _, row := range rows {
		review, err := row.toRecord()
		if err != nil {
			return nil, store.integrity("list-settlement-reviews")
		}
		current, stillOpen, err := store.validateOpenSettlementReviewListCandidate(ctx, review)
		if err != nil {
			return nil, store.normalize("list-settlement-reviews", err)
		}
		if !stillOpen {
			continue
		}
		reviews = append(reviews, current)
	}
	return reviews, nil
}

// validateOpenSettlementReviewListCandidate distinguishes malformed child
// state from the normal pending -> metered_held -> finalized_held races after
// the list query. A newer open row replaces the stale snapshot; a finalized
// row is omitted. Non-monotonic or mismatched state still fails closed.
func (store *SQLStore) validateOpenSettlementReviewListCandidate(
	ctx context.Context,
	review SettlementReviewRecord,
) (SettlementReviewRecord, bool, error) {
	database := store.db.WithContext(ctx)
	if _, err := store.validateSettlementReviewResolutionStateTx(database, review); err == nil {
		return review, true, nil
	} else if !errors.Is(err, ErrStoreIntegrity) {
		return SettlementReviewRecord{}, false, err
	}

	latest, found, err := store.lookupSettlementReviewTx(database, review.TurnID, false)
	if err != nil {
		return SettlementReviewRecord{}, false, err
	}
	if !found || latest.ReviewID != review.ReviewID || latest.RequestDigest != review.RequestDigest ||
		!latest.CreatedAt.Equal(review.CreatedAt) {
		return SettlementReviewRecord{}, false, ErrStoreIntegrity
	}
	if _, err := store.validateSettlementReviewResolutionStateTx(database, latest); err != nil {
		return SettlementReviewRecord{}, false, err
	}
	switch latest.Status {
	case SettlementReviewStatusPending, SettlementReviewStatusMeteredHeld:
		return latest, true, nil
	case SettlementReviewStatusFinalizedHeld:
		return SettlementReviewRecord{}, false, nil
	default:
		return SettlementReviewRecord{}, false, ErrStoreIntegrity
	}
}

// holdTurnEffectsForReview fences pending and already-leased Effects while the
// caller holds the Turn lock. Old Dispatchers only scan pending/delivering, so
// the durable review_hold value is also fail-closed during rolling upgrades.
func holdTurnEffectsForReview(tx *gorm.DB, turnID agentv1.TurnID, now time.Time) error {
	if tx == nil {
		return ErrStoreIntegrity
	}
	return tx.Table(SQLEffectOutboxTable).
		Where("turn_id = ? AND status IN ?", string(turnID), []string{string(EffectStatusPending), string(EffectStatusDelivering)}).
		UpdateColumns(map[string]any{
			"status":           string(EffectStatusReviewHold),
			"lease_owner_id":   nil,
			"lease_expires_at": nil,
			"updated_at": gorm.Expr(
				"CASE WHEN updated_at > ? THEN updated_at ELSE ? END", now, now,
			),
		}).Error
}
