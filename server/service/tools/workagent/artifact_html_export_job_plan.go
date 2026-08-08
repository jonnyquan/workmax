package workagent

import "strings"

const (
	ArtifactHTMLExportJobReady         = "ready"
	ArtifactHTMLExportJobBlocked       = "blocked"
	ArtifactHTMLExportJobWorkerPending = "worker_pending"
	ArtifactHTMLExportJobUnsupported   = "unsupported"
)

type ArtifactHTMLExportJobPlan struct {
	Target          string   `json:"target"`
	Kind            string   `json:"kind"`
	Worker          string   `json:"worker"`
	Status          string   `json:"status"`
	Reason          string   `json:"reason,omitempty"`
	OutputExtension string   `json:"outputExtension,omitempty"`
	Prerequisites   []string `json:"prerequisites,omitempty"`
}

func BuildHTMLExportJobPlan(view ArtifactView, target string) *ArtifactHTMLExportJobPlan {
	target = strings.TrimSpace(strings.ToLower(target))
	if view.OutputType != "html" && view.PreviewType != "html" {
		return nil
	}
	if target == "" {
		return &ArtifactHTMLExportJobPlan{
			Status: ArtifactHTMLExportJobUnsupported,
			Reason: "missing_target",
		}
	}
	if !supportedHTMLExportTarget(target) {
		return &ArtifactHTMLExportJobPlan{
			Target: target,
			Kind:   "unsupported",
			Status: ArtifactHTMLExportJobUnsupported,
			Reason: "unsupported_target",
		}
	}
	targetPlan := findHTMLExportTargetPlan(view.HTMLExportPlan, target)
	if targetPlan.Target == "" {
		targetPlan = buildHTMLExportTargetPlan(target, htmlValidationFromView(view), nil)
	}
	job := &ArtifactHTMLExportJobPlan{
		Target:          target,
		Kind:            targetPlan.Kind,
		OutputExtension: htmlExportOutputExtension(target),
		Prerequisites:   htmlExportJobPrerequisites(target),
	}
	if targetPlan.Status != "" && targetPlan.Status != ArtifactHTMLExportReady {
		job.Status = ArtifactHTMLExportJobBlocked
		job.Reason = targetPlan.Reason
		if job.Reason == "" {
			job.Reason = targetPlan.Status
		}
		return job
	}
	switch target {
	case "html":
		job.Worker = "source_file"
		job.Status = ArtifactHTMLExportJobReady
	case "zip":
		job.Worker = "zip_package"
		job.Status = ArtifactHTMLExportJobReady
	case "png", "pdf":
		job.Worker = "browser_static_render"
		job.Status = ArtifactHTMLExportJobWorkerPending
		job.Reason = "render_static_worker_unavailable"
	case "mp4", "gif":
		job.Worker = "browser_motion_render"
		job.Status = ArtifactHTMLExportJobWorkerPending
		job.Reason = "render_motion_worker_unavailable"
	default:
		job.Kind = "unsupported"
		job.Status = ArtifactHTMLExportJobUnsupported
		job.Reason = "unsupported_target"
	}
	return job
}

func findHTMLExportTargetPlan(plan *ArtifactHTMLExportPlan, target string) ArtifactHTMLExportTargetPlan {
	if plan == nil {
		return ArtifactHTMLExportTargetPlan{}
	}
	for _, candidate := range plan.Targets {
		if candidate.Target == target {
			return candidate
		}
	}
	return ArtifactHTMLExportTargetPlan{}
}

func htmlValidationFromView(view ArtifactView) ArtifactHTMLValidationResult {
	if view.HTMLValidation == nil {
		return ArtifactHTMLValidationResult{}
	}
	return *view.HTMLValidation
}

func htmlExportOutputExtension(target string) string {
	switch target {
	case "html":
		return ".html"
	case "zip":
		return ".zip"
	case "png":
		return ".png"
	case "pdf":
		return ".pdf"
	case "mp4":
		return ".mp4"
	case "gif":
		return ".gif"
	default:
		return ""
	}
}

func htmlExportJobPrerequisites(target string) []string {
	switch target {
	case "png", "pdf":
		return []string{"asset_bundle_ready", "browser_validation_passed"}
	case "mp4", "gif":
		return []string{"asset_bundle_ready", "browser_validation_passed", "motion_timeline_ready"}
	case "zip":
		return []string{"asset_bundle_manifest_ready"}
	default:
		return nil
	}
}
