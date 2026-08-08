// continuity_index_db_test.go — end-to-end DB-coupled assertions
// for BuildContinuityIndex (Task #13). Pure projection tests live
// in continuity_index_test.go; this file pins:
//
//   1. SQL JOIN of w_canvas_task_binding × w_generation_task
//      returns ordered rows
//   2. Cross-tenant / cross-project isolation — a task from
//      project B does NOT leak into project A's continuity even
//      under the same owner
//   3. status filter — failed / cancelled / pending tasks don't
//      contribute continuity URLs even if they technically reference
//      the character

package canvas

import (
	"context"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"
)

func TestBuildContinuityIndex_NoOpForGuards(t *testing.T) {
	// Every guard branch (nil db, uid=0, projectID=0, empty
	// characterIDs) must short-circuit BEFORE any DB call. Passing
	// nil for db proves the guard held — a leak would panic on
	// WithContext.
	cases := []struct {
		name         string
		uid          uint
		projectID    uint64
		characterIDs []int
	}{
		{"uid zero", 0, 1, []int{1}},
		{"project zero", 42, 0, []int{1}},
		{"empty chars", 42, 1, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, err := BuildContinuityIndex(context.Background(), nil, c.uid, c.projectID, c.characterIDs)
			if err != nil {
				t.Errorf("guard branch %s should return nil error, got %v", c.name, err)
			}
			if len(idx) != 0 {
				t.Errorf("guard branch %s should return empty index, got %d entries", c.name, len(idx))
			}
		})
	}
}

func TestBuildContinuityIndex_HappyPath(t *testing.T) {
	// Seed two tasks in project P, both referencing character 7.
	// Newer task wins; the older task's URL is ignored.
	db := testutil.NewTestDB(t)
	uid := uint(42)
	projectID := uint64(101)

	// Earlier completion
	taskOlder := &model.GenerationTask{
		TaskID:      "task-older",
		UID:         int(uid),
		ToolID:      "canvas",
		Model:       "x",
		Status:      model.TaskStatusCompleted,
		CompletedAt: ptrTime(time.Now().Add(-2 * time.Hour)),
		RequestData: model.JSONMap{
			"assetBindings": map[string]interface{}{
				"characterIds": []interface{}{float64(7)},
			},
		},
		ResultData: model.JSONMap{
			"imageUrls": []interface{}{"https://older.png"},
		},
	}
	taskNewer := &model.GenerationTask{
		TaskID:      "task-newer",
		UID:         int(uid),
		ToolID:      "canvas",
		Model:       "x",
		Status:      model.TaskStatusCompleted,
		CompletedAt: ptrTime(time.Now()),
		RequestData: model.JSONMap{
			"assetBindings": map[string]interface{}{
				"characterIds": []interface{}{float64(7)},
			},
		},
		ResultData: model.JSONMap{
			"imageUrls": []interface{}{"https://newer.png"},
		},
	}
	for _, task := range []*model.GenerationTask{taskOlder, taskNewer} {
		if err := db.Create(task).Error; err != nil {
			t.Fatalf("seed task %s: %v", task.TaskID, err)
		}
		if err := db.Create(&model.CanvasTaskBinding{
			UID:       uid,
			ProjectID: uint(projectID),
			TaskID:    task.TaskID,
		}).Error; err != nil {
			t.Fatalf("seed binding %s: %v", task.TaskID, err)
		}
	}

	idx, err := BuildContinuityIndex(context.Background(), db, uid, projectID, []int{7})
	if err != nil {
		t.Fatalf("BuildContinuityIndex: %v", err)
	}
	if got := idx[7]; got != "https://newer.png" {
		t.Errorf("idx[7] = %q, want newer URL (DESC ordering)", got)
	}
}

func TestBuildContinuityIndex_FailedTasksDoNotContribute(t *testing.T) {
	// A failed task referencing the character is in the table but
	// must NOT surface as a continuity ref. Same goes for pending
	// and cancelled — only Completed contributes.
	db := testutil.NewTestDB(t)
	uid := uint(42)
	projectID := uint64(101)

	failed := &model.GenerationTask{
		TaskID:      "task-failed",
		UID:         int(uid),
		ToolID:      "canvas",
		Model:       "x",
		Status:      model.TaskStatusFailed,
		CompletedAt: ptrTime(time.Now()),
		RequestData: model.JSONMap{
			"assetBindings": map[string]interface{}{
				"characterIds": []interface{}{float64(7)},
			},
		},
		ResultData: model.JSONMap{"imageUrls": []interface{}{"https://leaked.png"}},
	}
	if err := db.Create(failed).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	if err := db.Create(&model.CanvasTaskBinding{
		UID:       uid,
		ProjectID: uint(projectID),
		TaskID:    failed.TaskID,
	}).Error; err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	idx, err := BuildContinuityIndex(context.Background(), db, uid, projectID, []int{7})
	if err != nil {
		t.Fatalf("BuildContinuityIndex: %v", err)
	}
	if _, has := idx[7]; has {
		t.Errorf("failed task must NOT contribute to continuity, but idx[7] = %q", idx[7])
	}
}

func TestBuildContinuityIndex_CrossProjectIsolation(t *testing.T) {
	// Same owner uid, two projects A and B. A task bound to B that
	// references character 5 must NOT appear in project A's
	// continuity even though uid matches.
	db := testutil.NewTestDB(t)
	uid := uint(42)
	projectA := uint64(101)
	projectB := uint64(202)

	taskInB := &model.GenerationTask{
		TaskID:      "task-b",
		UID:         int(uid),
		ToolID:      "canvas",
		Model:       "x",
		Status:      model.TaskStatusCompleted,
		CompletedAt: ptrTime(time.Now()),
		RequestData: model.JSONMap{
			"assetBindings": map[string]interface{}{
				"characterIds": []interface{}{float64(5)},
			},
		},
		ResultData: model.JSONMap{"imageUrls": []interface{}{"https://b.png"}},
	}
	if err := db.Create(taskInB).Error; err != nil {
		t.Fatalf("seed task-b: %v", err)
	}
	if err := db.Create(&model.CanvasTaskBinding{
		UID:       uid,
		ProjectID: uint(projectB),
		TaskID:    taskInB.TaskID,
	}).Error; err != nil {
		t.Fatalf("seed binding-b: %v", err)
	}

	idx, err := BuildContinuityIndex(context.Background(), db, uid, projectA, []int{5})
	if err != nil {
		t.Fatalf("BuildContinuityIndex for project A: %v", err)
	}
	if _, has := idx[5]; has {
		t.Errorf("project A continuity must not contain B's render: %q", idx[5])
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
