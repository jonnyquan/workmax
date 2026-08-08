// Package agentturn implements the target durable Agent Turn kernel.
//
// It is intentionally independent from the legacy Work Agent HTTP/SSE
// handlers. Nothing in this package registers a route or starts an Agent
// execution. Its SQLStore is an unmounted persistence candidate, not a
// production composition. Production HTTP remains blocked on authenticated
// admission, atomic replay-to-live subscription, a queue/worker/reconciler
// composition, Effect Outbox dispatch and exactly-once settlement. The
// companion ExecutionStore fixes the persistence contract for explicit
// Attempt claim/lease/fencing and atomic Effect enqueue only; it does not
// provide those production runtime components.
package agentturn

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var (
	ErrTurnNotFound             = errors.New("durable turn not found")
	ErrTurnIDConflict           = errors.New("durable turn id already exists")
	ErrIdempotencyConflict      = errors.New("idempotency key was reused for a different command")
	ErrInvalidTransition        = errors.New("invalid durable turn transition")
	ErrTurnTerminal             = errors.New("durable turn is terminal")
	ErrCancellationNotRequested = errors.New("durable turn cancellation was not requested")
	ErrReplayCursorNotFound     = errors.New("replay cursor event was not found for durable turn")
	ErrReplayCursorAhead        = errors.New("replay cursor is ahead of the durable turn")
	ErrReplayGap                = errors.New("replay cursor precedes retained durable turn history")
	ErrSequenceExhausted        = errors.New("durable turn event sequence exhausted")
	ErrReservedEventType        = errors.New("event type belongs to the reserved core namespace")
	ErrStoreUnavailable         = errors.New("durable turn store unavailable")
	ErrStoreIntegrity           = errors.New("durable turn store integrity violation")
)

const (
	MaxPrincipalIDBytes    = 128
	MaxThreadIDBytes       = 256
	MaxTurnIDBytes         = 256
	MaxIdempotencyKeyBytes = 128
	MaxCommandDigestBytes  = 128
	MaxPluginFieldBytes    = 512
	MaxEventTypeBytes      = 255
	MaxEventIDBytes        = 320
	MaxEventDataBytes      = 1 << 20
	MaxEventResourceRefs   = 64
	MaxDurableSequence     = agentv1.Sequence(1<<63 - 1)
)

// PrincipalID is the authenticated resource owner supplied by a future Agent
// resource credential adapter. It never comes from the Start JSON body.
type PrincipalID string

// Turn is the durable state needed by admission, workers and observers. A
// stopped Turn always has a prior cancellation intent; observer disconnects
// never mutate this record.
type Turn struct {
	ID                agentv1.TurnID
	PrincipalID       PrincipalID
	ThreadID          agentv1.ThreadID
	IdempotencyKey    agentv1.IdempotencyKey
	CommandDigest     string
	Plugin            agentv1.EventPluginRef
	Status            agentv1.TurnStatus
	CancelRequestedAt *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
	StartedAt         *time.Time
	FinishedAt        *time.Time
}

func (t Turn) Validate() error {
	if err := validatePathSegment("turnId", string(t.ID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validateBoundedText("principalId", string(t.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(t.ThreadID), MaxThreadIDBytes); err != nil {
		return err
	}
	if err := validateBoundedText("idempotencyKey", string(t.IdempotencyKey), MaxIdempotencyKeyBytes); err != nil {
		return err
	}
	if err := validateCommandDigest(t.CommandDigest); err != nil {
		return err
	}
	if err := t.Plugin.Validate(); err != nil {
		return err
	}
	if err := validatePluginRef(t.Plugin); err != nil {
		return err
	}
	if !t.Status.Valid() {
		return fmt.Errorf("unknown turn status %q", t.Status)
	}
	if t.CreatedAt.IsZero() || t.UpdatedAt.IsZero() {
		return fmt.Errorf("createdAt and updatedAt are required")
	}
	if t.UpdatedAt.Before(t.CreatedAt) {
		return fmt.Errorf("updatedAt precedes createdAt")
	}
	if t.StartedAt != nil && t.StartedAt.Before(t.CreatedAt) {
		return fmt.Errorf("startedAt precedes createdAt")
	}
	if t.CancelRequestedAt != nil && t.CancelRequestedAt.Before(t.CreatedAt) {
		return fmt.Errorf("cancelRequestedAt precedes createdAt")
	}
	if t.FinishedAt != nil && t.FinishedAt.Before(t.CreatedAt) {
		return fmt.Errorf("finishedAt precedes createdAt")
	}
	if t.StartedAt != nil && t.FinishedAt != nil && t.FinishedAt.Before(*t.StartedAt) {
		return fmt.Errorf("finishedAt precedes startedAt")
	}
	for _, field := range []struct {
		name      string
		timestamp *time.Time
	}{
		{name: "startedAt", timestamp: t.StartedAt},
		{name: "cancelRequestedAt", timestamp: t.CancelRequestedAt},
		{name: "finishedAt", timestamp: t.FinishedAt},
	} {
		if field.timestamp != nil && field.timestamp.After(t.UpdatedAt) {
			return fmt.Errorf("%s exceeds updatedAt", field.name)
		}
	}
	if t.Status.Terminal() != (t.FinishedAt != nil) {
		return fmt.Errorf("terminal status and finishedAt must agree")
	}
	if t.Status == agentv1.TurnStatusQueued && t.StartedAt != nil {
		return fmt.Errorf("queued turn must not have startedAt")
	}
	if t.Status == agentv1.TurnStatusRunning && t.StartedAt == nil {
		return fmt.Errorf("running turn requires startedAt")
	}
	if t.Status == agentv1.TurnStatusStopped && t.CancelRequestedAt == nil {
		return fmt.Errorf("stopped turn requires prior cancellation intent")
	}
	return nil
}

// EventDraft contains caller-controlled event data. Turn identity, sequence,
// Event ID and the admitted Plugin snapshot are assigned atomically by Store.
type EventDraft struct {
	Type         agentv1.EventType
	ResourceRefs []string
	Data         json.RawMessage
}

func (d EventDraft) Validate() error {
	if err := validateBoundedText("event type", string(d.Type), MaxEventTypeBytes); err != nil {
		return err
	}
	if len(d.Data) == 0 || !json.Valid(d.Data) {
		return fmt.Errorf("event data must contain valid JSON")
	}
	if len(d.Data) > MaxEventDataBytes {
		return fmt.Errorf("event data exceeds %d bytes", MaxEventDataBytes)
	}
	if len(d.ResourceRefs) > MaxEventResourceRefs {
		return fmt.Errorf("event resource refs exceed %d items", MaxEventResourceRefs)
	}
	for i, ref := range d.ResourceRefs {
		if err := validateText(fmt.Sprintf("resourceRefs[%d]", i), ref); err != nil {
			return err
		}
	}
	return nil
}

// StartCommand combines the public v1 request with credential-owned and
// admission-owned data. PrincipalID, CommandDigest and Plugin must be produced
// by server-side credential/policy/resolver code and must never be decoded
// from an HTTP body. CommandDigest is a canonical digest of the domain command,
// excluding deployment-resolved Plugin metadata. Reusing the same idempotency
// scope with another digest fails closed.
type StartCommand struct {
	PrincipalID   PrincipalID
	Request       agentv1.StartRequest
	CommandDigest string
	Plugin        agentv1.EventPluginRef
}

func (c StartCommand) Validate() error {
	if err := validateBoundedText("principalId", string(c.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := c.Request.Validate(); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(c.Request.ThreadID), MaxThreadIDBytes); err != nil {
		return err
	}
	if err := validateBoundedText("idempotencyKey", string(c.Request.IdempotencyKey), MaxIdempotencyKeyBytes); err != nil {
		return err
	}
	if err := validateCommandDigest(c.CommandDigest); err != nil {
		return err
	}
	if err := c.Plugin.Validate(); err != nil {
		return err
	}
	return validatePluginRef(c.Plugin)
}

type StartResult struct {
	Admission        agentv1.StartAdmissionResult
	Turn             Turn
	IdempotentReplay bool
}

// OwnedTurnRequest is used by status reads so a future adapter cannot look up
// a Turn by opaque ID without also proving principal and thread ownership.
type OwnedTurnRequest struct {
	PrincipalID PrincipalID
	ThreadID    agentv1.ThreadID
	TurnID      agentv1.TurnID
}

func (r OwnedTurnRequest) Validate() error {
	if err := validateBoundedText("principalId", string(r.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(r.ThreadID), MaxThreadIDBytes); err != nil {
		return err
	}
	return validatePathSegment("turnId", string(r.TurnID), MaxTurnIDBytes)
}

type AttachCommand struct {
	PrincipalID PrincipalID
	ThreadID    agentv1.ThreadID
	Request     agentv1.AttachRequest
}

func (c AttachCommand) Validate() error {
	if err := validateBoundedText("principalId", string(c.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(c.ThreadID), MaxThreadIDBytes); err != nil {
		return err
	}
	if err := c.Request.Validate(); err != nil {
		return err
	}
	return validatePathSegment("turnId", string(c.Request.TurnID), MaxTurnIDBytes)
}

type Attachment struct {
	Turn          Turn
	AfterSequence agentv1.Sequence
	Window        agentv1.ReplayWindow
	Events        []agentv1.EventEnvelope
	HasMore       bool
}

type CancelCommand struct {
	PrincipalID PrincipalID
	ThreadID    agentv1.ThreadID
	Request     agentv1.CancelRequest
}

func (c CancelCommand) Validate() error {
	if err := validateBoundedText("principalId", string(c.PrincipalID), MaxPrincipalIDBytes); err != nil {
		return err
	}
	if err := validatePathSegment("threadId", string(c.ThreadID), MaxThreadIDBytes); err != nil {
		return err
	}
	if err := c.Request.Validate(); err != nil {
		return err
	}
	return validatePathSegment("turnId", string(c.Request.TurnID), MaxTurnIDBytes)
}

type CancelResult struct {
	Turn           Turn
	NewlyRequested bool
	Event          *agentv1.EventEnvelope
}

type TransitionResult struct {
	Turn    Turn
	Changed bool
	Event   *agentv1.EventEnvelope
}

type AdmissionRecord struct {
	Turn    Turn
	Created bool
}

type ReplayRecord struct {
	Turn          Turn
	AfterSequence agentv1.Sequence
	Window        agentv1.ReplayWindow
	Events        []agentv1.EventEnvelope
	HasMore       bool
}

// ReplayQuery bounds one replay page. Attach adapters must page until HasMore
// is false and then switch to a future live subscription; one snapshot must
// not be presented as an indefinitely attached SSE stream.
type ReplayQuery struct {
	Cursor agentv1.ReplayCursor
	Limit  int
}

func (q ReplayQuery) Validate() error {
	if err := q.Cursor.Validate(); err != nil {
		return err
	}
	if q.Cursor.LastEventID != "" {
		if err := validateBoundedText("lastEventId", q.Cursor.LastEventID, MaxEventIDBytes); err != nil {
			return err
		}
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		return fmt.Errorf("replay limit must be between 1 and 1000")
	}
	if q.Cursor.AfterSequence != nil && *q.Cursor.AfterSequence > MaxDurableSequence {
		return fmt.Errorf("replay cursor exceeds durable sequence limit")
	}
	return nil
}

// ReplayBoundaryError carries retained bounds so a future HTTP adapter can
// map cursor-ahead or retention-gap failures without parsing error strings.
type ReplayBoundaryError struct {
	Kind   error
	Cursor agentv1.Sequence
	Window agentv1.ReplayWindow
}

func (e *ReplayBoundaryError) Error() string {
	return fmt.Sprintf("%v: cursor %d outside retained window %d..%d", e.Kind, e.Cursor, e.Window.OldestSequence, e.Window.LatestSequence)
}

func (e *ReplayBoundaryError) Unwrap() error { return e.Kind }

func validateText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	if len(value) > 2048 {
		return fmt.Errorf("%s exceeds 2048 bytes", name)
	}
	return nil
}

func validateBoundedText(name, value string, maximum int) error {
	if err := validateText(name, value); err != nil {
		return err
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", name, maximum)
	}
	return nil
}

func validatePathSegment(name, value string, maximum int) error {
	if err := validateBoundedText(name, value, maximum); err != nil {
		return err
	}
	if strings.ContainsAny(value, "/\\?#%") {
		return fmt.Errorf("%s contains a path delimiter", name)
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

func validatePluginRef(plugin agentv1.EventPluginRef) error {
	if err := validateBoundedText("plugin.id", plugin.ID, MaxPluginFieldBytes); err != nil {
		return err
	}
	if err := validateBoundedText("plugin.version", plugin.Version, MaxPluginFieldBytes); err != nil {
		return err
	}
	return validateBoundedText("plugin.releaseDigest", plugin.ReleaseDigest, MaxPluginFieldBytes)
}

func validateCommandDigest(value string) error {
	if err := validateBoundedText("commandDigest", value, MaxCommandDigestBytes); err != nil {
		return err
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return fmt.Errorf("commandDigest must contain printable ASCII only")
		}
	}
	return nil
}
