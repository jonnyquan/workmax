//go:build desktop

package agentruntime

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// ApprovalDecision is the user's answer to one approval request. The four
// values mirror the renderer's card: allow this call, allow this tool for
// the thread, allow it permanently, or refuse.
type ApprovalDecision string

const (
	ApprovalAllowOnce    ApprovalDecision = "allow_once"
	ApprovalAllowSession ApprovalDecision = "allow_session"
	ApprovalAllowAlways  ApprovalDecision = "allow_always"
	ApprovalDeny         ApprovalDecision = "deny"
)

// ValidApprovalDecision reports whether s is one of the four decisions —
// the approve endpoint's input gate.
func ValidApprovalDecision(s string) bool {
	switch ApprovalDecision(s) {
	case ApprovalAllowOnce, ApprovalAllowSession, ApprovalAllowAlways, ApprovalDeny:
		return true
	}
	return false
}

// ApprovalReadSurface and ApprovalWriteSurface are the shared approval
// vocabulary: the tools that never ask, and the tools that do. They live here
// rather than in either engine because ONE ApprovalConfig serves both — the
// same local_agent.Engine builds it whether the turn runs on the claude CLI or
// on pi — and because a name missing from both sets is denied outright by
// Consult, with no card and no appeal.
//
// So the sets are the UNION of what the two runtimes can call, not either
// runtime's own surface. Find and Ls are the visible consequence: pi's read
// profile carries `find` and `ls`, the claude CLI has neither, and the
// vocabulary has to hold both or pi's own read tools fall off the edge of the
// policy. Today the pi extension only gates write/edit, so a find would never
// reach Consult and the gap was invisible; the moment that gate widens it
// becomes a flat refusal of a tool the engine deliberately enabled.
//
// The names are the Claude spellings because durable grants are stored under
// them (w_desktop_agent_permission_rule) and a grant given on one engine must
// silence the other. pi normalizes into this vocabulary — see
// pi_agent.claudeToolName — and each of its tools keeps a name of its own
// rather than being folded onto a near-equivalent: pi's tool_execution_end
// cannot name a file, so the renderer settles the oldest OPEN call of that
// tool, and two distinct tools sharing one name would settle each other's row.
//
// Both engines' surfaces are pinned against these sets by tests
// (pi_agent.TestEveryEnabledPiToolHasAHomeInTheVocabulary and
// local_agent.TestEveryClaudeToolHasAHomeInTheVocabulary), so a tool added to
// either runtime cannot quietly land outside the policy again.
var (
	ApprovalReadSurface  = []string{"Read", "Glob", "Grep", "Find", "Ls"}
	ApprovalWriteSurface = []string{"Write", "Edit"}
)

// OutsideSurfaceReason is the sentence a user reads when a call was refused
// for being outside the local loop's tool surface. One string, because the
// three places that can refuse a call for that reason — Consult, the claude
// engine's PreToolUse hook, and pi's own --tools restriction — are the same
// event as far as the reader is concerned, and telling them apart is not the
// reader's job.
const OutsideSurfaceReason = "工具不在本地循环的许可面内"

// ApprovalSurfaceHas reports whether a tool name has any home in the shared
// vocabulary — auto-allowed or askable. Anything else is outside the local
// loop's surface entirely.
func ApprovalSurfaceHas(tool string) bool {
	for _, known := range ApprovalReadSurface {
		if known == tool {
			return true
		}
	}
	for _, known := range ApprovalWriteSurface {
		if known == tool {
			return true
		}
	}
	return false
}

// DefaultApprovalTimeout bounds how long a tool call waits for the user.
// A renderer that never answers (closed window, old version without the
// card) must not hang the turn forever; timing out denies, and a denial is
// a visible, recoverable outcome.
const DefaultApprovalTimeout = 120 * time.Second

// ApprovalBroker connects the runtime goroutine that must block on a
// decision with the HTTP handler that delivers it. One broker per process;
// requests are keyed by (turnUUID, approvalID) so a stale answer for a
// finished turn resolves nothing.
//
// It also holds session grants — "allow this tool for this thread until the
// sidecar restarts". They live here rather than in SQLite because their
// lifetime IS the process.
type ApprovalBroker struct {
	mu      sync.Mutex
	counter int
	pending map[string]chan ApprovalDecision
	session map[string]bool // threadUUID + "\x00" + tool
}

func NewApprovalBroker() *ApprovalBroker {
	return &ApprovalBroker{
		pending: make(map[string]chan ApprovalDecision),
		session: make(map[string]bool),
	}
}

func pendingKey(turnUUID, approvalID string) string { return turnUUID + "\x00" + approvalID }
func sessionKey(threadUUID, tool string) string     { return threadUUID + "\x00" + tool }

// Begin registers a new approval request and returns its id (unique within
// the process lifetime, stable enough for the wire).
func (b *ApprovalBroker) Begin(turnUUID string) (id string, wait <-chan ApprovalDecision) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.counter++
	id = fmt.Sprintf("ap-%d", b.counter)
	ch := make(chan ApprovalDecision, 1)
	b.pending[pendingKey(turnUUID, id)] = ch
	return id, ch
}

// Resolve delivers the user's decision. Returns false when nothing is
// waiting under that key — the turn ended, timed out, or the id is made up.
func (b *ApprovalBroker) Resolve(turnUUID, approvalID string, d ApprovalDecision) bool {
	b.mu.Lock()
	ch, ok := b.pending[pendingKey(turnUUID, approvalID)]
	if ok {
		delete(b.pending, pendingKey(turnUUID, approvalID))
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	ch <- d // buffered; never blocks
	return true
}

// Abandon drops a request the runtime stopped waiting for, so a later
// Resolve returns false instead of writing to a channel nobody reads.
func (b *ApprovalBroker) Abandon(turnUUID, approvalID string) {
	b.mu.Lock()
	delete(b.pending, pendingKey(turnUUID, approvalID))
	b.mu.Unlock()
}

// Await blocks until the decision arrives, the timeout passes, or ctx ends.
// Timeout and cancellation both come back as ApprovalDeny with an error for
// the caller's narration; the request is abandoned either way.
func (b *ApprovalBroker) Await(ctx context.Context, turnUUID, approvalID string, wait <-chan ApprovalDecision, timeout time.Duration) (ApprovalDecision, error) {
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case d := <-wait:
		return d, nil
	case <-timer.C:
		b.Abandon(turnUUID, approvalID)
		return ApprovalDeny, fmt.Errorf("approval timed out after %s", timeout)
	case <-ctx.Done():
		b.Abandon(turnUUID, approvalID)
		return ApprovalDeny, ctx.Err()
	}
}

// GrantSession records "allow this tool for this thread" for the rest of
// the process lifetime.
func (b *ApprovalBroker) GrantSession(threadUUID, tool string) {
	b.mu.Lock()
	b.session[sessionKey(threadUUID, tool)] = true
	b.mu.Unlock()
}

// SessionGranted reports whether the thread already granted the tool.
func (b *ApprovalBroker) SessionGranted(threadUUID, tool string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.session[sessionKey(threadUUID, tool)]
}

// ApprovalConfig is a turn's approval policy, assembled by the engine from
// the tool surface, stored rules, and session grants. Nil on TurnInput means
// the legacy pre-approval mode (bypass + allowlist) — no asking.
type ApprovalConfig struct {
	Broker *ApprovalBroker
	// TurnUUID / ThreadUUID key pending requests and session grants.
	TurnUUID   string
	ThreadUUID string
	// AutoAllowed tools never ask this turn: the read-only surface plus
	// every stored "always" rule and session grant.
	AutoAllowed map[string]bool
	// AskAllowed tools may ask the user. A tool in neither set is denied
	// outright — fail closed for anything outside the declared surface.
	AskAllowed map[string]bool
	// Persist records an ApprovalAllowAlways answer durably. May be nil.
	Persist func(tool string)
	// Timeout overrides DefaultApprovalTimeout when > 0.
	Timeout time.Duration
}

// ConsultResult is the outcome of one Consult: whether the tool may run, the
// effective decision (allow_once on an auto-allow/session short-circuit), and
// an English denial reason for the model's benefit when it may not.
type ConsultResult struct {
	Allowed  bool
	Decision ApprovalDecision
	Reason   string
}

// Consult runs one tool call through the unified approval flow both engines
// share: auto-allow the declared read surface and every stored grant, ask the
// user for the write surface (Begin → EventApprovalReq → Await), deny
// everything else outright. allow_session/allow_always answers apply their
// grant bookkeeping here, so the two engines draw on the same grants.
//
// tool must already be in the config's vocabulary (the Claude tool names);
// engines with different native names normalize before calling. The error
// return is non-nil only when the EventApprovalReq emit failed — the client
// is gone and the caller decides how to abort. Denial events for the other
// paths are emitted best-effort: a denial is already terminal for the call.
func (cfg *ApprovalConfig) Consult(ctx context.Context, emit EmitFunc, tool, target string) (ConsultResult, error) {
	if cfg.AutoAllowed[tool] || cfg.Broker.SessionGranted(cfg.ThreadUUID, tool) {
		return ConsultResult{Allowed: true, Decision: ApprovalAllowOnce}, nil
	}
	if !cfg.AskAllowed[tool] {
		// Outside the declared surface entirely — no card, no appeal.
		_ = emit(Event{
			Kind: EventToolDenied,
			Tool: ToolEvent{Name: tool, Target: target, Reason: OutsideSurfaceReason},
		})
		return ConsultResult{
			Decision: ApprovalDeny,
			Reason:   fmt.Sprintf("tool %s is outside the local loop's surface", tool),
		}, nil
	}

	id, wait := cfg.Broker.Begin(cfg.TurnUUID)
	if werr := emit(Event{
		Kind:       EventApprovalReq,
		ApprovalID: id,
		Tool:       ToolEvent{Name: tool, Target: target},
	}); werr != nil {
		cfg.Broker.Abandon(cfg.TurnUUID, id)
		return ConsultResult{}, werr
	}
	decision, aerr := cfg.Broker.Await(ctx, cfg.TurnUUID, id, wait, cfg.Timeout)
	if aerr != nil {
		log.Printf("agent approval: %s %s: %v", tool, id, aerr)
		_ = emit(Event{
			Kind: EventToolDenied,
			Tool: ToolEvent{Name: tool, Target: target, Reason: "等待批准超时或中止"},
		})
		return ConsultResult{Decision: ApprovalDeny, Reason: "the user did not approve in time"}, nil
	}
	switch decision {
	case ApprovalAllowOnce:
		return ConsultResult{Allowed: true, Decision: decision}, nil
	case ApprovalAllowSession:
		cfg.Broker.GrantSession(cfg.ThreadUUID, tool)
		return ConsultResult{Allowed: true, Decision: decision}, nil
	case ApprovalAllowAlways:
		cfg.Broker.GrantSession(cfg.ThreadUUID, tool)
		if cfg.Persist != nil {
			cfg.Persist(tool)
		}
		return ConsultResult{Allowed: true, Decision: decision}, nil
	default:
		_ = emit(Event{
			Kind: EventToolDenied,
			Tool: ToolEvent{Name: tool, Target: target, Reason: "用户拒绝了此操作"},
		})
		return ConsultResult{Decision: ApprovalDeny, Reason: "the user declined this tool call"}, nil
	}
}
