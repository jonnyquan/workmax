package tools

import (
	"server/model"
	"testing"
)

func TestSanitizeVideoReferenceMediaAcceptsUploadedReference(t *testing.T) {
	items, err := sanitizeVideoReferenceMedia([]VideoReferenceMedia{{
		ID:       "ref-123",
		URL:      "/uploads/reference-videos/uid/42/2026/04/29/ref-123.mp4",
		MimeType: "video/mp4",
		FileName: "ref-123.mp4",
	}}, "reference-videos", allowedReferenceVideoTypes, 42)
	if err != nil {
		t.Fatalf("sanitizeVideoReferenceMedia() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("sanitizeVideoReferenceMedia() len = %d, want 1", len(items))
	}
	if items[0].URL != "/uploads/reference-videos/uid/42/2026/04/29/ref-123.mp4" {
		t.Fatalf("sanitizeVideoReferenceMedia() URL = %q", items[0].URL)
	}
}

func TestSanitizeVideoReferenceMediaRejectsUnsafeURL(t *testing.T) {
	cases := []string{
		"javascript:alert(1)",
		"file:///tmp/ref.mp4",
		"http://example.com/ref.mp4",
		"https://evil.example/uploads/reference-videos/uid/42/ref.mp4",
		"/uploads/reference-audios/uid/42/ref.mp4",
		"/uploads/reference-videos/../ref.mp4",
	}
	for _, rawURL := range cases {
		_, err := sanitizeVideoReferenceMedia([]VideoReferenceMedia{{
			URL:      rawURL,
			MimeType: "video/mp4",
		}}, "reference-videos", allowedReferenceVideoTypes, 42)
		if err == nil {
			t.Fatalf("sanitizeVideoReferenceMedia(%q) expected error", rawURL)
		}
	}
}

func TestResolveUploadedContentTypeRejectsForgedMediaType(t *testing.T) {
	if _, ok := resolveUploadedContentType([]byte("not actually a video"), "video/mp4", allowedReferenceVideoTypes); ok {
		t.Fatal("resolveUploadedContentType() accepted forged video/mp4 content")
	}
}

func TestResolveUploadedContentTypeAcceptsSniffedMP4(t *testing.T) {
	mp4Header := []byte{
		0x00, 0x00, 0x00, 0x18,
		'f', 't', 'y', 'p',
		'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00,
	}
	contentType, ok := resolveUploadedContentType(mp4Header, "video/mp4", allowedReferenceVideoTypes)
	if !ok {
		t.Fatal("resolveUploadedContentType() rejected valid mp4 header")
	}
	if contentType != "video/mp4" {
		t.Fatalf("resolveUploadedContentType() contentType = %q, want video/mp4", contentType)
	}
}

func TestSanitizeVideoReferenceMediaRejectsInvalidFields(t *testing.T) {
	cases := []VideoReferenceMedia{
		{
			URL:      "/uploads/reference-videos/uid/42/ref.mp4",
			MimeType: "text/plain",
		},
		{
			ID:       "../ref",
			URL:      "/uploads/reference-videos/uid/42/ref.mp4",
			MimeType: "video/mp4",
		},
		{
			URL:      "/uploads/reference-videos/uid/42/ref.mp4",
			MimeType: "video/mp4",
			FileName: "../ref.mp4",
		},
	}
	for _, item := range cases {
		_, err := sanitizeVideoReferenceMedia([]VideoReferenceMedia{item}, "reference-videos", allowedReferenceVideoTypes, 42)
		if err == nil {
			t.Fatalf("sanitizeVideoReferenceMedia(%+v) expected error", item)
		}
	}
}

func TestSanitizeVideoReferenceMediaRejectsTooManyItems(t *testing.T) {
	items := []VideoReferenceMedia{
		{URL: "/uploads/reference-videos/uid/42/1.mp4", MimeType: "video/mp4"},
		{URL: "/uploads/reference-videos/uid/42/2.mp4", MimeType: "video/mp4"},
		{URL: "/uploads/reference-videos/uid/42/3.mp4", MimeType: "video/mp4"},
		{URL: "/uploads/reference-videos/uid/42/4.mp4", MimeType: "video/mp4"},
	}
	_, err := sanitizeVideoReferenceMedia(items, "reference-videos", allowedReferenceVideoTypes, 42)
	if err == nil {
		t.Fatal("sanitizeVideoReferenceMedia() expected error for too many items")
	}
}

func TestSanitizeVideoReferenceMediaRejectsOtherUserURL(t *testing.T) {
	_, err := sanitizeVideoReferenceMedia([]VideoReferenceMedia{{
		URL:      "/uploads/reference-videos/uid/43/ref.mp4",
		MimeType: "video/mp4",
	}}, "reference-videos", allowedReferenceVideoTypes, 42)
	if err == nil {
		t.Fatal("sanitizeVideoReferenceMedia() expected owner mismatch error")
	}
}

func TestSanitizeVideoGeneratorReferenceImagesRejectsExternalURL(t *testing.T) {
	_, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "ref-image",
		URL:    "https://evil.example/ref.png",
		Weight: 1,
	}}, 42)
	if err == nil {
		t.Fatal("sanitizeVideoGeneratorReferenceImages() expected external URL error")
	}
}

func TestSanitizeVideoGeneratorReferenceImagesAcceptsOwnedUploadURL(t *testing.T) {
	items, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "ref-image",
		URL:    "/uploads/references/uid/42/2026/05/05/ref.png",
		Weight: 1,
	}}, 42)
	if err != nil {
		t.Fatalf("sanitizeVideoGeneratorReferenceImages() unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("sanitizeVideoGeneratorReferenceImages() len = %d, want 1", len(items))
	}
}

func TestSanitizeVideoGeneratorReferenceImagesRejectsOtherUserURL(t *testing.T) {
	_, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "ref-image",
		URL:    "/uploads/references/uid/43/2026/05/05/ref.png",
		Weight: 1,
	}}, 42)
	if err == nil {
		t.Fatal("sanitizeVideoGeneratorReferenceImages() expected owner mismatch error")
	}
}

func TestSanitizeVideoGeneratorReferenceImagesRejectsLegacyPathWithoutUID(t *testing.T) {
	_, err := sanitizeVideoGeneratorReferenceImages([]ReferenceImageInput{{
		ID:     "legacy-ref",
		URL:    "/uploads/generations/reference-images/legacy.png",
		Weight: 1,
	}}, 42)
	if err == nil {
		t.Fatal("sanitizeVideoGeneratorReferenceImages() expected missing uid error")
	}
}

func TestNormalizeVideoGenericReferenceImageIDsAvoidsReservedFrameIDs(t *testing.T) {
	items := []ReferenceImageInput{
		{ID: "start-frame", URL: "/uploads/references/uid/42/a.png", Weight: 1},
		{ID: "video-end-frame", URL: "/uploads/references/uid/42/b.png", Weight: 1},
		{ID: "character", URL: "/uploads/references/uid/42/c.png", Weight: 1},
		{ID: "", URL: "/uploads/references/uid/42/d.png", Weight: 1},
	}
	normalizeVideoGenericReferenceImageIDs(items)
	if items[0].ID == "start-frame" || items[1].ID == "video-end-frame" {
		t.Fatalf("reserved generic ids were not normalized: %#v", items)
	}
	if items[2].ID != "character" {
		t.Fatalf("non-reserved generic id changed: %q", items[2].ID)
	}
	if items[3].ID == "" {
		t.Fatal("empty generic id was not normalized")
	}
}

func TestSupportsGenericReferencesWithExplicitFrames(t *testing.T) {
	if !supportsGenericReferencesWithExplicitFrames(model.SEEDANCE_2) {
		t.Fatal("Seedance 2 should allow explicit frames plus generic references")
	}
	if supportsGenericReferencesWithExplicitFrames(model.KLING_2_6) {
		t.Fatal("Kling should reject explicit frames plus generic references to avoid silently dropping refs")
	}
}
