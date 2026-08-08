package agentturn

import (
	"context"
	"strings"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
)

// fullyComposed builds the composition a production deployment would need.
func fullyComposed(t *testing.T) PlatformComponents {
	t.Helper()
	_, store, _, _ := newSQLClaimNextFixture(t, "readiness")
	store.WithSettlementAuthority(newTestSettlementAuthority())

	stream, err := NewTurnEventStream(store, EventStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewWorker(store, testExecutor{
		run: func(context.Context, ExecutionSession) (agentv1.TurnStatus, error) {
			return agentv1.TurnStatusCompleted, nil
		},
	}, WorkerOptions{WorkerID: "w", WorkerBuildDigest: "sha256:w"})
	if err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(store, store, ReconcilerOptions{
		ReconcilerID: "r", ReconcilerBuildDigest: "sha256:r",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := NewEffectDispatcher(store, &testDeliverer{}, EffectDispatcherOptions{
		LeaseOwnerID: "d", IdleBackoff: time.Second, DeliveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	settlement, _ := store.settlementAuthority()
	return PlatformComponents{
		Store: store, Execution: store, Reclaim: store, Reconcile: store, Outbox: store,
		Stream: stream, Worker: worker, Reconciler: reconciler, Dispatcher: dispatcher,
		Settlement: settlement,
	}
}

func allDeclared() DeclaredReadiness {
	return DeclaredReadiness{
		TokenRolloverComplete: true, ActiveDeviceSessions: true, SQLStore: true,
		AtomicLiveEventStream: true, WorkerLeaseFencing: true,
		TransactionalOutbox: true, ExactlyOnceSettlement: true,
	}
}

func fullIntent() RolloutIntent {
	return RolloutIntent{
		PublicAPIEnabled: true, WorkerEnabled: true,
		DesktopDurable: true, CredentialEnforcement: true,
	}
}

func blockerContaining(report ReadinessReport, fragment string) bool {
	for _, blocker := range report.Blockers {
		if strings.Contains(blocker, fragment) {
			return true
		}
	}
	return false
}

func TestReadinessRefusesConfigurationThatOverclaimsMissingDependencies(t *testing.T) {
	// This is the whole point: a YAML block asserting every capability, with
	// nothing installed, must not produce a ready platform.
	report := DeriveReadiness(fullIntent(), allDeclared(), PlatformComponents{})
	if report.Ready {
		t.Fatal("an empty composition was reported ready because configuration said so")
	}
	// Sorted, so the report is stable enough to diff between deploys.
	want := []string{
		"atomic_live_event_stream", "exactly_once_settlement", "sql_store",
		"transactional_outbox", "worker_lease_fencing",
	}
	if len(report.Overclaimed) != len(want) {
		t.Fatalf("overclaimed = %v, want %v", report.Overclaimed, want)
	}
	for index, capability := range want {
		if report.Overclaimed[index] != capability {
			t.Fatalf("overclaimed = %v, want %v", report.Overclaimed, want)
		}
	}
	if (report.Derived != DerivedReadiness{}) {
		t.Fatalf("derived = %+v, want everything false", report.Derived)
	}
}

func TestReadinessIsDerivedFromObjectsNotConfiguration(t *testing.T) {
	components := fullyComposed(t)

	// Configuration can lower readiness...
	quiet := DeriveReadiness(RolloutIntent{}, DeclaredReadiness{}, components)
	if !quiet.Ready {
		t.Fatalf("a fully composed platform serving no traffic was blocked: %v", quiet.Blockers)
	}
	if len(quiet.Overclaimed) != 0 {
		t.Fatalf("declaring nothing produced overclaims: %v", quiet.Overclaimed)
	}
	// ...but the derived facts still report what is actually installed, so an
	// operator can see the gap rather than having it silently reinterpreted.
	if quiet.Derived != (DerivedReadiness{
		SQLStore: true, AtomicLiveEventStream: true, WorkerLeaseFencing: true,
		TransactionalOutbox: true, ExactlyOnceSettlement: true,
	}) {
		t.Fatalf("derived = %+v, want every capability present", quiet.Derived)
	}

	// ...and it can never raise it: a full intent on a full composition is the
	// only way to be ready for traffic.
	full := DeriveReadiness(fullIntent(), allDeclared(), components)
	if !full.Ready {
		t.Fatalf("a fully composed platform was blocked: %v", full.Blockers)
	}
}

func TestReadinessRejectsTheInMemoryHarnessWhateverConfigurationSays(t *testing.T) {
	components := fullyComposed(t)
	components.Store = NewMemoryStore()

	report := DeriveReadiness(RolloutIntent{}, DeclaredReadiness{}, components)
	if report.Ready {
		t.Fatal("the in-memory contract harness was accepted as a production store")
	}
	if !blockerContaining(report, "in-memory contract harness") {
		t.Fatalf("blockers = %v, want an explicit memory-store refusal", report.Blockers)
	}
	if report.Derived.SQLStore {
		t.Fatal("the memory harness was derived as a durable SQL store")
	}
}

func TestReadinessRequiresTheDependenciesThatMakeEachTrafficKindSafe(t *testing.T) {
	for name, tc := range map[string]struct {
		intent  RolloutIntent
		mutate  func(*PlatformComponents)
		blocker string
	}{
		"worker without a reconciler strands exhausted turns": {
			intent:  RolloutIntent{WorkerEnabled: true},
			mutate:  func(c *PlatformComponents) { c.Reconciler = nil },
			blocker: "requires a reconciler",
		},
		"worker without a dispatcher never delivers committed effects": {
			intent:  RolloutIntent{WorkerEnabled: true},
			mutate:  func(c *PlatformComponents) { c.Dispatcher = nil },
			blocker: "effect outbox dispatcher",
		},
		"worker without a worker runtime fences nothing": {
			intent:  RolloutIntent{WorkerEnabled: true},
			mutate:  func(c *PlatformComponents) { c.Worker = nil },
			blocker: "execution store and a worker runtime",
		},
		"public api without a stream cannot serve attach": {
			intent:  RolloutIntent{PublicAPIEnabled: true, WorkerEnabled: true},
			mutate:  func(c *PlatformComponents) { c.Stream = nil },
			blocker: "atomic replay-to-live event stream",
		},
		"any traffic without settlement moves money incorrectly": {
			intent:  RolloutIntent{WorkerEnabled: true},
			mutate:  func(c *PlatformComponents) { c.Settlement = nil },
			blocker: "settlement authority",
		},
	} {
		components := fullyComposed(t)
		tc.mutate(&components)
		report := DeriveReadiness(tc.intent, DeclaredReadiness{}, components)
		if report.Ready {
			t.Fatalf("%s: reported ready", name)
		}
		if !blockerContaining(report, tc.blocker) {
			t.Fatalf("%s: blockers = %v, want one containing %q", name, report.Blockers, tc.blocker)
		}
	}
}

func TestReadinessRefusesToAcceptStartsNothingWillRun(t *testing.T) {
	components := fullyComposed(t)
	// Serving the public API with the worker disabled accepts Turns that no
	// executor will ever pick up.
	report := DeriveReadiness(RolloutIntent{PublicAPIEnabled: true}, DeclaredReadiness{}, components)
	if report.Ready {
		t.Fatal("the public API was enabled without worker execution")
	}
	if !blockerContaining(report, "strands turns") {
		t.Fatalf("blockers = %v, want the stranded-start refusal", report.Blockers)
	}

	// Desktop may not leave legacy transport without strict credentials.
	lax := fullIntent()
	lax.CredentialEnforcement = false
	laxReport := DeriveReadiness(lax, DeclaredReadiness{}, components)
	if laxReport.Ready {
		t.Fatal("desktop durable transport was allowed without credential enforcement")
	}
	if !blockerContaining(laxReport, "strict credential enforcement") {
		t.Fatalf("blockers = %v, want the credential refusal", laxReport.Blockers)
	}
}
