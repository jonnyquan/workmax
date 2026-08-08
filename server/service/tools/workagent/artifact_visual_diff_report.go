package workagent

import (
	"fmt"
	"html"
	"strings"
)

type ArtifactVisualDiffReportInput struct {
	Latest         ArtifactView
	Previous       ArtifactView
	Summary        string
	Recommendation string
	Hotspots       []ArtifactVisualDiffHotspot
	ImageAnalysis  *ArtifactVisualDiffImageAnalysis
	ImageAnalyses  []ArtifactVisualDiffImageComparison
}

type ArtifactVisualDiffHotspot struct {
	Area     string `json:"area"`
	Change   string `json:"change"`
	Impact   string `json:"impact"`
	Severity string `json:"severity"`
}

type ArtifactVisualDiffImageComparison struct {
	Label    string                          `json:"label"`
	Analysis ArtifactVisualDiffImageAnalysis `json:"analysis"`
}

func BuildArtifactVisualDiffImageReportHTML(previous ArtifactView, latest ArtifactView, previousContent, latestContent []byte) (string, ArtifactVisualDiffImageAnalysis, error) {
	analysis, err := AnalyzeArtifactVisualDiffImages(previousContent, latestContent)
	if err != nil {
		return "", ArtifactVisualDiffImageAnalysis{}, err
	}
	summary := fmt.Sprintf(
		"Automated image comparison found %.1f%% changed pixels with %.1f%% average luminance delta across a %dx%d comparable area.",
		analysis.ChangedPixelRatio*100,
		analysis.AverageLuminanceDelta*100,
		analysis.ComparedWidth,
		analysis.ComparedHeight,
	)
	if analysis.DimensionMismatch {
		summary += fmt.Sprintf(" Canvas dimensions changed from %dx%d to %dx%d.", analysis.PreviousWidth, analysis.PreviousHeight, analysis.LatestWidth, analysis.LatestHeight)
	}
	report := BuildArtifactVisualDiffReportHTML(ArtifactVisualDiffReportInput{
		Previous:       previous,
		Latest:         latest,
		Summary:        summary,
		Recommendation: analysis.Recommendation,
		Hotspots:       analysis.Hotspots,
		ImageAnalysis:  &analysis,
	})
	return report, analysis, nil
}

func BuildArtifactVisualDiffReportHTML(input ArtifactVisualDiffReportInput) string {
	latest := input.Latest
	previous := input.Previous
	recommendation := normalizeVisualDiffRecommendation(input.Recommendation)
	if recommendation == "" {
		recommendation = "revise"
	}

	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	b.WriteString("<title>Work Agent Visual Diff Report</title>\n")
	b.WriteString("<style>")
	b.WriteString(`:root{color-scheme:light;--bg:#f7f8fa;--panel:#fff;--text:#15171a;--muted:#5d6673;--line:#d9dde5;--accent:#2457d6;--warn:#9a5b00;--bad:#b42318;--good:#067647}*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{max-width:1180px;margin:0 auto;padding:28px 20px 40px}header{display:flex;flex-wrap:wrap;gap:12px;align-items:flex-end;justify-content:space-between;margin-bottom:18px}h1{margin:0;font-size:24px;line-height:1.2}h2{margin:0 0 10px;font-size:15px}.meta{color:var(--muted);font-size:12px}.pill{display:inline-flex;align-items:center;border:1px solid var(--line);border-radius:999px;padding:3px 8px;background:#fff;color:var(--muted);font-size:12px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px}.panel{border:1px solid var(--line);background:var(--panel);border-radius:8px;padding:14px}.preview{display:grid;place-items:center;min-height:220px;border:1px dashed var(--line);border-radius:6px;background:#fbfcfd;overflow:hidden}.preview img,.preview video,.preview iframe{max-width:100%;width:100%;height:auto;border:0}.preview iframe{height:420px;background:#fff}.placeholder{padding:28px;text-align:center;color:var(--muted)}.summary{margin-top:14px}.hotspots{margin:0;padding:0;list-style:none;display:grid;gap:8px}.hotspot{border:1px solid var(--line);border-radius:6px;padding:10px;background:#fff}.hotspot strong{display:block;margin-bottom:3px}.severity-high{border-color:#f5b5ad}.severity-medium{border-color:#f2d18c}.recommendation{font-weight:700}.recommendation.keep{color:var(--good)}.recommendation.revise{color:var(--warn)}.recommendation.rollback{color:var(--bad)}@media(max-width:760px){.grid{grid-template-columns:1fr}main{padding:20px 12px 32px}}`)
	b.WriteString("</style>\n</head>\n<body>\n<main>\n")
	b.WriteString("<header><div><h1>Visual Diff Report</h1><div class=\"meta\">Work Agent artifact comparison</div></div>")
	b.WriteString(fmt.Sprintf("<span class=\"pill\">recommendation: %s</span></header>\n", escapeHTML(recommendation)))
	b.WriteString("<section class=\"grid\" aria-label=\"Artifact previews\">\n")
	b.WriteString(renderVisualDiffArtifactPanel("Previous", previous))
	b.WriteString(renderVisualDiffArtifactPanel("Latest", latest))
	b.WriteString("</section>\n")
	b.WriteString("<section class=\"panel summary\" aria-label=\"Summary\">\n<h2>Summary</h2>\n")
	summary := strings.TrimSpace(input.Summary)
	if summary == "" {
		summary = "Review the side-by-side previews and hotspot notes before deciding whether to keep, revise, or rollback."
	}
	b.WriteString("<p>")
	b.WriteString(escapeHTML(summary))
	b.WriteString("</p>\n")
	b.WriteString(fmt.Sprintf("<p class=\"recommendation %s\">Recommendation: %s</p>\n", escapeHTML(recommendation), escapeHTML(recommendation)))
	b.WriteString("</section>\n")
	if input.ImageAnalysis != nil {
		b.WriteString(renderVisualDiffImageAnalysis(*input.ImageAnalysis))
	}
	if len(input.ImageAnalyses) > 0 {
		b.WriteString(renderVisualDiffImageComparisons(input.ImageAnalyses))
	}
	b.WriteString("<section class=\"panel summary\" aria-label=\"Visible changes\">\n<h2>Visible changes</h2>\n")
	if len(input.Hotspots) == 0 {
		b.WriteString("<p class=\"meta\">No hotspots were supplied. Add layout, hierarchy, color, text, brand, or asset changes here.</p>\n")
	} else {
		b.WriteString("<ul class=\"hotspots\">\n")
		for _, hotspot := range input.Hotspots {
			severity := normalizeVisualDiffSeverity(hotspot.Severity)
			b.WriteString(fmt.Sprintf("<li class=\"hotspot severity-%s\">", escapeHTML(severity)))
			b.WriteString("<strong>")
			b.WriteString(escapeHTML(firstNonEmpty(hotspot.Area, "Change")))
			b.WriteString("</strong>")
			b.WriteString("<div>")
			b.WriteString(escapeHTML(hotspot.Change))
			b.WriteString("</div>")
			if strings.TrimSpace(hotspot.Impact) != "" {
				b.WriteString("<div class=\"meta\">Impact: ")
				b.WriteString(escapeHTML(hotspot.Impact))
				b.WriteString("</div>")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	b.WriteString("</section>\n")
	b.WriteString("</main>\n</body>\n</html>\n")
	return b.String()
}

func renderVisualDiffImageComparisons(comparisons []ArtifactVisualDiffImageComparison) string {
	var b strings.Builder
	b.WriteString("<section class=\"panel summary\" aria-label=\"Automated multi-screenshot analysis\">\n")
	b.WriteString("<h2>Automated multi-screenshot analysis</h2>\n")
	b.WriteString("<ul class=\"hotspots\">\n")
	for _, comparison := range comparisons {
		label := strings.TrimSpace(comparison.Label)
		if label == "" {
			label = "screenshot"
		}
		analysis := comparison.Analysis
		b.WriteString("<li class=\"hotspot\">")
		b.WriteString("<strong>")
		b.WriteString(escapeHTML(label))
		b.WriteString("</strong>")
		b.WriteString(fmt.Sprintf(
			"<div>changed %.1f%% · luminance %.1f%% · compared %dx%d",
			analysis.ChangedPixelRatio*100,
			analysis.AverageLuminanceDelta*100,
			analysis.ComparedWidth,
			analysis.ComparedHeight,
		))
		if analysis.DimensionMismatch {
			b.WriteString(fmt.Sprintf(" · size %dx%d -> %dx%d", analysis.PreviousWidth, analysis.PreviousHeight, analysis.LatestWidth, analysis.LatestHeight))
		}
		b.WriteString("</div>")
		b.WriteString(fmt.Sprintf("<div class=\"meta\">recommendation: %s</div>", escapeHTML(normalizeVisualDiffRecommendation(analysis.Recommendation))))
		b.WriteString("</li>\n")
	}
	b.WriteString("</ul>\n</section>\n")
	return b.String()
}

func renderVisualDiffImageAnalysis(analysis ArtifactVisualDiffImageAnalysis) string {
	var b strings.Builder
	b.WriteString("<section class=\"panel summary\" aria-label=\"Automated image analysis\">\n")
	b.WriteString("<h2>Automated image analysis</h2>\n")
	b.WriteString("<dl class=\"meta\">")
	b.WriteString(fmt.Sprintf("<dt>Previous size</dt><dd>%dx%d</dd>", analysis.PreviousWidth, analysis.PreviousHeight))
	b.WriteString(fmt.Sprintf("<dt>Latest size</dt><dd>%dx%d</dd>", analysis.LatestWidth, analysis.LatestHeight))
	b.WriteString(fmt.Sprintf("<dt>Compared area</dt><dd>%dx%d</dd>", analysis.ComparedWidth, analysis.ComparedHeight))
	b.WriteString(fmt.Sprintf("<dt>Changed pixels</dt><dd>%.1f%%</dd>", analysis.ChangedPixelRatio*100))
	b.WriteString(fmt.Sprintf("<dt>Average luminance delta</dt><dd>%.1f%%</dd>", analysis.AverageLuminanceDelta*100))
	if analysis.DimensionMismatch {
		b.WriteString("<dt>Dimension mismatch</dt><dd>yes</dd>")
	} else {
		b.WriteString("<dt>Dimension mismatch</dt><dd>no</dd>")
	}
	b.WriteString("</dl>\n</section>\n")
	return b.String()
}

func renderVisualDiffArtifactPanel(label string, artifact ArtifactView) string {
	var b strings.Builder
	b.WriteString("<article class=\"panel\">\n")
	b.WriteString("<h2>")
	b.WriteString(escapeHTML(label))
	b.WriteString("</h2>\n")
	b.WriteString("<div class=\"meta\">")
	b.WriteString(escapeHTML(formatVisualDiffArtifactMeta(artifact)))
	b.WriteString("</div>\n")
	b.WriteString("<div class=\"preview\">")
	url := strings.TrimSpace(firstNonEmpty(artifact.PreviewURL, artifact.DownloadURL))
	if url == "" {
		b.WriteString("<div class=\"placeholder\">No preview URL available. Use the artifact path for manual inspection.</div>")
	} else {
		switch strings.ToLower(strings.TrimSpace(firstNonEmpty(artifact.PreviewType, artifact.OutputType))) {
		case "image", "png", "jpg", "jpeg", "gif", "svg":
			b.WriteString(fmt.Sprintf("<img src=\"%s\" alt=\"%s artifact preview\">", escapeAttr(url), escapeAttr(strings.ToLower(label))))
		case "video", "mp4", "webm":
			b.WriteString(fmt.Sprintf("<video src=\"%s\" controls muted playsinline></video>", escapeAttr(url)))
		case "html":
			b.WriteString(fmt.Sprintf("<iframe src=\"%s\" sandbox=\"allow-scripts\" referrerpolicy=\"no-referrer\" title=\"%s artifact preview\"></iframe>", escapeAttr(url), escapeAttr(label)))
		default:
			b.WriteString(fmt.Sprintf("<a href=\"%s\">Open artifact</a>", escapeAttr(url)))
		}
	}
	b.WriteString("</div>\n</article>\n")
	return b.String()
}

func formatVisualDiffArtifactMeta(artifact ArtifactView) string {
	version := artifact.Version
	if version <= 0 {
		version = 1
	}
	parts := []string{
		firstNonEmpty(artifact.DisplayName, artifact.Name, artifact.ID),
		fmt.Sprintf("v%d", version),
		firstNonEmpty(artifact.OutputType, artifact.PreviewType, "artifact"),
	}
	if artifact.DesignSystemBasename != "" {
		parts = append(parts, "design system: "+artifact.DesignSystemBasename)
	}
	if artifact.FilePath != "" {
		parts = append(parts, "path: "+artifact.FilePath)
	}
	return strings.Join(parts, " · ")
}

func normalizeVisualDiffRecommendation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "keep", "revise", "rollback":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeVisualDiffSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return "high"
	case "medium":
		return "medium"
	default:
		return "low"
	}
}

func escapeHTML(value string) string {
	return html.EscapeString(strings.TrimSpace(value))
}

func escapeAttr(value string) string {
	return html.EscapeString(strings.TrimSpace(value))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
