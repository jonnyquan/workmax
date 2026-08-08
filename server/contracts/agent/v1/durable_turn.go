package v1

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DurableTurnContractVersion identifies the target-only Start / Attach /
// Replay / Cancel / Terminal contract scaffold. Existing Agent traffic does
// not use this contract yet.
const DurableTurnContractVersion = "1.0.0-draft"

// EventEnvelopeSchemaVersion is the integer schema carried by every v1 event
// envelope. It is separate from the SemVer used by the Agent API as a whole.
const EventEnvelopeSchemaVersion = 1

type ThreadID string
type TurnID string
type IdempotencyKey string
type Sequence uint64

// TurnStatus is the durable, server-authoritative state of a Turn. Desktop
// observer states such as connecting, streaming or background do not belong
// here and an observer disconnect never changes this status.
type TurnStatus string

const (
	TurnStatusQueued    TurnStatus = "queued"
	TurnStatusRunning   TurnStatus = "running"
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusStopped   TurnStatus = "stopped"
	TurnStatusFailed    TurnStatus = "failed"
	TurnStatusTimeout   TurnStatus = "timeout"
)

// Valid reports whether status is part of the public durable state machine.
func (s TurnStatus) Valid() bool {
	switch s {
	case TurnStatusQueued,
		TurnStatusRunning,
		TurnStatusCompleted,
		TurnStatusStopped,
		TurnStatusFailed,
		TurnStatusTimeout:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further execution state transition is expected.
func (s TurnStatus) Terminal() bool {
	switch s {
	case TurnStatusCompleted, TurnStatusStopped, TurnStatusFailed, TurnStatusTimeout:
		return true
	default:
		return false
	}
}

// StartRequest is the Kernel-owned identity shared by domain-specific Start
// commands. Domain input is deliberately not modeled until the Agent OpenAPI
// freezes it; Plugin and capability snapshots are resolved by admission, not
// trusted from this client request.
type StartRequest struct {
	ThreadID       ThreadID       `json:"threadId"`
	IdempotencyKey IdempotencyKey `json:"idempotencyKey"`
}

func (r StartRequest) Validate() error {
	if err := validateIdentifier("threadId", string(r.ThreadID)); err != nil {
		return err
	}
	if err := validateIdentifier("idempotencyKey", string(r.IdempotencyKey)); err != nil {
		return err
	}
	return nil
}

// StartAdmissionResult is returned after durable admission commits. HTTP uses
// 202 Accepted; execution and event observation happen independently.
type StartAdmissionResult struct {
	TurnID    TurnID `json:"turnId"`
	StreamURL string `json:"streamUrl"`
}

func (r StartAdmissionResult) Validate() error {
	if err := validateIdentifier("turnId", string(r.TurnID)); err != nil {
		return err
	}
	if err := validateIdentifier("streamUrl", r.StreamURL); err != nil {
		return err
	}
	return nil
}

// ReplayCursor represents the two equivalent transport forms accepted by an
// Attach endpoint: the Last-Event-ID header and the `after` sequence query. An
// empty cursor is valid for an initial attachment. Both may be present for
// compatibility with current clients; the HTTP adapter resolves only an Event
// ID belonging to the requested Turn and uses the furthest valid sequence.
type ReplayCursor struct {
	LastEventID   string    `json:"lastEventId,omitempty"`
	AfterSequence *Sequence `json:"after,omitempty"`
}

func (c ReplayCursor) Validate() error {
	if c.LastEventID != strings.TrimSpace(c.LastEventID) {
		return fmt.Errorf("lastEventId must not contain surrounding whitespace")
	}
	return nil
}

// AttachRequest identifies a Turn and an optional independent observer
// cursor. Attaching never acquires or extends the worker execution lease.
type AttachRequest struct {
	TurnID TurnID       `json:"turnId"`
	Cursor ReplayCursor `json:"cursor"`
}

func (r AttachRequest) Validate() error {
	if err := validateIdentifier("turnId", string(r.TurnID)); err != nil {
		return err
	}
	return r.Cursor.Validate()
}

// ReplayWindow reports the retained sequence bounds available for a Turn.
// Zero/zero is the only valid empty window; a non-empty window starts at a
// positive sequence and may contain gaps because delivery is at-least-once,
// not a promise of a contiguous or exactly-once consumer history.
type ReplayWindow struct {
	OldestSequence Sequence `json:"oldestSequence"`
	LatestSequence Sequence `json:"latestSequence"`
}

func (w ReplayWindow) Empty() bool {
	return w.OldestSequence == 0 && w.LatestSequence == 0
}

func (w ReplayWindow) Validate() error {
	if w.Empty() {
		return nil
	}
	if w.OldestSequence == 0 || w.LatestSequence == 0 {
		return fmt.Errorf("a non-empty replay window requires positive sequence bounds")
	}
	if w.OldestSequence > w.LatestSequence {
		return fmt.Errorf("oldestSequence %d exceeds latestSequence %d", w.OldestSequence, w.LatestSequence)
	}
	return nil
}

type EventFrameKind string

const EventFrameEvent EventFrameKind = "event"

func (k EventFrameKind) Valid() bool {
	return k == EventFrameEvent
}

type EventVisibility string

const EventVisibilityUser EventVisibility = "user"

func (v EventVisibility) Valid() bool {
	return v == EventVisibilityUser
}

// EventType remains open for namespaced Domain Plugin events. Constants cover
// only the core events explicitly frozen by the architecture baseline.
type EventType string

const (
	EventCoreTurnAttached       EventType = "core.turn.attached"
	EventCoreTurnStatus         EventType = "core.turn.status"
	EventAssistantTextDelta     EventType = "assistant.text.delta"
	EventAssistantThinkingDelta EventType = "assistant.thinking.delta"
	EventCoreToolStarted        EventType = "core.tool.started"
	EventCoreToolCompleted      EventType = "core.tool.completed"
	EventCorePlanUpdated        EventType = "core.plan.updated"
	EventCoreQuestionRequested  EventType = "core.question.requested"
	EventCoreArtifactDiscovered EventType = "core.artifact.discovered"
	EventCoreSyncRequired       EventType = "core.sync.required"
	EventCoreTurnCompleted      EventType = "core.turn.completed"
	EventCoreTurnFailed         EventType = "core.turn.failed"
)

// EventPluginRef freezes the Plugin release identity carried by historical
// events. Digest syntax is intentionally not restricted to one algorithm in
// this initial scaffold.
type EventPluginRef struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	ReleaseDigest string `json:"releaseDigest"`
}

func (r EventPluginRef) Validate() error {
	if err := validateIdentifier("plugin.id", r.ID); err != nil {
		return err
	}
	if err := validateIdentifier("plugin.version", r.Version); err != nil {
		return err
	}
	if err := validateIdentifier("plugin.releaseDigest", r.ReleaseDigest); err != nil {
		return err
	}
	return nil
}

// EventEnvelope is the versioned, replayable Turn event shape. Data is kept as
// raw JSON so the Kernel can transport namespaced domain events without
// depending on their schemas. Consumers must still validate domain payloads
// before rendering or applying them.
type EventEnvelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	FrameKind     EventFrameKind  `json:"frameKind"`
	EventID       string          `json:"eventId"`
	TurnID        TurnID          `json:"turnId"`
	Sequence      Sequence        `json:"sequence"`
	Plugin        EventPluginRef  `json:"plugin"`
	Type          EventType       `json:"type"`
	Visibility    EventVisibility `json:"visibility"`
	ResourceRefs  []string        `json:"resourceRefs,omitempty"`
	Data          json.RawMessage `json:"data"`
}

func (e EventEnvelope) Validate() error {
	if e.SchemaVersion != EventEnvelopeSchemaVersion {
		return fmt.Errorf("unsupported event schemaVersion %d", e.SchemaVersion)
	}
	if !e.FrameKind.Valid() {
		return fmt.Errorf("unsupported frameKind %q", e.FrameKind)
	}
	if err := validateIdentifier("eventId", e.EventID); err != nil {
		return err
	}
	if err := validateIdentifier("turnId", string(e.TurnID)); err != nil {
		return err
	}
	if e.Sequence == 0 {
		return fmt.Errorf("event sequence must be positive")
	}
	if err := e.Plugin.Validate(); err != nil {
		return err
	}
	if err := validateIdentifier("event type", string(e.Type)); err != nil {
		return err
	}
	if !e.Visibility.Valid() {
		return fmt.Errorf("unsupported event visibility %q", e.Visibility)
	}
	if len(e.Data) == 0 || !json.Valid(e.Data) {
		return fmt.Errorf("event data must contain valid JSON")
	}
	for i, ref := range e.ResourceRefs {
		if err := validateIdentifier(fmt.Sprintf("resourceRefs[%d]", i), ref); err != nil {
			return err
		}
	}
	return nil
}

// ValidateEventSequence validates the invariants a replay/stream consumer can
// check without assuming contiguous delivery: one Turn, strictly increasing
// Sequence values and unique Event IDs.
func ValidateEventSequence(events []EventEnvelope) error {
	if len(events) == 0 {
		return nil
	}
	turnID := events[0].TurnID
	seenEventIDs := make(map[string]struct{}, len(events))
	var previous Sequence
	for i, event := range events {
		if err := event.Validate(); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		if event.TurnID != turnID {
			return fmt.Errorf("event %d belongs to turn %q, want %q", i, event.TurnID, turnID)
		}
		if i > 0 && event.Sequence <= previous {
			return fmt.Errorf("event %d sequence %d is not greater than %d", i, event.Sequence, previous)
		}
		if _, duplicate := seenEventIDs[event.EventID]; duplicate {
			return fmt.Errorf("event %d repeats eventId %q", i, event.EventID)
		}
		seenEventIDs[event.EventID] = struct{}{}
		previous = event.Sequence
	}
	return nil
}

// CancelRequest records cancellation intent. A successful request does not
// itself prove execution stopped; clients reconcile against TerminalState.
type CancelRequest struct {
	TurnID TurnID `json:"turnId"`
}

func (r CancelRequest) Validate() error {
	return validateIdentifier("turnId", string(r.TurnID))
}

// TerminalState is the minimum durable-row projection required to reconcile
// an observer after EOF, reload or reconnect.
type TerminalState struct {
	TurnID TurnID     `json:"turnId"`
	Status TurnStatus `json:"status"`
}

func (s TerminalState) Validate() error {
	if err := validateIdentifier("turnId", string(s.TurnID)); err != nil {
		return err
	}
	if !s.Status.Valid() {
		return fmt.Errorf("unknown turn status %q", s.Status)
	}
	if !s.Status.Terminal() {
		return fmt.Errorf("turn status %q is not terminal", s.Status)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	return nil
}
