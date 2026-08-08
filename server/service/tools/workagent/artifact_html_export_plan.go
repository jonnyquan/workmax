package workagent

import "strings"

const (
	ArtifactHTMLExportReady                  = "ready"
	ArtifactHTMLExportNeedsAssets            = "needs_assets"
	ArtifactHTMLExportNeedsBrowserValidation = "needs_browser_validation"
	ArtifactHTMLExportBlocked                = "blocked"
	ArtifactHTMLExportUnsupported            = "unsupported"
)

type ArtifactHTMLExportPlan struct {
	Targets             []ArtifactHTMLExportTargetPlan   `json:"targets"`
	AssetReferences     []ArtifactHTMLAssetReference     `json:"assetReferences,omitempty"`
	AssetBundleManifest *ArtifactHTMLAssetBundleManifest `json:"assetBundleManifest,omitempty"`
	NextSteps           []ArtifactHTMLExportNextStep     `json:"nextSteps,omitempty"`
	Blocked             bool                             `json:"blocked"`
}

type ArtifactHTMLExportTargetPlan struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type ArtifactHTMLExportNextStep struct {
	Action  string   `json:"action"`
	Targets []string `json:"targets,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

func BuildHTMLExportPlan(view ArtifactView) *ArtifactHTMLExportPlan {
	if view.OutputType != "html" && view.PreviewType != "html" {
		return nil
	}
	targets := view.ExportTargets
	if len(targets) == 0 {
		targets = inferExportTargets("html")
	}
	var validation ArtifactHTMLValidationResult
	if view.HTMLValidation != nil {
		validation = *view.HTMLValidation
	}
	refs := validation.AssetReferences
	plan := &ArtifactHTMLExportPlan{
		Targets:             make([]ArtifactHTMLExportTargetPlan, 0, len(targets)),
		AssetReferences:     refs,
		AssetBundleManifest: BuildHTMLAssetBundleManifest(view),
	}
	for _, target := range targets {
		targetPlan := buildHTMLExportTargetPlan(strings.TrimSpace(strings.ToLower(target)), validation, refs)
		if targetPlan.Target == "" {
			continue
		}
		if targetPlan.Status == ArtifactHTMLExportBlocked {
			plan.Blocked = true
		}
		plan.Targets = append(plan.Targets, targetPlan)
	}
	plan.NextSteps = buildHTMLExportNextSteps(plan.Targets)
	return plan
}

func buildHTMLExportTargetPlan(target string, validation ArtifactHTMLValidationResult, refs []ArtifactHTMLAssetReference) ArtifactHTMLExportTargetPlan {
	if target == "" {
		return ArtifactHTMLExportTargetPlan{}
	}
	if !supportedHTMLExportTarget(target) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: "unsupported", Status: ArtifactHTMLExportUnsupported, Reason: "unsupported_target"}
	}
	kind := htmlExportTargetKind(target)
	if validation.Status == ArtifactHTMLValidationBlock {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportBlocked, Reason: "html_validation_block"}
	}
	if target != "html" && hasRemoteHTMLAssetReference(refs) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportNeedsAssets, Reason: "remote_asset_reference"}
	}
	if target != "html" && hasLocalHTMLAssetReference(refs) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportNeedsAssets, Reason: "asset_bundle_required"}
	}
	if target != "html" && hasHTMLPreviewResourceError(validation) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportNeedsAssets, Reason: "preview_resource_error"}
	}
	if target != "html" && hasHTMLPreviewRuntimeError(validation) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportBlocked, Reason: "preview_runtime_error"}
	}
	if target != "html" && hasBrowserValidationResourceMissing(validation) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportNeedsAssets, Reason: "browser_resource_missing"}
	}
	if target != "html" && hasBrowserValidationError(validation) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportBlocked, Reason: "browser_validation_failed"}
	}
	if (target == "mp4" || target == "gif") && hasHTMLValidationIssue(validation, "invalid_motion_timeline") {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportBlocked, Reason: "motion_timeline_invalid"}
	}
	if (target == "mp4" || target == "gif") && hasHTMLValidationIssue(validation, "missing_motion_timeline") {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportBlocked, Reason: "motion_timeline_required"}
	}
	if target != "html" && hasBrowserSensitiveHTMLIssue(validation) && !hasBrowserValidationPassed(validation) {
		return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportNeedsBrowserValidation, Reason: "browser_validation_required"}
	}
	return ArtifactHTMLExportTargetPlan{Target: target, Kind: kind, Status: ArtifactHTMLExportReady}
}

func supportedHTMLExportTarget(target string) bool {
	return IsSupportedHTMLExportTarget(target)
}

func IsSupportedHTMLExportTarget(target string) bool {
	switch target {
	case "html", "png", "pdf", "mp4", "gif", "zip":
		return true
	default:
		return false
	}
}

func htmlExportTargetKind(target string) string {
	switch target {
	case "html":
		return "source"
	case "zip":
		return "bundle"
	case "png", "pdf":
		return "render_static"
	case "mp4", "gif":
		return "render_motion"
	default:
		return "unsupported"
	}
}

func hasBrowserSensitiveHTMLIssue(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range validation.Issues {
		if issue.Source == "preview_runtime" && issue.Severity == "error" {
			return true
		}
		switch issue.Code {
		case "missing_artboard_constraints", "viewport_sized_artboard", "viewport_width_missing", "viewport_initial_scale_missing", "viewport_initial_scale_invalid", "viewport_zoom_locked", "viewport_multiple", "duplicate_element_id", "scrollable_artboard", "out_of_bounds_position", "missing_reduced_motion", "inline_script":
			return true
		}
	}
	return false
}

func hasHTMLValidationIssue(validation ArtifactHTMLValidationResult, code string) bool {
	for _, issue := range validation.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func hasHTMLPreviewResourceError(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range append(validation.Issues, validation.PreviewDiagnostics...) {
		if issue.Source == "preview_runtime" && issue.Code == "resource_error" && issue.Severity == "error" {
			return true
		}
	}
	return false
}

func hasHTMLPreviewRuntimeError(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range append(validation.Issues, validation.PreviewDiagnostics...) {
		if issue.Source == "preview_runtime" && isBlockingDiagnosticSeverity(issue.Severity) {
			return true
		}
	}
	return false
}

func hasBrowserValidationPassed(validation ArtifactHTMLValidationResult) bool {
	if hasBrowserValidationError(validation) {
		return false
	}
	for _, issue := range validation.PreviewDiagnostics {
		if issue.Source == "browser_validation" && issue.Code == "browser_validation_passed" && issue.Severity == "info" {
			return true
		}
	}
	return false
}

func hasBrowserValidationResourceMissing(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range validation.PreviewDiagnostics {
		if issue.Source == "browser_validation" && issue.Code == "browser_resource_missing" && isBlockingDiagnosticSeverity(issue.Severity) {
			return true
		}
	}
	return false
}

func hasBrowserValidationError(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range validation.PreviewDiagnostics {
		if issue.Source == "browser_validation" && isBlockingDiagnosticSeverity(issue.Severity) {
			return true
		}
	}
	return false
}

func isBlockingDiagnosticSeverity(severity string) bool {
	return severity == "error" || severity == "block"
}

func hasRemoteHTMLAssetReference(refs []ArtifactHTMLAssetReference) bool {
	for _, ref := range refs {
		if ref.Kind == "remote" || ref.Action == "inline_or_mirror" {
			return true
		}
	}
	return false
}

func hasLocalHTMLAssetReference(refs []ArtifactHTMLAssetReference) bool {
	for _, ref := range refs {
		if ref.Kind == "local" || ref.Action == "bundle" {
			return true
		}
	}
	return false
}

func buildHTMLExportNextSteps(targets []ArtifactHTMLExportTargetPlan) []ArtifactHTMLExportNextStep {
	type stepKey struct {
		status string
		reason string
	}
	byKey := map[stepKey][]string{}
	for _, target := range targets {
		switch target.Status {
		case ArtifactHTMLExportNeedsAssets, ArtifactHTMLExportNeedsBrowserValidation, ArtifactHTMLExportBlocked:
			reason := target.Reason
			if reason == "" {
				reason = defaultHTMLExportStepReason(target.Status)
			}
			byKey[stepKey{status: target.Status, reason: reason}] = append(byKey[stepKey{status: target.Status, reason: reason}], target.Target)
		}
	}
	steps := make([]ArtifactHTMLExportNextStep, 0, 3)
	if targets := byKey[stepKey{status: ArtifactHTMLExportBlocked, reason: "html_validation_block"}]; len(targets) > 0 {
		steps = append(steps, ArtifactHTMLExportNextStep{
			Action:  "fix_validation_blocks",
			Targets: targets,
			Reason:  "html_validation_block",
		})
	}
	if targets := byKey[stepKey{status: ArtifactHTMLExportBlocked, reason: "preview_runtime_error"}]; len(targets) > 0 {
		steps = append(steps, ArtifactHTMLExportNextStep{
			Action:  "fix_preview_runtime",
			Targets: targets,
			Reason:  "preview_runtime_error",
		})
	}
	for _, reason := range []string{"asset_bundle_required", "preview_resource_error", "browser_resource_missing"} {
		if targets := byKey[stepKey{status: ArtifactHTMLExportNeedsAssets, reason: reason}]; len(targets) > 0 {
			steps = append(steps, ArtifactHTMLExportNextStep{
				Action:  "bundle_assets",
				Targets: targets,
				Reason:  reason,
			})
		}
	}
	if targets := byKey[stepKey{status: ArtifactHTMLExportNeedsAssets, reason: "remote_asset_reference"}]; len(targets) > 0 {
		steps = append(steps, ArtifactHTMLExportNextStep{
			Action:  "mirror_remote_assets",
			Targets: targets,
			Reason:  "remote_asset_reference",
		})
	}
	if targets := byKey[stepKey{status: ArtifactHTMLExportBlocked, reason: "browser_validation_failed"}]; len(targets) > 0 {
		steps = append(steps, ArtifactHTMLExportNextStep{
			Action:  "fix_browser_validation",
			Targets: targets,
			Reason:  "browser_validation_failed",
		})
	}
	for _, reason := range []string{"motion_timeline_invalid", "motion_timeline_required"} {
		if targets := byKey[stepKey{status: ArtifactHTMLExportBlocked, reason: reason}]; len(targets) > 0 {
			steps = append(steps, ArtifactHTMLExportNextStep{
				Action:  "define_motion_timeline",
				Targets: targets,
				Reason:  reason,
			})
		}
	}
	if targets := byKey[stepKey{status: ArtifactHTMLExportNeedsBrowserValidation, reason: "browser_validation_required"}]; len(targets) > 0 {
		steps = append(steps, ArtifactHTMLExportNextStep{
			Action:  "run_browser_validation",
			Targets: targets,
			Reason:  "browser_validation_required",
		})
	}
	return steps
}

func defaultHTMLExportStepReason(status string) string {
	switch status {
	case ArtifactHTMLExportBlocked:
		return "html_validation_block"
	case ArtifactHTMLExportNeedsAssets:
		return "asset_bundle_required"
	case ArtifactHTMLExportNeedsBrowserValidation:
		return "browser_validation_required"
	default:
		return status
	}
}
