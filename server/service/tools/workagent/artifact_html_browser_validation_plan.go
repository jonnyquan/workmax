package workagent

const (
	ArtifactHTMLBrowserValidationNotRequired = "not_required"
	ArtifactHTMLBrowserValidationPending     = "pending"
	ArtifactHTMLBrowserValidationFailed      = "failed"
	ArtifactHTMLBrowserValidationBlocked     = "blocked"
)

type ArtifactHTMLBrowserValidationPlan struct {
	Status      string                                  `json:"status"`
	Reason      string                                  `json:"reason,omitempty"`
	Targets     []string                                `json:"targets,omitempty"`
	Viewports   []ArtifactHTMLBrowserValidationViewport `json:"viewports,omitempty"`
	Checks      []string                                `json:"checks,omitempty"`
	Diagnostics []ArtifactHTMLValidationIssue           `json:"diagnostics,omitempty"`
}

type ArtifactHTMLBrowserValidationViewport struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func BuildHTMLBrowserValidationPlan(view ArtifactView) *ArtifactHTMLBrowserValidationPlan {
	if view.OutputType != "html" && view.PreviewType != "html" {
		return nil
	}
	plan := &ArtifactHTMLBrowserValidationPlan{
		Status:    ArtifactHTMLBrowserValidationNotRequired,
		Reason:    "no_browser_sensitive_targets",
		Viewports: defaultHTMLBrowserValidationViewports(),
		Checks:    defaultHTMLBrowserValidationChecks(),
	}
	validation := view.HTMLValidation
	if validation != nil {
		plan.Diagnostics = validation.PreviewDiagnostics
		if hasBlockingHTMLValidationIssue(*validation) {
			plan.Status = ArtifactHTMLBrowserValidationBlocked
			plan.Reason = "html_validation_block"
			return plan
		}
		if hasBrowserValidationError(*validation) {
			plan.Status = ArtifactHTMLBrowserValidationFailed
			plan.Reason = "browser_validation_failed"
			return plan
		}
		if hasBrowserValidationPassed(*validation) {
			plan.Status = ArtifactHTMLBrowserValidationNotRequired
			plan.Reason = "browser_validation_passed"
			return plan
		}
		if hasFailingHTMLPreviewDiagnostic(*validation) {
			plan.Status = ArtifactHTMLBrowserValidationFailed
			plan.Reason = "preview_runtime_error"
			return plan
		}
	}
	targets := browserValidationTargets(view.HTMLExportPlan)
	if len(targets) == 0 {
		return plan
	}
	plan.Status = ArtifactHTMLBrowserValidationPending
	plan.Reason = "browser_validation_required"
	plan.Targets = targets
	return plan
}

func defaultHTMLBrowserValidationViewports() []ArtifactHTMLBrowserValidationViewport {
	return []ArtifactHTMLBrowserValidationViewport{
		{Name: "desktop", Width: 1440, Height: 900},
		{Name: "tablet", Width: 834, Height: 1112},
		{Name: "mobile", Width: 390, Height: 844},
	}
}

func defaultHTMLBrowserValidationChecks() []string {
	return []string{
		"screenshot_nonblank",
		"console_errors",
		"resource_errors",
		"text_overflow",
		"scroll_bounds",
		"dom_bounds",
		"screenshot_flat_color",
		"screenshot_low_detail",
		"screenshot_size_mismatch",
		"viewport_screenshot_near_identical",
		"viewport_screenshot_low_structure_delta",
		"viewport_screenshot_low_text_edge_delta",
		"viewport_screenshot_low_pixel_delta",
		"viewport_screenshot_low_perceptual_delta",
	}
}

func browserValidationTargets(plan *ArtifactHTMLExportPlan) []string {
	if plan == nil {
		return nil
	}
	targets := make([]string, 0, 2)
	for _, target := range plan.Targets {
		if target.Status == ArtifactHTMLExportNeedsBrowserValidation {
			targets = append(targets, target.Target)
		}
	}
	return targets
}

func hasBlockingHTMLValidationIssue(validation ArtifactHTMLValidationResult) bool {
	if validation.Status == ArtifactHTMLValidationBlock {
		return true
	}
	for _, issue := range validation.Issues {
		if issue.Severity == "block" {
			return true
		}
	}
	return false
}

func hasFailingHTMLPreviewDiagnostic(validation ArtifactHTMLValidationResult) bool {
	for _, issue := range validation.PreviewDiagnostics {
		if issue.Severity == "error" {
			return true
		}
	}
	return false
}
