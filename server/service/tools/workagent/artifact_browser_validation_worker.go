package workagent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type HTMLBrowserValidationInput struct {
	Artifact      workagentModel.ArtifactRegistry
	SourceFile    workagentModel.ThreadFile
	SourcePath    string
	SourceHTML    string
	WorkspaceRoot string
	StaticResult  ArtifactHTMLValidationResult
	ExportPlan    ArtifactHTMLExportPlan
	Plan          ArtifactHTMLBrowserValidationPlan
}

type HTMLBrowserValidator interface {
	ValidateHTMLInBrowser(ctx context.Context, input HTMLBrowserValidationInput) ([]ArtifactHTMLValidationIssue, error)
}

type ArtifactBrowserValidationWorker struct {
	db            *gorm.DB
	artifactRepo  *ArtifactRegistryRepository
	validator     HTMLBrowserValidator
	workspaceRoot string
}

type ArtifactBrowserValidationWorkerOptions struct {
	DB            *gorm.DB
	ArtifactRepo  *ArtifactRegistryRepository
	Validator     HTMLBrowserValidator
	WorkspaceRoot string
}

type ArtifactBrowserValidationResult struct {
	Artifact    *workagentModel.ArtifactRegistry
	Plan        *ArtifactHTMLBrowserValidationPlan
	Diagnostics []ArtifactHTMLValidationIssue
	Skipped     bool
	Reason      string
}

func NewArtifactBrowserValidationWorker(opts ArtifactBrowserValidationWorkerOptions) *ArtifactBrowserValidationWorker {
	db := opts.DB
	if db == nil {
		db = globals.GraDBs["system"]
	}
	artifactRepo := opts.ArtifactRepo
	if artifactRepo == nil {
		artifactRepo = NewArtifactRegistryRepository(db)
	}
	workspaceRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = ResolveWorkspaceRoot()
	}
	return &ArtifactBrowserValidationWorker{
		db:            db,
		artifactRepo:  artifactRepo,
		validator:     opts.Validator,
		workspaceRoot: workspaceRoot,
	}
}

func (w *ArtifactBrowserValidationWorker) ValidateArtifact(ctx context.Context, uid int, threadID uint, artifactID uint) (*ArtifactBrowserValidationResult, error) {
	if w == nil || w.db == nil || w.artifactRepo == nil {
		return nil, fmt.Errorf("browser validation worker: nil dependency")
	}
	if w.validator == nil {
		return nil, fmt.Errorf("browser validation worker: validator is required")
	}
	artifact, sourceFile, err := w.artifactRepo.LoadForOwnerWithThreadFile(uid, threadID, artifactID)
	if err != nil {
		return nil, fmt.Errorf("browser validation worker: load artifact source: %w", err)
	}
	sourceView := ArtifactViewFromRegistryAndThreadFile(*artifact, *sourceFile)
	if sourceView.OutputType != "html" && sourceView.PreviewType != "html" {
		return &ArtifactBrowserValidationResult{Artifact: artifact, Skipped: true, Reason: "not_html_artifact"}, nil
	}
	sourcePath := ResolveInsideWorkspace(w.workspaceRoot, sourceFile.FilePath)
	if sourcePath == "" {
		return nil, fmt.Errorf("browser validation worker: source file is outside workspace")
	}
	sourceBytes, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("browser validation worker: read source html: %w", err)
	}
	staticResult := ValidateHTMLArtifactContent(string(sourceBytes))
	staticResult.PreviewDiagnostics = parseHTMLPreviewDiagnostics(artifact.HTMLPreviewDiagnostics)
	sourceView.HTMLValidation = &staticResult
	exportPlan := BuildHTMLExportPlan(sourceView)
	sourceView.HTMLExportPlan = exportPlan
	plan := BuildHTMLBrowserValidationPlan(sourceView)
	if plan == nil || plan.Status != ArtifactHTMLBrowserValidationPending {
		reason := ""
		if plan != nil {
			reason = plan.Reason
		}
		return &ArtifactBrowserValidationResult{Artifact: artifact, Plan: plan, Skipped: true, Reason: reason}, nil
	}
	diagnostics, err := w.validator.ValidateHTMLInBrowser(ctx, HTMLBrowserValidationInput{
		Artifact:      *artifact,
		SourceFile:    *sourceFile,
		SourcePath:    sourcePath,
		SourceHTML:    string(sourceBytes),
		WorkspaceRoot: w.workspaceRoot,
		StaticResult:  staticResult,
		ExportPlan:    *exportPlan,
		Plan:          *plan,
	})
	if err != nil {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     browserValidationWorkerFailureCode(err),
			Severity: "error",
			Message:  err.Error(),
			Source:   "browser_validation",
		})
	}
	diagnostics = normalizeBrowserValidationDiagnostics(diagnostics)
	if !hasErrorDiagnostic(diagnostics) {
		diagnostics = append(diagnostics, ArtifactHTMLValidationIssue{
			Code:     "browser_validation_passed",
			Severity: "info",
			Message:  "Browser validation completed without blocking runtime issues",
			Source:   "browser_validation",
		})
	}
	merged := mergeBrowserValidationDiagnostics(staticResult.PreviewDiagnostics, diagnostics)
	updated, updateErr := w.artifactRepo.ReplaceHTMLPreviewDiagnostics(uid, threadID, artifactID, merged)
	if updateErr != nil {
		return nil, updateErr
	}
	updatedView := ArtifactViewFromRegistryAndThreadFile(*updated, *sourceFile)
	updatedStatic := ValidateHTMLArtifactContent(string(sourceBytes))
	updatedStatic.PreviewDiagnostics = parseHTMLPreviewDiagnostics(updated.HTMLPreviewDiagnostics)
	updatedView.HTMLValidation = &updatedStatic
	updatedView.HTMLExportPlan = BuildHTMLExportPlan(updatedView)
	updatedPlan := BuildHTMLBrowserValidationPlan(updatedView)
	emitArtifactBrowserValidationMetric(*updated, diagnostics, updatedPlan)
	return &ArtifactBrowserValidationResult{
		Artifact:    updated,
		Plan:        updatedPlan,
		Diagnostics: diagnostics,
	}, nil
}

func browserValidationWorkerFailureCode(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timed out"):
		return "browser_validation_timeout"
	case strings.Contains(msg, "command failed"):
		return "browser_validation_command_failed"
	case strings.Contains(msg, "source path is required"):
		return "browser_validation_source_missing"
	case strings.Contains(msg, "probe marker missing"):
		return "browser_validation_probe_output_invalid"
	case strings.Contains(msg, "probe marker end missing"):
		return "browser_validation_probe_output_invalid"
	default:
		return "browser_validation_worker_failed"
	}
}

func emitArtifactBrowserValidationMetric(artifact workagentModel.ArtifactRegistry, diagnostics []ArtifactHTMLValidationIssue, plan *ArtifactHTMLBrowserValidationPlan) {
	errorCount := 0
	warnCount := 0
	infoCount := 0
	for _, issue := range diagnostics {
		switch normalizeHTMLPreviewSeverity(issue.Severity) {
		case "error":
			errorCount++
		case "warn":
			warnCount++
		default:
			infoCount++
		}
	}
	status := "passed"
	reason := "browser_validation_passed"
	targetCount := 0
	viewportCount := 0
	if plan != nil {
		status = plan.Status
		reason = plan.Reason
		targetCount = len(plan.Targets)
		viewportCount = len(plan.Viewports)
	}
	if errorCount > 0 && status == ArtifactHTMLBrowserValidationNotRequired {
		status = ArtifactHTMLBrowserValidationFailed
	}
	EmitMetric("wa_artifact_browser_validation", map[string]any{
		"uid":              artifact.UID,
		"thread_id":        artifact.ThreadID,
		"artifact_id":      artifact.Id,
		"thread_file_id":   artifact.ThreadFileID,
		"artifact_type":    artifact.ArtifactType,
		"output_type":      artifact.OutputType,
		"preview_type":     artifact.PreviewType,
		"status":           status,
		"reason":           reason,
		"error_count":      errorCount,
		"warn_count":       warnCount,
		"info_count":       infoCount,
		"diagnostic_count": len(diagnostics),
		"target_count":     targetCount,
		"viewport_count":   viewportCount,
	})
}

func hasErrorDiagnostic(diagnostics []ArtifactHTMLValidationIssue) bool {
	for _, issue := range diagnostics {
		if issue.Severity == "error" || issue.Severity == "block" {
			return true
		}
	}
	return false
}

func normalizeBrowserValidationDiagnostics(in []ArtifactHTMLValidationIssue) []ArtifactHTMLValidationIssue {
	out := make([]ArtifactHTMLValidationIssue, 0, len(in))
	for _, issue := range in {
		issue.Code = strings.TrimSpace(issue.Code)
		issue.Severity = strings.TrimSpace(strings.ToLower(issue.Severity))
		issue.Message = strings.TrimSpace(issue.Message)
		if issue.Code == "" || issue.Message == "" {
			continue
		}
		if issue.Severity == "" {
			issue.Severity = "warn"
		}
		if strings.TrimSpace(issue.Source) == "" {
			issue.Source = "browser_validation"
		}
		out = append(out, issue)
	}
	return normalizeHTMLPreviewDiagnostics(out)
}

func mergeBrowserValidationDiagnostics(existing []ArtifactHTMLValidationIssue, browser []ArtifactHTMLValidationIssue) []ArtifactHTMLValidationIssue {
	merged := make([]ArtifactHTMLValidationIssue, 0, len(existing)+len(browser))
	for _, issue := range existing {
		if issue.Source == "browser_validation" {
			continue
		}
		merged = append(merged, issue)
	}
	merged = append(merged, browser...)
	return normalizeHTMLPreviewDiagnostics(merged)
}
