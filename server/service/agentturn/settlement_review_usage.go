package agentturn

import (
	"bytes"
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
	SQLSettlementReviewUsageEvidenceTable = "w_agent_turn_settlement_usage_evidence"

	MaxSettlementReviewUsageFieldBytes = 256
)

// CaptureSettlementReviewUsageEvidenceCommand identifies the immutable Review
// to measure. It deliberately carries no units, pricing, policy or digest that
// a caller could turn into a commercial assertion.
type CaptureSettlementReviewUsageEvidenceCommand struct {
	TurnID                agentv1.TurnID
	ReviewID              string
	ExpectedRequestDigest string
}

func (command CaptureSettlementReviewUsageEvidenceCommand) Validate() error {
	if err := validatePathSegment("turnId", string(command.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("reviewId", command.ReviewID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	return validateSettlementReviewSHA256Digest("expectedRequestDigest", command.ExpectedRequestDigest)
}

// MeasureSettlementReviewUsageCommand is assembled by the kernel only after
// locking and validating the terminal Turn and Review. It never contains a
// caller-selected billing policy or usage value.
type MeasureSettlementReviewUsageCommand struct {
	Review      SettlementReviewRecord
	PrincipalID PrincipalID
	Plugin      agentv1.EventPluginRef
	EvidenceID  string
}

func (command MeasureSettlementReviewUsageCommand) Validate() error {
	if err := command.Review.Validate(); err != nil || command.Review.Status != SettlementReviewStatusPending {
		return ErrStoreIntegrity
	}
	if err := validateBoundedText("principalId", string(command.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := command.Plugin.Validate(); err != nil {
		return err
	}
	if err := validatePluginRef(command.Plugin); err != nil {
		return err
	}
	if command.EvidenceID != settlementReviewUsageEvidenceID(command.Review.ReviewID) {
		return ErrStoreIntegrity
	}
	return nil
}

// SettlementReviewUsageAuthorityReceipt is the trusted meter's transaction-
// local result. Every commercial value is returned by the sealed Authority;
// none is accepted from the Capture caller.
type SettlementReviewUsageAuthorityReceipt struct {
	EvidenceID            string
	ReviewID              string
	TurnID                agentv1.TurnID
	SettlementKey         string
	ReviewRequestDigest   string
	Plugin                agentv1.EventPluginRef
	BillingPolicyKey      string
	PricingSnapshotDigest string
	MeterKey              string
	MeterVersion          string
	MeterBuildDigest      string
	UsageSourceDigest     string
	MeasurementDigest     string
	UsedUnits             int64
	MeterReceiptDigest    string
}

func (receipt SettlementReviewUsageAuthorityReceipt) Validate(
	command MeasureSettlementReviewUsageCommand,
) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if receipt.EvidenceID != command.EvidenceID || receipt.ReviewID != command.Review.ReviewID ||
		receipt.TurnID != command.Review.TurnID || receipt.SettlementKey != command.Review.SettlementKey ||
		receipt.ReviewRequestDigest != command.Review.RequestDigest || receipt.Plugin != command.Plugin {
		return ErrStoreIntegrity
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "billingPolicyKey", value: receipt.BillingPolicyKey},
		{name: "meterKey", value: receipt.MeterKey},
		{name: "meterVersion", value: receipt.MeterVersion},
	} {
		if err := validatePrintableASCII(field.name, field.value, MaxSettlementReviewUsageFieldBytes); err != nil {
			return err
		}
	}
	for name, digest := range map[string]string{
		"pricingSnapshotDigest": receipt.PricingSnapshotDigest,
		"meterBuildDigest":      receipt.MeterBuildDigest,
		"usageSourceDigest":     receipt.UsageSourceDigest,
		"measurementDigest":     receipt.MeasurementDigest,
		"meterReceiptDigest":    receipt.MeterReceiptDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	if receipt.UsedUnits < 1 || receipt.UsedUnits > int64(MaxDurableSequence) {
		return fmt.Errorf("usedUnits must be between 1 and %d", MaxDurableSequence)
	}
	return nil
}

// SettlementReviewUsageAuthority is the historical v3 compatibility
// capability. It can resolve its existing Evidence and retain v3 completed
// terminalization, but cannot create provider-provenance Evidence.
type SettlementReviewUsageAuthority interface {
	SettlementReviewResolutionAuthority
	MeasureReview(
		tx *gorm.DB,
		command MeasureSettlementReviewUsageCommand,
	) (SettlementReviewUsageAuthorityReceipt, error)
}

// MeasureSettlementReviewProviderUsageCommand is assembled exclusively from
// one locked provider-aware Review, its exact immutable Meter Release and the
// complete eligible journal receipt set. The Authority may interpret the
// frozen pricing and canonical Provider payloads, but cannot replace their
// provenance or choose another Meter.
type MeasureSettlementReviewProviderUsageCommand struct {
	Review            SettlementReviewRecord
	PrincipalID       PrincipalID
	Plugin            agentv1.EventPluginRef
	EvidenceID        string
	MeterRelease      UsageMeterReleaseRecord
	Sources           []ProviderUsageJournalRecord
	UsageSourceDigest string
}

func (command MeasureSettlementReviewProviderUsageCommand) Validate() error {
	if err := command.Review.Validate(); err != nil ||
		command.Review.Status != SettlementReviewStatusPending ||
		!settlementReviewProviderUsageAware(command.Review) {
		return ErrStoreIntegrity
	}
	if err := validateBoundedText("principalId", string(command.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if command.Plugin.Validate() != nil || validatePluginRef(command.Plugin) != nil ||
		command.EvidenceID != settlementReviewUsageEvidenceID(command.Review.ReviewID) ||
		command.MeterRelease.Validate() != nil || command.MeterRelease.Plugin != command.Plugin ||
		len(command.Sources) < 1 || len(command.Sources) > MaxProviderUsageSources {
		return ErrStoreIntegrity
	}
	if err := validateSettlementReviewSHA256Digest("usageSourceDigest", command.UsageSourceDigest); err != nil {
		return err
	}
	for index, source := range command.Sources {
		if source.Validate() != nil || source.TurnID != command.Review.TurnID ||
			source.Plugin != command.Plugin || source.MeterReleaseID != command.MeterRelease.ReleaseID ||
			!command.MeterRelease.containsSource(source.sourceRegistration()) ||
			!settlementReviewProviderUsageSourceEligible(command.Review, source) ||
			(index > 0 && command.Sources[index-1].ReceiptID >= source.ReceiptID) {
			return ErrStoreIntegrity
		}
	}
	if command.UsageSourceDigest != settlementReviewProviderUsageSourceDigest(
		command.Review, command.MeterRelease, command.Sources,
	) {
		return ErrStoreIntegrity
	}
	return nil
}

// SettlementReviewProviderUsageAuthorityReceipt is intentionally minimal:
// release identity, pricing, registry, source digest and meter receipt digest
// are kernel-owned. The sealed Authority can return only its measurement and
// the positive unit result.
type SettlementReviewProviderUsageAuthorityReceipt struct {
	MeasurementDigest string
	UsedUnits         int64
}

func (receipt SettlementReviewProviderUsageAuthorityReceipt) Validate(
	command MeasureSettlementReviewProviderUsageCommand,
) error {
	if err := command.Validate(); err != nil {
		return err
	}
	if err := validateSettlementReviewSHA256Digest("measurementDigest", receipt.MeasurementDigest); err != nil {
		return err
	}
	if receipt.UsedUnits < 1 || receipt.UsedUnits > int64(MaxDurableSequence) {
		return fmt.Errorf("usedUnits must be between 1 and %d", MaxDurableSequence)
	}
	return nil
}

// SettlementReviewProviderUsageAuthority is the only new-Evidence authority.
// It remains resolution-capable so the same exact sealed commercial boundary
// owns measurement and finalization.
type SettlementReviewProviderUsageAuthority interface {
	SettlementReviewResolutionAuthority
	MeasureProviderUsage(
		tx *gorm.DB,
		command MeasureSettlementReviewProviderUsageCommand,
	) (SettlementReviewProviderUsageAuthorityReceipt, error)
}

// SettlementReviewUsageEvidenceRecord is the append-only bridge from a
// trusted raw measurement and frozen pricing snapshot to positive settlement
// units. The kernel stores the bindings but never interprets pricing math.
type SettlementReviewUsageEvidenceRecord struct {
	EvidenceID            string
	ReviewID              string
	TurnID                agentv1.TurnID
	SettlementKey         string
	ReviewRequestDigest   string
	Plugin                agentv1.EventPluginRef
	BillingPolicyKey      string
	PricingSnapshotDigest string
	MeterKey              string
	MeterVersion          string
	MeterBuildDigest      string
	MeterReleaseID        string
	UsageSourceDigest     string
	SourceReceiptCount    int
	MeasurementDigest     string
	UsedUnits             int64
	MeterReceiptDigest    string
	EvidenceDigest        string
	CreatedAt             time.Time
}

func (evidence SettlementReviewUsageEvidenceRecord) Validate() error {
	if err := validatePrintableASCII("evidenceId", evidence.EvidenceID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("reviewId", evidence.ReviewID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("turnId", string(evidence.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("settlementKey", evidence.SettlementKey, MaxSettlementKeyBytes); err != nil {
		return err
	}
	if err := evidence.Plugin.Validate(); err != nil {
		return err
	}
	if err := validatePluginRef(evidence.Plugin); err != nil {
		return err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "billingPolicyKey", value: evidence.BillingPolicyKey},
		{name: "meterKey", value: evidence.MeterKey},
		{name: "meterVersion", value: evidence.MeterVersion},
		{name: "meterReleaseId", value: evidence.MeterReleaseID},
	} {
		if err := validatePrintableASCII(field.name, field.value, MaxSettlementReviewUsageFieldBytes); err != nil {
			return err
		}
	}
	for name, digest := range map[string]string{
		"reviewRequestDigest":   evidence.ReviewRequestDigest,
		"pricingSnapshotDigest": evidence.PricingSnapshotDigest,
		"meterBuildDigest":      evidence.MeterBuildDigest,
		"usageSourceDigest":     evidence.UsageSourceDigest,
		"measurementDigest":     evidence.MeasurementDigest,
		"meterReceiptDigest":    evidence.MeterReceiptDigest,
		"evidenceDigest":        evidence.EvidenceDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	if evidence.UsedUnits < 1 || evidence.UsedUnits > int64(MaxDurableSequence) {
		return ErrStoreIntegrity
	}
	if evidence.SourceReceiptCount < 1 || evidence.SourceReceiptCount > MaxProviderUsageSources {
		return ErrStoreIntegrity
	}
	createdAt, err := canonicalExecutionTime(evidence.CreatedAt)
	if err != nil || !createdAt.Equal(evidence.CreatedAt) {
		return ErrStoreIntegrity
	}
	if evidence.EvidenceID != settlementReviewUsageEvidenceID(evidence.ReviewID) ||
		evidence.EvidenceDigest != settlementReviewUsageEvidenceDigest(evidence) {
		return ErrStoreIntegrity
	}
	return nil
}

type CaptureSettlementReviewUsageEvidenceResult struct {
	Review   SettlementReviewRecord
	Evidence SettlementReviewUsageEvidenceRecord
	Replay   bool
}

type ListSettlementReviewUsageEvidenceQuery struct {
	Limit int
}

func (query ListSettlementReviewUsageEvidenceQuery) limit() int {
	if query.Limit <= 0 {
		return DefaultSettlementReviewListLimit
	}
	return query.Limit
}

func (query ListSettlementReviewUsageEvidenceQuery) Validate() error {
	if query.Limit < 0 || query.Limit > MaxSettlementReviewListLimit {
		return fmt.Errorf("limit must be between 0 and %d", MaxSettlementReviewListLimit)
	}
	return nil
}

type sqlSettlementReviewUsageEvidenceRow struct {
	ID                    uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	EvidenceID            string    `gorm:"column:evidence_id"`
	ReviewID              string    `gorm:"column:review_id"`
	TurnID                string    `gorm:"column:turn_id"`
	SettlementKey         string    `gorm:"column:settlement_key"`
	ReviewRequestDigest   string    `gorm:"column:review_request_digest"`
	PluginID              string    `gorm:"column:plugin_id"`
	PluginVersion         string    `gorm:"column:plugin_version"`
	PluginReleaseDigest   string    `gorm:"column:plugin_release_digest"`
	BillingPolicyKey      string    `gorm:"column:billing_policy_key"`
	PricingSnapshotDigest string    `gorm:"column:pricing_snapshot_digest"`
	MeterKey              string    `gorm:"column:meter_key"`
	MeterVersion          string    `gorm:"column:meter_version"`
	MeterBuildDigest      string    `gorm:"column:meter_build_digest"`
	MeterReleaseID        string    `gorm:"column:meter_release_id"`
	UsageSourceDigest     string    `gorm:"column:usage_source_digest"`
	SourceReceiptCount    int       `gorm:"column:source_receipt_count"`
	MeasurementDigest     string    `gorm:"column:measurement_digest"`
	UsedUnits             int64     `gorm:"column:used_units"`
	MeterReceiptDigest    string    `gorm:"column:meter_receipt_digest"`
	EvidenceDigest        string    `gorm:"column:evidence_digest"`
	CreatedAt             time.Time `gorm:"column:created_at"`
}

func (sqlSettlementReviewUsageEvidenceRow) TableName() string {
	return SQLSettlementReviewUsageEvidenceTable
}

func (row sqlSettlementReviewUsageEvidenceRow) toRecord() (SettlementReviewUsageEvidenceRecord, error) {
	evidence := SettlementReviewUsageEvidenceRecord{
		EvidenceID: row.EvidenceID, ReviewID: row.ReviewID, TurnID: agentv1.TurnID(row.TurnID),
		SettlementKey: row.SettlementKey, ReviewRequestDigest: row.ReviewRequestDigest,
		Plugin: agentv1.EventPluginRef{
			ID: row.PluginID, Version: row.PluginVersion, ReleaseDigest: row.PluginReleaseDigest,
		},
		BillingPolicyKey: row.BillingPolicyKey, PricingSnapshotDigest: row.PricingSnapshotDigest,
		MeterKey: row.MeterKey, MeterVersion: row.MeterVersion, MeterBuildDigest: row.MeterBuildDigest,
		MeterReleaseID: row.MeterReleaseID, UsageSourceDigest: row.UsageSourceDigest,
		SourceReceiptCount: row.SourceReceiptCount, MeasurementDigest: row.MeasurementDigest,
		UsedUnits: row.UsedUnits, MeterReceiptDigest: row.MeterReceiptDigest,
		EvidenceDigest: row.EvidenceDigest, CreatedAt: row.CreatedAt.UTC(),
	}
	if err := evidence.Validate(); err != nil {
		return SettlementReviewUsageEvidenceRecord{}, ErrStoreIntegrity
	}
	return evidence, nil
}

func settlementReviewUsageEvidenceToSQLRow(
	evidence SettlementReviewUsageEvidenceRecord,
) (sqlSettlementReviewUsageEvidenceRow, error) {
	if err := evidence.Validate(); err != nil {
		return sqlSettlementReviewUsageEvidenceRow{}, err
	}
	return sqlSettlementReviewUsageEvidenceRow{
		EvidenceID: evidence.EvidenceID, ReviewID: evidence.ReviewID, TurnID: string(evidence.TurnID),
		SettlementKey: evidence.SettlementKey, ReviewRequestDigest: evidence.ReviewRequestDigest,
		PluginID: evidence.Plugin.ID, PluginVersion: evidence.Plugin.Version,
		PluginReleaseDigest: evidence.Plugin.ReleaseDigest, BillingPolicyKey: evidence.BillingPolicyKey,
		PricingSnapshotDigest: evidence.PricingSnapshotDigest, MeterKey: evidence.MeterKey,
		MeterVersion: evidence.MeterVersion, MeterBuildDigest: evidence.MeterBuildDigest,
		MeterReleaseID: evidence.MeterReleaseID, UsageSourceDigest: evidence.UsageSourceDigest,
		SourceReceiptCount: evidence.SourceReceiptCount, MeasurementDigest: evidence.MeasurementDigest,
		UsedUnits: evidence.UsedUnits, MeterReceiptDigest: evidence.MeterReceiptDigest,
		EvidenceDigest: evidence.EvidenceDigest, CreatedAt: evidence.CreatedAt.UTC(),
	}, nil
}

func settlementReviewUsageEvidenceID(reviewID string) string {
	hash := sha256.New()
	settlementReviewHashParts(hash, "settlement-review-usage-evidence-id-v1", reviewID)
	return hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewUsageEvidenceDigest(evidence SettlementReviewUsageEvidenceRecord) string {
	hash := sha256.New()
	settlementReviewHashParts(hash,
		"settlement-review-usage-evidence-receipt-v2", evidence.EvidenceID, evidence.ReviewID,
		string(evidence.TurnID), evidence.SettlementKey, evidence.ReviewRequestDigest,
		evidence.Plugin.ID, evidence.Plugin.Version, evidence.Plugin.ReleaseDigest,
		evidence.BillingPolicyKey, evidence.PricingSnapshotDigest, evidence.MeterKey,
		evidence.MeterVersion, evidence.MeterBuildDigest, evidence.MeterReleaseID,
		evidence.UsageSourceDigest, strconv.Itoa(evidence.SourceReceiptCount),
		evidence.MeasurementDigest, strconv.FormatInt(evidence.UsedUnits, 10),
		evidence.MeterReceiptDigest,
		evidence.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func settlementReviewUsageEvidenceMatchesReview(
	evidence SettlementReviewUsageEvidenceRecord,
	review SettlementReviewRecord,
) bool {
	return evidence.Validate() == nil && evidence.ReviewID == review.ReviewID &&
		evidence.TurnID == review.TurnID && evidence.SettlementKey == review.SettlementKey &&
		evidence.ReviewRequestDigest == review.RequestDigest
}

// SettlementReviewUsageEvidenceSourceRecord is one immutable edge from a
// Meter Evidence receipt to an attested scoped-adapter journal receipt.
type SettlementReviewUsageEvidenceSourceRecord struct {
	EvidenceID               string
	Ordinal                  int
	ReviewID                 string
	TurnID                   agentv1.TurnID
	SettlementKey            string
	ReviewRequestDigest      string
	MeterReleaseID           string
	UsageSourceDigest        string
	EvidenceDigest           string
	SourceReceiptCount       int
	ReceiptID                string
	SourceRegistrationDigest string
	SourceSchemaDigest       string
	CanonicalUsageDigest     string
	ProviderReceiptDigest    string
	JournalRecordDigest      string
	CreatedAt                time.Time
}

func (source SettlementReviewUsageEvidenceSourceRecord) Validate() error {
	for _, field := range []struct{ name, value string }{
		{"evidenceId", source.EvidenceID}, {"reviewId", source.ReviewID},
		{"settlementKey", source.SettlementKey}, {"meterReleaseId", source.MeterReleaseID},
		{"receiptId", source.ReceiptID},
	} {
		limit := MaxSettlementReviewIDBytes
		if field.name == "settlementKey" {
			limit = MaxSettlementKeyBytes
		}
		if err := validatePrintableASCII(field.name, field.value, limit); err != nil {
			return err
		}
	}
	if err := validatePathSegment("turnId", string(source.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"reviewRequestDigest":      source.ReviewRequestDigest,
		"usageSourceDigest":        source.UsageSourceDigest,
		"evidenceDigest":           source.EvidenceDigest,
		"sourceRegistrationDigest": source.SourceRegistrationDigest,
		"sourceSchemaDigest":       source.SourceSchemaDigest,
		"canonicalUsageDigest":     source.CanonicalUsageDigest,
		"providerReceiptDigest":    source.ProviderReceiptDigest,
		"journalRecordDigest":      source.JournalRecordDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	if source.SourceReceiptCount < 1 || source.SourceReceiptCount > MaxProviderUsageSources ||
		source.Ordinal < 0 || source.Ordinal >= source.SourceReceiptCount {
		return ErrStoreIntegrity
	}
	createdAt, err := canonicalExecutionTime(source.CreatedAt)
	if err != nil || !createdAt.Equal(source.CreatedAt) {
		return ErrStoreIntegrity
	}
	return nil
}

func (source SettlementReviewUsageEvidenceSourceRecord) matchesEvidence(
	evidence SettlementReviewUsageEvidenceRecord,
) bool {
	return source.Validate() == nil && evidence.Validate() == nil &&
		source.EvidenceID == evidence.EvidenceID && source.ReviewID == evidence.ReviewID &&
		source.TurnID == evidence.TurnID && source.SettlementKey == evidence.SettlementKey &&
		source.ReviewRequestDigest == evidence.ReviewRequestDigest &&
		source.MeterReleaseID == evidence.MeterReleaseID &&
		source.UsageSourceDigest == evidence.UsageSourceDigest &&
		source.EvidenceDigest == evidence.EvidenceDigest &&
		source.SourceReceiptCount == evidence.SourceReceiptCount &&
		source.CreatedAt.Equal(evidence.CreatedAt)
}

type sqlSettlementReviewUsageEvidenceSourceRow struct {
	ID                       uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	EvidenceID               string    `gorm:"column:evidence_id"`
	Ordinal                  int       `gorm:"column:ordinal"`
	ReviewID                 string    `gorm:"column:review_id"`
	TurnID                   string    `gorm:"column:turn_id"`
	SettlementKey            string    `gorm:"column:settlement_key"`
	ReviewRequestDigest      string    `gorm:"column:review_request_digest"`
	MeterReleaseID           string    `gorm:"column:meter_release_id"`
	UsageSourceDigest        string    `gorm:"column:usage_source_digest"`
	EvidenceDigest           string    `gorm:"column:evidence_digest"`
	SourceReceiptCount       int       `gorm:"column:source_receipt_count"`
	ReceiptID                string    `gorm:"column:receipt_id"`
	SourceRegistrationDigest string    `gorm:"column:source_registration_digest"`
	SourceSchemaDigest       string    `gorm:"column:source_schema_digest"`
	CanonicalUsageDigest     string    `gorm:"column:canonical_usage_digest"`
	ProviderReceiptDigest    string    `gorm:"column:provider_receipt_digest"`
	JournalRecordDigest      string    `gorm:"column:journal_record_digest"`
	CreatedAt                time.Time `gorm:"column:created_at"`
}

func (sqlSettlementReviewUsageEvidenceSourceRow) TableName() string {
	return SQLSettlementReviewUsageEvidenceSourceTable
}

func (row sqlSettlementReviewUsageEvidenceSourceRow) toRecord() (
	SettlementReviewUsageEvidenceSourceRecord,
	error,
) {
	record := SettlementReviewUsageEvidenceSourceRecord{
		EvidenceID: row.EvidenceID, Ordinal: row.Ordinal, ReviewID: row.ReviewID,
		TurnID: agentv1.TurnID(row.TurnID), SettlementKey: row.SettlementKey,
		ReviewRequestDigest: row.ReviewRequestDigest, MeterReleaseID: row.MeterReleaseID,
		UsageSourceDigest: row.UsageSourceDigest, EvidenceDigest: row.EvidenceDigest,
		SourceReceiptCount: row.SourceReceiptCount, ReceiptID: row.ReceiptID,
		SourceRegistrationDigest: row.SourceRegistrationDigest,
		SourceSchemaDigest:       row.SourceSchemaDigest, CanonicalUsageDigest: row.CanonicalUsageDigest,
		ProviderReceiptDigest: row.ProviderReceiptDigest, JournalRecordDigest: row.JournalRecordDigest,
		CreatedAt: row.CreatedAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return SettlementReviewUsageEvidenceSourceRecord{}, ErrStoreIntegrity
	}
	return record, nil
}

func settlementReviewUsageEvidenceSourceToSQLRow(
	source SettlementReviewUsageEvidenceSourceRecord,
) (sqlSettlementReviewUsageEvidenceSourceRow, error) {
	if err := source.Validate(); err != nil {
		return sqlSettlementReviewUsageEvidenceSourceRow{}, err
	}
	return sqlSettlementReviewUsageEvidenceSourceRow{
		EvidenceID: source.EvidenceID, Ordinal: source.Ordinal, ReviewID: source.ReviewID,
		TurnID: string(source.TurnID), SettlementKey: source.SettlementKey,
		ReviewRequestDigest: source.ReviewRequestDigest, MeterReleaseID: source.MeterReleaseID,
		UsageSourceDigest: source.UsageSourceDigest, EvidenceDigest: source.EvidenceDigest,
		SourceReceiptCount: source.SourceReceiptCount, ReceiptID: source.ReceiptID,
		SourceRegistrationDigest: source.SourceRegistrationDigest,
		SourceSchemaDigest:       source.SourceSchemaDigest, CanonicalUsageDigest: source.CanonicalUsageDigest,
		ProviderReceiptDigest: source.ProviderReceiptDigest, JournalRecordDigest: source.JournalRecordDigest,
		CreatedAt: source.CreatedAt.UTC(),
	}, nil
}

func (store *SQLStore) settlementReviewUsageAuthorityLocked() (
	SettlementReviewUsageAuthority,
	error,
) {
	if store == nil || store.settlementViolated {
		return nil, ErrSettlementBindingInvalid
	}
	binding := store.settlementBinding
	if binding == nil || !binding.usageAware {
		return nil, ErrSettlementReviewUsageUnavailable
	}
	if binding.store != store || binding.marker == nil || *binding.marker != 1 ||
		store.settlement == nil {
		return nil, ErrSettlementBindingInvalid
	}
	authority, ok := store.settlement.(SettlementReviewUsageAuthority)
	if !ok || settlementAuthorityMissing(authority) {
		return nil, ErrSettlementReviewUsageUnavailable
	}
	return authority, nil
}

func (store *SQLStore) settlementReviewProviderUsageAuthorityLocked() (
	SettlementReviewProviderUsageAuthority,
	*ProviderUsageJournal,
	error,
) {
	if store == nil || store.settlementViolated {
		return nil, nil, ErrSettlementBindingInvalid
	}
	binding := store.settlementBinding
	if binding == nil || !binding.providerUsageAware {
		return nil, nil, ErrSettlementReviewUsageUnavailable
	}
	if binding.store != store || binding.marker == nil || *binding.marker != 1 ||
		binding.providerJournal == nil || !binding.providerJournal.MatchesStore(store) ||
		store.settlement == nil {
		return nil, nil, ErrSettlementBindingInvalid
	}
	authority, ok := store.settlement.(SettlementReviewProviderUsageAuthority)
	if !ok || settlementAuthorityMissing(authority) {
		return nil, nil, ErrSettlementReviewUsageUnavailable
	}
	return authority, binding.providerJournal, nil
}

func (store *SQLStore) lookupSettlementReviewUsageEvidenceTx(
	tx *gorm.DB,
	reviewID string,
	lock bool,
) (SettlementReviewUsageEvidenceRecord, bool, error) {
	if tx == nil {
		return SettlementReviewUsageEvidenceRecord{}, false, ErrStoreIntegrity
	}
	query := tx.Table(SQLSettlementReviewUsageEvidenceTable).Where("review_id = ?", reviewID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sqlSettlementReviewUsageEvidenceRow
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SettlementReviewUsageEvidenceRecord{}, false, nil
	}
	if err != nil {
		return SettlementReviewUsageEvidenceRecord{}, false, err
	}
	evidence, err := row.toRecord()
	if err != nil {
		return SettlementReviewUsageEvidenceRecord{}, false, err
	}
	return evidence, true, nil
}

func settlementReviewProviderUsageSourceEligible(
	review SettlementReviewRecord,
	record ProviderUsageJournalRecord,
) bool {
	if !settlementReviewProviderUsageAware(review) || record.Validate() != nil ||
		record.TurnID != review.TurnID {
		return false
	}
	switch review.Source {
	case SettlementReviewSourceExecutorCompletion, SettlementReviewSourceExecutorTerminal,
		SettlementReviewSourceReconcileTerminal:
		return record.FencingToken <= review.FencingToken
	default:
		return false
	}
}

func (store *SQLStore) lookupSettlementReviewProviderUsageTx(
	tx *gorm.DB,
	review SettlementReviewRecord,
	lock bool,
) ([]ProviderUsageJournalRecord, error) {
	if tx == nil || !settlementReviewProviderUsageAware(review) {
		return nil, ErrStoreIntegrity
	}
	query := tx.Table(SQLProviderUsageJournalTable).Where("turn_id = ?", string(review.TurnID))
	switch review.Source {
	case SettlementReviewSourceExecutorCompletion, SettlementReviewSourceExecutorTerminal,
		SettlementReviewSourceReconcileTerminal:
		query = query.Where("fencing_token <= ?", int64(review.FencingToken))
	default:
		return nil, ErrStoreIntegrity
	}
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []sqlProviderUsageJournalRow
	if err := query.Order("receipt_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	records := make([]ProviderUsageJournalRecord, 0, len(rows))
	for index, row := range rows {
		record, err := row.toRecord()
		if err != nil || !settlementReviewProviderUsageSourceEligible(review, record) ||
			(index > 0 && records[index-1].ReceiptID >= record.ReceiptID) {
			return nil, ErrStoreIntegrity
		}
		attemptRow, found, err := store.lookupAttemptByIDTx(tx, record.AttemptID, lock)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, ErrStoreIntegrity
		}
		attempt, err := attemptRow.toAttempt()
		if err != nil || attempt.TurnID != record.TurnID ||
			attempt.FencingToken != record.FencingToken || !attempt.Status.Terminal() {
			return nil, ErrStoreIntegrity
		}
		records = append(records, record)
	}
	return records, nil
}

func settlementReviewProviderUsageSourceDigest(
	review SettlementReviewRecord,
	release UsageMeterReleaseRecord,
	sources []ProviderUsageJournalRecord,
) string {
	parts := []string{
		review.ReviewID, string(review.TurnID), review.SettlementKey, review.RequestDigest,
		release.ReleaseID, release.ReleaseDigest, release.PluginSnapshotDigest,
		release.SourceRegistryDigest, strconv.Itoa(len(sources)),
	}
	for _, source := range sources {
		parts = append(parts,
			source.ReceiptID, source.AttemptID, strconv.FormatInt(int64(source.FencingToken), 10),
			source.SourceRegistrationDigest, source.SourceSchemaDigest,
			source.CanonicalUsageDigest, source.ProviderReceiptDigest, source.JournalRecordDigest,
		)
	}
	return providerUsageDigest("settlement-review-provider-usage-source-v1", parts...)
}

func settlementReviewProviderMeterReceiptDigest(
	evidence SettlementReviewUsageEvidenceRecord,
	release UsageMeterReleaseRecord,
) string {
	return providerUsageDigest(
		"settlement-review-provider-meter-receipt-v1",
		evidence.EvidenceID, evidence.ReviewID, string(evidence.TurnID), evidence.SettlementKey,
		evidence.ReviewRequestDigest, release.ReleaseID, release.ReleaseDigest,
		evidence.UsageSourceDigest, strconv.Itoa(evidence.SourceReceiptCount),
		evidence.MeasurementDigest, strconv.FormatInt(evidence.UsedUnits, 10),
	)
}

func cloneUsageMeterRelease(release UsageMeterReleaseRecord) UsageMeterReleaseRecord {
	cloned := release
	cloned.PricingSnapshotJSON = append([]byte(nil), release.PricingSnapshotJSON...)
	cloned.SourceRegistryJSON = append([]byte(nil), release.SourceRegistryJSON...)
	return cloned
}

func cloneProviderUsageJournalRecords(
	sources []ProviderUsageJournalRecord,
) []ProviderUsageJournalRecord {
	cloned := make([]ProviderUsageJournalRecord, len(sources))
	for index, source := range sources {
		cloned[index] = source
		cloned[index].ProviderUsageJSON = append([]byte(nil), source.ProviderUsageJSON...)
	}
	return cloned
}

func usageMeterReleaseEqual(left, right UsageMeterReleaseRecord) bool {
	return left.Validate() == nil && right.Validate() == nil &&
		left.ReleaseID == right.ReleaseID && left.ReleaseDigest == right.ReleaseDigest &&
		left.Plugin == right.Plugin && left.PluginSnapshotDigest == right.PluginSnapshotDigest &&
		left.BillingPolicyKey == right.BillingPolicyKey &&
		left.PricingSnapshotDigest == right.PricingSnapshotDigest &&
		left.MeterKey == right.MeterKey && left.MeterVersion == right.MeterVersion &&
		left.MeterBuildDigest == right.MeterBuildDigest &&
		left.SourceRegistryDigest == right.SourceRegistryDigest && left.CreatedAt.Equal(right.CreatedAt) &&
		bytes.Equal(left.PricingSnapshotJSON, right.PricingSnapshotJSON) &&
		bytes.Equal(left.SourceRegistryJSON, right.SourceRegistryJSON)
}

func providerUsageJournalRecordsEqual(
	left, right []ProviderUsageJournalRecord,
) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Validate() != nil || right[index].Validate() != nil ||
			left[index].ReceiptID != right[index].ReceiptID ||
			left[index].JournalRecordDigest != right[index].JournalRecordDigest ||
			!bytes.Equal(left[index].ProviderUsageJSON, right[index].ProviderUsageJSON) {
			return false
		}
	}
	return true
}

func lookupSettlementReviewUsageEvidenceSourcesTx(
	tx *gorm.DB,
	evidenceID string,
	lock bool,
) ([]SettlementReviewUsageEvidenceSourceRecord, error) {
	if tx == nil {
		return nil, ErrStoreIntegrity
	}
	query := tx.Table(SQLSettlementReviewUsageEvidenceSourceTable).Where("evidence_id = ?", evidenceID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []sqlSettlementReviewUsageEvidenceSourceRow
	if err := query.Order("ordinal ASC").Limit(MaxProviderUsageSources + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > MaxProviderUsageSources {
		return nil, ErrStoreIntegrity
	}
	sources := make([]SettlementReviewUsageEvidenceSourceRecord, 0, len(rows))
	for _, row := range rows {
		source, err := row.toRecord()
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func settlementReviewUsageEvidenceSourceRecord(
	evidence SettlementReviewUsageEvidenceRecord,
	ordinal int,
	source ProviderUsageJournalRecord,
) SettlementReviewUsageEvidenceSourceRecord {
	return SettlementReviewUsageEvidenceSourceRecord{
		EvidenceID: evidence.EvidenceID, Ordinal: ordinal, ReviewID: evidence.ReviewID,
		TurnID: evidence.TurnID, SettlementKey: evidence.SettlementKey,
		ReviewRequestDigest: evidence.ReviewRequestDigest, MeterReleaseID: evidence.MeterReleaseID,
		UsageSourceDigest: evidence.UsageSourceDigest, EvidenceDigest: evidence.EvidenceDigest,
		SourceReceiptCount: evidence.SourceReceiptCount, ReceiptID: source.ReceiptID,
		SourceRegistrationDigest: source.SourceRegistrationDigest,
		SourceSchemaDigest:       source.SourceSchemaDigest, CanonicalUsageDigest: source.CanonicalUsageDigest,
		ProviderReceiptDigest: source.ProviderReceiptDigest, JournalRecordDigest: source.JournalRecordDigest,
		CreatedAt: evidence.CreatedAt,
	}
}

// validateSettlementReviewUsageProvenanceTx replays the complete immutable
// Review -> Evidence -> source edges -> Journal -> Meter Release chain. It
// also proves that the Evidence consumed the complete eligible receipt set.
func (store *SQLStore) validateSettlementReviewUsageProvenanceTx(
	tx *gorm.DB,
	review SettlementReviewRecord,
	evidence SettlementReviewUsageEvidenceRecord,
	lock bool,
) ([]ProviderUsageJournalRecord, UsageMeterReleaseRecord, error) {
	if tx == nil || !settlementReviewProviderUsageAware(review) ||
		!settlementReviewUsageEvidenceMatchesReview(evidence, review) ||
		(review.Status != SettlementReviewStatusMeteredHeld &&
			review.Status != SettlementReviewStatusFinalizedHeld) {
		return nil, UsageMeterReleaseRecord{}, ErrStoreIntegrity
	}
	release, found, err := lookupUsageMeterReleaseTx(tx, evidence.MeterReleaseID, lock)
	if err != nil {
		return nil, UsageMeterReleaseRecord{}, err
	}
	if !found || release.Plugin != evidence.Plugin ||
		evidence.BillingPolicyKey != release.BillingPolicyKey ||
		evidence.PricingSnapshotDigest != release.PricingSnapshotDigest ||
		evidence.MeterKey != release.MeterKey || evidence.MeterVersion != release.MeterVersion ||
		evidence.MeterBuildDigest != release.MeterBuildDigest {
		return nil, UsageMeterReleaseRecord{}, ErrStoreIntegrity
	}
	journalSources, err := store.lookupSettlementReviewProviderUsageTx(tx, review, lock)
	if err != nil {
		return nil, UsageMeterReleaseRecord{}, err
	}
	links, err := lookupSettlementReviewUsageEvidenceSourcesTx(tx, evidence.EvidenceID, lock)
	if err != nil {
		return nil, UsageMeterReleaseRecord{}, err
	}
	if len(journalSources) != evidence.SourceReceiptCount || len(links) != evidence.SourceReceiptCount ||
		int64(len(journalSources)) < review.Evidence.PriorProviderUsageCount ||
		evidence.UsageSourceDigest != settlementReviewProviderUsageSourceDigest(review, release, journalSources) ||
		evidence.MeterReceiptDigest != settlementReviewProviderMeterReceiptDigest(evidence, release) {
		return nil, UsageMeterReleaseRecord{}, ErrStoreIntegrity
	}
	for ordinal := range journalSources {
		journal := journalSources[ordinal]
		link := links[ordinal]
		if !link.matchesEvidence(evidence) || link.Ordinal != ordinal ||
			link.ReceiptID != journal.ReceiptID || link.SourceRegistrationDigest != journal.SourceRegistrationDigest ||
			link.SourceSchemaDigest != journal.SourceSchemaDigest ||
			link.CanonicalUsageDigest != journal.CanonicalUsageDigest ||
			link.ProviderReceiptDigest != journal.ProviderReceiptDigest ||
			link.JournalRecordDigest != journal.JournalRecordDigest ||
			journal.MeterReleaseID != release.ReleaseID || journal.Plugin != release.Plugin ||
			!release.containsSource(journal.sourceRegistration()) {
			return nil, UsageMeterReleaseRecord{}, ErrStoreIntegrity
		}
	}
	return journalSources, release, nil
}

// CaptureSettlementReviewUsageEvidence asks the exact sealed Authority to
// measure one locked Review. The caller can select the Review but cannot
// submit a price, unit count, policy, source receipt or evidence digest.
// P0-044 routes an ordinary completed command through an executor_completion
// Review first; this method is the only path that can attach trusted usage to
// that Review before an operator resolves the held reservation.
func (store *SQLStore) CaptureSettlementReviewUsageEvidence(
	ctx context.Context,
	command CaptureSettlementReviewUsageEvidenceCommand,
) (CaptureSettlementReviewUsageEvidenceResult, error) {
	if err := contextError(ctx); err != nil {
		return CaptureSettlementReviewUsageEvidenceResult{}, err
	}
	if err := command.Validate(); err != nil {
		return CaptureSettlementReviewUsageEvidenceResult{}, err
	}
	if store == nil {
		return CaptureSettlementReviewUsageEvidenceResult{}, ErrSettlementReviewUsageUnavailable
	}

	var result CaptureSettlementReviewUsageEvidenceResult
	var authority SettlementReviewProviderUsageAuthority
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
			return ErrSettlementReviewUsageConflict
		}
		store.settlementMu.RLock()
		bindingLocked = true
		var journal *ProviderUsageJournal
		authority, journal, err = store.settlementReviewProviderUsageAuthorityLocked()
		if err != nil {
			return err
		}
		if journal == nil || !journal.MatchesStore(store) {
			return ErrSettlementBindingInvalid
		}
		if !settlementReviewProviderUsageAware(review) {
			return ErrSettlementReviewUsageConflict
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return err
		}

		if review.Status == SettlementReviewStatusMeteredHeld || review.Status == SettlementReviewStatusFinalizedHeld {
			evidence, found, err := store.lookupSettlementReviewUsageEvidenceTx(tx, review.ReviewID, false)
			if err != nil {
				return err
			}
			if !found {
				return ErrStoreIntegrity
			}
			if _, _, err := store.validateSettlementReviewUsageProvenanceTx(
				tx, review, evidence, false,
			); err != nil {
				return err
			}
			result = CaptureSettlementReviewUsageEvidenceResult{Review: review, Evidence: evidence, Replay: true}
			return nil
		}
		if review.Status != SettlementReviewStatusPending {
			return ErrStoreIntegrity
		}

		sources, err := store.lookupSettlementReviewProviderUsageTx(tx, review, true)
		if err != nil {
			return err
		}
		if len(sources) > MaxProviderUsageSources {
			// Journal append happens after Provider usage has already been
			// incurred, so it preserves every receipt. The bounded Meter/Evidence
			// boundary must not silently price only the first 64: keep the Review
			// held and surface an explicit operational overflow instead.
			return ErrSettlementReviewUsageOverflow
		}
		if len(sources) == 0 {
			if review.Evidence.PriorProviderUsageCount != 0 {
				return ErrStoreIntegrity
			}
			return ErrSettlementReviewUsagePending
		}
		if int64(len(sources)) < review.Evidence.PriorProviderUsageCount {
			return ErrStoreIntegrity
		}
		meterReleaseID := sources[0].MeterReleaseID
		for _, source := range sources {
			if source.MeterReleaseID != meterReleaseID || source.Plugin != turn.Plugin {
				return ErrStoreIntegrity
			}
		}
		release, found, err := lookupUsageMeterReleaseTx(tx, meterReleaseID, true)
		if err != nil {
			return err
		}
		if !found || release.Plugin != turn.Plugin {
			return ErrStoreIntegrity
		}
		for _, source := range sources {
			if !release.containsSource(source.sourceRegistration()) {
				return ErrStoreIntegrity
			}
		}
		receiptIDs := make([]string, 0, len(sources))
		for _, source := range sources {
			receiptIDs = append(receiptIDs, source.ReceiptID)
		}
		var alreadyUsed int64
		if err := tx.Table(SQLSettlementReviewUsageEvidenceSourceTable).
			Where("receipt_id IN ?", receiptIDs).Count(&alreadyUsed).Error; err != nil {
			return err
		}
		if alreadyUsed != 0 {
			return ErrSettlementReviewUsageConflict
		}
		usageSourceDigest := settlementReviewProviderUsageSourceDigest(review, release, sources)
		authorityCommand := MeasureSettlementReviewProviderUsageCommand{
			Review: review, PrincipalID: turn.PrincipalID, Plugin: turn.Plugin,
			EvidenceID:   settlementReviewUsageEvidenceID(review.ReviewID),
			MeterRelease: cloneUsageMeterRelease(release),
			Sources:      cloneProviderUsageJournalRecords(sources), UsageSourceDigest: usageSourceDigest,
		}
		if err := authorityCommand.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		receipt, err := authority.MeasureProviderUsage(tx, authorityCommand)
		if err != nil {
			if errors.Is(err, ErrSettlementBindingInvalid) ||
				errors.Is(err, ErrSettlementReviewUsageUnavailable) {
				return err
			}
			return ErrSettlementReviewUsageFailed
		}
		if err := receipt.Validate(authorityCommand); err != nil {
			return ErrSettlementReviewUsageFailed
		}
		freshSources, err := store.lookupSettlementReviewProviderUsageTx(tx, review, false)
		if err != nil {
			return err
		}
		freshRelease, found, err := lookupUsageMeterReleaseTx(tx, release.ReleaseID, false)
		if err != nil {
			return err
		}
		if !found || !usageMeterReleaseEqual(release, freshRelease) ||
			!providerUsageJournalRecordsEqual(sources, freshSources) ||
			usageSourceDigest != settlementReviewProviderUsageSourceDigest(review, freshRelease, freshSources) {
			return ErrStoreIntegrity
		}

		measuredAt, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		if measuredAt.Before(review.UpdatedAt) {
			measuredAt = review.UpdatedAt
		}
		evidence := SettlementReviewUsageEvidenceRecord{
			EvidenceID: authorityCommand.EvidenceID, ReviewID: review.ReviewID, TurnID: review.TurnID,
			SettlementKey: review.SettlementKey, ReviewRequestDigest: review.RequestDigest,
			Plugin: turn.Plugin, BillingPolicyKey: release.BillingPolicyKey,
			PricingSnapshotDigest: release.PricingSnapshotDigest,
			MeterKey:              release.MeterKey, MeterVersion: release.MeterVersion,
			MeterBuildDigest: release.MeterBuildDigest, MeterReleaseID: release.ReleaseID,
			UsageSourceDigest: usageSourceDigest, SourceReceiptCount: len(sources),
			MeasurementDigest: receipt.MeasurementDigest, UsedUnits: receipt.UsedUnits,
			CreatedAt: measuredAt,
		}
		evidence.MeterReceiptDigest = settlementReviewProviderMeterReceiptDigest(evidence, release)
		evidence.EvidenceDigest = settlementReviewUsageEvidenceDigest(evidence)
		evidenceRow, err := settlementReviewUsageEvidenceToSQLRow(evidence)
		if err != nil {
			return ErrStoreIntegrity
		}
		if err := tx.Create(&evidenceRow).Error; err != nil {
			return err
		}
		for ordinal, source := range sources {
			link := settlementReviewUsageEvidenceSourceRecord(evidence, ordinal, source)
			row, err := settlementReviewUsageEvidenceSourceToSQLRow(link)
			if err != nil {
				return ErrStoreIntegrity
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		updated := tx.Table(SQLSettlementReviewTable).
			Where("review_id = ? AND request_digest = ? AND status = ?",
				review.ReviewID, review.RequestDigest, SettlementReviewStatusPending).
			UpdateColumns(map[string]any{
				"status": SettlementReviewStatusMeteredHeld, "updated_at": measuredAt,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrStoreIntegrity
		}
		review.Status = SettlementReviewStatusMeteredHeld
		review.UpdatedAt = measuredAt
		if err := review.Validate(); err != nil {
			return ErrStoreIntegrity
		}
		if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
			return err
		}
		result = CaptureSettlementReviewUsageEvidenceResult{Review: review, Evidence: evidence}
		return nil
	})
	if txErr != nil {
		return CaptureSettlementReviewUsageEvidenceResult{}, store.normalize("capture-settlement-review-usage", txErr)
	}
	return result, nil
}

// ListSettlementReviewUsageEvidence is an internal bounded audit read. It is
// not a meter, settlement mutation or Effect authority.
func (store *SQLStore) ListSettlementReviewUsageEvidence(
	ctx context.Context,
	query ListSettlementReviewUsageEvidenceQuery,
) ([]SettlementReviewUsageEvidenceRecord, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := query.Validate(); err != nil {
		return nil, err
	}
	var rows []sqlSettlementReviewUsageEvidenceRow
	if err := store.db.WithContext(ctx).Table(SQLSettlementReviewUsageEvidenceTable).
		Order("created_at ASC, id ASC").Limit(query.limit()).Find(&rows).Error; err != nil {
		return nil, store.normalize("list-settlement-review-usage", err)
	}
	evidence := make([]SettlementReviewUsageEvidenceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := row.toRecord()
		if err != nil {
			return nil, store.integrity("list-settlement-review-usage")
		}
		if err := store.validateSettlementReviewUsageEvidenceListCandidate(ctx, record); err != nil {
			return nil, store.normalize("list-settlement-review-usage", err)
		}
		evidence = append(evidence, record)
	}
	return evidence, nil
}

// validateSettlementReviewUsageEvidenceListCandidate retries one parent read
// when a valid metered_held -> finalized_held transition commits between the
// parent lookup and child-state validation. The retry does not forgive a
// missing, changed or smuggled Evidence row.
func (store *SQLStore) validateSettlementReviewUsageEvidenceListCandidate(
	ctx context.Context,
	evidence SettlementReviewUsageEvidenceRecord,
) error {
	database := store.db.WithContext(ctx)
	for attempt := 0; attempt < 2; attempt++ {
		review, found, err := store.lookupSettlementReviewTx(database, evidence.TurnID, false)
		if err != nil {
			return err
		}
		if !found || review.ReviewID != evidence.ReviewID ||
			!settlementReviewUsageEvidenceMatchesReview(evidence, review) {
			return ErrStoreIntegrity
		}
		turn, found, err := store.lookupTurnByID(ctx, evidence.TurnID)
		if err != nil {
			return err
		}
		if !found || !turn.Status.Terminal() || turn.Plugin != evidence.Plugin {
			return ErrStoreIntegrity
		}
		if _, err := store.validateSettlementReviewResolutionStateTx(database, review); err != nil {
			if attempt == 0 && errors.Is(err, ErrStoreIntegrity) {
				continue
			}
			return err
		}
		validated, found, err := store.lookupSettlementReviewUsageEvidenceTx(database, review.ReviewID, false)
		if err != nil {
			return err
		}
		if !found || validated.EvidenceDigest != evidence.EvidenceDigest {
			return ErrStoreIntegrity
		}
		return nil
	}
	return ErrStoreIntegrity
}
