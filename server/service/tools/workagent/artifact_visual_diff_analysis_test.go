package workagent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestAnalyzeArtifactVisualDiffImagesDetectsPixelChanges(t *testing.T) {
	previous := testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	})
	latest := testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
		if x < 2 {
			return color.RGBA{R: 20, G: 20, B: 20, A: 255}
		}
		return color.RGBA{R: 250, G: 250, B: 250, A: 255}
	})

	got, err := AnalyzeArtifactVisualDiffImages(previous, latest)
	if err != nil {
		t.Fatalf("AnalyzeArtifactVisualDiffImages: %v", err)
	}
	if got.PreviousWidth != 4 || got.LatestWidth != 4 || got.ComparedWidth != 4 || got.ComparedHeight != 4 {
		t.Fatalf("dimensions = %#v", got)
	}
	if got.DimensionMismatch {
		t.Fatalf("did not expect dimension mismatch: %#v", got)
	}
	if got.ChangedPixelRatio < 0.49 || got.ChangedPixelRatio > 0.51 {
		t.Fatalf("changed ratio = %f, want about 0.5", got.ChangedPixelRatio)
	}
	if got.AverageLuminanceDelta <= 0.4 {
		t.Fatalf("average luminance delta = %f, want meaningful change", got.AverageLuminanceDelta)
	}
	if got.Recommendation != "revise" {
		t.Fatalf("recommendation = %q, want revise", got.Recommendation)
	}
	if len(got.Hotspots) != 1 || got.Hotspots[0].Area != "Rendered pixels" || got.Hotspots[0].Severity != "high" {
		t.Fatalf("hotspots = %#v", got.Hotspots)
	}
}

func TestAnalyzeArtifactVisualDiffImagesDetectsDimensionMismatch(t *testing.T) {
	previous := testVisualDiffPNG(t, 4, 4, func(x, y int) color.Color {
		return color.RGBA{R: 120, G: 120, B: 120, A: 255}
	})
	latest := testVisualDiffPNG(t, 6, 4, func(x, y int) color.Color {
		return color.RGBA{R: 120, G: 120, B: 120, A: 255}
	})

	got, err := AnalyzeArtifactVisualDiffImages(previous, latest)
	if err != nil {
		t.Fatalf("AnalyzeArtifactVisualDiffImages: %v", err)
	}
	if !got.DimensionMismatch || got.ComparedWidth != 4 || got.ComparedHeight != 4 {
		t.Fatalf("dimension analysis = %#v", got)
	}
	if got.Recommendation != "revise" {
		t.Fatalf("recommendation = %q, want revise", got.Recommendation)
	}
	if len(got.Hotspots) < 2 || got.Hotspots[0].Area != "Canvas dimensions" {
		t.Fatalf("hotspots = %#v, want dimension hotspot first", got.Hotspots)
	}
}

func TestAnalyzeArtifactVisualDiffImagesRejectsInvalidImages(t *testing.T) {
	latest := testVisualDiffPNG(t, 2, 2, func(x, y int) color.Color {
		return color.RGBA{R: 255, G: 255, B: 255, A: 255}
	})

	_, err := AnalyzeArtifactVisualDiffImages([]byte("not an image"), latest)
	if err == nil || !strings.Contains(err.Error(), "decode previous visual diff image") {
		t.Fatalf("error = %v, want previous decode error", err)
	}
}

func testVisualDiffPNG(t *testing.T, width, height int, pixel func(x, y int) color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, pixel(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
