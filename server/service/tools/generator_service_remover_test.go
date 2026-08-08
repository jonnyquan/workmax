package tools

import (
	"server/model"
	"testing"
)

func TestNearestAspectRatioForDimensions(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		want   string
	}{
		{name: "square", width: 1200, height: 1200, want: "1:1"},
		{name: "landscape photo", width: 1600, height: 900, want: "16:9"},
		{name: "portrait photo", width: 900, height: 1600, want: "9:16"},
		{name: "four five", width: 1200, height: 1500, want: "4:5"},
		{name: "invalid falls back", width: 0, height: 1200, want: "1:1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nearestAspectRatioForDimensions(tt.width, tt.height); got != tt.want {
				t.Fatalf("nearestAspectRatioForDimensions(%d, %d) = %s, want %s", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

func TestFirstRemoverSourceURLPrefersReferenceImage(t *testing.T) {
	req := &GenerateImageRequest{
		ReferenceImages: []model.ReferenceImageParam{
			{URL: "https://example.com/ref.png"},
		},
		RawRequestData: model.JSONMap{
			"imageUrl": "https://example.com/source.png",
		},
	}

	if got := firstRemoverSourceURL(req); got != "https://example.com/ref.png" {
		t.Fatalf("firstRemoverSourceURL returned %q", got)
	}
}
