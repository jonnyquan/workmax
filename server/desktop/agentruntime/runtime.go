//go:build desktop

// Package agentruntime defines the seam between WorkMax's turn plumbing and
// the agent engines that actually run a tool loop.
//
// A Runtime is one engine: today the Claude Agent SDK (server/desktop/
// local_agent), next the Pi RPC subprocess (ProjectDocs/design/
// l2-agent-runtime-study-2026-08.md). Both are "Go drives a subprocess and
// reads a typed stream"; what differs is the wire protocol. This package owns
// what they share — the unified event vocabulary and the bridge that turns
// those events into the workmax SSE frames and cache writes the rest of the
// sidecar already speaks — so a second engine is an adapter, not a fork of
// the turn pipeline.
//
// The interface is deliberately turn-scoped, not session-scoped: the sidecar's
// idempotency, intent, and cache machinery all key on one turn. Session
// continuity is the runtime's own business, addressed through
// TurnInput.SessionRef (Claude: session id via WithResume; Pi: session file
// path) — the caller stores the ref on the thread and hands it back.
package agentruntime

import (
	"context"
	"fmt"

	cloudproxy "server/desktop/cloud_proxy"
)

// Runtime runs one agent turn, streaming unified Events through emit as it
// goes. Implementations must:
//
//   - return ctx.Err() unwrapped (or wrapped with errors.Is intact) on user
//     cancellation — stopping is not a failure to report;
//   - return a *RuntimeError for every other failure, so the caller can emit
//     one typed proxy_error and mark the intent interrupted;
//   - stop at the first non-nil error from emit and return it — a dead SSE
//     writer means the client is gone.
type Runtime interface {
	// Name identifies the engine ("claude", "pi") for logs and telemetry.
	Name() string
	RunTurn(ctx context.Context, in TurnInput, emit EmitFunc) error
}

// EmitFunc receives unified events in stream order. It is called from the
// runtime's pump goroutine only — implementations need not be safe for
// concurrent use.
type EmitFunc func(Event) error

// TurnInput is everything a runtime needs to run one turn. The caller has
// already done the route-level work: profile resolution, retrieval injection,
// history assembly, attachment loading, workspace creation.
type TurnInput struct {
	// Prompt is the fully assembled prompt text. Structured message history
	// replaces this in R1 (Claude WithResume / Pi session files make the
	// runtime own its history); until then the caller flattens.
	Prompt string

	// Workspace is the per-thread directory tools operate in.
	Workspace string

	// BaseURL / APIKey / ModelID describe the user's configured endpoint.
	BaseURL string
	APIKey  string
	ModelID string

	// SessionRef is the runtime-interpreted continuity handle from the last
	// turn on this thread ("" = fresh session). The runtime reports the ref
	// to store for next time through Event{Kind: EventSessionRef}.
	SessionRef string

	// Approvals switches the runtime from pre-approved mode (nil: the legacy
	// bypass-plus-allowlist recipe) to interactive approvals: read surface
	// auto-allowed, write surface asks through the broker, everything else
	// denied.
	Approvals *ApprovalConfig
}

// RuntimeError is the one failure shape a Runtime may return besides context
// cancellation. Fields mirror cloudproxy.ProxyError so the caller's mapping
// is mechanical.
type RuntimeError struct {
	Kind      cloudproxy.ProxyErrorKind
	Message   string // user-facing, localized by the runtime
	Retryable bool
	Details   map[string]any
}

func (e *RuntimeError) Error() string {
	return fmt.Sprintf("agent runtime: %s", e.Message)
}
