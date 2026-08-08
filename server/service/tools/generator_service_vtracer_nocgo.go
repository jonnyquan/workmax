//go:build !cgo

package tools

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func vtracerCLIArgs(cfg vectorizerPostProcessConfig, inputPath, outputPath string) []string {
	colormode := "color"
	if normalizeVectorizerColorMode(cfg.ColorMode) == "monochrome" {
		colormode = "bw"
	}

	hierarchical := "cutout"
	if normalizeVectorizerDetailLevel(cfg.DetailLevel) == "low" {
		hierarchical = "stacked"
	}

	mode := "spline"
	switch normalizeVectorizerDetailLevel(cfg.DetailLevel) {
	case "low":
		mode = "polygon"
	case "high":
		if cfg.HighFidelity {
			mode = "pixel"
		}
	}

	colorPrecision := 8
	filterSpeckle := 1
	layerDifference := 3
	cornerThreshold := 25
	lengthThreshold := "3.5"
	spliceThreshold := 8
	pathPrecision := 6

	if !cfg.HighFidelity {
		filterSpeckle = 2
		layerDifference = 8
		cornerThreshold = 40
		spliceThreshold = 20
		pathPrecision = 5
	}

	args := []string{
		"--input", inputPath,
		"--output", outputPath,
		"--colormode", colormode,
		"--hierarchical", hierarchical,
		"--mode", mode,
		"--color_precision", fmt.Sprintf("%d", colorPrecision),
		"--filter_speckle", fmt.Sprintf("%d", filterSpeckle),
		"--gradient_step", fmt.Sprintf("%d", layerDifference),
		"--corner_threshold", fmt.Sprintf("%d", cornerThreshold),
		"--segment_length", lengthThreshold,
		"--splice_threshold", fmt.Sprintf("%d", spliceThreshold),
		"--path_precision", fmt.Sprintf("%d", pathPrecision),
	}

	if normalizeVectorizerDetailLevel(cfg.DetailLevel) == "high" && cfg.HighFidelity {
		args = append(args, "--preset", "photo")
	}

	return args
}

// rasterToVectorizedSVGViaVTracer is a stub for non-CGO builds.
// VTracer requires CGO; when CGO is disabled, this returns an error
// so that vectorizeImageData falls back to the legacy engine.
func rasterToVectorizedSVGViaVTracer(imageData []byte, cfg vectorizerPostProcessConfig) ([]byte, error) {
	bin, err := exec.LookPath("vtracer")
	if err != nil {
		return nil, fmt.Errorf("vtracer: not available (CGO disabled and vtracer CLI not installed)")
	}

	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("vtracer: failed to decode raster image: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "nanobanana-vtracer-*")
	if err != nil {
		return nil, fmt.Errorf("vtracer: create temp dir failed: %w", err)
	}
	defer os.RemoveAll(tempDir)

	inputPath := filepath.Join(tempDir, "input.png")
	outputPath := filepath.Join(tempDir, "output.svg")

	inputFile, err := os.Create(inputPath)
	if err != nil {
		return nil, fmt.Errorf("vtracer: create temp input failed: %w", err)
	}
	if err := png.Encode(inputFile, img); err != nil {
		inputFile.Close()
		return nil, fmt.Errorf("vtracer: encode temp input failed: %w", err)
	}
	if err := inputFile.Close(); err != nil {
		return nil, fmt.Errorf("vtracer: finalize temp input failed: %w", err)
	}

	cmd := exec.Command(bin, vtracerCLIArgs(cfg, inputPath, outputPath)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg != "" {
			return nil, fmt.Errorf("vtracer: cli conversion failed: %s", msg)
		}
		return nil, fmt.Errorf("vtracer: cli conversion failed: %w", err)
	}

	svgData, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("vtracer: read output failed: %w", err)
	}
	if len(strings.TrimSpace(string(svgData))) == 0 {
		return nil, fmt.Errorf("vtracer: cli produced empty SVG")
	}

	return svgData, nil
}
