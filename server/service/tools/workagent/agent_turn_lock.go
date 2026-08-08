package workagent

import "sync"

// agentTurnLocks serializes agent turns within a single conversation
// thread regardless of which surface (general work agent, canvas
// agent, or future surface) is driving the SSE handler. Threads
// share the `w_workagent_thread` table and carry globally-unique
// UUIDs, so no per-surface prefixing is required.
//
// Process-wide map. Entries are never deleted (mirrors
// file_service / sse_connection_manager patterns); bounded by total
// thread count.
//
// Acquisition uses TryLock + reject-with-busy-event in callers; never
// blocking. The lock guards the (Reserve → Process → Finalize) cycle
// so two concurrent SSE turns on the same thread can't double-charge
// when the time.Now()-fallback idempotency key collides.
var agentTurnLocks sync.Map // map[string]*sync.Mutex, keyed by thread UUID

// GetAgentTurnLock returns the per-thread mutex used by SSE turn
// dispatchers (HandleAgentChat for general work-agent, AgentChat for
// canvas-agent) to serialize concurrent turns on the same
// conversation. Caller must use TryLock + send a thread_busy SSE
// event on contention; see workagent/agent_api.go for the canonical
// pattern.
//
// Pre-2026-05 there were two parallel maps (agentTurnLocks +
// canvasAgentTurnLocks) implementing the same pattern; consolidating
// them here means future surfaces (R3 Option A surface registry)
// share one lock space and one bug surface.
func GetAgentTurnLock(threadID string) *sync.Mutex {
	if existing, ok := agentTurnLocks.Load(threadID); ok {
		return existing.(*sync.Mutex)
	}
	created := &sync.Mutex{}
	actual, _ := agentTurnLocks.LoadOrStore(threadID, created)
	return actual.(*sync.Mutex)
}
