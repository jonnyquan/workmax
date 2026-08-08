package workagent

import (
	"sync"
	"time"
)

// toolDurationTracker measures tool execution duration across a
// PreToolUse → PostToolUse pair. Per-turn instance so concurrent
// turns can't see each other's tool_use_ids.
//
// Lifetime is the same as the agent loop's stack frame: when
// processAgentConversationInternal returns, the tracker goes out of
// scope and the underlying map is GC'd. Tool calls are sequential per
// the SDK protocol so the map size stays bounded by tools-per-turn,
// not turn duration.
type toolDurationTracker struct {
	mu     sync.Mutex
	starts map[string]time.Time
}

// newToolDurationTracker returns an empty tracker ready for record.
func newToolDurationTracker() *toolDurationTracker {
	return &toolDurationTracker{starts: make(map[string]time.Time)}
}

// record marks toolUseID as started now. Empty/nil id is a no-op —
// some hook payloads omit the id and we tolerate rather than fail
// (a missing duration log line beats a panic).
func (t *toolDurationTracker) record(toolUseID *string) {
	if toolUseID == nil || *toolUseID == "" {
		return
	}
	t.mu.Lock()
	t.starts[*toolUseID] = time.Now()
	t.mu.Unlock()
}

// consume reads-and-removes the start time, returning the elapsed
// duration. ok=false signals no matching record (PostToolUse fired
// without a matching PreToolUse — e.g. denied tool, SDK reordering,
// or a hook fired pre-record). Callers log without the duration in
// that case rather than reporting a bogus value.
func (t *toolDurationTracker) consume(toolUseID *string) (time.Duration, bool) {
	if toolUseID == nil || *toolUseID == "" {
		return 0, false
	}
	t.mu.Lock()
	start, ok := t.starts[*toolUseID]
	if ok {
		delete(t.starts, *toolUseID)
	}
	t.mu.Unlock()
	if !ok {
		return 0, false
	}
	return time.Since(start), true
}

// inflightCount returns the number of recorded-but-not-yet-consumed
// entries. Exposed for tests + diagnostics; never read on the hot
// path since the inflight count isn't a control signal — the
// per-turn map is always tiny.
func (t *toolDurationTracker) inflightCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.starts)
}
