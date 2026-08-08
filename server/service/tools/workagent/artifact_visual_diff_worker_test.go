package workagent

import (
	"context"
	"encoding/json"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"gorm.io/gorm"
)

type visualDiffHTMLStaticRenderer struct {
	outputs           map[string]HTMLStaticRenderOutput
	outputsByFileName map[string]HTMLStaticRenderOutput
	inputs            []HTMLStaticRenderInput
}

func (r *visualDiffHTMLStaticRenderer) RenderStaticHTML(_ context.Context, input HTMLStaticRenderInput) (HTMLStaticRenderOutput, error) {
	r.inputs = append(r.inputs, input)
	if r.outputs != nil {
		if output, ok := r.outputs[input.OutputFileName]; ok {
			return output, nil
		}
	}
	if r.outputsByFileName != nil {
		if output, ok := r.outputsByFileName[input.SourceFile.FileName]; ok {
			return output, nil
		}
	}
	return HTMLStaticRenderOutput{}, nil
}

func TestArtifactVisualDiffImageReportService_GeneratesReportArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffImageFile(t, db, workspaceRoot, thread, "poster-v1.png", func(x, y int) color.Color {
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	})
	latestFile := seedVisualDiffImageFile(t, db, workspaceRoot, thread, "poster-v2.png", func(x, y int) color.Color {
		if x < 2 {
			return color.RGBA{R: 20, G: 20, B: 20, A: 255}
		}
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	})
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.ReportFile == nil || result.ReportFile.Id == 0 {
		t.Fatalf("missing report file: %#v", result.ReportFile)
	}
	if result.ReportArtifact == nil || result.ReportArtifact.Id == 0 {
		t.Fatalf("missing report artifact: %#v", result.ReportArtifact)
	}
	if result.ReportArtifact.ParentArtifactID != latestArtifact.Id || result.ReportArtifact.ArtifactRelation != workagentModel.ArtifactRelationComparisonReport {
		t.Fatalf("report relation = parent %d relation %q, want parent %d comparison_report", result.ReportArtifact.ParentArtifactID, result.ReportArtifact.ArtifactRelation, latestArtifact.Id)
	}
	if result.ReportArtifact.ComparisonSource != "auto_visual_diff" || result.ReportArtifact.ComparisonDecision != "revise" {
		t.Fatalf("comparison metadata = %q/%q", result.ReportArtifact.ComparisonSource, result.ReportArtifact.ComparisonDecision)
	}
	if result.Analysis.ChangedPixelRatio < 0.49 || result.Analysis.ChangedPixelRatio > 0.51 {
		t.Fatalf("changed ratio = %f, want about 0.5", result.Analysis.ChangedPixelRatio)
	}

	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{
		"Visual Diff Report",
		"Automated image analysis",
		"poster-v1.png",
		"poster-v2.png",
		"Changed pixels</dt><dd>50.0%",
	} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("report HTML missing %q in:\n%s", want, reportHTML)
		}
	}

	var description map[string]any
	if err := json.Unmarshal([]byte(result.ReportFile.Description), &description); err != nil {
		t.Fatalf("description JSON: %v", err)
	}
	if description["kind"] != "workagent_visual_diff_report" || description["source"] != "auto_visual_diff" {
		t.Fatalf("description = %#v", description)
	}

	views, err := ListArtifactViewsByThread(NewFileRepository(db), 42, thread.Id)
	if err != nil {
		t.Fatalf("ListArtifactViewsByThread: %v", err)
	}
	var reportView *ArtifactView
	for i := range views {
		if views[i].FileID == result.ReportFile.Id {
			reportView = &views[i]
			break
		}
	}
	if reportView == nil || reportView.ArtifactRelation != workagentModel.ArtifactRelationComparisonReport || reportView.ParentArtifactID != "artifact-"+strconv.FormatUint(uint64(latestArtifact.Id), 10) {
		t.Fatalf("report view = %#v", reportView)
	}
}

func TestArtifactVisualDiffImageReportService_GeneratesHTMLScreenshotReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffHTMLFile(t, db, workspaceRoot, thread, "landing-v1.html", "<!doctype html><main>Version one</main>")
	latestFile := seedVisualDiffHTMLFile(t, db, workspaceRoot, thread, "landing-v2.html", "<!doctype html><main>Version two</main>")
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	renderer := &visualDiffHTMLStaticRenderer{outputsByFileName: map[string]HTMLStaticRenderOutput{
		"landing-v1.html": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 250, G: 250, B: 250, A: 255} }),
			MimeType: "image/png",
		},
		"landing-v2.html": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if x < 1 {
					return color.RGBA{R: 20, G: 20, B: 20, A: 255}
				}
				return color.RGBA{R: 250, G: 250, B: 250, A: 255}
			}),
			MimeType: "image/png",
		},
	}}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:             db,
		StaticRenderer: renderer,
		WorkspaceRoot:  workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate HTML diff: %v", err)
	}
	if len(renderer.inputs) != 2 {
		t.Fatalf("renderer inputs = %d, want screenshots for previous and latest HTML", len(renderer.inputs))
	}
	for _, input := range renderer.inputs {
		if input.Target != "png" || input.OutputExtension != ".png" || !strings.Contains(input.OutputFileName, "-visual-diff-screenshot.png") {
			t.Fatalf("renderer input = %+v, want PNG screenshot contract", input)
		}
	}
	if result.ReportArtifact == nil || result.ReportArtifact.ComparisonSource != "auto_visual_diff" {
		t.Fatalf("missing report artifact metadata: %#v", result.ReportArtifact)
	}
	if result.Analysis.ChangedPixelRatio < 0.24 || result.Analysis.ChangedPixelRatio > 0.26 {
		t.Fatalf("changed ratio = %f, want about 0.25 from rendered screenshots", result.Analysis.ChangedPixelRatio)
	}
	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{"landing-v1.html", "landing-v2.html", "Automated image analysis"} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("HTML visual diff report missing %q in:\n%s", want, reportHTML)
		}
	}
}

func TestArtifactVisualDiffImageReportService_GeneratesPDFScreenshotReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffPDFFile(t, db, workspaceRoot, thread, "deck-v1.pdf")
	latestFile := seedVisualDiffPDFFile(t, db, workspaceRoot, thread, "deck-v2.pdf")
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	renderer := &visualDiffHTMLStaticRenderer{outputsByFileName: map[string]HTMLStaticRenderOutput{
		"deck-v1.pdf": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 250, G: 250, B: 250, A: 255} }),
			MimeType: "image/png",
		},
		"deck-v2.pdf": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if y < 2 {
					return color.RGBA{R: 20, G: 20, B: 20, A: 255}
				}
				return color.RGBA{R: 250, G: 250, B: 250, A: 255}
			}),
			MimeType: "image/png",
		},
	}}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:             db,
		StaticRenderer: renderer,
		WorkspaceRoot:  workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate PDF diff: %v", err)
	}
	if len(renderer.inputs) != 2 {
		t.Fatalf("renderer inputs = %d, want screenshots for previous and latest PDF", len(renderer.inputs))
	}
	for _, input := range renderer.inputs {
		if input.Target != "png" || input.OutputExtension != ".png" || input.SourceHTML != "" || !strings.Contains(input.OutputFileName, "-visual-diff-page-1.png") {
			t.Fatalf("renderer input = %+v, want PDF PNG screenshot contract", input)
		}
	}
	if result.Analysis.ChangedPixelRatio < 0.49 || result.Analysis.ChangedPixelRatio > 0.51 {
		t.Fatalf("changed ratio = %f, want about 0.5 from rendered PDF screenshots", result.Analysis.ChangedPixelRatio)
	}
	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{"deck-v1.pdf", "deck-v2.pdf", "Automated image analysis"} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("PDF visual diff report missing %q in:\n%s", want, reportHTML)
		}
	}
}

func TestArtifactVisualDiffImageReportService_GeneratesMultiPagePDFReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffPDFFile(t, db, workspaceRoot, thread, "deck-v1.pdf")
	latestFile := seedVisualDiffPDFFile(t, db, workspaceRoot, thread, "deck-v2.pdf")
	for _, file := range []*workagentModel.ThreadFile{previousFile, latestFile} {
		abs := ResolveInsideWorkspace(workspaceRoot, file.FilePath)
		if err := os.WriteFile(abs, []byte("%PDF-1.7\n/Type /Pages\n/Type /Page\n/Type /Page\n"), 0o644); err != nil {
			t.Fatalf("write multipage pdf fixture: %v", err)
		}
	}
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	renderer := &visualDiffHTMLStaticRenderer{outputs: map[string]HTMLStaticRenderOutput{
		"deck-v1-visual-diff-page-1.png": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 250, G: 250, B: 250, A: 255} }),
			MimeType: "image/png",
		},
		"deck-v1-visual-diff-page-2.png": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 240, G: 240, B: 240, A: 255} }),
			MimeType: "image/png",
		},
		"deck-v2-visual-diff-page-1.png": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if x < 2 {
					return color.RGBA{R: 20, G: 20, B: 20, A: 255}
				}
				return color.RGBA{R: 250, G: 250, B: 250, A: 255}
			}),
			MimeType: "image/png",
		},
		"deck-v2-visual-diff-page-2.png": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if y < 1 {
					return color.RGBA{R: 30, G: 30, B: 30, A: 255}
				}
				return color.RGBA{R: 240, G: 240, B: 240, A: 255}
			}),
			MimeType: "image/png",
		},
	}}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:             db,
		StaticRenderer: renderer,
		WorkspaceRoot:  workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate multi-page PDF diff: %v", err)
	}
	if len(renderer.inputs) != 4 {
		t.Fatalf("renderer inputs = %d, want previous/latest screenshots for two PDF pages", len(renderer.inputs))
	}
	if len(result.Comparisons) != 2 {
		t.Fatalf("comparisons = %d, want 2", len(result.Comparisons))
	}
	if !strings.Contains(renderer.inputs[1].SourceURL, "#page=2") || !strings.Contains(renderer.inputs[3].SourceURL, "#page=2") {
		t.Fatalf("renderer page URLs = %#v", renderer.inputs)
	}
	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{"Automated multi-screenshot analysis", "page 1", "page 2", "2 screenshot comparisons were analyzed"} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("multi-page report missing %q in:\n%s", want, reportHTML)
		}
	}
}

func TestArtifactVisualDiffImageReportService_GeneratesVideoScreenshotReport(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffVideoFile(t, db, workspaceRoot, thread, "story-v1.mp4")
	latestFile := seedVisualDiffVideoFile(t, db, workspaceRoot, thread, "story-v2.mp4")
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	renderer := &visualDiffHTMLStaticRenderer{outputsByFileName: map[string]HTMLStaticRenderOutput{
		"story-v1.mp4": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 250, G: 250, B: 250, A: 255} }),
			MimeType: "image/png",
		},
		"story-v2.mp4": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if x >= 2 {
					return color.RGBA{R: 20, G: 20, B: 20, A: 255}
				}
				return color.RGBA{R: 250, G: 250, B: 250, A: 255}
			}),
			MimeType: "image/png",
		},
	}}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:             db,
		StaticRenderer: renderer,
		WorkspaceRoot:  workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate video diff: %v", err)
	}
	if len(renderer.inputs) != 2 {
		t.Fatalf("renderer inputs = %d, want screenshots for previous and latest video", len(renderer.inputs))
	}
	for _, input := range renderer.inputs {
		if input.Target != "png" || input.OutputExtension != ".png" || input.SourceHTML != "" || !strings.Contains(input.OutputFileName, "-visual-diff-first-frame.png") {
			t.Fatalf("renderer input = %+v, want video PNG screenshot contract", input)
		}
	}
	if result.Analysis.ChangedPixelRatio < 0.49 || result.Analysis.ChangedPixelRatio > 0.51 {
		t.Fatalf("changed ratio = %f, want about 0.5 from rendered video screenshots", result.Analysis.ChangedPixelRatio)
	}
	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{"story-v1.mp4", "story-v2.mp4", "Automated image analysis"} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("Video visual diff report missing %q in:\n%s", want, reportHTML)
		}
	}
}

func TestArtifactVisualDiffImageReportService_GeneratesConfiguredVideoKeyframeReport(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_VISUAL_DIFF_VIDEO_TIMES", "0,1")
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffVideoFile(t, db, workspaceRoot, thread, "story-v1.mp4")
	latestFile := seedVisualDiffVideoFile(t, db, workspaceRoot, thread, "story-v2.mp4")
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}
	renderer := &visualDiffHTMLStaticRenderer{outputs: map[string]HTMLStaticRenderOutput{
		"story-v1-visual-diff-t-0s.png": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 250, G: 250, B: 250, A: 255} }),
			MimeType: "image/png",
		},
		"story-v1-visual-diff-t-1s.png": {
			Content:  testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color { return color.RGBA{R: 230, G: 230, B: 230, A: 255} }),
			MimeType: "image/png",
		},
		"story-v2-visual-diff-t-0s.png": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if x >= 2 {
					return color.RGBA{R: 20, G: 20, B: 20, A: 255}
				}
				return color.RGBA{R: 250, G: 250, B: 250, A: 255}
			}),
			MimeType: "image/png",
		},
		"story-v2-visual-diff-t-1s.png": {
			Content: testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
				if y >= 3 {
					return color.RGBA{R: 40, G: 40, B: 40, A: 255}
				}
				return color.RGBA{R: 230, G: 230, B: 230, A: 255}
			}),
			MimeType: "image/png",
		},
	}}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:             db,
		StaticRenderer: renderer,
		WorkspaceRoot:  workspaceRoot,
	})
	result, err := service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err != nil {
		t.Fatalf("Generate video keyframe diff: %v", err)
	}
	if len(renderer.inputs) != 4 {
		t.Fatalf("renderer inputs = %d, want previous/latest screenshots for two video times", len(renderer.inputs))
	}
	if len(result.Comparisons) != 2 {
		t.Fatalf("comparisons = %d, want 2", len(result.Comparisons))
	}
	if !strings.Contains(renderer.inputs[1].SourceURL, "#t=1") || !strings.Contains(renderer.inputs[3].SourceURL, "#t=1") {
		t.Fatalf("renderer video URLs = %#v", renderer.inputs)
	}
	reportAbs := ResolveInsideWorkspace(workspaceRoot, result.ReportFile.FilePath)
	reportBytes, err := os.ReadFile(reportAbs)
	if err != nil {
		t.Fatalf("read report html: %v", err)
	}
	reportHTML := string(reportBytes)
	for _, want := range []string{"Automated multi-screenshot analysis", "t=0s", "t=1s", "2 screenshot comparisons were analyzed"} {
		if !strings.Contains(reportHTML, want) {
			t.Fatalf("video keyframe report missing %q in:\n%s", want, reportHTML)
		}
	}
}

func TestVisualDiffScreenshotTargets_DeduplicatesConfiguredVideoTimes(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_VISUAL_DIFF_VIDEO_TIMES", "0, 1, 1.0, bad, -2, 2.50, 2.5")

	got := visualDiffScreenshotTargets(ArtifactView{OutputType: "mp4", PreviewType: "video", MimeType: "video/mp4"}, "/tmp/story.mp4")

	if len(got) != 3 {
		t.Fatalf("targets = %#v, want 3 unique configured video times", got)
	}
	wantLabels := []string{"t=0s", "t=1s", "t=2.5s"}
	wantFragments := []string{"#t=0", "#t=1", "#t=2.5"}
	for i := range wantLabels {
		if got[i].Label != wantLabels[i] || !strings.Contains(got[i].SourceURL, wantFragments[i]) {
			t.Fatalf("target[%d] = %#v, want label %q and URL fragment %q", i, got[i], wantLabels[i], wantFragments[i])
		}
	}
}

func TestArtifactVisualDiffImageReportServiceRejectsNonImageArtifact(t *testing.T) {
	db := testutil.NewTestDB(t)
	workspaceRoot := t.TempDir()
	thread := seedVisualDiffThread(t, db)
	previousFile := seedVisualDiffTextFile(t, db, workspaceRoot, thread)
	latestFile := seedVisualDiffImageFile(t, db, workspaceRoot, thread, "poster.png", func(x, y int) color.Color {
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	})
	repo := NewArtifactRegistryRepository(db)
	previousArtifact, err := repo.UpsertFromThreadFile(previousFile)
	if err != nil {
		t.Fatalf("upsert previous: %v", err)
	}
	latestArtifact, err := repo.UpsertFromThreadFile(latestFile)
	if err != nil {
		t.Fatalf("upsert latest: %v", err)
	}

	service := NewArtifactVisualDiffImageReportService(ArtifactVisualDiffImageReportOptions{
		DB:            db,
		WorkspaceRoot: workspaceRoot,
	})
	_, err = service.Generate(42, thread.Id, previousArtifact.Id, latestArtifact.Id)
	if err == nil || !strings.Contains(err.Error(), "previous artifact is not a supported image") {
		t.Fatalf("error = %v, want non-image rejection", err)
	}
}

func seedVisualDiffThread(t *testing.T, db *gorm.DB) *workagentModel.ChatThread {
	t.Helper()
	thread := workagentModel.ChatThread{
		UID:  42,
		UUID: "visual-diff-thread",
		Name: "Visual diff thread",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return &thread
}

func seedVisualDiffImageFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, fileName string, pixel func(x, y int) color.Color) *workagentModel.ThreadFile {
	t.Helper()
	content := testVisualDiffPNG(t, 4, 4, pixel)
	relPath := "uid/42/20260521/thread_visual-diff-thread/outputs/" + fileName
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create image dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     fileName,
		DisplayName:  fileName,
		FileSize:     uint64(len(content)),
		FileType:     "png",
		MimeType:     "image/png",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff image fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed image file: %v", err)
	}
	return &file
}

func seedVisualDiffPDFFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, fileName string) *workagentModel.ThreadFile {
	t.Helper()
	content := []byte("%PDF-1.7 fake visual diff fixture")
	relPath := "uid/42/20260521/thread_visual-diff-thread/outputs/" + fileName
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create pdf dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     fileName,
		DisplayName:  fileName,
		FileSize:     uint64(len(content)),
		FileType:     "pdf",
		MimeType:     "application/pdf",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff PDF fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed pdf file: %v", err)
	}
	return &file
}

func seedVisualDiffVideoFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, fileName string) *workagentModel.ThreadFile {
	t.Helper()
	content := []byte("\x00\x00\x00\x18ftypmp42fake visual diff fixture")
	relPath := "uid/42/20260521/thread_visual-diff-thread/outputs/" + fileName
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create video dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write video: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     fileName,
		DisplayName:  fileName,
		FileSize:     uint64(len(content)),
		FileType:     "mp4",
		MimeType:     "video/mp4",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff video fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed video file: %v", err)
	}
	return &file
}

func seedVisualDiffTextFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread) *workagentModel.ThreadFile {
	t.Helper()
	content := []byte("not an image")
	relPath := "uid/42/20260521/thread_visual-diff-thread/outputs/notes.txt"
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create text dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write text: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     "notes.txt",
		DisplayName:  "notes.txt",
		FileSize:     uint64(len(content)),
		FileType:     "txt",
		MimeType:     "text/plain",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff text fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed text file: %v", err)
	}
	return &file
}

func seedVisualDiffHTMLFile(t *testing.T, db *gorm.DB, workspaceRoot string, thread *workagentModel.ChatThread, fileName string, html string) *workagentModel.ThreadFile {
	t.Helper()
	content := []byte(html)
	relPath := "uid/42/20260521/thread_visual-diff-thread/outputs/" + fileName
	absPath := filepath.Join(workspaceRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create html dir: %v", err)
	}
	if err := os.WriteFile(absPath, content, 0o644); err != nil {
		t.Fatalf("write html: %v", err)
	}
	file := workagentModel.ThreadFile{
		UID:          thread.UID,
		ThreadID:     thread.Id,
		FileName:     fileName,
		DisplayName:  fileName,
		FileSize:     uint64(len(content)),
		FileType:     "html",
		MimeType:     "text/html",
		FilePath:     relPath,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  "Visual diff HTML fixture",
		ExistsOnDisk: true,
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatalf("seed html file: %v", err)
	}
	return &file
}
