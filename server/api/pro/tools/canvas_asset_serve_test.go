package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"
)

// canvas_asset_serve_test.go pins the access predicate that gates
// /uploads/canvas/uid/{uid}/{projectUuid}/{filename} byte fetches.
// Before the gate, Router.Static("/uploads", "./uploads") served every
// per-user asset to anyone who could guess (or list) the URL.

func TestCanvasAssetAccessAllowed_OwnerSkipsDBLookup(t *testing.T) {
	// Owner uid match short-circuits the DB lookup. The DB isn't even
	// dialed — pass nil and verify the call still returns true.
	got := canvasAssetAccessAllowed(context.Background(), nil, 42, "canvas/uid/42/uuid-private/file.png")
	if !got {
		t.Errorf("owner uid match should be allowed without a DB hit")
	}
}

func TestCanvasAssetAccessAllowed_AnonymousPrivateProjectDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Visibility 0 = private. Even with the project in the DB, a non-
	// owner request must be rejected — this is the bug the gate fixes.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "viewport": map[string]interface{}{"x": 0.0, "y": 0.0, "scale": 1.0}, "selectedIds": []string{}, "elements": []interface{}{
		map[string]interface{}{"id": "img-1", "type": "image", "src": "/uploads/generations/uid/42/out.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-private', 'p', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	if canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/42/uuid-private/file.png") {
		t.Errorf("anonymous request to a private project must be denied")
	}
	if canvasAssetAccessAllowed(context.Background(), db, 99, "canvas/uid/42/uuid-private/file.png") {
		t.Errorf("non-owner authed user must be denied for a private project")
	}
}

func TestCanvasAssetAccessAllowed_ProjectMemberCanReadPrivateProjectAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-private-member', 'p', 0, '', '{}', 1, 0, 1, ?, ?)`,
		now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: 1,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	for id, seed := range []struct {
		uid int
		url string
	}{
		{uid: 42, url: "/uploads/canvas/uid/42/uuid-private-member/owner.png"},
		{uid: 99, url: "/uploads/canvas/uid/99/uuid-private-member/editor.png"},
	} {
		projectID := uint(1)
		if err := db.Create(&model.GlobalAsset{
			UID:           seed.uid,
			ProjectID:     &projectID,
			UUID:          "private-member-asset-" + string(rune('a'+id)),
			Kind:          model.GlobalAssetKindImage,
			Source:        model.GlobalAssetSourceUpload,
			SourceTable:   "canvas_project_file",
			SourceID:      uint64(projectID),
			SourceItemKey: "private-member-" + string(rune('a'+id)),
			URL:           seed.url,
			ThumbURL:      seed.url,
			MimeType:      "image/png",
			SizeBytes:     10,
			Width:         1,
			Height:        1,
			Status:        model.GlobalAssetStatusActive,
			Visibility:    model.GlobalAssetVisibilityProject,
			VariantType:   model.GlobalAssetVariantOriginal,
		}).Error; err != nil {
			t.Fatalf("seed global asset %d: %v", id, err)
		}
	}

	if !canvasAssetAccessAllowed(context.Background(), db, 99, "canvas/uid/42/uuid-private-member/owner.png") {
		t.Errorf("project viewer should read owner-uploaded project asset")
	}
	if !canvasAssetAccessAllowed(context.Background(), db, 42, "canvas/uid/99/uuid-private-member/editor.png") {
		t.Errorf("project owner should read editor-uploaded project asset")
	}
	if canvasAssetAccessAllowed(context.Background(), db, 100, "canvas/uid/42/uuid-private-member/owner.png") {
		t.Errorf("non-member should not read private project asset")
	}
	if canvasAssetAccessAllowed(context.Background(), db, 99, "canvas/uid/42/uuid-private-member/unbound.png") {
		t.Errorf("project member should not read unbound private project path")
	}
}

func TestCanvasAssetAccessAllowed_AnonymousUnlistedProjectAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "viewport": map[string]interface{}{"x": 0.0, "y": 0.0, "scale": 1.0}, "selectedIds": []string{}, "elements": []interface{}{
		map[string]interface{}{"id": "img-1", "type": "image", "src": "/uploads/canvas/uid/42/uuid-shared/file.png"},
	}})
	// Visibility 1 = unlisted (share-link readable). 2 = public. Both
	// require a w_canvas_share_snapshot row — published projects always
	// have one (upsertCanvasShareSnapshot fires inside UpdateProject
	// and the publish path), so seed it alongside the project.
	for _, v := range []int8{1, 2} {
		if err := db.Exec(`DELETE FROM w_global_project WHERE id = 1`).Error; err != nil {
			t.Fatalf("clean: %v", err)
		}
		if err := db.Exec(`DELETE FROM w_canvas_share_snapshot WHERE project_id = 1`).Error; err != nil {
			t.Fatalf("clean snap: %v", err)
		}
		if err := db.Exec(
			`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
			 VALUES (1, 42, 'uuid-shared', 'p', ?, '', ?, 1, 0, 1, ?, ?)`,
			v, string(docBytes), now, now,
		).Error; err != nil {
			t.Fatalf("seed visibility=%d: %v", v, err)
		}
		if err := db.Exec(
			`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
			 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
			string(docBytes), now, now,
		).Error; err != nil {
			t.Fatalf("seed share snap visibility=%d: %v", v, err)
		}
		if !canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/42/uuid-shared/file.png") {
			t.Errorf("visibility=%d: anonymous read must be allowed (share-link UX)", v)
		}
		if !canvasAssetAccessAllowed(context.Background(), db, 99, "canvas/uid/42/uuid-shared/file.png") {
			t.Errorf("visibility=%d: non-owner authed read must be allowed", v)
		}
	}
}

func TestCanvasAssetAccessAllowed_PathUIDMustMatchProjectOwner(t *testing.T) {
	db := testutil.NewTestDB(t)
	// A maliciously-crafted URL that pairs another user's uid (the path
	// segment) with an unrelated public project's uuid must be refused
	// — even though GetSharedProjectByUUID would happily resolve. This
	// is the defense-in-depth check that stops a path-walking attack.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "viewport": map[string]interface{}{"x": 0.0, "y": 0.0, "scale": 1.0}, "selectedIds": []string{}, "elements": []interface{}{
		map[string]interface{}{"id": "img-1", "type": "image", "src": "/uploads/generations/uid/42/out.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-public', 'p', 2, '', ?, 1, 0, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed snap: %v", err)
	}

	// Path uid=99 doesn't match project.UID=42 — even though the project
	// is publicly visible, refuse to serve.
	if canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/99/uuid-public/file.png") {
		t.Errorf("path uid mismatch must be denied even for public projects")
	}
}

func TestCanvasAssetAccessAllowed_MalformedPathRejected(t *testing.T) {
	cases := []string{
		"canvas/uid/abc/uuid/file.png", // non-numeric uid
		"canvas/uid/-1/uuid/file.png",  // negative uid
		"canvas/uid/0/uuid/file.png",   // zero uid
		"canvas/uid/42//file.png",      // empty projectUuid
		"canvas/uid/42",                // truncated path
		"canvas/uid/42/uuid",           // missing filename
	}
	for _, c := range cases {
		if canvasAssetAccessAllowed(context.Background(), nil, 0, c) {
			t.Errorf("path %q must be rejected as malformed", c)
		}
	}
}

func TestCanvasAssetAccessAllowed_ProjectNotFoundDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Empty DB. A non-owner asking for any project's bytes must be denied
	// — not 500'd. (errors.Is(ErrCanvasProjectNotFound) path.)
	if canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/42/missing-uuid/file.png") {
		t.Errorf("non-existent project must be denied (not 500'd)")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_PrivateCanvasBindingDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "viewport": map[string]interface{}{"x": 0.0, "y": 0.0, "scale": 1.0}, "selectedIds": []string{}, "elements": []interface{}{
		map[string]interface{}{"id": "img-1", "type": "image", "src": "/uploads/generations/uid/42/out.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-private', 'p', 0, '', ?, 1, 0, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	projectID := uint(1)
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		ProjectID:     &projectID,
		UUID:          "private-bound-ref",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      uint64(projectID),
		SourceItemKey: "private-ref",
		URL:           "/uploads/references/uid/42/ref.png",
		MimeType:      "image/png",
		SizeBytes:     10,
		Width:         1,
		Height:        1,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("seed global asset: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: 1,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}

	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 42, "references/uid/42/ref.png") {
		t.Errorf("owner must be allowed for bound managed upload")
	}
	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 99, "references/uid/42/ref.png") {
		t.Errorf("project member must be allowed for private canvas-bound managed upload")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 100, "references/uid/42/ref.png") {
		t.Errorf("non-member must be denied for private canvas-bound managed upload")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "references/uid/42/ref.png") {
		t.Errorf("anonymous must be denied for private canvas-bound managed upload")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_PublicCanvasBindingAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	docBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "viewMode": "freeform", "viewport": map[string]interface{}{"x": 0.0, "y": 0.0, "scale": 1.0}, "selectedIds": []string{}, "elements": []interface{}{
		map[string]interface{}{"id": "img-1", "type": "image", "src": "/uploads/generations/uid/42/out.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-public', 'p', 2, '', ?, 1, 0, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
		string(docBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed share snapshot: %v", err)
	}
	projectID := uint(1)
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		ProjectID:     &projectID,
		UUID:          "asset-public-binding",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      1,
		SourceItemKey: "public-binding",
		URL:           "/uploads/generations/uid/42/out.png",
		MimeType:      "image/png",
		SizeBytes:     10,
		Width:         1,
		Height:        1,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata:      model.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "generations/uid/42/out.png") {
		t.Errorf("anonymous should be allowed for published canvas-bound managed upload")
	}
	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 99, "generations/uid/42/out.png") {
		t.Errorf("non-owner should be allowed for published canvas-bound managed upload")
	}
}

func TestCanvasAssetAccessAllowed_PublicProjectDraftAssetDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	liveDocBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "elements": []interface{}{
		map[string]interface{}{"id": "draft", "type": "image", "src": "/uploads/canvas/uid/42/uuid-public/draft.png"},
	}})
	publishedDocBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "elements": []interface{}{
		map[string]interface{}{"id": "published", "type": "image", "src": "/uploads/canvas/uid/42/uuid-public/published.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-public', 'p', 2, '', ?, 1, 1, 1, ?, ?)`,
		string(liveDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
		string(publishedDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed share snapshot: %v", err)
	}

	if !canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/42/uuid-public/published.png") {
		t.Errorf("published asset should be readable")
	}
	if canvasAssetAccessAllowed(context.Background(), db, 0, "canvas/uid/42/uuid-public/draft.png") {
		t.Errorf("draft-only asset must not be readable through a published project")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_PublicProjectDraftAssetDenied(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	publishedDocBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "elements": []interface{}{
		map[string]interface{}{"id": "published", "type": "image", "src": "/uploads/generations/uid/42/published.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-public', 'p', 2, '', ?, 1, 1, 1, ?, ?)`,
		string(publishedDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
		string(publishedDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed share snapshot: %v", err)
	}
	for id, url := range map[int]string{
		1: "/uploads/generations/uid/42/published.png",
		2: "/uploads/generations/uid/42/draft.png",
	} {
		projectID := uint(1)
		if err := db.Create(&model.GlobalAsset{
			UID:           42,
			ProjectID:     &projectID,
			UUID:          "public-bound-asset-" + string(rune('a'+id)),
			Kind:          model.GlobalAssetKindImage,
			Source:        model.GlobalAssetSourceUpload,
			SourceTable:   "canvas_project_file",
			SourceID:      uint64(projectID),
			SourceItemKey: "public-bound-" + string(rune('a'+id)),
			URL:           url,
			MimeType:      "image/png",
			SizeBytes:     10,
			Width:         1,
			Height:        1,
			Status:        model.GlobalAssetStatusActive,
			Visibility:    model.GlobalAssetVisibilityProject,
			VariantType:   model.GlobalAssetVariantOriginal,
		}).Error; err != nil {
			t.Fatalf("seed global asset %d: %v", id, err)
		}
	}
	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "generations/uid/42/published.png") {
		t.Errorf("published managed upload should be readable")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "generations/uid/42/draft.png") {
		t.Errorf("draft-only managed upload must not be readable through a published project")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_UnboundLegacyURLAllowed(t *testing.T) {
	db := testutil.NewTestDB(t)
	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "generations/uid/42/legacy.png") {
		t.Errorf("unbound legacy generation URLs should keep legacy static behavior")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_PrivateReferenceUploadDeniedToNonOwner(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := db.Exec(
		`INSERT INTO w_global_asset (id, uid, uuid, kind, source, source_table, source_id, source_item_key, url, thumb_url, mime_type, status, visibility, variant_type, metadata, created_at, updated_at)
		 VALUES (1, 42, 'private-reference-upload', 'image', 'upload', 'reference_upload', 42, 'upload-1', '/uploads/references/uid/42/private.png', '', 'image/png', 1, 0, 'original', '{}', ?, ?)`,
		now, now,
	).Error; err != nil {
		t.Fatalf("seed private reference upload: %v", err)
	}

	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 42, "references/uid/42/private.png") {
		t.Errorf("owner must be allowed for private reference upload")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 99, "references/uid/42/private.png") {
		t.Errorf("non-owner must be denied for private reference upload")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "references/uid/42/private.png") {
		t.Errorf("anonymous must be denied for private reference upload")
	}
}

func TestCanvasBoundManagedUploadAccessAllowed_GlobalAssetBinding(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	publishedDocBytes, _ := json.Marshal(model.JSONMap{"schemaVersion": 2, "elements": []interface{}{
		map[string]interface{}{"id": "published", "type": "image", "src": "/uploads/references/uid/42/published.png"},
	}})
	if err := db.Exec(
		`INSERT INTO w_global_project (id, uid, uuid, title, visibility, thumbnail_url, document, schema_version, element_count, latest_version, created_at, updated_at)
		 VALUES (1, 42, 'uuid-global-public', 'p', 2, '', ?, 1, 1, 1, ?, ?)`,
		string(publishedDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_canvas_share_snapshot (id, project_id, published_version, thumbnail_url, document, schema_version, created_at, updated_at)
		 VALUES (1, 1, 1, '', ?, 1, ?, ?)`,
		string(publishedDocBytes), now, now,
	).Error; err != nil {
		t.Fatalf("seed share snapshot: %v", err)
	}
	for _, seed := range []struct {
		id       int
		uuid     string
		itemKey  string
		urlValue string
	}{
		{id: 1, uuid: "global-asset-published", itemKey: "upload-published", urlValue: "/uploads/references/uid/42/published.png"},
		{id: 2, uuid: "global-asset-draft", itemKey: "upload-draft", urlValue: "/uploads/references/uid/42/draft.png"},
	} {
		if err := db.Exec(
			`INSERT INTO w_global_asset (id, uid, project_id, uuid, kind, source, source_table, source_id, source_item_key, url, thumb_url, mime_type, status, visibility, variant_type, metadata, created_at, updated_at)
			 VALUES (?, 42, 1, ?, 'image', 'upload', 'reference_upload', 42, ?, ?, '', 'image/png', 1, 2, 'original', '{}', ?, ?)`,
			seed.id, seed.uuid, seed.itemKey, seed.urlValue, now, now,
		).Error; err != nil {
			t.Fatalf("seed global asset %d: %v", seed.id, err)
		}
	}

	if !canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "references/uid/42/published.png") {
		t.Errorf("published global managed upload should be readable")
	}
	if canvasBoundManagedUploadAccessAllowed(context.Background(), db, 0, "references/uid/42/draft.png") {
		t.Errorf("draft-only global managed upload must not be readable")
	}
}
