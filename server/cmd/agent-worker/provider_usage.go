package main

import (
	"crypto/sha256"
	"errors"
	"sort"

	"gorm.io/gorm"

	agentv1 "server/contracts/agent/v1"
	"server/service/agentturn"
)

var (
	errWorkerProviderUsageBindingInvalid = errors.New("agent-worker provider usage binding is invalid")
	errWorkerPluginProviderUsageInvalid  = errors.New("agent-worker plugin provider usage scope is invalid")
)

// workerProviderUsageRecorderRegistration binds one core scoped Recorder to
// the immutable release/source coordinates used to create it. One exact
// Plugin release may have multiple entries because its Meter Release can
// authorize several Provider/source/schema registrations.
type workerProviderUsageRecorderRegistration struct {
	MeterReleaseID string
	Source         agentturn.ProviderUsageSourceRegistration
	Recorder       *agentturn.ProviderUsageRecorder
}

type workerProviderUsageRecorderRegistry map[string][]workerProviderUsageRecorderRegistration

// workerProviderUsageIdentity is an opaque pointer-identity token. It carries
// no database, Store, Journal or Recorder and is safe to include in a
// per-Plugin facade solely for provenance matching.
type workerProviderUsageIdentity struct {
	marker byte
}

// workerProviderUsageBinding proves that one core ProviderUsageJournal and
// its complete scoped-Recorder registry were constructed for the same
// database, SQLStore and exact Plugin release set used by production runtime.
type workerProviderUsageBinding struct {
	marker      byte
	database    *gorm.DB
	store       *agentturn.SQLStore
	journal     *agentturn.ProviderUsageJournal
	plugins     []agentv1.EventPluginRef
	recorders   workerProviderUsageRecorderRegistry
	scopeDigest [sha256.Size]byte
	identity    *workerProviderUsageIdentity
	seal        *workerProviderUsageBindingSeal
}

type workerProviderUsageBindingSeal struct {
	marker      byte
	binding     *workerProviderUsageBinding
	database    *gorm.DB
	store       *agentturn.SQLStore
	journal     *agentturn.ProviderUsageJournal
	plugins     []agentv1.EventPluginRef
	recorders   workerProviderUsageRecorderRegistry
	scopeDigest [sha256.Size]byte
	identity    *workerProviderUsageIdentity
}

func newWorkerProviderUsageBinding(
	database *gorm.DB,
	store *agentturn.SQLStore,
	journal *agentturn.ProviderUsageJournal,
	plugins []agentv1.EventPluginRef,
	recorders workerProviderUsageRecorderRegistry,
) (*workerProviderUsageBinding, error) {
	normalizedPlugins, ok := normalizeWorkerPluginSnapshots(plugins)
	if database == nil || store == nil || journal == nil ||
		!journal.MatchesStore(store) || !ok {
		return nil, errWorkerProviderUsageBindingInvalid
	}
	normalizedRecorders, ok := normalizeWorkerProviderUsageRecorderRegistry(
		journal, normalizedPlugins, recorders,
	)
	if !ok {
		return nil, errWorkerProviderUsageBindingInvalid
	}
	binding := &workerProviderUsageBinding{
		marker: 1, database: database, store: store, journal: journal,
		plugins: normalizedPlugins, recorders: normalizedRecorders,
		identity: &workerProviderUsageIdentity{marker: 1},
	}
	binding.scopeDigest = workerProviderUsageRegistryDigest(
		binding.plugins, binding.recorders,
	)
	binding.seal = &workerProviderUsageBindingSeal{
		marker: 1, binding: binding, database: database, store: store, journal: journal,
		plugins:     append([]agentv1.EventPluginRef(nil), normalizedPlugins...),
		recorders:   copyWorkerProviderUsageRecorderRegistry(normalizedRecorders),
		scopeDigest: binding.scopeDigest, identity: binding.identity,
	}
	return binding, nil
}

func (binding *workerProviderUsageBinding) intact(
	database *gorm.DB,
	store *agentturn.SQLStore,
	expected []agentv1.EventPluginRef,
) bool {
	normalizedPlugins, ok := normalizeWorkerPluginSnapshots(expected)
	if binding == nil || !ok || binding.marker != 1 || database == nil || store == nil ||
		binding.database != database || binding.store != store || binding.journal == nil ||
		!binding.journal.MatchesStore(store) || binding.identity == nil || binding.identity.marker != 1 ||
		!equalWorkerPluginSnapshots(binding.plugins, normalizedPlugins) || binding.seal == nil ||
		binding.seal.marker != 1 || binding.seal.binding != binding ||
		binding.seal.database != binding.database || binding.seal.store != binding.store ||
		binding.seal.journal != binding.journal || binding.seal.identity != binding.identity ||
		!equalWorkerPluginSnapshots(binding.seal.plugins, binding.plugins) {
		return false
	}
	normalizedRecorders, ok := normalizeWorkerProviderUsageRecorderRegistry(
		binding.journal, normalizedPlugins, binding.recorders,
	)
	if !ok || !equalWorkerProviderUsageRecorderRegistry(binding.recorders, normalizedRecorders) ||
		!equalWorkerProviderUsageRecorderRegistry(binding.seal.recorders, binding.recorders) {
		return false
	}
	digest := workerProviderUsageRegistryDigest(normalizedPlugins, normalizedRecorders)
	return binding.scopeDigest == digest && binding.seal.scopeDigest == binding.scopeDigest
}

func (binding *workerProviderUsageBinding) matchesStore(
	store *agentturn.SQLStore,
	expected []agentv1.EventPluginRef,
) bool {
	return binding != nil && binding.intact(binding.database, store, expected)
}

func normalizeWorkerProviderUsageRecorderRegistry(
	journal *agentturn.ProviderUsageJournal,
	plugins []agentv1.EventPluginRef,
	input workerProviderUsageRecorderRegistry,
) (workerProviderUsageRecorderRegistry, bool) {
	if journal == nil || len(input) != len(plugins) {
		return nil, false
	}
	output := make(workerProviderUsageRecorderRegistry, len(plugins))
	for _, plugin := range plugins {
		key := workerPluginSnapshotKey(plugin)
		registrations, found := input[key]
		if !found || len(registrations) < 1 || len(registrations) > agentturn.MaxProviderUsageSources {
			return nil, false
		}
		copyOfRegistrations := append([]workerProviderUsageRecorderRegistration(nil), registrations...)
		meterReleaseID := copyOfRegistrations[0].MeterReleaseID
		seenSources := make(map[string]struct{}, len(copyOfRegistrations))
		for _, registration := range copyOfRegistrations {
			if registration.MeterReleaseID != meterReleaseID || registration.Source.Validate() != nil ||
				dependencyMissing(registration.Recorder) ||
				!registration.Recorder.MatchesScope(
					journal, plugin, registration.MeterReleaseID, registration.Source,
				) {
				return nil, false
			}
			if _, duplicate := seenSources[registration.Source.RegistrationDigest]; duplicate {
				return nil, false
			}
			seenSources[registration.Source.RegistrationDigest] = struct{}{}
		}
		sort.Slice(copyOfRegistrations, func(left, right int) bool {
			return copyOfRegistrations[left].Source.RegistrationDigest <
				copyOfRegistrations[right].Source.RegistrationDigest
		})
		output[key] = copyOfRegistrations
	}
	return output, true
}

func copyWorkerProviderUsageRecorderRegistry(
	input workerProviderUsageRecorderRegistry,
) workerProviderUsageRecorderRegistry {
	if input == nil {
		return nil
	}
	output := make(workerProviderUsageRecorderRegistry, len(input))
	for key, registrations := range input {
		output[key] = append([]workerProviderUsageRecorderRegistration(nil), registrations...)
	}
	return output
}

func equalWorkerProviderUsageRecorderRegistry(
	left workerProviderUsageRecorderRegistry,
	right workerProviderUsageRecorderRegistry,
) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftRegistrations := range left {
		rightRegistrations, found := right[key]
		if !found || len(leftRegistrations) != len(rightRegistrations) {
			return false
		}
		for index := range leftRegistrations {
			if leftRegistrations[index] != rightRegistrations[index] {
				return false
			}
		}
	}
	return true
}

func workerProviderUsageRegistryDigest(
	plugins []agentv1.EventPluginRef,
	recorders workerProviderUsageRecorderRegistry,
) [sha256.Size]byte {
	payload := appendWorkerIntegrityUint64(nil, uint64(len(plugins)))
	for _, plugin := range plugins {
		payload = appendWorkerIntegrityString(payload, plugin.ID)
		payload = appendWorkerIntegrityString(payload, plugin.Version)
		payload = appendWorkerIntegrityString(payload, plugin.ReleaseDigest)
		registrations := recorders[workerPluginSnapshotKey(plugin)]
		payload = appendWorkerIntegrityUint64(payload, uint64(len(registrations)))
		for _, registration := range registrations {
			payload = appendWorkerIntegrityString(payload, registration.MeterReleaseID)
			payload = appendWorkerIntegrityString(payload, registration.Source.RegistrationDigest)
			payload = appendWorkerIntegrityFactory(payload, !dependencyMissing(registration.Recorder))
		}
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

func equalWorkerPluginSnapshots(left, right []agentv1.EventPluginRef) bool {
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

// workerPluginProviderUsage is the only provider/journal value an executor
// factory receives. It contains only the core Recorders already scoped to one
// exact Plugin release; it contains no raw ProviderUsageJournal or SQLStore.
type workerPluginProviderUsage struct {
	marker      byte
	plugin      agentv1.EventPluginRef
	recorders   []workerProviderUsageRecorderRegistration
	identity    *workerProviderUsageIdentity
	scopeDigest [sha256.Size]byte
}

func newWorkerPluginProviderUsage(
	binding *workerProviderUsageBinding,
	plugin agentv1.EventPluginRef,
) (workerPluginProviderUsage, error) {
	if binding == nil || !binding.matchesStore(binding.store, binding.plugins) ||
		!workerPluginSnapshotCovered(binding.plugins, plugin) {
		return workerPluginProviderUsage{}, errWorkerPluginProviderUsageInvalid
	}
	recorders := append([]workerProviderUsageRecorderRegistration(nil),
		binding.recorders[workerPluginSnapshotKey(plugin)]...)
	if len(recorders) == 0 {
		return workerPluginProviderUsage{}, errWorkerPluginProviderUsageInvalid
	}
	return workerPluginProviderUsage{
		marker: 1, plugin: plugin, recorders: recorders, identity: binding.identity,
		scopeDigest: workerPluginProviderUsageScopeDigest(binding.scopeDigest, plugin, recorders),
	}, nil
}

func (scope workerPluginProviderUsage) intact(
	binding *workerProviderUsageBinding,
	expected agentv1.EventPluginRef,
) bool {
	if binding == nil || !binding.matchesStore(binding.store, binding.plugins) || scope.marker != 1 ||
		scope.plugin != expected || scope.identity != binding.identity ||
		!workerPluginSnapshotCovered(binding.plugins, expected) {
		return false
	}
	want := binding.recorders[workerPluginSnapshotKey(expected)]
	if len(scope.recorders) == 0 || len(scope.recorders) != len(want) {
		return false
	}
	for index, registration := range scope.recorders {
		if registration != want[index] || !registration.Recorder.MatchesScope(
			binding.journal, expected, registration.MeterReleaseID, registration.Source,
		) {
			return false
		}
	}
	return scope.scopeDigest == workerPluginProviderUsageScopeDigest(
		binding.scopeDigest, expected, scope.recorders,
	)
}

func workerPluginSnapshotCovered(
	plugins []agentv1.EventPluginRef,
	expected agentv1.EventPluginRef,
) bool {
	if !validWorkerPluginSnapshot(expected) {
		return false
	}
	for _, plugin := range plugins {
		if plugin == expected {
			return true
		}
	}
	return false
}

func workerPluginProviderUsageScopeDigest(
	bindingDigest [sha256.Size]byte,
	plugin agentv1.EventPluginRef,
	recorders []workerProviderUsageRecorderRegistration,
) [sha256.Size]byte {
	payload := append([]byte(nil), bindingDigest[:]...)
	payload = appendWorkerIntegrityString(payload, plugin.ID)
	payload = appendWorkerIntegrityString(payload, plugin.Version)
	payload = appendWorkerIntegrityString(payload, plugin.ReleaseDigest)
	payload = appendWorkerIntegrityUint64(payload, uint64(len(recorders)))
	for _, registration := range recorders {
		payload = appendWorkerIntegrityString(payload, registration.MeterReleaseID)
		payload = appendWorkerIntegrityString(payload, registration.Source.RegistrationDigest)
		payload = appendWorkerIntegrityFactory(payload, !dependencyMissing(registration.Recorder))
	}
	digest := sha256.Sum256(payload)
	clear(payload)
	return digest
}

// workerJournalBackedSettlementAuthority is stronger than the core usage
// authority: exact production composition also requires a cmd-owned proof
// that Settlement uses the very same ProviderUsage binding acquired by the
// Builder. Public candidate Compose intentionally keeps its older interface.
type workerJournalBackedSettlementAuthority interface {
	agentturn.SettlementReviewProviderUsageAuthority
	matchesWorkerProviderUsageBinding(*workerProviderUsageBinding) bool
}
