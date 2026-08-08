package workagent

// turn_lifecycle.go — B5 Slice 1. Extracts the three lifecycle
// chores both SSE handlers (HandleAgentChat for general work-agent
// and HandleCanvasAgentChat for canvas-agent) duplicate at the top
// of every turn:
//
//   1. Per-thread mutex acquisition (reject-not-queue on contention).
//   2. THREAD_BUSY SSE envelope construction (same shape across
//      surfaces, only the human phrase differs).
//   3. SSE response headers + flusher cast + initial `: connected`
//      ping.
//
// Three primitives, each policy-free — they construct values, they
// don't write to the response. Surfaces still own how to emit
// envelopes through their own sendEvent closure (the two surfaces
// have slightly different signatures: general's sendEvent returns
// void; canvas's returns bool so the C-08 heartbeat can short-
// circuit). Keeping helpers value-only makes them trivially
// testable and avoids re-introducing the signature drift these
// were meant to deduplicate.
//
// Slice 2 will fold the SDK callback construction + result
// finalize on top of these; Slice 1 stays scoped to chores both
// handlers ALREADY do identically.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// TurnLock wraps the per-thread mutex acquired via the package's
// agentTurnLocks sync.Map. Returned by TryAcquireTurnLock on
// success; caller MUST defer Release() before any code path that
// could exit the handler.
//
// The mutex itself is process-wide (lives in agent_turn_lock.go's
// sync.Map keyed by thread UUID) — TurnLock is just a typed handle
// so the caller can't release a lock they didn't acquire.
type TurnLock struct{ mu *sync.Mutex }

// Release unlocks the per-thread mutex. Idempotency note: calling
// Release on the zero value is a no-op (mu == nil short-circuit),
// so a `defer lock.Release()` that's reached via the
// `if !ok { return }` path doesn't panic.
func (l TurnLock) Release() {
	if l.mu != nil {
		l.mu.Unlock()
	}
}

// TryAcquireTurnLock attempts the per-thread mutex without
// blocking. Returns (lock, true) on success — caller defers
// Release(). On contention returns (zero, false); caller is
// responsible for emitting the THREAD_BUSY done event via
// BuildThreadBusyPayload and exiting the handler.
//
// Why TryLock and not Lock: blocking would silently queue a
// second concurrent send behind a 10-minute agent turn. With
// time.Now() fallback idempotency keys, both reservations could
// finalize and the user pays twice. Reject-fast is the right
// posture; the user gets a structured error and can retry once
// the first turn completes.
func TryAcquireTurnLock(threadKey string) (TurnLock, bool) {
	mu := GetAgentTurnLock(threadKey)
	if !mu.TryLock() {
		return TurnLock{}, false
	}
	return TurnLock{mu: mu}, true
}

// BuildThreadBusyPayload returns the canonical THREAD_BUSY error
// envelope as raw JSON, ready to splice into an AgentSSEEvent.
// The human-readable phrase is caller-supplied so each surface
// can localise it ("Another message" / "Another Canvas Agent
// message" / future surfaces' own wording).
//
// The wire shape is fixed: type=result + subtype=thread_busy +
// is_error=true + code=THREAD_BUSY + error=<phrase>. The frontend
// dispatches on code, so the phrase is the only safe field to
// vary. Marshal failure is impossible for this static shape; an
// errors.Is check would be defensive theatre.
func BuildThreadBusyPayload(humanPhrase string) json.RawMessage {
	payload := map[string]interface{}{
		"type":     "result",
		"subtype":  "thread_busy",
		"is_error": true,
		"error":    humanPhrase,
		"code":     "THREAD_BUSY",
	}
	data, _ := json.Marshal(payload)
	return json.RawMessage(data)
}

// TurnSSESetup carries the values SetupTurnSSE produced — the
// flusher the handler writes through and the temp message ID
// every SSE event will carry until the real message row is
// inserted. Bundled in a struct so the caller binding is
// readable: `setup, ok := SetupTurnSSE(c, "agent")`.
type TurnSSESetup struct {
	// Flusher is the http.Flusher cast of c.Writer. Caller uses
	// it after every write that needs to land at the client
	// immediately (chunk-boundary writes; the initial ping).
	Flusher http.Flusher
	// TempMessageID is the placeholder message id every SSE
	// event carries until the persistent row exists. Mints with
	// the supplied prefix + UnixNano + atomic counter so two
	// turns minted in the same nanosecond don't share an SSE
	// row key on the frontend.
	TempMessageID string
}

// turnTempMessageIDSeq guarantees uniqueness when UnixNano
// resolution isn't enough (Go's runtime can mint two
// time.Now().UnixNano() values in the same nanosecond on hot
// loops). Mirrors the agentMessageIDSeq atomic in agent_api.go's
// older inline path — the two are consolidating here so future
// surfaces don't each invent their own counter.
var turnTempMessageIDSeq atomic.Uint64

// SetupTurnSSE writes the SSE response headers, casts the writer
// to http.Flusher, mints a temp message ID, and emits the
// initial `: connected` comment so the upstream Next.js stream
// proxy gets headers BEFORE the first SDK chunk (otherwise the
// proxy buffers up to several seconds waiting for content type
// to settle, which the user reads as "the agent is frozen").
//
// Returns (zero, false) when the writer can't stream — in that
// case the function ALSO emits a 500 JSON body so the caller can
// just `return` after the ok check; no double-write risk.
//
// tempMessageIDPrefix is the surface's stable prefix
// ("temp_agent" / "temp_canvas_agent") that lets log readers
// trace which surface owned a given temp id.
func SetupTurnSSE(c *gin.Context, tempMessageIDPrefix string) (TurnSSESetup, bool) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming not supported"})
		return TurnSSESetup{}, false
	}

	tempID := fmt.Sprintf("%s_%d-%d",
		tempMessageIDPrefix,
		time.Now().UnixNano(),
		turnTempMessageIDSeq.Add(1),
	)

	// Bootstrap comment — see function header.
	fmt.Fprint(c.Writer, ": connected\n\n")
	flusher.Flush()

	return TurnSSESetup{Flusher: flusher, TempMessageID: tempID}, true
}
