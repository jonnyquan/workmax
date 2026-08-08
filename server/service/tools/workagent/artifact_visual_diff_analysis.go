package workagent

import (
	"bytes"
	"fmt"
	"image"
	"math"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
)

type ArtifactVisualDiffImageAnalysis struct {
	PreviousWidth         int                         `json:"previousWidth"`
	PreviousHeight        int                         `json:"previousHeight"`
	LatestWidth           int                         `json:"latestWidth"`
	LatestHeight          int                         `json:"latestHeight"`
	ComparedWidth         int                         `json:"comparedWidth"`
	ComparedHeight        int                         `json:"comparedHeight"`
	DimensionMismatch     bool                        `json:"dimensionMismatch"`
	ChangedPixelRatio     float64                     `json:"changedPixelRatio"`
	AverageLuminanceDelta float64                     `json:"averageLuminanceDelta"`
	Recommendation        string                      `json:"recommendation"`
	Hotspots              []ArtifactVisualDiffHotspot `json:"hotspots"`
}

func AnalyzeArtifactVisualDiffImages(previousContent, latestContent []byte) (ArtifactVisualDiffImageAnalysis, error) {
	previous, _, err := image.Decode(bytes.NewReader(previousContent))
	if err != nil {
		return ArtifactVisualDiffImageAnalysis{}, fmt.Errorf("decode previous visual diff image: %w", err)
	}
	latest, _, err := image.Decode(bytes.NewReader(latestContent))
	if err != nil {
		return ArtifactVisualDiffImageAnalysis{}, fmt.Errorf("decode latest visual diff image: %w", err)
	}

	prevBounds := previous.Bounds()
	latestBounds := latest.Bounds()
	comparedWidth := minInt(prevBounds.Dx(), latestBounds.Dx())
	comparedHeight := minInt(prevBounds.Dy(), latestBounds.Dy())
	if comparedWidth <= 0 || comparedHeight <= 0 {
		return ArtifactVisualDiffImageAnalysis{}, fmt.Errorf("visual diff image has empty comparable bounds")
	}

	var changed int
	var luminanceDelta float64
	total := comparedWidth * comparedHeight
	for y := 0; y < comparedHeight; y++ {
		for x := 0; x < comparedWidth; x++ {
			pr, pg, pb, _ := previous.At(prevBounds.Min.X+x, prevBounds.Min.Y+y).RGBA()
			lr, lg, lb, _ := latest.At(latestBounds.Min.X+x, latestBounds.Min.Y+y).RGBA()
			previousLuma := rgbaLuminance8(pr, pg, pb)
			latestLuma := rgbaLuminance8(lr, lg, lb)
			delta := math.Abs(previousLuma - latestLuma)
			luminanceDelta += delta
			if delta >= 12 {
				changed++
			}
		}
	}

	out := ArtifactVisualDiffImageAnalysis{
		PreviousWidth:         prevBounds.Dx(),
		PreviousHeight:        prevBounds.Dy(),
		LatestWidth:           latestBounds.Dx(),
		LatestHeight:          latestBounds.Dy(),
		ComparedWidth:         comparedWidth,
		ComparedHeight:        comparedHeight,
		DimensionMismatch:     prevBounds.Dx() != latestBounds.Dx() || prevBounds.Dy() != latestBounds.Dy(),
		ChangedPixelRatio:     float64(changed) / float64(total),
		AverageLuminanceDelta: luminanceDelta / float64(total) / 255,
	}
	out.Recommendation = visualDiffRecommendationForImageAnalysis(out)
	out.Hotspots = visualDiffHotspotsForImageAnalysis(out)
	return out, nil
}

func visualDiffRecommendationForImageAnalysis(analysis ArtifactVisualDiffImageAnalysis) string {
	if analysis.DimensionMismatch || analysis.ChangedPixelRatio >= 0.18 || analysis.AverageLuminanceDelta >= 0.12 {
		return "revise"
	}
	return "keep"
}

func visualDiffHotspotsForImageAnalysis(analysis ArtifactVisualDiffImageAnalysis) []ArtifactVisualDiffHotspot {
	hotspots := make([]ArtifactVisualDiffHotspot, 0, 2)
	if analysis.DimensionMismatch {
		hotspots = append(hotspots, ArtifactVisualDiffHotspot{
			Area:     "Canvas dimensions",
			Change:   fmt.Sprintf("Previous image is %dx%d, latest image is %dx%d.", analysis.PreviousWidth, analysis.PreviousHeight, analysis.LatestWidth, analysis.LatestHeight),
			Impact:   "Export size, responsive framing, or downstream handoff may have changed.",
			Severity: "high",
		})
	}
	severity := "low"
	if analysis.ChangedPixelRatio >= 0.18 || analysis.AverageLuminanceDelta >= 0.12 {
		severity = "high"
	} else if analysis.ChangedPixelRatio >= 0.04 || analysis.AverageLuminanceDelta >= 0.04 {
		severity = "medium"
	}
	hotspots = append(hotspots, ArtifactVisualDiffHotspot{
		Area:     "Rendered pixels",
		Change:   fmt.Sprintf("%.1f%% of comparable pixels changed; average luminance delta is %.1f%%.", analysis.ChangedPixelRatio*100, analysis.AverageLuminanceDelta*100),
		Impact:   "Use this as the first automatic signal before manual review of hierarchy, text, brand, and asset changes.",
		Severity: severity,
	})
	return hotspots
}

func rgbaLuminance8(r, g, b uint32) float64 {
	red := float64(r >> 8)
	green := float64(g >> 8)
	blue := float64(b >> 8)
	return 0.2126*red + 0.7152*green + 0.0722*blue
}
