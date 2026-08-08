package workagent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"server/model"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildArtifactEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/conversations/:id/artifacts", withClaims(uid), api.GetConversationArtifacts)
	r.PATCH("/conversations/:id/artifacts/:artifactId/status", withClaims(uid), api.UpdateArtifactStatus)
	r.PATCH("/conversations/:id/artifacts/:artifactId/comparison", withClaims(uid), api.UpdateArtifactComparison)
	r.POST("/conversations/:id/artifacts/:artifactId/visual-diff-report", withClaims(uid), api.CreateArtifactVisualDiffReport)
	r.PATCH("/conversations/:id/artifacts/:artifactId/html-preview-diagnostics", withClaims(uid), api.UpdateArtifactHTMLPreviewDiagnostics)
	r.POST("/conversations/:id/artifacts/:artifactId/html-browser-validation", withClaims(uid), api.RunArtifactHTMLBrowserValidation)
	r.POST("/conversations/:id/artifacts/:artifactId/export", withClaims(uid), api.ExportArtifact)
	r.POST("/conversations/:id/artifacts/:artifactId/export-jobs", withClaims(uid), api.CreateArtifactExportJob)
	r.GET("/conversations/:id/artifacts/:artifactId/export-jobs/:jobId", withClaims(uid), api.GetArtifactExportJob)
	r.POST("/conversations/:id/artifacts/:artifactId/decision", withClaims(uid), api.ApplyArtifactDecision)
	r.POST("/conversations/:id/artifacts/:artifactId/asset-candidate", withClaims(uid), api.CreateArtifactAssetCandidate)
	r.GET("/conversations/:id/asset-candidates", withClaims(uid), api.ListArtifactAssetCandidates)
	r.PATCH("/conversations/:id/asset-candidates/:candidateId/status", withClaims(uid), api.UpdateArtifactAssetCandidateStatus)
	return r
}

func validDesignSystemCandidateProfileJSON() string {
	raw, _ := json.Marshal(map[string]any{
		"designSystemMarkdown": validDesignSystemMarkdown(),
	})
	return string(raw)
}

func validDesignSystemMarkdown() string {
	return strings.Join([]string{
		"# Design System - Project Campaign",
		"",
		"`derived_from: artifact-7`",
		"",
		"## 1. Color",
		"| Slot | OKLch | Hex | Role |",
		"|---|---|---|---|",
		"| bg | oklch(98% 0 0) | #fafafa | background |",
		"| fg | oklch(12% 0 0) | #111111 | foreground |",
		"| accent | oklch(55% 0.18 250) | #3151c4 | accent |",
		"| muted | oklch(75% 0 0) | #b5b5b5 | muted |",
		"",
		"## 2. Typography",
		"- Display: Inter Display, system-ui, sans-serif · weight 700 · sizes [48, 36]",
		"- Body: Inter, system-ui, sans-serif · weight 400 / 500 · sizes [16, 14]",
		"- Mono: JetBrains Mono, monospace · weight 400 · size 13",
		"",
		"## 3. Spacing",
		"Scale: 4 / 8 / 16 / 24 / 32 / 48 / 64",
		"",
		"## 4. Layout",
		"- Container: max-w 1200",
		"",
		"## 5. Components",
		"- Button: bg accent, fg white, radius 8",
		"",
		"## 6. Motion",
		"- Fast: 120ms ease-out",
		"- Default: 200ms ease-out",
		"- Slow: 320ms ease-out",
		"",
		"## 7. Voice",
		"Tone keywords: clear · direct · useful",
		"",
		"## 8. Brand",
		"- Logo on bg or accent only",
		"",
		"## 9. Anti-patterns",
		"- Do not add extra accent colors",
	}, "\n")
}

func TestGetConversationArtifacts_ReturnsArtifactViews(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	if err := db.Create(&workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSize:   2048,
		FileSource: workagentModel.FileSourceOutput,
	}).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := db.Create(&workagentModel.ThreadFile{
		UID:        99,
		ThreadID:   thread.Id,
		FileName:   "cross-tenant.pdf",
		FilePath:   "outputs/cross-tenant.pdf",
		FileSource: workagentModel.FileSourceOutput,
	}).Error; err != nil {
		t.Fatalf("seed cross tenant file: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := getRequest(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []workagentService.ArtifactView `json:"items"`
			Count int                             `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Count != 1 || len(body.Data.Items) != 1 {
		t.Fatalf("count/items = %d/%d, want 1/1; body=%s", body.Data.Count, len(body.Data.Items), w.Body.String())
	}
	item := body.Data.Items[0]
	if item.ArtifactType != "image" {
		t.Errorf("artifact type = %q, want image", item.ArtifactType)
	}
	if item.PreviewType != "image" {
		t.Errorf("preview type = %q, want image", item.PreviewType)
	}
	if item.Status != "draft" {
		t.Errorf("status = %q, want draft", item.Status)
	}
	if item.DownloadURL == "" {
		t.Errorf("downloadUrl should be populated for artifact preview")
	}
	if item.PreviewURL == "" {
		t.Errorf("previewUrl should be populated for artifact preview")
	}
}

func TestGetConversationArtifacts_ReturnsHTMLBrowserValidationPlan(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML browser validation thread")
	workspace := filepath.Join(".", "agent_workspace", "test-browser-validation-plan")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><head><meta name="viewport" content="width=device-width"></head><body><main>Poster</main></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	if err := db.Create(&workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-browser-validation-plan", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := getRequest(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []workagentService.ArtifactView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1; body=%s", len(body.Data.Items), w.Body.String())
	}
	plan := body.Data.Items[0].HTMLBrowserValidationPlan
	if plan == nil {
		t.Fatal("htmlBrowserValidationPlan is nil")
	}
	if plan.Status != workagentService.ArtifactHTMLBrowserValidationPending {
		t.Fatalf("status = %q, want pending", plan.Status)
	}
	if plan.Reason != "browser_validation_required" {
		t.Fatalf("reason = %q, want browser_validation_required", plan.Reason)
	}
	if !containsStringArtifactTest(plan.Targets, "png") || !containsStringArtifactTest(plan.Targets, "pdf") {
		t.Fatalf("targets = %#v, want png and pdf", plan.Targets)
	}
}

type fakeAPIHTMLBrowserValidator struct {
	diagnostics []workagentService.ArtifactHTMLValidationIssue
}

func (v fakeAPIHTMLBrowserValidator) ValidateHTMLInBrowser(_ context.Context, _ workagentService.HTMLBrowserValidationInput) ([]workagentService.ArtifactHTMLValidationIssue, error) {
	return v.diagnostics, nil
}

func TestRunArtifactHTMLBrowserValidation_WritesDiagnostics(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML browser validation run thread")
	workspace := filepath.Join(".", "agent_workspace", "test-browser-validation-run")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<!doctype html>
<html><head><meta name="viewport" content="width=device-width"></head>
<body><main style="width:100vw;height:100vh;overflow:auto">Poster</main></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-browser-validation-run", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	prevFactory := newArtifactBrowserValidationWorker
	newArtifactBrowserValidationWorker = func() (*workagentService.ArtifactBrowserValidationWorker, error) {
		return workagentService.NewArtifactBrowserValidationWorker(workagentService.ArtifactBrowserValidationWorkerOptions{
			DB:            db,
			WorkspaceRoot: filepath.Join(".", "agent_workspace"),
			Validator: fakeAPIHTMLBrowserValidator{diagnostics: []workagentService.ArtifactHTMLValidationIssue{
				{Code: "text_overflow", Severity: "warn", Message: "headline clips"},
			}},
		}), nil
	}
	t.Cleanup(func() { newArtifactBrowserValidationWorker = prevFactory })

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/html-browser-validation", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var updated workagentModel.ArtifactRegistry
	if err := db.First(&updated, artifact.Id).Error; err != nil {
		t.Fatalf("load updated artifact: %v", err)
	}
	var diagnostics []workagentService.ArtifactHTMLValidationIssue
	if err := json.Unmarshal([]byte(updated.HTMLPreviewDiagnostics), &diagnostics); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	if !hasAPIDiagnostic(diagnostics, "text_overflow", "browser_validation") ||
		!hasAPIDiagnostic(diagnostics, "browser_validation_passed", "browser_validation") {
		t.Fatalf("diagnostics = %#v, want browser validation issue and pass marker", diagnostics)
	}
}

func TestRunArtifactHTMLBrowserValidation_ReturnsStructuredFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML browser validation missing source thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "missing.html",
		FilePath:   "test-browser-validation-missing/missing.html",
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	prevFactory := newArtifactBrowserValidationWorker
	newArtifactBrowserValidationWorker = func() (*workagentService.ArtifactBrowserValidationWorker, error) {
		return workagentService.NewArtifactBrowserValidationWorker(workagentService.ArtifactBrowserValidationWorkerOptions{
			DB:            db,
			WorkspaceRoot: filepath.Join(".", "agent_workspace"),
			Validator:     fakeAPIHTMLBrowserValidator{},
		}), nil
	}
	t.Cleanup(func() { newArtifactBrowserValidationWorker = prevFactory })

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/html-browser-validation", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Code   string `json:"code"`
			Reason string `json:"reason"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != 0 {
		t.Fatalf("response code = %d, want failure; body=%s", body.Code, w.Body.String())
	}
	if body.Data.Code != "source_file_read_failed" || !strings.Contains(body.Data.Reason, "read source html") {
		t.Fatalf("failure data = %+v, want source_file_read_failed/read source html", body.Data)
	}
}

func TestUpdateArtifactHTMLPreviewDiagnostics_PersistsAndMergesIntoValidation(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   "outputs/landing.html",
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/html-preview-diagnostics", map[string]any{
		"diagnostics": []map[string]any{
			{"code": "resource-error", "severity": "warning", "message": "Failed to load /missing.png"},
			{"code": "resource-error", "severity": "warning", "message": "Failed to load /missing.png"},
			{"code": "console", "severity": "info", "message": "   "},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if !strings.Contains(reloaded.HTMLPreviewDiagnostics, "preview_runtime") {
		t.Fatalf("html preview diagnostics = %q", reloaded.HTMLPreviewDiagnostics)
	}
	view := workagentService.ArtifactViewFromRegistryAndThreadFile(reloaded, file)
	if view.HTMLValidation == nil || len(view.HTMLValidation.PreviewDiagnostics) != 1 {
		t.Fatalf("preview diagnostics = %#v", view.HTMLValidation)
	}
	if view.HTMLValidation.Status != workagentService.ArtifactHTMLValidationWarn {
		t.Fatalf("html validation status = %q, want warn from preview diagnostics", view.HTMLValidation.Status)
	}
	got := view.HTMLValidation.PreviewDiagnostics[0]
	if got.Code != "resource_error" || got.Severity != "warn" || got.Source != "preview_runtime" {
		t.Fatalf("normalized diagnostic = %#v", got)
	}
}

func TestUpdateArtifactHTMLPreviewDiagnostics_MergesWithExistingBrowserDiagnostics(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   "outputs/landing.html",
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	existing := `[{"code":"text_overflow","severity":"warn","message":"headline clips","source":"browser_validation"}]`
	if err := db.Model(&workagentModel.ArtifactRegistry{}).Where("id = ?", artifact.Id).Update("html_preview_diagnostics", existing).Error; err != nil {
		t.Fatalf("seed diagnostics: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/html-preview-diagnostics", map[string]any{
		"diagnostics": []map[string]any{
			{"code": "resource-error", "severity": "error", "message": "img failed: /missing.png"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	view := workagentService.ArtifactViewFromRegistryAndThreadFile(reloaded, file)
	if view.HTMLValidation == nil {
		t.Fatal("expected html validation diagnostics")
	}
	if !hasAPIDiagnostic(view.HTMLValidation.PreviewDiagnostics, "text_overflow", "browser_validation") ||
		!hasAPIDiagnostic(view.HTMLValidation.PreviewDiagnostics, "resource_error", "preview_runtime") {
		t.Fatalf("expected browser and preview diagnostics to merge, got %#v", view.HTMLValidation.PreviewDiagnostics)
	}
	if view.HTMLValidation.Status != workagentService.ArtifactHTMLValidationBlock {
		t.Fatalf("html validation status = %q, want block from error preview diagnostics", view.HTMLValidation.Status)
	}
}

func TestExportArtifact_ZipsHTMLWithLocalAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-artifact")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(filepath.Join(workspace, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	htmlPath := filepath.Join(workspace, "landing.html")
	if err := os.WriteFile(htmlPath, []byte(`<html><head><meta name="viewport" content="width=device-width"><link href="./styles/theme.css"></head><body><img src="./hero.png" srcset="./hero.png 1x, ./hero@2x.png 2x"></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hero.png"), []byte("hero-bytes"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "hero@2x.png"), []byte("hero-2x-bytes"), 0o644); err != nil {
		t.Fatalf("write retina asset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "styles", "theme.css"), []byte("css-bytes"), 0o644); err != nil {
		t.Fatalf("write css: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-artifact", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/zip") {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="landing.zip"`) {
		t.Fatalf("content-disposition = %q, want landing.zip", cd)
	}
	files := unzipArtifactAPIResponse(t, w.Body.Bytes())
	if string(files["index.html"]) != `<html><head><meta name="viewport" content="width=device-width"><link href="assets/theme.css"></head><body><img src="assets/hero.png" srcset="assets/hero.png 1x, assets/hero@2x.png 2x"></body></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	if string(files["assets/hero.png"]) != "hero-bytes" {
		t.Fatalf("hero asset = %q", string(files["assets/hero.png"]))
	}
	if string(files["assets/hero@2x.png"]) != "hero-2x-bytes" {
		t.Fatalf("retina hero asset = %q", string(files["assets/hero@2x.png"]))
	}
	if string(files["assets/theme.css"]) != "css-bytes" {
		t.Fatalf("css asset = %q", string(files["assets/theme.css"]))
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, want exported", reloaded.Status)
	}
}

func TestExportArtifact_RejectsRemoteHTMLAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-remote")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><body><img src="https://cdn.example.com/hero.png"></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-remote", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot yet inline or mirror remote assets") {
		t.Fatalf("body = %s", w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "remote_assets_unsupported" || failure.Target != "zip" || failure.Reason != "remote_asset_reference" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestExportArtifact_RejectsEscapingHTMLAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-escape")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><body><img src="../secret.png"></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-escape", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "HTML artifact has blocking export issues") {
		t.Fatalf("body = %s", w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "html_export_blocked" || failure.Target != "zip" || failure.Reason != "path_traversal" {
		t.Fatalf("failure = %#v", failure)
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status == workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, should not be exported after blocked ZIP export", reloaded.Status)
	}
}

func TestExportArtifact_RejectsMissingLocalHTMLAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-missing-local")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><body><img src="./missing.png"></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-missing-local", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "missing_local_asset" || failure.Target != "zip" || failure.Reason != "./missing.png" {
		t.Fatalf("failure = %#v", failure)
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status == workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, should not be exported after missing local asset", reloaded.Status)
	}
}

func TestExportArtifact_ZipsCleanHTMLWithoutAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-clean")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><head><meta name="viewport" content="width=device-width"></head><body>Clean</body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-clean", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	rec := &workagentService.RecordingSink{}
	prev := workagentService.SetMetricSink(rec)
	defer workagentService.SetMetricSink(prev)

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	files := unzipArtifactAPIResponse(t, w.Body.Bytes())
	if len(files) != 1 {
		t.Fatalf("zip files = %#v, want only index.html", files)
	}
	if string(files["index.html"]) != `<html><head><meta name="viewport" content="width=device-width"></head><body>Clean</body></html>` {
		t.Fatalf("index.html = %q", string(files["index.html"]))
	}
	ev := rec.FindByEvent("wa_artifact_export")
	if ev == nil {
		t.Fatal("expected wa_artifact_export metric")
	}
	if ev.Fields["status"] != "success" || ev.Fields["target"] != "zip" {
		t.Fatalf("metric fields = %#v, want success zip", ev.Fields)
	}
	if ev.Fields["artifact_type"] != "html" || ev.Fields["output_type"] != "html" {
		t.Fatalf("metric artifact fields = %#v", ev.Fields)
	}
}

func TestExportArtifact_ZipsExpandedHTMLAssetReferences(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-expanded-assets")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(filepath.Join(workspace, "styles"), 0o755); err != nil {
		t.Fatalf("mkdir styles: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "images"), 0o755); err != nil {
		t.Fatalf("mkdir images: %v", err)
	}
	assetFiles := map[string]string{
		"hero.png":          "hero",
		"hero@2x.png":       "hero2x",
		"preload-1x.png":    "preload1x",
		"preload-2x.png":    "preload2x",
		"demo.mp4":          "mp4",
		"demo-poster.jpg":   "poster",
		"captions.vtt":      "WEBVTT\n",
		"images/bg.png":     "bg1x",
		"images/bg@2x.png":  "bg2x",
		"styles/banner.css": `.hero{background-image:image-set("../images/bg.png" 1x, "../images/bg@2x.png" 2x)}`,
	}
	for rel, body := range assetFiles {
		if err := os.WriteFile(filepath.Join(workspace, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	html := `<!doctype html><html><head><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="./styles/banner.css"><link rel="preload" as="image" imagesrcset="./preload-1x.png 1x, ./preload-2x.png 2x"></head><body><main class="hero"><img src="./hero.png" srcset="./hero@2x.png 2x" alt="Hero"><video src="./demo.mp4" poster="./demo-poster.jpg" controls><track kind="captions" src="./captions.vtt"></video>Expanded assets</main></body></html>`
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(html), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}

	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-expanded-assets", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	files := unzipArtifactAPIResponse(t, w.Body.Bytes())
	for _, name := range []string{
		"index.html",
		"assets/banner.css",
		"assets/hero.png",
		"assets/hero@2x.png",
		"assets/preload-1x.png",
		"assets/preload-2x.png",
		"assets/demo.mp4",
		"assets/demo-poster.jpg",
		"assets/captions.vtt",
		"assets/bg.png",
		"assets/bg@2x.png",
	} {
		if _, ok := files[name]; !ok {
			t.Fatalf("zip missing %s; files=%v", name, zipFileNames(files))
		}
	}
	index := string(files["index.html"])
	for _, want := range []string{
		`href="assets/banner.css"`,
		`imagesrcset="assets/preload-1x.png 1x, assets/preload-2x.png 2x"`,
		`src="assets/hero.png"`,
		`srcset="assets/hero@2x.png 2x"`,
		`poster="assets/demo-poster.jpg"`,
		`src="assets/captions.vtt"`,
	} {
		if !strings.Contains(index, want) {
			t.Fatalf("index.html missing %q: %s", want, index)
		}
	}
	css := string(files["assets/banner.css"])
	if !strings.Contains(css, `image-set("bg.png" 1x, "bg@2x.png" 2x)`) {
		t.Fatalf("banner.css did not rewrite image-set candidates: %s", css)
	}
}

func TestExportArtifact_SanitizesZipDownloadFilename(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-filename")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><body>Filename</body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   `..\..\Campaign Final.HTML`,
		FilePath:   filepath.ToSlash(filepath.Join("test-export-filename", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	cd := w.Header().Get("Content-Disposition")
	if strings.Contains(cd, "..") || strings.Contains(cd, `\`) || strings.Contains(cd, "/") {
		t.Fatalf("content-disposition leaked path segments: %q", cd)
	}
	if !strings.Contains(cd, `filename="Campaign Final.zip"`) {
		t.Fatalf("content-disposition = %q, want Campaign Final.zip", cd)
	}
}

func TestExportArtifact_RejectsUnsupportedTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	rec := &workagentService.RecordingSink{}
	prev := workagentService.SetMetricSink(rec)
	defer workagentService.SetMetricSink(prev)

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-123/export", map[string]any{
		"target": "tar",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Unsupported HTML export target") {
		t.Fatalf("body = %s", w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "unsupported_target" || failure.Target != "tar" || failure.Reason != "unsupported_target" {
		t.Fatalf("failure = %#v", failure)
	}
	ev := rec.FindByEvent("wa_artifact_export")
	if ev == nil {
		t.Fatal("expected wa_artifact_export metric")
	}
	if ev.Fields["status"] != "failed" || ev.Fields["target"] != "tar" {
		t.Fatalf("metric fields = %#v, want failed tar", ev.Fields)
	}
	if ev.Fields["failure_code"] != "unsupported_target" || ev.Fields["failure_reason"] != "unsupported_target" {
		t.Fatalf("metric failure fields = %#v", ev.Fields)
	}
}

func TestExportArtifact_RendersPDFWithBrowserCommand(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-pdf-worker")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; height: 630px">PDF</main></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-pdf-worker", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(42, thread.Id, artifact.Id, []workagentService.ArtifactHTMLValidationIssue{
		{Code: "browser_validation_passed", Severity: "info", Message: "browser validation passed", Source: "browser_validation"},
	}); err != nil {
		t.Fatalf("seed browser validation diagnostics: %v", err)
	}
	browserPath := writeFakeArtifactExportBrowserCommand(t, t.TempDir())
	t.Setenv("WORKMAX_WORKAGENT_BROWSER_BIN", browserPath)

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "pdf",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/pdf") {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="landing.pdf"`) {
		t.Fatalf("content-disposition = %q, want landing.pdf", cd)
	}
	if !strings.HasPrefix(string(w.Body.Bytes()), "%PDF") {
		t.Fatalf("body = %q, want PDF signature", w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, want exported after PDF render", reloaded.Status)
	}
}

func TestExportArtifact_RendersPNGWithBrowserCommand(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-png-worker")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><head><meta name="viewport" content="width=device-width"></head><body><main>PNG</main></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-png-worker", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(42, thread.Id, artifact.Id, []workagentService.ArtifactHTMLValidationIssue{
		{Code: "browser_validation_passed", Severity: "info", Message: "browser validation passed", Source: "browser_validation"},
	}); err != nil {
		t.Fatalf("seed browser validation diagnostics: %v", err)
	}
	browserPath := writeFakeArtifactExportBrowserCommand(t, t.TempDir())
	t.Setenv("WORKMAX_WORKAGENT_BROWSER_BIN", browserPath)

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "png",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "image/png") {
		t.Fatalf("content-type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="landing.png"`) {
		t.Fatalf("content-disposition = %q, want landing.png", cd)
	}
	if !bytes.HasPrefix(w.Body.Bytes(), []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		t.Fatalf("body = %q, want PNG signature", w.Body.String())
	}
}

func TestExportArtifact_ReportsStaticRenderSignatureMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	workspace := filepath.Join(".", "agent_workspace", "test-export-png-bad-signature")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "landing.html"), []byte(`<html><head><meta name="viewport" content="width=device-width"></head><body><main>PNG</main></body></html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "landing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-png-bad-signature", "landing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(42, thread.Id, artifact.Id, []workagentService.ArtifactHTMLValidationIssue{
		{Code: "browser_validation_passed", Severity: "info", Message: "browser validation passed", Source: "browser_validation"},
	}); err != nil {
		t.Fatalf("seed browser validation diagnostics: %v", err)
	}
	t.Setenv("WORKMAX_WORKAGENT_BROWSER_BIN", writeFakeBadArtifactExportBrowserCommand(t, t.TempDir()))

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "png",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "html_static_render_failed" || failure.Target != "png" || failure.Reason != "render_output_signature_mismatch" {
		t.Fatalf("failure = %#v", failure)
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status == workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, should not be exported after failed PNG render", reloaded.Status)
	}
}

func TestHTMLStaticRenderFailureReason_ClassifiesOutputDirFailure(t *testing.T) {
	got := htmlStaticRenderFailureReason(errors.New("browser command renderer: create output dir: permission denied"))
	if got != "render_output_write_failed" {
		t.Fatalf("reason = %q, want render_output_write_failed", got)
	}
}

func TestCreateArtifactExportJob_QueuesPDFRenderJob(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML export job thread")
	workspace := filepath.Join(".", "agent_workspace", "test-export-job-pdf")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "page.html"), []byte(`<!doctype html>
<html>
<head>
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <style>.artboard{width:1200px;aspect-ratio:16/9}</style>
</head>
<body><main class="artboard">Export job ready</main></body>
</html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "page.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-job-pdf", "page.html")),
		FileSize:   1024,
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export-jobs", map[string]any{
		"target": "pdf",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Job     workagentModel.ArtifactExportJob           `json:"job"`
			JobPlan workagentService.ArtifactHTMLExportJobPlan `json:"jobPlan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Job.Target != "pdf" || body.Data.Job.Worker != "browser_static_render" {
		t.Fatalf("unexpected job: %+v", body.Data.Job)
	}
	if body.Data.Job.Status != workagentModel.ArtifactExportJobStatusQueued {
		t.Fatalf("job status = %q, want queued", body.Data.Job.Status)
	}
	if body.Data.Job.Reason != "render_static_worker_unavailable" {
		t.Fatalf("job reason = %q, want render_static_worker_unavailable", body.Data.Job.Reason)
	}
	if body.Data.JobPlan.Status != workagentService.ArtifactHTMLExportJobWorkerPending {
		t.Fatalf("job plan status = %q, want worker_pending", body.Data.JobPlan.Status)
	}
	var reloaded workagentModel.ArtifactExportJob
	if err := db.First(&reloaded, body.Data.Job.Id).Error; err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if reloaded.ArtifactID != artifact.Id || reloaded.ThreadFileID != file.Id {
		t.Fatalf("job linked artifact/file = %d/%d, want %d/%d", reloaded.ArtifactID, reloaded.ThreadFileID, artifact.Id, file.Id)
	}

	get := getRequest(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export-jobs/"+uintToStr(body.Data.Job.Id))
	if get.Code != http.StatusOK {
		t.Fatalf("get job status = %d body=%s", get.Code, get.Body.String())
	}
	var getBody struct {
		Data workagentModel.ArtifactExportJob `json:"data"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &getBody); err != nil {
		t.Fatalf("decode get job: %v", err)
	}
	if getBody.Data.Id != body.Data.Job.Id || getBody.Data.Status != workagentModel.ArtifactExportJobStatusQueued {
		t.Fatalf("get job = %+v, want id=%d queued", getBody.Data, body.Data.Job.Id)
	}
}

func TestCreateArtifactExportJob_UsesPersistedBrowserDiagnostics(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML export blocked job thread")
	workspace := filepath.Join(".", "agent_workspace", "test-export-job-browser-diagnostics")
	t.Cleanup(func() { _ = os.RemoveAll(workspace) })
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "page.html"), []byte(`<!doctype html>
<html>
<head><meta name="viewport" content="width=device-width"></head>
<body><main style="width:1200px;aspect-ratio:16/9">Export job blocked</main></body>
</html>`), 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "page.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-job-browser-diagnostics", "page.html")),
		FileSize:   1024,
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(42, thread.Id, artifact.Id, []workagentService.ArtifactHTMLValidationIssue{
		{Code: "browser_resource_missing", Severity: "error", Message: "missing ./hero.png", Source: "browser_validation"},
	}); err != nil {
		t.Fatalf("seed browser diagnostics: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export-jobs", map[string]any{
		"target": "pdf",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Job     workagentModel.ArtifactExportJob           `json:"job"`
			JobPlan workagentService.ArtifactHTMLExportJobPlan `json:"jobPlan"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Job.Status != workagentModel.ArtifactExportJobStatusBlocked {
		t.Fatalf("job status = %q, want blocked; job=%+v", body.Data.Job.Status, body.Data.Job)
	}
	if body.Data.Job.Reason != "browser_resource_missing" {
		t.Fatalf("job reason = %q, want browser_resource_missing", body.Data.Job.Reason)
	}
	if body.Data.JobPlan.Status != workagentService.ArtifactHTMLExportJobBlocked || body.Data.JobPlan.Reason != "browser_resource_missing" {
		t.Fatalf("job plan = %+v, want blocked browser_resource_missing", body.Data.JobPlan)
	}
}

func TestCreateArtifactExportJob_ReturnsStructuredFailureForMissingArtifactFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "HTML export missing job file thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "missing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-job-missing-file", "missing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export-jobs", map[string]any{
		"target": "mp4",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "artifact_file_not_found" || failure.Target != "mp4" {
		t.Fatalf("failure = %#v", failure)
	}
	var jobs []workagentModel.ArtifactExportJob
	if err := db.Find(&jobs).Error; err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs = %d, want none after create failure", len(jobs))
	}
}

func TestExportArtifact_RejectsNonHTMLArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "image/png",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Artifact is not an HTML artifact") {
		t.Fatalf("body = %s", w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "not_html_artifact" || failure.Target != "zip" || failure.Reason != "png" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestExportArtifact_RejectsMissingHTMLArtifactFile(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "missing.html",
		FilePath:   filepath.ToSlash(filepath.Join("test-export-missing-file", "missing.html")),
		FileSource: workagentModel.FileSourceOutput,
		MimeType:   "text/html",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/export", map[string]any{
		"target": "zip",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	failure := decodeExportArtifactFailure(t, w.Body.Bytes())
	if failure.Code != "artifact_file_not_found" || failure.Target != "zip" {
		t.Fatalf("failure = %#v", failure)
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status == workagentModel.ArtifactStatusExported {
		t.Fatalf("status = %q, should not be exported after missing artifact file", reloaded.Status)
	}
}

func TestUpdateArtifactStatus_UpdatesOwnedRegistryRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/status", map[string]any{
		"status":      "approved",
		"reviewState": "approved",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactStatusApproved {
		t.Fatalf("status = %q, want approved", reloaded.Status)
	}
	if reloaded.ReviewState != workagentModel.ArtifactReviewApproved {
		t.Fatalf("review = %q, want approved", reloaded.ReviewState)
	}
}

func unzipArtifactAPIResponse(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string][]byte{}
	for _, file := range zr.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatalf("open %s: %v", file.Name, err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			_ = rc.Close()
			t.Fatalf("read %s: %v", file.Name, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close %s: %v", file.Name, err)
		}
		out[file.Name] = buf.Bytes()
	}
	return out
}

func zipFileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decodeExportArtifactFailure(t *testing.T, raw []byte) exportArtifactFailure {
	t.Helper()
	var body struct {
		Code    int                   `json:"code"`
		Data    exportArtifactFailure `json:"data"`
		Message string                `json:"message"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode failure response: %v", err)
	}
	if body.Code != 0 {
		t.Fatalf("response code = %d, want failure; body=%s", body.Code, string(raw))
	}
	return body.Data
}

func writeFakeArtifactExportBrowserCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-browser")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --print-to-pdf=*) out="${arg#--print-to-pdf=}"; printf '%%PDF-1.4\nfake-pdf\n%%%%EOF\n' > "$out" ;;
    --screenshot=*) out="${arg#--screenshot=}"; printf '\211PNG\r\n\032\nfake-png' > "$out" ;;
  esac
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	return path
}

func writeFakeBadArtifactExportBrowserCommand(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-bad-browser")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --print-to-pdf=*) out="${arg#--print-to-pdf=}"; printf 'not-a-pdf' > "$out" ;;
    --screenshot=*) out="${arg#--screenshot=}"; printf 'not-a-png' > "$out" ;;
  esac
done
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bad browser: %v", err)
	}
	return path
}

func TestUpdateArtifactComparison_UpdatesOwnedRegistryRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/comparison", map[string]any{
		"source":   "manual_compare",
		"summary":  "Latest improves hierarchy but weakens brand contrast.",
		"decision": "revise",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.ComparisonSource != "manual_compare" {
		t.Fatalf("comparison source = %q, want manual_compare", reloaded.ComparisonSource)
	}
	if reloaded.ComparisonDecision != "revise" {
		t.Fatalf("comparison decision = %q, want revise", reloaded.ComparisonDecision)
	}
	if reloaded.ComparisonSummary != "Latest improves hierarchy but weakens brand contrast." {
		t.Fatalf("comparison summary = %q", reloaded.ComparisonSummary)
	}
}

func TestApplyArtifactDecision_UpdatesOwnedRegistryRow(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/decision", map[string]any{
		"decision": "keep",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactRegistry
	if err := db.First(&reloaded, artifact.Id).Error; err != nil {
		t.Fatalf("reload artifact: %v", err)
	}
	if reloaded.ComparisonDecision != "keep" {
		t.Fatalf("decision = %q, want keep", reloaded.ComparisonDecision)
	}
	if reloaded.Status != workagentModel.ArtifactStatusFinal {
		t.Fatalf("status = %q, want final", reloaded.Status)
	}
}

func TestCreateArtifactAssetCandidate_RequiresFinalArtifactAndStoresProfile(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateLifecycle(42, thread.Id, artifact.Id, workagentModel.ArtifactStatusFinal, workagentModel.ArtifactReviewApproved); err != nil {
		t.Fatalf("finalize artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/asset-candidate", map[string]any{
		"assetKind": "brand",
		"name":      "Acme Poster System",
		"slug":      "acme-poster-system",
		"profile": map[string]any{
			"intendedUse": "poster references",
			"constraints": []string{"keep logo lockup"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.Where("artifact_id = ? AND asset_kind = ?", artifact.Id, "brand").First(&reloaded).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Name != "Acme Poster System" {
		t.Fatalf("name = %q", reloaded.Name)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusDraft {
		t.Fatalf("status = %q, want draft", reloaded.Status)
	}
	if !strings.Contains(reloaded.ProfileJSON, "poster references") {
		t.Fatalf("profile json = %q", reloaded.ProfileJSON)
	}

	w = postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/asset-candidate", map[string]any{
		"assetKind": workagentModel.ArtifactAssetKindDesignSystem,
		"name":      "Acme Poster Design System",
		"slug":      "acme-poster-design-system",
		"profile": map[string]any{
			"schema": []string{"color", "typography", "layout", "motion"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("design system candidate status = %d body=%s", w.Code, w.Body.String())
	}
	var designSystemCandidate workagentModel.ArtifactAssetCandidate
	if err := db.Where("artifact_id = ? AND asset_kind = ?", artifact.Id, workagentModel.ArtifactAssetKindDesignSystem).First(&designSystemCandidate).Error; err != nil {
		t.Fatalf("reload design system candidate: %v", err)
	}
	if designSystemCandidate.Name != "Acme Poster Design System" {
		t.Fatalf("design system candidate name = %q", designSystemCandidate.Name)
	}
}

func TestAgentTurnCaptureAssetCandidate_UpsertsFromStructuredReply(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "poster.png",
		FilePath:   "outputs/poster.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := workagentService.NewArtifactRegistryRepository(db).UpdateLifecycle(42, thread.Id, artifact.Id, workagentModel.ArtifactStatusFinal, workagentModel.ArtifactReviewApproved); err != nil {
		t.Fatalf("finalize artifact: %v", err)
	}

	cb := &agentTurnCallbacks{
		chatThread:       thread,
		uid:              42,
		assetCandidateID: artifact.Id,
		accumulatedText: strings.Join([]string{
			"- asset_kind: reference",
			"- name: Poster Reference",
			"- slug: poster-reference",
			"- intended use: future poster variants",
			"- visual guidance: keep the bold headline and image crop",
		}, "\n"),
	}
	cb.captureArtifactAssetCandidate()

	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.Where("artifact_id = ? AND asset_kind = ?", artifact.Id, workagentModel.ArtifactAssetKindReference).First(&reloaded).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Name != "Poster Reference" || reloaded.Slug != "poster-reference" {
		t.Fatalf("candidate = %#v", reloaded)
	}
	if !strings.Contains(reloaded.ProfileJSON, "future poster variants") {
		t.Fatalf("profile json = %q", reloaded.ProfileJSON)
	}
}

func TestCreateArtifactAssetCandidate_RejectsDraftArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:        42,
		ThreadID:   thread.Id,
		FileName:   "draft.png",
		FilePath:   "outputs/draft.png",
		FileSource: workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	artifact, err := workagentService.NewArtifactRegistryRepository(db).UpsertFromThreadFile(&file)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := postJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/artifacts/artifact-"+uintToStr(artifact.Id)+"/asset-candidate", map[string]any{
		"assetKind": "brand",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "artifact must be final or exported") {
		t.Fatalf("expected draft rejection, got %s", body)
	}
}

func TestListAndUpdateArtifactAssetCandidates(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   7,
		ThreadFileID: 70,
		AssetKind:    workagentModel.ArtifactAssetKindBrand,
		Name:         "Poster Reference",
		Slug:         "poster-reference",
		ProfileJSON:  `{"intendedUse":"reference"}`,
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if err := db.Create(&workagentModel.ArtifactAssetCandidate{
		UID:          99,
		ThreadID:     thread.Id,
		ArtifactID:   8,
		ThreadFileID: 80,
		AssetKind:    workagentModel.ArtifactAssetKindBrand,
		Name:         "Other User",
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}).Error; err != nil {
		t.Fatalf("seed cross tenant candidate: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := getRequest(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []workagentModel.ArtifactAssetCandidate `json:"items"`
			Count int                                     `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if body.Data.Count != 1 || len(body.Data.Items) != 1 {
		t.Fatalf("count/items = %d/%d want 1/1", body.Data.Count, len(body.Data.Items))
	}
	if body.Data.Items[0].Name != "Poster Reference" {
		t.Fatalf("candidate name = %q", body.Data.Items[0].Name)
	}

	w = patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.First(&reloaded, candidate.Id).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		t.Fatalf("status = %q, want confirmed", reloaded.Status)
	}
}

func TestConfirmReferenceArtifactAssetCandidate_MaterializesGlobalAsset(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Artifact thread")
	file := workagentModel.ThreadFile{
		UID:         42,
		ThreadID:    thread.Id,
		FileName:    "reference.png",
		DisplayName: "reference.png",
		FilePath:    "outputs/reference.png",
		FileSize:    2048,
		MimeType:    "image/png",
		FileSource:  workagentModel.FileSourceOutput,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed file: %v", err)
	}
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   7,
		ThreadFileID: file.Id,
		AssetKind:    workagentModel.ArtifactAssetKindReference,
		Name:         "Reference Asset",
		Slug:         "reference-asset",
		ProfileJSON:  `{"intendedUse":"reference"}`,
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.First(&reloaded, candidate.Id).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		t.Fatalf("status = %q, want confirmed", reloaded.Status)
	}
	if reloaded.TargetKind != workagentModel.ArtifactAssetCandidateTargetGlobalAsset {
		t.Fatalf("target kind = %q, want global_asset", reloaded.TargetKind)
	}
	if reloaded.TargetID == 0 {
		t.Fatalf("target id should be populated")
	}
	var reloadedFile workagentModel.ThreadFile
	if err := db.First(&reloadedFile, file.Id).Error; err != nil {
		t.Fatalf("reload file: %v", err)
	}
	if reloadedFile.GlobalAssetID != reloaded.TargetID {
		t.Fatalf("file global asset = %d, want %d", reloadedFile.GlobalAssetID, reloaded.TargetID)
	}
	w = patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("second patch status = %d body=%s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.GlobalAsset{}).Count(&count).Error; err != nil {
		t.Fatalf("count global assets: %v", err)
	}
	if count != 1 {
		t.Fatalf("global asset count = %d, want idempotent single asset", count)
	}
}

func TestConfirmTypedArtifactAssetCandidates_MaterializesPlatformAssets(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Typed asset candidate thread")
	candidates := []workagentModel.ArtifactAssetCandidate{
		{
			UID:          42,
			ThreadID:     thread.Id,
			ArtifactID:   71,
			ThreadFileID: 701,
			AssetKind:    workagentModel.ArtifactAssetKindBrand,
			Name:         "Acme Brand",
			Slug:         "acme-brand",
			ProfileJSON:  `{"colors":{"primary":"#111111"},"typography":{"display":"Inter"},"voice":{"tone":"direct"}}`,
			Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
		},
		{
			UID:          42,
			ThreadID:     thread.Id,
			ArtifactID:   72,
			ThreadFileID: 702,
			AssetKind:    workagentModel.ArtifactAssetKindCharacter,
			Name:         "Mira",
			Slug:         "mira",
			ProfileJSON:  `{"role":"protagonist","appearance":{"hair":"black bob"},"personality":["curious"],"identityAnchors":{"face":"round"}}`,
			Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
		},
		{
			UID:          42,
			ThreadID:     thread.Id,
			ArtifactID:   73,
			ThreadFileID: 703,
			AssetKind:    workagentModel.ArtifactAssetKindProduct,
			Name:         "Nova Lamp",
			Slug:         "nova-lamp",
			ProfileJSON:  `{"sku":"NL-01","category":"lighting","description":"portable desk lamp","specs":{"finish":"matte"},"visualGuidance":{"angle":"3/4"}}`,
			Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
		},
		{
			UID:          42,
			ThreadID:     thread.Id,
			ArtifactID:   74,
			ThreadFileID: 704,
			AssetKind:    workagentModel.ArtifactAssetKindDirectorStyle,
			Name:         "Soft Editorial",
			Slug:         "soft-editorial",
			ProfileJSON:  `{"genre":"editorial","composition":{"grid":"centered"},"lighting":{"key":"softbox"},"motion":{"pace":"slow"}}`,
			Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
		},
	}
	for i := range candidates {
		if err := db.Create(&candidates[i]).Error; err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}

	engine := buildArtifactEngine(t, 42)
	for _, candidate := range candidates {
		w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
			"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
		})
		if w.Code != http.StatusOK {
			t.Fatalf("confirm %s status = %d body=%s", candidate.AssetKind, w.Code, w.Body.String())
		}
		var reloaded workagentModel.ArtifactAssetCandidate
		if err := db.First(&reloaded, candidate.Id).Error; err != nil {
			t.Fatalf("reload %s candidate: %v", candidate.AssetKind, err)
		}
		if reloaded.TargetKind != candidate.AssetKind || reloaded.TargetID == 0 {
			t.Fatalf("%s target = %q/%d, want typed asset target", candidate.AssetKind, reloaded.TargetKind, reloaded.TargetID)
		}
	}

	var brand model.Brand
	if err := db.Where("uid = ? AND slug = ?", 42, "acme-brand").First(&brand).Error; err != nil {
		t.Fatalf("load materialized brand: %v", err)
	}
	if !brand.Confirmed || brand.Colors["primary"] != "#111111" {
		t.Fatalf("brand = %+v, want confirmed colors", brand)
	}
	var character model.Character
	if err := db.Where("uid = ? AND slug = ?", 42, "mira").First(&character).Error; err != nil {
		t.Fatalf("load materialized character: %v", err)
	}
	if !character.Confirmed || !strings.Contains(character.Appearance, "black bob") {
		t.Fatalf("character = %+v, want confirmed appearance", character)
	}
	var product model.Product
	if err := db.Where("uid = ? AND slug = ?", 42, "nova-lamp").First(&product).Error; err != nil {
		t.Fatalf("load materialized product: %v", err)
	}
	if !product.Confirmed || product.SKU != "NL-01" || product.Specs["finish"] != "matte" {
		t.Fatalf("product = %+v, want confirmed specs", product)
	}
	var director model.DirectorStyle
	if err := db.Where("uid = ? AND slug = ?", 42, "soft-editorial").First(&director).Error; err != nil {
		t.Fatalf("load materialized director style: %v", err)
	}
	if !director.Confirmed || director.Motion["pace"] != "slow" {
		t.Fatalf("director style = %+v, want confirmed motion", director)
	}

	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidates[0].Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("second brand confirm status = %d body=%s", w.Code, w.Body.String())
	}
	var brandCount int64
	if err := db.Model(&model.Brand{}).Where("uid = ? AND slug = ?", 42, "acme-brand").Count(&brandCount).Error; err != nil {
		t.Fatalf("count brands: %v", err)
	}
	if brandCount != 1 {
		t.Fatalf("brand count = %d, want idempotent single asset", brandCount)
	}
}

func TestConfirmPromptAssetCandidate_MaterializesPromptAsset(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Prompt asset candidate thread")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   81,
		ThreadFileID: 801,
		AssetKind:    workagentModel.ArtifactAssetKindPromptAsset,
		Name:         "Product Hero Prompt",
		Slug:         "product-hero-prompt",
		ProfileJSON:  `{"promptContent":"hero product shot, soft light","negativePrompt":"blur"}`,
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed prompt candidate: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("confirm prompt asset status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.First(&reloaded, candidate.Id).Error; err != nil {
		t.Fatalf("reload prompt candidate: %v", err)
	}
	if reloaded.TargetKind != workagentModel.ArtifactAssetCandidateTargetPromptAsset || reloaded.TargetID == 0 {
		t.Fatalf("prompt target = %q/%d, want prompt asset target", reloaded.TargetKind, reloaded.TargetID)
	}
	var promptAsset workagentModel.PromptAsset
	if err := db.First(&promptAsset, reloaded.TargetID).Error; err != nil {
		t.Fatalf("load prompt asset: %v", err)
	}
	if promptAsset.CandidateID != candidate.Id || promptAsset.ProjectID != thread.ProjectID {
		t.Fatalf("prompt asset traceability = candidate %d project %d", promptAsset.CandidateID, promptAsset.ProjectID)
	}
	if promptAsset.Prompt != "hero product shot, soft light" || promptAsset.NegativePrompt != "blur" {
		t.Fatalf("prompt content = %q negative=%q", promptAsset.Prompt, promptAsset.NegativePrompt)
	}
}

func TestConfirmDesignSystemArtifactAssetCandidate_MarksProjectSystemTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Design system thread")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   7,
		ThreadFileID: 70,
		AssetKind:    workagentModel.ArtifactAssetKindDesignSystem,
		Name:         "Poster Design System",
		Slug:         "poster-design-system",
		ProfileJSON:  validDesignSystemCandidateProfileJSON(),
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.First(&reloaded, candidate.Id).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		t.Fatalf("status = %q, want confirmed", reloaded.Status)
	}
	if reloaded.TargetKind != workagentModel.ArtifactAssetCandidateTargetDesignSystem {
		t.Fatalf("target kind = %q, want design_system", reloaded.TargetKind)
	}
	if reloaded.TargetID == 0 {
		t.Fatalf("target id = %d, want materialized project design system id", reloaded.TargetID)
	}
	var designSystem workagentModel.ProjectDesignSystem
	if err := db.First(&designSystem, reloaded.TargetID).Error; err != nil {
		t.Fatalf("load materialized design system: %v", err)
	}
	if designSystem.CandidateID != candidate.Id || designSystem.Basename == "" || !strings.Contains(designSystem.Body, "## 1. Color") {
		t.Fatalf("design system = %+v, want candidate-backed 9-section system", designSystem)
	}
	if designSystem.Version != 1 {
		t.Fatalf("design system version = %d, want 1", designSystem.Version)
	}
}

func TestConfirmDesignSystemArtifactAssetCandidate_RejectsIncompleteProfile(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Design system thread")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   7,
		ThreadFileID: 70,
		AssetKind:    workagentModel.ArtifactAssetKindDesignSystem,
		Name:         "Incomplete Design System",
		Slug:         "incomplete-design-system",
		ProfileJSON:  `{"color":"poster red","layout":"hero first"}`,
		Status:       workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	engine := buildArtifactEngine(t, 42)
	w := patchJSON(engine, "/conversations/"+uintToStr(thread.Id)+"/asset-candidates/"+uintToStr(candidate.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "designSystemMarkdown") {
		t.Fatalf("expected designSystemMarkdown validation failure, got %s", w.Body.String())
	}
	var reloaded workagentModel.ArtifactAssetCandidate
	if err := db.First(&reloaded, candidate.Id).Error; err != nil {
		t.Fatalf("reload candidate: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusDraft {
		t.Fatalf("status = %q, want draft after rejected confirm", reloaded.Status)
	}
}

func TestGetConversationArtifacts_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildArtifactEngine(t, 42)

	w := getRequest(engine, "/conversations/not-a-number/artifacts")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !json.Valid(w.Body.Bytes()) {
		t.Fatalf("response should stay JSON: %s", w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "Invalid thread ID") {
		t.Fatalf("expected invalid-id failure body, got %s", body)
	}
}

func containsStringArtifactTest(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasAPIDiagnostic(values []workagentService.ArtifactHTMLValidationIssue, code string, source string) bool {
	for _, value := range values {
		if value.Code == code && value.Source == source {
			return true
		}
	}
	return false
}
