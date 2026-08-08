package workagent

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func TestCreateArtifactVisualDiffReport_GeneratesComparisonReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspaceRoot := t.TempDir()
	restore := overrideVisualDiffServiceForTest(workspaceRoot)
	defer restore()

	thread := seedConversationThread(t, db, 42, "Visual diff API")
	previousFile := seedVisualDiffAPIImageFile(t, db, workspaceRoot, thread, "poster-v1.png", func(x, y int) color.Color {
		return color.RGBA{R: 245, G: 245, B: 245, A: 255}
	})
	latestFile := seedVisualDiffAPIImageFile(t, db, workspaceRoot, thread, "poster-v2.png", func(x, y int) color.Color {
		if x < 2 {
			return color.RGBA{R: 30, G: 30, B: 30, A: 255}
		}
		return color.RGBA{R: 245, G: 245, B: 245, A: 255}
	})
	repo := workagentService.NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(latestArtifact.Id)+"/visual-diff-report", map[string]any{
		"previousArtifactId": "artifact-" + uintToStr(previousArtifact.Id),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data workagentService.ArtifactVisualDiffImageReportResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.Data.ReportFile == nil || body.Data.ReportFile.Id == 0 || body.Data.ReportArtifact == nil || body.Data.ReportArtifact.Id == 0 {
		t.Fatalf("missing report result: %#v", body.Data)
	}
	if body.Data.ReportArtifact.ParentArtifactID != latestArtifact.Id || body.Data.ReportArtifact.ArtifactRelation != workagentModel.ArtifactRelationComparisonReport {
		t.Fatalf("report relation = parent %d relation %q", body.Data.ReportArtifact.ParentArtifactID, body.Data.ReportArtifact.ArtifactRelation)
	}
	if body.Data.Analysis.ChangedPixelRatio < 0.49 || body.Data.Analysis.ChangedPixelRatio > 0.51 {
		t.Fatalf("changed ratio = %f, want about 0.5", body.Data.Analysis.ChangedPixelRatio)
	}
	reportAbs := workagentService.ResolveInsideWorkspace(workspaceRoot, body.Data.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report file: %v", err)
	}
	if !strings.Contains(string(reportBytes), "Automated image analysis") {
		t.Fatalf("report missing automated analysis:\n%s", string(reportBytes))
	}
}

func TestCreateArtifactVisualDiffReportRejectsCrossOwnerPreviousArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspaceRoot := t.TempDir()
	restore := overrideVisualDiffServiceForTest(workspaceRoot)
	defer restore()

	thread := seedConversationThread(t, db, 42, "Visual diff API")
	previousFile := seedVisualDiffAPIImageFileForUID(t, db, workspaceRoot, thread.Id, 99, "other-user.png", func(x, y int) color.Color {
		return color.RGBA{R: 245, G: 245, B: 245, A: 255}
	})
	latestFile := seedVisualDiffAPIImageFile(t, db, workspaceRoot, thread, "poster.png", func(x, y int) color.Color {
		return color.RGBA{R: 30, G: 30, B: 30, A: 255}
	})
	repo := workagentService.NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(latestArtifact.Id)+"/visual-diff-report", map[string]any{
		"previousArtifactId": "artifact-" + uintToStr(previousArtifact.Id),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("response wrapper status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "load previous artifact") {
		t.Fatalf("body should include scoped load failure, got %s", w.Body.String())
	}
	var failure struct {
		Code int `json:"code"`
		Data struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode failure: %v body=%s", err, w.Body.String())
	}
	if failure.Code != 0 || failure.Data.Code != "previous_artifact_not_found" || failure.Data.Reason != "owner_scope_or_missing" {
		t.Fatalf("failure = %+v, want structured previous artifact owner-scope failure", failure)
	}
}

func TestCreateArtifactVisualDiffReportRejectsNonImageWithStructuredCode(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspaceRoot := t.TempDir()
	restore := overrideVisualDiffServiceForTest(workspaceRoot)
	defer restore()

	thread := seedConversationThread(t, db, 42, "Visual diff API non-image")
	previousFile := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     "previous.txt",
		DisplayName:  "previous.txt",
		FileSize:     32,
		FileType:     "txt",
		MimeType:     "text/plain",
		FilePath:     "uid/42/20260521/thread_visual-diff-api/outputs/previous.txt",
		FileSource:   workagentModel.FileSourceOutput,
		ExistsOnDisk: true,
	}
	if err := db.Create(&previousFile).Error; err != nil {
		t.Fatalf("seed previous file: %v", err)
	}
	latestFile := seedVisualDiffAPIImageFile(t, db, workspaceRoot, thread, "poster.png", func(x, y int) color.Color {
		return color.RGBA{R: 30, G: 30, B: 30, A: 255}
	})
	repo := workagentService.NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(&previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(latestArtifact.Id)+"/visual-diff-report", map[string]any{
		"previousArtifactId": "artifact-" + uintToStr(previousArtifact.Id),
	})
	if w.Code != http.StatusOK {
		t.Fatalf("response wrapper status = %d body=%s", w.Code, w.Body.String())
	}
	var failure struct {
		Code int `json:"code"`
		Data struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatalf("decode failure: %v body=%s", err, w.Body.String())
	}
	if failure.Code != 0 || failure.Data.Code != "unsupported_previous_artifact" || failure.Data.Reason != "unsupported_comparable" {
		t.Fatalf("failure = %+v, want structured unsupported comparable failure body=%s", failure, w.Body.String())
	}
}

func overrideVisualDiffServiceForTest(workspaceRoot string) func() {
	previous := newArtifactVisualDiffImageReportService
	newArtifactVisualDiffImageReportService = func() *workagentService.ArtifactVisualDiffImageReportService {
		return workagentService.NewArtifactVisualDiffImageReportService(workagentService.ArtifactVisualDiffImageReportOptions{
			WorkspaceRoot: workspaceRoot,
		})
	}
	return func() {
		newArtifactVisualDiffImageReportService = previous
	}
}

func seedVisualDiffAPIImageFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, fileName string, pixel func(x, y int) color.Color) *workagentModel.ThreadFile {
	t.Helper()
	return seedVisualDiffAPIImageFileForUID(t, db, workspaceRoot, thread.Id, thread.UID, fileName, pixel)
}

func seedVisualDiffAPIImageFileForUID(t *testing.T, db *gorm.DB, workspaceRoot string, threadID uint, uid int, fileName string, pixel func(x, y int) color.Color) *workagentModel.ThreadFile {
	t.Helper()
	content := visualDiffAPIPNG(t, 4, 4, pixel)
	relPath := "uid/" + uintToStr(uint(uid)) + "/20260521/thread_visual-diff-api/outputs/" + fileName
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          uid,
		ThreadID:     threadID,
		FileName:     fileName,
		DisplayName:  fileName,
		FileSize:     uint64(len(content)),
		FileType:     "png",
		MimeType:     "image/png",
		FilePath:     relPath,
		FileHash:     visualDiffAPIMD5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff API fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed image file: %v", err)
	}
	return &file
}

func visualDiffAPIMD5Hex(content []byte) string {
	sum := md5.Sum(content)
	return hex.EncodeToString(sum[:])
}

func visualDiffAPIPNG(t *testing.T, width, height int, pixel func(x, y int) color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, pixel(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
