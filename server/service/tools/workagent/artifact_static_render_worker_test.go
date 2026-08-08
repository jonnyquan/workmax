package workagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

type fakeStaticRenderer struct {
	content []byte
	mime    string
	err     error
	seen    *HTMLStaticRenderInput
	calls   *int
}

func (r *fakeStaticRenderer) RenderStaticHTML(_ context.Context, input HTMLStaticRenderInput) (HTMLStaticRenderOutput, error) {
	if r.calls != nil {
		*r.calls = *r.calls + 1
	}
	if r.seen != nil {
		*r.seen = input
	}
	if r.err != nil {
		return HTMLStaticRenderOutput{}, r.err
	}
	return HTMLStaticRenderOutput{Content: r.content, MimeType: r.mime}, nil
}

func TestArtifactStaticRenderWorker_RunNextRendersPDFAndRegistersArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "pdf",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			Reason:          "render_static_worker_unavailable",
			OutputExtension: ".pdf",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	var seen HTMLStaticRenderInput
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer: &fakeStaticRenderer{
			content: []byte("%PDF-1.7 fake"),
			mime:    "application/pdf",
			seen:    &seen,
		},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || !result.Claimed {
		t.Fatal("expected worker to claim a job")
	}
	if result.Job.Id != job.Id || result.Job.Status != workagentModel.ArtifactExportJobStatusSucceeded {
		t.Fatalf("job = %+v, want succeeded job %d", result.Job, job.Id)
	}
	if result.OutputFile == nil || result.OutputFile.Id == 0 {
		t.Fatalf("missing output file: %+v", result.OutputFile)
	}
	if result.OutputFile.FileSource != workagentModel.FileSourceOutput {
		t.Fatalf("output source = %q, want output", result.OutputFile.FileSource)
	}
	if result.OutputFile.FileType != "pdf" || result.OutputFile.MimeType != "application/pdf" {
		t.Fatalf("output type/mime = %q/%q", result.OutputFile.FileType, result.OutputFile.MimeType)
	}
	assertRenderExportDescription(t, result.OutputFile.Description, "static", "pdf", job.Id, sourceArtifact.Id, sourceFile.Id, sourceFile.FilePath)
	if seen.Target != "pdf" || seen.SourceHTML == "" || seen.OutputFilePath != result.OutputFile.FilePath {
		t.Fatalf("renderer input = %+v", seen)
	}
	outputAbs := filepath.Join(workspaceRoot, filepath.FromSlash(result.OutputFile.FilePath))
	got, err := os.ReadFile(outputAbs)
	if err != nil {
		t.Fatalf("read rendered output: %v", err)
	}
	if string(got) != "%PDF-1.7 fake" {
		t.Fatalf("rendered bytes = %q", got)
	}
	var outputArtifact workagentModel.ArtifactRegistry
	if err := db.Where("thread_file_id = ?", result.OutputFile.Id).First(&outputArtifact).Error; err != nil {
		t.Fatalf("load output artifact: %v", err)
	}
	if outputArtifact.ArtifactType != "document" || outputArtifact.OutputType != "pdf" {
		t.Fatalf("output artifact = %+v, want document/pdf", outputArtifact)
	}
	if outputArtifact.Status != workagentModel.ArtifactStatusExported || outputArtifact.ReviewState != workagentModel.ArtifactReviewApproved {
		t.Fatalf("output artifact lifecycle = %q/%q, want exported/approved", outputArtifact.Status, outputArtifact.ReviewState)
	}
	if result.Job.OutputFileID != result.OutputFile.Id || result.Job.OutputPath != result.OutputFile.FilePath {
		t.Fatalf("job output = %d/%q, want %d/%q", result.Job.OutputFileID, result.Job.OutputPath, result.OutputFile.Id, result.OutputFile.FilePath)
	}
}

func TestArtifactStaticRenderWorker_RunNextRechecksExportReadiness(t *testing.T) {
	tests := []struct {
		name            string
		target          string
		diagnostics     []ArtifactHTMLValidationIssue
		sourceHTML      string
		wantReason      string
		wantMessagePart string
	}{
		{
			name:   "browser resource missing",
			target: "png",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_resource_missing", Severity: "error", Message: "missing ./hero.png", Source: "browser_validation"},
			},
			wantReason:      "browser_resource_missing",
			wantMessagePart: "browser_resource_missing",
		},
		{
			name:   "browser validation failed",
			target: "pdf",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "text_overflow", Severity: "error", Message: "headline clips", Source: "browser_validation"},
			},
			wantReason:      "browser_validation_failed",
			wantMessagePart: "browser_validation_failed",
		},
		{
			name:   "preview runtime error",
			target: "png",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "console", Severity: "error", Message: "ReferenceError: missing", Source: "preview_runtime"},
			},
			wantReason:      "preview_runtime_error",
			wantMessagePart: "preview_runtime_error",
		},
		{
			name:            "browser validation required after source changed",
			target:          "png",
			sourceHTML:      `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 100vw; height: 100vh; overflow: auto">Hello</main></body></html>`,
			wantReason:      "browser_validation_required",
			wantMessagePart: "browser_validation_required",
		},
		{
			name:            "remote asset added after job queued",
			target:          "pdf",
			sourceHTML:      `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; aspect-ratio: 16/9"><img src="https://cdn.example.com/hero.png" alt="Hero"></main></body></html>`,
			wantReason:      "remote_asset_reference",
			wantMessagePart: "remote_asset_reference",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installSystemDBForPreflight(t, db)
			workspaceRoot := t.TempDir()
			thread := seedStaticRenderThread(t, db)
			sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
			sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
			if err != nil {
				t.Fatalf("seed artifact: %v", err)
			}
			job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
				UID:          thread.UID,
				ThreadID:     thread.Id,
				ArtifactID:   sourceArtifact.Id,
				ThreadFileID: sourceFile.Id,
				Plan: ArtifactHTMLExportJobPlan{
					Target:          tt.target,
					Kind:            "render_static",
					Worker:          ArtifactStaticRenderWorkerName,
					Status:          ArtifactHTMLExportJobWorkerPending,
					Reason:          "render_static_worker_unavailable",
					OutputExtension: "." + tt.target,
				},
			})
			if err != nil {
				t.Fatalf("seed job: %v", err)
			}
			if len(tt.diagnostics) > 0 {
				if _, err := NewArtifactRegistryRepository(db).UpdateHTMLPreviewDiagnostics(thread.UID, thread.Id, sourceArtifact.Id, tt.diagnostics); err != nil {
					t.Fatalf("seed diagnostics: %v", err)
				}
			}
			if tt.sourceHTML != "" {
				absPath := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
				if err := os.WriteFile(absPath, []byte(tt.sourceHTML), 0o644); err != nil {
					t.Fatalf("rewrite source html: %v", err)
				}
			}

			calls := 0
			worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
				DB:            db,
				WorkspaceRoot: workspaceRoot,
				Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
			})
			result, err := worker.RunNext(context.Background())
			if err != nil {
				t.Fatalf("run worker: %v", err)
			}
			if result == nil || result.Job == nil || result.Job.Id != job.Id {
				t.Fatalf("result = %+v, want failed job %d", result, job.Id)
			}
			if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed {
				t.Fatalf("job status = %q, want failed", result.Job.Status)
			}
			if result.Job.Reason != tt.wantReason {
				t.Fatalf("job reason = %q, want %q; message=%q", result.Job.Reason, tt.wantReason, result.Job.ErrorMessage)
			}
			if tt.wantMessagePart != "" && !strings.Contains(result.Job.ErrorMessage, tt.wantMessagePart) {
				t.Fatalf("job message = %q, want containing %q", result.Job.ErrorMessage, tt.wantMessagePart)
			}
			if calls != 0 {
				t.Fatalf("renderer calls = %d, want 0 when export readiness is stale", calls)
			}
			if result.OutputFile != nil {
				t.Fatalf("output file = %+v, want nil on readiness failure", result.OutputFile)
			}
		})
	}
}

func TestArtifactStaticRenderWorker_RunNextMarksRendererFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{
			name:       "generic renderer failure",
			err:        errors.New("browser crashed"),
			wantReason: "worker_failed",
		},
		{
			name:       "renderer signature mismatch",
			err:        errors.New("browser command renderer: output does not look like png"),
			wantReason: "render_output_signature_mismatch",
		},
		{
			name:       "renderer timeout",
			err:        errors.New("browser command renderer: render timed out after 1m0s"),
			wantReason: "render_timeout",
		},
		{
			name:       "renderer command failed",
			err:        errors.New("browser command renderer: command failed: exit status 1: crashed"),
			wantReason: "render_command_failed",
		},
		{
			name:       "renderer output read failed",
			err:        errors.New("browser command renderer: read output: no such file or directory"),
			wantReason: "render_output_read_failed",
		},
		{
			name:       "renderer output dir create failed",
			err:        errors.New("browser command renderer: create output dir: permission denied"),
			wantReason: "render_output_write_failed",
		},
		{
			name:       "thread file register failed",
			err:        errors.New("static render worker: create static render output file: database is locked"),
			wantReason: "render_output_file_register_failed",
		},
		{
			name:       "artifact register failed",
			err:        errors.New("static render worker: register static render output artifact: database is locked"),
			wantReason: "render_output_artifact_register_failed",
		},
		{
			name:       "artifact lifecycle update failed",
			err:        errors.New("static render worker: mark static render output artifact exported: database is locked"),
			wantReason: "render_output_lifecycle_update_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installSystemDBForPreflight(t, db)
			workspaceRoot := t.TempDir()
			thread := seedStaticRenderThread(t, db)
			sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
			sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
			if err != nil {
				t.Fatalf("seed artifact: %v", err)
			}
			job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
				UID:          thread.UID,
				ThreadID:     thread.Id,
				ArtifactID:   sourceArtifact.Id,
				ThreadFileID: sourceFile.Id,
				Plan: ArtifactHTMLExportJobPlan{
					Target:          "png",
					Kind:            "render_static",
					Worker:          ArtifactStaticRenderWorkerName,
					Status:          ArtifactHTMLExportJobWorkerPending,
					OutputExtension: ".png",
				},
			})
			if err != nil {
				t.Fatalf("seed job: %v", err)
			}

			worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
				DB:            db,
				WorkspaceRoot: workspaceRoot,
				Renderer:      &fakeStaticRenderer{err: tt.err},
			})
			result, err := worker.RunNext(context.Background())
			if err != nil {
				t.Fatalf("run worker: %v", err)
			}
			if result == nil || result.Job == nil {
				t.Fatal("expected failed job result")
			}
			if result.Job.Id != job.Id || result.Job.Status != workagentModel.ArtifactExportJobStatusFailed {
				t.Fatalf("job = %+v, want failed", result.Job)
			}
			if result.Job.Reason != tt.wantReason || result.Job.ErrorMessage == "" {
				t.Fatalf("failure reason/message = %q/%q, want reason %q", result.Job.Reason, result.Job.ErrorMessage, tt.wantReason)
			}
			if result.OutputFile != nil {
				t.Fatalf("output file = %+v, want nil on failure", result.OutputFile)
			}
		})
	}
}

func TestArtifactStaticRenderWorker_RunNextFailsLoudlyWhenSourceHTMLMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := os.Remove(filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))); err != nil {
		t.Fatalf("remove source html: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "source_file_read_failed" {
		t.Fatalf("job status/reason = %q/%q, want failed/source_file_read_failed", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "read source html") {
		t.Fatalf("job error = %q, want source read detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 when source file is missing", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil when source file is missing", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsSourceOutsideWorkspace(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	if err := db.Model(sourceFile).Update("file_path", "../outside/page.html").Error; err != nil {
		t.Fatalf("move source outside workspace in db: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "source_file_outside_workspace" {
		t.Fatalf("job status/reason = %q/%q, want failed/source_file_outside_workspace", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "outside workspace") {
		t.Fatalf("job error = %q, want outside workspace detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 when source path escapes workspace", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil when source path escapes workspace", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsNonHTMLArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderImageFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "not_html_artifact" {
		t.Fatalf("job status/reason = %q/%q, want failed/not_html_artifact", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "not html") {
		t.Fatalf("job error = %q, want non-html detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 for non-html source", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil for non-html source", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsUnsupportedTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job := workagentModel.ArtifactExportJob{
		UID:             thread.UID,
		ThreadID:        thread.Id,
		ArtifactID:      sourceArtifact.Id,
		ThreadFileID:    sourceFile.Id,
		Target:          "jpg",
		Kind:            "render_static",
		Worker:          ArtifactStaticRenderWorkerName,
		Status:          workagentModel.ArtifactExportJobStatusQueued,
		OutputExtension: ".jpg",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed unsupported target job: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "unsupported_target" {
		t.Fatalf("job status/reason = %q/%q, want failed/unsupported_target", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "unsupported target") {
		t.Fatalf("job error = %q, want unsupported target detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 for unsupported target", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil for unsupported target", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsUnsupportedJobKind(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_motion",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "unsupported_job_kind" {
		t.Fatalf("job status/reason = %q/%q, want failed/unsupported_job_kind", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "unsupported job kind/worker") {
		t.Fatalf("job error = %q, want kind mismatch detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 for unsupported job kind", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil for unsupported job kind", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsOutputExtensionMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "pdf",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".html",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered"), calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "output_extension_mismatch" {
		t.Fatalf("job status/reason = %q/%q, want failed/output_extension_mismatch", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "output_extension_mismatch") {
		t.Fatalf("job error = %q, want mismatch detail", result.Job.ErrorMessage)
	}
	if calls != 0 {
		t.Fatalf("renderer calls = %d, want 0 for extension mismatch", calls)
	}
}

func TestArtifactStaticRenderWorker_RunNextRejectsMimeTypeMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("png bytes"), mime: "application/pdf"},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "mime_type_mismatch" {
		t.Fatalf("job status/reason = %q/%q, want failed/mime_type_mismatch", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "mime_type_mismatch") {
		t.Fatalf("job error = %q, want mismatch detail", result.Job.ErrorMessage)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil for MIME mismatch", result.OutputFile)
	}
}

func TestArtifactStaticRenderWorker_RunNextMarksOutputWriteFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job, err := NewArtifactExportJobRepository(db).CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		ArtifactID:   sourceArtifact.Id,
		ThreadFileID: sourceFile.Id,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "png",
			Kind:            "render_static",
			Worker:          ArtifactStaticRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".png",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	outputName := buildStaticRenderOutputName(sourceFile.FileName, "png", job.Id, ".png")
	outputAbs, _, err := resolveStaticRenderOutputPath(workspaceRoot, sourceAbs, outputName)
	if err != nil {
		t.Fatalf("resolve output path: %v", err)
	}
	if err := os.MkdirAll(outputAbs, 0o755); err != nil {
		t.Fatalf("prepare output path conflict: %v", err)
	}

	calls := 0
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("\x89PNG\r\n\x1a\nfake"), mime: "image/png", calls: &calls},
	})
	result, err := worker.RunNext(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Id != job.Id {
		t.Fatalf("result = %+v, want failed job %d", result, job.Id)
	}
	if result.Job.Status != workagentModel.ArtifactExportJobStatusFailed || result.Job.Reason != "render_output_write_failed" {
		t.Fatalf("job status/reason = %q/%q, want failed/render_output_write_failed", result.Job.Status, result.Job.Reason)
	}
	if !strings.Contains(result.Job.ErrorMessage, "write output file") {
		t.Fatalf("job error = %q, want write output detail", result.Job.ErrorMessage)
	}
	if calls != 1 {
		t.Fatalf("renderer calls = %d, want 1", calls)
	}
	if result.OutputFile != nil {
		t.Fatalf("output file = %+v, want nil for output write failure", result.OutputFile)
	}
}

func TestArtifactStaticRenderRunner_DrainOnceProcessesQueuedStaticJobs(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	jobRepo := NewArtifactExportJobRepository(db)
	for _, target := range []string{"pdf", "png"} {
		_, err := jobRepo.CreateFromHTMLJobPlan(ArtifactExportJobInput{
			UID:          thread.UID,
			ThreadID:     thread.Id,
			ArtifactID:   sourceArtifact.Id,
			ThreadFileID: sourceFile.Id,
			Plan: ArtifactHTMLExportJobPlan{
				Target:          target,
				Kind:            "render_static",
				Worker:          ArtifactStaticRenderWorkerName,
				Status:          ArtifactHTMLExportJobWorkerPending,
				OutputExtension: "." + target,
			},
		})
		if err != nil {
			t.Fatalf("seed %s job: %v", target, err)
		}
	}
	worker := NewArtifactStaticRenderWorker(ArtifactStaticRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeStaticRenderer{content: []byte("rendered")},
	})
	runner := NewArtifactStaticRenderRunner(worker, time.Hour, 5)
	processed := runner.drainOnce(context.Background())
	if processed != 2 {
		t.Fatalf("processed = %d, want 2", processed)
	}
	var succeeded int64
	if err := db.Model(&workagentModel.ArtifactExportJob{}).
		Where("status = ?", workagentModel.ArtifactExportJobStatusSucceeded).
		Count(&succeeded).Error; err != nil {
		t.Fatalf("count succeeded jobs: %v", err)
	}
	if succeeded != 2 {
		t.Fatalf("succeeded jobs = %d, want 2", succeeded)
	}
	var outputs int64
	if err := db.Model(&workagentModel.ThreadFile{}).
		Where("file_source = ? AND file_type IN ?", workagentModel.FileSourceOutput, []string{"pdf", "png"}).
		Count(&outputs).Error; err != nil {
		t.Fatalf("count outputs: %v", err)
	}
	if outputs != 2 {
		t.Fatalf("render outputs = %d, want 2", outputs)
	}
}

func seedStaticRenderThread(t *testing.T, db *gorm.DB) *workagentModel.ChatThread {
	t.Helper()
	thread := workagentModel.ChatThread{
		UID:  42,
		UUID: "static-render-thread",
		Name: "Static render thread",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return &thread
}

func seedStaticRenderHTMLFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread) *workagentModel.ThreadFile {
	t.Helper()
	relPath := "uid/42/20260520/thread_static-render-thread/outputs/page.html"
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create html dir: %v", err)
	}
	html := []byte(`<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><main style="width: 1200px; aspect-ratio: 16/9">Hello</main></body></html>`)
	if err := os.WriteFile(absPath, html, 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     "page.html",
		DisplayName:  "page.html",
		FileSize:     uint64(len(html)),
		FileType:     "html",
		MimeType:     "text/html",
		FilePath:     relPath,
		FileHash:     md5Hex(html),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "HTML artifact",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed html file: %v", err)
	}
	return &file
}

func seedStaticRenderImageFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread) *workagentModel.ThreadFile {
	t.Helper()
	relPath := "uid/42/20260520/thread_static-render-thread/outputs/poster.png"
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}
	content := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     "poster.png",
		DisplayName:  "poster.png",
		FileSize:     uint64(len(content)),
		FileType:     "png",
		MimeType:     "image/png",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "PNG artifact",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed image file: %v", err)
	}
	return &file
}

func assertRenderExportDescription(t *testing.T, raw string, renderKind string, target string, jobID uint, artifactID uint, fileID uint, filePath string) {
	t.Helper()
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("description must be render export JSON, got %q: %v", raw, err)
	}
	if payload["kind"] != "workagent_render_export" {
		t.Fatalf("description kind = %v, want workagent_render_export", payload["kind"])
	}
	if payload["render_kind"] != renderKind || payload["target"] != target {
		t.Fatalf("description render/target = %v/%v, want %s/%s", payload["render_kind"], payload["target"], renderKind, target)
	}
	if uint(payload["export_job_id"].(float64)) != jobID {
		t.Fatalf("description export_job_id = %v, want %d", payload["export_job_id"], jobID)
	}
	if uint(payload["source_artifact_id"].(float64)) != artifactID {
		t.Fatalf("description source_artifact_id = %v, want %d", payload["source_artifact_id"], artifactID)
	}
	if uint(payload["source_file_id"].(float64)) != fileID {
		t.Fatalf("description source_file_id = %v, want %d", payload["source_file_id"], fileID)
	}
	if payload["source_file_path"] != filePath {
		t.Fatalf("description source_file_path = %v, want %q", payload["source_file_path"], filePath)
	}
}
