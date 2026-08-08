package workagent

import "testing"

func TestBuildHTMLBrowserValidationPlan_NotRequiredForCleanHTML(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "zip"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationPass,
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationNotRequired {
		t.Fatalf("status = %q, want not_required", got.Status)
	}
	if len(got.Viewports) != 3 {
		t.Fatalf("viewports = %#v, want default desktop/tablet/mobile set", got.Viewports)
	}
	if len(got.Checks) == 0 {
		t.Fatal("expected browser validation checks")
	}
	if !containsString(got.Checks, "screenshot_flat_color") {
		t.Fatalf("checks = %#v, want screenshot_flat_color", got.Checks)
	}
	if !containsString(got.Checks, "screenshot_size_mismatch") {
		t.Fatalf("checks = %#v, want screenshot_size_mismatch", got.Checks)
	}
	if !containsString(got.Checks, "dom_bounds") {
		t.Fatalf("checks = %#v, want dom_bounds", got.Checks)
	}
	if !containsString(got.Checks, "viewport_screenshot_low_perceptual_delta") {
		t.Fatalf("checks = %#v, want viewport_screenshot_low_perceptual_delta", got.Checks)
	}
	if !containsString(got.Checks, "viewport_screenshot_low_structure_delta") {
		t.Fatalf("checks = %#v, want viewport_screenshot_low_structure_delta", got.Checks)
	}
	if !containsString(got.Checks, "viewport_screenshot_low_text_edge_delta") {
		t.Fatalf("checks = %#v, want viewport_screenshot_low_text_edge_delta", got.Checks)
	}
	if !containsString(got.Checks, "viewport_screenshot_low_pixel_delta") {
		t.Fatalf("checks = %#v, want viewport_screenshot_low_pixel_delta", got.Checks)
	}
}

func TestBuildHTMLBrowserValidationPlan_PendingForBrowserSensitiveTargets(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf", "zip"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "missing_artboard_constraints", Severity: "warn", Message: "missing fixed artboard"},
			},
			IssueCount: 1,
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
	if got.Reason != "browser_validation_required" {
		t.Fatalf("reason = %q, want browser_validation_required", got.Reason)
	}
	if !containsString(got.Targets, "png") || !containsString(got.Targets, "pdf") {
		t.Fatalf("targets = %#v, want png and pdf", got.Targets)
	}
}

func TestBuildHTMLBrowserValidationPlan_FailedForRuntimeErrors(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "resource_error", Severity: "error", Message: "img failed: missing.png", Source: "preview_runtime"},
			},
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Reason != "preview_runtime_error" {
		t.Fatalf("reason = %q, want preview_runtime_error", got.Reason)
	}
	if len(got.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one runtime diagnostic", got.Diagnostics)
	}
}

func TestBuildHTMLBrowserValidationPlan_NotRequiredAfterBrowserValidationPassed(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "scrollable_artboard", Severity: "warn", Message: "scrollable"},
			},
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_validation_passed", Severity: "info", Message: "passed", Source: "browser_validation"},
			},
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	if statusForHTMLExportTarget(view.HTMLExportPlan, "png") != ArtifactHTMLExportReady {
		t.Fatalf("png export status = %#v, want ready after browser validation passed", view.HTMLExportPlan.Targets)
	}
	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationNotRequired || got.Reason != "browser_validation_passed" {
		t.Fatalf("plan = %#v, want not_required/browser_validation_passed", got)
	}
}

func TestBuildHTMLBrowserValidationPlan_FailedForBrowserValidationErrors(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "scrollable_artboard", Severity: "warn", Message: "scrollable"},
			},
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_screenshot_blank", Severity: "error", Message: "blank", Source: "browser_validation"},
				{Code: "browser_validation_passed", Severity: "info", Message: "stale pass", Source: "browser_validation"},
			},
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Reason != "browser_validation_failed" {
		t.Fatalf("reason = %q, want browser_validation_failed", got.Reason)
	}
	if len(got.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v, want persisted browser diagnostics", got.Diagnostics)
	}
}

func TestBuildHTMLBrowserValidationPlan_BlockedForValidationBlocks(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationBlock,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "external_script", Severity: "block", Message: "external script"},
			},
			IssueCount: 1,
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)

	got := BuildHTMLBrowserValidationPlan(view)
	if got == nil {
		t.Fatal("plan is nil")
	}
	if got.Status != ArtifactHTMLBrowserValidationBlocked {
		t.Fatalf("status = %q, want blocked", got.Status)
	}
	if got.Reason != "html_validation_block" {
		t.Fatalf("reason = %q, want html_validation_block", got.Reason)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
