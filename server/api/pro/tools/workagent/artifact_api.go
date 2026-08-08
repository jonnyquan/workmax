package workagent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"server/model/common/response"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type updateArtifactStatusRequest struct {
	Status      string `json:"status" binding:"required"`
	ReviewState string `json:"reviewState"`
}

type updateArtifactComparisonRequest struct {
	Source   string `json:"source" binding:"required"`
	Summary  string `json:"summary" binding:"required"`
	Decision string `json:"decision"`
}

type createArtifactVisualDiffReportRequest struct {
	PreviousArtifactID string `json:"previousArtifactId" binding:"required"`
}

type updateHTMLPreviewDiagnosticsRequest struct {
	Diagnostics []workagentService.ArtifactHTMLValidationIssue `json:"diagnostics"`
}

type applyArtifactDecisionRequest struct {
	Decision string `json:"decision" binding:"required"`
}

type createArtifactAssetCandidateRequest struct {
	AssetKind string                 `json:"assetKind" binding:"required"`
	Name      string                 `json:"name"`
	Slug      string                 `json:"slug"`
	Profile   map[string]interface{} `json:"profile"`
}

type updateArtifactAssetCandidateStatusRequest struct {
	Status string `json:"status" binding:"required"`
}

type exportArtifactRequest struct {
	Target string `json:"target" binding:"required"`
}

type createArtifactExportJobRequest struct {
	Target string `json:"target" binding:"required"`
}

var newArtifactBrowserValidationWorker = func() (*workagentService.ArtifactBrowserValidationWorker, error) {
	validator, err := workagentService.NewDefaultBrowserCommandHTMLValidator()
	if err != nil {
		return nil, err
	}
	return workagentService.NewArtifactBrowserValidationWorker(workagentService.ArtifactBrowserValidationWorkerOptions{
		Validator: validator,
	}), nil
}

var newArtifactVisualDiffImageReportService = func() *workagentService.ArtifactVisualDiffImageReportService {
	return workagentService.NewArtifactVisualDiffImageReportService(workagentService.ArtifactVisualDiffImageReportOptions{})
}

type exportArtifactFailure struct {
	Code   string `json:"code"`
	Target string `json:"target,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type browserValidationFailure struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

type visualDiffFailure struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

func (api *AIChatApiNew) UpdateArtifactStatus(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	var req updateArtifactStatusRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactRegistryRepository(nil).UpdateLifecycle(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		req.Status,
		req.ReviewState,
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func (api *AIChatApiNew) UpdateArtifactComparison(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	var req updateArtifactComparisonRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactRegistryRepository(nil).UpdateComparison(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		req.Source,
		req.Summary,
		req.Decision,
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func (api *AIChatApiNew) CreateArtifactVisualDiffReport(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		failCreateArtifactVisualDiffReport(c, "Invalid thread ID", "invalid_thread_id", "")
		return
	}
	latestArtifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		failCreateArtifactVisualDiffReport(c, "Invalid artifact ID", "invalid_artifact_id", "")
		return
	}
	var req createArtifactVisualDiffReportRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		failCreateArtifactVisualDiffReport(c, "Invalid request", "invalid_request", "")
		return
	}
	previousArtifactID, err := parseArtifactID(req.PreviousArtifactID)
	if err != nil {
		failCreateArtifactVisualDiffReport(c, "Invalid previous artifact ID", "invalid_previous_artifact_id", "")
		return
	}
	service := newArtifactVisualDiffImageReportService()
	result, err := service.Generate(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(previousArtifactID),
		uint(latestArtifactID),
	)
	if err != nil {
		code, reason := classifyArtifactVisualDiffFailure(err)
		failCreateArtifactVisualDiffReport(c, err.Error(), code, reason)
		return
	}
	response.OkWithData(result, c)
}

func (api *AIChatApiNew) UpdateArtifactHTMLPreviewDiagnostics(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	var req updateHTMLPreviewDiagnosticsRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactRegistryRepository(nil).UpdateHTMLPreviewDiagnostics(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		req.Diagnostics,
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func (api *AIChatApiNew) RunArtifactHTMLBrowserValidation(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	worker, err := newArtifactBrowserValidationWorker()
	if err != nil {
		failArtifactBrowserValidation(c, "HTML browser validator is unavailable: "+err.Error(), "browser_validation_worker_unavailable", err.Error())
		return
	}
	result, err := worker.ValidateArtifact(c.Request.Context(), int(utils.GetUserID(c)), uint(threadID), uint(artifactID))
	if err != nil {
		code, reason := classifyArtifactBrowserValidationFailure(err)
		failArtifactBrowserValidation(c, err.Error(), code, reason)
		return
	}
	response.OkWithData(result, c)
}

func (api *AIChatApiNew) ExportArtifact(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		failExportArtifact(c, "Invalid thread ID", "invalid_thread_id", "", "")
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		failExportArtifact(c, "Invalid artifact ID", "invalid_artifact_id", "", "")
		return
	}
	var req exportArtifactRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		failExportArtifact(c, "Invalid request", "invalid_request", "", "")
		return
	}
	target := strings.TrimSpace(strings.ToLower(req.Target))
	if !workagentService.IsSupportedHTMLExportTarget(target) {
		failExportArtifact(c, "Unsupported HTML export target", "unsupported_target", target, "unsupported_target")
		return
	}
	artifact, file, err := workagentService.NewArtifactRegistryRepository(nil).LoadForOwnerWithThreadFile(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
	)
	if err != nil {
		failExportArtifact(c, "Artifact not found", "artifact_not_found", target, "")
		return
	}
	view := workagentService.ArtifactViewFromRegistryAndThreadFile(*artifact, *file)
	setExportArtifactMetricView(c, view)
	if view.OutputType != "html" && view.PreviewType != "html" {
		failExportArtifact(c, "Artifact is not an HTML artifact", "not_html_artifact", target, view.OutputType)
		return
	}
	fullPath := api.resolveWorkspaceFilePath(file.FilePath)
	if fullPath == "" {
		failExportArtifact(c, "Artifact file not found", "artifact_file_not_found", target, "")
		return
	}
	f, err := os.Open(fullPath)
	if err != nil {
		failExportArtifact(c, "Artifact file not found", "artifact_file_not_found", target, "")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, workAgentHTMLValidationMaxBytes+1))
	if err != nil {
		failExportArtifact(c, "Failed to read artifact", "artifact_read_failed", target, err.Error())
		return
	}
	validation := workagentService.ValidateHTMLArtifactContent(string(raw))
	if view.HTMLValidation != nil && len(view.HTMLValidation.PreviewDiagnostics) > 0 {
		validation.PreviewDiagnostics = view.HTMLValidation.PreviewDiagnostics
	}
	if len(raw) > workAgentHTMLValidationMaxBytes {
		validation.Issues = append(validation.Issues, workagentService.ArtifactHTMLValidationIssue{
			Code:     "validation_truncated",
			Severity: "warn",
			Message:  "HTML artifact exceeded static validation size budget",
		})
		validation.IssueCount = len(validation.Issues)
		if validation.Status == workagentService.ArtifactHTMLValidationPass {
			validation.Status = workagentService.ArtifactHTMLValidationWarn
		}
	}
	view.HTMLValidation = &validation
	plan := workagentService.BuildHTMLExportPlan(view)
	if plan == nil || plan.AssetBundleManifest == nil {
		failExportArtifact(c, "HTML export plan is unavailable", "html_export_plan_unavailable", target, "")
		return
	}
	view.HTMLExportPlan = plan
	if plan.Blocked || plan.AssetBundleManifest.Blocked {
		failExportArtifact(c, "HTML artifact has blocking export issues", "html_export_blocked", target, firstHTMLExportBlockReason(plan))
		return
	}
	if hasRemoteHTMLAssetReferences(validation.AssetReferences) {
		failExportArtifact(c, "HTML ZIP export cannot yet inline or mirror remote assets", "remote_assets_unsupported", target, "remote_asset_reference")
		return
	}
	if target == "png" || target == "pdf" {
		jobPlan := workagentService.BuildHTMLExportJobPlan(view, target)
		if jobPlan == nil || jobPlan.Status != workagentService.ArtifactHTMLExportJobWorkerPending || jobPlan.Kind != "render_static" || jobPlan.Worker != workagentService.ArtifactStaticRenderWorkerName {
			reason := "html_export_not_ready"
			if jobPlan != nil && jobPlan.Reason != "" {
				reason = jobPlan.Reason
			}
			failExportArtifact(c, "HTML export target is not ready for static render", "html_export_not_ready", target, reason)
			return
		}
		rendered, err := renderHTMLArtifactStaticExport(c, target, fullPath, string(raw), *artifact, *file)
		if err != nil {
			failExportArtifact(c, err.Error(), "html_static_render_failed", target, htmlStaticRenderFailureReason(err))
			return
		}
		if _, err := workagentService.NewArtifactRegistryRepository(nil).UpdateLifecycle(
			int(utils.GetUserID(c)),
			uint(threadID),
			uint(artifactID),
			workagentModel.ArtifactStatusExported,
			nonEmptyReviewState(artifact.ReviewState, workagentModel.ArtifactReviewApproved),
		); err != nil {
			failExportArtifact(c, err.Error(), "artifact_lifecycle_update_failed", target, err.Error())
			return
		}
		c.Header("Content-Disposition", ContentDispositionAttachment(htmlArtifactStaticDownloadName(file.FileName, target)))
		emitExportArtifactMetric(c, "success", "", target, "", nil)
		c.Data(http.StatusOK, rendered.MimeType, rendered.Content)
		return
	}
	if target != "zip" {
		jobPlan := workagentService.BuildHTMLExportJobPlan(view, target)
		reason := "export_worker_unavailable"
		if jobPlan != nil && jobPlan.Reason != "" {
			reason = jobPlan.Reason
		}
		failExportArtifact(c, "HTML export worker is not yet available", "html_export_worker_unavailable", target, reason)
		return
	}
	zipBytes, err := workagentService.BuildHTMLZipPackageForFile(string(raw), fullPath, plan.AssetBundleManifest)
	if err != nil {
		failExportArtifact(c, err.Error(), htmlZipExportFailureCode(err), target, htmlZipExportFailureReason(err))
		return
	}
	if _, err := workagentService.NewArtifactRegistryRepository(nil).UpdateLifecycle(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		workagentModel.ArtifactStatusExported,
		nonEmptyReviewState(artifact.ReviewState, workagentModel.ArtifactReviewApproved),
	); err != nil {
		failExportArtifact(c, err.Error(), "artifact_lifecycle_update_failed", target, err.Error())
		return
	}
	c.Header("Content-Disposition", ContentDispositionAttachment(htmlArtifactZipDownloadName(file.FileName)))
	emitExportArtifactMetric(c, "success", "", target, "", nil)
	c.Data(http.StatusOK, "application/zip", zipBytes)
}

func renderHTMLArtifactStaticExport(c *gin.Context, target string, sourcePath string, sourceHTML string, artifact workagentModel.ArtifactRegistry, file workagentModel.ThreadFile) (workagentService.HTMLStaticRenderOutput, error) {
	renderer, err := workagentService.NewDefaultBrowserCommandStaticRenderer()
	if err != nil {
		return workagentService.HTMLStaticRenderOutput{}, fmt.Errorf("HTML static export worker is unavailable: %w", err)
	}
	tmp, err := os.CreateTemp("", "workmax-html-static-export-*"+htmlStaticDownloadExtension(target))
	if err != nil {
		return workagentService.HTMLStaticRenderOutput{}, fmt.Errorf("create static export temp file: %w", err)
	}
	outputPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(outputPath)
	return renderer.RenderStaticHTML(c.Request.Context(), workagentService.HTMLStaticRenderInput{
		Artifact:        artifact,
		SourceFile:      file,
		SourcePath:      sourcePath,
		SourceHTML:      sourceHTML,
		Target:          target,
		OutputExtension: htmlStaticDownloadExtension(target),
		WorkspaceRoot:   workagentService.ResolveWorkspaceRoot(),
		OutputFileName:  filepath.Base(outputPath),
		OutputFilePath:  filepath.Base(outputPath),
		OutputFileAbs:   outputPath,
	})
}

func htmlStaticRenderFailureReason(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unavailable") || strings.Contains(msg, "no chrome") || strings.Contains(msg, "configured browser not found"):
		return "render_static_worker_unavailable"
	case strings.Contains(msg, "timed out"):
		return "render_timeout"
	case strings.Contains(msg, "output is empty") || strings.Contains(msg, "empty output"):
		return "empty_render_output"
	case strings.Contains(msg, "does not look like"):
		return "render_output_signature_mismatch"
	case strings.Contains(msg, "command failed"):
		return "render_command_failed"
	case strings.Contains(msg, "read output"):
		return "render_output_read_failed"
	case strings.Contains(msg, "create output dir"):
		return "render_output_write_failed"
	default:
		return "render_static_failed"
	}
}

func (api *AIChatApiNew) CreateArtifactExportJob(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		failCreateArtifactExportJob(c, "Invalid thread ID", "invalid_thread_id", "", "")
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		failCreateArtifactExportJob(c, "Invalid artifact ID", "invalid_artifact_id", "", "")
		return
	}
	var req createArtifactExportJobRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		failCreateArtifactExportJob(c, "Invalid request", "invalid_request", "", "")
		return
	}
	target := strings.TrimSpace(strings.ToLower(req.Target))
	if !workagentService.IsSupportedHTMLExportTarget(target) {
		failCreateArtifactExportJob(c, "Unsupported HTML export target", "unsupported_target", target, "unsupported_target")
		return
	}
	if target == "html" || target == "zip" {
		failCreateArtifactExportJob(c, "Target does not require an async export job", "async_export_not_required", target, "source_or_bundle_export")
		return
	}
	artifact, file, err := workagentService.NewArtifactRegistryRepository(nil).LoadForOwnerWithThreadFile(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
	)
	if err != nil {
		failCreateArtifactExportJob(c, "Artifact not found", "artifact_not_found", target, "")
		return
	}
	view := workagentService.ArtifactViewFromRegistryAndThreadFile(*artifact, *file)
	if view.OutputType != "html" && view.PreviewType != "html" {
		failCreateArtifactExportJob(c, "Artifact is not an HTML artifact", "not_html_artifact", target, view.OutputType)
		return
	}
	fullPath := api.resolveWorkspaceFilePath(file.FilePath)
	if fullPath == "" {
		failCreateArtifactExportJob(c, "Artifact file not found", "artifact_file_not_found", target, "")
		return
	}
	f, err := os.Open(fullPath)
	if err != nil {
		failCreateArtifactExportJob(c, "Artifact file not found", "artifact_file_not_found", target, "")
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, workAgentHTMLValidationMaxBytes+1))
	if err != nil {
		failCreateArtifactExportJob(c, "Failed to read artifact", "artifact_read_failed", target, err.Error())
		return
	}
	validation := workagentService.ValidateHTMLArtifactContent(string(raw))
	if view.HTMLValidation != nil && len(view.HTMLValidation.PreviewDiagnostics) > 0 {
		validation.PreviewDiagnostics = view.HTMLValidation.PreviewDiagnostics
	}
	if len(raw) > workAgentHTMLValidationMaxBytes {
		validation.Issues = append(validation.Issues, workagentService.ArtifactHTMLValidationIssue{
			Code:     "validation_truncated",
			Severity: "warn",
			Message:  "HTML artifact exceeded static validation size budget",
		})
		validation.IssueCount = len(validation.Issues)
		if validation.Status == workagentService.ArtifactHTMLValidationPass {
			validation.Status = workagentService.ArtifactHTMLValidationWarn
		}
	}
	view.HTMLValidation = &validation
	view.HTMLExportPlan = workagentService.BuildHTMLExportPlan(view)
	jobPlan := workagentService.BuildHTMLExportJobPlan(view, target)
	if jobPlan == nil {
		failCreateArtifactExportJob(c, "HTML export job plan is unavailable", "html_export_job_plan_unavailable", target, "")
		return
	}
	job, err := workagentService.NewArtifactExportJobRepository(nil).CreateFromHTMLJobPlan(workagentService.ArtifactExportJobInput{
		UID:          int(utils.GetUserID(c)),
		ThreadID:     uint(threadID),
		ArtifactID:   artifact.Id,
		ThreadFileID: file.Id,
		Plan:         *jobPlan,
	})
	if err != nil {
		failCreateArtifactExportJob(c, err.Error(), "artifact_export_job_create_failed", target, err.Error())
		return
	}
	response.OkWithData(gin.H{
		"job":     job,
		"jobPlan": jobPlan,
	}, c)
}

func (api *AIChatApiNew) GetArtifactExportJob(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	jobID, err := strconv.ParseUint(strings.TrimSpace(c.Param("jobId")), 10, 32)
	if err != nil || jobID == 0 {
		response.FailWithMessage("Invalid export job ID", c)
		return
	}
	job, err := workagentService.NewArtifactExportJobRepository(nil).LoadForOwner(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(jobID),
	)
	if err != nil || job.ArtifactID != uint(artifactID) {
		response.FailWithMessage("Artifact export job not found", c)
		return
	}
	response.OkWithData(job, c)
}

func htmlArtifactZipDownloadName(fileName string) string {
	name := strings.TrimSpace(strings.ReplaceAll(fileName, "\\", "/"))
	name = path.Base(name)
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		name = "artifact"
	}
	ext := strings.ToLower(path.Ext(name))
	if ext == ".html" || ext == ".htm" {
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		name = "artifact"
	}
	return name + ".zip"
}

func htmlArtifactStaticDownloadName(fileName string, target string) string {
	name := strings.TrimSpace(strings.ReplaceAll(fileName, "\\", "/"))
	name = path.Base(name)
	if name == "." || name == "/" || strings.TrimSpace(name) == "" {
		name = "artifact"
	}
	ext := strings.ToLower(path.Ext(name))
	if ext == ".html" || ext == ".htm" {
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		name = "artifact"
	}
	return name + htmlStaticDownloadExtension(target)
}

func htmlStaticDownloadExtension(target string) string {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "pdf":
		return ".pdf"
	case "png":
		return ".png"
	default:
		return ".bin"
	}
}

func failExportArtifact(c *gin.Context, message string, code string, target string, reason string) {
	emitExportArtifactMetric(c, "failed", code, target, reason, nil)
	response.FailWithDetailed(exportArtifactFailure{
		Code:   code,
		Target: target,
		Reason: reason,
	}, message, c)
}

func failCreateArtifactExportJob(c *gin.Context, message string, code string, target string, reason string) {
	response.FailWithDetailed(exportArtifactFailure{
		Code:   code,
		Target: target,
		Reason: reason,
	}, message, c)
}

func failArtifactBrowserValidation(c *gin.Context, message string, code string, reason string) {
	response.FailWithDetailed(browserValidationFailure{
		Code:   code,
		Reason: reason,
	}, message, c)
}

func failCreateArtifactVisualDiffReport(c *gin.Context, message string, code string, reason string) {
	response.FailWithDetailed(visualDiffFailure{
		Code:   code,
		Reason: reason,
	}, message, c)
}

func classifyArtifactVisualDiffFailure(err error) (string, string) {
	if err == nil {
		return "visual_diff_failed", ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "nil dependency"):
		return "visual_diff_service_unavailable", "nil_dependency"
	case strings.Contains(msg, "uid, thread, previous artifact, and latest artifact are required"):
		return "invalid_visual_diff_request", "missing_required_id"
	case strings.Contains(msg, "load previous artifact"):
		return "previous_artifact_not_found", "owner_scope_or_missing"
	case strings.Contains(msg, "load latest artifact"):
		return "latest_artifact_not_found", "owner_scope_or_missing"
	case strings.Contains(msg, "previous artifact is not a supported image"):
		return "unsupported_previous_artifact", "unsupported_comparable"
	case strings.Contains(msg, "latest artifact is not a supported image"):
		return "unsupported_latest_artifact", "unsupported_comparable"
	case strings.Contains(msg, "previous file is outside workspace"):
		return "previous_file_outside_workspace", "workspace_boundary"
	case strings.Contains(msg, "latest file is outside workspace"):
		return "latest_file_outside_workspace", "workspace_boundary"
	case strings.Contains(msg, "artifact screenshot renderer unavailable"):
		return "visual_diff_artifact_screenshot_unavailable", "renderer_unavailable"
	case strings.Contains(msg, "render artifact screenshot"):
		return "visual_diff_artifact_screenshot_failed", "renderer_failed"
	case strings.Contains(msg, "prepare previous image") && strings.Contains(msg, "read image"):
		return "previous_image_read_failed", "file_read_failed"
	case strings.Contains(msg, "prepare latest image") && strings.Contains(msg, "read image"):
		return "latest_image_read_failed", "file_read_failed"
	case strings.Contains(msg, "prepare previous image") && strings.Contains(msg, "read html source"):
		return "previous_html_read_failed", "file_read_failed"
	case strings.Contains(msg, "prepare latest image") && strings.Contains(msg, "read html source"):
		return "latest_html_read_failed", "file_read_failed"
	case strings.Contains(msg, "decode previous visual diff image"):
		return "previous_image_decode_failed", "decode_failed"
	case strings.Contains(msg, "decode latest visual diff image"):
		return "latest_image_decode_failed", "decode_failed"
	case strings.Contains(msg, "visual diff image has empty comparable bounds"):
		return "visual_diff_empty_bounds", "empty_comparable_bounds"
	case strings.Contains(msg, "build report"):
		return "visual_diff_report_build_failed", "report_build_failed"
	case strings.Contains(msg, "create output dir") || strings.Contains(msg, "write output file") || strings.Contains(msg, "output file is outside workspace") || strings.Contains(msg, "output filename is invalid"):
		return "visual_diff_report_write_failed", "workspace_output_failed"
	case strings.Contains(msg, "create visual diff report file") || strings.Contains(msg, "register visual diff report artifact") || strings.Contains(msg, "mark visual diff report artifact") || strings.Contains(msg, "reload visual diff report artifact"):
		return "visual_diff_report_register_failed", "artifact_registry_failed"
	default:
		return "visual_diff_failed", ""
	}
}

func classifyArtifactBrowserValidationFailure(err error) (string, string) {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "load artifact source"):
		return "artifact_not_found", ""
	case strings.Contains(msg, "outside workspace"):
		return "source_file_outside_workspace", ""
	case strings.Contains(msg, "read source html"):
		return "source_file_read_failed", err.Error()
	case strings.Contains(msg, "validator is required"):
		return "browser_validation_worker_unavailable", err.Error()
	case strings.Contains(msg, "nil dependency"):
		return "browser_validation_worker_unavailable", err.Error()
	case strings.Contains(msg, "replace html preview diagnostics"):
		return "browser_validation_diagnostics_update_failed", err.Error()
	case strings.Contains(msg, "update html preview diagnostics"):
		return "browser_validation_diagnostics_update_failed", err.Error()
	default:
		return "browser_validation_failed", err.Error()
	}
}

func setExportArtifactMetricView(c *gin.Context, view workagentService.ArtifactView) {
	c.Set("workagent_export_artifact_type", view.ArtifactType)
	c.Set("workagent_export_output_type", view.OutputType)
	c.Set("workagent_export_preview_type", view.PreviewType)
	c.Set("workagent_export_lifecycle_status", view.Status)
}

func emitExportArtifactMetric(c *gin.Context, status string, code string, target string, reason string, extra map[string]any) {
	if c == nil {
		return
	}
	fields := map[string]any{
		"uid":         utils.GetUserID(c),
		"thread_id":   c.Param("id"),
		"artifact_id": c.Param("artifactId"),
		"target":      strings.TrimSpace(strings.ToLower(target)),
		"status":      status,
	}
	if code != "" {
		fields["failure_code"] = code
	}
	if reason != "" {
		fields["failure_reason"] = reason
	}
	for _, key := range []string{
		"workagent_export_artifact_type",
		"workagent_export_output_type",
		"workagent_export_preview_type",
		"workagent_export_lifecycle_status",
	} {
		if value, exists := c.Get(key); exists {
			fields[strings.TrimPrefix(key, "workagent_export_")] = value
		}
	}
	for key, value := range extra {
		fields[key] = value
	}
	workagentService.EmitMetric("wa_artifact_export", fields)
}

func htmlZipExportFailureCode(err error) string {
	var packageErr *workagentService.HTMLZipPackageError
	if errors.As(err, &packageErr) && packageErr.Code != "" {
		return packageErr.Code
	}
	return "html_zip_build_failed"
}

func htmlZipExportFailureReason(err error) string {
	var packageErr *workagentService.HTMLZipPackageError
	if errors.As(err, &packageErr) && packageErr.SourceURL != "" {
		return packageErr.SourceURL
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstHTMLExportBlockReason(plan *workagentService.ArtifactHTMLExportPlan) string {
	if plan == nil {
		return ""
	}
	if plan.AssetBundleManifest != nil {
		for _, entry := range plan.AssetBundleManifest.Entries {
			if entry.Status == "blocked" && entry.Reason != "" {
				return entry.Reason
			}
		}
	}
	for _, target := range plan.Targets {
		if target.Status == "blocked" && target.Reason != "" {
			return target.Reason
		}
	}
	return ""
}

func (api *AIChatApiNew) ApplyArtifactDecision(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	var req applyArtifactDecisionRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactRegistryRepository(nil).ApplyComparisonDecision(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		req.Decision,
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func hasRemoteHTMLAssetReferences(refs []workagentService.ArtifactHTMLAssetReference) bool {
	for _, ref := range refs {
		if ref.Kind == "remote" {
			return true
		}
	}
	return false
}

func nonEmptyReviewState(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (api *AIChatApiNew) CreateArtifactAssetCandidate(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	artifactID, err := parseArtifactID(c.Param("artifactId"))
	if err != nil {
		response.FailWithMessage("Invalid artifact ID", c)
		return
	}
	var req createArtifactAssetCandidateRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactAssetCandidateRepository(nil).UpsertForArtifact(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(artifactID),
		workagentService.ArtifactAssetCandidateInput{
			AssetKind: req.AssetKind,
			Name:      req.Name,
			Slug:      req.Slug,
			Profile:   req.Profile,
		},
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func (api *AIChatApiNew) ListArtifactAssetCandidates(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	rows, err := workagentService.NewArtifactAssetCandidateRepository(nil).ListByThread(
		int(utils.GetUserID(c)),
		uint(threadID),
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(gin.H{
		"items": rows,
		"count": len(rows),
	}, c)
}

func (api *AIChatApiNew) UpdateArtifactAssetCandidateStatus(c *gin.Context) {
	threadID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}
	candidateID, err := strconv.ParseUint(strings.TrimSpace(c.Param("candidateId")), 10, 32)
	if err != nil || candidateID == 0 {
		response.FailWithMessage("Invalid candidate ID", c)
		return
	}
	var req updateArtifactAssetCandidateStatusRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}
	row, err := workagentService.NewArtifactAssetCandidateRepository(nil).UpdateStatus(
		int(utils.GetUserID(c)),
		uint(threadID),
		uint(candidateID),
		req.Status,
	)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	response.OkWithData(row, c)
}

func parseArtifactID(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "artifact-")
	if value == "" || strings.HasPrefix(value, "thread-file-") {
		return 0, fmt.Errorf("invalid artifact id")
	}
	return strconv.ParseUint(value, 10, 32)
}
