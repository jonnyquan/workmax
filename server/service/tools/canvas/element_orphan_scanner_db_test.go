// element_orphan_scanner_db_test.go — DB-coupled regression tests for
// ListOrphanElementAssetsByProject + ScanOrphanElementAssets. Uses the
// shared in-memory SQLite from testutil so the per-project diff and
// the cross-project aggregator both exercise the real GORM path.

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

func seedActiveProject(t *testing.T, db *gorm.DB, id uint, title string, doc model.JSONMap) {
	t.Helper()
	docBytes, err := stdjson.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, document, schema_version, element_count, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, 1, fmt.Sprintf("uuid-%d", id), title, string(docBytes), SchemaVersionV2, 0,
		time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed project %d: %v", id, err)
	}
}

func seedDeletedProject(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, document, schema_version, element_count, deleted_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, 1, fmt.Sprintf("uuid-%d", id), "deleted", "{}", SchemaVersionV2, 0,
		time.Now(), time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed deleted project %d: %v", id, err)
	}
}

func seedCanvasProjectFile(t *testing.T, db *gorm.DB, assetID, projectID uint, url string) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_global_asset
			(id, uid, project_id, uuid, kind, source, source_table, source_id, source_item_key, url, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assetID, 1, projectID, fmt.Sprintf("a-%d", assetID), "image", "upload",
		"canvas_project_file", uint64(projectID), fmt.Sprintf("k-%d", assetID), url,
		model.GlobalAssetStatusActive, time.Now(), time.Now(),
	).Error; err != nil {
		t.Fatalf("seed asset %d: %v", assetID, err)
	}
}

// docWithRefs builds a tiny canvas document that references the given
// global asset ids via element.metadata.globalAssetId.
func docWithRefs(ids ...uint) model.JSONMap {
	elements := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		elements = append(elements, map[string]interface{}{
			"id":       fmt.Sprintf("el-%d", id),
			"metadata": map[string]interface{}{"globalAssetId": float64(id)},
		})
	}
	return model.JSONMap{"elements": elements}
}

func TestListOrphanElementAssetsByProject_MissingProjectReturnsSentinel(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := ListOrphanElementAssetsByProject(context.Background(), db, 9999, 10)
	if !errors.Is(err, ErrCanvasProjectNotFound) {
		t.Fatalf("missing project: err = %v, want ErrCanvasProjectNotFound", err)
	}
}

func TestListOrphanElementAssetsByProject_ZeroWhenAllReferenced(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedActiveProject(t, db, 1, "all-referenced", docWithRefs(10, 11, 12))
	seedCanvasProjectFile(t, db, 10, 1, "/uploads/a.png")
	seedCanvasProjectFile(t, db, 11, 1, "/uploads/b.png")
	seedCanvasProjectFile(t, db, 12, 1, "/uploads/c.png")

	summary, err := ListOrphanElementAssetsByProject(context.Background(), db, 1, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Total != 0 {
		t.Errorf("expected 0 orphans, got %d (ids=%v)", summary.Total, summary.AssetIDs)
	}
}

func TestListOrphanElementAssetsByProject_DetectsUnreferencedAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	// 5 assets bound to project 1, only 2 referenced. Assets 30+31+32
	// should land in the orphan list.
	seedActiveProject(t, db, 1, "partial", docWithRefs(20, 21))
	for i := uint(20); i <= 24; i++ {
		seedCanvasProjectFile(t, db, i, 1, fmt.Sprintf("/uploads/img-%d.png", i))
	}

	summary, err := ListOrphanElementAssetsByProject(context.Background(), db, 1, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Total != 3 {
		t.Fatalf("expected 3 orphans, got %d (ids=%v)", summary.Total, summary.AssetIDs)
	}
	want := map[uint]struct{}{22: {}, 23: {}, 24: {}}
	for _, id := range summary.AssetIDs {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected orphan id %d in sample", id)
		}
	}
}

func TestListOrphanElementAssetsByProject_URLFallbackKeepsAssetLive(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Asset 40 is referenced only by URL (no globalAssetId in
	// metadata). Conservatism: must NOT be flagged as orphan.
	seedActiveProject(t, db, 1, "url-only", model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "el-1", "src": "/uploads/x.png"},
		},
	})
	seedCanvasProjectFile(t, db, 40, 1, "/uploads/x.png")

	summary, err := ListOrphanElementAssetsByProject(context.Background(), db, 1, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Total != 0 {
		t.Fatalf("URL-referenced asset must not be flagged, got Total=%d ids=%v", summary.Total, summary.AssetIDs)
	}
}

func TestListOrphanElementAssetsByProject_SkipsTombstoned(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedActiveProject(t, db, 1, "with-tombstoned", docWithRefs( /* no refs */ ))
	seedCanvasProjectFile(t, db, 50, 1, "/uploads/x.png")
	// Tombstone asset 50.
	if err := db.Exec(
		`UPDATE w_global_asset SET status = ?, deleted_at = ? WHERE id = ?`,
		model.GlobalAssetStatusDeleted, time.Now(), 50,
	).Error; err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	summary, err := ListOrphanElementAssetsByProject(context.Background(), db, 1, 10)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if summary.Total != 0 {
		t.Fatalf("tombstoned asset must be skipped, got Total=%d", summary.Total)
	}
}

func TestScanOrphanElementAssets_AggregatesAcrossProjects(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Project 1: 5 assets, 2 referenced → 3 orphans
	seedActiveProject(t, db, 1, "project-1", docWithRefs(60, 61))
	for i := uint(60); i <= 64; i++ {
		seedCanvasProjectFile(t, db, i, 1, fmt.Sprintf("/u/%d.png", i))
	}
	// Project 2: 3 assets, all referenced → 0 orphans
	seedActiveProject(t, db, 2, "project-2", docWithRefs(70, 71, 72))
	for i := uint(70); i <= 72; i++ {
		seedCanvasProjectFile(t, db, i, 2, fmt.Sprintf("/u/%d.png", i))
	}
	// Project 3: 10 assets, none referenced → 10 orphans (top offender)
	seedActiveProject(t, db, 3, "project-3", model.JSONMap{"elements": []interface{}{}})
	for i := uint(80); i <= 89; i++ {
		seedCanvasProjectFile(t, db, i, 3, fmt.Sprintf("/u/%d.png", i))
	}
	// Deleted project — must not enter the scan.
	seedDeletedProject(t, db, 99)

	report, err := ScanOrphanElementAssets(context.Background(), db, ScanOrphanElementAssetsOpts{TopK: 2, PageSize: 10})
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if report.ProjectsScanned != 3 {
		t.Fatalf("ProjectsScanned: want 3, got %d", report.ProjectsScanned)
	}
	if report.OrphansFound != 13 {
		t.Fatalf("OrphansFound: want 13 (3+0+10), got %d", report.OrphansFound)
	}
	if report.Distribution.Max != 10 {
		t.Errorf("Distribution.Max: want 10, got %d", report.Distribution.Max)
	}
	if report.Distribution.Min != 0 {
		t.Errorf("Distribution.Min: want 0, got %d", report.Distribution.Min)
	}
	if len(report.TopProjects) != 2 {
		t.Fatalf("TopProjects len: want 2 (k=2), got %d", len(report.TopProjects))
	}
	if report.TopProjects[0].ProjectID != 3 || report.TopProjects[0].OrphanCount != 10 {
		t.Errorf("top[0]: want {project=3, count=10}, got %+v", report.TopProjects[0])
	}
	if report.TopProjects[1].ProjectID != 1 || report.TopProjects[1].OrphanCount != 3 {
		t.Errorf("top[1]: want {project=1, count=3}, got %+v", report.TopProjects[1])
	}
}

