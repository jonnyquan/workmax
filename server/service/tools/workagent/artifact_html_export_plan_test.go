package workagent

import "testing"

func TestBuildHTMLExportPlan_MarksTargetsReadyWhenClean(t *testing.T) {
	view := ArtifactView{
		OutputType:     "html",
		PreviewType:    "html",
		ExportTargets:  []string{"html", "png", "pdf", "zip"},
		HTMLValidation: &ArtifactHTMLValidationResult{Status: ArtifactHTMLValidationPass},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	for _, target := range got.Targets {
		if target.Status != ArtifactHTMLExportReady {
			t.Fatalf("target %s status = %q, want ready", target.Target, target.Status)
		}
	}
}

func TestBuildHTMLExportPlan_BlocksAllTargetsOnValidationBlock(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationBlock,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "external_script", Severity: "block"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil || !got.Blocked {
		t.Fatalf("expected blocked plan, got %#v", got)
	}
	for _, target := range got.Targets {
		if target.Status != ArtifactHTMLExportBlocked || target.Reason != "html_validation_block" {
			t.Fatalf("target %#v, want blocked html_validation_block", target)
		}
	}
}

func TestBuildHTMLExportPlan_RequiresAssetsForRenderedTargets(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "zip"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			AssetReferences: []ArtifactHTMLAssetReference{
				{URL: "./hero.png", Kind: "local", Source: "html_attr", Action: "bundle"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html target should stay ready, got %#v", got.Targets)
	}
	if kindForHTMLExportTarget(got, "html") != "source" ||
		kindForHTMLExportTarget(got, "png") != "render_static" ||
		kindForHTMLExportTarget(got, "zip") != "bundle" {
		t.Fatalf("unexpected target kinds: %#v", got.Targets)
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportNeedsAssets ||
		statusForHTMLExportTarget(got, "zip") != ArtifactHTMLExportNeedsAssets {
		t.Fatalf("rendered targets should need assets, got %#v", got.Targets)
	}
	if !hasHTMLExportNextStep(got, "bundle_assets", "png") ||
		!hasHTMLExportNextStep(got, "bundle_assets", "zip") {
		t.Fatalf("expected bundle_assets next step, got %#v", got.NextSteps)
	}
	if got.AssetBundleManifest == nil || len(got.AssetBundleManifest.Entries) != 1 {
		t.Fatalf("expected bundle manifest entry, got %#v", got.AssetBundleManifest)
	}
}

func TestBuildHTMLExportPlan_RequiresRemoteAssetsToBeMirroredOrInlined(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "zip"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			AssetReferences: []ArtifactHTMLAssetReference{
				{URL: "https://cdn.example.com/hero.png", Kind: "remote", Source: "html_attr", Action: "inline_or_mirror"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html target should stay ready, got %#v", got.Targets)
	}
	for _, target := range []string{"png", "zip"} {
		if statusForHTMLExportTarget(got, target) != ArtifactHTMLExportNeedsAssets {
			t.Fatalf("%s target should need assets, got %#v", target, got.Targets)
		}
		if reasonForHTMLExportTarget(got, target) != "remote_asset_reference" {
			t.Fatalf("%s reason = %q, want remote_asset_reference", target, reasonForHTMLExportTarget(got, target))
		}
		if !hasHTMLExportNextStepWithReason(got, "mirror_remote_assets", target, "remote_asset_reference") {
			t.Fatalf("expected mirror_remote_assets next step for %s, got %#v", target, got.NextSteps)
		}
	}
}

func TestBuildHTMLExportPlan_ClassifiesMotionTargets(t *testing.T) {
	view := ArtifactView{
		OutputType:     "html",
		PreviewType:    "html",
		ExportTargets:  []string{"mp4", "gif"},
		HTMLValidation: &ArtifactHTMLValidationResult{Status: ArtifactHTMLValidationPass},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if kindForHTMLExportTarget(got, "mp4") != "render_motion" ||
		kindForHTMLExportTarget(got, "gif") != "render_motion" {
		t.Fatalf("motion targets should be render_motion, got %#v", got.Targets)
	}
}

func TestBuildHTMLExportPlan_BlocksMotionTargetsWithInvalidTimeline(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "mp4", "gif"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "invalid_motion_timeline", Severity: "warn"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady ||
		statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportReady {
		t.Fatalf("non-motion targets should stay ready, got %#v", got.Targets)
	}
	for _, target := range []string{"mp4", "gif"} {
		if statusForHTMLExportTarget(got, target) != ArtifactHTMLExportBlocked {
			t.Fatalf("%s target status = %q, want blocked; targets=%#v", target, statusForHTMLExportTarget(got, target), got.Targets)
		}
		if reasonForHTMLExportTarget(got, target) != "motion_timeline_invalid" {
			t.Fatalf("%s reason = %q, want motion_timeline_invalid", target, reasonForHTMLExportTarget(got, target))
		}
		if !hasHTMLExportNextStepWithReason(got, "define_motion_timeline", target, "motion_timeline_invalid") {
			t.Fatalf("expected define_motion_timeline next step for %s, got %#v", target, got.NextSteps)
		}
	}
}

func TestBuildHTMLExportPlan_BlocksMotionTargetsMissingTimeline(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"mp4"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "missing_motion_timeline", Severity: "warn"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "mp4") != ArtifactHTMLExportBlocked {
		t.Fatalf("mp4 target status = %q, want blocked; targets=%#v", statusForHTMLExportTarget(got, "mp4"), got.Targets)
	}
	if reasonForHTMLExportTarget(got, "mp4") != "motion_timeline_required" {
		t.Fatalf("mp4 reason = %q, want motion_timeline_required", reasonForHTMLExportTarget(got, "mp4"))
	}
	if !hasHTMLExportNextStepWithReason(got, "define_motion_timeline", "mp4", "motion_timeline_required") {
		t.Fatalf("expected define_motion_timeline next step, got %#v", got.NextSteps)
	}
}

func TestBuildHTMLExportPlan_RequiresBrowserValidationForLayoutSensitiveWarnings(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "scrollable_artboard", Severity: "warn"},
				{Code: "out_of_bounds_position", Severity: "warn"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportNeedsBrowserValidation ||
		statusForHTMLExportTarget(got, "pdf") != ArtifactHTMLExportNeedsBrowserValidation {
		t.Fatalf("targets should need browser validation, got %#v", got.Targets)
	}
	if !hasHTMLExportNextStep(got, "run_browser_validation", "png") ||
		!hasHTMLExportNextStep(got, "run_browser_validation", "pdf") {
		t.Fatalf("expected run_browser_validation next step, got %#v", got.NextSteps)
	}
}

func TestBuildHTMLExportPlan_RequiresBrowserValidationForViewportWarnings(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "viewport_width_missing", Severity: "warn"},
				{Code: "viewport_initial_scale_missing", Severity: "warn"},
				{Code: "viewport_initial_scale_invalid", Severity: "warn"},
				{Code: "viewport_zoom_locked", Severity: "warn"},
				{Code: "viewport_multiple", Severity: "warn"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	for _, target := range []string{"png", "pdf"} {
		if statusForHTMLExportTarget(got, target) != ArtifactHTMLExportNeedsBrowserValidation {
			t.Fatalf("%s target status = %q, want needs_browser_validation; targets=%#v", target, statusForHTMLExportTarget(got, target), got.Targets)
		}
		if reasonForHTMLExportTarget(got, target) != "browser_validation_required" {
			t.Fatalf("%s reason = %q, want browser_validation_required", target, reasonForHTMLExportTarget(got, target))
		}
		if !hasHTMLExportNextStepWithReason(got, "run_browser_validation", target, "browser_validation_required") {
			t.Fatalf("expected run_browser_validation next step for %s, got %#v", target, got.NextSteps)
		}
	}
}

func TestBuildHTMLExportPlan_RequiresBrowserValidationForDuplicateElementIDs(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "duplicate_element_id", Severity: "warn"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportNeedsBrowserValidation {
		t.Fatalf("png target status = %q, want needs_browser_validation; targets=%#v", statusForHTMLExportTarget(got, "png"), got.Targets)
	}
	if !hasHTMLExportNextStepWithReason(got, "run_browser_validation", "png", "browser_validation_required") {
		t.Fatalf("expected run_browser_validation next step, got %#v", got.NextSteps)
	}
}

func TestBuildHTMLExportPlan_UsesPreviewDiagnosticsForExportReadiness(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "resource_error", Severity: "error", Source: "preview_runtime", Message: "img failed: missing.png"},
			},
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "resource_error", Severity: "error", Source: "preview_runtime", Message: "img failed: missing.png"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportNeedsAssets ||
		statusForHTMLExportTarget(got, "pdf") != ArtifactHTMLExportNeedsAssets {
		t.Fatalf("rendered targets should need assets after preview resource errors, got %#v", got.Targets)
	}
	if reasonForHTMLExportTarget(got, "png") != "preview_resource_error" {
		t.Fatalf("png reason = %q, want preview_resource_error", reasonForHTMLExportTarget(got, "png"))
	}
	if !hasHTMLExportNextStepWithReason(got, "bundle_assets", "png", "preview_resource_error") ||
		!hasHTMLExportNextStepWithReason(got, "bundle_assets", "pdf", "preview_resource_error") {
		t.Fatalf("expected preview_resource_error bundle_assets next step, got %#v", got.NextSteps)
	}
}

func TestBuildHTMLExportPlan_BlocksRenderedTargetsAfterPreviewRuntimeError(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf", "mp4"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "console", Severity: "error", Source: "preview_runtime", Message: "ReferenceError: missing"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	for _, target := range []string{"png", "pdf", "mp4"} {
		if statusForHTMLExportTarget(got, target) != ArtifactHTMLExportBlocked {
			t.Fatalf("%s target status = %q, want blocked; targets=%#v", target, statusForHTMLExportTarget(got, target), got.Targets)
		}
		if reasonForHTMLExportTarget(got, target) != "preview_runtime_error" {
			t.Fatalf("%s reason = %q, want preview_runtime_error", target, reasonForHTMLExportTarget(got, target))
		}
		if !hasHTMLExportNextStepWithReason(got, "fix_preview_runtime", target, "preview_runtime_error") {
			t.Fatalf("expected fix_preview_runtime next step for %s, got %#v", target, got.NextSteps)
		}
	}
}

func TestBuildHTMLExportPlan_UsesBrowserValidationMissingResourceForExportReadiness(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "scrollable_artboard", Severity: "warn", Message: "scrollable"},
			},
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_resource_missing", Severity: "error", Source: "browser_validation", Message: "missing ./hero.png"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportNeedsAssets ||
		statusForHTMLExportTarget(got, "pdf") != ArtifactHTMLExportNeedsAssets {
		t.Fatalf("rendered targets should need assets after browser resource errors, got %#v", got.Targets)
	}
	if reasonForHTMLExportTarget(got, "png") != "browser_resource_missing" {
		t.Fatalf("png reason = %q, want browser_resource_missing", reasonForHTMLExportTarget(got, "png"))
	}
	if !hasHTMLExportNextStepWithReason(got, "bundle_assets", "png", "browser_resource_missing") ||
		!hasHTMLExportNextStepWithReason(got, "bundle_assets", "pdf", "browser_resource_missing") {
		t.Fatalf("expected browser_resource_missing bundle_assets next step, got %#v", got.NextSteps)
	}
}

func TestBuildHTMLExportPlan_BlocksRenderedTargetsAfterBrowserValidationFailure(t *testing.T) {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: []string{"html", "png", "pdf"},
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status: ArtifactHTMLValidationWarn,
			Issues: []ArtifactHTMLValidationIssue{
				{Code: "scrollable_artboard", Severity: "warn", Message: "scrollable"},
			},
			PreviewDiagnostics: []ArtifactHTMLValidationIssue{
				{Code: "browser_screenshot_blank", Severity: "error", Source: "browser_validation", Message: "blank"},
				{Code: "browser_validation_passed", Severity: "info", Source: "browser_validation", Message: "stale pass"},
			},
		},
	}
	got := BuildHTMLExportPlan(view)
	if got == nil {
		t.Fatal("expected export plan")
	}
	if statusForHTMLExportTarget(got, "html") != ArtifactHTMLExportReady {
		t.Fatalf("html source target should stay ready, got %#v", got.Targets)
	}
	if statusForHTMLExportTarget(got, "png") != ArtifactHTMLExportBlocked ||
		statusForHTMLExportTarget(got, "pdf") != ArtifactHTMLExportBlocked {
		t.Fatalf("rendered targets should be blocked after browser validation failure, got %#v", got.Targets)
	}
	if reasonForHTMLExportTarget(got, "png") != "browser_validation_failed" {
		t.Fatalf("png reason = %q, want browser_validation_failed", reasonForHTMLExportTarget(got, "png"))
	}
	if !hasHTMLExportNextStepWithReason(got, "fix_browser_validation", "png", "browser_validation_failed") ||
		!hasHTMLExportNextStepWithReason(got, "fix_browser_validation", "pdf", "browser_validation_failed") {
		t.Fatalf("expected browser_validation_failed next step, got %#v", got.NextSteps)
	}
}

func statusForHTMLExportTarget(plan *ArtifactHTMLExportPlan, target string) string {
	for _, item := range plan.Targets {
		if item.Target == target {
			return item.Status
		}
	}
	return ""
}

func reasonForHTMLExportTarget(plan *ArtifactHTMLExportPlan, target string) string {
	for _, item := range plan.Targets {
		if item.Target == target {
			return item.Reason
		}
	}
	return ""
}

func kindForHTMLExportTarget(plan *ArtifactHTMLExportPlan, target string) string {
	for _, item := range plan.Targets {
		if item.Target == target {
			return item.Kind
		}
	}
	return ""
}

func hasHTMLExportNextStep(plan *ArtifactHTMLExportPlan, action string, target string) bool {
	for _, step := range plan.NextSteps {
		if step.Action != action {
			continue
		}
		for _, item := range step.Targets {
			if item == target {
				return true
			}
		}
	}
	return false
}

func hasHTMLExportNextStepWithReason(plan *ArtifactHTMLExportPlan, action string, target string, reason string) bool {
	for _, step := range plan.NextSteps {
		if step.Action != action || step.Reason != reason {
			continue
		}
		for _, item := range step.Targets {
			if item == target {
				return true
			}
		}
	}
	return false
}
