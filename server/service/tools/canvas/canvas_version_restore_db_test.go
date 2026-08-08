// DB-coupled regression tests for RestoreCanvasSnapshot. Seeds rows
// directly via SQL (mirrors canvas_project_service_db_test.go's
// pattern) so the test doesn't drag in the asset-ledger schema.
//
// Coverage matrix:
//   1.  Happy path — restore v2 produces audit snapshot v3 with
//       source="restored", message="Restored from v2"
//   2.  Source out of [1, latest] range → ErrCanvasVersionNotFound
//   3.  Restore the current latest — still creates an audit row
//   4.  Restore the init snapshot (v=1)
//   5.  ExpectedProjectUpdatedAt mismatch → ErrCanvasVersionConflict
//   6.  Dangling refs, policy=abort → ErrCanvasRestoreDanglingAssets
//       with the dangling list populated on the result
//   7.  Dangling refs, policy=allow → success + list in result
//   8.  Non-owner uid → ErrCanvasProjectNotFound (access-deny path)
//   9.  Source snapshot byte-equal after restore (immutable invariant)
//   10. project.document mirrors source.Document after restore

package canvas

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// seedRestoreProject writes the project row + one or more snapshots.
// docs maps snapshot_no → JSON document. latestVersion is the
// project's latest_version (clients set this independently of
// max(snapshot_no) so the test can probe the validation paths).
func seedRestoreProject(t *testing.T, db *gorm.DB, uid int, projectID uint, latestVersion int, docs map[int]model.JSONMap) {
	t.Helper()
	now := time.Now().UTC()
	// Use the latest snapshot's document for the project row's
	// document column — mirrors what writeLatestVersionDocument
	// would have left.
	var liveDoc model.JSONMap
	if d, ok := docs[latestVersion]; ok {
		liveDoc = d
	} else {
		liveDoc = model.JSONMap{"schemaVersion": 2, "elements": []interface{}{}, "viewport": map[string]interface{}{"x": 0, "y": 0, "scale": 1}}
	}
	liveBytes, _ := stdjson.Marshal(liveDoc)
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (?, ?, ?, 'restore-test', 0, '', ?, 2, 0, ?, ?, ?)`,
		projectID, uid, "uuid-restore", string(liveBytes), latestVersion, now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	id := uint(0)
	for snapNo, doc := range docs {
		id++
		docBytes, _ := stdjson.Marshal(doc)
		if err := db.Exec(
			`INSERT INTO w_canvas_snapshot (id, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url, document, schema_version, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, 2, ?)`,
			projectID*100+uint(snapNo), projectID, snapNo, uid,
			"manual", fmt.Sprintf("snapshot %d", snapNo),
			elementCountFromDoc(doc), string(docBytes), now,
		).Error; err != nil {
			t.Fatalf("seed snapshot %d: %v", snapNo, err)
		}
	}
}

// elementCountFromDoc avoids importing ElementCountFromDocument
// signature mismatch noise in the helper.
func elementCountFromDoc(doc model.JSONMap) int { return ElementCountFromDocument(doc) }

// docWithMetadataRef builds a tiny v2 document referencing the given
// asset ids via element.metadata.globalAssetId — the primary channel
// the C-06 walker resolves. Each element carries a minimal `type` so
// the NormalizeCanvasDocumentForStorage gate (which RestoreCanvasSnapshot
// re-runs defensively on source.Document) accepts it.
func docWithMetadataRef(ids ...uint) model.JSONMap {
	elements := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		elements = append(elements, map[string]interface{}{
			"id":       fmt.Sprintf("el-%d", id),
			"type":     "image",
			"x":        0.0,
			"y":        0.0,
			"width":    100.0,
			"height":   100.0,
			"metadata": map[string]interface{}{"globalAssetId": float64(id)},
		})
	}
	return model.JSONMap{
		"schemaVersion": 2,
		"viewMode":      "freeform",
		"elements":      elements,
		"viewport":      map[string]interface{}{"x": 0, "y": 0, "scale": 1},
	}
}

func TestRestoreCanvasSnapshot_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),   // init, empty
		2: docWithMetadataRef(10), // current latest
	})
	// Seed asset 10 as live so the dangling check passes.
	seedCanvasProjectFile(t, db, 10, 1, "/uploads/a.png")

	// Add a third snapshot reference (10 still live) — we'll restore v1
	// (the init). Restore should produce a new snapshot v3 with
	// source="restored", message="Restored from v1".
	result, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 1, RestoreSnapshotInput{})
	if err != nil {
		t.Fatalf("RestoreCanvasSnapshot: %v", err)
	}
	if result.RestoredFrom != 1 {
		t.Errorf("RestoredFrom = %d, want 1", result.RestoredFrom)
	}
	if result.Project.LatestVersion != 3 {
		t.Errorf("project.LatestVersion = %d, want 3 (audit snapshot at LatestVersion+1)", result.Project.LatestVersion)
	}
	if result.NewSnapshot.Source != "restored" {
		t.Errorf("audit source = %q, want %q", result.NewSnapshot.Source, "restored")
	}
	if result.NewSnapshot.Message != "Restored from v1" {
		t.Errorf("audit message = %q, want %q", result.NewSnapshot.Message, "Restored from v1")
	}
	if result.NewSnapshot.Version != 3 {
		t.Errorf("audit snapshot_no = %d, want 3", result.NewSnapshot.Version)
	}
}

func TestRestoreCanvasSnapshot_SourceOutOfRange(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(),
	})
	cases := []int{0, -1, 3, 999}
	for _, v := range cases {
		_, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, v, RestoreSnapshotInput{})
		if !errors.Is(err, ErrCanvasVersionNotFound) {
			t.Errorf("source=%d: err = %v, want ErrCanvasVersionNotFound", v, err)
		}
	}
}

func TestRestoreCanvasSnapshot_RestoreLatestStillCreatesAuditRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(20),
	})
	seedCanvasProjectFile(t, db, 20, 1, "/uploads/b.png")

	result, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 2, RestoreSnapshotInput{})
	if err != nil {
		t.Fatalf("RestoreCanvasSnapshot: %v", err)
	}
	if result.NewSnapshot.Version != 3 {
		t.Errorf("audit snapshot_no = %d, want 3 (latest_version still bumps)", result.NewSnapshot.Version)
	}
	if result.NewSnapshot.Message != "Restored from v2" {
		t.Errorf("audit message = %q, want %q", result.NewSnapshot.Message, "Restored from v2")
	}
}

func TestRestoreCanvasSnapshot_RestoreInitSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 3, map[int]model.JSONMap{
		1: docWithMetadataRef(),     // init, empty
		2: docWithMetadataRef(20),
		3: docWithMetadataRef(20, 21),
	})
	seedCanvasProjectFile(t, db, 20, 1, "/uploads/b.png")
	seedCanvasProjectFile(t, db, 21, 1, "/uploads/c.png")

	result, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 1, RestoreSnapshotInput{})
	if err != nil {
		t.Fatalf("RestoreCanvasSnapshot init: %v", err)
	}
	if result.NewSnapshot.Version != 4 {
		t.Errorf("audit snapshot_no = %d, want 4", result.NewSnapshot.Version)
	}
	if got := result.NewSnapshot.ElementCount; got != 0 {
		t.Errorf("restored elementCount = %d, want 0 (init is empty)", got)
	}
}

func TestRestoreCanvasSnapshot_OptimisticLockMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(),
	})
	// Pretend the client observed an updated_at from way in the past;
	// the row's updated_at (just now) is far newer → conflict.
	stale := time.Now().Add(-1 * time.Hour)
	_, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 1, RestoreSnapshotInput{
		ExpectedProjectUpdatedAt: &stale,
	})
	if !errors.Is(err, ErrCanvasVersionConflict) {
		t.Fatalf("err = %v, want ErrCanvasVersionConflict", err)
	}
}

func TestRestoreCanvasSnapshot_DanglingAbort(t *testing.T) {
	db := testutil.NewTestDB(t)
	// v2 references asset 30 and asset 31; we'll seed only 30 as live.
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(30, 31),
	})
	seedCanvasProjectFile(t, db, 30, 1, "/uploads/d.png")
	// 31 deliberately not seeded → dangling.

	result, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 2, RestoreSnapshotInput{
		DanglingAssets: DanglingAssetsAbort,
	})
	if !errors.Is(err, ErrCanvasRestoreDanglingAssets) {
		t.Fatalf("err = %v, want ErrCanvasRestoreDanglingAssets", err)
	}
	if result.RestoredFrom != 2 {
		t.Errorf("RestoredFrom = %d, want 2", result.RestoredFrom)
	}
	if len(result.DanglingAssets) != 1 || result.DanglingAssets[0].GlobalAssetID != 31 {
		t.Errorf("DanglingAssets = %+v, want [{GlobalAssetID:31}]", result.DanglingAssets)
	}
	// Verify the project is unchanged — abort rolled back.
	var post model.CanvasProject
	if err := db.Raw(`SELECT latest_version FROM w_global_project WHERE id = 1`).Scan(&post).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if post.LatestVersion != 2 {
		t.Errorf("abort rollback failed: project.LatestVersion = %d, want 2", post.LatestVersion)
	}
}

func TestRestoreCanvasSnapshot_DanglingAllow(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(30, 31),
	})
	seedCanvasProjectFile(t, db, 30, 1, "/uploads/d.png")

	result, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 2, RestoreSnapshotInput{
		DanglingAssets: DanglingAssetsAllow,
	})
	if err != nil {
		t.Fatalf("allow path err = %v, want nil", err)
	}
	if len(result.DanglingAssets) != 1 || result.DanglingAssets[0].GlobalAssetID != 31 {
		t.Errorf("DanglingAssets = %+v, want [{GlobalAssetID:31}]", result.DanglingAssets)
	}
	if result.NewSnapshot.Version != 3 {
		t.Errorf("restore proceeded: NewSnapshot.Version = %d, want 3", result.NewSnapshot.Version)
	}
}

func TestRestoreCanvasSnapshot_NonOwnerDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: docWithMetadataRef(),
		2: docWithMetadataRef(),
	})
	// uid=99 is not the owner and not in w_global_project_member.
	_, err := RestoreCanvasSnapshot(context.Background(), db, 99, 1, 1, RestoreSnapshotInput{})
	if !errors.Is(err, ErrCanvasProjectNotFound) {
		t.Fatalf("non-owner err = %v, want ErrCanvasProjectNotFound", err)
	}
}

func TestRestoreCanvasSnapshot_SourceSnapshotIntact(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Pin source snapshot's exact document JSON; after restore, the
	// SAME row must still serve the SAME bytes (immutable invariant).
	sourceDoc := docWithMetadataRef(40)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: sourceDoc,
		2: docWithMetadataRef(40, 41),
	})
	seedCanvasProjectFile(t, db, 40, 1, "/uploads/e.png")
	seedCanvasProjectFile(t, db, 41, 1, "/uploads/f.png")

	wantBytes, _ := stdjson.Marshal(sourceDoc)
	if _, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 1, RestoreSnapshotInput{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var got model.CanvasSnapshot
	if err := db.Raw(`SELECT id, document FROM w_canvas_snapshot WHERE project_id = 1 AND snapshot_no = 1`).
		Scan(&got).Error; err != nil {
		t.Fatalf("read source snapshot back: %v", err)
	}
	gotBytes, _ := stdjson.Marshal(got.Document)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("source snapshot mutated by restore:\n want %s\n  got %s", wantBytes, gotBytes)
	}
}

func TestRestoreCanvasSnapshot_ProjectMirrorsSourceDocument(t *testing.T) {
	db := testutil.NewTestDB(t)
	sourceDoc := docWithMetadataRef(50)
	seedRestoreProject(t, db, 42, 1, 2, map[int]model.JSONMap{
		1: sourceDoc,
		2: docWithMetadataRef(50, 51),
	})
	seedCanvasProjectFile(t, db, 50, 1, "/uploads/g.png")
	seedCanvasProjectFile(t, db, 51, 1, "/uploads/h.png")

	if _, err := RestoreCanvasSnapshot(context.Background(), db, 42, 1, 1, RestoreSnapshotInput{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var post model.CanvasProject
	if err := db.Raw(`SELECT id, document, latest_version, element_count FROM w_global_project WHERE id = 1`).
		Scan(&post).Error; err != nil {
		t.Fatalf("read project: %v", err)
	}
	if post.LatestVersion != 3 {
		t.Errorf("project.LatestVersion = %d, want 3", post.LatestVersion)
	}
	if post.ElementCount != 1 {
		t.Errorf("project.ElementCount = %d, want 1 (sourceDoc has one element)", post.ElementCount)
	}
	wantBytes, _ := stdjson.Marshal(sourceDoc)
	gotBytes, _ := stdjson.Marshal(post.Document)
	if string(gotBytes) != string(wantBytes) {
		t.Fatalf("project document not updated to source:\n want %s\n  got %s", wantBytes, gotBytes)
	}
}
