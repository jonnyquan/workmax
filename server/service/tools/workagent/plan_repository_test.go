package workagent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

// mergePlanHistory drives the push-and-truncate behaviour for the
// rolling plan_history column. The contract is: append the new
// snapshot, keep at most planHistoryCap entries (drop oldest), tolerate
// corrupt existing JSON without losing the new entry, and emit valid
// JSON the frontend can decode. These four cases moved here verbatim
// from api/pro/tools/workagent/agent_api_todo_plan_test.go when the
// merge + DB write was extracted into PlanRepository.
func TestMergePlanHistory_AppendsToEmpty(t *testing.T) {
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	got := mergePlanHistory("", `[{"id":"1","content":"first"}]`, now)
	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("merged history must be valid JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one entry after empty append, got %d", len(entries))
	}
	if !strings.Contains(string(entries[0].Todos), `"first"`) {
		t.Errorf("entry todos lost: %s", string(entries[0].Todos))
	}
	if entries[0].CapturedAt != "2026-04-30T12:00:00Z" {
		t.Errorf("capturedAt = %q, want RFC3339 UTC", entries[0].CapturedAt)
	}
}

func TestMergePlanHistory_AppendsToExisting(t *testing.T) {
	existing := `[{"todos":[{"id":"1","content":"old"}],"captured_at":"2026-04-29T00:00:00Z"}]`
	got := mergePlanHistory(existing, `[{"id":"2","content":"new"}]`, time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC))
	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("merged history must be valid JSON: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(entries))
	}
	// Append must be at the END so the oldest stays at the head —
	// the timeline UI's iteration order depends on this.
	if !strings.Contains(string(entries[0].Todos), `"old"`) {
		t.Errorf("oldest entry not at index 0: %s", string(entries[0].Todos))
	}
	if !strings.Contains(string(entries[1].Todos), `"new"`) {
		t.Errorf("newest entry not at last index: %s", string(entries[1].Todos))
	}
}

func TestMergePlanHistory_TruncatesPastCap(t *testing.T) {
	// Build a full existing history, then append one more — oldest
	// must be dropped, leaving exactly planHistoryCap entries with
	// the brand-new one at the tail.
	var seed []planHistoryEntry
	for i := 0; i < planHistoryCap; i++ {
		seed = append(seed, planHistoryEntry{
			Todos:      json.RawMessage(`[{"id":"` + string(rune('a'+i)) + `","content":"phase ` + string(rune('a'+i)) + `"}]`),
			CapturedAt: "2026-04-29T00:00:00Z",
		})
	}
	existingJSON, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("seed marshal: %v", err)
	}

	newPhase := "phase new"
	got := mergePlanHistory(string(existingJSON), `[{"id":"new","content":"`+newPhase+`"}]`, time.Now().UTC())
	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("merged history must be valid JSON: %v", err)
	}
	if len(entries) != planHistoryCap {
		t.Fatalf("expected %d entries after cap, got %d", planHistoryCap, len(entries))
	}
	// The first seed entry ("phase a") was the oldest and must have
	// been evicted; the explicit new phase must be the final entry.
	if strings.Contains(string(entries[0].Todos), `"phase a"`) {
		t.Error("oldest entry was not evicted past cap")
	}
	if !strings.Contains(string(entries[len(entries)-1].Todos), `"`+newPhase+`"`) {
		t.Errorf("newest entry not at tail: %s", string(entries[len(entries)-1].Todos))
	}
}

func TestMergePlanHistory_TolerantToCorruptExisting(t *testing.T) {
	// Garbage in the existing column should not block the new save.
	// Falling back to a fresh single-entry slice is preferred over
	// dropping the column, so the next turn's history starts clean
	// rather than staying corrupt.
	got := mergePlanHistory("{not json}", `[{"id":"1","content":"recover"}]`, time.Now().UTC())
	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("recovery output must be valid JSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected single recovery entry, got %d", len(entries))
	}
	if !strings.Contains(string(entries[0].Todos), `"recover"`) {
		t.Errorf("recovery entry todos missing: %s", string(entries[0].Todos))
	}
}

// SnapshotPlan is the only DB-coupled path on PlanRepository — it does
// the read + merge + write in one shot. These integration tests run
// against an in-memory SQLite via testutil.NewTestDB and pin the four
// load-bearing behaviours: first-write, append, cap-and-truncate, and
// no-op on empty input.

func seedThread(t *testing.T) (uint, *PlanRepository) {
	t.Helper()
	db := testutil.NewTestDB(t)
	thread := workagentModel.ChatThread{
		UID:  42,
		UUID: "test-uuid",
		Name: "snapshot test thread",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return thread.Id, NewPlanRepository(db)
}

func loadPlanColumns(t *testing.T, repo *PlanRepository, threadID uint) (latest, history string) {
	t.Helper()
	var row workagentModel.ChatThread
	if err := repo.db.Where("id = ?", threadID).First(&row).Error; err != nil {
		t.Fatalf("load thread: %v", err)
	}
	return row.LatestPlan, row.PlanHistory
}

func TestPlanRepository_SnapshotPlan_FirstWriteSetsLatestAndOneEntryHistory(t *testing.T) {
	threadID, repo := seedThread(t)
	plan := `[{"id":"1","content":"step one"}]`
	now := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)

	if err := repo.SnapshotPlan(threadID, plan, now); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	latest, history := loadPlanColumns(t, repo, threadID)
	if latest != plan {
		t.Errorf("latest_plan = %q, want %q", latest, plan)
	}

	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(history), &entries); err != nil {
		t.Fatalf("history must be valid JSON: %v (raw=%q)", err, history)
	}
	if len(entries) != 1 {
		t.Errorf("expected single history entry on first write, got %d", len(entries))
	}
	if entries[0].CapturedAt != "2026-04-30T12:00:00Z" {
		t.Errorf("captured_at = %q, want RFC3339 UTC", entries[0].CapturedAt)
	}
}

func TestPlanRepository_SnapshotPlan_AppendsAndTruncates(t *testing.T) {
	threadID, repo := seedThread(t)
	base := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)

	// Push past planHistoryCap so the first one evicts. Verifies the
	// read-modify-write actually round-trips through the DB column
	// rather than just operating in memory.
	for i := 0; i < planHistoryCap+1; i++ {
		plan := `[{"id":"` + string(rune('a'+i)) + `","content":"phase ` + string(rune('a'+i)) + `"}]`
		if err := repo.SnapshotPlan(threadID, plan, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}

	latest, history := loadPlanColumns(t, repo, threadID)

	// latest_plan reflects the most recent write only.
	lastPhase := "phase " + string(rune('a'+planHistoryCap))
	if !strings.Contains(latest, `"`+lastPhase+`"`) {
		t.Errorf("latest_plan = %q, expected to contain %q", latest, lastPhase)
	}

	var entries []planHistoryEntry
	if err := json.Unmarshal([]byte(history), &entries); err != nil {
		t.Fatalf("history must be valid JSON: %v", err)
	}
	if len(entries) != planHistoryCap {
		t.Fatalf("expected exactly %d history entries after cap, got %d", planHistoryCap, len(entries))
	}
	// Oldest ("phase a") must have been evicted; newest phase
	// must sit at the tail. Captures the timeline-iteration contract
	// the frontend relies on.
	if strings.Contains(string(entries[0].Todos), `"phase a"`) {
		t.Error("oldest entry should have been evicted past cap")
	}
	if !strings.Contains(string(entries[len(entries)-1].Todos), `"`+lastPhase+`"`) {
		t.Errorf("newest entry not at tail: %s", string(entries[len(entries)-1].Todos))
	}
}

func TestPlanRepository_SnapshotPlan_EmptyPlanIsNoOp(t *testing.T) {
	threadID, repo := seedThread(t)

	// First write a real plan so the columns are populated.
	if err := repo.SnapshotPlan(threadID, `[{"id":"1","content":"first"}]`, time.Now().UTC()); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	latestBefore, historyBefore := loadPlanColumns(t, repo, threadID)

	// Empty plan must NOT clobber the existing values — the contract
	// is "if there's nothing to record, leave the row alone." A naive
	// Updates(map{"latest_plan": ""}) would zero the column.
	if err := repo.SnapshotPlan(threadID, "", time.Now().UTC()); err != nil {
		t.Fatalf("empty snapshot returned error: %v", err)
	}

	latestAfter, historyAfter := loadPlanColumns(t, repo, threadID)
	if latestAfter != latestBefore {
		t.Errorf("empty plan clobbered latest_plan: before=%q after=%q", latestBefore, latestAfter)
	}
	if historyAfter != historyBefore {
		t.Errorf("empty plan clobbered plan_history")
	}
}
