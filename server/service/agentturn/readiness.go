package agentturn

import (
	"fmt"
	"sort"
)

// PlatformComponents is what a composition root actually installed.
//
// Readiness is derived from these fields being non-nil, never from
// configuration. A nil member means the capability is genuinely absent, which
// is why this struct holds objects rather than booleans: a boolean can be
// wrong, an installed dependency cannot.
type PlatformComponents struct {
	Store      Store
	Execution  ExecutionStore
	Reclaim    ReclaimScanner
	Reconcile  ReconcileStore
	Outbox     EffectOutboxStore
	Stream     *TurnEventStream
	Worker     *Worker
	Reconciler *Reconciler
	Dispatcher *EffectDispatcher
	Settlement SettlementAuthority
}

// DeclaredReadiness mirrors the operator-facing readiness block as plain data.
//
// The kernel deliberately does not import runtime configuration: a caller maps
// its config into this struct. That keeps the dependency direction one-way and
// stops the readiness rules from becoming reachable from the composition root.
type DeclaredReadiness struct {
	TokenRolloverComplete bool
	ActiveDeviceSessions  bool
	SQLStore              bool
	AtomicLiveEventStream bool
	WorkerLeaseFencing    bool
	TransactionalOutbox   bool
	ExactlyOnceSettlement bool
}

// DerivedReadiness is what the installed composition can actually support.
type DerivedReadiness struct {
	SQLStore              bool
	AtomicLiveEventStream bool
	WorkerLeaseFencing    bool
	TransactionalOutbox   bool
	ExactlyOnceSettlement bool
}

// RolloutIntent is the traffic an operator asked for.
type RolloutIntent struct {
	// PublicAPIEnabled is true for canary or full durable public API.
	PublicAPIEnabled bool
	WorkerEnabled    bool
	// DesktopDurable is true when Desktop is asked to leave legacy transport.
	DesktopDurable bool
	// CredentialEnforcement is true when strict credential admission is on.
	CredentialEnforcement bool
}

func (intent RolloutIntent) enablesTraffic() bool {
	return intent.PublicAPIEnabled || intent.WorkerEnabled || intent.DesktopDurable
}

// ReadinessReport explains what may run and, more usefully, what may not.
type ReadinessReport struct {
	Derived DerivedReadiness
	// Overclaimed lists capabilities configuration asserted but the
	// composition does not provide. These are always blockers: a deployment
	// that believes it has exactly-once settlement when it does not is more
	// dangerous than one that knows it is missing.
	Overclaimed []string
	// Blockers is the ordered, human-readable reason set for refusing.
	Blockers []string
	// Ready reports whether the requested intent may be served.
	Ready bool
}

// DeriveReadiness answers "may this traffic run", from installed objects.
//
// The governing rule is one-way: configuration can only ever *lower* readiness,
// never raise it. A declared capability that the composition does not provide
// is an overclaim and blocks; a capability the composition provides but
// configuration disables is simply off. There is no path by which editing YAML
// makes an absent dependency appear.
func DeriveReadiness(intent RolloutIntent, declared DeclaredReadiness, components PlatformComponents) ReadinessReport {
	report := ReadinessReport{Derived: deriveCapabilities(components)}

	// A Turn store that cannot survive a restart is never production, whatever
	// anyone configured. This is checked first because every other capability
	// is meaningless on top of it.
	if _, isMemory := components.Store.(*MemoryStore); isMemory {
		report.Blockers = append(report.Blockers,
			"durable turn store is the in-memory contract harness, which must never back production or pilot traffic")
	}

	report.Overclaimed = overclaimedCapabilities(declared, report.Derived)
	for _, capability := range report.Overclaimed {
		report.Blockers = append(report.Blockers,
			fmt.Sprintf("configuration declares %q ready but no such dependency is installed", capability))
	}

	report.Blockers = append(report.Blockers, intentBlockers(intent, report.Derived, components)...)
	report.Ready = len(report.Blockers) == 0
	return report
}

func deriveCapabilities(components PlatformComponents) DerivedReadiness {
	_, durable := components.Store.(*SQLStore)
	return DerivedReadiness{
		SQLStore:              durable,
		AtomicLiveEventStream: components.Stream != nil,
		// Fencing is only real when something both arbitrates epochs and runs
		// them. An ExecutionStore with no Worker fences nothing.
		WorkerLeaseFencing: components.Execution != nil && components.Worker != nil,
		// An outbox nobody drains is a queue of undelivered side effects, not
		// a transactional outbox.
		TransactionalOutbox:   components.Outbox != nil && components.Dispatcher != nil,
		ExactlyOnceSettlement: components.Settlement != nil,
	}
}

func overclaimedCapabilities(declared DeclaredReadiness, derived DerivedReadiness) []string {
	overclaimed := map[string]bool{}
	if declared.SQLStore && !derived.SQLStore {
		overclaimed["sql_store"] = true
	}
	if declared.AtomicLiveEventStream && !derived.AtomicLiveEventStream {
		overclaimed["atomic_live_event_stream"] = true
	}
	if declared.WorkerLeaseFencing && !derived.WorkerLeaseFencing {
		overclaimed["worker_lease_fencing"] = true
	}
	if declared.TransactionalOutbox && !derived.TransactionalOutbox {
		overclaimed["transactional_outbox"] = true
	}
	if declared.ExactlyOnceSettlement && !derived.ExactlyOnceSettlement {
		overclaimed["exactly_once_settlement"] = true
	}
	names := make([]string, 0, len(overclaimed))
	for name := range overclaimed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func intentBlockers(intent RolloutIntent, derived DerivedReadiness, components PlatformComponents) []string {
	if !intent.enablesTraffic() {
		return nil
	}
	var blockers []string
	require := func(ok bool, reason string) {
		if !ok {
			blockers = append(blockers, reason)
		}
	}

	// Anything that moves real Turns needs durable storage and correct money.
	require(derived.SQLStore, "durable turn traffic requires an installed SQL turn store")
	require(derived.ExactlyOnceSettlement,
		"durable turn traffic requires an installed settlement authority; a terminal turn is not a settlement")

	if intent.WorkerEnabled {
		require(derived.WorkerLeaseFencing,
			"worker traffic requires both an execution store and a worker runtime")
		// Without a Reconciler a crashed Turn is reclaimed until its attempt
		// budget runs out and then stays non-terminal forever.
		require(components.Reconciler != nil && components.Reclaim != nil && components.Reconcile != nil,
			"worker traffic requires a reconciler; without one an exhausted turn never reaches a terminal state")
		require(derived.TransactionalOutbox,
			"worker traffic requires an effect outbox dispatcher; committed effects would otherwise never be delivered")
	}
	if intent.PublicAPIEnabled || intent.DesktopDurable {
		require(derived.AtomicLiveEventStream,
			"durable public API traffic requires an atomic replay-to-live event stream")
		// Serving Start without an executor accepts work nothing will run.
		require(intent.WorkerEnabled && derived.WorkerLeaseFencing,
			"durable public API traffic requires worker execution to be enabled; accepting starts nothing will run strands turns")
	}
	if intent.DesktopDurable {
		require(intent.CredentialEnforcement,
			"desktop durable transport requires strict credential enforcement")
	}
	return blockers
}
