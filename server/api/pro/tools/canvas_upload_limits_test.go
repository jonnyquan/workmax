package tools

import "testing"

// Pin the upload-limit policy lifted out of canvas_api.go: a
// 16MiB floor for both image and media, and image/* content
// types route to the image cap (everything else routes to
// media).

const floor16MiB = int64(16 * 1024 * 1024)

func TestMaxCanvasUploadSize_PicksLargest(t *testing.T) {
	cases := []struct {
		name string
		in   []int64
		want int64
	}{
		{"empty returns zero", nil, 0},
		{"single value", []int64{1024}, 1024},
		{"middle is max", []int64{1, 9, 3}, 9},
		{"negative ignored", []int64{-5, 7}, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maxCanvasUploadSize(tc.in...)
			if got != tc.want {
				t.Fatalf("maxCanvasUploadSize(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestCanvasAssetImageUploadSize_FloorIs16MiB(t *testing.T) {
	got := getCanvasAssetImageUploadSize()
	if got < floor16MiB {
		t.Fatalf("image upload size %d must be ≥ 16MiB floor %d", got, floor16MiB)
	}
}

func TestCanvasAssetMediaUploadSize_FloorIs16MiB(t *testing.T) {
	got := getCanvasAssetMediaUploadSize()
	if got < floor16MiB {
		t.Fatalf("media upload size %d must be ≥ 16MiB floor %d", got, floor16MiB)
	}
}

func TestCanvasAssetUploadBodyLimit_AtLeastBothCaps(t *testing.T) {
	body := getCanvasAssetUploadBodyLimit()
	if body < getCanvasAssetImageUploadSize() {
		t.Fatalf("body limit %d must be ≥ image cap %d", body, getCanvasAssetImageUploadSize())
	}
	if body < getCanvasAssetMediaUploadSize() {
		t.Fatalf("body limit %d must be ≥ media cap %d", body, getCanvasAssetMediaUploadSize())
	}
}

func TestCanvasAssetUploadSizeForContentType_DispatchesByMime(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		wantImage   bool
	}{
		{"png is image", "image/png", true},
		{"jpeg is image", "image/jpeg", true},
		{"uppercase image is image", "IMAGE/WEBP", true},
		{"whitespace-padded image is image", "  image/gif  ", true},
		{"video is media", "video/mp4", false},
		{"audio is media", "audio/mpeg", false},
		{"empty is media", "", false},
		{"unknown is media", "application/octet-stream", false},
	}
	imageCap := getCanvasAssetImageUploadSize()
	mediaCap := getCanvasAssetMediaUploadSize()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := getCanvasAssetUploadSizeForContentType(tc.contentType)
			want := mediaCap
			if tc.wantImage {
				want = imageCap
			}
			if got != want {
				t.Fatalf("getCanvasAssetUploadSizeForContentType(%q) = %d, want %d", tc.contentType, got, want)
			}
		})
	}
}
