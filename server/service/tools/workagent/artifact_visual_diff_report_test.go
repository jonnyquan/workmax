package workagent

import (
	"image/color"
	"strings"
	"testing"
)

func TestBuildArtifactVisualDiffReportHTML(t *testing.T) {
	analysis := ArtifactVisualDiffImageAnalysis{
		PreviousWidth:         1200,
		PreviousHeight:        630,
		LatestWidth:           1080,
		LatestHeight:          1080,
		ComparedWidth:         1080,
		ComparedHeight:        630,
		DimensionMismatch:     true,
		ChangedPixelRatio:     0.236,
		AverageLuminanceDelta: 0.184,
	}
	html := BuildArtifactVisualDiffReportHTML(ArtifactVisualDiffReportInput{
		Previous: ArtifactView{
			ID:                   "artifact-7",
			DisplayName:          "poster-v1.png",
			OutputType:           "png",
			PreviewType:          "image",
			PreviewURL:           "/workspace/poster-v1.png",
			Version:              1,
			DesignSystemBasename: "modern-minimal",
			FilePath:             "outputs/poster-v1.png",
		},
		Latest: ArtifactView{
			ID:                   "artifact-8",
			DisplayName:          "poster-v2.png",
			OutputType:           "png",
			PreviewType:          "image",
			PreviewURL:           "/workspace/poster-v2.png",
			Version:              2,
			DesignSystemBasename: "modern-minimal",
			FilePath:             "outputs/poster-v2.png",
		},
		Summary:        "Latest improves hierarchy but weakens brand contrast.",
		Recommendation: "revise",
		ImageAnalysis:  &analysis,
		Hotspots: []ArtifactVisualDiffHotspot{
			{Area: "Hero", Change: "Headline moved up", Impact: "Better hierarchy", Severity: "medium"},
		},
	})

	for _, want := range []string{
		"<!doctype html>",
		"Visual Diff Report",
		"poster-v1.png · v1 · png · design system: modern-minimal · path: outputs/poster-v1.png",
		"poster-v2.png · v2 · png · design system: modern-minimal · path: outputs/poster-v2.png",
		"<img src=\"/workspace/poster-v1.png\"",
		"<img src=\"/workspace/poster-v2.png\"",
		"Latest improves hierarchy but weakens brand contrast.",
		"Recommendation: revise",
		"Automated image analysis",
		"<dt>Previous size</dt><dd>1200x630</dd>",
		"<dt>Latest size</dt><dd>1080x1080</dd>",
		"<dt>Changed pixels</dt><dd>23.6%</dd>",
		"<dt>Average luminance delta</dt><dd>18.4%</dd>",
		"<dt>Dimension mismatch</dt><dd>yes</dd>",
		"Headline moved up",
		"Impact: Better hierarchy",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("report missing %q in:\n%s", want, html)
		}
	}
}

func TestBuildArtifactVisualDiffReportHTML_EscapesUntrustedContent(t *testing.T) {
	report := BuildArtifactVisualDiffReportHTML(ArtifactVisualDiffReportInput{
		Previous: ArtifactView{DisplayName: `<script>alert("prev")</script>`, PreviewURL: `"><script>alert(1)</script>`, PreviewType: "image"},
		Latest:   ArtifactView{DisplayName: "latest", PreviewURL: "/safe.png", PreviewType: "image"},
		Summary:  `<img src=x onerror=alert(1)>`,
		Hotspots: []ArtifactVisualDiffHotspot{
			{Area: "<b>Area</b>", Change: "<script>bad()</script>", Impact: "ok"},
		},
	})

	for _, forbidden := range []string{
		`<script>alert("prev")</script>`,
		`"><script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<script>bad()</script>`,
	} {
		if strings.Contains(report, forbidden) {
			t.Fatalf("report contains unescaped content %q:\n%s", forbidden, report)
		}
	}
	for _, want := range []string{
		"&lt;script&gt;alert(&#34;prev&#34;)&lt;/script&gt;",
		"&lt;img src=x onerror=alert(1)&gt;",
		"&lt;script&gt;bad()&lt;/script&gt;",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing escaped content %q:\n%s", want, report)
		}
	}
}

func TestBuildArtifactVisualDiffImageReportHTML(t *testing.T) {
	previousContent := testVisualDiffPNG(t, 3, 3, func(x, y int) color.Color {
		return color.RGBA{R: 240, G: 240, B: 240, A: 255}
	})
	latestContent := testVisualDiffPNG(t, 3, 3, func(x, y int) color.Color {
		if x == 0 {
			return color.RGBA{R: 10, G: 10, B: 10, A: 255}
		}
		return color.RGBA{R: 240, G: 240, B: 240, A: 255}
	})

	report, analysis, err := BuildArtifactVisualDiffImageReportHTML(
		ArtifactView{DisplayName: "previous.png", PreviewType: "image", PreviewURL: "/previous.png", Version: 1},
		ArtifactView{DisplayName: "latest.png", PreviewType: "image", PreviewURL: "/latest.png", Version: 2},
		previousContent,
		latestContent,
	)
	if err != nil {
		t.Fatalf("BuildArtifactVisualDiffImageReportHTML: %v", err)
	}
	if analysis.ChangedPixelRatio < 0.32 || analysis.ChangedPixelRatio > 0.34 {
		t.Fatalf("changed ratio = %f, want about 0.33", analysis.ChangedPixelRatio)
	}
	for _, want := range []string{
		"previous.png · v1 · image",
		"latest.png · v2 · image",
		"Automated image comparison found 33.3% changed pixels",
		"Automated image analysis",
		"<dt>Changed pixels</dt><dd>33.3%</dd>",
		"Rendered pixels",
		"Recommendation: revise",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q in:\n%s", want, report)
		}
	}
}
