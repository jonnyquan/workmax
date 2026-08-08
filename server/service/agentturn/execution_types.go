package agentturn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentv1 "server/contracts/agent/v1"
)

var (
	ErrAttemptNotFound        = errors.New("durable turn attempt not found")
	ErrAttemptConflict        = errors.New("durable turn attempt identity conflict")
	ErrAttemptBusy            = errors.New("durable turn already has an active attempt")
	ErrAttemptFenced          = errors.New("durable turn attempt was fenced")
	ErrAttemptLeaseExpired    = errors.New("durable turn attempt lease expired")
	ErrAttemptCancelled       = errors.New("durable turn cancellation was requested")
	ErrAttemptFenceExhausted  = errors.New("durable turn attempt fencing token exhausted")
	ErrOperationConflict      = errors.New("durable turn operation identity conflict")
	ErrEffectConflict         = errors.New("durable turn effect outbox identity conflict")
	ErrExecutionFenceRequired = errors.New("durable turn execution fence is required")
	ErrNoClaimableTurn        = errors.New("durable turn queue has no claimable work")
	ErrPluginScopeMismatch    = errors.New("durable turn plugin is outside the worker scope")
	ErrAttemptBudgetExhausted = errors.New("durable turn attempt budget exhausted")
	ErrReconcilePrecondition  = errors.New("durable turn no longer matches the reconcile precondition")
)

const (
	DefaultAttemptLeaseTTL    = 30 * time.Second
	DefaultClaimNextScanLimit = 16
	MaxClaimNextScanLimit     = 128
	// DefaultMaxTurnAttempts bounds how many execution epochs one Turn may
	// consume. Each claim increments the Turn fence, so the fence doubles as
	// the attempt counter. Without this bound a Turn whose executor keeps
	// dying is reclaimed forever and never reaches the terminal state the
	// reliability model requires after a crash.
	DefaultMaxTurnAttempts    = 3
	MaxTurnAttemptsLimit      = 64
	DefaultReclaimScanLimit   = 64
	MaxReclaimScanLimit       = 512
	MaxAttemptIDBytes         = 64
	MaxWorkerIDBytes          = 128
	MaxWorkerBuildDigestBytes = 128
	MaxOperationIDBytes       = 128
	MaxEffectOutboxIDBytes    = 64
	MaxEffectTopicBytes       = 128
	MaxEffectDedupeKeyBytes   = 256
	MaxEffectPayloadBytes     = 1 << 20
	MaxEffectsPerOperation    = 64
	MaxClaimPluginScopes      = 32
)

type AttemptStatus string

const (
	AttemptStatusRunning   AttemptStatus = "running"
	AttemptStatusCompleted AttemptStatus = "completed"
	AttemptStatusStopped   AttemptStatus = "stopped"
	AttemptStatusFailed    AttemptStatus = "failed"
	AttemptStatusTimeout   AttemptStatus = "timeout"
	AttemptStatusExpired   AttemptStatus = "expired"
)

func (status AttemptStatus) Valid() bool {
	switch status {
	case AttemptStatusRunning, AttemptStatusCompleted, AttemptStatusStopped,
		AttemptStatusFailed, AttemptStatusTimeout, AttemptStatusExpired:
		return true
	default:
		return false
	}
}

func (status AttemptStatus) Terminal() bool { return status.Valid() && status != AttemptStatusRunning }

type AttemptFence struct {
	TurnID            agentv1.TurnID
	AttemptID         string
	FencingToken      agentv1.Sequence
	WorkerID          string
	WorkerBuildDigest string
}

func (fence AttemptFence) Validate() error {
	if err := validatePathSegment("turnId", string(fence.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("attemptId", fence.AttemptID, MaxAttemptIDBytes); err != nil {
		return err
	}
	if fence.FencingToken == 0 || fence.FencingToken > MaxDurableSequence {
		return fmt.Errorf("fencingToken must be between 1 and %d", MaxDurableSequence)
	}
	if err := validatePrintableASCII("workerId", fence.WorkerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	return validatePrintableASCII("workerBuildDigest", fence.WorkerBuildDigest, MaxWorkerBuildDigestBytes)
}

type TurnAttempt struct {
	ID                string
	TurnID            agentv1.TurnID
	FencingToken      agentv1.Sequence
	Status            AttemptStatus
	WorkerID          string
	WorkerBuildDigest string
	LeaseExpiresAt    time.Time
	ClaimedAt         time.Time
	LastHeartbeatAt   time.Time
	FinishedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (attempt TurnAttempt) Fence() AttemptFence {
	return AttemptFence{
		TurnID:            attempt.TurnID,
		AttemptID:         attempt.ID,
		FencingToken:      attempt.FencingToken,
		WorkerID:          attempt.WorkerID,
		WorkerBuildDigest: attempt.WorkerBuildDigest,
	}
}

func (attempt TurnAttempt) Validate() error {
	if err := attempt.Fence().Validate(); err != nil {
		return err
	}
	if !attempt.Status.Valid() {
		return fmt.Errorf("unknown attempt status %q", attempt.Status)
	}
	if attempt.LeaseExpiresAt.IsZero() || attempt.ClaimedAt.IsZero() || attempt.LastHeartbeatAt.IsZero() ||
		attempt.CreatedAt.IsZero() || attempt.UpdatedAt.IsZero() {
		return fmt.Errorf("attempt lease and lifecycle timestamps are required")
	}
	if attempt.LastHeartbeatAt.Before(attempt.ClaimedAt) || attempt.UpdatedAt.Before(attempt.CreatedAt) ||
		attempt.UpdatedAt.Before(attempt.LastHeartbeatAt) {
		return fmt.Errorf("attempt lifecycle timestamps are out of order")
	}
	if !attempt.LastHeartbeatAt.Before(attempt.LeaseExpiresAt) {
		return fmt.Errorf("attempt heartbeat must precede lease expiry")
	}
	if attempt.Status.Terminal() != (attempt.FinishedAt != nil) {
		return fmt.Errorf("terminal attempt status and finishedAt must agree")
	}
	if attempt.FinishedAt != nil && attempt.FinishedAt.Before(attempt.LastHeartbeatAt) {
		return fmt.Errorf("attempt finishedAt precedes last heartbeat")
	}
	if attempt.FinishedAt != nil && attempt.UpdatedAt.Before(*attempt.FinishedAt) {
		return fmt.Errorf("attempt updatedAt precedes finishedAt")
	}
	return nil
}

type ClaimAttemptCommand struct {
	TurnID            agentv1.TurnID
	AttemptID         string
	WorkerID          string
	WorkerBuildDigest string
	// PluginScope optionally restricts this ownership mutation to an exact set
	// of immutable Plugin release snapshots. ClaimNext always supplies it for a
	// scoped worker; direct legacy callers may leave it empty.
	PluginScope []agentv1.EventPluginRef
}

func (command ClaimAttemptCommand) Validate() error {
	if err := (AttemptFence{
		TurnID:            command.TurnID,
		AttemptID:         command.AttemptID,
		FencingToken:      1,
		WorkerID:          command.WorkerID,
		WorkerBuildDigest: command.WorkerBuildDigest,
	}).Validate(); err != nil {
		return err
	}
	return validateClaimPluginScope(command.PluginScope)
}

type ClaimAttemptResult struct {
	Turn      Turn
	Attempt   TurnAttempt
	Claimed   bool
	Reclaimed bool
	Replay    bool
}

// ClaimNextCommand discovers work instead of naming it. Unlike
// ClaimAttemptCommand it carries no TurnID: the store selects one claimable
// Turn and then arbitrates ownership through the same fenced ClaimAttempt
// path, so discovery adds no second ownership authority.
//
// AttemptID must be unique per logical claim. It stays caller-supplied so a
// worker that loses the response to a crash or timeout can re-issue the same
// command and recover its epoch instead of stranding one Turn per retry until
// the lease lapses.
type ClaimNextCommand struct {
	AttemptID         string
	WorkerID          string
	WorkerBuildDigest string
	// ScanLimit bounds how many candidate Turns one call may contend for
	// before reporting no work. Zero selects DefaultClaimNextScanLimit.
	ScanLimit int
	// PluginScope filters discovery by the complete immutable release snapshot.
	// Empty preserves the unscoped candidate contract for legacy tests; a
	// production Worker must use a sealed scoped ExecutionStore.
	PluginScope []agentv1.EventPluginRef
}

func (command ClaimNextCommand) Validate() error {
	if err := validatePrintableASCII("attemptId", command.AttemptID, MaxAttemptIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("workerId", command.WorkerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("workerBuildDigest", command.WorkerBuildDigest, MaxWorkerBuildDigestBytes); err != nil {
		return err
	}
	if command.ScanLimit < 0 || command.ScanLimit > MaxClaimNextScanLimit {
		return fmt.Errorf("scanLimit must be between 0 and %d", MaxClaimNextScanLimit)
	}
	return validateClaimPluginScope(command.PluginScope)
}

func (command ClaimNextCommand) scanLimit() int {
	if command.ScanLimit <= 0 {
		return DefaultClaimNextScanLimit
	}
	return command.ScanLimit
}

func (command ClaimNextCommand) claimFor(turnID agentv1.TurnID) ClaimAttemptCommand {
	return ClaimAttemptCommand{
		TurnID:            turnID,
		AttemptID:         command.AttemptID,
		WorkerID:          command.WorkerID,
		WorkerBuildDigest: command.WorkerBuildDigest,
		PluginScope:       append([]agentv1.EventPluginRef(nil), command.PluginScope...),
	}
}

func validateClaimPluginScope(scope []agentv1.EventPluginRef) error {
	if len(scope) > MaxClaimPluginScopes {
		return fmt.Errorf("pluginScope must contain at most %d releases", MaxClaimPluginScopes)
	}
	seen := make(map[agentv1.EventPluginRef]struct{}, len(scope))
	for index, plugin := range scope {
		if err := plugin.Validate(); err != nil {
			return fmt.Errorf("pluginScope[%d]: %w", index, err)
		}
		if err := validatePluginRef(plugin); err != nil {
			return fmt.Errorf("pluginScope[%d]: %w", index, err)
		}
		if _, duplicate := seen[plugin]; duplicate {
			return fmt.Errorf("pluginScope[%d] repeats a release", index)
		}
		seen[plugin] = struct{}{}
	}
	return nil
}

func claimPluginScopeAllows(scope []agentv1.EventPluginRef, plugin agentv1.EventPluginRef) bool {
	if len(scope) == 0 {
		return true
	}
	for _, allowed := range scope {
		if allowed == plugin {
			return true
		}
	}
	return false
}

// ReclaimReason explains why a Turn cannot make progress on its own.
type ReclaimReason string

const (
	// ReclaimReasonLeaseExpired marks a Turn whose active Attempt stopped
	// heartbeating. Its executor may still be alive but is no longer the
	// fenced owner.
	ReclaimReasonLeaseExpired ReclaimReason = "lease_expired"
	// ReclaimReasonCancellationPending marks a Turn that recorded a
	// cancellation intent with no live Attempt to observe it. ClaimNext
	// refuses such Turns by design, so only a Reconciler can retire them.
	ReclaimReasonCancellationPending ReclaimReason = "cancellation_pending"
	// ReclaimReasonAttemptsExhausted marks a Turn that consumed its attempt
	// budget without reaching a terminal state. Retrying it again would loop,
	// so a Reconciler must retire it.
	ReclaimReasonAttemptsExhausted ReclaimReason = "attempts_exhausted"
	// ReclaimReasonReservationExpired marks an exact Agent-bound reserved hold
	// whose database-clock TTL elapsed before another execution epoch began.
	// A dedicated commercial scanner discovers it; the kernel re-verifies the
	// exact owner tuple under lock before retiring the Turn.
	ReclaimReasonReservationExpired ReclaimReason = "reservation_expired"
)

func (reason ReclaimReason) Valid() bool {
	switch reason {
	case ReclaimReasonLeaseExpired, ReclaimReasonCancellationPending,
		ReclaimReasonAttemptsExhausted, ReclaimReasonReservationExpired:
		return true
	default:
		return false
	}
}

// Actionable separates rows a Reconciler must retire from rows that are merely
// waiting for the next claim. A lapsed lease with budget remaining is normal
// retry traffic: ClaimNext will pick it up, and failing it would convert a
// recoverable worker restart into a lost Turn.
func (reason ReclaimReason) Actionable() bool {
	return reason == ReclaimReasonCancellationPending || reason == ReclaimReasonAttemptsExhausted ||
		reason == ReclaimReasonReservationExpired
}

// TerminalStatus is the state a Reconciler drives an actionable Turn to.
// Cancellation resolves to the intent the caller recorded; an exhausted
// attempt budget resolves to `timeout` rather than `failed`, because every
// epoch ended by lease expiry rather than by a reported execution failure.
func (reason ReclaimReason) TerminalStatus() (agentv1.TurnStatus, bool) {
	switch reason {
	case ReclaimReasonCancellationPending:
		return agentv1.TurnStatusStopped, true
	case ReclaimReasonAttemptsExhausted, ReclaimReasonReservationExpired:
		return agentv1.TurnStatusTimeout, true
	default:
		return "", false
	}
}

// ReclaimableTurn is read-only discovery output. It reports what a Reconciler
// must act on and deliberately carries no lease, fence or mutation authority:
// acting on a row still requires ClaimAttempt or an explicit fenced commit.
type ReclaimableTurn struct {
	TurnID            agentv1.TurnID
	Status            agentv1.TurnStatus
	Reason            ReclaimReason
	AttemptID         string
	FencingToken      agentv1.Sequence
	WorkerID          string
	LeaseExpiresAt    *time.Time
	CancelRequestedAt *time.Time
}

type ReclaimQuery struct {
	// Limit bounds one discovery page. Zero selects DefaultReclaimScanLimit.
	Limit int
	// ActionableOnly drops rows a Reconciler must not retire, leaving only
	// cancellation and exhausted-budget work.
	ActionableOnly bool
}

func (query ReclaimQuery) Validate() error {
	if query.Limit < 0 || query.Limit > MaxReclaimScanLimit {
		return fmt.Errorf("limit must be between 0 and %d", MaxReclaimScanLimit)
	}
	return nil
}

func (query ReclaimQuery) limit() int {
	if query.Limit <= 0 {
		return DefaultReclaimScanLimit
	}
	return query.Limit
}

type HeartbeatAttemptCommand struct {
	Fence AttemptFence
}

func (command HeartbeatAttemptCommand) Validate() error { return command.Fence.Validate() }

type HeartbeatAttemptResult struct {
	Attempt           TurnAttempt
	CancelRequestedAt *time.Time
}

type EffectOutboxDraft struct {
	OutboxID    string
	Topic       string
	DedupeKey   string
	Payload     json.RawMessage
	AvailableAt time.Time
}

func (draft EffectOutboxDraft) Validate() error {
	if err := validatePrintableASCII("effect.outboxId", draft.OutboxID, MaxEffectOutboxIDBytes); err != nil {
		return err
	}
	if err := validatePrintableASCII("effect.topic", draft.Topic, MaxEffectTopicBytes); err != nil {
		return err
	}
	if err := validateBoundedText("effect.dedupeKey", draft.DedupeKey, MaxEffectDedupeKeyBytes); err != nil {
		return err
	}
	if len(draft.Payload) == 0 || !json.Valid(draft.Payload) {
		return fmt.Errorf("effect payload must contain valid JSON")
	}
	if len(draft.Payload) > MaxEffectPayloadBytes {
		return fmt.Errorf("effect payload exceeds %d bytes", MaxEffectPayloadBytes)
	}
	if draft.AvailableAt.IsZero() {
		return fmt.Errorf("effect availableAt is required")
	}
	return nil
}

type EffectOutboxRecord struct {
	OutboxID     string
	TurnID       agentv1.TurnID
	AttemptID    string
	FencingToken agentv1.Sequence
	OperationID  string
	Ordinal      int
	Topic        string
	DedupeKey    string
	Payload      json.RawMessage
	Status       string
	AvailableAt  time.Time
}

type CommitAttemptCommand struct {
	Fence          AttemptFence
	OperationID    string
	Event          *EventDraft
	TerminalStatus agentv1.TurnStatus
	Effects        []EffectOutboxDraft
	// Settlement is the optional commercial outcome of a terminal commit. It
	// is rejected on a non-terminal Operation, and it does nothing unless the
	// store has a SettlementAuthority installed.
	Settlement *SettlementRequest
}

func (command CommitAttemptCommand) Validate() error {
	if err := command.Fence.Validate(); err != nil {
		return err
	}
	if err := validatePrintableASCII("operationId", command.OperationID, MaxOperationIDBytes); err != nil {
		return err
	}
	if command.TerminalStatus == "" {
		if command.Event == nil {
			return fmt.Errorf("non-terminal attempt commit requires an event")
		}
		if err := command.Event.Validate(); err != nil {
			return err
		}
		if strings.HasPrefix(string(command.Event.Type), "core.") {
			return ErrReservedEventType
		}
	} else {
		if !command.TerminalStatus.Valid() || !command.TerminalStatus.Terminal() {
			return fmt.Errorf("attempt terminal status must be completed, stopped, failed or timeout")
		}
		if command.Event != nil {
			return fmt.Errorf("terminal attempt event is built by the kernel")
		}
	}
	if command.Settlement != nil {
		if command.TerminalStatus == "" {
			return fmt.Errorf("settlement requires a terminal attempt commit")
		}
		if command.Settlement.Intent != "" && !command.Settlement.Intent.Valid() {
			return fmt.Errorf("unknown settlement intent %q", command.Settlement.Intent)
		}
		if command.Settlement.UsedUnits < 0 {
			return fmt.Errorf("settlement usedUnits must not be negative")
		}
	}
	if len(command.Effects) > MaxEffectsPerOperation {
		return fmt.Errorf("attempt effects exceed %d items", MaxEffectsPerOperation)
	}
	seenOutboxIDs := make(map[string]struct{}, len(command.Effects))
	seenDedupeKeys := make(map[string]struct{}, len(command.Effects))
	for index, effect := range command.Effects {
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("effect %d: %w", index, err)
		}
		if _, exists := seenOutboxIDs[effect.OutboxID]; exists {
			return fmt.Errorf("effect %d repeats outboxId", index)
		}
		dedupeScope := effect.Topic + "\x00" + effect.DedupeKey
		if _, exists := seenDedupeKeys[dedupeScope]; exists {
			return fmt.Errorf("effect %d repeats topic and dedupeKey", index)
		}
		seenOutboxIDs[effect.OutboxID] = struct{}{}
		seenDedupeKeys[dedupeScope] = struct{}{}
	}
	return nil
}

type CommitAttemptResult struct {
	OperationID      string
	OperationDigest  string
	Event            agentv1.EventEnvelope
	Turn             Turn
	Attempt          TurnAttempt
	TurnStatus       agentv1.TurnStatus
	Effects          []EffectOutboxRecord
	SettlementReview *SettlementReviewRecord
	Replay           bool
}

// ExecutionStore is the fenced execution boundary. ClaimNext is work
// discovery; the other three are all fence-authoritative mutations. None of
// them starts a worker loop, a heartbeat ticker, a reconciliation pass or an
// effect dispatcher. Commercial authority is installed on the concrete Store,
// not granted through this execution interface.
type ExecutionStore interface {
	ClaimNext(context.Context, ClaimNextCommand) (ClaimAttemptResult, error)
	ClaimAttempt(context.Context, ClaimAttemptCommand) (ClaimAttemptResult, error)
	HeartbeatAttempt(context.Context, HeartbeatAttemptCommand) (HeartbeatAttemptResult, error)
	CommitAttempt(context.Context, CommitAttemptCommand) (CommitAttemptResult, error)
}

// ReconcileCommand retires one stuck Turn. Reason is the precondition the
// caller observed, not an instruction: the store re-derives the Turn's state
// under lock and refuses the command if the Turn moved on, so a Reconciler
// that raced a live worker cannot terminate work that recovered.
type ReconcileCommand struct {
	TurnID                agentv1.TurnID
	Reason                ReclaimReason
	ReconcilerID          string
	ReconcilerBuildDigest string
}

func (command ReconcileCommand) Validate() error {
	if err := validatePathSegment("turnId", string(command.TurnID), MaxTurnIDBytes); err != nil {
		return err
	}
	if !command.Reason.Actionable() {
		return fmt.Errorf("reconcile reason %q is not actionable", command.Reason)
	}
	if err := validatePrintableASCII("reconcilerId", command.ReconcilerID, MaxWorkerIDBytes); err != nil {
		return err
	}
	return validatePrintableASCII("reconcilerBuildDigest", command.ReconcilerBuildDigest, MaxWorkerBuildDigestBytes)
}

type ReconcileResult struct {
	Turn           Turn
	Event          agentv1.EventEnvelope
	TerminalStatus agentv1.TurnStatus
	// Changed is false when the Turn was already terminal. Reconciliation is
	// idempotent so a retried pass cannot append a second terminal event.
	Changed bool
	// FencedAttemptID names the Attempt this pass invalidated, if any.
	FencedAttemptID string
	// SettlementReview is present when an ambiguous release was atomically
	// terminalized and placed under a durable commercial hold.
	SettlementReview *SettlementReviewRecord
}

// ReclaimScanner is read-only discovery for a lease-expiry Reconciler. It is
// deliberately separate from ExecutionStore: listing stuck work grants no
// ownership.
type ReclaimScanner interface {
	ListReclaimableTurns(context.Context, ReclaimQuery) ([]ReclaimableTurn, error)
}

// ReconcileStore is the Reconciler's mutation authority. It is separate from
// ExecutionStore because a Reconciler is not an executor: it never runs a
// Turn, so it takes no lease and produces no Operation receipt. It bumps the
// fence to invalidate any late executor and writes one terminal event.
//
// Retiring a Turn here is not settlement. Releasing a Credits reservation
// remains a separate authoritative step that no candidate code performs.
type ReconcileStore interface {
	ReconcileTerminal(context.Context, ReconcileCommand) (ReconcileResult, error)
}

func validatePrintableASCII(name, value string, maximum int) error {
	if err := validateBoundedText(name, value, maximum); err != nil {
		return err
	}
	for _, character := range []byte(value) {
		if character < 0x20 || character > 0x7e {
			return fmt.Errorf("%s must contain printable ASCII only", name)
		}
	}
	return nil
}
