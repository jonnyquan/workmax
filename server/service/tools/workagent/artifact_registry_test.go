package workagent

import (
	"testing"

	workagentModel "server/model/workagent"
)

func TestArtifactRegistryRepository_UpsertFromThreadFileCreatesVersionChain(t *testing.T) {
	repo, db := newFileRepo(t)
	registry := NewArtifactRegistryRepository(db)
	firstID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster-v1.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	secondID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster-v2.png"
		f.FileSource = workagentModel.FileSourceOutput
	})

	firstFile, err := repo.LoadByIDForOwner(firstID, 5)
	if err != nil {
		t.Fatalf("load first: %v", err)
	}
	firstArtifact, err := registry.UpsertFromThreadFile(firstFile)
	if err != nil {
		t.Fatalf("upsert first: %v", err)
	}
	secondFile, err := repo.LoadByIDForOwner(secondID, 5)
	if err != nil {
		t.Fatalf("load second: %v", err)
	}
	secondArtifact, err := registry.UpsertFromThreadFile(secondFile)
	if err != nil {
		t.Fatalf("upsert second: %v", err)
	}

	if firstArtifact.Version != 1 {
		t.Fatalf("first version = %d, want 1", firstArtifact.Version)
	}
	if secondArtifact.Version != 2 {
		t.Fatalf("second version = %d, want 2", secondArtifact.Version)
	}
	if secondArtifact.ParentArtifactID != firstArtifact.Id {
		t.Fatalf("second parent = %d, want %d", secondArtifact.ParentArtifactID, firstArtifact.Id)
	}
	if secondArtifact.Status != workagentModel.ArtifactStatusDraft {
		t.Fatalf("second status = %q, want draft", secondArtifact.Status)
	}
}

func TestArtifactRegistryRepository_UpsertFromThreadFileEmitsCreatedMetric(t *testing.T) {
	repo, db := newFileRepo(t)
	registry := NewArtifactRegistryRepository(db)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "landing.html"
		f.DisplayName = "landing.html"
		f.FilePath = "outputs/landing.html"
		f.FileSource = workagentModel.FileSourceOutput
		f.MimeType = "text/html"
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	artifact, err := registry.UpsertFromThreadFile(file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	ev := rec.FindByEvent("wa_artifact_created")
	if ev == nil {
		t.Fatal("expected wa_artifact_created metric")
	}
	if ev.Fields["artifact_id"] != artifact.Id || ev.Fields["thread_file_id"] != file.Id {
		t.Fatalf("metric ids = %#v, want artifact/file ids", ev.Fields)
	}
	if ev.Fields["artifact_type"] != "html" || ev.Fields["output_type"] != "html" || ev.Fields["preview_type"] != "html" {
		t.Fatalf("metric artifact fields = %#v", ev.Fields)
	}
	if ev.Fields["export_target_count"] != 6 {
		t.Fatalf("export_target_count = %#v, want 6", ev.Fields["export_target_count"])
	}
}

func TestListArtifactViewsByThread_PrefersRegistryLifecycle(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	artifact, err := NewArtifactRegistryRepository(db).UpsertFromThreadFile(file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if err := db.Model(artifact).Updates(map[string]interface{}{
		"status":         workagentModel.ArtifactStatusApproved,
		"review_state":   workagentModel.ArtifactReviewApproved,
		"review_source":  "critique_gate",
		"review_summary": "- Fix hierarchy",
	}).Error; err != nil {
		t.Fatalf("update artifact status: %v", err)
	}

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ID != "artifact-"+uintToStringWA(artifact.Id) {
		t.Fatalf("artifact id = %q, want registry id", got[0].ID)
	}
	if got[0].Status != workagentModel.ArtifactStatusApproved {
		t.Fatalf("status = %q, want approved", got[0].Status)
	}
	if got[0].ReviewState != workagentModel.ArtifactReviewApproved {
		t.Fatalf("review state = %q, want approved", got[0].ReviewState)
	}
	if got[0].ReviewSource != "critique_gate" {
		t.Fatalf("review source = %q, want critique_gate", got[0].ReviewSource)
	}
	if got[0].ReviewSummary != "- Fix hierarchy" {
		t.Fatalf("review summary = %q, want fix hint", got[0].ReviewSummary)
	}
}

func TestListArtifactViewsByThread_AttachesLockedDesignSystem(t *testing.T) {
	repo, db := newFileRepo(t)
	seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	if err := db.Create(&workagentModel.ChatMessage{
		UID:      5,
		UUID:     "visual-direction-message",
		ThreadID: 50,
		Metadata: `{"kind":"visual_direction_selected","direction_id":"modern_minimal","design_system_basename":"modern-minimal","design_system_title":"Design System Modern Minimal","design_system_derived_from":"modern_minimal"}`,
	}).Error; err != nil {
		t.Fatalf("seed metadata message: %v", err)
	}

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].DesignSystemBasename != "modern-minimal" {
		t.Fatalf("designSystemBasename = %q", got[0].DesignSystemBasename)
	}
	if got[0].DesignSystemTitle != "Design System Modern Minimal" {
		t.Fatalf("designSystemTitle = %q", got[0].DesignSystemTitle)
	}
	if got[0].DesignSystemDerivedFrom != "modern_minimal" {
		t.Fatalf("designSystemDerivedFrom = %q", got[0].DesignSystemDerivedFrom)
	}
}

func TestArtifactRegistryRepository_FreezesArtifactDesignSystemSnapshot(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.DisplayName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	if err := db.Create(&workagentModel.ChatMessage{
		UID:      5,
		UUID:     "visual-direction-old",
		ThreadID: 50,
		Metadata: `{"kind":"visual_direction_selected","design_system_basename":"modern-minimal","design_system_title":"Modern Minimal","design_system_derived_from":"modern_minimal"}`,
	}).Error; err != nil {
		t.Fatalf("seed old metadata message: %v", err)
	}
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	if _, err := registry.UpsertFromThreadFile(file); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	if err := db.Create(&workagentModel.ChatMessage{
		UID:      5,
		UUID:     "visual-direction-new",
		ThreadID: 50,
		Metadata: `{"kind":"visual_direction_selected","design_system_basename":"bold-editorial","design_system_title":"Bold Editorial","design_system_derived_from":"bold_editorial"}`,
	}).Error; err != nil {
		t.Fatalf("seed new metadata message: %v", err)
	}

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if got[0].DesignSystemBasename != "modern-minimal" {
		t.Fatalf("designSystemBasename = %q, want frozen modern-minimal", got[0].DesignSystemBasename)
	}
	if got[0].DesignSystemTitle != "Modern Minimal" {
		t.Fatalf("designSystemTitle = %q, want frozen title", got[0].DesignSystemTitle)
	}
}

func TestArtifactRegistryRepository_UpdateLifecycleForThreadFiles(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	if _, err := registry.UpsertFromThreadFile(file); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	t.Cleanup(func() { SetMetricSink(prev) })

	count, err := registry.UpdateLifecycleForThreadFilesWithReview(
		5,
		50,
		[]uint{fileID},
		workagentModel.ArtifactStatusChangesRequested,
		workagentModel.ArtifactReviewChangesRequested,
		"checklist_gate",
		"- Missing source image",
	)
	if err != nil {
		t.Fatalf("UpdateLifecycleForThreadFiles: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if got[0].Status != workagentModel.ArtifactStatusChangesRequested {
		t.Fatalf("status = %q, want changes-requested", got[0].Status)
	}
	if got[0].ReviewState != workagentModel.ArtifactReviewChangesRequested {
		t.Fatalf("review = %q, want changes-requested", got[0].ReviewState)
	}
	if got[0].ReviewSource != "checklist_gate" {
		t.Fatalf("review source = %q, want checklist_gate", got[0].ReviewSource)
	}
	if got[0].ReviewSummary != "- Missing source image" {
		t.Fatalf("review summary = %q, want checklist hint", got[0].ReviewSummary)
	}
	ev := rec.FindByEvent("wa_artifact_review_decision")
	if ev == nil {
		t.Fatal("expected wa_artifact_review_decision metric")
	}
	if ev.Fields["source"] != "checklist_gate" || ev.Fields["review_state"] != workagentModel.ArtifactReviewChangesRequested {
		t.Fatalf("metric fields = %#v", ev.Fields)
	}
	if ev.Fields["artifact_count"] != 1 {
		t.Fatalf("artifact_count = %#v, want 1", ev.Fields["artifact_count"])
	}
}

func TestArtifactRegistryRepository_UpdateLifecycleForThreadFilesWithReviewDetails(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	if _, err := registry.UpsertFromThreadFile(file); err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	details := `{"source":"critique_gate","decision":"block","overallScore":62.5,"verdict":"Weak hierarchy","topFixes":["Increase CTA contrast"],"dimensions":{"craft":{"score":5,"notes":"Flat","fixes":["Add depth"]}}}`
	count, err := registry.UpdateLifecycleForThreadFilesWithReviewDetails(
		5,
		50,
		[]uint{fileID},
		workagentModel.ArtifactStatusChangesRequested,
		workagentModel.ArtifactReviewChangesRequested,
		"critique_gate",
		"- Increase CTA contrast",
		details,
	)
	if err != nil {
		t.Fatalf("UpdateLifecycleForThreadFilesWithReviewDetails: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if got[0].ReviewDetails == nil {
		t.Fatal("review details missing")
	}
	if got[0].ReviewDetails.Decision != "block" || got[0].ReviewDetails.OverallScore != 62.5 {
		t.Fatalf("review details = %#v", got[0].ReviewDetails)
	}
	if got[0].ReviewDetails.TopFixes[0] != "Increase CTA contrast" {
		t.Fatalf("top fixes = %#v", got[0].ReviewDetails.TopFixes)
	}
	if got[0].ReviewDetails.Dimensions["craft"].Score != 5 {
		t.Fatalf("dimensions = %#v", got[0].ReviewDetails.Dimensions)
	}
}

func TestArtifactRegistryRepository_UpdateReviewDetailsEmitsRedoDeltaMetric(t *testing.T) {
	repo, db := newFileRepo(t)
	parentFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster-v1.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	childFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster-v2.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	parentFile, err := repo.LoadByIDForOwner(parentFileID, 5)
	if err != nil {
		t.Fatalf("load parent file: %v", err)
	}
	childFile, err := repo.LoadByIDForOwner(childFileID, 5)
	if err != nil {
		t.Fatalf("load child file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	parent, err := registry.UpsertFromThreadFile(parentFile)
	if err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	child, err := registry.UpsertFromThreadFile(childFile)
	if err != nil {
		t.Fatalf("upsert child: %v", err)
	}
	parentDetails := `{"source":"critique_gate","decision":"block","overallScore":62,"dimensions":{"hierarchy":{"score":4},"craft":{"score":7},"brand":{"score":8}}}`
	if _, err := registry.UpdateLifecycleForThreadFilesWithReviewDetails(
		5,
		50,
		[]uint{parentFileID},
		workagentModel.ArtifactStatusChangesRequested,
		workagentModel.ArtifactReviewChangesRequested,
		"critique_gate",
		"parent critique",
		parentDetails,
	); err != nil {
		t.Fatalf("update parent review details: %v", err)
	}
	if _, err := registry.LinkThreadFilesToParent(5, 50, []uint{childFileID}, parent.Id); err != nil {
		t.Fatalf("link child to parent: %v", err)
	}

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	t.Cleanup(func() { SetMetricSink(prev) })
	childDetails := `{"source":"critique_gate","decision":"warn","overallScore":78,"dimensions":{"hierarchy":{"score":8},"craft":{"score":6},"brand":{"score":8}}}`
	if _, err := registry.UpdateLifecycleForThreadFilesWithReviewDetails(
		5,
		50,
		[]uint{childFileID},
		workagentModel.ArtifactStatusNeedsReview,
		workagentModel.ArtifactReviewNeedsReview,
		"critique_gate",
		"child critique",
		childDetails,
	); err != nil {
		t.Fatalf("update child review details: %v", err)
	}

	ev := rec.FindByEvent("wa_artifact_redo_review_delta")
	if ev == nil {
		t.Fatal("expected wa_artifact_redo_review_delta metric")
	}
	if ev.Fields["artifact_id"] != child.Id || ev.Fields["parent_artifact_id"] != parent.Id {
		t.Fatalf("metric artifact ids = %#v", ev.Fields)
	}
	if ev.Fields["overall_score_before"] != float64(62) || ev.Fields["overall_score_after"] != float64(78) || ev.Fields["overall_score_delta"] != float64(16) {
		t.Fatalf("metric score delta fields = %#v", ev.Fields)
	}
	if ev.Fields["improved_dimensions"] != 1 || ev.Fields["regressed_dimensions"] != 1 {
		t.Fatalf("metric dimension delta fields = %#v", ev.Fields)
	}
}

func TestArtifactRegistryRepository_LinkThreadFilesToParent(t *testing.T) {
	repo, db := newFileRepo(t)
	parentFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	childFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster-redone.png"
		f.FilePath = "outputs/poster-redone.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	parentFile, err := repo.LoadByIDForOwner(parentFileID, 5)
	if err != nil {
		t.Fatalf("load parent file: %v", err)
	}
	childFile, err := repo.LoadByIDForOwner(childFileID, 5)
	if err != nil {
		t.Fatalf("load child file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	parent, err := registry.UpsertFromThreadFile(parentFile)
	if err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	if _, err := registry.UpsertFromThreadFile(childFile); err != nil {
		t.Fatalf("upsert child: %v", err)
	}

	count, err := registry.LinkThreadFilesToParent(5, 50, []uint{childFileID}, parent.Id)
	if err != nil {
		t.Fatalf("LinkThreadFilesToParent: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	var child ArtifactView
	for _, row := range got {
		if row.FileID == childFileID {
			child = row
			break
		}
	}
	if child.ParentArtifactID != "artifact-"+uintToStringWA(parent.Id) {
		t.Fatalf("child parent = %q, want artifact-%d", child.ParentArtifactID, parent.Id)
	}
	if child.ArtifactRelation != workagentModel.ArtifactRelationRevision {
		t.Fatalf("child relation = %q, want revision", child.ArtifactRelation)
	}
}

func TestArtifactRegistryRepository_LinkThreadFilesToParentWithComparisonRelation(t *testing.T) {
	repo, db := newFileRepo(t)
	parentFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster-v2.png"
		f.FilePath = "outputs/poster-v2.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	reportFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster-visual-diff.html"
		f.FilePath = "outputs/poster-visual-diff.html"
		f.FileSource = workagentModel.FileSourceOutput
	})
	parentFile, err := repo.LoadByIDForOwner(parentFileID, 5)
	if err != nil {
		t.Fatalf("load parent file: %v", err)
	}
	reportFile, err := repo.LoadByIDForOwner(reportFileID, 5)
	if err != nil {
		t.Fatalf("load report file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	parent, err := registry.UpsertFromThreadFile(parentFile)
	if err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	if _, err := registry.UpsertFromThreadFile(reportFile); err != nil {
		t.Fatalf("upsert report: %v", err)
	}

	count, err := registry.LinkThreadFilesToParentWithRelation(
		5,
		50,
		[]uint{reportFileID},
		parent.Id,
		workagentModel.ArtifactRelationComparisonReport,
	)
	if err != nil {
		t.Fatalf("LinkThreadFilesToParentWithRelation: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	var report ArtifactView
	for _, row := range got {
		if row.FileID == reportFileID {
			report = row
			break
		}
	}
	if report.ParentArtifactID != "artifact-"+uintToStringWA(parent.Id) {
		t.Fatalf("report parent = %q, want artifact-%d", report.ParentArtifactID, parent.Id)
	}
	if report.ArtifactRelation != workagentModel.ArtifactRelationComparisonReport {
		t.Fatalf("report relation = %q, want comparison_report", report.ArtifactRelation)
	}
}

func TestArtifactRegistryRepository_LatestArtifactIDForThreadFiles(t *testing.T) {
	repo, db := newFileRepo(t)
	firstFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster-a.png"
		f.FilePath = "outputs/poster-a.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	secondFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster-b.png"
		f.FilePath = "outputs/poster-b.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	firstFile, err := repo.LoadByIDForOwner(firstFileID, 5)
	if err != nil {
		t.Fatalf("load first file: %v", err)
	}
	secondFile, err := repo.LoadByIDForOwner(secondFileID, 5)
	if err != nil {
		t.Fatalf("load second file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	firstArtifact, err := registry.UpsertFromThreadFile(firstFile)
	if err != nil {
		t.Fatalf("upsert first artifact: %v", err)
	}
	secondArtifact, err := registry.UpsertFromThreadFile(secondFile)
	if err != nil {
		t.Fatalf("upsert second artifact: %v", err)
	}

	got, err := registry.LatestArtifactIDForThreadFiles(5, 50, []uint{firstFileID, secondFileID})
	if err != nil {
		t.Fatalf("LatestArtifactIDForThreadFiles: %v", err)
	}
	if got != secondArtifact.Id {
		t.Fatalf("latest artifact id = %d, want %d (first=%d)", got, secondArtifact.Id, firstArtifact.Id)
	}
}

func TestArtifactRegistryRepository_UpdateComparison(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	artifact, err := registry.UpsertFromThreadFile(file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	updated, err := registry.UpdateComparison(
		5,
		50,
		artifact.Id,
		"manual_compare",
		"Latest fixes hierarchy but loses brand contrast.",
		"revise",
	)
	if err != nil {
		t.Fatalf("UpdateComparison: %v", err)
	}
	if updated.ComparisonSource != "manual_compare" {
		t.Fatalf("comparison source = %q, want manual_compare", updated.ComparisonSource)
	}
	if updated.ComparisonDecision != "revise" {
		t.Fatalf("comparison decision = %q, want revise", updated.ComparisonDecision)
	}

	got, err := ListArtifactViewsByThread(repo, 5, 50)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	if got[0].ComparisonSummary != "Latest fixes hierarchy but loses brand contrast." {
		t.Fatalf("comparison summary = %q", got[0].ComparisonSummary)
	}
	if got[0].ComparisonDecision != "revise" {
		t.Fatalf("comparison decision = %q, want revise", got[0].ComparisonDecision)
	}
}

func TestArtifactRegistryRepository_ApplyComparisonDecisionRollbackApprovesParent(t *testing.T) {
	repo, db := newFileRepo(t)
	parentFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster-v1.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	latestFileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "poster.png"
		f.FilePath = "outputs/poster-v2.png"
		f.FileSource = workagentModel.FileSourceOutput
	})
	parentFile, err := repo.LoadByIDForOwner(parentFileID, 5)
	if err != nil {
		t.Fatalf("load parent: %v", err)
	}
	latestFile, err := repo.LoadByIDForOwner(latestFileID, 5)
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	parent, err := registry.UpsertFromThreadFile(parentFile)
	if err != nil {
		t.Fatalf("upsert parent: %v", err)
	}
	latest, err := registry.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	if latest.ParentArtifactID != parent.Id {
		t.Fatalf("latest parent = %d, want %d", latest.ParentArtifactID, parent.Id)
	}
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	updated, err := registry.ApplyComparisonDecision(5, 50, latest.Id, "rollback")
	if err != nil {
		t.Fatalf("ApplyComparisonDecision: %v", err)
	}
	if updated.ComparisonDecision != "rollback" {
		t.Fatalf("decision = %q, want rollback", updated.ComparisonDecision)
	}
	if updated.Status != workagentModel.ArtifactStatusChangesRequested {
		t.Fatalf("latest status = %q, want changes-requested", updated.Status)
	}
	var reloadedParent workagentModel.ArtifactRegistry
	if err := db.First(&reloadedParent, parent.Id).Error; err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if reloadedParent.Status != workagentModel.ArtifactStatusFinal {
		t.Fatalf("parent status = %q, want final", reloadedParent.Status)
	}
	ev := rec.FindByEvent("wa_artifact_comparison_decision")
	if ev == nil {
		t.Fatal("expected wa_artifact_comparison_decision metric")
	}
	if ev.Fields["decision"] != "rollback" || ev.Fields["artifact_id"] != latest.Id {
		t.Fatalf("metric decision fields = %#v", ev.Fields)
	}
	if ev.Fields["parent_artifact_id"] != parent.Id || ev.Fields["adopted_artifact_id"] != parent.Id {
		t.Fatalf("metric adopted parent fields = %#v, want parent %d", ev.Fields, parent.Id)
	}
	if ev.Fields["status"] != workagentModel.ArtifactStatusChangesRequested || ev.Fields["review_state"] != workagentModel.ArtifactReviewChangesRequested {
		t.Fatalf("metric lifecycle fields = %#v", ev.Fields)
	}
}

func TestArtifactRegistryRepository_ApplyComparisonDecisionEmitsDecisionMetric(t *testing.T) {
	for _, tt := range []struct {
		name              string
		decision          string
		wantStatus        string
		wantReviewState   string
		wantAdoptedLatest bool
	}{
		{
			name:              "keep adopts latest",
			decision:          "keep",
			wantStatus:        workagentModel.ArtifactStatusFinal,
			wantReviewState:   workagentModel.ArtifactReviewApproved,
			wantAdoptedLatest: true,
		},
		{
			name:            "revise requests another redo",
			decision:        "revise",
			wantStatus:      workagentModel.ArtifactStatusChangesRequested,
			wantReviewState: workagentModel.ArtifactReviewChangesRequested,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo, db := newFileRepo(t)
			fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
				f.UID = 5
				f.ThreadID = 50
				f.FileName = "poster.png"
				f.FilePath = "outputs/poster.png"
				f.FileSource = workagentModel.FileSourceOutput
			})
			file, err := repo.LoadByIDForOwner(fileID, 5)
			if err != nil {
				t.Fatalf("load file: %v", err)
			}
			registry := NewArtifactRegistryRepository(db)
			artifact, err := registry.UpsertFromThreadFile(file)
			if err != nil {
				t.Fatalf("upsert artifact: %v", err)
			}
			if _, err := registry.UpdateComparison(5, 50, artifact.Id, "agent_compare", "latest comparison summary", "revise"); err != nil {
				t.Fatalf("UpdateComparison: %v", err)
			}

			rec := &RecordingSink{}
			prev := SetMetricSink(rec)
			defer SetMetricSink(prev)

			updated, err := registry.ApplyComparisonDecision(5, 50, artifact.Id, tt.decision)
			if err != nil {
				t.Fatalf("ApplyComparisonDecision: %v", err)
			}
			if updated.Status != tt.wantStatus || updated.ReviewState != tt.wantReviewState {
				t.Fatalf("updated lifecycle = %q/%q, want %q/%q", updated.Status, updated.ReviewState, tt.wantStatus, tt.wantReviewState)
			}

			ev := rec.FindByEvent("wa_artifact_comparison_decision")
			if ev == nil {
				t.Fatal("expected wa_artifact_comparison_decision metric")
			}
			if ev.Fields["decision"] != tt.decision || ev.Fields["source"] != "agent_compare" {
				t.Fatalf("metric decision/source = %#v", ev.Fields)
			}
			wantAdopted := uint(0)
			if tt.wantAdoptedLatest {
				wantAdopted = artifact.Id
			}
			if ev.Fields["adopted_artifact_id"] != wantAdopted {
				t.Fatalf("adopted_artifact_id = %#v, want %d", ev.Fields["adopted_artifact_id"], wantAdopted)
			}
		})
	}
}

func TestArtifactRegistryRepository_LoadForOwnerWithThreadFileScopesOwnerAndThread(t *testing.T) {
	repo, db := newFileRepo(t)
	fileID := seedFile(t, db, func(f *workagentModel.ThreadFile) {
		f.UID = 5
		f.ThreadID = 50
		f.FileName = "landing.html"
		f.FilePath = "outputs/landing.html"
		f.FileSource = workagentModel.FileSourceOutput
	})
	file, err := repo.LoadByIDForOwner(fileID, 5)
	if err != nil {
		t.Fatalf("load file: %v", err)
	}
	registry := NewArtifactRegistryRepository(db)
	artifact, err := registry.UpsertFromThreadFile(file)
	if err != nil {
		t.Fatalf("upsert artifact: %v", err)
	}

	gotArtifact, gotFile, err := registry.LoadForOwnerWithThreadFile(5, 50, artifact.Id)
	if err != nil {
		t.Fatalf("LoadForOwnerWithThreadFile: %v", err)
	}
	if gotArtifact.Id != artifact.Id || gotFile.Id != file.Id {
		t.Fatalf("loaded artifact/file = %d/%d, want %d/%d", gotArtifact.Id, gotFile.Id, artifact.Id, file.Id)
	}
	if _, _, err := registry.LoadForOwnerWithThreadFile(99, 50, artifact.Id); !isRecordNotFound(err) {
		t.Fatalf("cross-owner err = %v, want record not found", err)
	}
	if _, _, err := registry.LoadForOwnerWithThreadFile(5, 99, artifact.Id); !isRecordNotFound(err) {
		t.Fatalf("cross-thread err = %v, want record not found", err)
	}
}
