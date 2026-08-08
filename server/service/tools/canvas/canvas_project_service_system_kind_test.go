// Stage 1.5 system_kind delete-guard regression tests. The
// DeleteProject default refuses any project flagged system_kind > 0;
// only DeleteProjectWithOptions with AllowSystemKind=true (the
// account-close cascade path) gets through. These tests pin both
// directions so a future caller can't accidentally regress either.

package canvas

import (
	"context"
	"errors"
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestDeleteProject_RefusesSystemKindProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:      "Personal Workspace",
		SystemKind: model.GlobalProjectKindPersonalWorkspace,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	err = DeleteProject(context.Background(), db, 42, created.Id)
	if !errors.Is(err, ErrSystemKindProjectUndeletable) {
		t.Fatalf("DeleteProject: err = %v, want ErrSystemKindProjectUndeletable", err)
	}

	// Row must still be live — the refusal rolled back without
	// applying the soft-delete.
	var alive model.CanvasProject
	if err := db.Where("id = ? AND deleted_at IS NULL", created.Id).First(&alive).Error; err != nil {
		t.Fatalf("system-managed project should still be live: %v", err)
	}
	if alive.SystemKind != model.GlobalProjectKindPersonalWorkspace {
		t.Errorf("SystemKind = %d, want %d", alive.SystemKind, model.GlobalProjectKindPersonalWorkspace)
	}
}

func TestDeleteProject_AllowsUserKindProject(t *testing.T) {
	// Sanity case: the guard MUST NOT regress the default delete
	// path. A user-created project (system_kind=0) still deletes
	// without any options.
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "User Project"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject on user project: %v", err)
	}

	var live int64
	if err := db.Model(&model.CanvasProject{}).
		Where("id = ? AND deleted_at IS NULL", created.Id).Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Errorf("user project should be soft-deleted, live count = %d", live)
	}
}

func TestDeleteProjectWithOptions_AllowSystemKindDeletesIt(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:      "Personal Workspace",
		SystemKind: model.GlobalProjectKindPersonalWorkspace,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if err := DeleteProjectWithOptions(context.Background(), db, 42, created.Id, DeleteProjectOptions{AllowSystemKind: true}); err != nil {
		t.Fatalf("DeleteProjectWithOptions(AllowSystemKind=true): %v", err)
	}

	var live int64
	if err := db.Model(&model.CanvasProject{}).
		Where("id = ? AND deleted_at IS NULL", created.Id).Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Errorf("system project with AllowSystemKind should be soft-deleted, live count = %d", live)
	}
}

func TestDeleteProject_GuardChecksOwnershipFirst(t *testing.T) {
	// Defence in depth: a non-owner attacker probing whether a given
	// id is system-managed must hit the access-deny path FIRST and
	// never learn the system_kind bit. The guard is positioned after
	// requireProjectManageAccess so the order is observable.
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:      "Personal Workspace",
		SystemKind: model.GlobalProjectKindPersonalWorkspace,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// uid=99 has no access to project owned by uid=42.
	err = DeleteProject(context.Background(), db, 99, created.Id)
	if err == nil {
		t.Fatalf("non-owner delete should error, got nil")
	}
	// Must be ProjectNotFound (the access-deny sentinel), NOT
	// ErrSystemKindProjectUndeletable — otherwise the attacker
	// learns "this is a system project" via the error type.
	if errors.Is(err, ErrSystemKindProjectUndeletable) {
		t.Errorf("non-owner saw system-kind sentinel — info leak (err = %v)", err)
	}
}
