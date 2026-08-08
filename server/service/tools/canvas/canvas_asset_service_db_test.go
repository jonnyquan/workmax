package canvas

import (
	"context"
	"errors"
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestListProjectFileAssets_GlobalOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(7)
	project := model.CanvasProject{
		UID:          42,
		UUID:         "project-uuid",
		Title:        "Project",
		ThumbnailURL: "",
		Document:     model.JSONMap{},
	}
	project.Id = pid
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	global := model.GlobalAsset{
		UID:           42,
		ProjectID:     &pid,
		UUID:          "asset-global",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      uint64(pid),
		SourceItemKey: "global",
		URL:           "/uploads/canvas/uid/42/project-uuid/global.png",
		ThumbURL:      "/uploads/canvas/uid/42/project-uuid/global.png",
		MimeType:      "image/png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&global).Error; err != nil {
		t.Fatalf("create global asset: %v", err)
	}

	result, err := ListProjectFileAssets(context.Background(), db, 42, pid, ListAssetsInput{Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("ListProjectFileAssets: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("total = %d, want 1", result.Total)
	}
	byURL := make(map[string]ProjectFileAsset, len(result.Items))
	for _, item := range result.Items {
		byURL[item.URL] = item
	}
	globalItem, ok := byURL[global.URL]
	if !ok {
		t.Fatalf("global asset missing from result: %#v", result.Items)
	}
	if globalItem.Id != global.Id || globalItem.GlobalAssetID != global.Id {
		t.Fatalf("global identity = id:%d global:%d, want %d/%d", globalItem.Id, globalItem.GlobalAssetID, global.Id, global.Id)
	}
}

func TestListProjectFileAssets_GlobalPaginationDoesNotRepeat(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(8)
	project := model.CanvasProject{
		UID:          42,
		UUID:         "project-uuid-2",
		Title:        "Project",
		ThumbnailURL: "",
		Document:     model.JSONMap{},
	}
	project.Id = pid
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	for i, url := range []string{
		"/uploads/canvas/uid/42/project-uuid-2/a.png",
		"/uploads/canvas/uid/42/project-uuid-2/b.png",
		"/uploads/canvas/uid/42/project-uuid-2/c.png",
	} {
		row := model.GlobalAsset{
			UID:           42,
			ProjectID:     &pid,
			UUID:          "asset-global-page-" + string(rune('a'+i)),
			Kind:          model.GlobalAssetKindImage,
			Source:        model.GlobalAssetSourceUpload,
			SourceTable:   "canvas_project_file",
			SourceID:      uint64(pid),
			SourceItemKey: "page-" + string(rune('a'+i)),
			MimeType:      "image/png",
			URL:           url,
			ThumbURL:      url,
			Status:        model.GlobalAssetStatusActive,
			Visibility:    model.GlobalAssetVisibilityProject,
			VariantType:   model.GlobalAssetVariantOriginal,
		}
		row.Id = uint(i + 1)
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create global row %d: %v", i, err)
		}
	}

	page1, err := ListProjectFileAssets(context.Background(), db, 42, pid, ListAssetsInput{Page: 1, Limit: 2, NoCount: true})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	page2, err := ListProjectFileAssets(context.Background(), db, 42, pid, ListAssetsInput{Page: 2, Limit: 2, NoCount: true})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page1.Items) != 2 || len(page2.Items) != 1 {
		t.Fatalf("page lengths = %d/%d, want 2/1", len(page1.Items), len(page2.Items))
	}
	seen := map[string]bool{}
	for _, item := range page1.Items {
		seen[item.URL] = true
	}
	for _, item := range page2.Items {
		if seen[item.URL] {
			t.Fatalf("page2 repeated asset %q from page1", item.URL)
		}
	}
}

func TestUploadAsset_CreatesNativeGlobalProjectFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(9)
	project := model.CanvasProject{
		UID:          42,
		UUID:         "native-global-project",
		Title:        "Project",
		ThumbnailURL: "",
		Document:     model.JSONMap{},
	}
	project.Id = pid
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	asset, err := UploadAsset(context.Background(), db, LocalAssetStorage{Root: t.TempDir()}, 42, pid, UploadAssetInput{
		FileBytes:         pngMagic,
		OriginalName:      "hero.png",
		HeaderContentType: "image/png",
		Kind:              "upload",
		Width:             320,
		Height:            180,
	})
	if err != nil {
		t.Fatalf("UploadAsset: %v", err)
	}
	if asset.GlobalAssetID == 0 {
		t.Fatalf("UploadAsset did not backfill global asset id")
	}

	var global model.GlobalAsset
	if err := db.First(&global, asset.GlobalAssetID).Error; err != nil {
		t.Fatalf("load global asset: %v", err)
	}
	if global.SourceTable != "canvas_project_file" {
		t.Fatalf("source_table = %q, want canvas_project_file", global.SourceTable)
	}
	if global.SourceID != uint64(pid) {
		t.Fatalf("source_id = %d, want project id %d", global.SourceID, pid)
	}
	if global.ProjectID == nil || *global.ProjectID != pid {
		t.Fatalf("project_id = %v, want %d", global.ProjectID, pid)
	}
	if global.URL != asset.URL || global.ThumbURL != asset.ThumbURL {
		t.Fatalf("global urls = %q/%q, want %q/%q", global.URL, global.ThumbURL, asset.URL, asset.ThumbURL)
	}
	if global.Kind != model.GlobalAssetKindImage || global.Source != model.GlobalAssetSourceUpload {
		t.Fatalf("global kind/source = %q/%q, want image/upload", global.Kind, global.Source)
	}
	if global.ContentHash == "" {
		t.Fatalf("content hash is empty")
	}

	var count int64
	if err := db.Model(&model.GlobalAsset{}).Count(&count).Error; err != nil {
		t.Fatalf("count globals: %v", err)
	}
	if count != 1 {
		t.Fatalf("global asset count = %d, want 1", count)
	}
}

func TestProjectFileAssets_ProjectMemberAccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(10)
	project := model.CanvasProject{
		UID:      42,
		UUID:     "member-project",
		Title:    "Project",
		Document: model.JSONMap{},
	}
	project.Id = pid
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: pid,
		UID:       99,
		Role:      model.GlobalProjectRoleEditor,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("create editor member: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: pid,
		UID:       100,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("create viewer member: %v", err)
	}

	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	uploaded, err := UploadAsset(context.Background(), db, LocalAssetStorage{Root: t.TempDir()}, 99, pid, UploadAssetInput{
		FileBytes:         pngMagic,
		OriginalName:      "editor.png",
		HeaderContentType: "image/png",
		Kind:              "upload",
	})
	if err != nil {
		t.Fatalf("editor UploadAsset: %v", err)
	}
	if uploaded.UID != 99 || uploaded.ProjectID == nil || *uploaded.ProjectID != pid {
		t.Fatalf("uploaded ownership = uid %d project %v, want editor uid and shared project", uploaded.UID, uploaded.ProjectID)
	}

	ownerList, err := ListProjectFileAssets(context.Background(), db, 42, pid, ListAssetsInput{Page: 1, Limit: 10})
	if err != nil {
		t.Fatalf("owner ListProjectFileAssets: %v", err)
	}
	if len(ownerList.Items) != 1 || ownerList.Items[0].URL != uploaded.URL {
		t.Fatalf("owner list = %#v, want editor upload", ownerList.Items)
	}

	ownerURL := "/uploads/canvas/uid/42/member-project/owner.png"
	ownerGlobal := model.GlobalAsset{
		UID:           42,
		ProjectID:     &pid,
		UUID:          "owner-global-asset",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      uint64(pid),
		SourceItemKey: "owner-url",
		MimeType:      "image/png",
		URL:           ownerURL,
		ThumbURL:      ownerURL,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&ownerGlobal).Error; err != nil {
		t.Fatalf("create owner global asset: %v", err)
	}
	normalizedOwnerURL, err := NormalizeOwnedCanvasReferenceURL(context.Background(), db, 99, pid, ownerURL, true)
	if err != nil {
		t.Fatalf("editor NormalizeOwnedCanvasReferenceURL owner asset: %v", err)
	}
	if normalizedOwnerURL != ownerURL {
		t.Fatalf("normalized owner url = %q, want %q", normalizedOwnerURL, ownerURL)
	}
	reusedOwnerAsset, err := CreateAsset(context.Background(), db, 99, pid, CreateAssetInput{
		Kind:     "upload",
		MimeType: "image/png",
		URL:      ownerURL,
		ThumbURL: ownerURL,
	})
	if err != nil {
		t.Fatalf("editor CreateAsset existing owner asset: %v", err)
	}
	if reusedOwnerAsset.Id != ownerGlobal.Id || reusedOwnerAsset.UID != 42 {
		t.Fatalf("reused owner asset = id %d uid %d, want existing id %d owner uid 42", reusedOwnerAsset.Id, reusedOwnerAsset.UID, ownerGlobal.Id)
	}
	if reusedOwnerAsset.GlobalAssetID != ownerGlobal.Id {
		t.Fatalf("reused owner global id = %d, want %d", reusedOwnerAsset.GlobalAssetID, ownerGlobal.Id)
	}
	if ownerGlobal.UID != 42 || ownerGlobal.ProjectID == nil || *ownerGlobal.ProjectID != pid {
		t.Fatalf("owner global identity = uid %d project %v, want owner uid 42 project %d", ownerGlobal.UID, ownerGlobal.ProjectID, pid)
	}

	_, err = UploadAsset(context.Background(), db, LocalAssetStorage{Root: t.TempDir()}, 100, pid, UploadAssetInput{
		FileBytes:         pngMagic,
		OriginalName:      "viewer.png",
		HeaderContentType: "image/png",
		Kind:              "upload",
	})
	if !errors.Is(err, ErrCanvasAssetProjectNotFound) {
		t.Fatalf("viewer UploadAsset err = %v, want ErrCanvasAssetProjectNotFound", err)
	}
}
