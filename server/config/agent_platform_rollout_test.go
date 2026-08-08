package config

import (
	"strconv"
	"testing"
)

func TestAgentPlatformRolloutDefaultsClosedWithoutChangingLegacyTransport(t *testing.T) {
	got := EffectiveAgentPlatformRollout(nil)
	if got.Credential.DesktopResource != CredentialRolloutOff || got.Credential.AgentResource != CredentialRolloutOff {
		t.Fatalf("credential defaults = %+v", got.Credential)
	}
	if got.Durable.PublicAPI != DurablePublicAPIOff || got.Durable.Worker != DurableWorkerOff || got.Durable.AllowNewStarts {
		t.Fatalf("durable defaults = %+v", got.Durable)
	}
	if got.Desktop.AgentTransport != DesktopAgentTransportLegacy {
		t.Fatalf("Desktop transport = %q", got.Desktop.AgentTransport)
	}
	if err := (*AgentPlatformRollout)(nil).Validate(); err != nil {
		t.Fatalf("closed defaults rejected: %v", err)
	}
	if err := (*AgentPlatformRollout)(nil).ValidateWorkerRole(); err != nil {
		t.Fatalf("closed Worker defaults rejected: %v", err)
	}
	if (*AgentPlatformRollout)(nil).IncludesSubject("u_42") {
		t.Fatal("closed defaults admitted a canary subject")
	}
}

func TestAgentPlatformRolloutWorkerRoleValidationIsScoped(t *testing.T) {
	// Fields owned by API/Desktop processes cannot force the Worker role on or
	// make its default-off startup fail.
	off := &AgentPlatformRollout{
		Credential: CredentialRollout{
			DesktopResource: CredentialRolloutMode("not-a-worker-field"),
			AgentResource:   CredentialRolloutMode("not-a-worker-field"),
		},
		Durable: DurableTurnRollout{
			PublicAPI: DurablePublicAPIMode("not-a-worker-field"),
			Worker:    DurableWorkerOff,
		},
		Desktop: DesktopTransportRollout{AgentTransport: DesktopAgentTransport("not-a-worker-field")},
		Readiness: AgentPlatformReadiness{
			TokenRolloverComplete: true,
			ActiveDeviceSessions:  true,
			AtomicLiveEventStream: true,
		},
	}
	if err := off.ValidateWorkerRole(); err != nil {
		t.Fatalf("Worker-off role validation was coupled to another role: %v", err)
	}

	on := &AgentPlatformRollout{Durable: DurableTurnRollout{Worker: DurableWorkerOn}}
	if err := on.ValidateWorkerRole(); err == nil {
		t.Fatal("Worker-on rollout without Worker readiness was accepted")
	}
	on.Readiness.SQLStore = true
	on.Readiness.WorkerLeaseFencing = true
	on.Readiness.TransactionalOutbox = true
	on.Readiness.ExactlyOnceSettlement = true
	if err := on.ValidateWorkerRole(); err != nil {
		t.Fatalf("ready Worker role rejected: %v", err)
	}

	on.Durable.Worker = DurableWorkerMode("invalid")
	if err := on.ValidateWorkerRole(); err == nil {
		t.Fatal("unknown Worker mode was accepted")
	}
}

func TestAgentPlatformRolloutRejectsReadinessBypass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AgentPlatformRollout)
	}{
		{name: "credential enforce without rollover", mutate: func(config *AgentPlatformRollout) {
			config.Credential.AgentResource = CredentialRolloutEnforce
		}},
		{name: "worker without durable dependencies", mutate: func(config *AgentPlatformRollout) {
			config.Durable.Worker = DurableWorkerOn
		}},
		{name: "public api without credential enforcement", mutate: func(config *AgentPlatformRollout) {
			config.Durable.PublicAPI = DurablePublicAPICanary
			config.Durable.CanaryPercent = 10
		}},
		{name: "client-controlled candidate without public api", mutate: func(config *AgentPlatformRollout) {
			config.Desktop.AgentTransport = DesktopAgentTransportCandidate
		}},
		{name: "new starts without worker", mutate: func(config *AgentPlatformRollout) {
			config.Durable.AllowNewStarts = true
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := &AgentPlatformRollout{}
			test.mutate(config)
			if err := config.Validate(); err == nil {
				t.Fatal("unsafe rollout was accepted")
			}
		})
	}
}

func TestAgentPlatformRolloutAcceptsReadyCanaryAndBucketsBySubject(t *testing.T) {
	config := readyAgentPlatformRollout()
	config.Durable.PublicAPI = DurablePublicAPICanary
	config.Durable.CanaryPercent = 17
	config.Desktop.AgentTransport = DesktopAgentTransportCandidate
	if err := config.Validate(); err != nil {
		t.Fatalf("ready canary rejected: %v", err)
	}

	first := config.IncludesSubject("u_42")
	for i := 0; i < 20; i++ {
		if config.IncludesSubject("u_42") != first {
			t.Fatal("subject canary assignment was not stable")
		}
	}
	if config.IncludesSubject("") || config.IncludesSubject(" u_42") {
		t.Fatal("invalid subjects entered the canary")
	}
	selected := 0
	for i := 1; i <= 500; i++ {
		if config.IncludesSubject("subject-" + strconv.Itoa(i)) {
			selected++
		}
	}
	if selected == 0 || selected == 500 {
		t.Fatalf("canary selection did not partition subjects: selected=%d", selected)
	}
}

func TestAgentPlatformRolloutAcceptsReadyOnWithEmergencyStartGate(t *testing.T) {
	config := readyAgentPlatformRollout()
	config.Durable.PublicAPI = DurablePublicAPIOn
	config.Durable.CanaryPercent = 100
	config.Desktop.AgentTransport = DesktopAgentTransportDurable
	config.Durable.AllowNewStarts = false
	if err := config.Validate(); err != nil {
		t.Fatalf("ready rollout rejected: %v", err)
	}
	if !config.IncludesSubject("u_42") {
		t.Fatal("public API on excluded a valid subject")
	}
}

func readyAgentPlatformRollout() *AgentPlatformRollout {
	return &AgentPlatformRollout{
		Credential: CredentialRollout{
			DesktopResource: CredentialRolloutEnforce,
			AgentResource:   CredentialRolloutEnforce,
		},
		Durable: DurableTurnRollout{
			Worker: DurableWorkerOn,
		},
		Readiness: AgentPlatformReadiness{
			TokenRolloverComplete: true,
			ActiveDeviceSessions:  true,
			SQLStore:              true,
			AtomicLiveEventStream: true,
			WorkerLeaseFencing:    true,
			TransactionalOutbox:   true,
			ExactlyOnceSettlement: true,
		},
	}
}
