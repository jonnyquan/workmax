package workagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

type fakeHTMLBrowserValidator struct {
	diagnostics []ArtifactHTMLValidationIssue
	err         error
	seen        *HTMLBrowserValidationInput
}

func (v *fakeHTMLBrowserValidator) ValidateHTMLInBrowser(_ context.Context, input HTMLBrowserValidationInput) ([]ArtifactHTMLValidationIssue, error) {
	if v.seen != nil {
		*v.seen = input
	}
	return v.diagnostics, v.err
}

func TestArtifactBrowserValidationWorker_ValidateArtifactWritesDiagnostics(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedBrowserValidationThread(t, db)
	sourceFile := seedBrowserValidationHTMLFile(t, db, workspaceRoot, thread)
	artifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if _, err := NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(thread.UID, thread.Id, artifact.Id, []ArtifactHTMLValidationIssue{
		{Code: "console_warn", Severity: "warn", Message: "layout warning", Source: "preview_runtime"},
		{Code: "old_browser_issue", Severity: "warn", Message: "old", Source: "browser_validation"},
	}); err != nil {
		t.Fatalf("seed diagnostics: %v", err)
	}

	var seen HTMLBrowserValidationInput
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)
	worker := NewArtifactBrowserValidationWorker(ArtifactBrowserValidationWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Validator: &fakeHTMLBrowserValidator{
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "text_overflow", Severity: "warn", Message: "headline clips"},
			},
			seen: &seen,
		},
	})
	result, err := worker.ValidateArtifact(context.Background(), thread.UID, thread.Id, artifact.Id)
	if err != nil {
		t.Fatalf("validate artifact: %v", err)
	}
	if result == nil || result.Skipped {
		t.Fatalf("result = %+v, want processed", result)
	}
	if seen.Plan.Status != ArtifactHTMLBrowserValidationPending || !containsString(seen.Plan.Targets, "png") {
		t.Fatalf("seen plan = %+v, want pending png validation", seen.Plan)
	}
	if len(result.Diagnostics) != 2 || !hasDiagnostic(result.Diagnostics, "browser_validation_passed", "browser_validation") {
		t.Fatalf("diagnostics = %#v, want normalized browser validation diagnostic", result.Diagnostics)
	}
	var updated workagentModel.ArtifactRegistry
	if err := db.First(&updated, artifact.Id).Error; err != nil {
		t.Fatalf("load updated artifact: %v", err)
	}
	merged := parseHTMLPreviewDiagnostics(updated.HTMLPreviewDiagnostics)
	if !hasDiagnostic(merged, "console_warn", "preview_runtime") {
		t.Fatalf("merged diagnostics lost preview runtime issue: %#v", merged)
	}
	if !hasDiagnostic(merged, "text_overflow", "browser_validation") {
		t.Fatalf("merged diagnostics missing browser validation issue: %#v", merged)
	}
	if !hasDiagnostic(merged, "browser_validation_passed", "browser_validation") {
		t.Fatalf("merged diagnostics missing pass marker: %#v", merged)
	}
	if hasDiagnostic(merged, "old_browser_issue", "browser_validation") {
		t.Fatalf("merged diagnostics kept stale browser validation issue: %#v", merged)
	}
	ev := rec.FindByEvent("wa_artifact_browser_validation")
	if ev == nil {
		t.Fatal("expected wa_artifact_browser_validation metric")
	}
	if ev.Fields["artifact_id"] != artifact.Id || ev.Fields["status"] != ArtifactHTMLBrowserValidationNotRequired {
		t.Fatalf("metric artifact/status fields = %#v", ev.Fields)
	}
	if ev.Fields["warn_count"] != 1 || ev.Fields["info_count"] != 1 || ev.Fields["error_count"] != 0 {
		t.Fatalf("metric diagnostic counts = %#v", ev.Fields)
	}
	if ev.Fields["target_count"] != 0 || ev.Fields["viewport_count"] != 3 {
		t.Fatalf("metric resolved plan counts = %#v", ev.Fields)
	}
}

func TestArtifactBrowserValidationWorker_ValidateArtifactSkipsCleanHTML(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedBrowserValidationThread(t, db)
	sourceFile := seedBrowserValidationCleanHTMLFile(t, db, workspaceRoot, thread)
	artifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	worker := NewArtifactBrowserValidationWorker(ArtifactBrowserValidationWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Validator:     &fakeHTMLBrowserValidator{diagnostics: []ArtifactHTMLValidationIssue{{Code: "should_not_run", Message: "no"}}},
	})
	result, err := worker.ValidateArtifact(context.Background(), thread.UID, thread.Id, artifact.Id)
	if err != nil {
		t.Fatalf("validate artifact: %v", err)
	}
	if result == nil || !result.Skipped || result.Plan == nil || result.Plan.Status != ArtifactHTMLBrowserValidationNotRequired {
		t.Fatalf("result = %+v, want not-required skip", result)
	}
}

func TestArtifactBrowserValidationWorker_ValidateArtifactClassifiesValidatorFailure(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name:     "timeout",
			err:      errors.New("browser command validator: layout probe timed out"),
			wantCode: "browser_validation_timeout",
		},
		{
			name:     "command failed",
			err:      errors.New("browser command validator: command failed: exit status 1"),
			wantCode: "browser_validation_command_failed",
		},
		{
			name:     "source missing",
			err:      errors.New("browser command validator: source path is required"),
			wantCode: "browser_validation_source_missing",
		},
		{
			name:     "invalid probe output",
			err:      errors.New("browser command validator: probe marker missing"),
			wantCode: "browser_validation_probe_output_invalid",
		},
		{
			name:     "generic validator failure",
			err:      errors.New("browser command validator: unexpected crash"),
			wantCode: "browser_validation_worker_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installSystemDBForPreflight(t, db)
			workspaceRoot := t.TempDir()
			thread := seedBrowserValidationThread(t, db)
			sourceFile := seedBrowserValidationHTMLFile(t, db, workspaceRoot, thread)
			artifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
			if err != nil {
				t.Fatalf("seed artifact: %v", err)
			}
			worker := NewArtifactBrowserValidationWorker(ArtifactBrowserValidationWorkerOptions{
				DB:            db,
				WorkspaceRoot: workspaceRoot,
				Validator:     &fakeHTMLBrowserValidator{err: tt.err},
			})
			result, err := worker.ValidateArtifact(context.Background(), thread.UID, thread.Id, artifact.Id)
			if err != nil {
				t.Fatalf("validate artifact: %v", err)
			}
			if result == nil || result.Skipped {
				t.Fatalf("result = %+v, want processed failure diagnostics", result)
			}
			if !hasDiagnostic(result.Diagnostics, tt.wantCode, "browser_validation") {
				t.Fatalf("diagnostics = %#v, want %s", result.Diagnostics, tt.wantCode)
			}
			if hasDiagnostic(result.Diagnostics, "browser_validation_passed", "browser_validation") {
				t.Fatalf("diagnostics = %#v, should not include pass marker after validator error", result.Diagnostics)
			}
			var updated workagentModel.ArtifactRegistry
			if err := db.First(&updated, artifact.Id).Error; err != nil {
				t.Fatalf("load updated artifact: %v", err)
			}
			if !hasDiagnostic(parseHTMLPreviewDiagnostics(updated.HTMLPreviewDiagnostics), tt.wantCode, "browser_validation") {
				t.Fatalf("persisted diagnostics missing %s: %s", tt.wantCode, updated.HTMLPreviewDiagnostics)
			}
		})
	}
}

func seedBrowserValidationThread(t *testing.T, db *gorm.DB) *workagentModel.ChatThread {
	t.Helper()
	thread := workagentModel.ChatThread{
		UID:  77,
		UUID: "browser-validation-thread",
		Name: "Browser validation thread",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return &thread
}

func seedBrowserValidationHTMLFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread) *workagentModel.ThreadFile {
	t.Helper()
	return seedBrowserValidationFile(t, db, workspaceRoot, thread, `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 100vw; height: 100vh; overflow: auto">Hello</main></body></html>`)
}

func seedBrowserValidationCleanHTMLFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread) *workagentModel.ThreadFile {
	t.Helper()
	return seedBrowserValidationFile(t, db, workspaceRoot, thread, `<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><main style="width: 1200px; aspect-ratio: 16/9">Hello</main></body></html>`)
}

func seedBrowserValidationFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, html string) *workagentModel.ThreadFile {
	t.Helper()
	relPath := "uid/77/20260520/thread_browser-validation-thread/outputs/page.html"
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create html dir: %v", err)
	}
	content := []byte(html)
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     "page.html",
		DisplayName:  "page.html",
		FileSize:     uint64(len(content)),
		FileType:     "html",
		MimeType:     "text/html",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "HTML artifact",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed html file: %v", err)
	}
	return &file
}

func hasDiagnostic(values []ArtifactHTMLValidationIssue, code string, source string) bool {
	for _, value := range values {
		if value.Code == code && value.Source == source {
			return true
		}
	}
	return false
}
