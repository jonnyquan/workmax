package globalasset

import (
	"context"
	"fmt"
	"testing"
	"time"

	"server/model"
	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestCreateCanvasProjectFile_UpsertsByProjectAndSourceKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(7)
	repo := NewRepository(db)
	global, err := repo.CreateCanvasProjectFile(CanvasProjectFileInput{
		UID:           42,
		ProjectID:     pid,
		SourceItemKey: canvasProjectFileSourceKey("upload", pid, "/uploads/canvas/uid/42/project/a.png"),
		URL:           "/uploads/canvas/uid/42/project/a.png",
		ThumbURL:      "/uploads/canvas/uid/42/project/a.png",
		MimeType:      "image/png",
		SizeBytes:     123,
		Width:         512,
		Height:        256,
		Kind:          model.GlobalAssetKindImage,
		VariantType:   model.GlobalAssetVariantOriginal,
		Metadata:      model.JSONMap{"originalName": "a.png"},
	})
	if err != nil {
		t.Fatalf("CreateCanvasProjectFile: %v", err)
	}
	if global == nil || global.Id == 0 {
		t.Fatalf("expected global asset")
	}
	if global.Kind != model.GlobalAssetKindImage || global.Source != model.GlobalAssetSourceUpload {
		t.Fatalf("unexpected global asset kind/source: %+v", global)
	}
	if global.SourceTable != sourceTableCanvasProjectFile {
		t.Fatalf("source_table = %q, want %q", global.SourceTable, sourceTableCanvasProjectFile)
	}
	if global.SourceID != uint64(pid) {
		t.Fatalf("source_id = %d, want project id %d", global.SourceID, pid)
	}
	again, err := repo.CreateCanvasProjectFile(CanvasProjectFileInput{
		UID:           42,
		ProjectID:     pid,
		SourceItemKey: canvasProjectFileSourceKey("upload", pid, "/uploads/canvas/uid/42/project/a.png"),
		URL:           "/uploads/canvas/uid/42/project/a.png",
		MimeType:      "image/png",
		Kind:          model.GlobalAssetKindImage,
	})
	if err != nil {
		t.Fatalf("second CreateCanvasProjectFile: %v", err)
	}
	if again.Id != global.Id {
		t.Fatalf("idempotent upsert id = %d, want %d", again.Id, global.Id)
	}
	var count int64
	db.Model(&model.GlobalAsset{}).Count(&count)
	if count != 1 {
		t.Fatalf("global asset count = %d, want 1", count)
	}
}

func TestCreateManagedUpload_ReturnsReusablePrivateAsset(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)

	global, err := repo.CreateManagedUpload(ManagedUploadInput{
		UID:         42,
		UploadID:    "upload-1",
		URL:         "/uploads/references/uid/42/2026/05/13/ref.png",
		MimeType:    "image/png",
		SizeBytes:   128,
		ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Kind:        model.GlobalAssetKindImage,
	})
	if err != nil {
		t.Fatalf("CreateManagedUpload: %v", err)
	}
	if global == nil || global.Id == 0 {
		t.Fatalf("expected global asset")
	}
	if global.UID != 42 || global.Kind != model.GlobalAssetKindImage || global.Visibility != model.GlobalAssetVisibilityPrivate {
		t.Fatalf("unexpected global asset: %+v", global)
	}

	loaded, err := repo.LoadForAccess(global.Id, 42)
	if err != nil {
		t.Fatalf("LoadForAccess owner: %v", err)
	}
	if loaded.URL != global.URL {
		t.Fatalf("loaded URL = %q, want %q", loaded.URL, global.URL)
	}
	if _, err := repo.LoadForAccess(global.Id, 99); err == nil {
		t.Fatalf("expected cross-user access to be denied")
	}

	again, err := repo.CreateManagedUpload(ManagedUploadInput{
		UID:         42,
		UploadID:    "upload-1",
		URL:         "/uploads/references/uid/42/2026/05/13/ref.png",
		MimeType:    "image/png",
		SizeBytes:   128,
		ContentHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Kind:        model.GlobalAssetKindImage,
	})
	if err != nil {
		t.Fatalf("second CreateManagedUpload: %v", err)
	}
	if again.Id != global.Id {
		t.Fatalf("idempotent managed upload id = %d, want %d", again.Id, global.Id)
	}
	var count int64
	db.Model(&model.GlobalAsset{}).
		Where("source_table = ? AND source_id = ? AND source_item_key = ?", sourceTableReferenceUpload, 42, "upload-1").
		Count(&count)
	if count != 1 {
		t.Fatalf("managed upload global asset count = %d, want 1", count)
	}
}

func TestCreateFromGenerationObject_MapsObjectMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	object := model.GenerationObject{
		UID:         42,
		TaskID:      "task-1",
		RecordID:    9,
		ToolID:      "video-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "videos/task-1.mp4",
		AssetKind:   "video",
		ContentType: "video/mp4",
		SizeBytes:   999,
		PublicURL:   "https://cdn.example.com/videos/task-1.mp4",
		Status:      model.GenerationObjectStatusActive,
	}
	if err := db.Create(&object).Error; err != nil {
		t.Fatalf("create generation object: %v", err)
	}

	global, err := NewRepository(db).CreateFromGenerationObject(&object)
	if err != nil {
		t.Fatalf("CreateFromGenerationObject: %v", err)
	}
	if global.Kind != model.GlobalAssetKindVideo || global.Source != model.GlobalAssetSourceGeneration {
		t.Fatalf("unexpected global asset: %+v", global)
	}
	if global.Metadata["taskId"] != "task-1" {
		t.Fatalf("metadata taskId = %#v", global.Metadata["taskId"])
	}

	var reloaded model.GenerationObject
	if err := db.First(&reloaded, object.Id).Error; err != nil {
		t.Fatalf("reload generation object: %v", err)
	}
	if reloaded.GlobalAssetID != global.Id {
		t.Fatalf("global bridge = %d, want %d", reloaded.GlobalAssetID, global.Id)
	}
}

func TestCreateFromThreadFile_BridgesAgentFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   100,
		FileName:   "brief.pdf",
		FileSize:   321,
		FileType:   "application/pdf",
		MimeType:   "application/pdf",
		FilePath:   "workspaces/42/brief.pdf",
		FileSource: workagentModel.FileSourceUpload,
		FileHash:   "abc123",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create thread file: %v", err)
	}

	global, err := NewRepository(db).CreateFromThreadFile(&file)
	if err != nil {
		t.Fatalf("CreateFromThreadFile: %v", err)
	}
	if global.Source != model.GlobalAssetSourceAgent {
		t.Fatalf("source = %q", global.Source)
	}
	if global.URL != fmt.Sprintf("/api/work-agent/file/%d/download", file.Id) {
		t.Fatalf("url = %q", global.URL)
	}
	if global.Metadata["threadId"] == nil {
		t.Fatalf("expected thread metadata")
	}

	var reloaded workagentModel.ThreadFile
	if err := db.First(&reloaded, file.Id).Error; err != nil {
		t.Fatalf("reload thread file: %v", err)
	}
	if reloaded.GlobalAssetID != global.Id {
		t.Fatalf("global bridge = %d, want %d", reloaded.GlobalAssetID, global.Id)
	}
}

func TestCreateFromThreadFileForThread_CarriesProjectVisibility(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectID := uint(99)
	thread := workagentModel.ChatThread{
		UID:       42,
		UUID:      "thread-project",
		Name:      "Project thread",
		ProjectID: projectID,
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("create thread: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "output.png",
		FileSize:   123,
		FileType:   "image",
		MimeType:   "image/png",
		FilePath:   "workspaces/42/output.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create thread file: %v", err)
	}

	global, err := NewRepository(db).CreateFromThreadFileForThread(&file, &thread)
	if err != nil {
		t.Fatalf("CreateFromThreadFileForThread: %v", err)
	}
	if global.ProjectID == nil || *global.ProjectID != projectID {
		t.Fatalf("project id = %#v, want %d", global.ProjectID, projectID)
	}
	if global.Visibility != model.GlobalAssetVisibilityProject {
		t.Fatalf("visibility = %d, want project", global.Visibility)
	}
	if got := global.Metadata["storagePath"]; got != "workspaces/42/output.png" {
		t.Fatalf("storagePath = %#v", got)
	}
}

func TestLoadForAccess_AllowsOwnerAndProjectMember(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(7)
	ownerAsset := model.GlobalAsset{
		UID:           42,
		ProjectID:     &pid,
		UUID:          "asset-owner",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableCanvasProjectFile,
		SourceID:      1,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/p/a.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&ownerAsset).Error; err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: pid,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("create member: %v", err)
	}
	repo := NewRepository(db)

	if _, err := repo.LoadForAccess(ownerAsset.Id, 42); err != nil {
		t.Fatalf("owner LoadForAccess: %v", err)
	}
	if _, err := repo.LoadForAccess(ownerAsset.Id, 99); err != nil {
		t.Fatalf("member LoadForAccess: %v", err)
	}
	if _, err := repo.LoadByURLForAccess("/uploads/canvas/uid/42/p/a.png", 99); err != nil {
		t.Fatalf("member LoadByURLForAccess: %v", err)
	}
}

func TestLoadForAccess_DeniesNonMemberAndDeletedAsset(t *testing.T) {
	db := testutil.NewTestDB(t)
	pid := uint(7)
	active := model.GlobalAsset{
		UID:           42,
		ProjectID:     &pid,
		UUID:          "asset-active",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableCanvasProjectFile,
		SourceID:      2,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/p/b.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	deleted := active
	deleted.UUID = "asset-deleted"
	deleted.SourceID = 3
	deleted.URL = "/uploads/canvas/uid/42/p/c.png"
	deleted.Status = model.GlobalAssetStatusDeleted
	if err := db.Create(&active).Error; err != nil {
		t.Fatalf("create active asset: %v", err)
	}
	if err := db.Create(&deleted).Error; err != nil {
		t.Fatalf("create deleted asset: %v", err)
	}
	repo := NewRepository(db)

	if _, err := repo.LoadForAccess(active.Id, 99); err == nil {
		t.Fatal("non-member access unexpectedly succeeded")
	}
	if _, err := repo.LoadForAccess(deleted.Id, 42); err == nil {
		t.Fatal("deleted asset access unexpectedly succeeded")
	}
}

func TestBackfillExistingSources_IsIdempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	object := model.GenerationObject{
		UID:         42,
		TaskID:      "task-1",
		ToolID:      "video-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "videos/task-1.mp4",
		AssetKind:   "video",
		ContentType: "video/mp4",
		PublicURL:   "https://cdn.example.com/videos/task-1.mp4",
		Status:      model.GenerationObjectStatusActive,
	}
	if err := db.Create(&object).Error; err != nil {
		t.Fatalf("create generation object: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   100,
		FileName:   "brief.pdf",
		FilePath:   "workspaces/42/brief.pdf",
		MimeType:   "application/pdf",
		FileSource: workagentModel.FileSourceUpload,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("create thread file: %v", err)
	}

	report, err := BackfillExistingSources(context.Background(), db, BackfillOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("BackfillExistingSources: %v", err)
	}
	if report.GenerationObjects != 1 || report.WorkAgentFiles != 1 {
		t.Fatalf("report = %+v", report)
	}
	report, err = BackfillExistingSources(context.Background(), db, BackfillOptions{BatchSize: 1})
	if err != nil {
		t.Fatalf("second BackfillExistingSources: %v", err)
	}
	if report.GenerationObjects != 0 || report.WorkAgentFiles != 0 {
		t.Fatalf("second report = %+v, want zero", report)
	}
	var count int64
	if err := db.Model(&model.GlobalAsset{}).Count(&count).Error; err != nil {
		t.Fatalf("count global assets: %v", err)
	}
	if count != 2 {
		t.Fatalf("global asset count = %d, want 2", count)
	}
}

func TestAuditCoverage_ReportsMissingBridgesAndDanglingSources(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Create(&model.GenerationObject{
		UID:         42,
		TaskID:      "task-1",
		ToolID:      "video-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "videos/task-1.mp4",
		AssetKind:   "video",
		ContentType: "video/mp4",
		PublicURL:   "https://cdn.example.com/videos/task-1.mp4",
		Status:      model.GenerationObjectStatusActive,
	}).Error; err != nil {
		t.Fatalf("create generation object: %v", err)
	}
	if err := db.Create(&workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   100,
		FileName:   "brief.pdf",
		FilePath:   "workspaces/42/brief.pdf",
		MimeType:   "application/pdf",
		FileSource: workagentModel.FileSourceUpload,
	}).Error; err != nil {
		t.Fatalf("create thread file: %v", err)
	}
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		UUID:          "dangling-canvas-project-file",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableCanvasProjectFile,
		SourceID:      999,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/missing-project.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("create dangling canvas project file global asset: %v", err)
	}
	deletedObject := model.GenerationObject{
		UID:         42,
		TaskID:      "task-deleted",
		ToolID:      "video-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "videos/task-deleted.mp4",
		AssetKind:   "video",
		ContentType: "video/mp4",
		PublicURL:   "https://cdn.example.com/videos/task-deleted.mp4",
		Status:      model.GenerationObjectStatusDeleted,
	}
	if err := db.Create(&deletedObject).Error; err != nil {
		t.Fatalf("create deleted generation object: %v", err)
	}
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		UUID:          "deleted-source",
		Kind:          model.GlobalAssetKindVideo,
		Source:        model.GlobalAssetSourceGeneration,
		SourceTable:   sourceTableGenerationObject,
		SourceID:      uint64(deletedObject.Id),
		SourceItemKey: "object",
		URL:           deletedObject.PublicURL,
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("create global asset for deleted source: %v", err)
	}
	deletedAt := time.Now()
	deletedProject := model.CanvasProject{
		UID:       42,
		UUID:      "deleted-project",
		Title:     "Deleted",
		Document:  model.JSONMap{},
		DeletedAt: &deletedAt,
	}
	deletedProject.Id = 77
	if err := db.Create(&deletedProject).Error; err != nil {
		t.Fatalf("create deleted project: %v", err)
	}
	if err := db.Create(&model.GlobalAsset{
		UID:           42,
		ProjectID:     &deletedProject.Id,
		UUID:          "deleted-canvas-project-file",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   sourceTableCanvasProjectFile,
		SourceID:      uint64(deletedProject.Id),
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/deleted-project.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityProject,
		VariantType:   model.GlobalAssetVariantOriginal,
	}).Error; err != nil {
		t.Fatalf("create global asset for deleted project: %v", err)
	}

	report, err := AuditCoverage(context.Background(), db)
	if err != nil {
		t.Fatalf("AuditCoverage: %v", err)
	}
	if report.MissingGenerationObjects != 2 ||
		report.MissingWorkAgentFiles != 1 ||
		report.GlobalAssetsMissingSource != 1 ||
		report.GlobalAssetsDeletedSource != 2 {
		t.Fatalf("coverage report = %+v", report)
	}
}

func TestSyncFromGenerationObjectsByRecordIDs_UpdatesGlobalAssetStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	object := model.GenerationObject{
		UID:         42,
		TaskID:      "task-1",
		RecordID:    9,
		ToolID:      "video-generator",
		Provider:    "r2",
		Bucket:      "bucket",
		ObjectKey:   "videos/task-1.mp4",
		AssetKind:   "video",
		ContentType: "video/mp4",
		PublicURL:   "https://cdn.example.com/videos/task-1.mp4",
		Status:      model.GenerationObjectStatusActive,
	}
	if err := db.Create(&object).Error; err != nil {
		t.Fatalf("create generation object: %v", err)
	}
	repo := NewRepository(db)
	global, err := repo.CreateFromGenerationObject(&object)
	if err != nil {
		t.Fatalf("CreateFromGenerationObject: %v", err)
	}
	if err := db.Model(&model.GenerationObject{}).
		Where("id = ?", object.Id).
		Update("status", model.GenerationObjectStatusDeleted).Error; err != nil {
		t.Fatalf("delete object: %v", err)
	}

	count, err := repo.SyncFromGenerationObjectsByRecordIDs(context.Background(), []uint{9})
	if err != nil {
		t.Fatalf("SyncFromGenerationObjectsByRecordIDs: %v", err)
	}
	if count != 1 {
		t.Fatalf("sync count = %d, want 1", count)
	}
	var reloaded model.GlobalAsset
	if err := db.First(&reloaded, global.Id).Error; err != nil {
		t.Fatalf("load global asset: %v", err)
	}
	if reloaded.Status != model.GlobalAssetStatusDeleted {
		t.Fatalf("global asset status = %d, want deleted", reloaded.Status)
	}
}
