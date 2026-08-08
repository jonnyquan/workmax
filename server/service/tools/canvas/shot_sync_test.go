package canvas

import (
	"encoding/json"
	"testing"
	"time"

	"server/globals"
	"server/model"
)

// shot_sync_test.go pins down DiffShots — the pure reconciliation
// planner behind POST /canvas/projects/:id/shots/sync. The DB-backed
// SyncShots wrapper is out of scope here (no sqlite in this package's
// test setup); integration tests exercise the commit path.

func mkShot(id uint, projectID uint, localCardID string, order int, title string) model.Shot {
	s := model.Shot{
		UID:             42,
		CanvasProjectID: projectID,
		LocalCardID:     localCardID,
		OrderIndex:      order,
		Title:           title,
		Status:          model.ShotStatusDraft,
	}
	s.GraMODEL = globals.GraMODEL{Id: id}
	return s
}

func mkLink(localCardID string, order int) ShotLink {
	return ShotLink{
		LocalCardID: localCardID,
		OrderIndex:  order,
	}
}

func TestDiffShots_EmptyDBAllCreates(t *testing.T) {
	incoming := []ShotLink{
		mkLink("card-a", 0),
		mkLink("card-b", 1),
	}
	plan := DiffShots(42, 7, nil, incoming)
	if len(plan.Created) != 2 {
		t.Fatalf("expected 2 creates, got %d", len(plan.Created))
	}
	if plan.Unchanged != 0 || len(plan.Updated) != 0 || len(plan.Deleted) != 0 {
		t.Fatalf("unexpected other actions: %+v", plan)
	}
}

func TestDiffShots_IdempotentReplay(t *testing.T) {
	// After a first pass created two rows, running the planner again
	// with the "existing" set equal to the "incoming" set must produce
	// an empty plan. That's the behaviour that lets the endpoint be
	// called defensively on every save without thrashing the DB.
	incoming := []ShotLink{
		mkLink("card-a", 0),
		mkLink("card-b", 1),
	}
	existing := []model.Shot{
		mkShot(1, 7, "card-a", 0, ""),
		mkShot(2, 7, "card-b", 1, ""),
	}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Created) != 0 || len(plan.Updated) != 0 || len(plan.Deleted) != 0 {
		t.Fatalf("replay mutated DB: %+v", plan)
	}
	if plan.Unchanged != 2 {
		t.Fatalf("expected 2 unchanged, got %d", plan.Unchanged)
	}
}

func TestDiffShots_OrderIndexChangeUpdates(t *testing.T) {
	incoming := []ShotLink{
		mkLink("card-a", 2), // was 0
	}
	existing := []model.Shot{
		mkShot(11, 7, "card-a", 0, ""),
	}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Updated) != 1 {
		t.Fatalf("expected 1 update, got %d", len(plan.Updated))
	}
	if plan.Updated[0].Id != 11 {
		t.Fatalf("update must preserve existing row ID, got %d", plan.Updated[0].Id)
	}
	if plan.Updated[0].OrderIndex != 2 {
		t.Fatalf("order not applied, got %d", plan.Updated[0].OrderIndex)
	}
}

func TestDiffShots_MissingIncomingSoftDeletes(t *testing.T) {
	incoming := []ShotLink{
		mkLink("card-a", 0),
	}
	existing := []model.Shot{
		mkShot(11, 7, "card-a", 0, ""),
		mkShot(12, 7, "card-b", 1, ""),
	}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Deleted) != 1 || plan.Deleted[0] != 12 {
		t.Fatalf("expected soft-delete of id 12, got %+v", plan.Deleted)
	}
	if plan.Unchanged != 1 {
		t.Fatalf("expected 1 unchanged, got %d", plan.Unchanged)
	}
}

func TestDiffShots_ResurrectsSoftDeleted(t *testing.T) {
	// If a card was soft-deleted and then re-added with the same
	// LocalCardID, the planner should produce an update (clearing
	// deleted_at + updating fields) rather than a second create —
	// otherwise the unique index would blow up.
	deletedAt := anyTime()
	deletedRow := mkShot(99, 7, "card-a", 0, "")
	deletedRow.DeletedAt = &deletedAt

	incoming := []ShotLink{
		mkLink("card-a", 5),
	}
	plan := DiffShots(42, 7, []model.Shot{deletedRow}, incoming)
	if len(plan.Created) != 0 {
		t.Fatalf("resurrection must not create a new row: %+v", plan.Created)
	}
	if len(plan.Updated) != 1 {
		t.Fatalf("expected 1 update, got %d", len(plan.Updated))
	}
	if plan.Updated[0].Id != 99 {
		t.Fatalf("must update the existing soft-deleted row, got id %d", plan.Updated[0].Id)
	}
	if plan.Updated[0].DeletedAt != nil {
		t.Fatalf("must clear deleted_at on resurrect, got %+v", plan.Updated[0].DeletedAt)
	}
	if plan.Updated[0].OrderIndex != 5 {
		t.Fatalf("new order not applied: %d", plan.Updated[0].OrderIndex)
	}
}

func TestDiffShots_EmptyLocalCardIDIsDropped(t *testing.T) {
	// A canvas doc with an empty LocalCardID is malformed — silently
	// skip so we don't generate INSERTs with empty unique-key values.
	incoming := []ShotLink{
		mkLink("", 0),
		mkLink("card-a", 1),
	}
	plan := DiffShots(42, 7, nil, incoming)
	if len(plan.Created) != 1 || plan.Created[0].LocalCardID != "card-a" {
		t.Fatalf("empty id must be dropped, got %+v", plan.Created)
	}
}

func TestDiffShots_DuplicateIncomingIDFirstWins(t *testing.T) {
	incoming := []ShotLink{
		mkLink("card-a", 0),
		mkLink("card-a", 99), // duplicate — second must be ignored
	}
	plan := DiffShots(42, 7, nil, incoming)
	if len(plan.Created) != 1 {
		t.Fatalf("duplicate incoming ids must collapse to 1 row, got %d", len(plan.Created))
	}
	if plan.Created[0].OrderIndex != 0 {
		t.Fatalf("first occurrence must win, got order %d", plan.Created[0].OrderIndex)
	}
}

func TestDiffShots_TimelinePtrRoundtrip(t *testing.T) {
	start := int64(100)
	dur := int64(2500)
	incoming := []ShotLink{{
		LocalCardID:      "card-a",
		OrderIndex:       0,
		TimelineStart:    &start,
		TimelineDuration: &dur,
	}}
	// Case 1: DB has no timeline → expect update, not create.
	existing := []model.Shot{mkShot(1, 7, "card-a", 0, "")}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Updated) != 1 {
		t.Fatalf("timeline add should update, got %+v", plan)
	}
	got := plan.Updated[0]
	if got.TimelineStartMs == nil || *got.TimelineStartMs != 100 {
		t.Fatalf("start not applied: %+v", got.TimelineStartMs)
	}
	if got.TimelineDurationMs == nil || *got.TimelineDurationMs != 2500 {
		t.Fatalf("duration not applied: %+v", got.TimelineDurationMs)
	}

	// Case 2: DB already has matching timeline → unchanged.
	existing2 := []model.Shot{mkShot(1, 7, "card-a", 0, "")}
	existing2[0].TimelineStartMs = &start
	existing2[0].TimelineDurationMs = &dur
	plan2 := DiffShots(42, 7, existing2, incoming)
	if plan2.Unchanged != 1 || len(plan2.Updated) != 0 {
		t.Fatalf("matching timeline should be unchanged, got %+v", plan2)
	}
}

func TestDiffShots_EntriesListedInIncomingOrder(t *testing.T) {
	// Entry order matters for callers that render the plan as
	// a list the user can scan. Incoming order wins; soft-deletes tail.
	incoming := []ShotLink{
		mkLink("card-b", 0),
		mkLink("card-a", 1),
	}
	existing := []model.Shot{
		mkShot(1, 7, "card-a", 0, ""),
		mkShot(2, 7, "card-c", 1, ""),
	}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Entries) < 3 {
		t.Fatalf("expected >=3 entries, got %d: %+v", len(plan.Entries), plan.Entries)
	}
	if plan.Entries[0].LocalCardID != "card-b" {
		t.Fatalf("first entry should follow incoming order, got %q", plan.Entries[0].LocalCardID)
	}
	if plan.Entries[1].LocalCardID != "card-a" {
		t.Fatalf("second entry should follow incoming order, got %q", plan.Entries[1].LocalCardID)
	}
	// Tail: the soft-delete for card-c.
	tail := plan.Entries[len(plan.Entries)-1]
	if tail.LocalCardID != "card-c" || tail.Action != ShotSyncActionSoftDel {
		t.Fatalf("expected tail to be card-c soft-delete, got %+v", tail)
	}
}

func anyTime() time.Time { return time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC) }

// TestShotSyncEntryMarshalShape pins the wire shape returned by
// POST /canvas/projects/:id/shots/sync — the handler emits
// `plan.Entries` directly as `entries[]`, and the FE consumer
// (useCanvasShotSync.shotSyncIdsByLocalCardId) reads
// `entry.localCardId` + `entry.shotId`. Without explicit json tags,
// Go marshals to PascalCase, which the FE only survives by carrying
// a defensive `entry.localCardId || entry.LocalCardID` fallback —
// removing the fallback was the wire-shape drift this test pins
// against.
func TestShotSyncEntryMarshalShape(t *testing.T) {
	entry := ShotSyncEntry{
		LocalCardID: "card-1",
		Action:      ShotSyncActionCreate,
		ShotID:      42,
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, ok := decoded["localCardId"].(string); !ok || v != "card-1" {
		t.Fatalf("expected localCardId=card-1, got %v (raw=%s)", decoded["localCardId"], raw)
	}
	if v, ok := decoded["action"].(string); !ok || v != string(ShotSyncActionCreate) {
		t.Fatalf("expected action=%q, got %v (raw=%s)", ShotSyncActionCreate, decoded["action"], raw)
	}
	if v, ok := decoded["shotId"].(float64); !ok || v != 42 {
		t.Fatalf("expected shotId=42, got %v (raw=%s)", decoded["shotId"], raw)
	}
	if _, present := decoded["LocalCardID"]; present {
		t.Fatalf("legacy PascalCase key LocalCardID must not be emitted (raw=%s)", raw)
	}
	if _, present := decoded["Action"]; present {
		t.Fatalf("legacy PascalCase key Action must not be emitted (raw=%s)", raw)
	}
	if _, present := decoded["ShotID"]; present {
		t.Fatalf("legacy PascalCase key ShotID must not be emitted (raw=%s)", raw)
	}
}
