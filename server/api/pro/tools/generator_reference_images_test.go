package tools

import (
	"testing"

	"server/model"
	"server/utils/testutil"
)

func TestSanitizeGeneratorReferenceImages(t *testing.T) {
	out, err := sanitizeGeneratorReferenceImages([]ReferenceImageInput{
		{ID: "ref-1", URL: " /uploads/generations/reference-images/uid/42/a.png ", Weight: 9},
		{ID: "empty", URL: "   ", Weight: 1},
	})
	if err != nil {
		t.Fatalf("sanitizeGeneratorReferenceImages returned error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 sanitized reference image, got %d", len(out))
	}
	if out[0].URL != "/uploads/generations/reference-images/uid/42/a.png" {
		t.Fatalf("unexpected url: %q", out[0].URL)
	}
	if out[0].Weight != 2 {
		t.Fatalf("expected weight to be clamped to 2, got %v", out[0].Weight)
	}
}

func TestSanitizeGeneratorReferenceImagesRejectsUnsafeInput(t *testing.T) {
	tests := []struct {
		name  string
		input []ReferenceImageInput
	}{
		{
			name:  "too many",
			input: []ReferenceImageInput{{URL: "/uploads/a.png"}, {URL: "/uploads/b.png"}, {URL: "/uploads/c.png"}, {URL: "/uploads/d.png"}, {URL: "/uploads/e.png"}},
		},
		{
			name:  "untrusted relative path",
			input: []ReferenceImageInput{{URL: "/api/internal/secret.png"}},
		},
		{
			name:  "control rune",
			input: []ReferenceImageInput{{URL: "/uploads/a.png\n"}},
		},
		{
			name:  "unsupported scheme",
			input: []ReferenceImageInput{{URL: "file:///tmp/a.png"}},
		},
		{
			name:  "external absolute url",
			input: []ReferenceImageInput{{URL: "https://evil.example.com/a.png"}},
		},
		{
			name:  "external host with uploaded path",
			input: []ReferenceImageInput{{URL: "https://evil.example.com/uploads/references/uid/42/a.png"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := sanitizeGeneratorReferenceImages(tt.input); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func TestResolveGeneratorReferenceImagesAcceptsOwnedAssetID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := model.GlobalAsset{
		UID:           42,
		UUID:          "asset-42",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      1,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/project-a/a.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	out, err := resolveGeneratorReferenceImages([]ReferenceImageInput{{
		ID:      "ref-1",
		AssetID: asset.Id,
		Weight:  0,
	}}, 42)
	if err != nil {
		t.Fatalf("resolveGeneratorReferenceImages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].AssetID != asset.Id || out[0].URL != asset.URL {
		t.Fatalf("resolved ref mismatch: %+v", out[0])
	}
	if out[0].Weight != 1 {
		t.Fatalf("weight = %v, want 1", out[0].Weight)
	}
}

func TestResolveGeneratorReferenceImagesRejectsOtherUserAssetID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	asset := model.GlobalAsset{
		UID:           42,
		UUID:          "asset-42",
		Kind:          model.GlobalAssetKindImage,
		Source:        model.GlobalAssetSourceUpload,
		SourceTable:   "canvas_project_file",
		SourceID:      2,
		SourceItemKey: "asset",
		URL:           "/uploads/canvas/uid/42/p/a.png",
		Status:        model.GlobalAssetStatusActive,
		Visibility:    model.GlobalAssetVisibilityPrivate,
		VariantType:   model.GlobalAssetVariantOriginal,
	}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}

	if _, err := resolveGeneratorReferenceImages([]ReferenceImageInput{{AssetID: asset.Id}}, 99); err == nil {
		t.Fatal("expected cross-user asset rejection")
	}
}

func TestResolveGeneratorReferenceImagesRejectsOtherUserURL(t *testing.T) {
	if _, err := resolveGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "ref-1",
		URL:    "/uploads/references/uid/43/2026/05/14/ref.png",
		Weight: 1,
	}}, 42); err == nil {
		t.Fatal("expected cross-user url rejection")
	}
}

func TestResolveGeneratorReferenceImagesRejectsLegacyURLWithoutUID(t *testing.T) {
	if _, err := resolveGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "ref-1",
		URL:    "/uploads/legacy/ref.png",
		Weight: 1,
	}}, 42); err == nil {
		t.Fatal("expected legacy url without uid rejection")
	}
}

func TestResolveGeneratorReferenceImagesRejectsRetiredClientAssetURLs(t *testing.T) {
	for _, rawURL := range []string{
		"/images/tools/example.png",
		"/_next/image?url=%2Fimages%2Ftools%2Fexample.png&w=640&q=75",
	} {
		if _, err := resolveGeneratorReferenceImages([]ReferenceImageInput{{
			ID:     "example",
			URL:    rawURL,
			Weight: 1,
		}}, 42); err == nil {
			t.Fatalf("expected retired client URL %q to be rejected", rawURL)
		}
	}
}
