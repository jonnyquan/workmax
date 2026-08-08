package workagent

// turn_sse_emitter.go — B5 Slice 2a. Shared SSE event emitter.
//
// Both surfaces (general work-agent and canvas-agent) opened every
// turn with the same ~20-line sendEvent closure:
//
//   1. json.Marshal(event) → bytes
//   2. Frame as "data: <bytes>\n\n"
//   3. If a registered SSEConnection exists, write through it
//      (the cleanup-ticker / LastActivity bumps live there)
//   4. Otherwise write via c.Writer under a mutex + Flush()
//
// The pre-Slice-2a versions diverged on two minor axes:
//
//   • Return type: work-agent's closure returned void; canvas's
//     returned bool so the C-08 heartbeat goroutine could short-
//     circuit on broken streams. SSEEmitter standardises on the
//     bool return; callers that don't care just discard it.
//
//   • Stored conn shape: work-agent used a plain *SSEConnection
//     (assigned once after registerSSEConnection, read from
//     callbacks afterward); canvas used atomic.Pointer because the
//     C-08 heartbeat goroutine reads concurrently with the main
//     assign. SSEEmitter always uses atomic.Pointer — strictly
//     safer than the plain ptr (the work-agent pattern relied on
//     channel happens-before; explicit atomic stays correct under
//     future refactors).
//
//   • Log prefix: "[Agent API]" vs "[CanvasAgent]". Caller-supplied
//     so log greps stay meaningful per surface.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"server/globals"
	workagentModel "server/model/workagent"
)

// SSEEmitter wraps the per-turn "marshal + frame + write" loop.
// Constructed once at handler entry, registered with the SSE
// connection manager via SetConnection after registerSSEConnection
// returns, and called from every callback goroutine + the surface-
// specific heartbeat goroutine.
//
// Thread-safety:
//   • conn is atomic.Pointer so the heartbeat goroutine reads
//     don't race with the main-goroutine Store after registration.
//   • writeMu serialises the pre-registration fallback path (raw
//     writer write + Flush) so two concurrent SSE writes can't
//     interleave a frame.
//
// Once SetConnection has been called, every Send routes through
// the SSEConnection's WriteChunk (which has its own mutex), so the
// writeMu becomes dormant. The mutex exists purely for the
// transient window between SetupTurnSSE and registerSSEConnection.
type SSEEmitter struct {
	writer  io.Writer
	flusher http.Flusher
	writeMu sync.Mutex
	conn    atomic.Pointer[SSEConnection]
	logPfx  string
}

// NewSSEEmitter returns an emitter bound to a pre-flushed
// streaming writer + the matching Flusher cast (both produced by
// SetupTurnSSE). logPrefix is the bracketed surface tag prepended
// to warn/error log lines so a "SSE write failed" entry tells the
// log reader which surface it came from.
//
// Construction is allocation-only — the underlying connection is
// nil at this point; the caller calls SetConnection after
// registerSSEConnection returns its tracked conn. Pre-registration
// Send() writes still work, just via the writeMu+flusher fallback.
func NewSSEEmitter(writer io.Writer, flusher http.Flusher, logPrefix string) *SSEEmitter {
	return &SSEEmitter{
		writer:  writer,
		flusher: flusher,
		logPfx:  logPrefix,
	}
}

// SetConnection swaps in the tracked SSEConnection so subsequent
// Send calls route through its WriteChunk (which bumps
// LastActivity and shares the connection's own mutex). Atomic
// because the surface's heartbeat goroutine reads conn
// concurrently with this Store.
func (e *SSEEmitter) SetConnection(conn *SSEConnection) {
	e.conn.Store(conn)
}

// HasConnection reports whether a tracked SSEConnection is
// currently registered. Surfaces that run their own pre-
// registration heartbeat goroutine check this each tick to
// suppress their own heartbeat once the shared SSEConnection
// takes over (its runWriter does its own ticking at a different
// cadence; emitting from both sources would double-pay the
// proxy + race the bounded write queue).
//
// Lock-free read via atomic.Pointer.Load — safe to call from any
// goroutine concurrently with SetConnection / Send.
func (e *SSEEmitter) HasConnection() bool {
	return e.conn.Load() != nil
}

// Send marshals the event to JSON, frames it as one SSE record
// ("data: <json>\n\n"), and writes it through the registered
// connection (preferred) or the raw flusher (transient pre-
// registration window).
//
// Returns true on a successful write, false on marshal or write
// error. Callers that only need the side-effect (most callback
// paths) discard the bool; callers that need to short-circuit a
// heartbeat or break a retry loop on a broken stream act on it.
//
// Every error path logs once at warn / error level via the
// caller-supplied log prefix. No errors are surfaced to the
// caller via panic / return value beyond the bool — sendEvent
// must not block the lifecycle on a transient write.
func (e *SSEEmitter) Send(event workagentModel.AgentSSEEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		globals.Error(fmt.Sprintf("%s Failed to marshal SSE event: %v", e.logPfx, err))
		return false
	}
	payload := []byte(fmt.Sprintf("data: %s\n\n", string(data)))
	if conn := e.conn.Load(); conn != nil {
		if writeErr := conn.WriteChunk(payload); writeErr != nil {
			globals.Warn(fmt.Sprintf("%s SSE write failed: %v", e.logPfx, writeErr))
			return false
		}
		return true
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if _, err := e.writer.Write(payload); err != nil {
		globals.Warn(fmt.Sprintf("%s SSE write failed: %v", e.logPfx, err))
		return false
	}
	e.flusher.Flush()
	return true
}

// WriteRaw bypasses the JSON-event path for callers that need to
// emit a bare SSE comment (e.g. heartbeat: ": hb <unix>\n\n").
// Honors the same connection/fallback dispatch as Send. Returns
// true on a successful write.
//
// Comments are not events — they don't bump LastActivity in the
// usual way, but they DO route through WriteChunk so the
// connection manager treats them like any other byte flow.
func (e *SSEEmitter) WriteRaw(payload []byte) bool {
	if conn := e.conn.Load(); conn != nil {
		if writeErr := conn.WriteChunk(payload); writeErr != nil {
			return false
		}
		return true
	}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if _, err := e.writer.Write(payload); err != nil {
		return false
	}
	e.flusher.Flush()
	return true
}
