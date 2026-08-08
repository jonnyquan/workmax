package canvas

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canvas_asset_service_test.go pins the pure layer — classification,
// normalisation, filename shape, keyframe request validation, and the
// LocalAssetStorage contract. DB-coupled wrappers (CreateAsset,
// UploadAsset, ListAssets) are exercised by handler-level tests
// against integration MySQL.

func TestNormalizeAssetKind_DefaultsToUpload(t *testing.T) {
	if got := NormalizeAssetKind(""); got != "upload" {
		t.Errorf("blank → %q, want upload", got)
	}
}

func TestNormalizeAssetKind_TrimsWhitespace(t *testing.T) {
	if got := NormalizeAssetKind("  character  "); got != "character" {
		t.Errorf("trim → %q, want character", got)
	}
}

func TestNormalizeAssetKind_WhitespaceOnlyDefaults(t *testing.T) {
	// Drag-and-drop clients occasionally send a " " kind — treat it
	// the same as empty so we don't end up with a junk row.
	if got := NormalizeAssetKind("   "); got != "upload" {
		t.Errorf("spaces → %q, want upload", got)
	}
}

func TestAllowedUploadContentTypes_CoversHandlerContract(t *testing.T) {
	// Pin the MIME list — adding/removing a type changes what the
	// canvas frontend can upload, which is a cross-surface contract.
	got := AllowedUploadContentTypes()
	want := map[string]string{
		"image/png":       ".png",
		"image/jpeg":      ".jpg",
		"image/webp":      ".webp",
		"image/gif":       ".gif",
		"video/mp4":       ".mp4",
		"application/pdf": ".pdf",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%q → %q, want %q", k, got[k], v)
		}
	}
}

func TestAllowedUploadContentTypes_ReturnsFreshMap(t *testing.T) {
	// Callers mustn't be able to corrupt the canonical table.
	a := AllowedUploadContentTypes()
	a["image/png"] = ".corrupt"
	b := AllowedUploadContentTypes()
	if b["image/png"] != ".png" {
		t.Errorf("mutation leaked: %q", b["image/png"])
	}
}

func TestClassifyUploadedContent_RequiresHeaderToMatchSniff(t *testing.T) {
	ct, ext, ok := ClassifyUploadedContent("image/png", []byte("whatever"))
	if ok {
		t.Errorf("expected spoofed image/png to fail, got (%q,%q,%v)", ct, ext, ok)
	}
}

func TestClassifyUploadedContent_AcceptsMatchingHeaderAndSniff(t *testing.T) {
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ct, ext, ok := ClassifyUploadedContent("image/png", pngMagic)
	if !ok || ct != "image/png" || ext != ".png" {
		t.Errorf("got (%q,%q,%v), want (image/png, .png, true)", ct, ext, ok)
	}
}

func TestClassifyUploadedContent_SniffsWhenHeaderBlank(t *testing.T) {
	// Minimal PNG signature — net/http.DetectContentType keys on the
	// 8-byte magic.
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ct, ext, ok := ClassifyUploadedContent("", pngMagic)
	if !ok || ct != "image/png" || ext != ".png" {
		t.Errorf("got (%q,%q,%v), want (image/png, .png, true)", ct, ext, ok)
	}
}

func TestClassifyUploadedContent_SniffsOnOctetStream(t *testing.T) {
	// Browsers frequently send application/octet-stream for drag-in
	// files — we must fall through to sniffing or upload breaks.
	pngMagic := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	_, ext, ok := ClassifyUploadedContent("application/octet-stream", pngMagic)
	if !ok || ext != ".png" {
		t.Errorf("octet-stream fallback failed: ext=%q ok=%v", ext, ok)
	}
}

func TestClassifyUploadedContent_RejectsUnlisted(t *testing.T) {
	// text/html isn't in the allow list — must come back as not ok.
	ct, ext, ok := ClassifyUploadedContent("text/html", []byte("<html>"))
	if ok {
		t.Errorf("expected ok=false for text/html, got ct=%q ext=%q", ct, ext)
	}
}

func TestGenerateAssetFilename_ShapeMatchesHandlerLegacy(t *testing.T) {
	got := GenerateAssetFilename(".png")
	if !strings.HasPrefix(got, "asset_") {
		t.Errorf("%q missing asset_ prefix", got)
	}
	if !strings.HasSuffix(got, ".png") {
		t.Errorf("%q missing .png suffix", got)
	}
	// Filename between the prefix and the extension must be an
	// integer — drifting off this shape breaks assumptions in
	// existing canvas documents that encode URLs directly.
	mid := strings.TrimSuffix(strings.TrimPrefix(got, "asset_"), ".png")
	if mid == "" {
		t.Fatalf("empty mid: %q", got)
	}
	for _, r := range mid {
		if r < '0' || r > '9' {
			t.Errorf("non-digit %q in %q", r, got)
			break
		}
	}
}

func TestNormalizeKeyframeRequest_RejectsEmpty(t *testing.T) {
	if _, err := NormalizeKeyframeRequest("  ", 5); !errors.Is(err, ErrCanvasInvalidVideoURL) {
		t.Errorf("err = %v, want ErrCanvasInvalidVideoURL", err)
	}
}

func TestNormalizeKeyframeRequest_RejectsUnparsable(t *testing.T) {
	if _, err := NormalizeKeyframeRequest("::not-a-url::", 5); !errors.Is(err, ErrCanvasInvalidVideoURL) {
		t.Errorf("err = %v, want ErrCanvasInvalidVideoURL", err)
	}
}

func TestNormalizeKeyframeRequest_RejectsMissingScheme(t *testing.T) {
	// The handler previously rejected bare hostnames to prevent
	// accidental ftp:// / file:// smuggling.
	if _, err := NormalizeKeyframeRequest("//no-scheme.example/video.mp4", 5); !errors.Is(err, ErrCanvasInvalidVideoURL) {
		t.Errorf("err = %v, want ErrCanvasInvalidVideoURL", err)
	}
}

func TestNormalizeKeyframeRequest_RejectsNonHTTPScheme(t *testing.T) {
	if _, err := NormalizeKeyframeRequest("ftp://example.com/v.mp4", 5); !errors.Is(err, ErrCanvasInvalidVideoURL) {
		t.Errorf("err = %v, want ErrCanvasInvalidVideoURL", err)
	}
}

func TestNormalizeKeyframeRequest_ClampsNaNToDefault(t *testing.T) {
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", math.NaN())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 2.5 {
		t.Errorf("timestamp = %v, want 2.5", got.Timestamp)
	}
}

func TestNormalizeKeyframeRequest_ClampsInfToDefault(t *testing.T) {
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", math.Inf(1))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 2.5 {
		t.Errorf("timestamp = %v, want 2.5", got.Timestamp)
	}
}

func TestNormalizeKeyframeRequest_ClampsBelowFloor(t *testing.T) {
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", -1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 0.1 {
		t.Errorf("timestamp = %v, want 0.1", got.Timestamp)
	}
}

func TestNormalizeKeyframeRequest_ClampsAboveCeiling(t *testing.T) {
	got, err := NormalizeKeyframeRequest("https://8.8.8.8/v.mp4", 999)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Timestamp != 120 {
		t.Errorf("timestamp = %v, want 120", got.Timestamp)
	}
}

func TestNormalizeKeyframeRequest_PassesThroughValid(t *testing.T) {
	got, err := NormalizeKeyframeRequest("  https://8.8.8.8/v.mp4  ", 3.7)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.VideoURL != "https://8.8.8.8/v.mp4" {
		t.Errorf("url = %q, want trimmed https url", got.VideoURL)
	}
	if got.Timestamp != 3.7 {
		t.Errorf("timestamp = %v, want 3.7", got.Timestamp)
	}
}

func TestNormalizeListAssetsInput_Defaults(t *testing.T) {
	got := normalizeListAssetsInput(ListAssetsInput{})
	if got.Page != 1 || got.Limit != 50 {
		t.Errorf("got page=%d limit=%d, want 1/50", got.Page, got.Limit)
	}
}

func TestNormalizeListAssetsInput_ClampsLimit(t *testing.T) {
	got := normalizeListAssetsInput(ListAssetsInput{Page: 2, Limit: 5000})
	if got.Limit != 200 {
		t.Errorf("limit = %d, want 200", got.Limit)
	}
	if got.Page != 2 {
		t.Errorf("page = %d, want 2", got.Page)
	}
}

func TestNormalizeListAssetsInput_FloorsPage(t *testing.T) {
	got := normalizeListAssetsInput(ListAssetsInput{Page: -3, Limit: 10})
	if got.Page != 1 {
		t.Errorf("page = %d, want 1", got.Page)
	}
}

func TestLocalAssetStorage_StoreWritesUnderExpectedLayout(t *testing.T) {
	// Pin the on-disk layout — the frontend reads /uploads/canvas/...
	// URLs directly, so changing the layout silently breaks every
	// existing canvas document.
	tmp := t.TempDir()
	storage := LocalAssetStorage{Root: tmp}
	stored, err := storage.Store(context.Background(), 42, "proj-uuid", "asset_1.png", []byte("hello"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	wantRel := filepath.Join("canvas", "uid", "42", "proj-uuid", "asset_1.png")
	wantAbs := filepath.Join(tmp, wantRel)
	data, err := os.ReadFile(wantAbs)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", wantAbs, err)
	}
	if string(data) != "hello" {
		t.Errorf("contents = %q, want hello", string(data))
	}

	// URL is returned as a web path (slash-separated, leading slash).
	if !strings.HasPrefix(stored.URL, "/") {
		t.Errorf("url %q missing leading slash", stored.URL)
	}
	if !strings.HasSuffix(stored.URL, "/canvas/uid/42/proj-uuid/asset_1.png") {
		t.Errorf("url %q missing expected tail", stored.URL)
	}
	if strings.Contains(stored.URL, "\\") {
		t.Errorf("url %q must use forward slashes", stored.URL)
	}
}

func TestLocalAssetStorage_StoreCleanupRemovesFile(t *testing.T) {
	// When the DB TX rolls back we must delete the file; this is the
	// only thing preventing orphaned uploads from piling up on disk.
	tmp := t.TempDir()
	storage := LocalAssetStorage{Root: tmp}
	stored, err := storage.Store(context.Background(), 7, "uuid", "asset_x.png", []byte("data"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	dst := filepath.Join(tmp, "canvas", "uid", "7", "uuid", "asset_x.png")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("pre-cleanup stat: %v", err)
	}
	stored.Cleanup()
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("file still present after cleanup: %v", err)
	}
}

func TestLocalAssetStorage_EmptyRootDefaultsToUploads(t *testing.T) {
	// Pin the fallback so a zero-value LocalAssetStorage doesn't end
	// up writing to CWD root.
	storage := LocalAssetStorage{}
	// Create a temp CWD for the test — the fallback writes a real
	// "uploads" directory, so scope it so we clean up after.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(cwd)

	stored, err := storage.Store(context.Background(), 1, "u", "f.png", []byte("x"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if !strings.Contains(stored.URL, "/uploads/canvas/uid/1/u/f.png") {
		t.Errorf("url %q missing default uploads root", stored.URL)
	}
}
