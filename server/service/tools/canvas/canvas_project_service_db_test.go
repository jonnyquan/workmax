// canvas_project_service_db_test.go — DB-coupled regression tests for
// the project service. The pure-function tests live in
// canvas_project_service_test.go; this file exercises the GORM path
// against an in-memory SQLite installed by testutil.NewTestDB.

package canvas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"
)

// assertCanvasDocumentParity is the M5-B-06 Stage 2 dual-write drift
// check shared across the 4 path tests (CreateProject / PatchElements /
// UpdateCanvasVersion / CreateCanvasVersion). Marshal-compares the two
// JSONMaps so map-key order doesn't make the assertion flaky —
// encoding/json sorts keys deterministically.
//
// label tags the assertion in the failure message so a flaky CI run
// names the offending pair (e.g. "project vs version").
func assertCanvasDocumentParity(t *testing.T, label string, want, got model.JSONMap) {
	t.Helper()
	wantBytes, _ := json.Marshal(want)
	gotBytes, _ := json.Marshal(got)
	if string(wantBytes) != string(gotBytes) {
		t.Errorf("%s drift:\n  expected: %s\n  actual:   %s", label, wantBytes, gotBytes)
	}
}

// TestPatchElements_PreservesSourceAndMessage pins the contract that
// PatchElements is a sub-snapshot operation: it changes `document` /
// `element_count` / `updated_at`, but the user-facing `source` and
// `message` (the snapshot's intent labels) survive untouched.
//
// Regression for the bug where PatchElements wrote source="patch",
// stomping a manual save's "web"/message="Manual save" label on the
// next element drag.
func TestPatchElements_PreservesSourceAndMessage(t *testing.T) {
	db := testutil.NewTestDB(t)

	// 1. Seed project + snapshot directly. Bypass CreateProject so the
	//    test doesn't drag in the asset-ledger schema.
	//
	//    M5-B-06 retire-T3: live state lives on project.document; the
	//    snapshot row carries the source/message labels. PatchElements
	//    no longer touches w_canvas_version (write-disabled).
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "shape",
				"x":    100.0,
				"y":    200.0,
			},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', ?, 1, 1, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', 'Manual save', 1, '', ?, 1, ?)`,
		string(docBytes), now,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// 2. Apply a patch that moves el-1.
	_, err := PatchElements(context.Background(), db, 42, 1, PatchElementsInput{
		Patches: []map[string]interface{}{
			{"id": "el-1", "x": 999.0},
		},
	})
	if err != nil {
		t.Fatalf("PatchElements: %v", err)
	}

	// 3. Read the snapshot row back and assert source/message are
	//    preserved — PatchElements is a sub-snapshot operation and
	//    doesn't touch the snapshot's intent labels.
	var got model.CanvasSnapshot
	if err := db.Raw(`SELECT id, source, message, element_count FROM w_canvas_snapshot WHERE project_id = 1 AND snapshot_no = 1`).
		Scan(&got).Error; err != nil {
		t.Fatalf("read back snapshot: %v", err)
	}
	if got.Source != "web" {
		t.Errorf("source = %q, want %q (PatchElements should not overwrite the snapshot's source label)", got.Source, "web")
	}
	if got.Message != "Manual save" {
		t.Errorf("message = %q, want %q", got.Message, "Manual save")
	}
	// Snapshot's element_count stays at 1 (the "frozen" count when the
	// snapshot was saved); the live count (1 → still 1, since the patch
	// only moved an element, didn't add) is on project.element_count.
	if got.ElementCount != 1 {
		t.Errorf("snapshot.elementCount = %d, want 1", got.ElementCount)
	}
}

// TestCreateProject_DualWritesLiveDocumentToProjectRow pins the
// CreateProject invariant under M5-B-06 retire-T3: project.document
// holds the initial canvas document and an init w_canvas_snapshot
// row (snapshot_no=1) mirrors it. The retire dropped the
// w_canvas_version write — the snapshot row is now the only
// version-history record.
//
// Asserts:
//   - project.document is non-null and matches BuildInitialCanvasDocument().
//   - project.schema_version is current and project.element_count is 0.
//   - project.latest_version is 1.
//   - An init w_canvas_snapshot row (snapshot_no=1) is inserted, with
//     source="init", message="Initial version", and document mirroring
//     the project row.
func TestCreateProject_DualWritesLiveDocumentToProjectRow(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:        "My Project",
		ThumbnailURL: "",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, uid, title, latest_version, document, schema_version, element_count FROM w_global_project WHERE id = ?`,
		created.Id,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if p.Document == nil {
		t.Errorf("project.document is nil; expected initial canvas document")
	}
	if p.SchemaVersion != SchemaVersionV2 {
		t.Errorf("project.schema_version = %d, want %d", p.SchemaVersion, SchemaVersionV2)
	}
	if p.ElementCount != 0 {
		t.Errorf("project.element_count = %d, want 0", p.ElementCount)
	}
	if p.LatestVersion != 1 {
		t.Errorf("project.latest_version = %d, want 1", p.LatestVersion)
	}

	// M5-B-06 retire-T3: init writes snapshot_no=1 (no w_canvas_version
	// row anymore — that table is write-disabled).
	var initSnap model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, project_id, snapshot_no, source, message, element_count, document, schema_version FROM w_canvas_snapshot WHERE project_id = ? ORDER BY snapshot_no`,
		created.Id,
	).Scan(&initSnap).Error; err != nil {
		t.Fatalf("read init snapshot: %v", err)
	}
	if initSnap.SnapshotNo != 1 {
		t.Errorf("init snapshot snapshot_no = %d, want 1", initSnap.SnapshotNo)
	}
	if initSnap.Source != "init" {
		t.Errorf("init snapshot source = %q, want %q", initSnap.Source, "init")
	}
	if initSnap.Message != "Initial version" {
		t.Errorf("init snapshot message = %q, want %q", initSnap.Message, "Initial version")
	}
	assertCanvasDocumentParity(t, "project vs init snapshot", p.Document, initSnap.Document)
}

func TestCreateProject_WritesOwnerProjectMember(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title: "My Project",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	var member model.GlobalProjectMember
	if err := db.
		Where("project_id = ? AND uid = ?", created.Id, 42).
		First(&member).Error; err != nil {
		t.Fatalf("owner member row missing: %v", err)
	}
	if member.Role != model.GlobalProjectRoleOwner {
		t.Fatalf("member role = %q, want owner", member.Role)
	}
	if member.Source != model.GlobalProjectMemberSourceOwner {
		t.Fatalf("member source = %q, want owner", member.Source)
	}
}

func TestProjectMemberViewerCanReadButCannotPatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "Shared"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: created.Id,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed viewer: %v", err)
	}

	if _, err := GetProject(context.Background(), db, 99, created.Id); err != nil {
		t.Fatalf("viewer GetProject: %v", err)
	}
	_, err = PatchElements(context.Background(), db, 99, created.Id, PatchElementsInput{
		Patches: []map[string]interface{}{{"id": "missing", "x": 1}},
	})
	if !errors.Is(err, ErrCanvasProjectNotFound) {
		t.Fatalf("viewer PatchElements err = %v, want ErrCanvasProjectNotFound", err)
	}
}

func TestProjectMemberEditorCanPatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	doc := model.JSONMap{
		"schemaVersion": SchemaVersionV2,
		"viewMode":      "freeform",
		"elements": []interface{}{
			map[string]interface{}{"id": "el-1", "type": "shape", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "Shared", Document: doc})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: created.Id,
		UID:       99,
		Role:      model.GlobalProjectRoleEditor,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed editor: %v", err)
	}

	result, err := PatchElements(context.Background(), db, 99, created.Id, PatchElementsInput{
		Patches: []map[string]interface{}{{"id": "el-1", "x": 5.0}},
	})
	if err != nil {
		t.Fatalf("editor PatchElements: %v", err)
	}
	if !result.Patched {
		t.Fatal("editor patch should be applied")
	}
}

func TestDeleteProject_TombstonesGlobalProjectAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "With Asset"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	asset, err := UploadAsset(context.Background(), db, LocalAssetStorage{Root: t.TempDir()}, 42, created.Id, UploadAssetInput{
		FileBytes:         pngMagic,
		OriginalName:      "asset.png",
		HeaderContentType: "image/png",
		Kind:              "upload",
	})
	if err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	if asset.GlobalAssetID == 0 {
		t.Fatalf("uploaded asset missing global bridge")
	}

	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var global model.GlobalAsset
	if err := db.Unscoped().First(&global, asset.GlobalAssetID).Error; err != nil {
		t.Fatalf("load global asset: %v", err)
	}
	if global.Status != model.GlobalAssetStatusDeleted || global.DeletedAt == nil {
		t.Fatalf("global asset deletion state = status %d deleted_at %v, want deleted tombstone", global.Status, global.DeletedAt)
	}
}

func TestCreateProject_CopySharedProjectAssetsForDocument(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	sourceDoc := model.JSONMap{
		"schemaVersion": SchemaVersionV2,
		"viewMode":      "freeform",
		"elements": []interface{}{
			map[string]interface{}{
				"id":     "image-1",
				"type":   "image",
				"x":      0,
				"y":      0,
				"width":  640,
				"height": 480,
				"src":    "/uploads/canvas/uid/7/source-uuid/image.png",
			},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	docBytes, _ := json.Marshal(sourceDoc)
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (11, 7, 'source-uuid', 'Shared', 2, '', ?, 2, 1, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed source project: %v", err)
	}
	sourceProjectID := uint(11)
	if err := db.Create(&model.GlobalAsset{
		UID:           7,
		ProjectID:     &sourceProjectID,
		UUID:          "source-global-asset",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      uint64(sourceProjectID),
		SourceItemKey: "source-image",
		URL:           "/uploads/canvas/uid/7/source-uuid/image.png",
		MimeType:      "image/png",
		SizeBytes:     123,
		Width:         640,
		Height:        480,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata:      model.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("seed source global asset: %v", err)
	}

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{
		Title:             "Copied",
		Document:          sourceDoc,
		SourceProjectUUID: "source-uuid",
	})
	if err != nil {
		t.Fatalf("CreateProject copy: %v", err)
	}

	var copied model.GlobalAsset
	if err := db.
		Where("uid = ? AND project_id = ? AND url = ?", 42, created.Id, "/uploads/canvas/uid/7/source-uuid/image.png").
		First(&copied).Error; err != nil {
		t.Fatalf("read copied global asset: %v", err)
	}
	if copied.UID != 42 || copied.ProjectID == nil || *copied.ProjectID != created.Id {
		t.Fatalf("copied asset ownership = uid %d project %v, want uid 42 project %d", copied.UID, copied.ProjectID, created.Id)
	}
	if copied.MimeType != "image/png" {
		t.Errorf("copied asset mime = %q, want image/png", copied.MimeType)
	}
	if copied.Metadata["copiedFromProjectUuid"] != "source-uuid" {
		t.Errorf("copied metadata missing source uuid: %#v", copied.Metadata)
	}
	if copied.SourceTable != "canvas_project_file" || copied.ProjectID == nil || *copied.ProjectID != created.Id {
		t.Fatalf("copied global asset = source_table %q project %v, want canvas_project_file project %d", copied.SourceTable, copied.ProjectID, created.Id)
	}
	var ledger model.UserAssetLedger
	if err := db.
		Where("uid = ? AND source = ? AND global_asset_id = ?", 42, "canvas", copied.Id).
		First(&ledger).Error; err != nil {
		t.Fatalf("read copied asset ledger: %v", err)
	}
	if ledger.SourceID != uint64(copied.Id) || ledger.ProjectID != created.Id {
		t.Fatalf("copied asset ledger = source_id %d project %d, want source_id %d project %d", ledger.SourceID, ledger.ProjectID, copied.Id, created.Id)
	}
}

// TestPatchElements_DualWritesLiveDocumentToProjectRow pins the
// PatchElements invariant under M5-B-06 retire-T3: PatchElements
// reads from project.document (live state) and writes back to it.
// The w_canvas_version table is no longer read or written; the
// snapshot row's source/message stay untouched.
//
// Setup mirrors TestPatchElements_PreservesSourceAndMessage: seed via
// raw SQL to bypass CreateProject's asset-ledger sync.
func TestPatchElements_DualWritesLiveDocumentToProjectRow(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Seed project (with document) + snapshot row.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{
				"id":   "el-1",
				"type": "shape",
				"x":    100.0,
				"y":    200.0,
			},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', ?, 1, 1, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', 'Manual save', 1, '', ?, 1, ?)`,
		string(docBytes), now,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Apply a patch that moves el-1.
	if _, err := PatchElements(context.Background(), db, 42, 1, PatchElementsInput{
		Patches: []map[string]interface{}{
			{"id": "el-1", "x": 999.0},
		},
	}); err != nil {
		t.Fatalf("PatchElements: %v", err)
	}

	// Read the project row back and assert the live-document fields.
	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, document, schema_version, element_count FROM w_global_project WHERE id = 1`,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if p.Document == nil {
		t.Errorf("project.document is nil; PatchElements should have populated it")
	}
	if p.SchemaVersion != SchemaVersionV2 {
		t.Errorf("project.schema_version = %d, want %d", p.SchemaVersion, SchemaVersionV2)
	}
	if p.ElementCount != 1 {
		t.Errorf("project.element_count = %d, want 1", p.ElementCount)
	}

	// Spot-check: the patched x value should be in project.document.
	pBytes, _ := json.Marshal(p.Document)
	if !strings.Contains(string(pBytes), "999") {
		t.Errorf("project.document didn't reflect the patched x=999.0: %s", pBytes)
	}

	// Snapshot row is NOT touched — PatchElements is a sub-snapshot op.
	var snap model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, source, message, document FROM w_canvas_snapshot WHERE project_id = 1 AND snapshot_no = 1`,
	).Scan(&snap).Error; err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if snap.Source != "web" {
		t.Errorf("snapshot.source = %q, want %q (PatchElements must not stomp source)", snap.Source, "web")
	}
	if snap.Message != "Manual save" {
		t.Errorf("snapshot.message = %q, want %q", snap.Message, "Manual save")
	}
}

// TestUpdateCanvasVersion_DualWritesLiveDocumentToProjectRow pins the
// UpdateCanvasVersion invariant under M5-B-06 retire-T3 (autosave and
// "save current version" path): updating the latest version writes
// both the snapshot row's metadata + document AND the project row's
// live document. The w_canvas_version table is no longer touched.
//
// Setup mirrors the PatchElements test: seed via raw SQL, invoke
// UpdateCanvasVersion with a fresh document, assert the project row +
// snapshot row both reflect the new content.
func TestUpdateCanvasVersion_DualWritesLiveDocumentToProjectRow(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	initialDocBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(initialDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', 'Manual save', 0, '', ?, 1, ?)`,
		string(initialDocBytes), now,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Apply UpdateCanvasVersion with a doc that has 2 elements (autosave shape).
	newDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "el-1", "type": "shape", "x": 0.0, "y": 0.0},
			map[string]interface{}{"id": "el-2", "type": "text", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	autosaveSource := "autosave"
	if _, err := UpdateCanvasVersion(context.Background(), db, 42, 1, 1, UpdateVersionInput{
		Document: newDoc,
		Source:   &autosaveSource,
	}); err != nil {
		t.Fatalf("UpdateCanvasVersion: %v", err)
	}

	// Assert project row was updated.
	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, document, schema_version, element_count FROM w_global_project WHERE id = 1`,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if p.Document == nil {
		t.Errorf("project.document is nil; UpdateCanvasVersion should have populated it")
	}
	if p.SchemaVersion != SchemaVersionV2 {
		t.Errorf("project.schema_version = %d, want %d", p.SchemaVersion, SchemaVersionV2)
	}
	if p.ElementCount != 2 {
		t.Errorf("project.element_count = %d, want 2", p.ElementCount)
	}

	// Assert no drift between project.document and snapshot.document.
	var snap model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, document, element_count, source FROM w_canvas_snapshot WHERE project_id = 1 AND snapshot_no = 1`,
	).Scan(&snap).Error; err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	assertCanvasDocumentParity(t, "project vs snapshot (UpdateCanvasVersion)", p.Document, snap.Document)
	if snap.Source != "autosave" {
		t.Errorf("snapshot.source = %q, want %q (UpdateCanvasVersion writes the new source label)", snap.Source, "autosave")
	}

	// Spot-check: el-2 should be in project.document.
	pBytes, _ := json.Marshal(p.Document)
	if !strings.Contains(string(pBytes), `"el-2"`) {
		t.Errorf("project.document didn't reflect the new elements: %s", pBytes)
	}
}

// TestWriteLatestVersionDocument_RejectsSmuggledDocumentInMetadata pins
// the parity-invariant guard added in P2-2: the dual-write helper
// `writeLatestVersionDocument` is the SINGLE seam for writing the live
// document mirror. A future contributor who tries to slip a divergent
// `document` (or element_count / schema_version) into the helper via
// the snapshotUpdates metadata bag must NOT be able to break parity —
// these three keys are owned by the helper and overrides are silently
// dropped.
//
// Without this guard, the helper would only enforce parity by convention.
// With it, a misuse is impossible by construction.
func TestWriteLatestVersionDocument_RejectsSmuggledDocumentInMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	canonicalDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "canonical", "type": "shape", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	canonicalBytes, _ := json.Marshal(canonicalDoc)
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(canonicalBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', '', 0, '', '{}', 1, ?)`,
		now,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Adversarial metadata: try to smuggle a divergent document, count,
	// and schema_version through the metadata channel. The helper must
	// drop them all.
	maliciousMetadata := map[string]interface{}{
		"document":       model.JSONMap{"elements": []interface{}{"DIVERGENT"}},
		"element_count":  9999,
		"schema_version": 7,
		"message":        "legit metadata still works",
	}

	if err := writeLatestVersionDocument(
		db, 1, 42, 1, canonicalDoc, maliciousMetadata, time.Now(),
	); err != nil {
		t.Fatalf("writeLatestVersionDocument: %v", err)
	}

	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, document, schema_version, element_count FROM w_global_project WHERE id = 1`,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	var snap model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, document, element_count, schema_version, message FROM w_canvas_snapshot WHERE id = 1`,
	).Scan(&snap).Error; err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	assertCanvasDocumentParity(t, "project vs snapshot (smuggled metadata)", p.Document, snap.Document)
	assertCanvasDocumentParity(t, "project vs canonical doc", canonicalDoc, p.Document)

	if p.ElementCount != 1 || snap.ElementCount != 1 {
		t.Errorf("element_count drift: project=%d snapshot=%d, want 1 on both (helper computed from canonical doc)",
			p.ElementCount, snap.ElementCount)
	}
	if p.SchemaVersion != snap.SchemaVersion {
		t.Errorf("schema_version drift: project=%d snapshot=%d", p.SchemaVersion, snap.SchemaVersion)
	}
	if snap.Message != "legit metadata still works" {
		t.Errorf("snapshot.message = %q, want metadata passthrough to apply", snap.Message)
	}
}

// TestWriteLatestVersionDocument_PinsParityAcrossDualWrite is the
// happy-path counterpart to the smuggling test: the same canonical doc
// flows to both rows via the helper, and the rows are byte-identical
// afterwards. Cheap to keep around — guards against a refactor that
// might "optimize" by inlining one write and dropping the other.
func TestWriteLatestVersionDocument_PinsParityAcrossDualWrite(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', '{"elements":[]}', 1, 0, 1, ?, ?)`,
		now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', '', 0, '', '{"elements":[]}', 1, ?)`,
		now,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	doc := model.JSONMap{
		"schemaVersion": 2,
		"viewMode":      "freeform",
		"elements": []interface{}{
			map[string]interface{}{"id": "x", "type": "shape", "x": 10.0, "y": 20.0},
			map[string]interface{}{"id": "y", "type": "text", "content": "hello"},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{"x"},
	}

	if err := writeLatestVersionDocument(
		db, 1, 42, 1, doc, nil, time.Now(),
	); err != nil {
		t.Fatalf("writeLatestVersionDocument: %v", err)
	}

	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, document, element_count, schema_version FROM w_global_project WHERE id = 1`,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	var snap model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, document, element_count, schema_version FROM w_canvas_snapshot WHERE id = 1`,
	).Scan(&snap).Error; err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	assertCanvasDocumentParity(t, "project vs snapshot (helper happy path)", p.Document, snap.Document)
	if p.ElementCount != snap.ElementCount {
		t.Errorf("element_count drift: project=%d snapshot=%d", p.ElementCount, snap.ElementCount)
	}
	if p.SchemaVersion != snap.SchemaVersion {
		t.Errorf("schema_version drift: project=%d snapshot=%d", p.SchemaVersion, snap.SchemaVersion)
	}
	if p.ElementCount != 2 {
		t.Errorf("element_count = %d, want 2 (helper computes from doc)", p.ElementCount)
	}
}

// TestCreateCanvasVersion_DualWritesSnapshotAndProjectMirror pins the
// CreateCanvasVersion invariant under M5-B-06 retire-T3 ("Save as new
// version"): the call inserts a w_canvas_snapshot row with the next
// snapshot_no, advances project.latest_version, and updates
// project.document. The w_canvas_version table is no longer written.
//
// Asserts:
//   - exactly one new w_canvas_snapshot row exists at snapshot_no=2
//     (after the seeded init snapshot at snapshot_no=1).
//   - snapshot.document/source/message/element_count/schema_version
//     match the input.
//   - project.document mirrors the new state.
//   - project.latest_version is 2.
//   - drift check: project.document == snapshot.document.
func TestCreateCanvasVersion_DualWritesSnapshotAndProjectMirror(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Seed project + init snapshot (snapshot_no=1).
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	initBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'My Project', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(initBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'init', 'Initial version', 0, '', ?, 1, ?)`,
		string(initBytes), now,
	).Error; err != nil {
		t.Fatalf("seed init snapshot: %v", err)
	}

	// User hits "Save as new version" with a 3-element doc.
	newDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "a", "type": "shape", "x": 0.0, "y": 0.0},
			map[string]interface{}{"id": "b", "type": "shape", "x": 0.0, "y": 0.0},
			map[string]interface{}{"id": "c", "type": "text", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	created, err := CreateCanvasVersion(context.Background(), db, 42, 1, CreateVersionInput{
		Document:     newDoc,
		Message:      "Pre-launch backup",
		Source:       "web",
		ThumbnailURL: "",
	})
	if err != nil {
		t.Fatalf("CreateCanvasVersion: %v", err)
	}
	if created.Version != 2 {
		t.Errorf("created.Version = %d, want 2", created.Version)
	}

	// Snapshot table: 2 rows total (init + new), new at snapshot_no=2.
	var snaps []model.CanvasSnapshot
	if err := db.Raw(
		`SELECT id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version FROM w_canvas_snapshot WHERE project_id = 1 ORDER BY snapshot_no`,
	).Scan(&snaps).Error; err != nil {
		t.Fatalf("read snapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("snapshot count = %d, want 2 (init + new)", len(snaps))
	}
	s := snaps[1]
	if s.SnapshotNo != 2 {
		t.Errorf("new snapshot_no = %d, want 2", s.SnapshotNo)
	}
	if s.Source != "web" {
		t.Errorf("snapshot.source = %q, want %q", s.Source, "web")
	}
	if s.Message != "Pre-launch backup" {
		t.Errorf("snapshot.message = %q, want %q", s.Message, "Pre-launch backup")
	}
	if s.ElementCount != 3 {
		t.Errorf("snapshot.element_count = %d, want 3", s.ElementCount)
	}
	if s.SchemaVersion != SchemaVersionV2 {
		t.Errorf("snapshot.schema_version = %d, want %d", s.SchemaVersion, SchemaVersionV2)
	}
	if s.CreatedBy != 42 {
		t.Errorf("snapshot.created_by = %d, want 42", s.CreatedBy)
	}

	// Project mirror updated.
	var p model.CanvasProject
	if err := db.Raw(
		`SELECT id, document, schema_version, element_count, latest_version FROM w_global_project WHERE id = 1`,
	).Scan(&p).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if p.Document == nil {
		t.Errorf("project.document is nil; CreateCanvasVersion should have populated it")
	}
	if p.ElementCount != 3 {
		t.Errorf("project.element_count = %d, want 3", p.ElementCount)
	}
	if p.LatestVersion != 2 {
		t.Errorf("project.latest_version = %d, want 2", p.LatestVersion)
	}

	// Drift check: project.document == new snapshot.document.
	assertCanvasDocumentParity(t, "project vs new snapshot (CreateCanvasVersion)", p.Document, s.Document)
}

// TestCreateCanvasVersion_AllocatesMonotonicSnapshotNo ensures
// snapshot_no advances correctly across multiple CreateCanvasVersion
// calls on the same project. Race conditions are caught by the unique
// (project_id, snapshot_no) constraint, so we only need to prove the
// happy-path monotonicity here.
//
// M5-B-06 retire-T3: snapshot_no is now allocated as
// project.LatestVersion+1 (the project row is FOR UPDATE-locked), and
// the seed includes an init snapshot at snapshot_no=1.
func TestCreateCanvasVersion_AllocatesMonotonicSnapshotNo(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	initBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{}, "viewport": map[string]interface{}{"x": 0, "y": 0, "scale": 1}, "selectedIds": []string{}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'P', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(initBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'init', 'Initial version', 0, '', ?, 1, ?)`,
		string(initBytes), now,
	).Error; err != nil {
		t.Fatalf("seed init snapshot: %v", err)
	}

	doc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{map[string]interface{}{"id": "x", "type": "image", "x": 0.0, "y": 0.0}},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	for i := 0; i < 3; i++ {
		if _, err := CreateCanvasVersion(context.Background(), db, 42, 1, CreateVersionInput{
			Document: doc,
			Source:   "web",
		}); err != nil {
			t.Fatalf("CreateCanvasVersion #%d: %v", i+1, err)
		}
	}

	var nos []int
	if err := db.Raw(`SELECT snapshot_no FROM w_canvas_snapshot WHERE project_id = 1 ORDER BY snapshot_no`).Scan(&nos).Error; err != nil {
		t.Fatalf("read snapshot_nos: %v", err)
	}
	// 1 (init) + 3 (created) = 4 rows, snapshot_no 1..4.
	if len(nos) != 4 {
		t.Fatalf("snapshot count = %d, want 4 (init + 3)", len(nos))
	}
	for i, n := range nos {
		if n != i+1 {
			t.Errorf("snapshot_no[%d] = %d, want %d", i, n, i+1)
		}
	}
}

// TestGetCanvasVersion_LatestReturnsLiveStateFromProject pins the
// retire-step contract: GetCanvasVersion for the latest version
// returns project.document (the live mirror updated by autosave /
// patch / manual save), NOT the snapshot's frozen copy. Snapshots
// fall behind project.document between "Save as new version" calls.
func TestGetCanvasVersion_LatestReturnsLiveStateFromProject(t *testing.T) {
	db := testutil.NewTestDB(t)

	// CreateProject seeds project + init snapshot (snapshot_no=1).
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P", ThumbnailURL: ""})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Simulate autosave: project.document moves on, but snapshot stays frozen.
	liveDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{map[string]interface{}{"id": "live-1", "type": "shape", "x": 0.0, "y": 0.0}},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	liveBytes, _ := json.Marshal(liveDoc)
	if err := db.Exec(
		`UPDATE w_global_project SET document = ?, element_count = ? WHERE id = ?`,
		string(liveBytes), 1, created.Id,
	).Error; err != nil {
		t.Fatalf("simulate autosave: %v", err)
	}

	// GetCanvasVersion(latest) should return liveDoc, not init snapshot's empty doc.
	v, err := GetCanvasVersion(context.Background(), db, 42, uint64(created.Id), 1)
	if err != nil {
		t.Fatalf("GetCanvasVersion: %v", err)
	}
	if v.Version != 1 {
		t.Errorf("v.Version = %d, want 1", v.Version)
	}
	if v.ElementCount != 1 {
		t.Errorf("v.ElementCount = %d, want 1 (live state, not init snapshot's 0)", v.ElementCount)
	}
	assertCanvasDocumentParity(t, "GetCanvasVersion(latest) vs liveDoc", liveDoc, v.Document)
}

// TestGetCanvasVersion_HistoricalReturnsFrozenSnapshot pins that for
// version < latest, GetCanvasVersion returns the snapshot's frozen
// document — NOT project.document. (E.g. a user browsing version
// history sees what was saved, not the current live state.)
func TestGetCanvasVersion_HistoricalReturnsFrozenSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P", ThumbnailURL: ""})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	// Add a 2nd snapshot via "Save as new version" with a 2-element doc.
	saveDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "save-1", "type": "shape", "x": 0.0, "y": 0.0},
			map[string]interface{}{"id": "save-2", "type": "shape", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	if _, err := CreateCanvasVersion(context.Background(), db, 42, uint64(created.Id), CreateVersionInput{
		Document: saveDoc,
		Message:  "Snap 2",
		Source:   "web",
	}); err != nil {
		t.Fatalf("CreateCanvasVersion: %v", err)
	}

	// Simulate post-save autosave: project.document moves to a 3-element doc.
	liveDoc := model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "live-a"},
			map[string]interface{}{"id": "live-b"},
			map[string]interface{}{"id": "live-c"},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	}
	liveBytes, _ := json.Marshal(liveDoc)
	if err := db.Exec(
		`UPDATE w_global_project SET document = ?, element_count = ? WHERE id = ?`,
		string(liveBytes), 3, created.Id,
	).Error; err != nil {
		t.Fatalf("simulate autosave: %v", err)
	}

	// version=1 (init) should still return init's frozen empty doc.
	v1, err := GetCanvasVersion(context.Background(), db, 42, uint64(created.Id), 1)
	if err != nil {
		t.Fatalf("GetCanvasVersion(1): %v", err)
	}
	if v1.ElementCount != 0 {
		t.Errorf("v1.ElementCount = %d, want 0 (init snapshot frozen)", v1.ElementCount)
	}
	if v1.Source != "init" {
		t.Errorf("v1.Source = %q, want %q", v1.Source, "init")
	}

	// version=2 (latest, after Save as new version + autosave) should return liveDoc.
	v2, err := GetCanvasVersion(context.Background(), db, 42, uint64(created.Id), 2)
	if err != nil {
		t.Fatalf("GetCanvasVersion(2): %v", err)
	}
	if v2.ElementCount != 3 {
		t.Errorf("v2.ElementCount = %d, want 3 (live state)", v2.ElementCount)
	}
	assertCanvasDocumentParity(t, "v2 vs liveDoc (live state)", liveDoc, v2.Document)
}

// TestListCanvasVersions_ReturnsAllSnapshotsDescending pins listing
// from the snapshot table, ordered by snapshot_no DESC, document
// excluded (metadata-only).
func TestListCanvasVersions_ReturnsAllSnapshotsDescending(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P", ThumbnailURL: ""})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	doc := model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{}, "viewport": map[string]interface{}{"x": 0, "y": 0, "scale": 1}, "selectedIds": []string{}}
	for i := 0; i < 2; i++ {
		if _, err := CreateCanvasVersion(context.Background(), db, 42, uint64(created.Id), CreateVersionInput{
			Document: doc,
			Source:   "web",
			Message:  "save",
		}); err != nil {
			t.Fatalf("CreateCanvasVersion #%d: %v", i+1, err)
		}
	}

	res, err := ListCanvasVersions(context.Background(), db, 42, uint64(created.Id), ListVersionsInput{
		Page: 1, Limit: 10, IncludeTotal: true,
	})
	if err != nil {
		t.Fatalf("ListCanvasVersions: %v", err)
	}
	if res.Total != 3 {
		t.Errorf("res.Total = %d, want 3 (init + 2 saves)", res.Total)
	}
	if len(res.Items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(res.Items))
	}
	// DESC order: 3, 2, 1
	if res.Items[0].Version != 3 || res.Items[1].Version != 2 || res.Items[2].Version != 1 {
		t.Errorf("version order = %d/%d/%d, want 3/2/1",
			res.Items[0].Version, res.Items[1].Version, res.Items[2].Version)
	}
	// Document excluded from listing
	for i, item := range res.Items {
		if item.Document != nil {
			t.Errorf("item[%d].Document is non-nil; should be excluded from listing", i)
		}
	}
}

// TestDeleteProject_HardDeletesSnapshots pins M5-B-06 cleanup contract:
// when a project is soft-deleted, its w_canvas_snapshot rows must be
// hard-deleted (the table has no deleted_at column by design).
//
// Regression: Retire-T2/T3 missed this code path because no service-
// level test exercised DeleteProject. Retire-T5's table DROP would
// have failed at runtime had a delete been issued; the implementer
// caught it and removed a stale w_canvas_version UPDATE in the same
// commit. This test prevents the inverse omission for snapshots.
func TestDeleteProject_HardDeletesSnapshots(t *testing.T) {
	db := testutil.NewTestDB(t)

	// Seed a project (with init snapshot via CreateProject), add 2
	// more snapshots via CreateCanvasVersion, confirm we have 3
	// snapshot rows, then delete and assert all snapshots are gone.
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P", ThumbnailURL: ""})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	doc := model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{}, "viewport": map[string]interface{}{"x": 0, "y": 0, "scale": 1}, "selectedIds": []string{}}
	for i := 0; i < 2; i++ {
		if _, err := CreateCanvasVersion(context.Background(), db, 42, uint64(created.Id), CreateVersionInput{
			Document: doc,
			Source:   "web",
			Message:  "save",
		}); err != nil {
			t.Fatalf("CreateCanvasVersion #%d: %v", i+1, err)
		}
	}

	// Confirm 3 snapshots before delete (init + 2 saves).
	var beforeCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_snapshot WHERE project_id = ?`, created.Id).Scan(&beforeCount).Error; err != nil {
		t.Fatalf("count snapshots before: %v", err)
	}
	if beforeCount != 3 {
		t.Fatalf("snapshot count before delete = %d, want 3", beforeCount)
	}

	// Delete the project.
	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	// Project row soft-deleted (deleted_at IS NOT NULL).
	var deletedAt sql.NullString
	if err := db.Raw(`SELECT deleted_at FROM w_global_project WHERE id = ?`, created.Id).Scan(&deletedAt).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if !deletedAt.Valid {
		t.Errorf("project.deleted_at is NULL after DeleteProject; expected soft-delete timestamp")
	}

	// Snapshot rows hard-deleted.
	var afterCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_snapshot WHERE project_id = ?`, created.Id).Scan(&afterCount).Error; err != nil {
		t.Fatalf("count snapshots after: %v", err)
	}
	if afterCount != 0 {
		t.Errorf("snapshot count after delete = %d, want 0 (snapshots must hard-delete with project)", afterCount)
	}
}

// TestPatchElements_RejectsStaleExpectedProjectUpdatedAt pins the
// multi-tab last-writer-wins guard added to PatchElements: when the
// caller passes ExpectedProjectUpdatedAt and the project row has
// advanced past it (allowing a 500ms tolerance to absorb MySQL's
// second-rounding window), the patch fails with ErrCanvasVersionConflict.
//
// Mirror of UpdateCanvasVersion's gate (canvas_version_service.go:441)
// so the two endpoints share identical semantics — a client cacheing
// the same revision token can use either.
func TestPatchElements_RejectsStaleExpectedProjectUpdatedAt(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC()
	nowSQL := now.Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "el-1", "type": "shape", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'P', 0, '', ?, 1, 1, 1, ?, ?)`,
		string(docBytes), nowSQL, nowSQL,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', 'Manual save', 1, '', ?, 1, ?)`,
		string(docBytes), nowSQL,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Token captured *before* a competing tab wrote — simulate by
	// stamping project.updated_at to a time clearly past the tolerance
	// (3s ahead so the 500ms grace doesn't mask the conflict).
	staleToken := now.Add(-3 * time.Second)
	if err := db.Exec(
		`UPDATE w_global_project SET updated_at = ? WHERE id = 1`,
		now.Format("2006-01-02 15:04:05.000000"),
	).Error; err != nil {
		t.Fatalf("bump updated_at: %v", err)
	}

	_, err := PatchElements(context.Background(), db, 42, 1, PatchElementsInput{
		ExpectedProjectUpdatedAt: &staleToken,
		Patches: []map[string]interface{}{
			{"id": "el-1", "x": 999.0},
		},
	})
	if !errors.Is(err, ErrCanvasVersionConflict) {
		t.Fatalf("PatchElements: got err=%v, want ErrCanvasVersionConflict", err)
	}

	// Document on disk must be unchanged — the conflict aborts before
	// the write, so el-1.x stays at 0 (not 999).
	var stored model.CanvasProject
	if err := db.Raw(`SELECT document FROM w_global_project WHERE id = 1`).Scan(&stored).Error; err != nil {
		t.Fatalf("read project doc: %v", err)
	}
	storedElements, _ := stored.Document["elements"].([]interface{})
	if len(storedElements) != 1 {
		t.Fatalf("storedElements len = %d, want 1", len(storedElements))
	}
	first, _ := storedElements[0].(map[string]interface{})
	if x, _ := first["x"].(float64); x != 0 {
		t.Errorf("el-1.x = %v after rejected patch, want 0 (conflict must abort the write)", x)
	}
}

// TestPatchElements_AllowsFreshExpectedProjectUpdatedAt pins the
// happy-path complement: when the caller's token matches the project
// row's updated_at (within tolerance), the patch goes through. Without
// this assertion, the conflict test could still pass against a setter
// that always rejects — this proves the gate isn't mistakenly closed
// for the common single-tab case.
func TestPatchElements_AllowsFreshExpectedProjectUpdatedAt(t *testing.T) {
	db := testutil.NewTestDB(t)

	now := time.Now().UTC()
	nowSQL := now.Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{
		"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{
			map[string]interface{}{"id": "el-1", "type": "shape", "x": 0.0, "y": 0.0},
		},
		"viewport":    map[string]interface{}{"x": 0, "y": 0, "scale": 1},
		"selectedIds": []string{},
	})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-1', 'P', 0, '', ?, 1, 1, 1, ?, ?)`,
		string(docBytes), nowSQL, nowSQL,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
		 VALUES (1, 1, 1, 42, 'web', 'Manual save', 1, '', ?, 1, ?)`,
		string(docBytes), nowSQL,
	).Error; err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}

	// Token within the 500ms tolerance window — should be accepted.
	freshToken := now.Add(100 * time.Millisecond)
	if _, err := PatchElements(context.Background(), db, 42, 1, PatchElementsInput{
		ExpectedProjectUpdatedAt: &freshToken,
		Patches: []map[string]interface{}{
			{"id": "el-1", "x": 999.0},
		},
	}); err != nil {
		t.Fatalf("PatchElements: %v (expected success with fresh token)", err)
	}

	var stored model.CanvasProject
	if err := db.Raw(`SELECT document FROM w_global_project WHERE id = 1`).Scan(&stored).Error; err != nil {
		t.Fatalf("read project doc: %v", err)
	}
	storedElements, _ := stored.Document["elements"].([]interface{})
	first, _ := storedElements[0].(map[string]interface{})
	if x, _ := first["x"].(float64); x != 999 {
		t.Errorf("el-1.x = %v, want 999 (fresh-token patch must apply)", x)
	}
}

// TestCreateCanvasVersion_PrunesSnapshotsToCap pins B3: w_canvas_snapshot
// rows are hard-capped at MaxSnapshotsPerProject. The init snapshot
// (snapshot_no=1) survives every prune so the project's origin stays
// traceable.
//
// Test seeds (cap-1) snapshots beyond init (so total = cap), creates one
// more via the service, asserts:
//   - count == cap (not cap+1)
//   - init snapshot still exists
//   - the next-oldest non-init snapshot was the one dropped
//   - newest snapshot is the just-created one
func TestCreateCanvasVersion_PrunesSnapshotsToCap(t *testing.T) {
	if MaxSnapshotsPerProject < 3 {
		t.Skip("cap too low for this test to be meaningful")
	}
	db := testutil.NewTestDB(t)

	// Seed project + init snapshot via CreateProject (real path).
	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{}})
	// Backfill snapshots 2..MaxSnapshotsPerProject so we already sit
	// AT the cap. The next CreateCanvasVersion call must trigger one
	// prune to keep total == cap.
	for n := 2; n <= MaxSnapshotsPerProject; n++ {
		if err := db.Exec(
			`INSERT INTO w_canvas_snapshot (project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
			 VALUES (?, ?, 42, 'web', 'save', 0, '', ?, 1, ?)`,
			created.Id, n, string(docBytes), now,
		).Error; err != nil {
			t.Fatalf("seed snapshot %d: %v", n, err)
		}
	}
	// Bump project.latest_version so CreateCanvasVersion's allocator
	// sees the right next-snapshot-no.
	if err := db.Exec(`UPDATE w_global_project SET latest_version = ? WHERE id = ?`, MaxSnapshotsPerProject, created.Id).Error; err != nil {
		t.Fatalf("bump latest_version: %v", err)
	}

	// Create one more — should land snapshot_no = cap+1, then prune
	// the oldest non-init (snapshot_no=2) so total stays at cap.
	if _, err := CreateCanvasVersion(context.Background(), db, 42, uint64(created.Id), CreateVersionInput{
		Document: model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "elements": []interface{}{}, "viewport": map[string]interface{}{"x": 0, "y": 0, "scale": 1}, "selectedIds": []string{}},
		Source:   "web",
		Message:  "after-cap",
	}); err != nil {
		t.Fatalf("CreateCanvasVersion (post-cap): %v", err)
	}

	var total int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_snapshot WHERE project_id = ?`, created.Id).Scan(&total).Error; err != nil {
		t.Fatalf("count after prune: %v", err)
	}
	if total != int64(MaxSnapshotsPerProject) {
		t.Errorf("snapshot count after prune = %d, want %d", total, MaxSnapshotsPerProject)
	}

	// Init snapshot (snapshot_no=1) must survive.
	var initCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_snapshot WHERE project_id = ? AND snapshot_no = 1`, created.Id).Scan(&initCount).Error; err != nil {
		t.Fatalf("count init: %v", err)
	}
	if initCount != 1 {
		t.Errorf("init snapshot count = %d, want 1 (prune must preserve snapshot_no=1)", initCount)
	}

	// The dropped snapshot is the oldest non-init: snapshot_no=2.
	var droppedExists int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_snapshot WHERE project_id = ? AND snapshot_no = 2`, created.Id).Scan(&droppedExists).Error; err != nil {
		t.Fatalf("count snapshot_no=2: %v", err)
	}
	if droppedExists != 0 {
		t.Errorf("snapshot_no=2 still exists, want it pruned (oldest non-init)")
	}

	// The newest snapshot is the one we just inserted.
	var maxNo int64
	if err := db.Raw(`SELECT MAX(snapshot_no) FROM w_canvas_snapshot WHERE project_id = ?`, created.Id).Scan(&maxNo).Error; err != nil {
		t.Fatalf("max snapshot_no: %v", err)
	}
	if maxNo != int64(MaxSnapshotsPerProject+1) {
		t.Errorf("max snapshot_no = %d, want %d (the new write must land at cap+1 even though total==cap)", maxNo, MaxSnapshotsPerProject+1)
	}
}

// TestDeleteProject_CascadesTaskBindings pins B2: outstanding task
// bindings (w_canvas_task_binding) must be removed in the same TX as
// the project soft-delete. Otherwise useCanvasTaskRecovery rehydrates
// pollers against ghost projects after page refresh — UI bug + slow
// orphan-row growth in the bindings table.
func TestDeleteProject_CascadesTaskBindings(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	for i := 0; i < 3; i++ {
		if err := db.Exec(
			`INSERT INTO w_canvas_task_binding (uid, project_id, task_id, element_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			42, created.Id, fmt.Sprintf("task-%d", i), fmt.Sprintf("el-%d", i), now, now,
		).Error; err != nil {
			t.Fatalf("seed binding %d: %v", i, err)
		}
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_task_binding (uid, project_id, task_id, element_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		77, created.Id, "task-collaborator", "el-collaborator", now, now,
	).Error; err != nil {
		t.Fatalf("seed collaborator binding: %v", err)
	}
	// Foreign-project binding (proves the WHERE scope respects project_id).
	if err := db.Exec(
		`INSERT INTO w_canvas_task_binding (uid, project_id, task_id, element_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		42, 9999, "task-foreign", "", now, now,
	).Error; err != nil {
		t.Fatalf("seed foreign binding: %v", err)
	}

	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var ownCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_task_binding WHERE project_id = ?`, created.Id).Scan(&ownCount).Error; err != nil {
		t.Fatalf("count own bindings: %v", err)
	}
	if ownCount != 0 {
		t.Errorf("project bindings after delete = %d, want 0 (cascade missed)", ownCount)
	}
	var foreignCount int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_canvas_task_binding WHERE project_id = 9999`).Scan(&foreignCount).Error; err != nil {
		t.Fatalf("count foreign bindings: %v", err)
	}
	if foreignCount != 1 {
		t.Errorf("foreign-project binding count = %d, want 1 (cascade over-deleted)", foreignCount)
	}
}

func TestDeleteProject_CascadesCanvasAgentThreads(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7001, 42, "thread-own", created.Id, "canvas", "Canvas thread", now, now,
	).Error; err != nil {
		t.Fatalf("seed own thread: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (id, uid, uuid, thread_id, user_text, ai_text, chat_mode, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		8001, 42, "msg-own", 7001, "generate", "done", "image", now, now,
	).Error; err != nil {
		t.Fatalf("seed own message: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7002, 42, "thread-foreign-project", 9999, "canvas", "Other canvas", now, now,
	).Error; err != nil {
		t.Fatalf("seed foreign project thread: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7003, 42, "thread-other-agent", created.Id, "general_agent", "General", now, now,
	).Error; err != nil {
		t.Fatalf("seed non-canvas thread: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7004, 77, "thread-collaborator", created.Id, "canvas", "Collaborator canvas", now, now,
	).Error; err != nil {
		t.Fatalf("seed collaborator canvas thread: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (id, uid, uuid, thread_id, user_text, ai_text, chat_mode, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		8004, 77, "msg-collaborator", 7004, "generate", "done", "image", now, now,
	).Error; err != nil {
		t.Fatalf("seed collaborator message: %v", err)
	}

	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var ownThreads int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_thread WHERE id IN (7001, 7004)`).Scan(&ownThreads).Error; err != nil {
		t.Fatalf("count own canvas thread: %v", err)
	}
	if ownThreads != 0 {
		t.Errorf("project canvas thread count = %d, want 0", ownThreads)
	}
	var ownMessages int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message WHERE thread_id IN (7001, 7004)`).Scan(&ownMessages).Error; err != nil {
		t.Fatalf("count own canvas messages: %v", err)
	}
	if ownMessages != 0 {
		t.Errorf("project canvas message count = %d, want 0", ownMessages)
	}
	var preservedThreads int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_thread WHERE id IN (7002, 7003)`).Scan(&preservedThreads).Error; err != nil {
		t.Fatalf("count preserved threads: %v", err)
	}
	if preservedThreads != 2 {
		t.Errorf("preserved thread count = %d, want 2", preservedThreads)
	}
}

func TestDeleteProject_CascadesCanvasAssetLedgerForAllProjectUsers(t *testing.T) {
	db := testutil.NewTestDB(t)

	created, err := CreateProject(context.Background(), db, 42, CreateProjectInput{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	rows := []struct {
		uid       int
		source    string
		sourceID  int
		projectID uint
	}{
		{uid: 42, source: "canvas", sourceID: 1, projectID: created.Id},
		{uid: 77, source: "canvas", sourceID: 2, projectID: created.Id},
		{uid: 42, source: "canvas_project_thumbnail", sourceID: 3, projectID: created.Id},
		{uid: 77, source: "canvas_snapshot_thumbnail", sourceID: 4, projectID: created.Id},
		{uid: 77, source: "thread_upload", sourceID: 5, projectID: created.Id},
		{uid: 42, source: "canvas", sourceID: 6, projectID: 9999},
	}
	for _, row := range rows {
		if err := db.Exec(
			`INSERT INTO w_user_asset_ledger (uid, source, source_id, project_id, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			row.uid, row.source, row.sourceID, row.projectID, row.source, now, now,
		).Error; err != nil {
			t.Fatalf("seed ledger row %+v: %v", row, err)
		}
	}

	if err := DeleteProject(context.Background(), db, 42, created.Id); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var projectCanvasRows int64
	if err := db.Raw(
		`SELECT COUNT(*) FROM w_user_asset_ledger WHERE project_id = ? AND source IN ('canvas', 'canvas_project_thumbnail', 'canvas_snapshot_thumbnail')`,
		created.Id,
	).Scan(&projectCanvasRows).Error; err != nil {
		t.Fatalf("count project canvas ledger rows: %v", err)
	}
	if projectCanvasRows != 0 {
		t.Errorf("project canvas ledger rows after delete = %d, want 0", projectCanvasRows)
	}

	var preservedRows int64
	if err := db.Raw(`SELECT COUNT(*) FROM w_user_asset_ledger`).Scan(&preservedRows).Error; err != nil {
		t.Fatalf("count preserved ledger rows: %v", err)
	}
	if preservedRows != 2 {
		t.Errorf("preserved ledger rows = %d, want 2", preservedRows)
	}
}
