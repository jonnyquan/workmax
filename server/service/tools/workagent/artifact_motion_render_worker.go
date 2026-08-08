package workagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

const ArtifactMotionRenderWorkerName = "browser_motion_render"

type HTMLMotionRenderInput struct {
	Job             workagentModel.ArtifactExportJob
	Artifact        workagentModel.ArtifactRegistry
	SourceFile      workagentModel.ThreadFile
	SourcePath      string
	SourceHTML      string
	Target          string
	OutputExtension string
	WorkspaceRoot   string
	OutputFileName  string
	OutputFilePath  string
	OutputFileAbs   string
	MotionSettings  HTMLMotionRenderSettings
}

type HTMLMotionRenderOutput struct {
	Content  []byte
	MimeType string
}

type HTMLMotionRenderer interface {
	RenderMotionHTML(ctx context.Context, input HTMLMotionRenderInput) (HTMLMotionRenderOutput, error)
}

type ArtifactMotionRenderWorker struct {
	db            *gorm.DB
	jobRepo       *ArtifactExportJobRepository
	artifactRepo  *ArtifactRegistryRepository
	renderer      HTMLMotionRenderer
	workspaceRoot string
}

type ArtifactMotionRenderWorkerOptions struct {
	DB            *gorm.DB
	JobRepo       *ArtifactExportJobRepository
	ArtifactRepo  *ArtifactRegistryRepository
	Renderer      HTMLMotionRenderer
	WorkspaceRoot string
}

type ArtifactMotionRenderWorkerResult struct {
	Claimed    bool
	Job        *workagentModel.ArtifactExportJob
	OutputFile *workagentModel.ThreadFile
}

func NewArtifactMotionRenderWorker(opts ArtifactMotionRenderWorkerOptions) *ArtifactMotionRenderWorker {
	db := opts.DB
	if db == nil {
		db = globals.GraDBs["system"]
	}
	jobRepo := opts.JobRepo
	if jobRepo == nil {
		jobRepo = NewArtifactExportJobRepository(db)
	}
	artifactRepo := opts.ArtifactRepo
	if artifactRepo == nil {
		artifactRepo = NewArtifactRegistryRepository(db)
	}
	workspaceRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = ResolveWorkspaceRoot()
	}
	return &ArtifactMotionRenderWorker{
		db:            db,
		jobRepo:       jobRepo,
		artifactRepo:  artifactRepo,
		renderer:      opts.Renderer,
		workspaceRoot: workspaceRoot,
	}
}

func (w *ArtifactMotionRenderWorker) RunNext(ctx context.Context) (*ArtifactMotionRenderWorkerResult, error) {
	if w == nil || w.db == nil || w.jobRepo == nil || w.artifactRepo == nil {
		return nil, fmt.Errorf("motion render worker: nil dependency")
	}
	if w.renderer == nil {
		return nil, fmt.Errorf("motion render worker: renderer is required")
	}
	job, err := w.jobRepo.ClaimNext(ArtifactMotionRenderWorkerName)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return &ArtifactMotionRenderWorkerResult{Claimed: false}, nil
	}
	result := &ArtifactMotionRenderWorkerResult{Claimed: true, Job: job}
	output, runErr := w.runClaimed(ctx, job)
	if runErr != nil {
		failed, markErr := w.jobRepo.MarkFailed(job.Id, motionRenderFailureReason(runErr), runErr.Error())
		if markErr != nil {
			return result, markErr
		}
		result.Job = failed
		return result, nil
	}
	succeeded, err := w.jobRepo.MarkSucceeded(job.Id, output.Id, output.FilePath)
	if err != nil {
		return result, err
	}
	result.Job = succeeded
	result.OutputFile = output
	return result, nil
}

func (w *ArtifactMotionRenderWorker) runClaimed(ctx context.Context, job *workagentModel.ArtifactExportJob) (*workagentModel.ThreadFile, error) {
	if job == nil || job.Id == 0 {
		return nil, fmt.Errorf("motion render worker: empty job")
	}
	if job.Kind != "render_motion" || job.Worker != ArtifactMotionRenderWorkerName {
		return nil, fmt.Errorf("motion render worker: unsupported job kind/worker %s/%s", job.Kind, job.Worker)
	}
	target := strings.ToLower(strings.TrimSpace(job.Target))
	if target != "mp4" && target != "gif" {
		return nil, fmt.Errorf("motion render worker: unsupported target %s", target)
	}
	artifact, sourceFile, err := w.artifactRepo.LoadForOwnerWithThreadFile(job.UID, job.ThreadID, job.ArtifactID)
	if err != nil {
		return nil, fmt.Errorf("motion render worker: load artifact source: %w", err)
	}
	if sourceFile.Id != job.ThreadFileID {
		return nil, fmt.Errorf("motion render worker: job source file mismatch")
	}
	sourceView := ArtifactViewFromRegistryAndThreadFile(*artifact, *sourceFile)
	if sourceView.OutputType != "html" && sourceView.PreviewType != "html" {
		return nil, fmt.Errorf("motion render worker: source artifact is not html")
	}
	workspaceRoot := strings.TrimSpace(w.workspaceRoot)
	sourcePath := ResolveInsideWorkspace(workspaceRoot, sourceFile.FilePath)
	if sourcePath == "" {
		return nil, fmt.Errorf("motion render worker: source file is outside workspace")
	}
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("motion render worker: read source html: %w", err)
	}
	if err := ensureMotionRenderJobStillReady(*artifact, sourceView, string(sourceBytes), target); err != nil {
		return nil, err
	}
	outputExt := normalizeExportOutputExtension(job.OutputExtension, target)
	if err := validateRenderOutputExtension(target, outputExt); err != nil {
		return nil, fmt.Errorf("motion render worker: %w", err)
	}
	outputName := buildMotionRenderOutputName(sourceFile.FileName, target, job.Id, outputExt)
	outputAbs, outputRel, err := resolveStaticRenderOutputPath(workspaceRoot, sourcePath, outputName)
	if err != nil {
		return nil, err
	}
	input := HTMLMotionRenderInput{
		Job:             *job,
		Artifact:        *artifact,
		SourceFile:      *sourceFile,
		SourcePath:      sourcePath,
		SourceHTML:      string(sourceBytes),
		Target:          target,
		OutputExtension: outputExt,
		WorkspaceRoot:   workspaceRoot,
		OutputFileName:  outputName,
		OutputFilePath:  outputRel,
		OutputFileAbs:   outputAbs,
		MotionSettings:  ExtractHTMLMotionRenderSettings(string(sourceBytes)),
	}
	rendered, err := w.renderer.RenderMotionHTML(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("motion render worker: render %s: %w", target, err)
	}
	if len(rendered.Content) == 0 {
		return nil, fmt.Errorf("motion render worker: renderer returned empty %s output", target)
	}
	if rendered.MimeType == "" {
		rendered.MimeType = mimeTypeForMotionRenderTarget(target)
	}
	if err := validateRenderMimeType(target, rendered.MimeType); err != nil {
		return nil, fmt.Errorf("motion render worker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outputAbs), 0o755); err != nil {
		return nil, fmt.Errorf("motion render worker: create output dir: %w", err)
	}
	if err := os.WriteFile(outputAbs, rendered.Content, 0o644); err != nil {
		return nil, fmt.Errorf("motion render worker: write output file: %w", err)
	}
	outputRow := workagentModel.ThreadFile{
		UID:          job.UID,
		ThreadID:     job.ThreadID,
		FileName:     outputName,
		DisplayName:  outputName,
		FileSize:     uint64(len(rendered.Content)),
		FileType:     target,
		MimeType:     rendered.MimeType,
		FilePath:     outputRel,
		FileHash:     md5Hex(rendered.Content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  renderExportDescription("motion", target, *job, *artifact, *sourceFile),
		ExistsOnDisk: true,
	}
	err = w.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&outputRow).Error; err != nil {
			return fmt.Errorf("create motion render output file: %w", err)
		}
		registered, err := NewArtifactRegistryRepository(tx).UpsertFromThreadFile(&outputRow)
		if err != nil {
			return fmt.Errorf("register motion render output artifact: %w", err)
		}
		if err := tx.Model(registered).Updates(map[string]interface{}{
			"status":       workagentModel.ArtifactStatusExported,
			"review_state": workagentModel.ArtifactReviewApproved,
		}).Error; err != nil {
			return fmt.Errorf("mark motion render output artifact exported: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &outputRow, nil
}

func ensureMotionRenderJobStillReady(artifact workagentModel.ArtifactRegistry, sourceView ArtifactView, sourceHTML string, target string) error {
	staticResult := ValidateHTMLArtifactContent(sourceHTML)
	staticResult.PreviewDiagnostics = parseHTMLPreviewDiagnostics(artifact.HTMLPreviewDiagnostics)
	sourceView.HTMLValidation = &staticResult
	sourceView.HTMLExportPlan = BuildHTMLExportPlan(sourceView)
	jobPlan := BuildHTMLExportJobPlan(sourceView, target)
	if jobPlan == nil {
		return fmt.Errorf("motion render worker: html_export_not_ready: export plan unavailable")
	}
	if jobPlan.Kind != "render_motion" || jobPlan.Worker != ArtifactMotionRenderWorkerName || jobPlan.Status != ArtifactHTMLExportJobWorkerPending {
		reason := strings.TrimSpace(jobPlan.Reason)
		if reason == "" {
			reason = strings.TrimSpace(jobPlan.Status)
		}
		if reason == "" {
			reason = "html_export_not_ready"
		}
		return fmt.Errorf("motion render worker: %s: target %s is no longer ready for motion render", reason, target)
	}
	return nil
}

func motionRenderFailureReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unsupported job kind/worker"):
		return "unsupported_job_kind"
	case strings.Contains(msg, "outside workspace"):
		return "source_file_outside_workspace"
	case strings.Contains(msg, "read source html"):
		return "source_file_read_failed"
	case strings.Contains(msg, "not html"):
		return "not_html_artifact"
	case strings.Contains(msg, "unsupported target"):
		return "unsupported_target"
	case strings.Contains(msg, "output_extension_mismatch"):
		return "output_extension_mismatch"
	case strings.Contains(msg, "mime_type_mismatch"):
		return "mime_type_mismatch"
	case strings.Contains(msg, "browser_resource_missing"):
		return "browser_resource_missing"
	case strings.Contains(msg, "browser_validation_failed"):
		return "browser_validation_failed"
	case strings.Contains(msg, "browser_validation_required"):
		return "browser_validation_required"
	case strings.Contains(msg, "motion_timeline_invalid"):
		return "motion_timeline_invalid"
	case strings.Contains(msg, "motion_timeline_required"):
		return "motion_timeline_required"
	case strings.Contains(msg, "preview_runtime_error"):
		return "preview_runtime_error"
	case strings.Contains(msg, "preview_resource_error"):
		return "preview_resource_error"
	case strings.Contains(msg, "asset_bundle_required"):
		return "asset_bundle_required"
	case strings.Contains(msg, "remote_asset_reference"):
		return "remote_asset_reference"
	case strings.Contains(msg, "html_validation_block"):
		return "html_validation_block"
	case strings.Contains(msg, "html_export_not_ready"):
		return "html_export_not_ready"
	case strings.Contains(msg, "timed out"):
		return "render_timeout"
	case strings.Contains(msg, "does not look like"):
		return "render_output_signature_mismatch"
	case strings.Contains(msg, "command failed"):
		return "render_command_failed"
	case strings.Contains(msg, "read output"):
		return "render_output_read_failed"
	case strings.Contains(msg, "create output dir"):
		return "render_output_write_failed"
	case strings.Contains(msg, "write output file"):
		return "render_output_write_failed"
	case strings.Contains(msg, "create motion render output file"):
		return "render_output_file_register_failed"
	case strings.Contains(msg, "register motion render output artifact"):
		return "render_output_artifact_register_failed"
	case strings.Contains(msg, "mark motion render output artifact exported"):
		return "render_output_lifecycle_update_failed"
	case strings.Contains(msg, "empty"):
		return "empty_render_output"
	default:
		return "worker_failed"
	}
}

func buildMotionRenderOutputName(sourceName string, target string, jobID uint, ext string) string {
	base := strings.TrimSuffix(filepath.Base(strings.TrimSpace(sourceName)), filepath.Ext(sourceName))
	if base == "" || base == "." {
		base = "artifact"
	}
	return fmt.Sprintf("%s-%s-export-%d%s", base, strings.ToLower(target), jobID, ext)
}

func mimeTypeForMotionRenderTarget(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "mp4":
		return "video/mp4"
	case "gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
