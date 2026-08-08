// Account-close cascade for Canvas data — C-10 (closed 2026-05-15 by
// owner). Removes everything a user owns / belongs to on the Canvas
// surface, so the existing account anonymization in
// server/api/pro/account/account_api.go DeleteAccount can rely on
// "user closed → their Canvas footprint is gone" instead of leaving
// projects + assets + memberships orphaned indefinitely (the audit
// finding behind C-10).
//
// Three discrete cleanup phases, all idempotent so a retry after a
// partial failure can resume without surfacing duplicate work:
//
//   1. Owner-of projects → per-project DeleteProject (reuses the
//      existing transactional cascade: project → assets → snapshots
//      → share snapshot → task bindings → workagent threads/files →
//      asset-ledger entries). Each project is its own transaction;
//      we deliberately do NOT wrap the whole purge in one outer TX
//      because Canvas projects can run minutes of work across N
//      tables — a single long-running lock would block concurrent
//      writers for the duration.
//
//   2. Membership in other people's projects → hard-DELETE from
//      w_global_project_member (the closing user's rows only). The
//      target project itself stays — that data belongs to its owner,
//      who is not being deleted.
//
//   3. Canvas-source orphan assets (project_id IS NULL) bound to
//      this uid → tombstone. These are failed-binding uploads that
//      never made it into a project. The existing
//      CleanOrphanCanvasAssets cron handles them on a 7-day cadence,
//      but account close shouldn't wait — we tombstone them
//      immediately so ops cleanup hits "already gone".
//
// Failure model: returns (PurgeReport, error) — the report reflects
// what completed BEFORE the error. DeleteAccount uses this to log
// the partial state when a retry is needed. Retries are safe because
// every step's WHERE clause excludes already-processed rows.

package canvas

import (
	"context"
	"fmt"
	"time"

	"server/model"

	"gorm.io/gorm"
)

// PurgeReport summarises what was cleaned. Returned even on error so
// callers can see which step partial-completed.
type PurgeReport struct {
	ProjectsDeleted       int `json:"projectsDeleted"`
	MembershipsRevoked    int `json:"membershipsRevoked"`
	ResidualAssetsTombed  int `json:"residualAssetsTombed"`
}

// PurgeUserCanvasData removes everything the given uid owns on the
// Canvas surface and severs the user's memberships in projects they
// don't own. Safe to call multiple times — every internal step
// filters on the "not yet processed" predicate.
//
// Callers (DeleteAccount today; in principle any account-lifecycle
// orchestrator) should run this BEFORE anonymizing the user row so a
// mid-flight failure leaves the account in a re-runnable state.
func PurgeUserCanvasData(ctx context.Context, db *gorm.DB, uid int) (PurgeReport, error) {
	var report PurgeReport
	if uid <= 0 {
		return report, fmt.Errorf("PurgeUserCanvasData: uid must be > 0, got %d", uid)
	}

	// Phase 1: per-project cascade-delete. Snapshot the id list up
	// front so the loop iterates over a stable set even if some
	// other writer touches the project table between iterations.
	var projectIDs []uint
	if err := db.WithContext(ctx).Model(&model.CanvasProject{}).
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("id ASC").
		Pluck("id", &projectIDs).Error; err != nil {
		return report, fmt.Errorf("list owned projects: %w", err)
	}
	for _, projectID := range projectIDs {
		// AllowSystemKind=true: account close MUST tear down the
		// user's Personal Workspace too, otherwise the cascade is
		// half-done and a re-registered user could end up sharing
		// w_workagent_thread rows with the closed account via
		// project_id collision. Stage 1.5 added this opt-in
		// specifically for this path; every other DeleteProject
		// caller keeps the default refusal.
		if err := DeleteProjectWithOptions(ctx, db, uid, projectID, DeleteProjectOptions{AllowSystemKind: true}); err != nil {
			return report, fmt.Errorf("delete project %d: %w", projectID, err)
		}
		report.ProjectsDeleted++
	}

	// Phase 2: revoke memberships in OTHER people's projects. Hard-
	// delete: a row tagged deleted_at for a closed account adds no
	// audit value (the user is gone, the role label is meaningless)
	// and would compete with the unique index on (project_id, uid)
	// if the same projectID ever gets re-shared with a new uid.
	res := db.WithContext(ctx).Where("uid = ?", uid).Delete(&model.GlobalProjectMember{})
	if res.Error != nil {
		return report, fmt.Errorf("revoke memberships: %w", res.Error)
	}
	report.MembershipsRevoked = int(res.RowsAffected)

	// Phase 3: tombstone canvas-source orphan assets. project_id IS
	// NULL filters to "uploads that never got bound to a project"
	// (failed-binding orphans the C-06 cron picks up after 7 days).
	// Project-bound assets were already tombstoned by DeleteProject
	// in Phase 1.
	//
	// source_table list covers every table CreateManagedUpload writes
	// from canvas surfaces (globalasset/repository.go): canvas_project_file
	// for project assets, reference_upload for reference images the user
	// dropped in the composer / @mention picker. Both are canvas-originating
	// and belong to the closing user; both must be tombstoned on close
	// or the bytes survive indefinitely.
	now := time.Now()
	res2 := db.WithContext(ctx).Model(&model.GlobalAsset{}).
		Where("uid = ? AND source_table IN ? AND project_id IS NULL AND deleted_at IS NULL AND status <> ?",
			uid, []string{"canvas_project_file", "reference_upload"}, model.GlobalAssetStatusDeleted).
		Updates(map[string]interface{}{
			"status":     model.GlobalAssetStatusDeleted,
			"deleted_at": now,
		})
	if res2.Error != nil {
		return report, fmt.Errorf("tombstone residual assets: %w", res2.Error)
	}
	report.ResidualAssetsTombed = int(res2.RowsAffected)

	return report, nil
}
