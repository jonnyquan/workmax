package config

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

type CredentialRolloutMode string

const (
	CredentialRolloutOff     CredentialRolloutMode = "off"
	CredentialRolloutShadow  CredentialRolloutMode = "shadow"
	CredentialRolloutEnforce CredentialRolloutMode = "enforce"
)

type LegacyTurnShadowMode string

const (
	LegacyTurnShadowOff      LegacyTurnShadowMode = "off"
	LegacyTurnShadowValidate LegacyTurnShadowMode = "validate"
)

type DurablePublicAPIMode string

const (
	DurablePublicAPIOff    DurablePublicAPIMode = "off"
	DurablePublicAPICanary DurablePublicAPIMode = "canary"
	DurablePublicAPIOn     DurablePublicAPIMode = "on"
)

type DurableWorkerMode string

const (
	DurableWorkerOff DurableWorkerMode = "off"
	DurableWorkerOn  DurableWorkerMode = "on"
)

type DesktopAgentTransport string

const (
	DesktopAgentTransportLegacy    DesktopAgentTransport = "legacy"
	DesktopAgentTransportCandidate DesktopAgentTransport = "candidate"
	DesktopAgentTransportDurable   DesktopAgentTransport = "durable"
)

// AgentPlatformRollout is the default-off operator contract for the target
// credential and Durable Turn rollout. The separate agent-worker consumes only
// its Worker-owned subset; no current Router or Desktop transport consumes the
// block, so adding it cannot switch API or Desktop traffic.
type AgentPlatformRollout struct {
	Credential CredentialRollout       `mapstructure:"credential" json:"credential" yaml:"credential"`
	Durable    DurableTurnRollout      `mapstructure:"durable_turn" json:"durable_turn" yaml:"durable_turn"`
	Desktop    DesktopTransportRollout `mapstructure:"desktop" json:"desktop" yaml:"desktop"`
	Readiness  AgentPlatformReadiness  `mapstructure:"readiness" json:"readiness" yaml:"readiness"`
}

type CredentialRollout struct {
	DesktopResource CredentialRolloutMode `mapstructure:"desktop_resource" json:"desktop_resource" yaml:"desktop_resource"`
	AgentResource   CredentialRolloutMode `mapstructure:"agent_resource" json:"agent_resource" yaml:"agent_resource"`
}

type DurableTurnRollout struct {
	LegacyShadow   LegacyTurnShadowMode `mapstructure:"legacy_shadow" json:"legacy_shadow" yaml:"legacy_shadow"`
	PublicAPI      DurablePublicAPIMode `mapstructure:"public_api" json:"public_api" yaml:"public_api"`
	CanaryPercent  int                  `mapstructure:"canary_percent" json:"canary_percent" yaml:"canary_percent"`
	Worker         DurableWorkerMode    `mapstructure:"worker" json:"worker" yaml:"worker"`
	AllowNewStarts bool                 `mapstructure:"allow_new_starts" json:"allow_new_starts" yaml:"allow_new_starts"`
}

type DesktopTransportRollout struct {
	AgentTransport DesktopAgentTransport `mapstructure:"agent_transport" json:"agent_transport" yaml:"agent_transport"`
}

// AgentPlatformReadiness is an explicit startup attestation. It does not make
// a missing implementation ready; future composition code must derive these
// values from installed production dependencies before honoring a rollout.
type AgentPlatformReadiness struct {
	TokenRolloverComplete bool `mapstructure:"token_rollover_complete" json:"token_rollover_complete" yaml:"token_rollover_complete"`
	ActiveDeviceSessions  bool `mapstructure:"active_device_sessions" json:"active_device_sessions" yaml:"active_device_sessions"`
	SQLStore              bool `mapstructure:"sql_store" json:"sql_store" yaml:"sql_store"`
	AtomicLiveEventStream bool `mapstructure:"atomic_live_event_stream" json:"atomic_live_event_stream" yaml:"atomic_live_event_stream"`
	WorkerLeaseFencing    bool `mapstructure:"worker_lease_fencing" json:"worker_lease_fencing" yaml:"worker_lease_fencing"`
	TransactionalOutbox   bool `mapstructure:"transactional_outbox" json:"transactional_outbox" yaml:"transactional_outbox"`
	ExactlyOnceSettlement bool `mapstructure:"exactly_once_settlement" json:"exactly_once_settlement" yaml:"exactly_once_settlement"`
}

// EffectiveAgentPlatformRollout applies fail-closed defaults to an optional
// config block. The legacy transport remains selected so an absent block does
// not reinterpret existing traffic.
func EffectiveAgentPlatformRollout(raw *AgentPlatformRollout) AgentPlatformRollout {
	effective := AgentPlatformRollout{}
	if raw != nil {
		effective = *raw
	}
	if effective.Credential.DesktopResource == "" {
		effective.Credential.DesktopResource = CredentialRolloutOff
	}
	if effective.Credential.AgentResource == "" {
		effective.Credential.AgentResource = CredentialRolloutOff
	}
	if effective.Durable.LegacyShadow == "" {
		effective.Durable.LegacyShadow = LegacyTurnShadowOff
	}
	if effective.Durable.PublicAPI == "" {
		effective.Durable.PublicAPI = DurablePublicAPIOff
	}
	if effective.Durable.Worker == "" {
		effective.Durable.Worker = DurableWorkerOff
	}
	if effective.Desktop.AgentTransport == "" {
		effective.Desktop.AgentTransport = DesktopAgentTransportLegacy
	}
	return effective
}

// Validate rejects unsafe or internally inconsistent rollout declarations.
// This is a candidate startup gate; production bootstrap does not call it yet.
func (raw *AgentPlatformRollout) Validate() error {
	config := EffectiveAgentPlatformRollout(raw)
	if !config.Credential.DesktopResource.valid() {
		return fmt.Errorf("agent_platform_rollout.credential.desktop_resource must be off, shadow or enforce")
	}
	if !config.Credential.AgentResource.valid() {
		return fmt.Errorf("agent_platform_rollout.credential.agent_resource must be off, shadow or enforce")
	}
	if !config.Durable.LegacyShadow.valid() {
		return fmt.Errorf("agent_platform_rollout.durable_turn.legacy_shadow must be off or validate")
	}
	if !config.Durable.PublicAPI.valid() {
		return fmt.Errorf("agent_platform_rollout.durable_turn.public_api must be off, canary or on")
	}
	if !config.Durable.Worker.valid() {
		return fmt.Errorf("agent_platform_rollout.durable_turn.worker must be off or on")
	}
	if !config.Desktop.AgentTransport.valid() {
		return fmt.Errorf("agent_platform_rollout.desktop.agent_transport must be legacy, candidate or durable")
	}

	if config.Credential.DesktopResource == CredentialRolloutEnforce || config.Credential.AgentResource == CredentialRolloutEnforce {
		if !config.Readiness.TokenRolloverComplete || !config.Readiness.ActiveDeviceSessions {
			return fmt.Errorf("credential enforce requires token rollover and active device-session validation")
		}
	}
	if config.Durable.Worker == DurableWorkerOn {
		if !config.Readiness.SQLStore || !config.Readiness.WorkerLeaseFencing || !config.Readiness.TransactionalOutbox || !config.Readiness.ExactlyOnceSettlement {
			return fmt.Errorf("durable worker requires SQL store, lease fencing, transactional outbox and exactly-once settlement")
		}
	}
	if config.Durable.PublicAPI != DurablePublicAPIOff {
		if config.Credential.AgentResource != CredentialRolloutEnforce {
			return fmt.Errorf("durable public API requires enforced Agent resource credentials")
		}
		if !config.Readiness.SQLStore || !config.Readiness.AtomicLiveEventStream || config.Durable.Worker != DurableWorkerOn {
			return fmt.Errorf("durable public API requires SQL store, atomic live event stream and worker")
		}
	}
	switch config.Durable.PublicAPI {
	case DurablePublicAPIOff:
		if config.Durable.CanaryPercent != 0 {
			return fmt.Errorf("durable public API off requires canary_percent=0")
		}
	case DurablePublicAPICanary:
		if config.Durable.CanaryPercent < 1 || config.Durable.CanaryPercent > 99 {
			return fmt.Errorf("durable public API canary requires canary_percent between 1 and 99")
		}
	case DurablePublicAPIOn:
		if config.Durable.CanaryPercent != 100 {
			return fmt.Errorf("durable public API on requires canary_percent=100")
		}
	}
	if config.Durable.AllowNewStarts && (config.Durable.PublicAPI == DurablePublicAPIOff || config.Durable.Worker != DurableWorkerOn) {
		return fmt.Errorf("durable new starts require an enabled public API and worker")
	}
	if config.Desktop.AgentTransport != DesktopAgentTransportLegacy && config.Durable.PublicAPI == DurablePublicAPIOff {
		return fmt.Errorf("non-legacy Desktop transport requires an enabled durable public API")
	}
	if config.Desktop.AgentTransport == DesktopAgentTransportDurable && config.Durable.PublicAPI != DurablePublicAPIOn {
		return fmt.Errorf("durable Desktop transport requires durable public API on")
	}
	return nil
}

// ValidateWorkerRole validates only configuration owned by the separate
// agent-worker process. Public API, Desktop transport and credential fields
// belong to other process roles and must not make this binary open a database,
// construct an EventStream or claim their readiness.
func (raw *AgentPlatformRollout) ValidateWorkerRole() error {
	config := EffectiveAgentPlatformRollout(raw)
	if !config.Durable.Worker.valid() {
		return fmt.Errorf("agent_platform_rollout.durable_turn.worker must be off or on")
	}
	if config.Durable.Worker == DurableWorkerOff {
		return nil
	}
	if !config.Readiness.SQLStore || !config.Readiness.WorkerLeaseFencing ||
		!config.Readiness.TransactionalOutbox || !config.Readiness.ExactlyOnceSettlement {
		return fmt.Errorf("durable worker requires SQL store, lease fencing, transactional outbox and exactly-once settlement")
	}
	return nil
}

// IncludesSubject performs stable server-owned canary selection. Client
// headers and request parameters cannot influence the bucket.
func (raw *AgentPlatformRollout) IncludesSubject(subject string) bool {
	config := EffectiveAgentPlatformRollout(raw)
	if subject == "" || subject != strings.TrimSpace(subject) {
		return false
	}
	switch config.Durable.PublicAPI {
	case DurablePublicAPIOn:
		return true
	case DurablePublicAPICanary:
		digest := sha256.Sum256([]byte(subject))
		bucket := int(binary.BigEndian.Uint64(digest[:8]) % 100)
		return bucket < config.Durable.CanaryPercent
	default:
		return false
	}
}

func (mode CredentialRolloutMode) valid() bool {
	return mode == CredentialRolloutOff || mode == CredentialRolloutShadow || mode == CredentialRolloutEnforce
}

func (mode LegacyTurnShadowMode) valid() bool {
	return mode == LegacyTurnShadowOff || mode == LegacyTurnShadowValidate
}

func (mode DurablePublicAPIMode) valid() bool {
	return mode == DurablePublicAPIOff || mode == DurablePublicAPICanary || mode == DurablePublicAPIOn
}

func (mode DurableWorkerMode) valid() bool {
	return mode == DurableWorkerOff || mode == DurableWorkerOn
}

func (transport DesktopAgentTransport) valid() bool {
	return transport == DesktopAgentTransportLegacy || transport == DesktopAgentTransportCandidate || transport == DesktopAgentTransportDurable
}
