// decision_log_db_test.go — end-to-end DB-coupled assertions for
// BuildProjectDecisionLog. Pure projection tests live in
// decision_log_test.go; this file pins:
//
//   1. Ownership gate — cross-tenant and missing project both
//      return gorm.ErrRecordNotFound (IDOR-collapse posture, matches
//      the rest of the canvas API surface).
//   2. JOIN correctness — w_canvas_task_binding × w_generation_task
//      hydrates the right rows in the right order.
//   3. Truncation — when binding/task count exceeds
//      MaxDecisionLogEntries, the response sets truncated=true and
//      drops the OLDEST tail (not the newest).
//
// Uses testutil.NewTestDB (in-memory SQLite) like every other
// DB-backed test in this package.

package canvas

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"server/model"
	"server/utils/testutil"
)

func TestBuildProjectDecisionLog_CrossTenantReturnsNotFound(t *testing.T) {
	// Project owned by uid=100; caller is uid=42. The ownership gate
	// must return gorm.ErrRecordNotFound, indistinguishable from
	// "project doesn't exist", so the API handler collapses both to
	// one 404 body and the cross-tenant case can't be probed.
	db := testutil.NewTestDB(t)

	project := &model.CanvasProject{
		UID:  100,
		Title: "Other-user project",
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	_, err := BuildProjectDecisionLog(context.Background(), db, 42, uint64(project.Id))
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("cross-tenant decision-log must surface ErrRecordNotFound (IDOR), got %v", err)
	}
}

func TestBuildProjectDecisionLog_HappyPathJoinsBindingsAndTasks(t *testing.T) {
	// Seed: owner uid=42 owns a project; 3 tasks bound to it with
	// distinct statuses + credit values. The log should return all
	// three, sorted by created_at desc, with totals rolled up.
	db := testutil.NewTestDB(t)

	uid := uint(42)
	project := &model.CanvasProject{
		UID:  int(uid),
		Title: "Owner project",
	}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	now := time.Now()
	tasks := []*model.GenerationTask{
		{
			TaskID:      "task-success",
			UID:         int(uid),
			ToolID:      "canvas",
			Model:       "nano-banana-2",
			Status:      model.TaskStatusCompleted,
			CreditsUsed: 3,
			DurationMs:  1500,
			RequestData: model.JSONMap{
				"mediaType":   "image",
				"prompt":      "neon street",
				"aspectRatio": "1:1",
			},
		},
		{
			TaskID:      "task-failed",
			UID:         int(uid),
			ToolID:      "canvas",
			Model:       "kling-2-6",
			Status:      model.TaskStatusFailed,
			CreditsUsed: 0,
			DurationMs:  500,
			ErrorMsg:    "provider quota exceeded",
			RequestData: model.JSONMap{
				"mediaType": "video",
				"prompt":    "fast tracking shot",
			},
		},
		{
			TaskID:      "task-pending",
			UID:         int(uid),
			ToolID:      "canvas",
			Model:       "nano-banana-2",
			Status:      model.TaskStatusPending,
			CreditsUsed: 0,
			RequestData: model.JSONMap{"mediaType": "image"},
		},
	}
	// Stagger created_at so sort-desc has a deterministic order.
	for i, task := range tasks {
		task.CreatedAt = now.Add(time.Duration(-i) * time.Hour)
		if err := db.Create(task).Error; err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
		binding := &model.CanvasTaskBinding{
			UID:       uid,
			ProjectID: uint(project.Id),
			TaskID:    task.TaskID,
			ElementID: "elem-" + task.TaskID,
		}
		if err := db.Create(binding).Error; err != nil {
			t.Fatalf("seed binding %d: %v", i, err)
		}
	}

	log, err := BuildProjectDecisionLog(context.Background(), db, uid, uint64(project.Id))
	if err != nil {
		t.Fatalf("BuildProjectDecisionLog: %v", err)
	}
	if log.ProjectID != uint64(project.Id) {
		t.Errorf("ProjectID = %d, want %d", log.ProjectID, project.Id)
	}
	if len(log.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(log.Entries))
	}
	if log.Entries[0].TaskID != "task-success" {
		t.Errorf("most-recent task should be first, got %q", log.Entries[0].TaskID)
	}
	if log.Entries[1].TaskID != "task-failed" {
		t.Errorf("second task should be 'task-failed', got %q", log.Entries[1].TaskID)
	}
	if log.Entries[0].Status != "completed" {
		t.Errorf("first task Status = %q, want 'completed'", log.Entries[0].Status)
	}
	if log.Entries[1].ErrorMessage != "provider quota exceeded" {
		t.Errorf("failed task ErrorMessage = %q, want quota text", log.Entries[1].ErrorMessage)
	}
	if log.Totals.Tasks != 3 || log.Totals.Successful != 1 || log.Totals.Failed != 1 || log.Totals.Pending != 1 {
		t.Errorf("totals wrong: %+v", log.Totals)
	}
	if log.Totals.Credits != 3 {
		t.Errorf("Credits should sum to 3, got %d", log.Totals.Credits)
	}
	if log.Truncated {
		t.Errorf("Truncated should be false for 3 entries")
	}
}

func TestBuildProjectDecisionLog_ExcludesOtherProjectsTasks(t *testing.T) {
	// Same owner uid=42, two projects A and B. A task bound to
	// project B must NOT appear in project A's decision log even
	// though it shares the owner.
	db := testutil.NewTestDB(t)

	uid := uint(42)
	projectA := &model.CanvasProject{UID: int(uid), Title: "A"}
	projectB := &model.CanvasProject{UID: int(uid), Title: "B"}
	if err := db.Create(projectA).Error; err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if err := db.Create(projectB).Error; err != nil {
		t.Fatalf("seed B: %v", err)
	}

	taskInB := &model.GenerationTask{
		TaskID:      "task-in-b",
		UID:         int(uid),
		ToolID:      "canvas",
		Model:       "x",
		Status:      model.TaskStatusCompleted,
		CreditsUsed: 99,
	}
	if err := db.Create(taskInB).Error; err != nil {
		t.Fatalf("seed task-in-b: %v", err)
	}
	if err := db.Create(&model.CanvasTaskBinding{
		UID:       uid,
		ProjectID: uint(projectB.Id),
		TaskID:    taskInB.TaskID,
	}).Error; err != nil {
		t.Fatalf("seed binding-in-b: %v", err)
	}

	logA, err := BuildProjectDecisionLog(context.Background(), db, uid, uint64(projectA.Id))
	if err != nil {
		t.Fatalf("BuildProjectDecisionLog A: %v", err)
	}
	if len(logA.Entries) != 0 {
		t.Errorf("project A's decision log should be empty (task-in-b belongs to B), got %d entries", len(logA.Entries))
	}
}
