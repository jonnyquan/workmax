package agentturn

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

const (
	SQLSettlementReviewResolutionTable = "w_agent_turn_settlement_review_resolution"

	SettlementReviewResolutionReasonMeteredUsageConfirmed = "metered_usage_confirmed"
	MaxSettlementReviewActorIDBytes                       = 256
	MaxSettlementReviewResolutionDigestBytes              = 128
)

// ResolveSettlementReviewCommand is an internal adjudication command. It is
// deliberately not an authentication or HTTP contract: a future protected
// operator surface must derive ActorID from its credential rather than trust
// an arbitrary request field.
type ResolveSettlementReviewCommand struct {
	TurnID                 agentv1.TurnID
	ReviewID               string
	ExpectedRequestDigest  string
	EvidenceID             string
	ExpectedEvidenceDigest string
	ActorID                string
}

func (command ResolveSettlementReviewCommand) Validate() error {
	if err := validatePathSegment("turnId", string(command.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("reviewId", command.ReviewID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validateSettlementReviewSHA256Digest("expectedRequestDigest", command.ExpectedRequestDigest); err != nil {
		return err
	}
	if err := validatePrintableASCII("evidenceId", command.EvidenceID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validateSettlementReviewSHA256Digest("expectedEvidenceDigest", command.ExpectedEvidenceDigest); err != nil {
		return err
	}
	if err := validatePrintableASCII("actorId", command.ActorID, MaxSettlementReviewActorIDBytes); err != nil {
		return err
	}
	return nil
}

// SettlementReviewResolutionAuthorityCommand contains the exact immutable
// decision presented to the transaction-local commercial authority. It never
// contains a UID or Reservation ID supplied by the operator; resolving the
// locked Turn principal to the authoritative reservation remains the
// authority's responsibility.
type SettlementReviewResolutionAuthorityCommand struct {
	Review                SettlementReviewRecord
	Evidence              SettlementReviewUsageEvidenceRecord
	PrincipalID           PrincipalID
	ResolutionID          string
	DecisionDigest        string
	Intent                SettlementIntent
	UsedUnits             int64
	ActorID               string
	Reason                string
	EvidenceDigest        string
	PricingSnapshotDigest string
}

func (command SettlementReviewResolutionAuthorityCommand) Validate() error {
	if err := command.Review.Validate(); err != nil || command.Review.Status != SettlementReviewStatusMeteredHeld {
		return ErrStoreIntegrity
	}
	if err := command.Evidence.Validate(); err != nil ||
		command.Evidence.ReviewID != command.Review.ReviewID ||
		command.Evidence.TurnID != command.Review.TurnID ||
		command.Evidence.SettlementKey != command.Review.SettlementKey ||
		command.Evidence.ReviewRequestDigest != command.Review.RequestDigest ||
		!command.Evidence.CreatedAt.Equal(command.Review.UpdatedAt) {
		return ErrStoreIntegrity
	}
	if err := validateBoundedText("principalId", string(command.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	input := ResolveSettlementReviewCommand{
		TurnID: command.Review.TurnID, ReviewID: command.Review.ReviewID,
		ExpectedRequestDigest: command.Review.RequestDigest,
		EvidenceID:            command.Evidence.EvidenceID, ExpectedEvidenceDigest: command.Evidence.EvidenceDigest,
		ActorID: command.ActorID,
	}
	if err := input.Validate(); err != nil {
		return err
	}
	if command.Intent != SettlementIntentFinalize || command.UsedUnits != command.Evidence.UsedUnits ||
		command.Reason != SettlementReviewResolutionReasonMeteredUsageConfirmed ||
		command.EvidenceDigest != command.Evidence.EvidenceDigest ||
		command.PricingSnapshotDigest != command.Evidence.PricingSnapshotDigest {
		return ErrStoreIntegrity
	}
	if command.ResolutionID != settlementReviewResolutionID(command.Review.ReviewID) ||
		command.DecisionDigest != settlementReviewResolutionDecisionDigest(command.Review, command.Evidence, input) {
		return ErrStoreIntegrity
	}
	return nil
}

// SettlementReviewResolutionAuthorityReceipt is the authority's durable
// transaction-local result. ReservedUnits is authoritative commercial data;
// the kernel only verifies that the positive finalized usage fits within it.
type SettlementReviewResolutionAuthorityReceipt struct {
	ResolutionID          string
	DecisionDigest        string
	EvidenceID            string
	EvidenceDigest        string
	PricingSnapshotDigest string
	UsedUnits             int64
	ReservedUnits         int64
	ReceiptDigest         string
}

func (receipt SettlementReviewResolutionAuthorityReceipt) Validate(command SettlementReviewResolutionAuthorityCommand) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if receipt.ResolutionID != command.ResolutionID || receipt.DecisionDigest != command.DecisionDigest ||
		receipt.EvidenceID != command.Evidence.EvidenceID ||
		receipt.EvidenceDigest != command.Evidence.EvidenceDigest ||
		receipt.PricingSnapshotDigest != command.Evidence.PricingSnapshotDigest ||
		receipt.UsedUnits != command.UsedUnits {
		return ErrStoreIntegrity
	}
	if receipt.ReservedUnits < receipt.UsedUnits || receipt.ReservedUnits < 1 ||
		receipt.ReservedUnits > int64(MaxDurableSequence) {
		return ErrSettlementReviewUnitsExceedReserved
	}
	return validateSettlementReviewSHA256Digest("authorityReceiptDigest", receipt.ReceiptDigest)
}

// SettlementReviewResolutionAuthority is intentionally stronger than the
// hold-only P0-041 capability. ResolveReview runs under the Turn and Review
// locks in the caller's transaction and must not perform network I/O.
type SettlementReviewResolutionAuthority interface {
	SettlementReviewAuthority
	ResolveReview(
		tx *gorm.DB,
		command SettlementReviewResolutionAuthorityCommand,
	) (SettlementReviewResolutionAuthorityReceipt, error)
}

// SettlementReviewResolutionRecord is the immutable financial receipt. A
// successful evidence-backed resolution does not dispose of Effects:
// review_hold stays in force and is represented by the parent Review's
// finalized_held status.
type SettlementReviewResolutionRecord struct {
	ResolutionID           string
	ReviewID               string
	TurnID                 agentv1.TurnID
	SettlementKey          string
	ReviewRequestDigest    string
	EvidenceID             string
	PricingSnapshotDigest  string
	DecisionDigest         string
	ResolutionDigest       string
	Intent                 SettlementIntent
	UsedUnits              int64
	ReservedUnits          int64
	ActorID                string
	Reason                 string
	EvidenceDigest         string
	AuthorityReceiptDigest string
	CreatedAt              time.Time
}

func (resolution SettlementReviewResolutionRecord) Validate() error {
	if err := validatePrintableASCII("resolutionId", resolution.ResolutionID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("reviewId", resolution.ReviewID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("turnId", string(resolution.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("settlementKey", resolution.SettlementKey, MaxSettlementKeyBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("evidenceId", resolution.EvidenceID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"reviewRequestDigest":    resolution.ReviewRequestDigest,
		"pricingSnapshotDigest":  resolution.PricingSnapshotDigest,
		"decisionDigest":         resolution.DecisionDigest,
		"resolutionDigest":       resolution.ResolutionDigest,
		"evidenceDigest":         resolution.EvidenceDigest,
		"authorityReceiptDigest": resolution.AuthorityReceiptDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	if resolution.Intent != SettlementIntentFinalize || resolution.UsedUnits < 1 ||
		resolution.UsedUnits > int64(MaxDurableSequence) || resolution.ReservedUnits < resolution.UsedUnits ||
		resolution.ReservedUnits > int64(MaxDurableSequence) {
		return ErrStoreIntegrity
	}
	if err := validatePrintableASCII("actorId", resolution.ActorID, MaxSettlementReviewActorIDBytes); err != nil {
		return err
	}
	createdAt, err := canonicalExecutionTime(resolution.CreatedAt)
	if err != nil || !createdAt.Equal(resolution.CreatedAt) ||
		resolution.Reason != SettlementReviewResolutionReasonMeteredUsageConfirmed {
		return ErrStoreIntegrity
	}
	if resolution.ResolutionID != settlementReviewResolutionID(resolution.ReviewID) ||
		resolution.EvidenceID != settlementReviewUsageEvidenceID(resolution.ReviewID) ||
		resolution.DecisionDigest != settlementReviewResolutionRecordDecisionDigest(resolution) ||
		resolution.ResolutionDigest != settlementReviewResolutionDigest(resolution) {
		return ErrStoreIntegrity
	}
	return nil
}

type ResolveSettlementReviewResult struct {
	Review     SettlementReviewRecord
	Resolution SettlementReviewResolutionRecord
	Replay     bool
}

type ListSettlementReviewResolutionsQuery struct {
	Limit int
}

func (query ListSettlementReviewResolutionsQuery) limit() int {
	if query.Limit <= 0 {
		return DefaultSettlementReviewListLimit
	}
	return query.Limit
}

func (query ListSettlementReviewResolutionsQuery) Validate() error {
	if query.Limit < 0 || query.Limit > MaxSettlementReviewListLimit {
		return fmt.Errorf("limit must be between 0 and %d", MaxSettlementReviewListLimit)
	}
	return nil
}

type sqlSettlementReviewResolutionRow struct {
	ID                     uint64    `gorm:"column:id;primaryKey;autoIncrement"`
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

func (sqlSettlementReviewResolutionRow) TableName() string {
	return SQLSettlementReviewResolutionTable
}

func (row sqlSettlementReviewResolutionRow) toRecord() (SettlementReviewResolutionRecord, error) {
	resolution := SettlementReviewResolutionRecord{
		ResolutionID: row.ResolutionID, ReviewID: row.ReviewID, TurnID: agentv1.TurnID(row.TurnID),
		SettlementKey: row.SettlementKey, ReviewRequestDigest: row.ReviewRequestDigest,
		EvidenceID: row.EvidenceID, PricingSnapshotDigest: row.PricingSnapshotDigest,
		DecisionDigest: row.DecisionDigest, ResolutionDigest: row.ResolutionDigest,
		Intent: SettlementIntent(row.Intent), UsedUnits: row.UsedUnits, ReservedUnits: row.ReservedUnits,
		ActorID: row.ActorID, Reason: row.Reason, EvidenceDigest: row.EvidenceDigest,
		AuthorityReceiptDigest: row.AuthorityReceiptDigest, CreatedAt: row.CreatedAt.UTC(),
	}
	if err := resolution.Validate(); err != nil {
		return SettlementReviewResolutionRecord{}, ErrStoreIntegrity
	}
	return resolution, nil
}

func settlementReviewResolutionToSQLRow(
	resolution SettlementReviewResolutionRecord,
) (sqlSettlementReviewResolutionRow, error) {
	if err := resolution.Validate(); err != nil {
		return sqlSettlementReviewResolutionRow{}, err
	}
	return sqlSettlementReviewResolutionRow{
		ResolutionID: resolution.ResolutionID, ReviewID: resolution.ReviewID,
		TurnID: string(resolution.TurnID), SettlementKey: resolution.SettlementKey,
		ReviewRequestDigest: resolution.ReviewRequestDigest, DecisionDigest: resolution.DecisionDigest,
		EvidenceID: resolution.EvidenceID, PricingSnapshotDigest: resolution.PricingSnapshotDigest,
		ResolutionDigest: resolution.ResolutionDigest, Intent: string(resolution.Intent),
		UsedUnits: resolution.UsedUnits, ReservedUnits: resolution.ReservedUnits,
		ActorID: resolution.ActorID, Reason: resolution.Reason, EvidenceDigest: resolution.EvidenceDigest,
		AuthorityReceiptDigest: resolution.AuthorityReceiptDigest, CreatedAt: resolution.CreatedAt.UTC(),
	}, nil
}

func settlementReviewResolutionID(reviewID string) string {
	hash := sha256.New()
	settlementReviewHashParts(hash, "settlement-review-resolution-id-v1", reviewID)
	return hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewResolutionDecisionDigest(
	review SettlementReviewRecord,
	evidence SettlementReviewUsageEvidenceRecord,
	command ResolveSettlementReviewCommand,
) string {
	return settlementReviewResolutionDecisionDigestFields(
		settlementReviewResolutionID(review.ReviewID), review.ReviewID, review.TurnID,
		review.SettlementKey, review.RequestDigest, evidence.EvidenceID,
		evidence.EvidenceDigest, evidence.PricingSnapshotDigest,
		SettlementIntentFinalize, evidence.UsedUnits, command.ActorID,
		SettlementReviewResolutionReasonMeteredUsageConfirmed,
	)
}

func settlementReviewResolutionRecordDecisionDigest(resolution SettlementReviewResolutionRecord) string {
	return settlementReviewResolutionDecisionDigestFields(
		resolution.ResolutionID, resolution.ReviewID, resolution.TurnID,
		resolution.SettlementKey, resolution.ReviewRequestDigest, resolution.EvidenceID,
		resolution.EvidenceDigest, resolution.PricingSnapshotDigest, resolution.Intent,
		resolution.UsedUnits, resolution.ActorID, resolution.Reason,
	)
}

func settlementReviewResolutionDecisionDigestFields(
	resolutionID string,
	reviewID string,
	turnID agentv1.TurnID,
	settlementKey string,
	reviewRequestDigest string,
	evidenceID string,
	evidenceDigest string,
	pricingSnapshotDigest string,
	intent SettlementIntent,
	usedUnits int64,
	actorID string,
	reason string,
) string {
	hash := sha256.New()
	settlementReviewHashParts(hash,
		"settlement-review-resolution-decision-v2", resolutionID, reviewID, string(turnID),
		settlementKey, reviewRequestDigest, evidenceID, evidenceDigest, pricingSnapshotDigest,
		string(intent), strconv.FormatInt(usedUnits, 10), actorID, reason,
	)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewResolutionDigest(resolution SettlementReviewResolutionRecord) string {
	hash := sha256.New()
	settlementReviewHashParts(hash,
		"settlement-review-resolution-receipt-v2", resolution.ResolutionID, resolution.ReviewID,
		string(resolution.TurnID), resolution.SettlementKey, resolution.ReviewRequestDigest,
		resolution.EvidenceID, resolution.EvidenceDigest, resolution.PricingSnapshotDigest,
		resolution.DecisionDigest, string(resolution.Intent), strconv.FormatInt(resolution.UsedUnits, 10),
		strconv.FormatInt(resolution.ReservedUnits, 10), resolution.ActorID, resolution.Reason,
		resolution.AuthorityReceiptDigest,
		resolution.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewHashParts(hash interface{ Write([]byte) (int, error) }, parts ...string) {
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:%s", len(part), part)
	}
}

func validateSettlementReviewSHA256Digest(name, value string) error {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) ||
		value[len(prefix):] != strings.ToLower(value[len(prefix):]) {
		return fmt.Errorf("%s must be a lowercase SHA-256 digest", name)
	}
	if _, err := hex.DecodeString(value[len(prefix):]); err != nil {
		return fmt.Errorf("%s must be a lowercase SHA-256 digest", name)
	}
	return nil
}

func (store *SQLStore) settlementReviewResolutionAuthorityLocked() (
	SettlementReviewResolutionAuthority,
	error,
) {
	if store == nil || store.settlementViolated {
		return nil, ErrSettlementBindingInvalid
	}
	binding := store.settlementBinding
	if binding == nil || (!binding.usageAware && !binding.providerUsageAware) {
		return nil, ErrSettlementReviewResolutionUnavailable
	}
	if binding.store != store || binding.marker == nil || *binding.marker != 1 ||
		store.settlement == nil {
		return nil, ErrSettlementBindingInvalid
	}
	authority, ok := store.settlement.(SettlementReviewResolutionAuthority)
	if !ok || settlementAuthorityMissing(authority) {
		return nil, ErrSettlementReviewResolutionUnavailable
	}
	return authority, nil
}

func (store *SQLStore) lookupSettlementReviewResolutionTx(
	tx *gorm.DB,
	reviewID string,
	lock bool,
) (SettlementReviewResolutionRecord, bool, error) {
	if tx == nil {
		return SettlementReviewResolutionRecord{}, false, ErrStoreIntegrity
	}
	query := tx.Table(SQLSettlementReviewResolutionTable).Where("review_id = ?", reviewID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sqlSettlementReviewResolutionRow
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SettlementReviewResolutionRecord{}, false, nil
	}
	if err != nil {
		return SettlementReviewResolutionRecord{}, false, err
	}
	resolution, err := row.toRecord()
	if err != nil {
		return SettlementReviewResolutionRecord{}, false, err
	}
	return resolution, true, nil
}

// validateSettlementReviewResolutionStateTx binds mutable Review status to
// its append-only usage evidence and financial receipt. Missing, extra or
// cross-bound rows fail closed at every state.
func (store *SQLStore) validateSettlementReviewResolutionStateTx(
	tx *gorm.DB,
	review SettlementReviewRecord,
) (*SettlementReviewResolutionRecord, error) {
	if tx == nil || review.Validate() != nil {
		return nil, ErrStoreIntegrity
	}
	resolution, found, err := store.lookupSettlementReviewResolutionTx(tx, review.ReviewID, false)
	if err != nil {
		return nil, err
	}
	evidence, evidenceFound, err := store.lookupSettlementReviewUsageEvidenceTx(tx, review.ReviewID, false)
	if err != nil {
		return nil, err
	}
	switch review.Status {
	case SettlementReviewStatusPending:
		if found || evidenceFound {
			return nil, ErrStoreIntegrity
		}
		return nil, nil
	case SettlementReviewStatusMeteredHeld:
		if found || !evidenceFound || !settlementReviewUsageEvidenceMatchesReview(evidence, review) ||
			!evidence.CreatedAt.Equal(review.UpdatedAt) {
			return nil, ErrStoreIntegrity
		}
		if _, _, err := store.validateSettlementReviewUsageProvenanceTx(tx, review, evidence, false); err != nil {
			return nil, err
		}
		return nil, nil
	case SettlementReviewStatusFinalizedHeld:
		if !found || !evidenceFound || !settlementReviewUsageEvidenceMatchesReview(evidence, review) ||
			resolution.ReviewID != review.ReviewID || resolution.TurnID != review.TurnID ||
			resolution.SettlementKey != review.SettlementKey ||
			resolution.ReviewRequestDigest != review.RequestDigest ||
			resolution.EvidenceID != evidence.EvidenceID ||
			resolution.EvidenceDigest != evidence.EvidenceDigest ||
			resolution.PricingSnapshotDigest != evidence.PricingSnapshotDigest ||
			resolution.UsedUnits != evidence.UsedUnits || resolution.CreatedAt.Before(evidence.CreatedAt) ||
			!resolution.CreatedAt.Equal(review.UpdatedAt) {
			return nil, ErrStoreIntegrity
		}
		if _, _, err := store.validateSettlementReviewUsageProvenanceTx(tx, review, evidence, false); err != nil {
			return nil, err
		}
		return &resolution, nil
	default:
		return nil, ErrStoreIntegrity
	}
}

// ResolveSettlementReview records a positive financial finalize derived from
// immutable trusted-meter evidence and requires the same sealed usage-capable
// Authority. It never releases or re-dispatches a held Effect. Authentication,
// approval and HTTP mounting are intentionally outside this boundary.
func (store *SQLStore) ResolveSettlementReview(
	ctx context.Context,
	command ResolveSettlementReviewCommand,
) (ResolveSettlementReviewResult, error) {
	if err := contextError(ctx); err != nil {
		return ResolveSettlementReviewResult{}, err
	}
	if err := command.Validate(); err != nil {
		return ResolveSettlementReviewResult{}, err
	}
	if store == nil {
		return ResolveSettlementReviewResult{}, ErrSettlementReviewResolutionUnavailable
	}

	var result ResolveSettlementReviewResult
	var authority SettlementReviewResolutionAuthority
	bindingLocked := false
	defer func() {
		if bindingLocked {
			store.settlementMu.RUnlock()
		}
	}()
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		turnRow, err := store.lockTurn(tx, "turn_id = ?", string(command.TurnID))
		if errors.Is(err, ErrTurnNotFound) {
			return ErrSettlementReviewNotFound
		}
		if err != nil {
			return err
		}
		turn, err := turnRow.toTurn()
		if err != nil || !turn.Status.Terminal() {
			return ErrStoreIntegrity
		}
		review, found, err := store.lookupSettlementReviewTx(tx, command.TurnID, true)
		if err != nil {
			return err
		}
		if !found || review.ReviewID != command.ReviewID {
			return ErrSettlementReviewNotFound
		}
		if review.RequestDigest != command.ExpectedRequestDigest {
			return ErrSettlementReviewResolutionConflict
		}
		store.settlementMu.RLock()
		bindingLocked = true
		authority, err = store.settlementReviewResolutionAuthorityLocked()
		if err != nil {
			return err
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return err
		}

		evidence, found, err := store.lookupSettlementReviewUsageEvidenceTx(tx, review.ReviewID, true)
		if err != nil {
			return err
		}
		if !found {
			return ErrStoreIntegrity
		}
		if command.EvidenceID != evidence.EvidenceID ||
			command.ExpectedEvidenceDigest != evidence.EvidenceDigest {
			return ErrSettlementReviewResolutionConflict
		}
		decisionDigest := settlementReviewResolutionDecisionDigest(review, evidence, command)
		if review.Status == SettlementReviewStatusFinalizedHeld {
			resolution, found, err := store.lookupSettlementReviewResolutionTx(tx, review.ReviewID, false)
			if err != nil {
				return err
			}
			if !found {
				return ErrStoreIntegrity
			}
			if resolution.DecisionDigest != decisionDigest {
				return ErrSettlementReviewResolutionConflict
			}
			result = ResolveSettlementReviewResult{Review: review, Resolution: resolution, Replay: true}
			return nil
		}
		if review.Status != SettlementReviewStatusMeteredHeld {
			return ErrStoreIntegrity
		}

		authorityCommand := SettlementReviewResolutionAuthorityCommand{
			Review: review, Evidence: evidence, PrincipalID: turn.PrincipalID,
			ResolutionID: settlementReviewResolutionID(review.ReviewID), DecisionDigest: decisionDigest,
			Intent: SettlementIntentFinalize, UsedUnits: evidence.UsedUnits, ActorID: command.ActorID,
			Reason:                SettlementReviewResolutionReasonMeteredUsageConfirmed,
			EvidenceDigest:        evidence.EvidenceDigest,
			PricingSnapshotDigest: evidence.PricingSnapshotDigest,
		}
		if err := authorityCommand.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		receipt, err := authority.ResolveReview(tx, authorityCommand)
		if err != nil {
			if errors.Is(err, ErrSettlementBindingInvalid) ||
				errors.Is(err, ErrSettlementReviewResolutionUnavailable) {
				return err
			}
			if errors.Is(err, ErrSettlementReviewUnitsExceedReserved) {
				return ErrSettlementReviewUnitsExceedReserved
			}
			return ErrSettlementReviewResolutionFailed
		}
		if err := receipt.Validate(authorityCommand); err != nil {
			if errors.Is(err, ErrSettlementReviewUnitsExceedReserved) {
				return ErrSettlementReviewUnitsExceedReserved
			}
			return ErrSettlementReviewResolutionFailed
		}

		resolvedAt, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		if resolvedAt.Before(review.UpdatedAt) {
			resolvedAt = review.UpdatedAt
		}
		resolution := SettlementReviewResolutionRecord{
			ResolutionID: authorityCommand.ResolutionID, ReviewID: review.ReviewID,
			TurnID: review.TurnID, SettlementKey: review.SettlementKey,
			ReviewRequestDigest: review.RequestDigest, EvidenceID: evidence.EvidenceID,
			PricingSnapshotDigest: evidence.PricingSnapshotDigest, DecisionDigest: decisionDigest,
			Intent: SettlementIntentFinalize, UsedUnits: evidence.UsedUnits, ReservedUnits: receipt.ReservedUnits,
			ActorID: command.ActorID, Reason: SettlementReviewResolutionReasonMeteredUsageConfirmed,
			EvidenceDigest:         evidence.EvidenceDigest,
			AuthorityReceiptDigest: receipt.ReceiptDigest, CreatedAt: resolvedAt,
		}
		resolution.ResolutionDigest = settlementReviewResolutionDigest(resolution)
		resolutionRow, err := settlementReviewResolutionToSQLRow(resolution)
		if err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&resolutionRow).Error; err != nil {
			return err
		}
		updated := tx.Table(SQLSettlementReviewTable).
			Where("review_id = ? AND request_digest = ? AND status = ?",
				review.ReviewID, review.RequestDigest, SettlementReviewStatusMeteredHeld).
			UpdateColumns(map[string]any{
				"status": SettlementReviewStatusFinalizedHeld, "updated_at": resolvedAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrStoreIntegrity
		}
		review.Status = SettlementReviewStatusFinalizedHeld
		review.UpdatedAt = resolvedAt
		if err := review.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return err
		}
		result = ResolveSettlementReviewResult{Review: review, Resolution: resolution}
		return nil
	})
	if txErr != nil {
		return ResolveSettlementReviewResult{}, store.normalize("resolve-settlement-review", txErr)
	}
	return result, nil
}

// ListSettlementReviewResolutions is a bounded read-only audit model. It does
// not grant financial or Effect authority and is not mounted by any route.
func (store *SQLStore) ListSettlementReviewResolutions(
	ctx context.Context,
	query ListSettlementReviewResolutionsQuery,
) ([]SettlementReviewResolutionRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	var rows []sqlSettlementReviewResolutionRow
	if err := store.db.WithContext(ctx).Table(SQLSettlementReviewResolutionTable).
		Order("created_at ASC, id ASC").Limit(query.limit()).Find(&rows).Error; err != nil {
		return nil, store.normalize("list-settlement-review-resolutions", err)
	}
	resolutions := make([]SettlementReviewResolutionRecord, 0, len(rows))
	for _, row := range rows {
		resolution, err := row.toRecord()
		if err != nil {
			return nil, store.integrity("list-settlement-review-resolutions")
		}
		review, found, err := store.lookupSettlementReviewTx(store.db.WithContext(ctx), resolution.TurnID, false)
		if err != nil {
			return nil, store.normalize("list-settlement-review-resolutions", err)
		}
		if !found || review.ReviewID != resolution.ReviewID {
			return nil, store.integrity("list-settlement-review-resolutions")
		}
		validated, err := store.validateSettlementReviewResolutionStateTx(store.db.WithContext(ctx), review)
		if err != nil || validated == nil || validated.ResolutionDigest != resolution.ResolutionDigest {
			if err != nil {
				return nil, store.normalize("list-settlement-review-resolutions", err)
			}
			return nil, store.integrity("list-settlement-review-resolutions")
		}
		resolutions = append(resolutions, resolution)
	}
	return resolutions, nil
}
