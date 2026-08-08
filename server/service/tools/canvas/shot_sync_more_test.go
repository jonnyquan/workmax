package canvas

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"server/model"
	"server/utils/testutil"
)

// shot_sync_test.go pins the headline DiffShots paths (create / update /
// soft-delete / resurrect / dedupe / timeline ptr). These fill in the
// quieter branches that the SyncShots wrapper + per-entry Action field
// depend on:
//
//   - SyncShots returns a typed error on missing db / uid / projectID
//     rather than silently no-oping. Callers test `err != nil` to decide
//     whether anything changed; a returned zero-plan would be treated
//     as "success, nothing to do" and the real misconfiguration would
//     hide behind an empty response.
//   - DiffShots fills Entry.Action for every decision; a regression that
//     left Action as the zero string would break the response payload
//     renderer which prints `action=%s`.
//   - shotFromLink position-fallback: OrderIndex=0 must fall back to the
//     incoming slice position so the first-pass write is deterministic
//     even when the canvas document forgot to stamp orderIndex.
//   - A Title change on an existing row must produce an Update even
//     when OrderIndex and timeline match — otherwise renaming a shot
//     card in the canvas would silently fail to persist.
//   - existingByKey must drop whitespace-only LocalCardID; otherwise a
//     stray " " row in the DB would participate in intersection and
//     leak into updates against the incoming list.

func TestSyncShots_NilDBReturnsError(t *testing.T) {
	// The guard fires before any row is read. If a future refactor
	// dropped the nil-db check, GORM's WithContext(...).Find would
	// panic inside the handler and take the request down — callers
	// passing a nil db on purpose (tests, dry runs) would see the
	// whole server goroutine crash rather than a clean error.
	plan, err := SyncShots(context.Background(), nil, 42, 7, nil)
	if err == nil {
		t.Fatal("nil db should return a typed error")
	}
	if len(plan.Entries) != 0 || len(plan.Created) != 0 {
		t.Fatalf("plan should be empty on validation error, got %+v", plan)
	}
}

func TestSyncShots_ZeroUIDReturnsError(t *testing.T) {
	// A caller that forgets to propagate the authenticated uid from the
	// request context must NOT be allowed to sync anonymous rows — the
	// canvas project's owner column is the primary isolation boundary.
	// Pin the uid<=0 guard as an explicit error, not a silent noop.
	// The empty *gorm.DB is non-nil so we clear that guard; the uid
	// guard fires next, before any DB method is touched.
	db := &gorm.DB{}
	if _, err := SyncShots(context.Background(), db, 0, 7, nil); err == nil {
		t.Fatal("uid=0 should return a typed error")
	}
	if _, err := SyncShots(context.Background(), db, -1, 7, nil); err == nil {
		t.Fatal("negative uid should return a typed error")
	}
}

func TestSyncShots_ZeroProjectIDReturnsError(t *testing.T) {
	// Same isolation principle: projectID=0 would WHERE-match every row
	// with canvas_project_id IS NULL / 0 and potentially soft-delete
	// orphans from the draft-project path. Guard with a typed error.
	db := &gorm.DB{}
	if _, err := SyncShots(context.Background(), db, 42, 0, nil); err == nil {
		t.Fatal("projectID=0 should return a typed error")
	}
}

func TestSyncShots_EditorWritesOwnerScopedRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := model.CanvasProject{
		UID:      42,
		UUID:     "shot-sync-project",
		Title:    "Shared",
		Document: model.JSONMap{},
	}
	project.Id = 7
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: project.Id,
		UID:       99,
		Role:      model.GlobalProjectRoleEditor,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed editor: %v", err)
	}

	plan, err := SyncShots(context.Background(), db, 99, project.Id, []ShotLink{mkLink("card-a", 0)})
	if err != nil {
		t.Fatalf("editor SyncShots: %v", err)
	}
	if len(plan.Created) != 1 {
		t.Fatalf("created = %d, want 1", len(plan.Created))
	}
	if plan.Created[0].UID != 42 {
		t.Fatalf("created shot uid = %d, want project owner uid 42", plan.Created[0].UID)
	}

	var persisted model.Shot
	if err := db.First(&persisted, plan.Created[0].Id).Error; err != nil {
		t.Fatalf("load shot: %v", err)
	}
	if persisted.UID != 42 || persisted.CanvasProjectID != project.Id {
		t.Fatalf("persisted shot = uid %d project %d, want owner/project", persisted.UID, persisted.CanvasProjectID)
	}
}

func TestDiffShots_EntryActionIsPopulatedPerDecision(t *testing.T) {
	// API consumers render `action=%s` for every entry. A regression
	// that left Action="" on some code path would render "action=" in
	// the payload and downstream filters (e.g. "show me all creates")
	// would miss those rows. Pin the per-branch Action value.
	existing := []model.Shot{
		mkShot(11, 7, "card-a", 0, ""),
		mkShot(12, 7, "card-b", 1, ""),
	}
	incoming := []ShotLink{
		mkLink("card-a", 0), // unchanged
		mkLink("card-b", 5), // update (order changed)
		mkLink("card-c", 2), // create
	}
	plan := DiffShots(42, 7, existing, incoming)

	byKey := make(map[string]ShotSyncAction, len(plan.Entries))
	for _, e := range plan.Entries {
		byKey[e.LocalCardID] = e.Action
	}
	if byKey["card-a"] != ShotSyncActionUnchanged {
		t.Errorf("card-a Action = %q, want %q", byKey["card-a"], ShotSyncActionUnchanged)
	}
	if byKey["card-b"] != ShotSyncActionUpdate {
		t.Errorf("card-b Action = %q, want %q", byKey["card-b"], ShotSyncActionUpdate)
	}
	if byKey["card-c"] != ShotSyncActionCreate {
		t.Errorf("card-c Action = %q, want %q", byKey["card-c"], ShotSyncActionCreate)
	}
}

func TestDiffShots_OrderIndexFallsBackToPosition(t *testing.T) {
	// When the canvas document hasn't stamped orderIndex yet (fresh
	// doc, AI-generated draft), shotFromLink must use the slice
	// position so the first pass writes rows in incoming order. A
	// regression that persisted OrderIndex=0 for every card would
	// collapse all shots to the same row in the storyboard view.
	incoming := []ShotLink{
		{LocalCardID: "card-a"}, // OrderIndex zero → position 0
		{LocalCardID: "card-b"}, // OrderIndex zero → position 1
	}
	plan := DiffShots(42, 7, nil, incoming)
	if len(plan.Created) != 2 {
		t.Fatalf("expected 2 creates, got %d", len(plan.Created))
	}
	if plan.Created[1].OrderIndex == 0 {
		t.Errorf("second card should fall back to position 1, got 0")
	}
}

func TestDiffShots_TitleChangeForcesUpdate(t *testing.T) {
	// shotsEquivalent compares Title too. Pin the branch so that a
	// regression which dropped Title from the comparison (and relied
	// only on OrderIndex / LocalCardID) would leave the old title in
	// the DB even after a rename in the canvas.
	existing := []model.Shot{mkShot(11, 7, "card-a", 0, "OldTitle")}
	// ShotLink has no Title field, so shotFromLink seeds Title="" —
	// that's different from "OldTitle" and must drive an Update.
	incoming := []ShotLink{mkLink("card-a", 0)}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Updated) != 1 {
		t.Fatalf("title mismatch should produce update, got %+v", plan)
	}
	if plan.Updated[0].Title != "" {
		t.Errorf("updated Title = %q, expected empty (link carries no Title)", plan.Updated[0].Title)
	}
}

func TestDiffShots_WhitespaceLocalCardIDInExistingIsDropped(t *testing.T) {
	// A whitespace-only LocalCardID in the DB (garbage from a botched
	// import) must NOT participate in intersection — otherwise the
	// garbage row could mask a legitimate create or leak into updates.
	// Pin both halves: the garbage row does NOT prevent the create,
	// AND it does NOT show up as an Updated target.
	existing := []model.Shot{
		mkShot(99, 7, "   ", 0, ""), // whitespace LocalCardID
	}
	incoming := []ShotLink{mkLink("card-a", 0)}
	plan := DiffShots(42, 7, existing, incoming)
	if len(plan.Created) != 1 || plan.Created[0].LocalCardID != "card-a" {
		t.Fatalf("whitespace existing row should not block the create, got %+v", plan.Created)
	}
	for _, u := range plan.Updated {
		if u.Id == 99 {
			t.Errorf("whitespace row 99 must not be chosen as an Update target")
		}
	}
}

func TestInt64PtrEqual_AllBranches(t *testing.T) {
	// int64PtrEqual is the timeline-field comparator that shotsEquivalent
	// uses to decide whether an incoming ShotLink's TimelineStart/Duration
	// drift from the DB row justifies an Update. The existing DiffShots
	// tests only exercise the "same value, different pointers" path
	// (via timeline ptr helpers); pin all four branches directly so a
	// refactor that flipped, say, the nil|nil check to return false would
	// suddenly mark every shot as dirty on every sync and fail here.
	v1 := int64(1000)
	v1copy := int64(1000) // same value, different address
	v2 := int64(2000)

	cases := []struct {
		name string
		a, b *int64
		want bool
	}{
		{"both nil", nil, nil, true},
		{"a nil, b set", nil, &v1, false},
		{"a set, b nil", &v1, nil, false},
		{"equal by value, distinct pointers", &v1, &v1copy, true},
		{"unequal", &v1, &v2, false},
		{"same pointer", &v1, &v1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := int64PtrEqual(tc.a, tc.b); got != tc.want {
				t.Errorf("int64PtrEqual = %v, want %v", got, tc.want)
			}
		})
	}
}
