package workagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

type fakeMotionRenderer struct {
	content []byte
	mime    string
	err     error
	seen    *HTMLMotionRenderInput
	calls   *int
}

func (r *fakeMotionRenderer) RenderMotionHTML(_ context.Context, input HTMLMotionRenderInput) (HTMLMotionRenderOutput, error) {
	if r.calls != nil {
		*r.calls = *r.calls + 1
	}
	if r.seen != nil {
		*r.seen = input
	}
	if r.err != nil {
		return HTMLMotionRenderOutput{}, r.err
	}
	return HTMLMotionRenderOutput{Content: r.content, MimeType: r.mime}, nil
}

func TestArtifactMotionRenderWorker_RunNextRendersMP4AndRegistersArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			Reason:          "render_motion_worker_unavailable",
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	var seen HTMLMotionRenderInput
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer: &fakeMotionRenderer{
			content: []byte("fake mp4 bytes"),
			mime:    "video/mp4",
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
	if result.OutputFile.FileType != "mp4" || result.OutputFile.MimeType != "video/mp4" {
		t.Fatalf("output type/mime = %q/%q", result.OutputFile.FileType, result.OutputFile.MimeType)
	}
	assertRenderExportDescription(t, result.OutputFile.Description, "motion", "mp4", job.Id, sourceArtifact.Id, sourceFile.Id, sourceFile.FilePath)
	if seen.Target != "mp4" || seen.SourceHTML == "" || seen.OutputFilePath != result.OutputFile.FilePath {
		t.Fatalf("renderer input = %+v", seen)
	}
	if seen.MotionSettings.DurationMs != 3500 || seen.MotionSettings.FPS != 24 || seen.MotionSettings.Width != 1080 || seen.MotionSettings.Height != 1920 {
		t.Fatalf("motion settings = %+v", seen.MotionSettings)
	}
	outputAbs := filepath.Join(workspaceRoot, filepath.FromSlash(result.OutputFile.FilePath))
	got, err := os.ReadFile(outputAbs)
	if err != nil {
		t.Fatalf("read rendered output: %v", err)
	}
	if string(got) != "fake mp4 bytes" {
		t.Fatalf("rendered bytes = %q", got)
	}
	var outputArtifact workagentModel.ArtifactRegistry
	if err := db.Where("thread_file_id = ?", result.OutputFile.Id).First(&outputArtifact).Error; err != nil {
		t.Fatalf("load output artifact: %v", err)
	}
	if outputArtifact.ArtifactType != "video" || outputArtifact.OutputType != "mp4" {
		t.Fatalf("output artifact = %+v, want video/mp4", outputArtifact)
	}
	if outputArtifact.Status != workagentModel.ArtifactStatusExported || outputArtifact.ReviewState != workagentModel.ArtifactReviewApproved {
		t.Fatalf("output artifact lifecycle = %q/%q, want exported/approved", outputArtifact.Status, outputArtifact.ReviewState)
	}
	if result.Job.OutputFileID != result.OutputFile.Id || result.Job.OutputPath != result.OutputFile.FilePath {
		t.Fatalf("job output = %d/%q, want %d/%q", result.Job.OutputFileID, result.Job.OutputPath, result.OutputFile.Id, result.OutputFile.FilePath)
	}
}

func TestArtifactMotionRenderWorker_RunNextRechecksExportReadiness(t *testing.T) {
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
			target: "mp4",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_resource_missing", Severity: "error", Message: "missing ./frame.png", Source: "browser_validation"},
			},
			wantReason:      "browser_resource_missing",
			wantMessagePart: "browser_resource_missing",
		},
		{
			name:   "browser validation failed",
			target: "gif",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "scroll_bounds", Severity: "error", Message: "content outside viewport", Source: "browser_validation"},
			},
			wantReason:      "browser_validation_failed",
			wantMessagePart: "browser_validation_failed",
		},
		{
			name:   "preview runtime error",
			target: "mp4",
			diagnostics: []ArtifactHTMLValidationIssue{
				{Code: "runtime_error", Severity: "error", Message: "TypeError: missing", Source: "preview_runtime"},
			},
			wantReason:      "preview_runtime_error",
			wantMessagePart: "preview_runtime_error",
		},
		{
			name:            "browser validation required after source changed",
			target:          "mp4",
			sourceHTML:      `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 100vw; height: 100vh; overflow: auto">Hello</main></body></html>`,
			wantReason:      "browser_validation_required",
			wantMessagePart: "browser_validation_required",
		},
		{
			name:            "remote asset added after job queued",
			target:          "gif",
			sourceHTML:      `<!doctype html><html><head><meta name="viewport" content="width=device-width"></head><body><main style="width: 1200px; aspect-ratio: 16/9"><img src="https://cdn.example.com/frame.png" alt="Frame"></main></body></html>`,
			wantReason:      "remote_asset_reference",
			wantMessagePart: "remote_asset_reference",
		},
		{
			name:   "motion timeline invalid after source changed",
			target: "mp4",
			sourceHTML: `<!doctype html><html><head>
<meta name="viewport" content="width=device-width">
<meta name="workmax:motion-duration-ms" content="10">
<meta name="workmax:motion-fps" content="240">
<style>
main { animation: fade 300ms ease; }
@keyframes fade { from { opacity: 0; } to { opacity: 1; } }
@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }
</style>
</head><body><main style="width: 1200px; aspect-ratio: 16/9">Motion</main></body></html>`,
			wantReason:      "motion_timeline_invalid",
			wantMessagePart: "motion_timeline_invalid",
		},
		{
			name:   "motion timeline missing after source changed",
			target: "gif",
			sourceHTML: `<!doctype html><html><head>
<meta name="viewport" content="width=device-width">
<style>
main { transition: opacity 300ms ease; }
@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }
</style>
</head><body><main style="width: 1200px; aspect-ratio: 16/9">Motion</main></body></html>`,
			wantReason:      "motion_timeline_required",
			wantMessagePart: "motion_timeline_required",
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
					Kind:            "render_motion",
					Worker:          ArtifactMotionRenderWorkerName,
					Status:          ArtifactHTMLExportJobWorkerPending,
					Reason:          "render_motion_worker_unavailable",
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
			worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
				DB:            db,
				WorkspaceRoot: workspaceRoot,
				Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextMarksRendererFailure(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{
			name:       "generic renderer failure",
			err:        errors.New("capture failed"),
			wantReason: "worker_failed",
		},
		{
			name:       "renderer signature mismatch",
			err:        errors.New("browser command motion renderer: output does not look like gif"),
			wantReason: "render_output_signature_mismatch",
		},
		{
			name:       "renderer timeout",
			err:        errors.New("browser command motion renderer: render timed out after 3m0s"),
			wantReason: "render_timeout",
		},
		{
			name:       "renderer command failed",
			err:        errors.New("browser command motion renderer: command failed: exit status 1: crashed"),
			wantReason: "render_command_failed",
		},
		{
			name:       "renderer output read failed",
			err:        errors.New("browser command motion renderer: read output: no such file or directory"),
			wantReason: "render_output_read_failed",
		},
		{
			name:       "renderer output dir create failed",
			err:        errors.New("browser command motion renderer: create output dir: permission denied"),
			wantReason: "render_output_write_failed",
		},
		{
			name:       "thread file register failed",
			err:        errors.New("motion render worker: create motion render output file: database is locked"),
			wantReason: "render_output_file_register_failed",
		},
		{
			name:       "artifact register failed",
			err:        errors.New("motion render worker: register motion render output artifact: database is locked"),
			wantReason: "render_output_artifact_register_failed",
		},
		{
			name:       "artifact lifecycle update failed",
			err:        errors.New("motion render worker: mark motion render output artifact exported: database is locked"),
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
			sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
			if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
				t.Fatalf("write motion html: %v", err)
			}
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
					Target:          "gif",
					Kind:            "render_motion",
					Worker:          ArtifactMotionRenderWorkerName,
					Status:          ArtifactHTMLExportJobWorkerPending,
					OutputExtension: ".gif",
				},
			})
			if err != nil {
				t.Fatalf("seed job: %v", err)
			}

			worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
				DB:            db,
				WorkspaceRoot: workspaceRoot,
				Renderer:      &fakeMotionRenderer{err: tt.err},
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

func TestArtifactMotionRenderWorker_RunNextFailsLoudlyWhenSourceHTMLMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if err := os.Remove(sourceAbs); err != nil {
		t.Fatalf("remove source html: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsSourceOutsideWorkspace(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsNonHTMLArtifact(t *testing.T) {
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsUnsupportedTarget(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	job := workagentModel.ArtifactExportJob{
		UID:             thread.UID,
		ThreadID:        thread.Id,
		ArtifactID:      sourceArtifact.Id,
		ThreadFileID:    sourceFile.Id,
		Target:          "webm",
		Kind:            "render_motion",
		Worker:          ArtifactMotionRenderWorkerName,
		Status:          workagentModel.ArtifactExportJobStatusQueued,
		OutputExtension: ".webm",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed unsupported target job: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsUnsupportedJobKind(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_static",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsOutputExtensionMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".gif",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered"), calls: &calls},
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

func TestArtifactMotionRenderWorker_RunNextRejectsMimeTypeMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "gif",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".gif",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}

	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("gif bytes"), mime: "video/mp4"},
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

func TestArtifactMotionRenderWorker_RunNextMarksOutputWriteFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
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
			Target:          "mp4",
			Kind:            "render_motion",
			Worker:          ArtifactMotionRenderWorkerName,
			Status:          ArtifactHTMLExportJobWorkerPending,
			OutputExtension: ".mp4",
		},
	})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
	outputName := buildMotionRenderOutputName(sourceFile.FileName, "mp4", job.Id, ".mp4")
	outputAbs, _, err := resolveStaticRenderOutputPath(workspaceRoot, sourceAbs, outputName)
	if err != nil {
		t.Fatalf("resolve output path: %v", err)
	}
	if err := os.MkdirAll(outputAbs, 0o755); err != nil {
		t.Fatalf("prepare output path conflict: %v", err)
	}

	calls := 0
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("fake mp4 bytes"), mime: "video/mp4", calls: &calls},
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

func TestArtifactMotionRenderRunner_DrainOnceProcessesQueuedMotionJobs(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	workspaceRoot := t.TempDir()
	thread := seedStaticRenderThread(t, db)
	sourceFile := seedStaticRenderHTMLFile(t, db, workspaceRoot, thread)
	sourceAbs := filepath.Join(workspaceRoot, filepath.FromSlash(sourceFile.FilePath))
	if err := os.WriteFile(sourceAbs, []byte(motionRenderReadyHTML()), 0o644); err != nil {
		t.Fatalf("write motion html: %v", err)
	}
	sourceArtifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(sourceFile)
	if err != nil {
		t.Fatalf("seed artifact: %v", err)
	}
	jobRepo := NewArtifactExportJobRepository(db)
	for _, target := range []string{"mp4", "gif"} {
		_, err := jobRepo.CreateFromHTMLJobPlan(ArtifactExportJobInput{
			UID:          thread.UID,
			ThreadID:     thread.Id,
			ArtifactID:   sourceArtifact.Id,
			ThreadFileID: sourceFile.Id,
			Plan: ArtifactHTMLExportJobPlan{
				Target:          target,
				Kind:            "render_motion",
				Worker:          ArtifactMotionRenderWorkerName,
				Status:          ArtifactHTMLExportJobWorkerPending,
				OutputExtension: "." + target,
			},
		})
		if err != nil {
			t.Fatalf("seed %s job: %v", target, err)
		}
	}
	worker := NewArtifactMotionRenderWorker(ArtifactMotionRenderWorkerOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
		Renderer:      &fakeMotionRenderer{content: []byte("rendered motion")},
	})
	runner := NewArtifactMotionRenderRunner(worker, time.Hour, 5)
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
		Where("file_source = ? AND file_type IN ?", workagentModel.FileSourceOutput, []string{"mp4", "gif"}).
		Count(&outputs).Error; err != nil {
		t.Fatalf("count outputs: %v", err)
	}
	if outputs != 2 {
		t.Fatalf("motion render outputs = %d, want 2", outputs)
	}
	for _, target := range []struct {
		fileType     string
		mimeType     string
		artifactType string
		previewType  string
	}{
		{fileType: "mp4", mimeType: "video/mp4", artifactType: "video", previewType: "video"},
		{fileType: "gif", mimeType: "image/gif", artifactType: "image", previewType: "image"},
	} {
		var output workagentModel.ThreadFile
		if err := db.Where("file_source = ? AND file_type = ?", workagentModel.FileSourceOutput, target.fileType).First(&output).Error; err != nil {
			t.Fatalf("load %s output: %v", target.fileType, err)
		}
		if output.MimeType != target.mimeType {
			t.Fatalf("%s mime = %q, want %q", target.fileType, output.MimeType, target.mimeType)
		}
		var artifact workagentModel.ArtifactRegistry
		if err := db.Where("thread_file_id = ?", output.Id).First(&artifact).Error; err != nil {
			t.Fatalf("load %s artifact: %v", target.fileType, err)
		}
		if artifact.ArtifactType != target.artifactType || artifact.OutputType != target.fileType || artifact.PreviewType != target.previewType {
			t.Fatalf("%s artifact = %+v, want %s/%s preview %s", target.fileType, artifact, target.artifactType, target.fileType, target.previewType)
		}
		if artifact.Status != workagentModel.ArtifactStatusExported || artifact.ReviewState != workagentModel.ArtifactReviewApproved {
			t.Fatalf("%s lifecycle = %q/%q, want exported/approved", target.fileType, artifact.Status, artifact.ReviewState)
		}
	}
}

func motionRenderReadyHTML() string {
	return `<!doctype html><html><head>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="workmax:motion-duration-ms" content="3500">
<meta name="workmax:motion-fps" content="24">
<meta name="workmax:motion-width" content="1080">
<meta name="workmax:motion-height" content="1920">
</head><body><main style="width: 1200px; aspect-ratio: 16/9">motion</main></body></html>`
}
