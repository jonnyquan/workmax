package workagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"

	"gorm.io/gorm"
)

type ArtifactVisualDiffImageReportService struct {
	db             *gorm.DB
	artifactRepo   *ArtifactRegistryRepository
	staticRenderer HTMLStaticRenderer
	workspaceRoot  string
}

type ArtifactVisualDiffImageReportOptions struct {
	DB             *gorm.DB
	ArtifactRepo   *ArtifactRegistryRepository
	StaticRenderer HTMLStaticRenderer
	WorkspaceRoot  string
}

type ArtifactVisualDiffImageReportResult struct {
	ReportFile     *workagentModel.ThreadFile          `json:"reportFile"`
	ReportArtifact *workagentModel.ArtifactRegistry    `json:"reportArtifact"`
	Analysis       ArtifactVisualDiffImageAnalysis     `json:"analysis"`
	Comparisons    []ArtifactVisualDiffImageComparison `json:"comparisons,omitempty"`
}

type artifactVisualDiffScreenshot struct {
	Label   string
	Content []byte
}

func NewArtifactVisualDiffImageReportService(opts ArtifactVisualDiffImageReportOptions) *ArtifactVisualDiffImageReportService {
	db := opts.DB
	if db == nil {
		db = globals.GraDBs["system"]
	}
	artifactRepo := opts.ArtifactRepo
	if artifactRepo == nil {
		artifactRepo = NewArtifactRegistryRepository(db)
	}
	workspaceRoot := strings.TrimSpace(opts.WorkspaceRoot)
	if workspaceRoot == "" {
		workspaceRoot = ResolveWorkspaceRoot()
	}
	return &ArtifactVisualDiffImageReportService{
		db:             db,
		artifactRepo:   artifactRepo,
		staticRenderer: opts.StaticRenderer,
		workspaceRoot:  workspaceRoot,
	}
}

func (s *ArtifactVisualDiffImageReportService) Generate(uid int, threadID uint, previousArtifactID uint, latestArtifactID uint) (*ArtifactVisualDiffImageReportResult, error) {
	if s == nil || s.db == nil || s.artifactRepo == nil {
		return nil, fmt.Errorf("visual diff image report: nil dependency")
	}
	if uid == 0 || threadID == 0 || previousArtifactID == 0 || latestArtifactID == 0 {
		return nil, fmt.Errorf("visual diff image report: uid, thread, previous artifact, and latest artifact are required")
	}
	previousArtifact, previousFile, err := s.artifactRepo.LoadForOwnerWithThreadFile(uid, threadID, previousArtifactID)
	if err != nil {
		return nil, fmt.Errorf("visual diff image report: load previous artifact: %w", err)
	}
	latestArtifact, latestFile, err := s.artifactRepo.LoadForOwnerWithThreadFile(uid, threadID, latestArtifactID)
	if err != nil {
		return nil, fmt.Errorf("visual diff image report: load latest artifact: %w", err)
	}
	previousView := ArtifactViewFromRegistryAndThreadFile(*previousArtifact, *previousFile)
	latestView := ArtifactViewFromRegistryAndThreadFile(*latestArtifact, *latestFile)
	if !isVisualDiffComparableArtifact(previousView) {
		return nil, fmt.Errorf("visual diff image report: previous artifact is not a supported image, html, pdf, or video")
	}
	if !isVisualDiffComparableArtifact(latestView) {
		return nil, fmt.Errorf("visual diff image report: latest artifact is not a supported image, html, pdf, or video")
	}

	workspaceRoot := strings.TrimSpace(s.workspaceRoot)
	previousPath := ResolveInsideWorkspace(workspaceRoot, previousFile.FilePath)
	if previousPath == "" {
		return nil, fmt.Errorf("visual diff image report: previous file is outside workspace")
	}
	latestPath := ResolveInsideWorkspace(workspaceRoot, latestFile.FilePath)
	if latestPath == "" {
		return nil, fmt.Errorf("visual diff image report: latest file is outside workspace")
	}
	previousScreenshots, err := s.loadComparableImageContents(previousView, previousFile, previousPath)
	if err != nil {
		return nil, fmt.Errorf("visual diff image report: prepare previous image: %w", err)
	}
	latestScreenshots, err := s.loadComparableImageContents(latestView, latestFile, latestPath)
	if err != nil {
		return nil, fmt.Errorf("visual diff image report: prepare latest image: %w", err)
	}
	comparisons, analysis, err := analyzeVisualDiffScreenshotPairs(previousScreenshots, latestScreenshots)
	if err != nil {
		return nil, fmt.Errorf("visual diff image report: build report: %w", err)
	}
	reportHTML := BuildArtifactVisualDiffReportHTML(ArtifactVisualDiffReportInput{
		Previous:       previousView,
		Latest:         latestView,
		Summary:        visualDiffAnalysisSummary(analysis),
		Recommendation: analysis.Recommendation,
		Hotspots:       analysis.Hotspots,
		ImageAnalysis:  &analysis,
		ImageAnalyses:  comparisons,
	})
	outputName := buildVisualDiffReportOutputName(previousArtifactID, latestArtifactID)
	outputAbs, outputRel, err := resolveVisualDiffReportOutputPath(workspaceRoot, latestPath, outputName)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outputAbs), 0o755); err != nil {
		return nil, fmt.Errorf("visual diff image report: create output dir: %w", err)
	}
	content := []byte(reportHTML)
	if err := os.WriteFile(outputAbs, content, 0o644); err != nil {
		return nil, fmt.Errorf("visual diff image report: write output file: %w", err)
	}

	reportFile := workagentModel.ThreadFile{
		UID:          uid,
		ThreadID:     threadID,
		FileName:     outputName,
		DisplayName:  outputName,
		FileSize:     uint64(len(content)),
		FileType:     "html",
		MimeType:     "text/html",
		FilePath:     outputRel,
		FileHash:     md5Hex(content),
		FileSource:   workagentModel.FileSourceOutput,
		Description:  visualDiffReportDescription(previousArtifactID, latestArtifactID, previousFile.Id, latestFile.Id, outputRel, analysis, comparisons),
		ExistsOnDisk: true,
	}
	var reportArtifact workagentModel.ArtifactRegistry
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&reportFile).Error; err != nil {
			return fmt.Errorf("create visual diff report file: %w", err)
		}
		registered, err := NewArtifactRegistryRepository(tx).UpsertFromThreadFile(&reportFile)
		if err != nil {
			return fmt.Errorf("register visual diff report artifact: %w", err)
		}
		updates := map[string]interface{}{
			"status":              workagentModel.ArtifactStatusFinal,
			"review_state":        workagentModel.ArtifactReviewApproved,
			"parent_artifact_id":  latestArtifactID,
			"artifact_relation":   workagentModel.ArtifactRelationComparisonReport,
			"comparison_source":   "auto_visual_diff",
			"comparison_summary":  visualDiffAnalysisSummary(analysis),
			"comparison_decision": analysis.Recommendation,
		}
		if err := tx.Model(registered).Updates(updates).Error; err != nil {
			return fmt.Errorf("mark visual diff report artifact: %w", err)
		}
		if err := tx.First(&reportArtifact, registered.Id).Error; err != nil {
			return fmt.Errorf("reload visual diff report artifact: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ArtifactVisualDiffImageReportResult{
		ReportFile:     &reportFile,
		ReportArtifact: &reportArtifact,
		Analysis:       analysis,
		Comparisons:    comparisons,
	}, nil
}

func (s *ArtifactVisualDiffImageReportService) loadComparableImageContents(view ArtifactView, file *workagentModel.ThreadFile, sourcePath string) ([]artifactVisualDiffScreenshot, error) {
	if isVisualDiffImageArtifact(view) {
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("read image: %w", err)
		}
		return []artifactVisualDiffScreenshot{{Label: "image", Content: content}}, nil
	}
	if !isVisualDiffHTMLArtifact(view) && !isVisualDiffPDFArtifact(view) && !isVisualDiffVideoArtifact(view) {
		return nil, fmt.Errorf("unsupported comparable artifact")
	}
	sourceHTML := ""
	if isVisualDiffHTMLArtifact(view) {
		sourceBytes, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("read html source: %w", err)
		}
		sourceHTML = string(sourceBytes)
	}
	renderer := s.staticRenderer
	if renderer == nil {
		defaultRenderer, err := NewDefaultBrowserCommandStaticRenderer()
		if err != nil {
			return nil, fmt.Errorf("artifact screenshot renderer unavailable: %w", err)
		}
		renderer = defaultRenderer
	}
	targets := visualDiffScreenshotTargets(view, sourcePath)
	out := make([]artifactVisualDiffScreenshot, 0, len(targets))
	for _, target := range targets {
		outputName := visualDiffScreenshotOutputName(file, target)
		outputAbs, outputRel, err := resolveVisualDiffReportOutputPath(strings.TrimSpace(s.workspaceRoot), sourcePath, outputName)
		if err != nil {
			return nil, err
		}
		output, err := renderer.RenderStaticHTML(context.Background(), HTMLStaticRenderInput{
			SourceFile:      *file,
			SourcePath:      sourcePath,
			SourceURL:       target.SourceURL,
			SourceHTML:      sourceHTML,
			Target:          "png",
			OutputExtension: ".png",
			WorkspaceRoot:   strings.TrimSpace(s.workspaceRoot),
			OutputFileName:  outputName,
			OutputFilePath:  outputRel,
			OutputFileAbs:   outputAbs,
		})
		if err != nil {
			return nil, fmt.Errorf("render artifact screenshot %s: %w", target.Label, err)
		}
		if len(output.Content) == 0 {
			return nil, fmt.Errorf("render artifact screenshot %s: empty png output", target.Label)
		}
		if err := validateRenderMimeType("png", output.MimeType); err != nil {
			return nil, fmt.Errorf("render artifact screenshot %s: %w", target.Label, err)
		}
		if err := validateRenderOutputSignature("png", output.Content); err != nil {
			return nil, fmt.Errorf("render artifact screenshot %s: %w", target.Label, err)
		}
		out = append(out, artifactVisualDiffScreenshot{Label: target.Label, Content: output.Content})
	}
	return out, nil
}

type visualDiffScreenshotTarget struct {
	Label     string
	SourceURL string
}

func visualDiffScreenshotTargets(view ArtifactView, sourcePath string) []visualDiffScreenshotTarget {
	if isVisualDiffPDFArtifact(view) {
		pageCount := detectPDFPageCount(sourcePath)
		if pageCount > 1 {
			maxPages := visualDiffPDFPageLimit()
			if pageCount < maxPages {
				maxPages = pageCount
			}
			targets := make([]visualDiffScreenshotTarget, 0, maxPages)
			for page := 1; page <= maxPages; page++ {
				targets = append(targets, visualDiffScreenshotTarget{
					Label:     fmt.Sprintf("page %d", page),
					SourceURL: fileURL(sourcePath) + "#page=" + strconv.Itoa(page),
				})
			}
			return targets
		}
		return []visualDiffScreenshotTarget{{Label: "page 1"}}
	}
	if isVisualDiffVideoArtifact(view) {
		times := configuredVisualDiffVideoTimes()
		if len(times) > 0 {
			targets := make([]visualDiffScreenshotTarget, 0, len(times))
			for _, seconds := range times {
				targets = append(targets, visualDiffScreenshotTarget{
					Label:     fmt.Sprintf("t=%ss", seconds),
					SourceURL: fileURL(sourcePath) + "#t=" + seconds,
				})
			}
			return targets
		}
		return []visualDiffScreenshotTarget{{Label: "first frame"}}
	}
	return []visualDiffScreenshotTarget{{Label: "screenshot"}}
}

func visualDiffScreenshotOutputName(file *workagentModel.ThreadFile, target visualDiffScreenshotTarget) string {
	base := strings.TrimSuffix(filepath.Base(file.FileName), filepath.Ext(file.FileName))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = fmt.Sprintf("artifact-%d", file.Id)
	}
	suffix := strings.NewReplacer(" ", "-", "=", "-", ".", "-", ":", "-").Replace(strings.ToLower(strings.TrimSpace(target.Label)))
	if suffix == "" {
		suffix = "screenshot"
	}
	return base + "-visual-diff-" + suffix + ".png"
}

var pdfPageTypePattern = regexp.MustCompile(`/Type\s*/Page\b`)

func detectPDFPageCount(path string) int {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	count := len(pdfPageTypePattern.FindAll(content, -1))
	if count <= 0 {
		return 1
	}
	return count
}

func visualDiffPDFPageLimit() int {
	value := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_VISUAL_DIFF_PDF_PAGE_LIMIT"))
	if value == "" {
		return 3
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		return 3
	}
	if limit > 10 {
		return 10
	}
	return limit
}

func configuredVisualDiffVideoTimes() []string {
	raw := strings.TrimSpace(os.Getenv("WORKMAX_WORKAGENT_VISUAL_DIFF_VIDEO_TIMES"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || seconds < 0 {
			continue
		}
		normalized := strconv.FormatFloat(seconds, 'f', -1, 64)
		if seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
		if len(out) >= 10 {
			break
		}
	}
	return out
}

func analyzeVisualDiffScreenshotPairs(previous []artifactVisualDiffScreenshot, latest []artifactVisualDiffScreenshot) ([]ArtifactVisualDiffImageComparison, ArtifactVisualDiffImageAnalysis, error) {
	count := minInt(len(previous), len(latest))
	if count <= 0 {
		return nil, ArtifactVisualDiffImageAnalysis{}, fmt.Errorf("no comparable screenshots")
	}
	comparisons := make([]ArtifactVisualDiffImageComparison, 0, count)
	analyses := make([]ArtifactVisualDiffImageAnalysis, 0, count)
	for i := 0; i < count; i++ {
		analysis, err := AnalyzeArtifactVisualDiffImages(previous[i].Content, latest[i].Content)
		if err != nil {
			return nil, ArtifactVisualDiffImageAnalysis{}, err
		}
		label := previous[i].Label
		if latest[i].Label != "" && latest[i].Label != label {
			label = strings.TrimSpace(label + " vs " + latest[i].Label)
		}
		comparisons = append(comparisons, ArtifactVisualDiffImageComparison{Label: label, Analysis: analysis})
		analyses = append(analyses, analysis)
	}
	return comparisons, aggregateVisualDiffImageAnalyses(analyses), nil
}

func aggregateVisualDiffImageAnalyses(analyses []ArtifactVisualDiffImageAnalysis) ArtifactVisualDiffImageAnalysis {
	if len(analyses) == 0 {
		return ArtifactVisualDiffImageAnalysis{}
	}
	out := analyses[0]
	var changedSum float64
	var lumaSum float64
	requiresRevision := false
	out.Hotspots = nil
	for _, analysis := range analyses {
		changedSum += analysis.ChangedPixelRatio
		lumaSum += analysis.AverageLuminanceDelta
		if analysis.Recommendation == "revise" || analysis.Recommendation == "rollback" {
			requiresRevision = true
		}
		if analysis.ChangedPixelRatio > out.ChangedPixelRatio {
			out.ChangedPixelRatio = analysis.ChangedPixelRatio
		}
		if analysis.AverageLuminanceDelta > out.AverageLuminanceDelta {
			out.AverageLuminanceDelta = analysis.AverageLuminanceDelta
		}
		out.DimensionMismatch = out.DimensionMismatch || analysis.DimensionMismatch
	}
	out.ChangedPixelRatio = changedSum / float64(len(analyses))
	out.AverageLuminanceDelta = lumaSum / float64(len(analyses))
	out.Recommendation = visualDiffRecommendationForImageAnalysis(out)
	if requiresRevision {
		out.Recommendation = "revise"
	}
	out.Hotspots = visualDiffHotspotsForImageAnalysis(out)
	if len(analyses) > 1 {
		out.Hotspots = append([]ArtifactVisualDiffHotspot{{
			Area:     "Screenshot set",
			Change:   fmt.Sprintf("%d screenshot comparisons were analyzed; summary metrics are averaged across comparable pages or frames.", len(analyses)),
			Impact:   "Review the multi-screenshot section before deciding whether a PDF page or video keyframe needs targeted revision.",
			Severity: "medium",
		}}, out.Hotspots...)
	}
	return out
}

func isVisualDiffComparableArtifact(view ArtifactView) bool {
	return isVisualDiffImageArtifact(view) || isVisualDiffHTMLArtifact(view) || isVisualDiffPDFArtifact(view) || isVisualDiffVideoArtifact(view)
}

func isVisualDiffImageArtifact(view ArtifactView) bool {
	if view.PreviewType == "image" {
		switch strings.ToLower(strings.TrimSpace(view.OutputType)) {
		case "png", "jpg", "jpeg", "gif":
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(view.MimeType)) {
	case "image/png", "image/jpeg", "image/gif":
		return true
	}
	return false
}

func isVisualDiffHTMLArtifact(view ArtifactView) bool {
	if strings.ToLower(strings.TrimSpace(view.OutputType)) == "html" || strings.ToLower(strings.TrimSpace(view.PreviewType)) == "html" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(view.MimeType)) == "text/html"
}

func isVisualDiffPDFArtifact(view ArtifactView) bool {
	if strings.ToLower(strings.TrimSpace(view.OutputType)) == "pdf" || strings.ToLower(strings.TrimSpace(view.PreviewType)) == "pdf" {
		return true
	}
	return strings.ToLower(strings.TrimSpace(view.MimeType)) == "application/pdf"
}

func isVisualDiffVideoArtifact(view ArtifactView) bool {
	outputType := strings.ToLower(strings.TrimSpace(view.OutputType))
	previewType := strings.ToLower(strings.TrimSpace(view.PreviewType))
	mimeType := strings.ToLower(strings.TrimSpace(view.MimeType))
	if previewType == "video" {
		return true
	}
	switch outputType {
	case "mp4", "webm", "mov":
		return true
	}
	switch mimeType {
	case "video/mp4", "video/webm", "video/quicktime":
		return true
	}
	return false
}

func buildVisualDiffReportOutputName(previousArtifactID uint, latestArtifactID uint) string {
	return fmt.Sprintf("visual-diff-artifact-%d-vs-%d.html", previousArtifactID, latestArtifactID)
}

func resolveVisualDiffReportOutputPath(workspaceRoot string, latestPath string, outputName string) (string, string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return "", "", fmt.Errorf("visual diff image report: workspace root is required")
	}
	outputName = filepath.Base(outputName)
	if outputName == "" || outputName == "." || outputName == string(filepath.Separator) {
		return "", "", fmt.Errorf("visual diff image report: output filename is invalid")
	}
	outputDir := filepath.Dir(latestPath)
	if filepath.Base(outputDir) == "uploads" {
		outputDir = filepath.Join(filepath.Dir(outputDir), "outputs")
	}
	outputAbs := filepath.Join(outputDir, outputName)
	rootAbs, err := filepath.Abs(filepath.Clean(workspaceRoot))
	if err != nil {
		return "", "", fmt.Errorf("visual diff image report: resolve workspace root: %w", err)
	}
	absOut, err := filepath.Abs(outputAbs)
	if err != nil {
		return "", "", fmt.Errorf("visual diff image report: resolve output path: %w", err)
	}
	sep := string(filepath.Separator)
	if absOut != rootAbs && !strings.HasPrefix(absOut+sep, rootAbs+sep) {
		return "", "", fmt.Errorf("visual diff image report: output file is outside workspace")
	}
	rel, err := filepath.Rel(rootAbs, absOut)
	if err != nil {
		return "", "", fmt.Errorf("visual diff image report: output path relative: %w", err)
	}
	return absOut, filepath.ToSlash(rel), nil
}

func visualDiffReportDescription(previousArtifactID uint, latestArtifactID uint, previousFileID uint, latestFileID uint, reportPath string, analysis ArtifactVisualDiffImageAnalysis, comparisons []ArtifactVisualDiffImageComparison) string {
	labels := make([]string, 0, len(comparisons))
	for _, comparison := range comparisons {
		if strings.TrimSpace(comparison.Label) != "" {
			labels = append(labels, strings.TrimSpace(comparison.Label))
		}
	}
	payload := map[string]interface{}{
		"kind":                    "workagent_visual_diff_report",
		"source":                  "auto_visual_diff",
		"previous_artifact_id":    previousArtifactID,
		"latest_artifact_id":      latestArtifactID,
		"previous_file_id":        previousFileID,
		"latest_file_id":          latestFileID,
		"report_path":             reportPath,
		"changed_pixel_ratio":     analysis.ChangedPixelRatio,
		"average_luminance_delta": analysis.AverageLuminanceDelta,
		"dimension_mismatch":      analysis.DimensionMismatch,
		"recommendation":          analysis.Recommendation,
		"comparison_count":        len(comparisons),
		"comparison_labels":       labels,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "workagent_visual_diff_report"
	}
	return string(raw)
}

func visualDiffAnalysisSummary(analysis ArtifactVisualDiffImageAnalysis) string {
	summary := fmt.Sprintf(
		"auto visual diff: %.1f%% changed pixels, %.1f%% average luminance delta",
		analysis.ChangedPixelRatio*100,
		analysis.AverageLuminanceDelta*100,
	)
	if analysis.DimensionMismatch {
		summary += fmt.Sprintf(", dimensions %dx%d -> %dx%d", analysis.PreviousWidth, analysis.PreviousHeight, analysis.LatestWidth, analysis.LatestHeight)
	}
	return summary
}
