// DB-coupled regression tests for PurgeUserCanvasData. Exercises the
// three-phase cleanup against testutil.NewTestDB (in-memory SQLite),
// using CreateProject + UploadAsset for setup so the same code paths
// that production runs are also what the test seeds against.
//
// Coverage matrix:
//   1.  HappyPath_DeletesAllOwnedProjects: 2 owned projects → both
//       soft-deleted + asset tombstone, report reflects count.
//   2.  NoOpForUserWithNoProjects: zero-project user → all-zero
//       report, no error.
//   3.  RevokesMembershipsWithoutTouchingOthersProjects: user B is
//       editor on user A's project. PurgeUserCanvasData(B) deletes
//       B's member row; project P + user A's data fully intact.
//   4.  Idempotent_SecondCallIsNoOp: run twice in a row; second call
//       returns all-zero report and the system stays consistent.
//   5.  TombstonesResidualOrphanAssets: project_id IS NULL canvas-
//       source assets bound to the user → tombstoned.
//   6.  RejectsZeroUID: defensive guard.
//   7.  CascadesAcrossSnapshotsAndShareRow: a published project with
//       snapshots + share snapshot → everything gone post-purge.

package canvas

import (
	"context"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedMembershipRow writes one w_global_project_member entry directly.
// We don't go through a public service helper because there isn't one
// in canvas (membership rows are set up by the project service during
// share / invite, both outside C-10's surface).
func seedMembershipRow(t *testing.T, db *gorm.DB, projectID uint, uid int, role string) {
	t.Helper()
	now := time.Now()
	if err := db.Exec(
		`INSERT INTO w_global_project_member (project_id, uid, role, source, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, 'invite', ?, ?, ?)`,
		projectID, uid, role, uid, now, now,
	).Error; err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

// seedResidualOrphanAsset writes one w_global_asset row with
// project_id IS NULL — the failed-binding shape Phase 3 sweeps.
// Defaults to source_table=canvas_project_file; use
// seedResidualOrphanAssetForTable to exercise other canvas-origin
// source tables (notably reference_upload).
func seedResidualOrphanAsset(t *testing.T, db *gorm.DB, uid int, url string) {
	t.Helper()
	seedResidualOrphanAssetForTable(t, db, uid, url, "canvas_project_file")
}

func seedResidualOrphanAssetForTable(t *testing.T, db *gorm.DB, uid int, url, sourceTable string) {
	t.Helper()
	now := time.Now()
	if err := db.Exec(
		`INSERT INTO w_global_asset
			(uid, uuid, kind, source, source_table, source_id, source_item_key, url, status, created_at, updated_at)
		 VALUES (?, ?, 'image', 'upload', ?, 0, ?, ?, ?, ?, ?)`,
		uid, "orphan-uuid-"+sourceTable+"-"+url, sourceTable, "k-"+url, url, model.GlobalAssetStatusActive, now, now,
	).Error; err != nil {
		t.Fatalf("seed orphan asset: %v", err)
	}
}

func TestPurgeUserCanvasData_HappyPath_DeletesAllOwnedProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	for i := 0; i < 2; i++ {
		if _, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "p"}); err != nil {
			t.Fatalf("CreateProject %d: %v", i, err)
		}
	}

	report, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}
	if report.ProjectsDeleted != 2 {
		t.Errorf("ProjectsDeleted = %d, want 2", report.ProjectsDeleted)
	}

	var liveCount int64
	if err := db.Model(&model.CanvasProject{}).
		Where("uid = ? AND deleted_at IS NULL", 42).
		Count(&liveCount).Error; err != nil {
		t.Fatalf("count live: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("live projects after purge = %d, want 0", liveCount)
	}
}

func TestPurgeUserCanvasData_NoOpForUserWithNoProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	report, err := PurgeUserCanvasData(context.Background(), db, 99)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}
	if report.ProjectsDeleted != 0 || report.MembershipsRevoked != 0 || report.ResidualAssetsTombed != 0 {
		t.Errorf("zero-user report should be all zero, got %+v", report)
	}
}

func TestPurgeUserCanvasData_RevokesMembershipsWithoutTouchingOthersProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	// User A (uid=42) owns project P.
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "A owns"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// User B (uid=43) is editor on P via direct seed (the public
	// invite path isn't relevant to this test).
	seedMembershipRow(t, db, created.Id, 43, model.GlobalProjectRoleEditor)

	report, err := PurgeUserCanvasData(context.Background(), db, 43)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData(B): %v", err)
	}
	if report.ProjectsDeleted != 0 {
		t.Errorf("B owns nothing — ProjectsDeleted = %d, want 0", report.ProjectsDeleted)
	}
	if report.MembershipsRevoked != 1 {
		t.Errorf("MembershipsRevoked = %d, want 1", report.MembershipsRevoked)
	}

	// User A's project must still be alive.
	var p model.CanvasProject
	if err := db.Where("id = ? AND deleted_at IS NULL", created.Id).First(&p).Error; err != nil {
		t.Fatalf("project P should still be live: %v", err)
	}
	// B's membership row gone.
	var mc int64
	if err := db.Model(&model.GlobalProjectMember{}).
		Where("project_id = ? AND uid = ?", created.Id, 43).
		Count(&mc).Error; err != nil {
		t.Fatalf("count B's membership: %v", err)
	}
	if mc != 0 {
		t.Errorf("B's membership row still present: count=%d", mc)
	}
}

func TestPurgeUserCanvasData_Idempotent_SecondCallIsNoOp(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "p"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	seedResidualOrphanAsset(t, db, 42, "/uploads/orphan.png")

	first, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("first purge: %v", err)
	}
	if first.ProjectsDeleted != 1 || first.ResidualAssetsTombed != 1 {
		t.Fatalf("first purge: %+v, want ProjectsDeleted=1 ResidualAssetsTombed=1", first)
	}

	second, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if second.ProjectsDeleted != 0 || second.MembershipsRevoked != 0 || second.ResidualAssetsTombed != 0 {
		t.Errorf("second purge should be all-zero (idempotent), got %+v", second)
	}
}

func TestPurgeUserCanvasData_TombstonesResidualOrphanAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedResidualOrphanAsset(t, db, 42, "/uploads/a.png")
	seedResidualOrphanAsset(t, db, 42, "/uploads/b.png")
	// Asset for a DIFFERENT user — must NOT be touched.
	seedResidualOrphanAsset(t, db, 99, "/uploads/c.png")

	report, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}
	if report.ResidualAssetsTombed != 2 {
		t.Errorf("ResidualAssetsTombed = %d, want 2 (user 99's asset must be untouched)", report.ResidualAssetsTombed)
	}

	// Verify the other user's asset is still active.
	var alive model.GlobalAsset
	if err := db.Unscoped().Where("uid = ? AND url = ?", 99, "/uploads/c.png").First(&alive).Error; err != nil {
		t.Fatalf("read other user's asset: %v", err)
	}
	if alive.Status == model.GlobalAssetStatusDeleted {
		t.Errorf("other user's asset got tombstoned — boundary leak")
	}
}

// TestPurgeUserCanvasData_TombstonesReferenceUploadOrphans covers the
// second canvas-origin source_table (`reference_upload`, produced by
// composer / @mention reference image uploads via CreateManagedUpload
// in globalasset/repository.go:342). Before this test landed, Phase 3
// filtered source_table = 'canvas_project_file' only, leaving every
// reference image alive after account close — a privacy / retention
// gap (audit 2026-05-16 / P0-②).
//
// Boundary: non-canvas source_table (w_generation_object) must NOT be
// touched by the canvas purge — that ownership belongs to the
// generator surface's own cleanup path.
func TestPurgeUserCanvasData_TombstonesReferenceUploadOrphans(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedResidualOrphanAssetForTable(t, db, 42, "/uploads/ref1.png", "reference_upload")
	seedResidualOrphanAssetForTable(t, db, 42, "/uploads/proj1.png", "canvas_project_file")
	// Boundary: same uid, non-canvas source_table must survive.
	seedResidualOrphanAssetForTable(t, db, 42, "/uploads/gen1.png", "w_generation_object")

	report, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}
	if report.ResidualAssetsTombed != 2 {
		t.Errorf("ResidualAssetsTombed = %d, want 2 (ref + project; generator out of scope)",
			report.ResidualAssetsTombed)
	}

	var generatorAsset model.GlobalAsset
	if err := db.Unscoped().Where("uid = ? AND source_table = ?", 42, "w_generation_object").
		First(&generatorAsset).Error; err != nil {
		t.Fatalf("read generator asset: %v", err)
	}
	if generatorAsset.Status == model.GlobalAssetStatusDeleted {
		t.Errorf("non-canvas source_table got tombstoned — scope leak")
	}
}

func TestPurgeUserCanvasData_RejectsZeroUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	if _, err := PurgeUserCanvasData(context.Background(), db, 0); err == nil {
		t.Fatalf("zero uid should error, got nil")
	}
	if _, err := PurgeUserCanvasData(context.Background(), db, -5); err == nil {
		t.Fatalf("negative uid should error, got nil")
	}
}

// Stage 1.5 regression: PurgeUserCanvasData MUST delete the user's
// Personal Workspace via the DeleteProject AllowSystemKind opt-in.
// Without that opt-in, the Stage 1.5 guard would refuse to delete
// system_kind > 0 rows and the account-close cascade would leak the
// workspace (plus all its threads) into the closed-account graveyard.
func TestPurgeUserCanvasData_DeletesPersonalWorkspaceViaSystemKindOptIn(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Seed the system-managed workspace alongside a regular project.
	ws, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:      "Personal Workspace",
		SystemKind: model.GlobalProjectKindPersonalWorkspace,
	})
	if err != nil {
		t.Fatalf("seed Personal Workspace: %v", err)
	}
	regular, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "regular project"})
	if err != nil {
		t.Fatalf("seed regular project: %v", err)
	}

	report, err := PurgeUserCanvasData(context.Background(), db, 42)
	if err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}
	if report.ProjectsDeleted != 2 {
		t.Errorf("ProjectsDeleted = %d, want 2 (Personal Workspace + regular)", report.ProjectsDeleted)
	}

	// Both rows soft-deleted including the system-managed one.
	for _, id := range []uint{ws.Id, regular.Id} {
		var live int64
		if err := db.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", id).Count(&live).Error; err != nil {
			t.Fatalf("count id=%d: %v", id, err)
		}
		if live != 0 {
			t.Errorf("id=%d should be soft-deleted by account-close cascade, live count = %d", id, live)
		}
	}
}

func TestPurgeUserCanvasData_CascadesAcrossSnapshotsAndShareRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	// We need the asset-ledger schema for DeleteProject to run end-to-end.
	// CreateProject brings everything along.
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "with share"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Seed a w_canvas_share_snapshot row so we can verify it gets cleaned.
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (?, 1, '', '{}', 2, ?, ?)`,
		created.Id, time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed share snapshot: %v", err)
	}

	if _, err := PurgeUserCanvasData(context.Background(), db, 42); err != nil {
		t.Fatalf("PurgeUserCanvasData: %v", err)
	}

	// Init snapshot was hard-deleted.
	var snapCount int64
	if err := db.Model(&model.CanvasSnapshot{}).
		Where("project_id = ?", created.Id).Count(&snapCount).Error; err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapCount != 0 {
		t.Errorf("snapshots remain after purge: count=%d", snapCount)
	}
	// Share snapshot row gone.
	var shareCount int64
	if err := db.Model(&model.CanvasShareSnapshot{}).
		Where("project_id = ?", created.Id).Count(&shareCount).Error; err != nil {
		t.Fatalf("count share snapshots: %v", err)
	}
	if shareCount != 0 {
		t.Errorf("share snapshot remains: count=%d", shareCount)
	}
}

