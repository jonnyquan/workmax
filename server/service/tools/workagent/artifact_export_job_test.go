package workagent

import (
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"
)

func TestArtifactExportJobRepository_ClaimNextAndMarkSucceeded(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	repo := NewArtifactExportJobRepository(db)
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	t.Cleanup(func() { SetMetricSink(prev) })

	job, err := repo.CreateFromHTMLJobPlan(ArtifactExportJobInput{
		UID:          42,
		ThreadID:     10,
		ArtifactID:   8,
		ThreadFileID: 80,
		Plan: ArtifactHTMLExportJobPlan{
			Target:          "pdf",
			Kind:            "render_static",
			Worker:          "browser_static_render",
			Status:          ArtifactHTMLExportJobWorkerPending,
			Reason:          "render_static_worker_unavailable",
			OutputExtension: ".pdf",
			Prerequisites:   []string{"asset_bundle_ready", "browser_validation_passed"},
		},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if job.Status != workagentModel.ArtifactExportJobStatusQueued {
		t.Fatalf("created status = %q, want queued", job.Status)
	}

	claimed, err := repo.ClaimNext("browser_static_render")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed == nil {
		t.Fatal("expected claimed job")
	}
	if claimed.Id != job.Id || claimed.Status != workagentModel.ArtifactExportJobStatusRunning {
		t.Fatalf("claimed = %+v, want job %d running", claimed, job.Id)
	}
	if claimed.Reason != "" {
		t.Fatalf("claimed reason = %q, want cleared reason", claimed.Reason)
	}

	next, err := repo.ClaimNext("browser_static_render")
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if next != nil {
		t.Fatalf("second claim = %+v, want nil", next)
	}

	succeeded, err := repo.MarkSucceeded(job.Id, 99, "outputs/page.pdf")
	if err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if succeeded.Status != workagentModel.ArtifactExportJobStatusSucceeded {
		t.Fatalf("succeeded status = %q", succeeded.Status)
	}
	if succeeded.OutputFileID != 99 || succeeded.OutputPath != "outputs/page.pdf" {
		t.Fatalf("succeeded output = %d/%q", succeeded.OutputFileID, succeeded.OutputPath)
	}
	if rec.FindByEvent("wa_artifact_export_job") == nil {
		t.Fatal("expected export job metrics")
	}
}

func TestArtifactExportJobRepository_MarkFailed(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)
	repo := NewArtifactExportJobRepository(db)
	job := workagentModel.ArtifactExportJob{
		UID:          42,
		ThreadID:     10,
		ArtifactID:   8,
		ThreadFileID: 80,
		Target:       "png",
		Kind:         "render_static",
		Worker:       "browser_static_render",
		Status:       workagentModel.ArtifactExportJobStatusRunning,
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatalf("seed job: %v", err)
	}

	failed, err := repo.MarkFailed(job.Id, "", "browser crashed")
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if failed.Status != workagentModel.ArtifactExportJobStatusFailed {
		t.Fatalf("failed status = %q", failed.Status)
	}
	if failed.Reason != "missing_failure_reason" || failed.ErrorMessage != "browser crashed" {
		t.Fatalf("failed reason/message = %q/%q", failed.Reason, failed.ErrorMessage)
	}
}
