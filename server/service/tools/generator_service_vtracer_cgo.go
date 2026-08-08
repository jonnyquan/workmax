//go:build cgo

package tools

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"strings"

	vtracer "github.com/yclw/go-vtracer"
)

// buildVTracerConfig maps our vectorizerPostProcessConfig to go-vtracer Config.
func buildVTracerConfig(cfg vectorizerPostProcessConfig) *vtracer.Config {
	vcfg := vtracer.DefaultConfig()
	detail := normalizeVectorizerDetailLevel(cfg.DetailLevel)
	colorCount := clampVectorizerColorCount(cfg.Colors)

	// Color mode
	if normalizeVectorizerColorMode(cfg.ColorMode) == "monochrome" {
		vcfg.ColorMode = vtracer.ColorModeBinary
	} else {
		vcfg.ColorMode = vtracer.ColorModeColor
	}

	// Photo-friendly defaults: preserve more layers and geometry before applying
	// detail-level simplification. This trades SVG size for closer resemblance.
	vcfg.Mode = vtracer.PathModeSpline
	vcfg.Hierarchical = vtracer.HierarchicalCutout
	vcfg.FilterSpeckle = 2
	vcfg.ColorPrecision = 8
	vcfg.LayerDifference = max(1, 256/colorCount)
	vcfg.CornerThreshold = 50
	vcfg.LengthThreshold = 3.5
	vcfg.MaxIterations = 10
	vcfg.SpliceThreshold = 35
	vcfg.PathPrecision = 4

	// Detail level → simplification strength
	switch detail {
	case "low":
		vcfg.Hierarchical = vtracer.HierarchicalStacked
		vcfg.FilterSpeckle = 6
		vcfg.ColorPrecision = 4
		vcfg.Mode = vtracer.PathModePolygon
		vcfg.LayerDifference = max(vcfg.LayerDifference, 24)
		vcfg.CornerThreshold = 85
		vcfg.LengthThreshold = 6.5
		vcfg.MaxIterations = 4
		vcfg.SpliceThreshold = 65
		vcfg.PathPrecision = 2
	case "high":
		vcfg.FilterSpeckle = 1
		vcfg.ColorPrecision = 8
		vcfg.Mode = vtracer.PathModeSpline
		vcfg.LayerDifference = max(1, min(vcfg.LayerDifference, 10))
		vcfg.CornerThreshold = 40
		vcfg.LengthThreshold = 3.5
		vcfg.MaxIterations = 14
		vcfg.SpliceThreshold = 25
		vcfg.PathPrecision = 5
	default: // medium
		vcfg.FilterSpeckle = 2
		vcfg.ColorPrecision = 7
		vcfg.Mode = vtracer.PathModeSpline
		vcfg.LayerDifference = max(1, min(vcfg.LayerDifference, 16))
		vcfg.CornerThreshold = 50
		vcfg.LengthThreshold = 4.0
		vcfg.MaxIterations = 10
		vcfg.SpliceThreshold = 35
		vcfg.PathPrecision = 4
	}

	// High fidelity: preserve more small regions and curve detail.
	if cfg.HighFidelity {
		if vcfg.ColorPrecision < 8 {
			vcfg.ColorPrecision = vcfg.ColorPrecision + 1
			if vcfg.ColorPrecision > 8 {
				vcfg.ColorPrecision = 8
			}
		}
		if vcfg.FilterSpeckle > 1 {
			vcfg.FilterSpeckle = vcfg.FilterSpeckle / 2
			if vcfg.FilterSpeckle < 1 {
				vcfg.FilterSpeckle = 1
			}
		}
		if vcfg.LayerDifference > 6 {
			vcfg.LayerDifference = max(1, vcfg.LayerDifference/2)
		}
		vcfg.LengthThreshold = 3.5
		if vcfg.CornerThreshold > 35 {
			vcfg.CornerThreshold = 35
		}
		if vcfg.SpliceThreshold > 20 {
			vcfg.SpliceThreshold = 20
		}
		if vcfg.MaxIterations < 16 {
			vcfg.MaxIterations = 16
		}
		vcfg.Mode = vtracer.PathModeSpline
		vcfg.Hierarchical = vtracer.HierarchicalCutout
		if vcfg.PathPrecision < 5 {
			vcfg.PathPrecision = 5
		}
	}

	// Fixed "best quality" profile for vectorizer: prefer fidelity over compactness.
	// This intentionally generates heavier SVGs so the output stays closer to the source.
	if detail == "high" && cfg.HighFidelity && colorCount >= 48 {
		vcfg.Mode = vtracer.PathModeNone
		vcfg.Hierarchical = vtracer.HierarchicalCutout
		vcfg.FilterSpeckle = 1
		vcfg.ColorPrecision = 8
		vcfg.LayerDifference = 3
		vcfg.CornerThreshold = 25
		vcfg.LengthThreshold = 3.5
		vcfg.MaxIterations = 24
		vcfg.SpliceThreshold = 8
		vcfg.PathPrecision = 6
	}

	// Color count → layer difference mapping. Higher requested color count means
	// lower layer difference and less aggressive merging.
	switch {
	case colorCount >= 48:
		vcfg.LayerDifference = min(vcfg.LayerDifference, 5)
	case colorCount >= 32:
		vcfg.LayerDifference = min(vcfg.LayerDifference, 8)
	case colorCount >= 16:
		vcfg.LayerDifference = min(vcfg.LayerDifference, 14)
	default:
		vcfg.LayerDifference = max(vcfg.LayerDifference, 20)
	}

	if detail == "high" && cfg.HighFidelity && colorCount >= 48 {
		vcfg.LayerDifference = 3
	}

	return vcfg
}

func medianUint8(values []uint8) uint8 {
	sorted := append([]uint8(nil), values...)
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}
	return sorted[len(sorted)/2]
}

func preprocessVectorizerImageForVTracer(img image.Image, cfg vectorizerPostProcessConfig) image.Image {
	if img == nil {
		return img
	}
	if !cfg.HighFidelity {
		return img
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width < 8 || height < 8 {
		return img
	}

	src := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			src.Set(x, y, img.At(x, y))
		}
	}

	dst := image.NewRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			center := src.RGBAAt(x, y)
			if x == bounds.Min.X || x == bounds.Max.X-1 || y == bounds.Min.Y || y == bounds.Max.Y-1 {
				dst.SetRGBA(x, y, center)
				continue
			}

			rs := make([]uint8, 0, 9)
			gs := make([]uint8, 0, 9)
			bs := make([]uint8, 0, 9)
			for ky := -1; ky <= 1; ky++ {
				for kx := -1; kx <= 1; kx++ {
					px := src.RGBAAt(x+kx, y+ky)
					rs = append(rs, px.R)
					gs = append(gs, px.G)
					bs = append(bs, px.B)
				}
			}

			dst.SetRGBA(x, y, color.RGBA{
				R: medianUint8(rs),
				G: medianUint8(gs),
				B: medianUint8(bs),
				A: center.A,
			})
		}
	}

	return dst
}

// rasterToVectorizedSVGViaVTracer uses the go-vtracer library for high-quality vectorization.
// This implementation is only available when CGO is enabled.
func rasterToVectorizedSVGViaVTracer(imageData []byte, cfg vectorizerPostProcessConfig) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("vtracer: failed to decode raster image: %w", err)
	}

	img = preprocessVectorizerImageForVTracer(img, cfg)
	vcfg := buildVTracerConfig(cfg)
	svgStr, err := vtracer.ConvertImage(img, vcfg)
	if err != nil {
		return nil, fmt.Errorf("vtracer: conversion failed: %w", err)
	}

	if strings.TrimSpace(svgStr) == "" {
		return nil, fmt.Errorf("vtracer: produced empty SVG")
	}

	return []byte(svgStr), nil
}
