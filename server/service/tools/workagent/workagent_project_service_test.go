// DB-coupled regression tests for the workagent project surface
// (Stage 1 of §1 Project/Workspace 模型升级).

package workagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedProjectTestThread writes a workagent thread row owned by uid with the
// given project_id. Keeps the test bodies readable.
func seedProjectTestThread(t *testing.T, db *gorm.DB, uid int, projectID uint, name string) workagentModel.ChatThread {
	t.Helper()
	now := time.Now()
	thread := workagentModel.ChatThread{
		UID:          uid,
		UUID:         "thread-uuid-" + name,
		ProjectID:    projectID,
		Name:         name,
		AgentType:    "general_agent",
		AgentMode:    "ppt",
		IsPublic:     false,
		MessageCount: 0,
		FileCount:    0,
	}
	thread.CreatedAt = now
	thread.UpdatedAt = now
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread %q: %v", name, err)
	}
	return thread
}

func TestEnsurePersonalWorkspace_FirstCallCreates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace: %v", err)
	}
	if ws.UID != 42 {
		t.Errorf("UID = %d, want 42", ws.UID)
	}
	if ws.Title != PersonalWorkspaceTitle {
		t.Errorf("Title = %q, want %q", ws.Title, PersonalWorkspaceTitle)
	}
	if ws.Id == 0 {
		t.Errorf("Id is zero — workspace not persisted")
	}
	// Owner member must exist (canvasService.CreateProject upserts it).
	canEdit, err := projectService.NewRepository(db).CanEditProject(ws.Id, 42)
	if err != nil {
		t.Fatalf("CanEditProject: %v", err)
	}
	if !canEdit {
		t.Errorf("owner can't edit own personal workspace")
	}
}

func TestEnsurePersonalWorkspace_IdempotentSecondCall(t *testing.T) {
	db := testutil.NewTestDB(t)
	first, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	second, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first.Id != second.Id {
		t.Fatalf("re-creation: first.Id=%d second.Id=%d (idempotency violated)", first.Id, second.Id)
	}

	// And only one row exists for this user.
	var count int64
	if err := db.Model(&model.CanvasProject{}).
		Where("uid = ? AND title = ? AND deleted_at IS NULL", 42, PersonalWorkspaceTitle).
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("personal workspace count = %d, want 1", count)
	}
}

func TestEnsurePersonalWorkspace_RejectsZeroAndNegativeUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, uid := range []int{0, -1, -100} {
		if _, err := EnsurePersonalWorkspace(context.Background(), db, uid); err == nil {
			t.Errorf("uid=%d should error, got nil", uid)
		}
	}
}

func TestEnsurePersonalWorkspace_ConcurrentCallsSerializeViaPerUIDLock(t *testing.T) {
	// In-process serialization protects against the two-tabs race.
	// Without the lock, 5 parallel first-calls can land 5 separate
	// "Personal Workspace" rows.
	db := testutil.NewTestDB(t)
	var wg sync.WaitGroup
	results := make([]uint, 5)
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
			results[idx] = ws.Id
			errs[idx] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
	// All goroutines must observe the same workspace id.
	for i := 1; i < 5; i++ {
		if results[i] != results[0] {
			t.Errorf("workspace ids diverged: %v", results)
			break
		}
	}
	var count int64
	if err := db.Model(&model.CanvasProject{}).
		Where("uid = ? AND title = ? AND deleted_at IS NULL", 42, PersonalWorkspaceTitle).
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("after 5 concurrent calls, workspace count = %d, want 1", count)
	}
}

func TestListWorkagentProjectsForUser_PersonalWorkspaceAutoCreatedAndPinnedFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	items, err := ListWorkagentProjectsForUser(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("ListWorkagentProjectsForUser: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1 (auto-created personal workspace)", len(items))
	}
	if !items[0].IsPersonal {
		t.Errorf("items[0].IsPersonal = false, want true")
	}
	if items[0].Title != PersonalWorkspaceTitle {
		t.Errorf("items[0].Title = %q, want %q", items[0].Title, PersonalWorkspaceTitle)
	}
}

func TestListWorkagentProjectsForUser_LazyBackfill(t *testing.T) {
	db := testutil.NewTestDB(t)
	cache := GetThreadCache()
	cache.Clear()
	// Seed 3 threads with project_id=0 (grandfathered state).
	t1 := seedProjectTestThread(t, db, 42, 0, "t1")
	t2 := seedProjectTestThread(t, db, 42, 0, "t2")
	t3 := seedProjectTestThread(t, db, 42, 0, "t3")
	for _, thread := range []workagentModel.ChatThread{t1, t2, t3} {
		cache.Set(fmt.Sprint(thread.Id), &workagentModel.ChatThread{ProjectID: 0})
	}

	items, err := ListWorkagentProjectsForUser(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("ListWorkagentProjectsForUser: %v", err)
	}
	if len(items) != 1 || !items[0].IsPersonal {
		t.Fatalf("expected single personal workspace, got %+v", items)
	}
	wsID := items[0].Id

	// All 3 threads must now point at the workspace.
	for _, original := range []workagentModel.ChatThread{t1, t2, t3} {
		var got workagentModel.ChatThread
		if err := db.Where("id = ?", original.Id).First(&got).Error; err != nil {
			t.Fatalf("read thread %d back: %v", original.Id, err)
		}
		if got.ProjectID != wsID {
			t.Errorf("thread %d project_id = %d, want %d (backfill failed)", original.Id, got.ProjectID, wsID)
		}
		if _, ok := cache.Get(fmt.Sprint(original.Id)); ok {
			t.Errorf("ThreadCache must be invalidated after lazy project backfill for thread %d", original.Id)
		}
	}
}

func TestListWorkagentProjectsForUser_DoesNotCrossUsers(t *testing.T) {
	db := testutil.NewTestDB(t)
	// User 42 has 2 projects (Personal + custom).
	if _, err := EnsurePersonalWorkspace(context.Background(), db, 42); err != nil {
		t.Fatalf("user42 personal: %v", err)
	}
	if err := db.Create(&model.CanvasProject{
		UID:   42,
		UUID:  "u42-custom",
		Title: "Custom",
	}).Error; err != nil {
		t.Fatalf("user42 custom: %v", err)
	}
	// User 99 makes their first call — should ONLY see their own workspace.
	items, err := ListWorkagentProjectsForUser(context.Background(), db, 99)
	if err != nil {
		t.Fatalf("ListWorkagentProjectsForUser uid=99: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("uid=99 saw %d projects, want 1 (own personal workspace only)", len(items))
	}
	if items[0].IsPersonal == false {
		t.Errorf("uid=99 first project should be their personal workspace")
	}
}

func TestAssignThreadToProject_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	cache := GetThreadCache()
	cache.Clear()
	ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	thread := seedProjectTestThread(t, db, 42, 0, "to-assign")
	cache.Set(fmt.Sprint(thread.Id), &workagentModel.ChatThread{ProjectID: 0})

	if err := AssignThreadToProject(context.Background(), db, 42, thread.Id, ws.Id); err != nil {
		t.Fatalf("AssignThreadToProject: %v", err)
	}
	var got workagentModel.ChatThread
	if err := db.Where("id = ?", thread.Id).First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.ProjectID != ws.Id {
		t.Errorf("project_id = %d, want %d", got.ProjectID, ws.Id)
	}
	if _, ok := cache.Get(fmt.Sprint(thread.Id)); ok {
		t.Errorf("ThreadCache must be invalidated after AssignThreadToProject")
	}
}

func TestAssignThreadToProject_ThreadOwnedByDifferentUser(t *testing.T) {
	db := testutil.NewTestDB(t)
	ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("ws: %v", err)
	}
	// Thread is owned by user 99, not 42.
	thread := seedProjectTestThread(t, db, 99, 0, "other-users-thread")

	err = AssignThreadToProject(context.Background(), db, 42, thread.Id, ws.Id)
	if err == nil {
		t.Fatalf("cross-user assign should error, got nil")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err = %v, want ErrRecordNotFound (generic 404 posture)", err)
	}
}

func TestAssignThreadToProject_ProjectNotAccessible(t *testing.T) {
	db := testutil.NewTestDB(t)
	// User 42's own thread.
	thread := seedProjectTestThread(t, db, 42, 0, "u42")
	// Project belongs to user 99; user 42 has no membership row.
	otherProject := model.CanvasProject{UID: 99, UUID: "u99-project", Title: "Other"}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatalf("seed other-user project: %v", err)
	}

	err := AssignThreadToProject(context.Background(), db, 42, thread.Id, otherProject.Id)
	if err == nil {
		t.Fatalf("assign to inaccessible project should error, got nil")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("err = %v, want ErrRecordNotFound", err)
	}
}

func TestAssignThreadToProject_RejectsZeroInputs(t *testing.T) {
	db := testutil.NewTestDB(t)
	for _, c := range []struct {
		uid     int
		thread  uint
		project uint
	}{
		{0, 1, 1},
		{1, 0, 1},
		{1, 1, 0},
	} {
		if err := AssignThreadToProject(context.Background(), db, c.uid, c.thread, c.project); err == nil {
			t.Errorf("uid=%d thread=%d project=%d should error, got nil", c.uid, c.thread, c.project)
		}
	}
}

func TestDeleteWorkagentProject_DeletesGeneralThreadsAndInvalidatesCache(t *testing.T) {
	db := testutil.NewTestDB(t)
	cache := GetThreadCache()
	cache.Clear()

	project, err := canvasService.CreateProject(context.Background(), db, 42, canvasService.CreateProjectInput{
		Title: "Campaign",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	thread := seedProjectTestThread(t, db, 42, project.Id, "campaign-thread")
	cache.Set(fmt.Sprint(thread.Id), &workagentModel.ChatThread{ProjectID: project.Id})

	if err := DeleteWorkagentProject(context.Background(), db, 42, project.Id); err != nil {
		t.Fatalf("DeleteWorkagentProject: %v", err)
	}

	var projectCount int64
	if err := db.Model(&model.CanvasProject{}).
		Where("id = ? AND deleted_at IS NULL", project.Id).
		Count(&projectCount).Error; err != nil {
		t.Fatalf("count project: %v", err)
	}
	if projectCount != 0 {
		t.Errorf("project still active after delete")
	}

	var threadCount int64
	if err := db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", thread.Id).
		Count(&threadCount).Error; err != nil {
		t.Fatalf("count thread: %v", err)
	}
	if threadCount != 0 {
		t.Errorf("general_agent thread still active after project delete")
	}
	if _, ok := cache.Get(fmt.Sprint(thread.Id)); ok {
		t.Errorf("ThreadCache must be invalidated after project thread delete")
	}
}

func TestDeleteWorkagentProject_RejectsPersonalWorkspace(t *testing.T) {
	db := testutil.NewTestDB(t)
	ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("personal workspace: %v", err)
	}

	err = DeleteWorkagentProject(context.Background(), db, 42, ws.Id)
	if !errors.Is(err, ErrPersonalWorkspaceUndeletable) {
		t.Fatalf("err = %v, want ErrPersonalWorkspaceUndeletable", err)
	}
}

// TestIsPersonalWorkspace pins the Stage 1.5 contract: detection
// keys on the SystemKind column, NOT on title. A user-created
// project with a colliding title stays user-managed; a row tagged
// system_kind=PersonalWorkspace is detected even with an empty
// title (defensive: a future migration could leave the title
// blank).
func TestIsPersonalWorkspace(t *testing.T) {
	tests := []struct {
		name    string
		project model.CanvasProject
		want    bool
	}{
		{"system_kind=PW + title=PW", model.CanvasProject{SystemKind: model.GlobalProjectKindPersonalWorkspace, Title: PersonalWorkspaceTitle}, true},
		{"system_kind=PW + title empty", model.CanvasProject{SystemKind: model.GlobalProjectKindPersonalWorkspace}, true},
		{"system_kind=0 + title=PW (collision)", model.CanvasProject{Title: PersonalWorkspaceTitle}, false},
		{"system_kind=0 + custom title", model.CanvasProject{Title: "Custom"}, false},
		{"zero value", model.CanvasProject{}, false},
	}
	for _, tc := range tests {
		got := IsPersonalWorkspace(tc.project)
		if got != tc.want {
			t.Errorf("%s: IsPersonalWorkspace = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestEnsurePersonalWorkspace_TitleCollisionDoesNotShadowSystemKind
// pins the Stage 1.5 risk-1 fix: a pre-existing user-created project
// with title='Personal Workspace' MUST NOT be returned as the user's
// system workspace. EnsurePersonalWorkspace looks up by system_kind
// (not title), so the colliding user row stays user-managed and a
// fresh system row is created.
func TestEnsurePersonalWorkspace_TitleCollisionDoesNotShadowSystemKind(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Seed a user-created project that happens to be named "Personal
	// Workspace" — the pre-Stage-1.5 sentinel collision scenario.
	colliding := model.CanvasProject{
		UID:   42,
		UUID:  "u42-collision",
		Title: PersonalWorkspaceTitle,
		// SystemKind left at zero (the default) — this is a regular
		// user project that just happens to be named like the
		// system default.
	}
	if err := db.Create(&colliding).Error; err != nil {
		t.Fatalf("seed colliding: %v", err)
	}

	ws, err := EnsurePersonalWorkspace(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("EnsurePersonalWorkspace: %v", err)
	}

	// The returned workspace must be NEW (different id from the
	// colliding seed) and must carry the system_kind marker.
	if ws.Id == colliding.Id {
		t.Errorf("EnsurePersonalWorkspace returned the colliding user row (id=%d) instead of creating a fresh system row", ws.Id)
	}
	if ws.SystemKind != model.GlobalProjectKindPersonalWorkspace {
		t.Errorf("returned workspace.SystemKind = %d, want %d", ws.SystemKind, model.GlobalProjectKindPersonalWorkspace)
	}

	// And the colliding user row stays user-managed (SystemKind=0).
	var post model.CanvasProject
	if err := db.Where("id = ?", colliding.Id).First(&post).Error; err != nil {
		t.Fatalf("read colliding row back: %v", err)
	}
	if post.SystemKind != model.GlobalProjectKindUser {
		t.Errorf("colliding user row SystemKind = %d, want %d (must not be mutated)", post.SystemKind, model.GlobalProjectKindUser)
	}
}

// Sanity: package-level globals.GraDBs is unused in this file, but
// other tests rely on it; this ensures the import doesn't get linted.
var _ = globals.GraDBs
