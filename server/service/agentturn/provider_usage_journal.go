package agentturn

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	agentv1 "server/contracts/agent/v1"
)

const (
	SQLProviderUsageJournalTable = "w_agent_provider_usage_journal"
	SQLUsageMeterReleaseTable    = "w_agent_usage_meter_release"

	MaxProviderUsageFieldBytes       = 256
	MaxProviderUsageVerificationKind = 32
	MaxProviderUsageJSONBytes        = 64 << 10
	MaxProviderUsageSources          = 64
)

var (
	ErrProviderUsageJournalInvalid = errors.New("durable provider usage journal binding is invalid")
	ErrProviderUsageConflict       = errors.New("durable provider usage receipt conflicts with the journal")
	ErrProviderUsageForbidden      = errors.New("durable provider usage receipt is not authorized for this turn")
	ErrUsageMeterReleaseNotFound   = errors.New("durable usage meter release was not found")
	ErrUsageMeterReleaseConflict   = errors.New("durable usage meter release conflicts with the registry")
)

// ProviderUsageSourceRegistration is immutable release-owned provenance. A
// production Builder gives a Plugin only a recorder already scoped to one of
// these exact registrations; AppendAttestedProviderUsageCommand consequently
// cannot select a Provider, account, verification key, schema or Meter.
type ProviderUsageSourceRegistration struct {
	ProviderKey             string `json:"providerKey"`
	ProviderAccountDigest   string `json:"providerAccountDigest"`
	SourceKey               string `json:"sourceKey"`
	SourceVersion           string `json:"sourceVersion"`
	SourceBuildDigest       string `json:"sourceBuildDigest"`
	UsageSchemaKey          string `json:"usageSchemaKey"`
	UsageSchemaVersion      string `json:"usageSchemaVersion"`
	SourceSchemaDigest      string `json:"sourceSchemaDigest"`
	VerificationKind        string `json:"verificationKind"`
	VerificationKeyDigest   string `json:"verificationKeyDigest"`
	VerificationBuildDigest string `json:"verificationBuildDigest"`
	RegistrationDigest      string `json:"registrationDigest"`
}

type ProviderUsageSourceRegistrationSpec struct {
	ProviderKey             string
	ProviderAccountDigest   string
	SourceKey               string
	SourceVersion           string
	SourceBuildDigest       string
	UsageSchemaKey          string
	UsageSchemaVersion      string
	SourceSchemaDigest      string
	VerificationKind        string
	VerificationKeyDigest   string
	VerificationBuildDigest string
}

func NewProviderUsageSourceRegistration(
	spec ProviderUsageSourceRegistrationSpec,
) (ProviderUsageSourceRegistration, error) {
	registration := ProviderUsageSourceRegistration{
		ProviderKey: spec.ProviderKey, ProviderAccountDigest: spec.ProviderAccountDigest,
		SourceKey: spec.SourceKey, SourceVersion: spec.SourceVersion,
		SourceBuildDigest: spec.SourceBuildDigest, UsageSchemaKey: spec.UsageSchemaKey,
		UsageSchemaVersion: spec.UsageSchemaVersion, SourceSchemaDigest: spec.SourceSchemaDigest,
		VerificationKind: spec.VerificationKind, VerificationKeyDigest: spec.VerificationKeyDigest,
		VerificationBuildDigest: spec.VerificationBuildDigest,
	}
	registration.RegistrationDigest = providerUsageSourceRegistrationDigest(registration)
	if err := registration.Validate(); err != nil {
		return ProviderUsageSourceRegistration{}, err
	}
	return registration, nil
}

func (registration ProviderUsageSourceRegistration) Validate() error {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"providerKey", registration.ProviderKey, MaxProviderUsageFieldBytes},
		{"sourceKey", registration.SourceKey, MaxProviderUsageFieldBytes},
		{"sourceVersion", registration.SourceVersion, MaxProviderUsageFieldBytes},
		{"usageSchemaKey", registration.UsageSchemaKey, MaxProviderUsageFieldBytes},
		{"usageSchemaVersion", registration.UsageSchemaVersion, MaxProviderUsageFieldBytes},
		{"verificationKind", registration.VerificationKind, MaxProviderUsageVerificationKind},
	} {
		if err := validatePrintableASCII(field.name, field.value, field.limit); err != nil {
			return err
		}
	}
	for name, digest := range map[string]string{
		"providerAccountDigest":   registration.ProviderAccountDigest,
		"sourceBuildDigest":       registration.SourceBuildDigest,
		"sourceSchemaDigest":      registration.SourceSchemaDigest,
		"verificationKeyDigest":   registration.VerificationKeyDigest,
		"verificationBuildDigest": registration.VerificationBuildDigest,
		"registrationDigest":      registration.RegistrationDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	if registration.RegistrationDigest != providerUsageSourceRegistrationDigest(registration) {
		return ErrStoreIntegrity
	}
	return nil
}

func providerUsageSourceRegistrationDigest(registration ProviderUsageSourceRegistration) string {
	return providerUsageDigest("provider-usage-source-registration-v1",
		registration.ProviderKey, registration.ProviderAccountDigest,
		registration.SourceKey, registration.SourceVersion, registration.SourceBuildDigest,
		registration.UsageSchemaKey, registration.UsageSchemaVersion, registration.SourceSchemaDigest,
		registration.VerificationKind, registration.VerificationKeyDigest,
		registration.VerificationBuildDigest,
	)
}

// ProviderUsageSourceRegistry is the canonical, sorted and complete set of
// Provider sources accepted by one Meter Release.
type ProviderUsageSourceRegistry struct {
	Sources       []ProviderUsageSourceRegistration
	CanonicalJSON json.RawMessage
	Digest        string
}

func NewProviderUsageSourceRegistry(
	input []ProviderUsageSourceRegistration,
) (ProviderUsageSourceRegistry, error) {
	sources := append([]ProviderUsageSourceRegistration(nil), input...)
	if len(sources) < 1 || len(sources) > MaxProviderUsageSources {
		return ProviderUsageSourceRegistry{}, fmt.Errorf("provider usage source registry requires between 1 and %d sources", MaxProviderUsageSources)
	}
	for _, source := range sources {
		if err := source.Validate(); err != nil {
			return ProviderUsageSourceRegistry{}, err
		}
	}
	sort.Slice(sources, func(left, right int) bool {
		return sources[left].RegistrationDigest < sources[right].RegistrationDigest
	})
	for index := 1; index < len(sources); index++ {
		if sources[index-1].RegistrationDigest == sources[index].RegistrationDigest {
			return ProviderUsageSourceRegistry{}, ErrUsageMeterReleaseConflict
		}
	}
	raw, err := json.Marshal(sources)
	if err == nil {
		raw, err = canonicalProviderUsageJSON(raw)
	}
	if err != nil {
		return ProviderUsageSourceRegistry{}, ErrUsageMeterReleaseConflict
	}
	return ProviderUsageSourceRegistry{
		Sources: sources, CanonicalJSON: raw,
		Digest: providerUsageJSONDigest("provider-usage-source-registry-v1", raw),
	}, nil
}

func (registry ProviderUsageSourceRegistry) Validate() error {
	canonical, sources, err := decodeProviderUsageSourceRegistry(registry.CanonicalJSON)
	if err != nil || !bytes.Equal(canonical, registry.CanonicalJSON) ||
		registry.Digest != providerUsageJSONDigest("provider-usage-source-registry-v1", canonical) ||
		len(sources) != len(registry.Sources) {
		return ErrStoreIntegrity
	}
	for index := range sources {
		if sources[index] != registry.Sources[index] {
			return ErrStoreIntegrity
		}
	}
	return nil
}

func (registry ProviderUsageSourceRegistry) Contains(want ProviderUsageSourceRegistration) bool {
	if registry.Validate() != nil || want.Validate() != nil {
		return false
	}
	index := sort.Search(len(registry.Sources), func(index int) bool {
		return registry.Sources[index].RegistrationDigest >= want.RegistrationDigest
	})
	return index < len(registry.Sources) && registry.Sources[index] == want
}

// UsageMeterReleaseRecord freezes the exact Plugin Meter, pricing snapshot and
// complete sorted Provider source registry. It is read-only here: deployment
// tooling owns inserting release rows, while the runtime only verifies them.
type UsageMeterReleaseRecord struct {
	ReleaseID             string
	Plugin                agentv1.EventPluginRef
	PluginSnapshotDigest  string
	BillingPolicyKey      string
	PricingSnapshotJSON   json.RawMessage
	PricingSnapshotDigest string
	MeterKey              string
	MeterVersion          string
	MeterBuildDigest      string
	SourceRegistryJSON    json.RawMessage
	SourceRegistryDigest  string
	ReleaseDigest         string
	CreatedAt             time.Time
}

type UsageMeterReleaseSpec struct {
	Plugin              agentv1.EventPluginRef
	BillingPolicyKey    string
	PricingSnapshotJSON json.RawMessage
	MeterKey            string
	MeterVersion        string
	MeterBuildDigest    string
	Sources             []ProviderUsageSourceRegistration
}

func NewUsageMeterReleaseRecord(
	spec UsageMeterReleaseSpec,
	createdAt time.Time,
) (UsageMeterReleaseRecord, error) {
	pricing, err := canonicalProviderUsageJSON(spec.PricingSnapshotJSON)
	if err != nil {
		return UsageMeterReleaseRecord{}, err
	}
	registry, err := NewProviderUsageSourceRegistry(spec.Sources)
	if err != nil {
		return UsageMeterReleaseRecord{}, err
	}
	createdAt, err = canonicalExecutionTime(createdAt)
	if err != nil {
		return UsageMeterReleaseRecord{}, err
	}
	release := UsageMeterReleaseRecord{
		Plugin: spec.Plugin, BillingPolicyKey: spec.BillingPolicyKey,
		PricingSnapshotJSON: pricing, MeterKey: spec.MeterKey, MeterVersion: spec.MeterVersion,
		MeterBuildDigest: spec.MeterBuildDigest, SourceRegistryJSON: registry.CanonicalJSON, CreatedAt: createdAt,
	}
	release.PluginSnapshotDigest = providerUsagePluginSnapshotDigest(release.Plugin)
	release.PricingSnapshotDigest = providerUsageJSONDigest("provider-usage-pricing-snapshot-v1", pricing)
	release.SourceRegistryDigest = registry.Digest
	release.ReleaseDigest = usageMeterReleaseDigest(release)
	release.ReleaseID = providerUsageOpaqueID("provider-usage-meter-release-id-v1", release.ReleaseDigest)
	if err := release.Validate(); err != nil {
		return UsageMeterReleaseRecord{}, err
	}
	return release, nil
}

func (release UsageMeterReleaseRecord) Validate() error {
	if err := validatePrintableASCII("releaseId", release.ReleaseID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := release.Plugin.Validate(); err != nil {
		return err
	}
	if err := validatePluginRef(release.Plugin); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"billingPolicyKey", release.BillingPolicyKey},
		{"meterKey", release.MeterKey},
		{"meterVersion", release.MeterVersion},
	} {
		if err := validatePrintableASCII(field.name, field.value, MaxProviderUsageFieldBytes); err != nil {
			return err
		}
	}
	for name, digest := range map[string]string{
		"pluginSnapshotDigest":  release.PluginSnapshotDigest,
		"pricingSnapshotDigest": release.PricingSnapshotDigest,
		"meterBuildDigest":      release.MeterBuildDigest,
		"sourceRegistryDigest":  release.SourceRegistryDigest,
		"releaseDigest":         release.ReleaseDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	pricing, err := canonicalProviderUsageJSON(release.PricingSnapshotJSON)
	if err != nil || !bytes.Equal(pricing, release.PricingSnapshotJSON) ||
		release.PricingSnapshotDigest != providerUsageJSONDigest("provider-usage-pricing-snapshot-v1", pricing) {
		return ErrStoreIntegrity
	}
	registry, sources, err := decodeProviderUsageSourceRegistry(release.SourceRegistryJSON)
	if err != nil || !bytes.Equal(registry, release.SourceRegistryJSON) ||
		release.SourceRegistryDigest != providerUsageJSONDigest("provider-usage-source-registry-v1", registry) {
		return ErrStoreIntegrity
	}
	if len(sources) < 1 || len(sources) > MaxProviderUsageSources {
		return ErrStoreIntegrity
	}
	if createdAt, canonicalErr := canonicalExecutionTime(release.CreatedAt); canonicalErr != nil || !createdAt.Equal(release.CreatedAt) {
		return ErrStoreIntegrity
	}
	if release.PluginSnapshotDigest != providerUsagePluginSnapshotDigest(release.Plugin) ||
		release.ReleaseDigest != usageMeterReleaseDigest(release) ||
		release.ReleaseID != providerUsageOpaqueID("provider-usage-meter-release-id-v1", release.ReleaseDigest) {
		return ErrStoreIntegrity
	}
	return nil
}

func (release UsageMeterReleaseRecord) containsSource(want ProviderUsageSourceRegistration) bool {
	registry, err := release.SourceRegistry()
	if err != nil {
		return false
	}
	return registry.Contains(want)
}

func (release UsageMeterReleaseRecord) SourceRegistry() (ProviderUsageSourceRegistry, error) {
	canonical, sources, err := decodeProviderUsageSourceRegistry(release.SourceRegistryJSON)
	if err != nil {
		return ProviderUsageSourceRegistry{}, err
	}
	registry := ProviderUsageSourceRegistry{
		Sources: sources, CanonicalJSON: append(json.RawMessage(nil), canonical...),
		Digest: release.SourceRegistryDigest,
	}
	if err := registry.Validate(); err != nil {
		return ProviderUsageSourceRegistry{}, err
	}
	return registry, nil
}

func usageMeterReleaseDigest(release UsageMeterReleaseRecord) string {
	return providerUsageDigest("provider-usage-meter-release-v1",
		release.Plugin.ID, release.Plugin.Version, release.Plugin.ReleaseDigest,
		release.PluginSnapshotDigest, release.BillingPolicyKey, release.PricingSnapshotDigest,
		release.MeterKey, release.MeterVersion, release.MeterBuildDigest,
		release.SourceRegistryDigest,
	)
}

func decodeProviderUsageSourceRegistry(
	raw json.RawMessage,
) ([]byte, []ProviderUsageSourceRegistration, error) {
	canonical, err := canonicalProviderUsageJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, nil, ErrStoreIntegrity
	}
	var sources []ProviderUsageSourceRegistration
	if err := json.Unmarshal(canonical, &sources); err != nil || len(sources) < 1 || len(sources) > MaxProviderUsageSources {
		return nil, nil, ErrStoreIntegrity
	}
	normalized, err := json.Marshal(sources)
	if err == nil {
		normalized, err = canonicalProviderUsageJSON(normalized)
	}
	if err != nil || !bytes.Equal(normalized, canonical) {
		return nil, nil, ErrStoreIntegrity
	}
	for index, source := range sources {
		if source.Validate() != nil || (index > 0 && sources[index-1].RegistrationDigest >= source.RegistrationDigest) {
			return nil, nil, ErrStoreIntegrity
		}
	}
	return canonical, sources, nil
}

// ProviderUsageJournal is an opaque handle bound to one exact SQLStore. Raw
// Journal access is retained by composition; Plugins receive only a scoped
// ProviderUsageRecorder.
type ProviderUsageJournal struct {
	store  *SQLStore
	marker byte
}

func NewProviderUsageJournal(store *SQLStore) (*ProviderUsageJournal, error) {
	if store == nil || store.db == nil {
		return nil, ErrProviderUsageJournalInvalid
	}
	return &ProviderUsageJournal{store: store, marker: 1}, nil
}

func (journal *ProviderUsageJournal) MatchesStore(store *SQLStore) bool {
	return journal != nil && journal.marker == 1 && journal.store == store && store != nil
}

// ScopeRecorder verifies an immutable Meter Release and returns the only
// append surface a Plugin should receive.
func (journal *ProviderUsageJournal) ScopeRecorder(
	ctx context.Context,
	plugin agentv1.EventPluginRef,
	meterReleaseID string,
	source ProviderUsageSourceRegistration,
) (*ProviderUsageRecorder, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if journal == nil || !journal.MatchesStore(journal.store) || plugin.Validate() != nil || validatePluginRef(plugin) != nil ||
		validatePrintableASCII("meterReleaseId", meterReleaseID, MaxSettlementReviewIDBytes) != nil || source.Validate() != nil {
		return nil, ErrProviderUsageJournalInvalid
	}
	release, found, err := lookupUsageMeterReleaseTx(journal.store.db.WithContext(ctx), meterReleaseID, false)
	if err != nil {
		return nil, journal.store.normalize("scope-provider-usage-recorder", err)
	}
	if !found {
		return nil, ErrUsageMeterReleaseNotFound
	}
	if release.Plugin != plugin || !release.containsSource(source) {
		return nil, ErrUsageMeterReleaseConflict
	}
	return &ProviderUsageRecorder{
		journal: journal, plugin: plugin, meterReleaseID: meterReleaseID,
		meterReleaseDigest: release.ReleaseDigest, source: source, marker: 1,
	}, nil
}

type ProviderUsageRecorder struct {
	journal            *ProviderUsageJournal
	plugin             agentv1.EventPluginRef
	meterReleaseID     string
	meterReleaseDigest string
	source             ProviderUsageSourceRegistration
	marker             byte
}

// MatchesScope lets a sealed composition prove that a facade retains the
// exact Journal, Plugin release, Meter release and source registration it was
// built from, without exposing mutable Journal operations to the Plugin.
func (recorder *ProviderUsageRecorder) MatchesScope(
	journal *ProviderUsageJournal,
	plugin agentv1.EventPluginRef,
	meterReleaseID string,
	source ProviderUsageSourceRegistration,
) bool {
	return recorder != nil && recorder.intact() && recorder.journal == journal &&
		recorder.plugin == plugin && recorder.meterReleaseID == meterReleaseID &&
		recorder.source == source
}

func (recorder *ProviderUsageRecorder) intact() bool {
	return recorder != nil && recorder.marker == 1 && recorder.journal != nil &&
		recorder.journal.MatchesStore(recorder.journal.store) && recorder.plugin.Validate() == nil &&
		validatePluginRef(recorder.plugin) == nil && recorder.source.Validate() == nil &&
		validatePrintableASCII("meterReleaseId", recorder.meterReleaseID, MaxSettlementReviewIDBytes) == nil &&
		validateSettlementReviewSHA256Digest("meterReleaseDigest", recorder.meterReleaseDigest) == nil
}

// AppendAttestedProviderUsageCommand carries one execution fence and output
// attested by the exact scoped adapter. This boundary does not claim
// end-to-end Provider authenticity: the kernel verifies the immutable
// registry, adapter attestation, digests, fence and provenance.
// Provider/account/source/schema/Meter/pricing are private fields on
// ProviderUsageRecorder and cannot be selected here.
type AppendAttestedProviderUsageCommand struct {
	Fence                 AttemptFence
	ProviderRequestDigest string
	ProviderEventDigest   string
	CanonicalUsageJSON    json.RawMessage
	ProviderReceiptDigest string
	AttestationDigest     string
	ProviderReportedAt    time.Time
}

func (command AppendAttestedProviderUsageCommand) Validate() error {
	if err := command.Fence.Validate(); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"providerRequestDigest": command.ProviderRequestDigest,
		"providerEventDigest":   command.ProviderEventDigest,
		"providerReceiptDigest": command.ProviderReceiptDigest,
		"attestationDigest":     command.AttestationDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	canonical, err := canonicalProviderUsageJSON(command.CanonicalUsageJSON)
	if err != nil || !bytes.Equal(canonical, command.CanonicalUsageJSON) {
		return fmt.Errorf("canonicalUsageJson must be canonical JSON")
	}
	reportedAt, err := canonicalExecutionTime(command.ProviderReportedAt)
	if err != nil || !reportedAt.Equal(command.ProviderReportedAt) {
		return fmt.Errorf("providerReportedAt must be a canonical SQL timestamp")
	}
	return nil
}

type ProviderUsageJournalRecord struct {
	ReceiptID                string
	TurnID                   agentv1.TurnID
	AttemptID                string
	FencingToken             agentv1.Sequence
	MeterReleaseID           string
	Plugin                   agentv1.EventPluginRef
	PluginSnapshotDigest     string
	ProviderKey              string
	ProviderAccountDigest    string
	ProviderRequestDigest    string
	ProviderEventDigest      string
	SourceKey                string
	SourceVersion            string
	SourceBuildDigest        string
	SourceRegistrationDigest string
	UsageSchemaKey           string
	UsageSchemaVersion       string
	SourceSchemaDigest       string
	CanonicalUsageDigest     string
	ProviderReceiptDigest    string
	VerificationKind         string
	VerificationKeyDigest    string
	VerificationBuildDigest  string
	AttestationDigest        string
	JournalRecordDigest      string
	ProviderUsageJSON        json.RawMessage
	ProviderReportedAt       time.Time
	CreatedAt                time.Time
}

func (record ProviderUsageJournalRecord) Validate() error {
	if err := validatePrintableASCII("receiptId", record.ReceiptID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	if err := (AttemptFence{TurnID: record.TurnID, AttemptID: record.AttemptID,
		FencingToken: record.FencingToken, WorkerID: "journal", WorkerBuildDigest: "journal"}).Validate(); err != nil {
		return err
	}
	if err := record.Plugin.Validate(); err != nil {
		return err
	}
	if err := validatePluginRef(record.Plugin); err != nil {
		return err
	}
	registration := record.sourceRegistration()
	if err := registration.Validate(); err != nil {
		return err
	}
	if err := validatePrintableASCII("meterReleaseId", record.MeterReleaseID, MaxSettlementReviewIDBytes); err != nil {
		return err
	}
	for name, digest := range map[string]string{
		"pluginSnapshotDigest":  record.PluginSnapshotDigest,
		"providerRequestDigest": record.ProviderRequestDigest,
		"providerEventDigest":   record.ProviderEventDigest,
		"canonicalUsageDigest":  record.CanonicalUsageDigest,
		"providerReceiptDigest": record.ProviderReceiptDigest,
		"attestationDigest":     record.AttestationDigest,
		"journalRecordDigest":   record.JournalRecordDigest,
	} {
		if err := validateSettlementReviewSHA256Digest(name, digest); err != nil {
			return err
		}
	}
	canonical, err := canonicalProviderUsageJSON(record.ProviderUsageJSON)
	if err != nil || !bytes.Equal(canonical, record.ProviderUsageJSON) ||
		record.CanonicalUsageDigest != providerUsageJSONDigest("provider-usage-canonical-payload-v1", canonical) {
		return ErrStoreIntegrity
	}
	for _, stamp := range []time.Time{record.ProviderReportedAt, record.CreatedAt} {
		canonicalStamp, stampErr := canonicalExecutionTime(stamp)
		if stampErr != nil || !canonicalStamp.Equal(stamp) {
			return ErrStoreIntegrity
		}
	}
	if record.ProviderReportedAt.After(record.CreatedAt) ||
		record.PluginSnapshotDigest != providerUsagePluginSnapshotDigest(record.Plugin) ||
		record.ReceiptID != providerUsageReceiptID(record.ProviderKey, record.ProviderAccountDigest, record.ProviderEventDigest) ||
		record.JournalRecordDigest != providerUsageJournalRecordDigest(record) {
		return ErrStoreIntegrity
	}
	return nil
}

func (record ProviderUsageJournalRecord) sourceRegistration() ProviderUsageSourceRegistration {
	return ProviderUsageSourceRegistration{
		ProviderKey: record.ProviderKey, ProviderAccountDigest: record.ProviderAccountDigest,
		SourceKey: record.SourceKey, SourceVersion: record.SourceVersion,
		SourceBuildDigest: record.SourceBuildDigest, UsageSchemaKey: record.UsageSchemaKey,
		UsageSchemaVersion: record.UsageSchemaVersion, SourceSchemaDigest: record.SourceSchemaDigest,
		VerificationKind: record.VerificationKind, VerificationKeyDigest: record.VerificationKeyDigest,
		VerificationBuildDigest: record.VerificationBuildDigest,
		RegistrationDigest:      record.SourceRegistrationDigest,
	}
}

type AppendAttestedProviderUsageResult struct {
	Record ProviderUsageJournalRecord
	Replay bool
}

func (recorder *ProviderUsageRecorder) AppendAttested(
	ctx context.Context,
	command AppendAttestedProviderUsageCommand,
) (AppendAttestedProviderUsageResult, error) {
	if err := contextError(ctx); err != nil {
		return AppendAttestedProviderUsageResult{}, err
	}
	if !recorder.intact() {
		return AppendAttestedProviderUsageResult{}, ErrProviderUsageJournalInvalid
	}
	if err := command.Validate(); err != nil {
		return AppendAttestedProviderUsageResult{}, err
	}
	store := recorder.journal.store
	var result AppendAttestedProviderUsageResult
	insertAttempted := false
	txErr := store.writeTransaction(ctx, func(tx *gorm.DB) error {
		turnRow, err := store.lockTurn(tx, "turn_id = ?", string(command.Fence.TurnID))
		if err != nil {
			return ErrProviderUsageForbidden
		}
		turn, err := turnRow.toTurn()
		if err != nil {
			return ErrStoreIntegrity
		}
		if turn.Plugin != recorder.plugin {
			return ErrProviderUsageForbidden
		}
		attemptRow, err := store.lockAttempt(tx, string(turn.ID), command.Fence.AttemptID, int64(command.Fence.FencingToken))
		if err != nil {
			return ErrProviderUsageForbidden
		}
		attempt, err := attemptRow.toAttempt()
		if err != nil || attempt.WorkerID != command.Fence.WorkerID ||
			attempt.WorkerBuildDigest != command.Fence.WorkerBuildDigest {
			return ErrProviderUsageForbidden
		}
		// Shared Provider-usage resources use one global order: event/journal row
		// before Meter Release. Capture follows the same order, preventing a
		// cross-Turn Append/Capture inversion when both touch one release.
		existing, existingFound, err := lookupProviderUsageJournalByEventTx(
			tx, recorder.source.ProviderKey, recorder.source.ProviderAccountDigest,
			command.ProviderEventDigest, true,
		)
		if err != nil {
			return err
		}
		release, releaseFound, err := lookupUsageMeterReleaseTx(tx, recorder.meterReleaseID, true)
		if err != nil {
			return err
		}
		if !releaseFound {
			return ErrUsageMeterReleaseNotFound
		}
		if release.ReleaseDigest != recorder.meterReleaseDigest || release.Plugin != recorder.plugin ||
			!release.containsSource(recorder.source) {
			return ErrUsageMeterReleaseConflict
		}

		if existingFound {
			if !providerUsageRecordMatchesAppend(existing, recorder, command) {
				return ErrProviderUsageConflict
			}
			switch {
			case turn.Status == agentv1.TurnStatusRunning:
				// The immutable receipt remains replayable after this particular
				// lease expires or a later Attempt takes ownership while the Turn
				// is still running.
			case turn.Status.Terminal():
				if !attempt.Status.Terminal() {
					return ErrStoreIntegrity
				}
				review, reviewFound, reviewErr := store.lookupSettlementReviewTx(tx, turn.ID, true)
				if reviewErr != nil {
					return reviewErr
				}
				if !reviewFound || !settlementReviewProviderUsageAware(review) ||
					attempt.FencingToken > review.FencingToken {
					return ErrProviderUsageForbidden
				}
				if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
					return err
				}
			default:
				return ErrStoreIntegrity
			}
			result = AppendAttestedProviderUsageResult{Record: existing, Replay: true}
			return nil
		}

		now, err := store.executionNow(ctx, tx)
		if err != nil {
			return err
		}
		if command.ProviderReportedAt.After(now) {
			return ErrProviderUsageForbidden
		}
		if turn.Status == agentv1.TurnStatusRunning {
			if turnRow.ActiveAttemptID == nil || *turnRow.ActiveAttemptID != attempt.ID ||
				turnRow.FencingToken != int64(attempt.FencingToken) || attempt.Status != AttemptStatusRunning ||
				!attempt.LeaseExpiresAt.After(now) {
				return ErrProviderUsageForbidden
			}
		} else if turn.Status.Terminal() {
			if !attempt.Status.Terminal() {
				return ErrProviderUsageForbidden
			}
			review, found, err := store.lookupSettlementReviewTx(tx, turn.ID, true)
			if err != nil {
				return err
			}
			if !found || review.Status != SettlementReviewStatusPending ||
				!settlementReviewProviderUsageAware(review) {
				return ErrProviderUsageForbidden
			}
			switch review.Source {
			case SettlementReviewSourceExecutorCompletion, SettlementReviewSourceExecutorTerminal,
				SettlementReviewSourceReconcileTerminal:
				if attempt.FencingToken > review.FencingToken {
					return ErrProviderUsageForbidden
				}
			default:
				return ErrProviderUsageForbidden
			}
			if err := store.validateTerminalSettlementReviewTx(tx, turnRow, turn, review); err != nil {
				return err
			}
		} else {
			return ErrProviderUsageForbidden
		}
		record := newProviderUsageJournalRecord(recorder, command, now)
		row, err := providerUsageJournalToSQLRow(record)
		if err != nil {
			return ErrStoreIntegrity
		}
		insertAttempted = true
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		result = AppendAttestedProviderUsageResult{Record: record}
		return nil
	})
	if txErr == nil {
		return result, nil
	}
	for _, known := range []error{
		ErrProviderUsageJournalInvalid, ErrProviderUsageConflict, ErrProviderUsageForbidden,
		ErrUsageMeterReleaseNotFound, ErrUsageMeterReleaseConflict,
	} {
		if errors.Is(txErr, known) {
			return AppendAttestedProviderUsageResult{}, known
		}
	}
	if !insertAttempted {
		// Parent Turn/Attempt/Review/Meter integrity failures happen before the
		// INSERT. Never let an older matching event receipt mask those failures;
		// the read-after-error fallback below exists only for a cross-Turn
		// provider-event uniqueness race reached at the INSERT boundary.
		return AppendAttestedProviderUsageResult{}, store.normalize("append-provider-usage", txErr)
	}
	// A provider-event race may occur across different Turn locks. Classify it
	// through the immutable row without exposing the driver error.
	if existing, found, lookupErr := lookupProviderUsageJournalByEventTx(
		store.db.WithContext(ctx), recorder.source.ProviderKey, recorder.source.ProviderAccountDigest,
		command.ProviderEventDigest, false,
	); lookupErr == nil && found {
		if providerUsageRecordMatchesAppend(existing, recorder, command) {
			return AppendAttestedProviderUsageResult{Record: existing, Replay: true}, nil
		}
		return AppendAttestedProviderUsageResult{}, ErrProviderUsageConflict
	}
	return AppendAttestedProviderUsageResult{}, store.normalize("append-provider-usage", txErr)
}

func newProviderUsageJournalRecord(
	recorder *ProviderUsageRecorder,
	command AppendAttestedProviderUsageCommand,
	createdAt time.Time,
) ProviderUsageJournalRecord {
	source := recorder.source
	record := ProviderUsageJournalRecord{
		ReceiptID: providerUsageReceiptID(source.ProviderKey, source.ProviderAccountDigest, command.ProviderEventDigest),
		TurnID:    command.Fence.TurnID, AttemptID: command.Fence.AttemptID,
		FencingToken: command.Fence.FencingToken, MeterReleaseID: recorder.meterReleaseID,
		Plugin: recorder.plugin, PluginSnapshotDigest: providerUsagePluginSnapshotDigest(recorder.plugin),
		ProviderKey: source.ProviderKey, ProviderAccountDigest: source.ProviderAccountDigest,
		ProviderRequestDigest: command.ProviderRequestDigest, ProviderEventDigest: command.ProviderEventDigest,
		SourceKey: source.SourceKey, SourceVersion: source.SourceVersion,
		SourceBuildDigest: source.SourceBuildDigest, SourceRegistrationDigest: source.RegistrationDigest,
		UsageSchemaKey: source.UsageSchemaKey, UsageSchemaVersion: source.UsageSchemaVersion,
		SourceSchemaDigest:    source.SourceSchemaDigest,
		CanonicalUsageDigest:  providerUsageJSONDigest("provider-usage-canonical-payload-v1", command.CanonicalUsageJSON),
		ProviderReceiptDigest: command.ProviderReceiptDigest,
		VerificationKind:      source.VerificationKind, VerificationKeyDigest: source.VerificationKeyDigest,
		VerificationBuildDigest: source.VerificationBuildDigest,
		AttestationDigest:       command.AttestationDigest,
		ProviderUsageJSON:       append(json.RawMessage(nil), command.CanonicalUsageJSON...),
		ProviderReportedAt:      command.ProviderReportedAt, CreatedAt: createdAt,
	}
	record.JournalRecordDigest = providerUsageJournalRecordDigest(record)
	return record
}

func providerUsageRecordMatchesAppend(
	record ProviderUsageJournalRecord,
	recorder *ProviderUsageRecorder,
	command AppendAttestedProviderUsageCommand,
) bool {
	return record.Validate() == nil && record.TurnID == command.Fence.TurnID &&
		record.AttemptID == command.Fence.AttemptID && record.FencingToken == command.Fence.FencingToken &&
		record.MeterReleaseID == recorder.meterReleaseID && record.Plugin == recorder.plugin &&
		record.sourceRegistration() == recorder.source && record.ProviderRequestDigest == command.ProviderRequestDigest &&
		record.ProviderEventDigest == command.ProviderEventDigest &&
		bytes.Equal(record.ProviderUsageJSON, command.CanonicalUsageJSON) &&
		record.ProviderReceiptDigest == command.ProviderReceiptDigest &&
		record.AttestationDigest == command.AttestationDigest &&
		record.ProviderReportedAt.Equal(command.ProviderReportedAt)
}

func providerUsageReceiptID(providerKey, accountDigest, eventDigest string) string {
	return providerUsageOpaqueID("provider-usage-receipt-id-v1", providerKey, accountDigest, eventDigest)
}

func providerUsageJournalRecordDigest(record ProviderUsageJournalRecord) string {
	return providerUsageDigest("provider-usage-journal-record-v1",
		record.ReceiptID, string(record.TurnID), record.AttemptID,
		strconv.FormatInt(int64(record.FencingToken), 10), record.MeterReleaseID,
		record.Plugin.ID, record.Plugin.Version, record.Plugin.ReleaseDigest, record.PluginSnapshotDigest,
		record.ProviderKey, record.ProviderAccountDigest, record.ProviderRequestDigest, record.ProviderEventDigest,
		record.SourceKey, record.SourceVersion, record.SourceBuildDigest, record.SourceRegistrationDigest,
		record.UsageSchemaKey, record.UsageSchemaVersion, record.SourceSchemaDigest,
		record.CanonicalUsageDigest, record.ProviderReceiptDigest, record.VerificationKind,
		record.VerificationKeyDigest, record.VerificationBuildDigest, record.AttestationDigest,
		record.ProviderReportedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
		record.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	)
}

func providerUsagePluginSnapshotDigest(plugin agentv1.EventPluginRef) string {
	return providerUsageDigest("provider-usage-plugin-snapshot-v1", plugin.ID, plugin.Version, plugin.ReleaseDigest)
}

func providerUsageJSONDigest(domain string, raw []byte) string {
	return providerUsageDigest(domain, string(raw))
}

func providerUsageDigest(domain string, parts ...string) string {
	hash := sha256.New()
	settlementReviewHashParts(hash, append([]string{domain}, parts...)...)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func providerUsageOpaqueID(domain string, parts ...string) string {
	hash := sha256.New()
	settlementReviewHashParts(hash, append([]string{domain}, parts...)...)
	return hex.EncodeToString(hash.Sum(nil))
}

func canonicalProviderUsageJSON(raw []byte) ([]byte, error) {
	if len(raw) < 1 || len(raw) > MaxProviderUsageJSONBytes {
		return nil, ErrStoreIntegrity
	}
	return canonicalJSONContent(raw)
}

type sqlUsageMeterReleaseRow struct {
	ID                    uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ReleaseID             string    `gorm:"column:release_id"`
	PluginID              string    `gorm:"column:plugin_id"`
	PluginVersion         string    `gorm:"column:plugin_version"`
	PluginReleaseDigest   string    `gorm:"column:plugin_release_digest"`
	PluginSnapshotDigest  string    `gorm:"column:plugin_snapshot_digest"`
	BillingPolicyKey      string    `gorm:"column:billing_policy_key"`
	PricingSnapshotJSON   []byte    `gorm:"column:pricing_snapshot_json"`
	PricingSnapshotDigest string    `gorm:"column:pricing_snapshot_digest"`
	MeterKey              string    `gorm:"column:meter_key"`
	MeterVersion          string    `gorm:"column:meter_version"`
	MeterBuildDigest      string    `gorm:"column:meter_build_digest"`
	SourceRegistryJSON    []byte    `gorm:"column:source_registry_json"`
	SourceRegistryDigest  string    `gorm:"column:source_registry_digest"`
	ReleaseDigest         string    `gorm:"column:release_digest"`
	CreatedAt             time.Time `gorm:"column:created_at"`
}

func (sqlUsageMeterReleaseRow) TableName() string { return SQLUsageMeterReleaseTable }

func (row sqlUsageMeterReleaseRow) toRecord() (UsageMeterReleaseRecord, error) {
	record := UsageMeterReleaseRecord{
		ReleaseID:            row.ReleaseID,
		Plugin:               agentv1.EventPluginRef{ID: row.PluginID, Version: row.PluginVersion, ReleaseDigest: row.PluginReleaseDigest},
		PluginSnapshotDigest: row.PluginSnapshotDigest, BillingPolicyKey: row.BillingPolicyKey,
		PricingSnapshotJSON:   append(json.RawMessage(nil), row.PricingSnapshotJSON...),
		PricingSnapshotDigest: row.PricingSnapshotDigest, MeterKey: row.MeterKey,
		MeterVersion: row.MeterVersion, MeterBuildDigest: row.MeterBuildDigest,
		SourceRegistryJSON:   append(json.RawMessage(nil), row.SourceRegistryJSON...),
		SourceRegistryDigest: row.SourceRegistryDigest, ReleaseDigest: row.ReleaseDigest,
		CreatedAt: row.CreatedAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return UsageMeterReleaseRecord{}, ErrStoreIntegrity
	}
	return record, nil
}

func usageMeterReleaseToSQLRow(record UsageMeterReleaseRecord) (sqlUsageMeterReleaseRow, error) {
	if err := record.Validate(); err != nil {
		return sqlUsageMeterReleaseRow{}, err
	}
	return sqlUsageMeterReleaseRow{
		ReleaseID: record.ReleaseID, PluginID: record.Plugin.ID, PluginVersion: record.Plugin.Version,
		PluginReleaseDigest: record.Plugin.ReleaseDigest, PluginSnapshotDigest: record.PluginSnapshotDigest,
		BillingPolicyKey: record.BillingPolicyKey, PricingSnapshotJSON: append([]byte(nil), record.PricingSnapshotJSON...),
		PricingSnapshotDigest: record.PricingSnapshotDigest, MeterKey: record.MeterKey,
		MeterVersion: record.MeterVersion, MeterBuildDigest: record.MeterBuildDigest,
		SourceRegistryJSON:   append([]byte(nil), record.SourceRegistryJSON...),
		SourceRegistryDigest: record.SourceRegistryDigest, ReleaseDigest: record.ReleaseDigest,
		CreatedAt: record.CreatedAt.UTC(),
	}, nil
}

func lookupUsageMeterReleaseTx(tx *gorm.DB, releaseID string, lock bool) (UsageMeterReleaseRecord, bool, error) {
	if tx == nil {
		return UsageMeterReleaseRecord{}, false, ErrStoreIntegrity
	}
	query := tx.Table(SQLUsageMeterReleaseTable).Where("release_id = ?", releaseID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sqlUsageMeterReleaseRow
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return UsageMeterReleaseRecord{}, false, nil
	}
	if err != nil {
		return UsageMeterReleaseRecord{}, false, err
	}
	record, err := row.toRecord()
	return record, err == nil, err
}

type sqlProviderUsageJournalRow struct {
	ID                       uint64    `gorm:"column:id;primaryKey;autoIncrement"`
	ReceiptID                string    `gorm:"column:receipt_id"`
	TurnID                   string    `gorm:"column:turn_id"`
	AttemptID                string    `gorm:"column:attempt_id"`
	FencingToken             int64     `gorm:"column:fencing_token"`
	MeterReleaseID           string    `gorm:"column:meter_release_id"`
	PluginID                 string    `gorm:"column:plugin_id"`
	PluginVersion            string    `gorm:"column:plugin_version"`
	PluginReleaseDigest      string    `gorm:"column:plugin_release_digest"`
	PluginSnapshotDigest     string    `gorm:"column:plugin_snapshot_digest"`
	ProviderKey              string    `gorm:"column:provider_key"`
	ProviderAccountDigest    string    `gorm:"column:provider_account_digest"`
	ProviderRequestDigest    string    `gorm:"column:provider_request_digest"`
	ProviderEventDigest      string    `gorm:"column:provider_event_digest"`
	SourceKey                string    `gorm:"column:source_key"`
	SourceVersion            string    `gorm:"column:source_version"`
	SourceBuildDigest        string    `gorm:"column:source_build_digest"`
	SourceRegistrationDigest string    `gorm:"column:source_registration_digest"`
	UsageSchemaKey           string    `gorm:"column:usage_schema_key"`
	UsageSchemaVersion       string    `gorm:"column:usage_schema_version"`
	SourceSchemaDigest       string    `gorm:"column:source_schema_digest"`
	CanonicalUsageDigest     string    `gorm:"column:canonical_usage_digest"`
	ProviderReceiptDigest    string    `gorm:"column:provider_receipt_digest"`
	VerificationKind         string    `gorm:"column:verification_kind"`
	VerificationKeyDigest    string    `gorm:"column:verification_key_digest"`
	VerificationBuildDigest  string    `gorm:"column:verification_build_digest"`
	AttestationDigest        string    `gorm:"column:attestation_digest"`
	JournalRecordDigest      string    `gorm:"column:journal_record_digest"`
	ProviderUsageJSON        []byte    `gorm:"column:provider_usage_json"`
	ProviderReportedAt       time.Time `gorm:"column:provider_reported_at"`
	CreatedAt                time.Time `gorm:"column:created_at"`
}

func (sqlProviderUsageJournalRow) TableName() string { return SQLProviderUsageJournalTable }

func (row sqlProviderUsageJournalRow) toRecord() (ProviderUsageJournalRecord, error) {
	record := ProviderUsageJournalRecord{
		ReceiptID: row.ReceiptID, TurnID: agentv1.TurnID(row.TurnID), AttemptID: row.AttemptID,
		FencingToken: agentv1.Sequence(row.FencingToken), MeterReleaseID: row.MeterReleaseID,
		Plugin:               agentv1.EventPluginRef{ID: row.PluginID, Version: row.PluginVersion, ReleaseDigest: row.PluginReleaseDigest},
		PluginSnapshotDigest: row.PluginSnapshotDigest, ProviderKey: row.ProviderKey,
		ProviderAccountDigest: row.ProviderAccountDigest, ProviderRequestDigest: row.ProviderRequestDigest,
		ProviderEventDigest: row.ProviderEventDigest, SourceKey: row.SourceKey, SourceVersion: row.SourceVersion,
		SourceBuildDigest: row.SourceBuildDigest, SourceRegistrationDigest: row.SourceRegistrationDigest,
		UsageSchemaKey: row.UsageSchemaKey, UsageSchemaVersion: row.UsageSchemaVersion,
		SourceSchemaDigest: row.SourceSchemaDigest, CanonicalUsageDigest: row.CanonicalUsageDigest,
		ProviderReceiptDigest: row.ProviderReceiptDigest, VerificationKind: row.VerificationKind,
		VerificationKeyDigest: row.VerificationKeyDigest, VerificationBuildDigest: row.VerificationBuildDigest,
		AttestationDigest: row.AttestationDigest, JournalRecordDigest: row.JournalRecordDigest,
		ProviderUsageJSON:  append(json.RawMessage(nil), row.ProviderUsageJSON...),
		ProviderReportedAt: row.ProviderReportedAt.UTC(), CreatedAt: row.CreatedAt.UTC(),
	}
	if err := record.Validate(); err != nil {
		return ProviderUsageJournalRecord{}, ErrStoreIntegrity
	}
	return record, nil
}

func providerUsageJournalToSQLRow(record ProviderUsageJournalRecord) (sqlProviderUsageJournalRow, error) {
	if err := record.Validate(); err != nil {
		return sqlProviderUsageJournalRow{}, err
	}
	return sqlProviderUsageJournalRow{
		ReceiptID: record.ReceiptID, TurnID: string(record.TurnID), AttemptID: record.AttemptID,
		FencingToken: int64(record.FencingToken), MeterReleaseID: record.MeterReleaseID,
		PluginID: record.Plugin.ID, PluginVersion: record.Plugin.Version,
		PluginReleaseDigest: record.Plugin.ReleaseDigest, PluginSnapshotDigest: record.PluginSnapshotDigest,
		ProviderKey: record.ProviderKey, ProviderAccountDigest: record.ProviderAccountDigest,
		ProviderRequestDigest: record.ProviderRequestDigest, ProviderEventDigest: record.ProviderEventDigest,
		SourceKey: record.SourceKey, SourceVersion: record.SourceVersion, SourceBuildDigest: record.SourceBuildDigest,
		SourceRegistrationDigest: record.SourceRegistrationDigest, UsageSchemaKey: record.UsageSchemaKey,
		UsageSchemaVersion: record.UsageSchemaVersion, SourceSchemaDigest: record.SourceSchemaDigest,
		CanonicalUsageDigest: record.CanonicalUsageDigest, ProviderReceiptDigest: record.ProviderReceiptDigest,
		VerificationKind: record.VerificationKind, VerificationKeyDigest: record.VerificationKeyDigest,
		VerificationBuildDigest: record.VerificationBuildDigest, AttestationDigest: record.AttestationDigest,
		JournalRecordDigest: record.JournalRecordDigest, ProviderUsageJSON: append([]byte(nil), record.ProviderUsageJSON...),
		ProviderReportedAt: record.ProviderReportedAt.UTC(), CreatedAt: record.CreatedAt.UTC(),
	}, nil
}

func lookupProviderUsageJournalByEventTx(
	tx *gorm.DB,
	providerKey string,
	accountDigest string,
	eventDigest string,
	lock bool,
) (ProviderUsageJournalRecord, bool, error) {
	if tx == nil {
		return ProviderUsageJournalRecord{}, false, ErrStoreIntegrity
	}
	query := tx.Table(SQLProviderUsageJournalTable).Where(
		"provider_key = ? AND provider_account_digest = ? AND provider_event_digest = ?",
		providerKey, accountDigest, eventDigest,
	)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row sqlProviderUsageJournalRow
	err := query.Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProviderUsageJournalRecord{}, false, nil
	}
	if err != nil {
		return ProviderUsageJournalRecord{}, false, err
	}
	record, err := row.toRecord()
	return record, err == nil, err
}
