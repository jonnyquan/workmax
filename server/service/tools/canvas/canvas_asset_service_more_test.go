package canvas

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"server/globals"
	"server/model"
)

// canvas_asset_service_test.go pins every obvious branch of the pure
// helpers (NormalizeAssetKind, ClassifyUploadedContent, the keyframe
// rejecters, the above/below clamps, the LocalAssetStorage happy path).
// These fill in the boundary + drift-guard branches whose regressions
// would silently pass the existing suite:
//
//   • NormalizeKeyframeRequest at the EXACT inclusive boundaries (0.1
//     and 120.0). Existing tests pin "below" and "above"; the clamp
//     uses `< 0.1` and `> 120` so exact boundary values must pass
//     through unchanged. A regression tightening to `<=` / `>=` would
//     turn a legitimate 120s keyframe into 120s (unchanged in value
//     but a silent clamp) — and 0.1 would snap to the same — the real
//     worry is the opposite tightening that would exclude the
//     literal endpoints used as UI presets.
//   • normalizeListAssetsInput Limit == 200 exact boundary — the cap
//     uses `> 200`. Existing suite only tests >200. Pin the inclusive
//     boundary so a `>= 200` drift would surface.
//   • normalizeListAssetsInput Limit == 1 (floor). Existing tests pin
//     defaults-when-zero but not the 1-row poll.
//   • normalizeListAssetsInput default constant values (50, 200) — pin
//     the numeric defaults against drift. These are documented contract
//     in the comment; a change here is breaking for any client that
//     expects the handler to cap at 200.
//   • GenerateAssetFilename monotonicity — two calls produce different
//     filenames (time.Now().UnixNano() moves forward). A regression that
//     swapped to a static token or a per-second timestamp would hit
//     this (or a noop like `asset_%d` vs asset_ prefix).
//   • LocalAssetStorage.Store path contains the canonical segments:
//     `canvas/uid/{uid}/{projectUUID}/{filename}` in forward slash form
//     — the returned URL is exposed to browsers. Existing test pins
//     write-succeeds and cleanup but does not pin the URL shape.
//   • AllowedUploadContentTypes enumerates exactly 6 entries (3 image,
//     1 video, 1 PDF, 1 gif) and no others — a regression that added a
//     disallowed type (e.g. image/bmp) would surface here as a loud
//     test failure rather than slip in via a handler whitelist bypass.

func TestNormalizeKeyframeRequest_TimestampExactlyAtFloorPassesThrough(t *testing.T) {
	// The lower bound is 0.1 (inclusive — clamp uses `< 0.1`). An
	// explicit 0.1 must round-trip unchanged so a UI preset for
	// "start of video" doesn't shift silently.
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", 0.1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 0.1 {
		t.Errorf("Timestamp = %v, want 0.1 (inclusive floor)", got.Timestamp)
	}
}

func TestNormalizeKeyframeRequest_TimestampExactlyAtCeilingPassesThrough(t *testing.T) {
	// Upper bound uses `> 120`. Exact 120 must round-trip.
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", 120.0)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 120.0 {
		t.Errorf("Timestamp = %v, want 120.0 (inclusive ceiling)", got.Timestamp)
	}
}

func TestNormalizeListAssetsInput_LimitAtMaxBoundaryPassesThrough(t *testing.T) {
	// The cap is `> 200`; 200 must pass through untouched. A tightening
	// to `>= 200` would silently clamp 200 → 200 (no numeric change)
	// but the != assertion would catch any future reduction in the
	// cap value itself.
	out := normalizeListAssetsInput(ListAssetsInput{Page: 1, Limit: 200})
	if out.Limit != 200 {
		t.Errorf("Limit=200: got %d, want 200 (inclusive boundary)", out.Limit)
	}
}

func TestNormalizeListAssetsInput_LimitOneIsLegal(t *testing.T) {
	// Smallest valid paging window — a "peek latest asset" probe. Pin
	// passthrough so a regression that used `< defaultLimit` would
	// surface here (it would inflate Limit=1 into 50).
	out := normalizeListAssetsInput(ListAssetsInput{Page: 1, Limit: 1})
	if out.Limit != 1 {
		t.Errorf("Limit=1: got %d, want 1", out.Limit)
	}
}

func TestNormalizeListAssetsInput_DefaultsPinContract(t *testing.T) {
	// The comment on normalizeListAssetsInput documents "default 50,
	// max 200". Pin both numeric defaults so a change to either would
	// show up as an intentional contract revision rather than silent drift.
	zeroed := normalizeListAssetsInput(ListAssetsInput{})
	if zeroed.Limit != 50 {
		t.Errorf("default Limit = %d, want 50", zeroed.Limit)
	}
	capped := normalizeListAssetsInput(ListAssetsInput{Limit: 99999})
	if capped.Limit != 200 {
		t.Errorf("max Limit = %d, want 200", capped.Limit)
	}
}

func TestGenerateAssetFilename_IsMonotonicAcrossConsecutiveCalls(t *testing.T) {
	// Two calls in the same process must yield distinct filenames —
	// time.Now().UnixNano() advances between calls. A regression that
	// swapped to a coarser timestamp or a static token would produce
	// collisions, which on LocalAssetStorage would overwrite a previous
	// upload's bytes silently.
	a := GenerateAssetFilename(".png")
	b := GenerateAssetFilename(".png")
	if a == b {
		t.Errorf("two consecutive filenames collide: %q", a)
	}
	for _, name := range []string{a, b} {
		if !strings.HasPrefix(name, "asset_") {
			t.Errorf("filename %q missing asset_ prefix", name)
		}
		if !strings.HasSuffix(name, ".png") {
			t.Errorf("filename %q missing .png suffix", name)
		}
	}
}

func TestLocalAssetStorage_StoreURLHasCanonicalLayout(t *testing.T) {
	// Returned URL becomes a browser-visible path — canvas/uid/{uid}/
	// {projectUUID}/{filename}. A regression that reordered segments
	// or swapped backslashes (Windows) would surface here.
	tmp := t.TempDir()
	storage := LocalAssetStorage{Root: tmp}
	stored, err := storage.Store(context.Background(), 42, "project-abc", "asset_test.png", []byte("bytes"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// URL must start with "/" and use forward slashes.
	if !strings.HasPrefix(stored.URL, "/") {
		t.Errorf("URL = %q, want leading slash", stored.URL)
	}
	if strings.Contains(stored.URL, "\\") {
		t.Errorf("URL = %q contains backslash; must be filepath.ToSlash-normalised", stored.URL)
	}
	// Canonical segment order: canvas/uid/{uid}/{projectUUID}/{filename}.
	// We don't pin the root prefix (it's a tmp path) but the suffix must
	// be stable.
	wantSuffix := filepath.ToSlash(filepath.Join("canvas", "uid", "42", "project-abc", "asset_test.png"))
	if !strings.HasSuffix(stored.URL, wantSuffix) {
		t.Errorf("URL = %q, want suffix %q", stored.URL, wantSuffix)
	}
}

func TestAllowedUploadContentTypes_PinEntryCountAndExpectedSet(t *testing.T) {
	// The allowed list is an API contract — adding a type silently
	// widens the attack surface. Pin the cardinality and the canonical
	// set so a regression that hoisted "image/bmp" or similar into the
	// map would surface as a failed test, not only as a security review
	// oversight.
	types := AllowedUploadContentTypes()
	expected := map[string]string{
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"image/webp":      ".webp",
		"image/gif":       ".gif",
		"video/mp4":       ".mp4",
		"application/pdf": ".pdf",
	}
	if len(types) != len(expected) {
		t.Errorf("allowed types count = %d, want %d (set must be exactly %v)",
			len(types), len(expected), expected)
	}
	for mime, ext := range expected {
		if got, ok := types[mime]; !ok || got != ext {
			t.Errorf("types[%q] = %q (present=%v), want %q", mime, got, ok, ext)
		}
	}
}

func TestNormalizeCreateAssetInput_AllowsSafeReferences(t *testing.T) {
	// IP literals only — bare hostnames would require live DNS at test
	// time. safehttp_test.go follows the same convention.
	out, err := NormalizeCreateAssetInput(CreateAssetInput{
		Kind:      "  generated  ",
		MimeType:  " image/png ",
		SizeBytes: 1024,
		Width:     1280,
		Height:    720,
		URL:       " /uploads/canvas/asset.png ",
		ThumbURL:  "https://1.1.1.1/thumb.jpg",
		Metadata:  map[string]any{"source": "canvas"},
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Kind != "generated" || out.MimeType != "image/png" || out.URL != "/uploads/canvas/asset.png" {
		t.Errorf("normalization mismatch: %+v", out)
	}
}

func TestNormalizeCreateAssetInput_RejectsUnsafeURLs(t *testing.T) {
	// Closed gap: previously the SSRF guard only checked literal IPs.
	// Hostnames like `localhost` (resolves to 127.0.0.1) are now caught
	// because validateCanvasAssetReferenceURL delegates to
	// safehttp.ValidateURL which does the DNS resolution.
	cases := []string{
		"javascript:alert(1)",
		"file:///etc/passwd",
		"http://127.0.0.1/asset.png",
		"http://10.0.0.1/asset.png",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata endpoint
		"http://[::1]/asset.png",
		"http://localhost/asset.png", // resolves to 127.0.0.1 — DNS path now exercised
		"//evil.example.com/asset.png",
		"https://user:pass@1.1.1.1/asset.png", // creds in URL
	}
	for _, rawURL := range cases {
		_, err := NormalizeCreateAssetInput(CreateAssetInput{URL: rawURL, MimeType: "image/png"})
		if !errors.Is(err, ErrCanvasAssetInvalidInput) {
			t.Errorf("URL %q err = %v, want ErrCanvasAssetInvalidInput", rawURL, err)
		}
	}
}

func TestNormalizeCreateAssetInput_RejectsInvalidMetadataAndDimensions(t *testing.T) {
	cases := []CreateAssetInput{
		{URL: "/asset.png", MimeType: "text/html"},
		{URL: "/asset.png", MimeType: "image/png", SizeBytes: -1},
		{URL: "/asset.png", MimeType: "image/png", Width: maxCanvasAssetDimension + 1},
		{URL: "/asset.png", MimeType: "image/png", Metadata: map[string]any{"bad": math.Inf(1)}},
	}
	for _, input := range cases {
		if _, err := NormalizeCreateAssetInput(input); !errors.Is(err, ErrCanvasAssetInvalidInput) {
			t.Errorf("input %+v err = %v, want ErrCanvasAssetInvalidInput", input, err)
		}
	}
}

func TestValidateManagedCanvasCreateAssetURL_RejectsExternalHostWithManagedPath(t *testing.T) {
	previous := globals.GraConf.Generator.Storage.R2.CDNUrl
	globals.GraConf.Generator.Storage.R2.CDNUrl = "https://cdn.example.com"
	t.Cleanup(func() {
		globals.GraConf.Generator.Storage.R2.CDNUrl = previous
	})

	project := model.CanvasProject{UID: 42, UUID: "project-uuid"}
	managedPath := "/uploads/canvas/uid/42/project-uuid/asset.png"
	if err := validateManagedCanvasCreateAssetURL("https://cdn.example.com"+managedPath, 26, project); err != nil {
		t.Fatalf("allowed managed host rejected: %v", err)
	}
	if err := validateManagedCanvasCreateAssetURL("https://evil.example.com"+managedPath, 26, project); !errors.Is(err, ErrCanvasAssetInvalidInput) {
		t.Fatalf("external host with managed-looking path err = %v, want ErrCanvasAssetInvalidInput", err)
	}
}
