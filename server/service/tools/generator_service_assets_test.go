package tools

import "testing"

func TestDetectStoredAssetExtensionSupportsMeshFormats(t *testing.T) {
	tests := []struct {
		name        string
		sourceURL   string
		contentType string
		assetKind   string
		expected    string
	}{
		{
			name:        "glb content type",
			contentType: "model/gltf-binary",
			assetKind:   "asset",
			expected:    ".glb",
		},
		{
			name:      "obj url fallback",
			sourceURL: "https://example.com/model.obj?download=1",
			assetKind: "asset",
			expected:  ".obj",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectStoredAssetExtension(tc.sourceURL, tc.contentType, tc.assetKind); got != tc.expected {
				t.Fatalf("detectStoredAssetExtension() = %q, want %q", got, tc.expected)
			}
		})
	}
}
