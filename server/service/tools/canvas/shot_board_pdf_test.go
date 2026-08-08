// shot_board_pdf_test.go — pure-function tests for the ShotBoard
// PDF export (Task #5). DB-coupled handler is tested separately
// at the canvas_shot_board_pdf_api_test layer (when one lands);
// this file pins the projection + PDF assembler in isolation:
//
//   1. extractShotEntries: walks the loose JSONMap document,
//      picks image elements, skips non-image / hidden rows, caps
//      at MaxShotsPerPDF.
//   2. BuildShotBoardPDF: produces non-empty PDF bytes, refuses
//      empty input with ErrNoShots, tolerates a fetcher that
//      returns no image (placeholder fallback never panics).
//   3. sanitizeForPDF: ASCII / latin-1 passes through; CJK falls
//      to space; smart quotes / dashes normalize to ASCII.

package canvas

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"server/model"
)

func TestExtractShotEntries_PicksImagesInOrderAndSkipsOthers(t *testing.T) {
	// The document mixes image, text, shape, drawing, video. Only
	// image-typed rows should land in the entries slice, in source
	// order, with 1-based Index.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "a", "type": "image", "src": "https://cdn/a.png", "prompt": "hero"},
			map[string]interface{}{"id": "b", "type": "text", "content": "title text"},
			map[string]interface{}{"id": "c", "type": "image", "src": "", "prompt": "no src yet"},
			map[string]interface{}{"id": "d", "type": "video", "videoSrc": "https://cdn/d.mp4"},
			map[string]interface{}{"id": "e", "type": "image", "src": "https://cdn/e.jpg", "prompt": "wide"},
		},
	}
	entries := extractShotEntries(doc)
	if len(entries) != 3 {
		t.Fatalf("expected 3 image entries, got %d", len(entries))
	}
	if entries[0].ID != "a" || entries[0].Index != 1 {
		t.Errorf("entries[0] = %+v, want id=a index=1", entries[0])
	}
	if entries[1].ID != "c" || entries[1].Index != 2 {
		t.Errorf("entries[1] = %+v, want id=c index=2 (empty src is still an image)", entries[1])
	}
	if entries[2].ID != "e" || entries[2].Index != 3 {
		t.Errorf("entries[2] = %+v, want id=e index=3", entries[2])
	}
}

func TestExtractShotEntries_SkipsHiddenElements(t *testing.T) {
	// An image with visible=false is intentionally hidden in the
	// canvas; it should NOT appear on the printable board.
	doc := model.JSONMap{
		"elements": []interface{}{
			map[string]interface{}{"id": "vis", "type": "image", "src": "x"},
			map[string]interface{}{"id": "hidden", "type": "image", "src": "y", "visible": false},
		},
	}
	entries := extractShotEntries(doc)
	if len(entries) != 1 || entries[0].ID != "vis" {
		t.Errorf("hidden image leaked into export: %+v", entries)
	}
}

func TestExtractShotEntries_EmptyDocPathsReturnNil(t *testing.T) {
	// Pin all the "document doesn't carry a usable elements list"
	// branches: nil doc, missing key, wrong type. None should
	// panic; all should return nil.
	cases := []struct {
		name string
		doc  model.JSONMap
	}{
		{"nil doc", nil},
		{"no elements key", model.JSONMap{"viewport": map[string]interface{}{}}},
		{"elements is not array", model.JSONMap{"elements": "oops"}},
		{"elements is empty array", model.JSONMap{"elements": []interface{}{}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractShotEntries(c.doc)
			if len(got) != 0 {
				t.Errorf("%s: expected nil/empty, got %d entries", c.name, len(got))
			}
		})
	}
}

func TestExtractShotEntries_CapsAtMaxShotsPerPDF(t *testing.T) {
	// A pathological project with thousands of image elements
	// should be cut at MaxShotsPerPDF so the PDF stays printable.
	// Build a doc with MaxShotsPerPDF + 50 image rows.
	elements := make([]interface{}, 0, MaxShotsPerPDF+50)
	for i := 0; i < MaxShotsPerPDF+50; i++ {
		elements = append(elements, map[string]interface{}{
			"id":   "i",
			"type": "image",
			"src":  "x",
		})
	}
	entries := extractShotEntries(model.JSONMap{"elements": elements})
	if len(entries) != MaxShotsPerPDF {
		t.Errorf("expected cap at %d, got %d", MaxShotsPerPDF, len(entries))
	}
}

func TestBuildShotBoardPDF_EmptyInputReturnsErrNoShots(t *testing.T) {
	_, err := BuildShotBoardPDF(context.Background(), "Empty", nil, nil)
	if !errors.Is(err, ErrNoShots) {
		t.Errorf("empty entries should return ErrNoShots, got %v", err)
	}
}

func TestBuildShotBoardPDF_RendersNonEmptyPDFWithoutFetcher(t *testing.T) {
	// nil fetcher → every cell falls back to the "no image"
	// placeholder. The PDF must still produce non-empty output
	// starting with the PDF magic bytes.
	entries := []ShotPDFEntry{
		{ID: "s1", Index: 1, Prompt: "wide street, golden hour"},
		{ID: "s2", Index: 2, Prompt: "close up on protagonist"},
	}
	bytesOut, err := BuildShotBoardPDF(context.Background(), "Test Project", entries, nil)
	if err != nil {
		t.Fatalf("BuildShotBoardPDF: %v", err)
	}
	if len(bytesOut) < 100 {
		t.Errorf("PDF output too small: %d bytes", len(bytesOut))
	}
	if !bytes.HasPrefix(bytesOut, []byte("%PDF-")) {
		t.Errorf("output does not start with PDF magic header, got %q", bytesOut[:10])
	}
}

func TestBuildShotBoardPDF_FetcherFailureFallsToPlaceholder(t *testing.T) {
	// A fetcher that returns (nil, "", nil) for every URL signals
	// "image unavailable" — the assembler must not abort, must
	// fall through to the placeholder, must still produce a valid
	// PDF byte stream.
	entries := []ShotPDFEntry{
		{ID: "s1", Index: 1, Prompt: "p", Src: "https://example/broken.png"},
	}
	stub := func(_ context.Context, _ string) ([]byte, string, error) {
		return nil, "", nil
	}
	bytesOut, err := BuildShotBoardPDF(context.Background(), "Project", entries, stub)
	if err != nil {
		t.Errorf("failed-fetcher path should still produce a PDF, got %v", err)
	}
	if !bytes.HasPrefix(bytesOut, []byte("%PDF-")) {
		t.Errorf("output not a PDF")
	}
}

func TestBuildShotBoardPDF_PaginatesWhenManyShots(t *testing.T) {
	// 13 shots at 6 per page = 3 pages. The output should be
	// noticeably larger than a single-page version. Use byte size
	// as a proxy for page count (asserting exact page count would
	// require a PDF parser).
	one := []ShotPDFEntry{{ID: "s1", Index: 1, Prompt: "p"}}
	many := make([]ShotPDFEntry, 0, 13)
	for i := 0; i < 13; i++ {
		many = append(many, ShotPDFEntry{ID: "s", Index: i + 1, Prompt: "p"})
	}
	smallBytes, _ := BuildShotBoardPDF(context.Background(), "P", one, nil)
	bigBytes, _ := BuildShotBoardPDF(context.Background(), "P", many, nil)
	if len(bigBytes) <= len(smallBytes) {
		t.Errorf("13-shot PDF (%d bytes) should be larger than 1-shot PDF (%d bytes)",
			len(bigBytes), len(smallBytes))
	}
}

// ---------------------------------------------------------------------
// sanitizeForPDF
// ---------------------------------------------------------------------

func TestSanitizeForPDF_NormalizesSmartPunctuation(t *testing.T) {
	// Helvetica can't render smart quotes / em-dash / ellipsis —
	// without sanitization they'd silently disappear from the
	// caption. Pin the ASCII fallback.
	cases := []struct {
		in   string
		want string
	}{
		{"plain ASCII", "plain ASCII"},
		{"smart ‘quotes’", "smart 'quotes'"},
		{"smart “double”", "smart \"double\""},
		{"em — dash", "em - dash"},
		{"en – dash", "en - dash"},
		{"ellipsis…", "ellipsis..."},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := sanitizeForPDF(c.in)
			if got != c.want {
				t.Errorf("sanitizeForPDF(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeForPDF_FoldsCJKToSpace(t *testing.T) {
	// CJK doesn't render on the default Helvetica font; folding to
	// space at least preserves token boundaries so "林夏 runs"
	// becomes "   runs" instead of "?? runs" or empty output.
	// Word boundary survives.
	got := sanitizeForPDF("林夏 runs fast")
	if !strings.Contains(got, "runs fast") {
		t.Errorf("CJK fold should preserve trailing ASCII; got %q", got)
	}
}

func TestSanitizeForPDF_TruncatesRunawayInput(t *testing.T) {
	// A 5000-char prompt would overflow even MultiCell wrapping
	// across multiple pages. Pin the hard truncation.
	long := strings.Repeat("a", 5000)
	got := sanitizeForPDF(long)
	if len(got) > 300 {
		t.Errorf("long input should be truncated; got %d chars", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated output should end in '...' so the reader knows; got tail %q",
			got[len(got)-5:])
	}
}
