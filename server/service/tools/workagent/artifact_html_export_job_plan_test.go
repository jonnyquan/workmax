package workagent

import "testing"

func TestBuildHTMLExportJobPlan_ClassifiesReadyZipJob(t *testing.T) {
	view := htmlExportJobPlanFixture([]string{"html", "zip"}, ArtifactHTMLValidationPass, nil)

	got := BuildHTMLExportJobPlan(view, "zip")
	if got == nil {
		t.Fatal("job plan is nil")
	}
	if got.Status != ArtifactHTMLExportJobReady || got.Worker != "zip_package" || got.OutputExtension != ".zip" {
		t.Fatalf("job = %#v, want ready zip_package .zip", got)
	}
}

func TestBuildHTMLExportJobPlan_StaticRenderWorkerPending(t *testing.T) {
	view := htmlExportJobPlanFixture([]string{"html", "png", "pdf"}, ArtifactHTMLValidationPass, nil)

	got := BuildHTMLExportJobPlan(view, "png")
	if got == nil {
		t.Fatal("job plan is nil")
	}
	if got.Status != ArtifactHTMLExportJobWorkerPending {
		t.Fatalf("status = %q, want worker_pending", got.Status)
	}
	if got.Worker != "browser_static_render" || got.Kind != "render_static" {
		t.Fatalf("job = %#v, want browser_static_render render_static", got)
	}
	if got.Reason != "render_static_worker_unavailable" {
		t.Fatalf("reason = %q, want render_static_worker_unavailable", got.Reason)
	}
	if !containsString(got.Prerequisites, "browser_validation_passed") {
		t.Fatalf("prerequisites = %#v, want browser_validation_passed", got.Prerequisites)
	}
}

func TestBuildHTMLExportJobPlan_MotionRenderWorkerPending(t *testing.T) {
	view := htmlExportJobPlanFixture([]string{"html", "mp4", "gif"}, ArtifactHTMLValidationPass, nil)

	got := BuildHTMLExportJobPlan(view, "gif")
	if got == nil {
		t.Fatal("job plan is nil")
	}
	if got.Status != ArtifactHTMLExportJobWorkerPending {
		t.Fatalf("status = %q, want worker_pending", got.Status)
	}
	if got.Worker != "browser_motion_render" || got.Kind != "render_motion" {
		t.Fatalf("job = %#v, want browser_motion_render render_motion", got)
	}
	if got.Reason != "render_motion_worker_unavailable" {
		t.Fatalf("reason = %q, want render_motion_worker_unavailable", got.Reason)
	}
	if !containsString(got.Prerequisites, "motion_timeline_ready") {
		t.Fatalf("prerequisites = %#v, want motion_timeline_ready", got.Prerequisites)
	}
}

func TestBuildHTMLExportJobPlan_BlockedByTargetReadiness(t *testing.T) {
	view := htmlExportJobPlanFixture([]string{"html", "png"}, ArtifactHTMLValidationWarn, []ArtifactHTMLValidationIssue{
		{Code: "missing_artboard_constraints", Severity: "warn", Message: "missing artboard"},
	})

	got := BuildHTMLExportJobPlan(view, "png")
	if got == nil {
		t.Fatal("job plan is nil")
	}
	if got.Status != ArtifactHTMLExportJobBlocked {
		t.Fatalf("status = %q, want blocked", got.Status)
	}
	if got.Reason != "browser_validation_required" {
		t.Fatalf("reason = %q, want browser_validation_required", got.Reason)
	}
}

func TestBuildHTMLExportJobPlan_UnsupportedTarget(t *testing.T) {
	view := htmlExportJobPlanFixture([]string{"html"}, ArtifactHTMLValidationPass, nil)

	got := BuildHTMLExportJobPlan(view, "tar")
	if got == nil {
		t.Fatal("job plan is nil")
	}
	if got.Status != ArtifactHTMLExportJobUnsupported || got.Reason != "unsupported_target" {
		t.Fatalf("job = %#v, want unsupported target", got)
	}
}

func htmlExportJobPlanFixture(targets []string, status string, issues []ArtifactHTMLValidationIssue) ArtifactView {
	view := ArtifactView{
		OutputType:    "html",
		PreviewType:   "html",
		ExportTargets: targets,
		HTMLValidation: &ArtifactHTMLValidationResult{
			Status:     status,
			Issues:     issues,
			IssueCount: len(issues),
		},
	}
	view.HTMLExportPlan = BuildHTMLExportPlan(view)
	return view
}
