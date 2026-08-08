package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"server/config"
	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

// P0-038 froze the pre-dependency-acquisition contract. P0-039 supplies the
// sealed Builder, exact runtime scope, fail-closed composite probe and
// acquisition primitives. Claim, Effect routing and probe aggregation are
// platform-owned constructions rather than catalog extension points. The
// shipped process still deliberately registers no real domain, Effect,
// Credits or promotion-evidence factories. Keeping the validator pure lets
// those absences stop startup before a database pool, provider transport,
// subprocess or background goroutine is constructed.
var (
	errWorkerDependencyPlanInvalid              = errors.New("agent-worker production dependency plan is invalid")
	errWorkerDependencyIdentityUnavailable      = errors.New("agent-worker production build identity is unavailable")
	errWorkerDependencyPluginUnavailable        = errors.New("agent-worker production plugin coverage is unavailable")
	errWorkerDependencyEffectUnavailable        = errors.New("agent-worker production effect coverage is unavailable")
	errWorkerDependencyDatabaseUnavailable      = errors.New("agent-worker production database factory is unavailable")
	errWorkerDependencyProviderUsageUnavailable = errors.New(
		"agent-worker production provider usage factory is unavailable",
	)
	errWorkerDependencySettlementUnavailable = errors.New(
		"agent-worker production settlement factory is unavailable",
	)
)

const (
	maxWorkerProductionPlugins = 32
	maxWorkerEffectTopics      = 128
	maxWorkerEffectTopicBytes  = agentturn.MaxEffectTopicBytes
	maxWorkerExecutionTimeout  = 24 * time.Hour

	workerSettlementCreditsV1            workerSettlementKind    = "credits_v1"
	workerProviderUsageJournalRegistryV1 workerProviderUsageKind = "journal_registry_v1"
)

// workerBuildIdentity is intentionally separate from workerStartupSnapshot.
// Its digest slot is reserved for a future release/build-owned injector;
// snapshot.Digest identifies config bytes and is not an acceptable substitute.
// P0-038 validates this shape but does not provide or attest that injector.
type workerBuildIdentity struct {
	identity   ProcessIdentity
	provenance workerBuildIdentityProvenance
}

type workerBuildIdentityProvenance uint8

const workerBuildIdentityFromArtifact workerBuildIdentityProvenance = 1

type workerSettlementKind string

type workerProviderUsageKind string

// workerPluginPromotionEvidence is the input shape a future parity-ledger
// verifier must produce for one exact Plugin release. P0-038 checks its
// binding and digest syntax; it does not read or attest a real ledger. No
// production code creates this value, so YAML cannot currently pass the gate.
type workerPluginPromotionEvidence struct {
	marker       byte
	snapshot     agentv1.EventPluginRef
	ledgerDigest string
}

type workerPluginRequirement struct {
	Snapshot         agentv1.EventPluginRef
	EffectTopics     []string
	ExecutionTimeout time.Duration
	ProgressTimeout  time.Duration
	Promotion        workerPluginPromotionEvidence
}

type workerDependencyPlan struct {
	Plugins       []workerPluginRequirement
	ProviderUsage workerProviderUsageKind
	Settlement    workerSettlementKind
}

// workerResourceRegistrar is the ownership hand-off enforced by the Builder.
// Every acquired dependency must be registered immediately, before the next
// factory is invoked. The cancellable guard performs one sealed transfer into
// Compose.
type workerResourceRegistrar interface {
	Own(WorkerResourceCloser) error
}

// workerFactoryOwnership makes every external Factory say whether this call
// created resources registered through Own or is a pure adapter borrowing its
// declared parents. The acquisition guard verifies that statement against the
// number of successful Own calls made by this exact Factory step.
type workerFactoryOwnership uint8

const (
	workerFactoryRegisteredResources workerFactoryOwnership = iota + 1
	workerFactoryBorrowedOnly
)

// workerValidatedDatabaseConfig retains only bounded scalar values from the
// production driver's parsed, normalized settings and shares no mutable
// Driver/TLS/map state. The external database factory receives this opaque
// value, not workerStartupSnapshot or the legacy DSN helper.
type workerValidatedDatabaseConfig struct {
	marker       byte
	configDigest [sha256.Size]byte
	settings     workerValidatedMySQLSettings
}

func (database workerValidatedDatabaseConfig) intact(configDigest [sha256.Size]byte) bool {
	settings := database.settings
	return database.marker == 1 && database.configDigest == configDigest &&
		settings.intact()
}

type workerDatabaseFactory func(
	context.Context,
	workerValidatedDatabaseConfig,
	workerResourceRegistrar,
) (*gorm.DB, RuntimeProbe, workerFactoryOwnership, error)

// workerExactClaimStore is constructed only inside the production Builder.
// Its ExecutionStore ClaimNext atomically filters on every field of the
// supplied Plugin snapshots. A startup queue scan is not a substitute: an
// unsupported Turn can be admitted immediately after such a scan.
type workerExactClaimStore struct {
	marker      byte
	execution   *agentturn.PluginScopedExecutionStore
	scopeDigest [sha256.Size]byte
}

type workerSettlementFactory func(
	context.Context,
	*gorm.DB,
	*workerProviderUsageBinding,
	workerResourceRegistrar,
) (agentturn.SettlementAuthority, RuntimeProbe, workerFactoryOwnership, error)

type workerProviderUsageFactory func(
	context.Context,
	*gorm.DB,
	*agentturn.SQLStore,
	[]agentv1.EventPluginRef,
	workerResourceRegistrar,
) (*workerProviderUsageBinding, RuntimeProbe, workerFactoryOwnership, error)

type workerExecutorFactory func(
	context.Context,
	*gorm.DB,
	workerPluginRequirement,
	workerPluginProviderUsage,
	workerResourceRegistrar,
) (agentturn.TurnExecutor, RuntimeProbe, workerFactoryOwnership, error)

type workerEffectFactory func(
	context.Context,
	*gorm.DB,
	string,
	workerResourceRegistrar,
) (agentturn.Deliverer, RuntimeProbe, workerFactoryOwnership, error)

type workerEffectBinding struct {
	Topic     string
	Deliverer agentturn.Deliverer
}

// workerExactEffectRouter is the runtime object that both routes delivery by
// exact Topic and supplies the same non-empty Topic set to the dispatcher
// claim. It is deliberately constructed inside the production Builder from
// the exact Topic factories' acquired results. There is no external scope
// factory that can reorder or exchange Topic-to-Deliverer bindings.
type workerExactEffectRouter struct {
	marker      byte
	deliverer   *workerTopicRouter
	topics      []string
	scopeDigest [sha256.Size]byte
}

type workerPluginRegistration struct {
	Snapshot     agentv1.EventPluginRef
	EffectTopics []string
	Factory      workerExecutorFactory
}

type workerEffectRegistration struct {
	Topic   string
	Factory workerEffectFactory
}

type workerDependencyCatalog struct {
	Database      workerDatabaseFactory
	ProviderUsage workerProviderUsageFactory
	Settlement    workerSettlementFactory
	Plugins       []workerPluginRegistration
	Effects       []workerEffectRegistration
}

// validatedWorkerDependencyPlan is a defensive static snapshot consumed by
// the P0-039 Builder. The Builder internally constructs exact Claim scope,
// Topic routing and fail-closed probe aggregation. Static validation is still
// not production authorization: it does not attest external factory behavior,
// build provenance or parity-ledger contents.
type validatedWorkerDependencyPlan struct {
	marker          byte
	integrityDigest [sha256.Size]byte
	configDigest    [sha256.Size]byte
	database        workerValidatedDatabaseConfig
	identity        ProcessIdentity
	rollout         config.AgentPlatformRollout
	plugins         []workerPluginRequirement
	effectTopics    []string
	providerUsage   workerProviderUsageKind
	settlement      workerSettlementKind
	catalog         workerDependencyCatalog
}

func (plan validatedWorkerDependencyPlan) intact() bool {
	rollout := plan.rollout
	if plan.marker != 1 || plan.configDigest == ([sha256.Size]byte{}) ||
		!plan.database.intact(plan.configDigest) ||
		(&rollout).ValidateWorkerRole() != nil || !workerRoleIntent(rollout).WorkerEnabled ||
		!validPrintableWorkerField(plan.identity.WorkerID, agentturn.MaxWorkerIDBytes) ||
		!validCanonicalSHA256(plan.identity.BuildDigest) ||
		plan.identity.BuildDigest == "sha256:"+hex.EncodeToString(plan.configDigest[:]) ||
		plan.providerUsage != workerProviderUsageJournalRegistryV1 ||
		plan.settlement != workerSettlementCreditsV1 {
		return false
	}
	plugins, topics, ok := normalizeWorkerPluginRequirements(plan.plugins)
	if !ok || !equalWorkerPluginRequirements(plugins, plan.plugins) ||
		!equalWorkerStrings(topics, plan.effectTopics) ||
		!workerPluginCatalogExactlyCovers(plugins, plan.catalog.Plugins) ||
		!workerEffectCatalogExactlyCovers(topics, plan.catalog.Effects) {
		return false
	}
	if plan.catalog.Database == nil || plan.catalog.ProviderUsage == nil ||
		plan.catalog.Settlement == nil {
		return false
	}
	// Compute the checksum only after every variable-length field has passed
	// its bound, so an accidental in-package mutation cannot cause unbounded
	// hashing before structural rejection.
	return plan.integrityDigest == workerDependencyPlanIntegrityDigest(plan)
}

// validateWorkerDependencyPlan is pure: it performs no file, database,
// network, provider, process, probe or goroutine operation and does not invoke
// a single registered factory. Errors are a closed, secret-free set.
func validateWorkerDependencyPlan(
	snapshot workerStartupSnapshot,
	build workerBuildIdentity,
	requested workerDependencyPlan,
	catalog workerDependencyCatalog,
) (validatedWorkerDependencyPlan, error) {
	database, ok := validateProductionWorkerSnapshot(snapshot)
	if !ok {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyPlanInvalid
	}
	identity, ok := validProductionBuildIdentity(snapshot, build)
	if !ok {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyIdentityUnavailable
	}

	plugins, topics, ok := normalizeWorkerPluginRequirements(requested.Plugins)
	if !ok {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyPluginUnavailable
	}
	if requested.Settlement != workerSettlementCreditsV1 {
		return validatedWorkerDependencyPlan{}, errWorkerDependencySettlementUnavailable
	}
	if requested.ProviderUsage != workerProviderUsageJournalRegistryV1 {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyProviderUsageUnavailable
	}
	if !workerPluginCatalogExactlyCovers(plugins, catalog.Plugins) {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyPluginUnavailable
	}
	if !workerEffectCatalogExactlyCovers(topics, catalog.Effects) {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyEffectUnavailable
	}
	if catalog.Database == nil {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyDatabaseUnavailable
	}
	if catalog.ProviderUsage == nil {
		return validatedWorkerDependencyPlan{}, errWorkerDependencyProviderUsageUnavailable
	}
	if catalog.Settlement == nil {
		return validatedWorkerDependencyPlan{}, errWorkerDependencySettlementUnavailable
	}

	validated := validatedWorkerDependencyPlan{
		marker:        1,
		configDigest:  snapshot.digest,
		database:      database,
		identity:      identity,
		rollout:       snapshot.rollout,
		plugins:       plugins,
		effectTopics:  topics,
		providerUsage: requested.ProviderUsage,
		settlement:    requested.Settlement,
		catalog:       copyWorkerDependencyCatalog(catalog),
	}
	validated.integrityDigest = workerDependencyPlanIntegrityDigest(validated)
	return validated, nil
}

func validateProductionWorkerSnapshot(snapshot workerStartupSnapshot) (workerValidatedDatabaseConfig, bool) {
	if snapshot.digest == ([sha256.Size]byte{}) || snapshot.checkDatabase ||
		snapshot.allowDatabasePlaintext || !snapshot.WorkerEnabled() {
		return workerValidatedDatabaseConfig{}, false
	}
	switch snapshot.source {
	case workerConfigSourceCommandLine, workerConfigSourceEnvironment, workerConfigSourceLocal:
	default:
		return workerValidatedDatabaseConfig{}, false
	}
	if err := (&snapshot.rollout).ValidateWorkerRole(); err != nil {
		return workerValidatedDatabaseConfig{}, false
	}
	// This parser is still pure but enforces the production driver's complete
	// host/port/TLS/query-option and pool policy, not only YAML required fields.
	parsedSettings, err := newWorkerMySQLSettings(snapshot.mysql)
	if err != nil {
		return workerValidatedDatabaseConfig{}, false
	}
	settings, ok := freezeWorkerMySQLSettings(parsedSettings)
	if !ok {
		return workerValidatedDatabaseConfig{}, false
	}
	return workerValidatedDatabaseConfig{
		marker: 1, configDigest: snapshot.digest, settings: settings,
	}, true
}

func validProductionBuildIdentity(snapshot workerStartupSnapshot, build workerBuildIdentity) (ProcessIdentity, bool) {
	identity := build.identity
	if build.provenance != workerBuildIdentityFromArtifact ||
		!validPrintableWorkerField(identity.WorkerID, agentturn.MaxWorkerIDBytes) ||
		!validCanonicalSHA256(identity.BuildDigest) {
		return ProcessIdentity{}, false
	}
	// The two digests have different authorities. Explicitly rejecting their
	// equality catches the most likely accidental wiring error deterministically.
	configDigest := "sha256:" + hex.EncodeToString(snapshot.digest[:])
	if identity.BuildDigest == configDigest {
		return ProcessIdentity{}, false
	}
	return identity, true
}

func validPrintableWorkerField(value string, maximum int) bool {
	if value == "" || len(value) > maximum || value != strings.TrimSpace(value) {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func validCanonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := strings.TrimPrefix(value, "sha256:")
	if digest != strings.ToLower(digest) {
		return false
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	for _, octet := range decoded {
		if octet != 0 {
			return true
		}
	}
	return false
}

func normalizeWorkerPluginRequirements(input []workerPluginRequirement) (
	[]workerPluginRequirement,
	[]string,
	bool,
) {
	if len(input) == 0 || len(input) > maxWorkerProductionPlugins {
		return nil, nil, false
	}
	plugins := make([]workerPluginRequirement, 0, len(input))
	pluginIDs := make(map[string]struct{}, len(input))
	snapshots := make(map[string]struct{}, len(input))
	allTopics := make(map[string]struct{})
	for _, requirement := range input {
		if !validWorkerPluginSnapshot(requirement.Snapshot) ||
			requirement.ExecutionTimeout <= 0 || requirement.ExecutionTimeout > maxWorkerExecutionTimeout ||
			requirement.ProgressTimeout <= 0 || requirement.ProgressTimeout > requirement.ExecutionTimeout ||
			!validWorkerPromotion(requirement.Snapshot, requirement.Promotion) {
			return nil, nil, false
		}
		// The current registry dispatches by ID. Even with an exact claim scope,
		// two releases sharing one ID cannot be routed safely by this build.
		if _, duplicate := pluginIDs[requirement.Snapshot.ID]; duplicate {
			return nil, nil, false
		}
		pluginIDs[requirement.Snapshot.ID] = struct{}{}
		key := workerPluginSnapshotKey(requirement.Snapshot)
		if _, duplicate := snapshots[key]; duplicate {
			return nil, nil, false
		}
		snapshots[key] = struct{}{}

		topics, ok := normalizeWorkerTopics(requirement.EffectTopics)
		if !ok {
			return nil, nil, false
		}
		for _, topic := range topics {
			allTopics[topic] = struct{}{}
		}
		copyOfRequirement := requirement
		copyOfRequirement.EffectTopics = topics
		plugins = append(plugins, copyOfRequirement)
	}
	if len(allTopics) == 0 || len(allTopics) > maxWorkerEffectTopics {
		// An empty Topics filter means "claim every topic" in the current
		// outbox API. It cannot safely represent a no-effects production plan.
		return nil, nil, false
	}
	sort.Slice(plugins, func(left, right int) bool {
		return workerPluginSnapshotKey(plugins[left].Snapshot) < workerPluginSnapshotKey(plugins[right].Snapshot)
	})
	topics := make([]string, 0, len(allTopics))
	for topic := range allTopics {
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	return plugins, topics, true
}

func validWorkerPluginSnapshot(snapshot agentv1.EventPluginRef) bool {
	if snapshot.Validate() != nil {
		return false
	}
	for _, field := range []string{snapshot.ID, snapshot.Version, snapshot.ReleaseDigest} {
		if len(field) > agentturn.MaxPluginFieldBytes || !validPrintableWorkerField(field, agentturn.MaxPluginFieldBytes) {
			return false
		}
	}
	return validCanonicalSHA256(snapshot.ReleaseDigest)
}

func validWorkerPromotion(snapshot agentv1.EventPluginRef, evidence workerPluginPromotionEvidence) bool {
	return evidence.marker == 1 && evidence.snapshot == snapshot && validCanonicalSHA256(evidence.ledgerDigest)
}

func normalizeWorkerTopics(input []string) ([]string, bool) {
	if len(input) > maxWorkerEffectTopics {
		return nil, false
	}
	seen := make(map[string]struct{}, len(input))
	topics := make([]string, 0, len(input))
	for _, topic := range input {
		if !validPrintableWorkerField(topic, maxWorkerEffectTopicBytes) || strings.ContainsAny(topic, "/\\?#%") {
			return nil, false
		}
		if _, duplicate := seen[topic]; duplicate {
			return nil, false
		}
		seen[topic] = struct{}{}
		topics = append(topics, topic)
	}
	sort.Strings(topics)
	return topics, true
}

func workerPluginCatalogExactlyCovers(
	required []workerPluginRequirement,
	registered []workerPluginRegistration,
) bool {
	if len(required) != len(registered) {
		return false
	}
	bySnapshot := make(map[string]workerPluginRegistration, len(registered))
	for _, registration := range registered {
		if registration.Factory == nil || !validWorkerPluginSnapshot(registration.Snapshot) {
			return false
		}
		key := workerPluginSnapshotKey(registration.Snapshot)
		if _, duplicate := bySnapshot[key]; duplicate {
			return false
		}
		topics, ok := normalizeWorkerTopics(registration.EffectTopics)
		if !ok {
			return false
		}
		registration.EffectTopics = topics
		bySnapshot[key] = registration
	}
	for _, requirement := range required {
		registration, found := bySnapshot[workerPluginSnapshotKey(requirement.Snapshot)]
		if !found || !equalWorkerStrings(requirement.EffectTopics, registration.EffectTopics) {
			return false
		}
	}
	return true
}

func workerEffectCatalogExactlyCovers(required []string, registered []workerEffectRegistration) bool {
	if len(required) != len(registered) {
		return false
	}
	factories := make(map[string]workerEffectFactory, len(registered))
	for _, registration := range registered {
		if !validPrintableWorkerField(registration.Topic, maxWorkerEffectTopicBytes) ||
			strings.ContainsAny(registration.Topic, "/\\?#%") || registration.Factory == nil {
			return false
		}
		if _, duplicate := factories[registration.Topic]; duplicate {
			return false
		}
		factories[registration.Topic] = registration.Factory
	}
	for _, topic := range required {
		if factories[topic] == nil {
			return false
		}
	}
	return true
}

func workerPluginSnapshotKey(snapshot agentv1.EventPluginRef) string {
	return snapshot.ID + "\x00" + snapshot.Version + "\x00" + snapshot.ReleaseDigest
}

func equalWorkerStrings(left, right []string) bool {
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

func equalWorkerPluginRequirements(left, right []workerPluginRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Snapshot != right[index].Snapshot ||
			left[index].ExecutionTimeout != right[index].ExecutionTimeout ||
			left[index].ProgressTimeout != right[index].ProgressTimeout ||
			left[index].Promotion != right[index].Promotion ||
			!equalWorkerStrings(left[index].EffectTopics, right[index].EffectTopics) {
			return false
		}
	}
	return true
}

func copyWorkerDependencyCatalog(catalog workerDependencyCatalog) workerDependencyCatalog {
	copyOfCatalog := catalog
	copyOfCatalog.Plugins = make([]workerPluginRegistration, len(catalog.Plugins))
	for index, registration := range catalog.Plugins {
		copyOfCatalog.Plugins[index] = registration
		copyOfCatalog.Plugins[index].EffectTopics = append([]string(nil), registration.EffectTopics...)
		sort.Strings(copyOfCatalog.Plugins[index].EffectTopics)
	}
	sort.Slice(copyOfCatalog.Plugins, func(left, right int) bool {
		return workerPluginSnapshotKey(copyOfCatalog.Plugins[left].Snapshot) <
			workerPluginSnapshotKey(copyOfCatalog.Plugins[right].Snapshot)
	})
	copyOfCatalog.Effects = append([]workerEffectRegistration(nil), catalog.Effects...)
	sort.Slice(copyOfCatalog.Effects, func(left, right int) bool {
		return copyOfCatalog.Effects[left].Topic < copyOfCatalog.Effects[right].Topic
	})
	return copyOfCatalog
}

// workerDependencyPlanIntegrityDigest detects accidental in-package mutation
// of a validated snapshot. It is an integrity checksum, not a signature and
// not evidence that any registered factory implements its declared behavior.
func workerDependencyPlanIntegrityDigest(plan validatedWorkerDependencyPlan) [sha256.Size]byte {
	payload := make([]byte, 0, 2048)
	payload = append(payload, plan.configDigest[:]...)
	payload = appendWorkerDatabaseIntegrity(payload, plan.database)
	payload = appendWorkerIntegrityString(payload, plan.identity.WorkerID)
	payload = appendWorkerIntegrityString(payload, plan.identity.BuildDigest)
	payload = appendWorkerIntegrityString(payload, string(plan.rollout.Durable.Worker))
	payload = appendWorkerIntegrityFactory(payload, plan.rollout.Readiness.SQLStore)
	payload = appendWorkerIntegrityFactory(payload, plan.rollout.Readiness.WorkerLeaseFencing)
	payload = appendWorkerIntegrityFactory(payload, plan.rollout.Readiness.TransactionalOutbox)
	payload = appendWorkerIntegrityFactory(payload, plan.rollout.Readiness.ExactlyOnceSettlement)
	payload = appendWorkerIntegrityString(payload, string(plan.providerUsage))
	payload = appendWorkerIntegrityString(payload, string(plan.settlement))
	payload = appendWorkerIntegrityUint64(payload, uint64(len(plan.plugins)))
	for _, plugin := range plan.plugins {
		payload = appendWorkerIntegrityString(payload, plugin.Snapshot.ID)
		payload = appendWorkerIntegrityString(payload, plugin.Snapshot.Version)
		payload = appendWorkerIntegrityString(payload, plugin.Snapshot.ReleaseDigest)
		payload = appendWorkerIntegrityUint64(payload, uint64(plugin.ExecutionTimeout))
		payload = appendWorkerIntegrityUint64(payload, uint64(plugin.ProgressTimeout))
		payload = append(payload, plugin.Promotion.marker)
		payload = appendWorkerIntegrityString(payload, plugin.Promotion.snapshot.ID)
		payload = appendWorkerIntegrityString(payload, plugin.Promotion.snapshot.Version)
		payload = appendWorkerIntegrityString(payload, plugin.Promotion.snapshot.ReleaseDigest)
		payload = appendWorkerIntegrityString(payload, plugin.Promotion.ledgerDigest)
		payload = appendWorkerIntegrityStrings(payload, plugin.EffectTopics)
	}
	payload = appendWorkerIntegrityStrings(payload, plan.effectTopics)
	payload = appendWorkerIntegrityFactory(payload, plan.catalog.Database != nil)
	payload = appendWorkerIntegrityFactory(payload, plan.catalog.ProviderUsage != nil)
	payload = appendWorkerIntegrityFactory(payload, plan.catalog.Settlement != nil)
	payload = appendWorkerIntegrityUint64(payload, uint64(len(plan.catalog.Plugins)))
	for _, registration := range plan.catalog.Plugins {
		payload = appendWorkerIntegrityString(payload, registration.Snapshot.ID)
		payload = appendWorkerIntegrityString(payload, registration.Snapshot.Version)
		payload = appendWorkerIntegrityString(payload, registration.Snapshot.ReleaseDigest)
		payload = appendWorkerIntegrityStrings(payload, registration.EffectTopics)
		payload = appendWorkerIntegrityFactory(payload, registration.Factory != nil)
	}
	payload = appendWorkerIntegrityUint64(payload, uint64(len(plan.catalog.Effects)))
	for _, registration := range plan.catalog.Effects {
		payload = appendWorkerIntegrityString(payload, registration.Topic)
		payload = appendWorkerIntegrityFactory(payload, registration.Factory != nil)
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

func appendWorkerDatabaseIntegrity(payload []byte, database workerValidatedDatabaseConfig) []byte {
	payload = append(payload, database.marker)
	payload = append(payload, database.configDigest[:]...)
	settings := database.settings
	payload = appendWorkerIntegrityString(payload, settings.username)
	payload = appendWorkerIntegrityString(payload, settings.password)
	payload = appendWorkerIntegrityString(payload, settings.address)
	payload = appendWorkerIntegrityString(payload, settings.databaseName)
	payload = appendWorkerIntegrityFactory(payload, settings.requireTLS)
	payload = appendWorkerIntegrityString(payload, settings.tlsServerName)
	payload = appendWorkerIntegrityUint64(payload, uint64(settings.timeout))
	payload = appendWorkerIntegrityUint64(payload, uint64(settings.readTimeout))
	payload = appendWorkerIntegrityUint64(payload, uint64(settings.writeTimeout))
	payload = appendWorkerIntegrityUint64(payload, uint64(settings.maxOpen))
	payload = appendWorkerIntegrityUint64(payload, uint64(settings.maxIdle))
	return payload
}

func appendWorkerIntegrityStrings(payload []byte, values []string) []byte {
	payload = appendWorkerIntegrityUint64(payload, uint64(len(values)))
	for _, value := range values {
		payload = appendWorkerIntegrityString(payload, value)
	}
	return payload
}

func appendWorkerIntegrityString(payload []byte, value string) []byte {
	payload = appendWorkerIntegrityUint64(payload, uint64(len(value)))
	return append(payload, value...)
}

func appendWorkerIntegrityUint64(payload []byte, value uint64) []byte {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	return append(payload, encoded[:]...)
}

func appendWorkerIntegrityFactory(payload []byte, present bool) []byte {
	if present {
		return append(payload, 1)
	}
	return append(payload, 0)
}
