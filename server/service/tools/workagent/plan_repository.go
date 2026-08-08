package workagent

import (
	"encoding/json"
	"fmt"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

// PlanRepository owns every write that touches a chat thread's
// latest_plan / plan_history columns. Pulled out of HandleAgentChat —
// previously the handler reached into globals.GraDBs["system"] to do
// the read, the merge, and the write inline (~25 lines per save), and
// every other column on chat_thread also had its own ad-hoc Updates(...)
// call site. The handler is now a single SnapshotPlan call; future
// thread-row writers can grow on this same shape.
//
// Read-modify-write safety is unchanged from the inline version:
// agentTurnLocks in api/pro/tools/workagent serializes turns per thread
// within the process, and a sticky-session deployment is assumed (see
// the platform-state docs and the C2 architectural note in the
// architecture review). When that assumption is dropped, swap the body
// of SnapshotPlan to a single SQL UPDATE with JSON_ARRAY_APPEND and the
// rest of the system stays put.
type PlanRepository struct {
	db *gorm.DB
}

// NewPlanRepository wraps a *gorm.DB. Tests construct a repository
// against a fixture DB; production calls DefaultPlanRepository to pick
// up the system-tenant connection from globals.
func NewPlanRepository(db *gorm.DB) *PlanRepository {
	return &PlanRepository{db: db}
}

// DefaultPlanRepository returns a repository bound to the system DB.
// Convenience for handler code that has no other reason to plumb a *gorm.DB.
func DefaultPlanRepository() *PlanRepository {
	return NewPlanRepository(globals.GraDBs["system"])
}

// planHistoryCap bounds the rolling-history depth. 10 gives the timeline
// enough depth for longer design/revision sessions while keeping the
// thread-row JSON bounded. Live here so a future change is a one-constant
// tweak rather than a schema migration.
const planHistoryCap = 10

// planHistoryEntry mirrors the JSON shape stored in
// w_workagent_thread.plan_history: each entry pairs a TodoWrite
// snapshot (the raw input.todos value) with the wall-clock time
// the turn that emitted it was saved. The frontend's TodoSnapshot
// reads {todos, captured_at} keys directly.
type planHistoryEntry struct {
	Todos      json.RawMessage `json:"todos"`
	CapturedAt string          `json:"captured_at"`
}

// SnapshotPlan persists a fresh TodoWrite snapshot for the given thread:
// it sets latest_plan to the new JSON and appends-and-truncates the
// rolling plan_history. Empty planJSON is a no-op (no point overwriting
// the column with nothing).
//
// Failure is non-fatal in the caller's perspective — the message-walk
// fallback in the frontend still rehydrates the Progress section from
// loaded pages — so the handler can log-and-continue rather than rolling
// back the whole conversation save. We still return the error so the
// caller decides how loud to be about it.
func (r *PlanRepository) SnapshotPlan(threadID uint, planJSON string, now time.Time) error {
	if planJSON == "" {
		return nil
	}

	var existingHistory string
	// Pluck error is intentionally ignored: a missing row, a NULL
	// column, or a transient connection blip should fall through to
	// "treat as empty history" rather than abort the whole save. The
	// follow-up Updates() will surface a real DB problem with a clear
	// error path.
	_ = r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Pluck("plan_history", &existingHistory).Error

	nextHistory := mergePlanHistory(existingHistory, planJSON, now)

	updates := map[string]interface{}{"latest_plan": planJSON}
	if nextHistory != "" {
		updates["plan_history"] = nextHistory
	}

	if err := r.db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", threadID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("plan snapshot: %w", err)
	}
	return nil
}

// mergePlanHistory takes the existing plan_history JSON (may be empty
// or NULL-equivalent ""), appends a new {todos, captured_at} entry,
// and truncates to the most-recent planHistoryCap entries. Returns
// the JSON to write back, or "" if marshaling fails (caller should
// keep the old value rather than blanking the column).
//
// Tolerant of corrupt existing JSON: a parse failure resets to a
// fresh single-entry slice rather than rejecting the whole save.
// That matches the "writer is best-effort, message walk is the truth"
// stance for latest_plan — we'd rather have a partial history than
// drop the column on a one-off corruption.
func mergePlanHistory(existing, newTodosJSON string, now time.Time) string {
	var entries []planHistoryEntry
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &entries)
	}
	entries = append(entries, planHistoryEntry{
		Todos:      json.RawMessage(newTodosJSON),
		CapturedAt: now.UTC().Format(time.RFC3339),
	})
	if len(entries) > planHistoryCap {
		entries = entries[len(entries)-planHistoryCap:]
	}
	out, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(out)
}
