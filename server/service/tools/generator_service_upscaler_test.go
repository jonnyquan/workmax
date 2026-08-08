package tools

import (
	"server/model"
	"testing"
)

func TestFirstUpscalerSourceURLPrefersReferenceImage(t *testing.T) {
	req := &GenerateImageRequest{
		ReferenceImages: []model.ReferenceImageParam{
			{URL: "https://example.com/ref.png"},
		},
		RawRequestData: model.JSONMap{
			"imageUrl": "https://example.com/source.png",
		},
	}

	if got := firstUpscalerSourceURL(req); got != "https://example.com/ref.png" {
		t.Fatalf("firstUpscalerSourceURL returned %q", got)
	}
}
