package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestReduceVectorLoopPointsKeepsClosedShape(t *testing.T) {
	raw := []vectorGridPoint{
		{X: 0, Y: 0},
		{X: 1, Y: 0},
		{X: 2, Y: 0},
		{X: 3, Y: 0},
		{X: 4, Y: 0},
		{X: 4, Y: 1},
		{X: 4, Y: 2},
		{X: 4, Y: 3},
		{X: 4, Y: 4},
		{X: 3, Y: 4},
		{X: 2, Y: 4},
		{X: 1, Y: 4},
		{X: 0, Y: 4},
		{X: 0, Y: 3},
		{X: 0, Y: 2},
		{X: 0, Y: 1},
		{X: 0, Y: 0},
	}

	reduced := reduceVectorLoopPoints(raw, "medium", false)
	if len(reduced) < 5 {
		t.Fatalf("expected at least 5 points in closed rectangle, got %d", len(reduced))
	}
	if reduced[0] != reduced[len(reduced)-1] {
		t.Fatalf("expected closed loop, got start=%v end=%v", reduced[0], reduced[len(reduced)-1])
	}
}

func TestBuildVectorPathDataUsesSmoothCommandsForMedium(t *testing.T) {
	loop := []vectorGridPoint{
		{X: 0, Y: 0},
		{X: 4, Y: 0},
		{X: 4, Y: 4},
		{X: 0, Y: 4},
		{X: 0, Y: 0},
	}

	linearPath := buildVectorPathData([][]vectorGridPoint{loop}, "low", 0, false)
	if strings.Contains(linearPath, "Q") {
		t.Fatalf("expected low detail path to stay linear, got: %s", linearPath)
	}
	if !strings.Contains(linearPath, "L") {
		t.Fatalf("expected linear path to contain L command, got: %s", linearPath)
	}

	smoothPath := buildVectorPathData([][]vectorGridPoint{loop}, "medium", 0, false)
	if !strings.Contains(smoothPath, "Q") {
		t.Fatalf("expected medium detail path to contain Q command, got: %s", smoothPath)
	}
}

func TestBuildVectorPathDataFiltersTinyLoops(t *testing.T) {
	tinyLoop := []vectorGridPoint{
		{X: 1, Y: 1},
		{X: 2, Y: 1},
		{X: 2, Y: 2},
		{X: 1, Y: 2},
		{X: 1, Y: 1},
	}

	path := buildVectorPathData([][]vectorGridPoint{tinyLoop}, "medium", 10, false)
	if strings.TrimSpace(path) != "" {
		t.Fatalf("expected tiny loop to be filtered, got: %s", path)
	}
}

func TestNormalizeVectorizerColorModeAlwaysUsesColor(t *testing.T) {
	if mode := normalizeVectorizerColorMode("monochrome"); mode != "color" {
		t.Fatalf("expected monochrome input to normalize to color, got %s", mode)
	}
	if mode := normalizeVectorizerColorMode("black-white"); mode != "color" {
		t.Fatalf("expected black-white input to normalize to color, got %s", mode)
	}
}

func TestEncodeRasterPreviewPNGNormalizesPreviewImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.Set(0, 0, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	src.Set(1, 0, color.NRGBA{R: 0, G: 255, B: 0, A: 255})
	src.Set(0, 1, color.NRGBA{R: 0, G: 0, B: 255, A: 255})
	src.Set(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})

	var input bytes.Buffer
	if err := png.Encode(&input, src); err != nil {
		t.Fatalf("encode source png: %v", err)
	}

	preview, err := encodeRasterPreviewPNG(input.Bytes())
	if err != nil {
		t.Fatalf("encodeRasterPreviewPNG returned error: %v", err)
	}

	decoded, _, err := image.Decode(bytes.NewReader(preview))
	if err != nil {
		t.Fatalf("preview decode failed: %v", err)
	}
	if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 2 {
		t.Fatalf("unexpected preview size: %v", decoded.Bounds())
	}
}
