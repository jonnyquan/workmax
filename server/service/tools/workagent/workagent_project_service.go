// Project / Workspace 模型升级 Stage 1 — workagent project surface.
//
// The platform already has the data layer in place (Sprint-E
// 2026-05-11 moved everything to w_global_*):
//   - w_global_project — the project container (uid + title +
//     visibility + budget). Same table canvas writes to.
//   - w_global_project_member — collaboration role binding.
//   - w_workagent_thread.project_id — column exists (migration
//     20260605); was previously only populated by canvas-spawned
//     threads.
//
// What this file adds is the workagent-side product surface that lets
// users SEE and CHOOSE their projects, plus the "Personal Workspace"
// pattern (every user has a default project they can't delete) and
// the lazy backfill that grandfathers existing project_id=0 threads.
//
// Three primitives:
//
//   EnsurePersonalWorkspace(ctx, db, uid) — idempotent lookup-or-
//     create of the user's default project. First call creates;
//     subsequent calls return the existing row.
//
//   ListWorkagentProjectsForUser(ctx, db, uid) — workagent surface's
//     project list. Calls EnsurePersonalWorkspace, runs the lazy
//     backfill, returns the user's projects ordered (Personal first,
//     then updated_at DESC).
//
//   AssignThreadToProject(ctx, db, uid, threadID, projectID) —
//     binds an existing thread to a project; checks ownership of
//     both ends.
//
// Current scope limitations (intentional):
//
//   - Personal Workspace is detected by system_kind. The title literal
//     is still reserved at the HTTP surface to prevent confusable user
//     projects.
//
//   - Single-instance per-uid mutex prevents the common first-call
//     race. A DB-level multi-instance uniqueness strategy waits for
//     an actual multi-pod deployment shape.

package workagent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"server/model"
	workagentModel "server/model/workagent"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"

	"gorm.io/gorm"
)

// PersonalWorkspaceTitle is the display title stored on the row.
// The FE renders a localized label keyed off IsPersonal=true rather
// than reading this title literally — so the English literal here
// is just a fallback for clients that don't know about the
// system_kind marker (e.g. canvas's project list pre-Stage-1.5).
//
// Stage 1.5: this is NO LONGER the detection sentinel. Detection
// uses model.GlobalProjectKindPersonalWorkspace via the new
// system_kind column. The title is still used at the HTTP POST
// surface (projects_api.go) to refuse a user creating a confusable
// "Personal Workspace"-named project of their own.
const PersonalWorkspaceTitle = "Personal Workspace"

// ErrPersonalWorkspaceUndeletable surfaces when a caller tries to
// delete the per-user default workspace. Kept as an exported sentinel
// so handlers can map to a stable error code.
var ErrPersonalWorkspaceUndeletable = errors.New("workagent: personal workspace cannot be deleted")

// ProjectListItem is the workagent surface's compact project DTO.
// Mirrors only the fields the Rail / ProjectSwitcher needs; the full
// CanvasProject shape (snapshot metadata, document body, etc.) is
// owner-internal and not exposed here.
type ProjectListItem struct {
	Id         uint      `json:"id"`
	Title      string    `json:"title"`
	IsPersonal bool      `json:"isPersonal"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// personalWorkspaceLocks serializes the lookup-or-create for
// EnsurePersonalWorkspace per-uid. Single-process scope is enough for
// the current deployment shape; DB-level multi-instance uniqueness
// remains a separate platform hardening item.
var personalWorkspaceLocks sync.Map // map[int]*sync.Mutex

func acquirePersonalWorkspaceLock(uid int) *sync.Mutex {
	if existing, ok := personalWorkspaceLocks.Load(uid); ok {
		return existing.(*sync.Mutex)
	}
	fresh := &sync.Mutex{}
	actual, _ := personalWorkspaceLocks.LoadOrStore(uid, fresh)
	return actual.(*sync.Mutex)
}

// EnsurePersonalWorkspace returns the user's Personal Workspace,
// creating it on first call. Idempotent within a process via the
// per-uid mutex; cross-process races settle by First() returning the
// oldest row.
//
// Reuses canvasService.CreateProject so the project row gets the same
// shape canvas projects do (initial snapshot + owner member upsert).
// The init snapshot is unused by the workagent surface but harmless;
// canvas's own listing path filters by visibility/source as needed.
func EnsurePersonalWorkspace(ctx context.Context, db *gorm.DB, uid int) (model.CanvasProject, error) {
	if uid <= 0 {
		return model.CanvasProject{}, errors.New("EnsurePersonalWorkspace: uid must be > 0")
	}

	mu := acquirePersonalWorkspaceLock(uid)
	mu.Lock()
	defer mu.Unlock()

	// Stage 1.5: lookup uses the unambiguous system_kind column.
	// Falls through to creation only when no system-managed row
	// exists for this uid — a user-named project that happens to
	// have title='Personal Workspace' will NOT short-circuit this
	// path (system_kind stays 0 for user-created rows even when
	// the title collides).
	var existing model.CanvasProject
	err := db.WithContext(ctx).
		Where("uid = ? AND system_kind = ? AND deleted_at IS NULL", uid, model.GlobalProjectKindPersonalWorkspace).
		Order("id ASC").
		First(&existing).Error
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasProject{}, err
	}

	// Create with SystemKind=1 so the system marker rides through
	// CreateProject's transaction (the canvas service persists it
	// alongside title / uid / etc. in the same INSERT).
	return canvasService.CreateProject(ctx, db, uid, canvasService.CreateProjectInput{
		Title:      PersonalWorkspaceTitle,
		SystemKind: model.GlobalProjectKindPersonalWorkspace,
	})
}

// ListWorkagentProjectsForUser is the workagent surface's project
// list endpoint backing. Returns the user's projects with Personal
// Workspace pinned first.
//
// Side effects (intentional, idempotent):
//   - Creates Personal Workspace on first call (via EnsurePersonalWorkspace).
//   - Backfills any w_workagent_thread rows the user owns with
//     project_id = 0 into the Personal Workspace. This is the
//     §1 design's "懒迁移" — existing threads from before Stage 1
//     get grandfathered on the user's first workagent surface load,
//     no separate one-shot script needed.
func ListWorkagentProjectsForUser(ctx context.Context, db *gorm.DB, uid int) ([]ProjectListItem, error) {
	if uid <= 0 {
		return nil, errors.New("ListWorkagentProjectsForUser: uid must be > 0")
	}

	workspace, err := EnsurePersonalWorkspace(ctx, db, uid)
	if err != nil {
		return nil, err
	}

	// Lazy backfill: any of the user's threads still at project_id=0
	// get migrated to Personal Workspace. Idempotent — once a thread
	// has a project_id, the WHERE clause excludes it next time.
	var backfillThreadIDs []uint
	if err := db.WithContext(ctx).Model(&workagentModel.ChatThread{}).
		Where("uid = ? AND project_id = 0", uid).
		Pluck("id", &backfillThreadIDs).Error; err != nil {
		return nil, err
	}
	if err := db.WithContext(ctx).Model(&workagentModel.ChatThread{}).
		Where("uid = ? AND project_id = 0", uid).
		Update("project_id", workspace.Id).Error; err != nil {
		return nil, err
	}
	for _, threadID := range backfillThreadIDs {
		GetThreadCache().Invalidate(fmt.Sprint(threadID))
	}

	var projects []model.CanvasProject
	// Order: system_kind > 0 (Personal Workspace today; future team
	// default / template root) always sorts first, then by
	// updated_at DESC for stable "most recently active" Rail
	// ordering. Switched from a title-string CASE to a numeric
	// system_kind compare in Stage 1.5 — the index
	// idx_global_project_uid_system_kind covers the (uid,
	// system_kind) prefix.
	if err := db.WithContext(ctx).
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("CASE WHEN system_kind > 0 THEN 0 ELSE 1 END, updated_at DESC").
		Find(&projects).Error; err != nil {
		return nil, err
	}

	items := make([]ProjectListItem, 0, len(projects))
	for i := range projects {
		p := &projects[i]
		items = append(items, ProjectListItem{
			Id:         p.Id,
			Title:      p.Title,
			IsPersonal: p.SystemKind == model.GlobalProjectKindPersonalWorkspace,
			CreatedAt:  p.CreatedAt,
			UpdatedAt:  p.UpdatedAt,
		})
	}
	return items, nil
}

// AssignThreadToProject binds an existing workagent thread to a
// project. Both ends are ownership-checked:
//   - thread must belong to uid (WHERE id=? AND uid=?)
//   - project must grant the user at least editor access (CanEditProject)
//
// Returns gorm.ErrRecordNotFound on either failure so the handler can
// map to a generic 404 without revealing whether the thread or the
// project is the missing ref.
func AssignThreadToProject(ctx context.Context, db *gorm.DB, uid int, threadID uint, projectID uint) error {
	if uid <= 0 || threadID == 0 || projectID == 0 {
		return errors.New("AssignThreadToProject: uid, threadID, projectID must all be > 0")
	}

	var thread workagentModel.ChatThread
	if err := db.WithContext(ctx).
		Where("id = ? AND uid = ?", threadID, uid).
		Select("id, uid, project_id").
		First(&thread).Error; err != nil {
		return err
	}

	canEdit, err := projectService.NewRepository(db.WithContext(ctx)).
		CanEditProject(projectID, uint(uid))
	if err != nil {
		return err
	}
	if !canEdit {
		return gorm.ErrRecordNotFound
	}

	return NewThreadRepository(db.WithContext(ctx)).
		SetProjectForOwner(threadID, uint(uid), projectID, time.Now())
}

// DeleteWorkagentProject deletes a user-created project from the
// WorkAgent surface and removes non-canvas WorkAgent threads bound
// to it before delegating the shared project/container cleanup to
// Canvas. Canvas already owns canvas-agent thread cascade; this
// wrapper covers general_agent threads so a deleted WorkAgent project
// does not leave unreachable conversations with a dead project_id.
func DeleteWorkagentProject(ctx context.Context, db *gorm.DB, uid int, projectID uint) error {
	if uid <= 0 || projectID == 0 {
		return errors.New("DeleteWorkagentProject: uid and projectID must both be > 0")
	}

	var threadIDs []uint
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project model.CanvasProject
		if err := tx.
			Where("id = ? AND uid = ? AND deleted_at IS NULL", projectID, uid).
			First(&project).Error; err != nil {
			return err
		}
		if IsPersonalWorkspace(project) {
			return ErrPersonalWorkspaceUndeletable
		}

		if err := tx.Model(&workagentModel.ChatThread{}).
			Where("uid = ? AND project_id = ? AND agent_type <> ?", uid, projectID, "canvas").
			Pluck("id", &threadIDs).Error; err != nil {
			return err
		}
		if len(threadIDs) == 0 {
			return canvasService.DeleteProject(ctx, tx, uid, projectID)
		}

		var threadFileIDs []uint
		if err := tx.Model(&workagentModel.ThreadFile{}).
			Where("uid = ? AND thread_id IN ?", uid, threadIDs).
			Pluck("id", &threadFileIDs).Error; err != nil {
			return err
		}
		if len(threadFileIDs) > 0 {
			now := time.Now()
			if err := tx.Model(&model.GlobalAsset{}).
				Where("source_table = ? AND source_id IN ? AND deleted_at IS NULL", workagentModel.ThreadFile{}.TableName(), threadFileIDs).
				Updates(map[string]interface{}{
					"status":     model.GlobalAssetStatusDeleted,
					"deleted_at": now,
				}).Error; err != nil {
				return err
			}
			if err := tx.Where("uid = ? AND thread_id IN ?", uid, threadIDs).
				Delete(&workagentModel.ThreadFile{}).Error; err != nil {
				return err
			}
		}

		if err := tx.Where("uid = ? AND source IN ? AND thread_id IN ?", uid, []string{"thread_upload", "thread_output"}, threadIDs).
			Delete(&model.UserAssetLedger{}).Error; err != nil {
			return err
		}
		if err := tx.Where("uid = ? AND thread_id IN ?", uid, threadIDs).
			Delete(&workagentModel.ChatMessage{}).Error; err != nil {
			return err
		}
		if err := tx.Where("uid = ? AND project_id = ? AND agent_type <> ? AND id IN ?", uid, projectID, "canvas", threadIDs).
			Delete(&workagentModel.ChatThread{}).Error; err != nil {
			return err
		}
		return canvasService.DeleteProject(ctx, tx, uid, projectID)
	}); err != nil {
		return err
	}

	for _, threadID := range threadIDs {
		GetThreadCache().Invalidate(fmt.Sprint(threadID))
	}
	return nil
}

// IsPersonalWorkspace inspects a project row and reports whether it
// is the user's protected default workspace. Used by deletion guards
// to refuse the operation. Stage 1.5: switched from title-based
// comparison to the unambiguous system_kind column.
func IsPersonalWorkspace(p model.CanvasProject) bool {
	return p.SystemKind == model.GlobalProjectKindPersonalWorkspace
}
