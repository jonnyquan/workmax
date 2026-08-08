package canvas

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// processor_helpers_test.go pins the headline contract (TruncateString
// rune-counting + ellipsis, mimeToExtension known-map + .bin fallback,
// ErrorResultPayload shape, PrepareAttachments empty/base64/data-URI
// strip / synthesised filename / invalid base64 drop). These fill the
// quieter gate invariants a silent regression would slip past:
//
//   • TruncateString: empty input + any positive maxLen returns "" (no
//     ellipsis), because len(runes)==0 <= maxLen is true. Pin so a
//     refactor that inverted the guard would start emitting "..." for
//     empty strings — the SDK would see phantom ellipses.
//   • TruncateString: maxLen=1 of a 1-rune string is pass-through (no
//     trunc). Boundary-pin beyond base's exact-5 test.
//   • TruncateString: ellipsis is literal "..." (3 ASCII dots), NOT
//     U+2026 "…". Pin so a Unicode-polish refactor would surface — the
//     client-side re-trim logic expects 3-byte suffix.
//   • TruncateString: the return uses string(runes[:maxLen]) so it is
//     deterministic — same args always same result, no hidden state.
//
//   • mimeToExtension: parameterised mime (`image/png; charset=x`)
//     falls through to .bin. Strict map-key equality only — pin so a
//     refactor that split on `;` would surface as a deliberate loosen.
//   • mimeToExtension: `image/jpg` (wrong casing / non-IANA) → .bin.
//     The registered mime is `image/jpeg`; clients that shorthand to
//     `image/jpg` must hit the default.
//   • mimeToExtension: every lookup is deterministic — repeat calls
//     return the same string (map literal has no RNG).
//   • mimeToExtension: `video/quicktime` → .bin (not in map). Pin so
//     a future .mov addition is a deliberate change.
//
//   • ErrorResultPayload: called twice yields byte-identical JSON. Pin
//     determinism — a refactor to include a timestamp or request-id
//     would break SSE equality across retries.
//   • ErrorResultPayload: the top-level keys are exactly {type, subtype,
//     is_error, error} — no surprise fields. A frontend that enumerates
//     keys would break if a new one snuck in.
//   • ErrorResultPayload: the "error" copy contains the phrase "try
//     again" as user-facing guidance. Pin the intent-level text so a
//     refactor that rewrites the copy has to update this assertion on
//     purpose.
//
//   • PrepareAttachments: an EMPTY (non-nil) slice exits via the
//     `len == 0` guard — does NOT create uploads/ dir. Base test covers
//     nil; pin empty-but-non-nil here.
//   • PrepareAttachments: filename WITHOUT extension is preserved
//     after safety normalization (no automatic ext from mime).
//   • PrepareAttachments: multiple attachments — filenames are
//     1-indexed (`attachment_1`, `attachment_2`), but IDs are 0-indexed
//     (`canvas_att_0`, `canvas_att_1`). Pin this index asymmetry so a
//     refactor that aligned them (both 0 or both 1) would surface — UI
//     referencing either anchor would break otherwise.
//   • PrepareAttachments: synthesised filename with UNKNOWN mime
//     yields `attachment_N.bin`. Pin the .bin-fallback path through the
//     filename pipeline, not just mimeToExtension in isolation.
//   • PrepareAttachments: attachment with EMPTY url AND EMPTY data
//     (header-only metadata) is silently skipped — the result omits it
//     and the call doesn't panic. Pin the "no-op on empty" contract so a
//     refactor that synthesised a phantom file would surface.
//   • PrepareAttachments: mixed valid + invalid batch — the valid
//     entry survives with its correct ID, the invalid one drops. Pin so
//     a refactor that bailed on first error would surface.
//   • PrepareAttachments: Type is copied from input verbatim — it
//     is NOT cross-checked against MimeType. A caller that set Type to
//     "image" with a pdf MimeType still sees Type=image in the result.

func TestTruncateString_EmptyInputNoEllipsis(t *testing.T) {
	// `len([]rune("")) == 0` ≤ any positive maxLen → the guard triggers
	// and the empty string is returned unchanged. No phantom "...".
	for _, maxLen := range []int{0, 1, 5, 100} {
		if got := TruncateString("", maxLen); got != "" {
			t.Errorf("TruncateString(\"\", %d) = %q, want \"\"", maxLen, got)
		}
	}
}

func TestTruncateString_SingleRuneWithMaxLen1PassesThrough(t *testing.T) {
	if got := TruncateString("a", 1); got != "a" {
		t.Errorf("TruncateString(%q, 1) = %q, want %q", "a", got, "a")
	}
	if got := TruncateString("你", 1); got != "你" {
		t.Errorf("TruncateString(%q, 1) = %q, want %q", "你", got, "你")
	}
}

func TestTruncateString_EllipsisIsThreeAsciiDots(t *testing.T) {
	got := TruncateString("abcdef", 3)
	if got != "abc..." {
		t.Fatalf("TruncateString(%q, 3) = %q, want %q", "abcdef", got, "abc...")
	}
	// Explicit byte check — U+2026 would be 3 bytes (0xe2 0x80 0xa6),
	// ASCII "..." is 3 bytes (0x2e 0x2e 0x2e).
	tail := got[len(got)-3:]
	if tail != "..." {
		t.Errorf("tail = %q (codepoints %v), want literal '...'", tail, []byte(tail))
	}
	for _, b := range []byte(tail) {
		if b != 0x2e {
			t.Errorf("ellipsis byte = 0x%x, want 0x2e ('.')", b)
		}
	}
}

func TestTruncateString_Deterministic(t *testing.T) {
	a := TruncateString("abcdefghij", 4)
	b := TruncateString("abcdefghij", 4)
	if a != b {
		t.Errorf("TruncateString non-deterministic: %q vs %q", a, b)
	}
}

func TestMimeToExtension_ParameterisedMimeFallsThrough(t *testing.T) {
	// Strict map-equal: `image/png; charset=x` is NOT a key.
	cases := []string{
		"image/png; charset=utf-8",
		"image/png;charset=utf-8",
		"application/pdf; version=1.7",
		"video/mp4; codec=h264",
	}
	for _, mt := range cases {
		if got := mimeToExtension(mt); got != ".bin" {
			t.Errorf("mimeToExtension(%q) = %q, want %q (strict-equal fallthrough)", mt, got, ".bin")
		}
	}
}

func TestMimeToExtension_NonIANAShorthandFallsThrough(t *testing.T) {
	// `image/jpg` is a common client shorthand but isn't registered.
	// Pin so a future "normalise" refactor is deliberate.
	if got := mimeToExtension("image/jpg"); got != ".bin" {
		t.Errorf("mimeToExtension(image/jpg) = %q, want %q", got, ".bin")
	}
}

func TestMimeToExtension_UnknownVideoAndApplicationDefault(t *testing.T) {
	for _, mt := range []string{"video/quicktime", "video/mov", "application/json", "application/zip"} {
		if got := mimeToExtension(mt); got != ".bin" {
			t.Errorf("mimeToExtension(%q) = %q, want %q", mt, got, ".bin")
		}
	}
}

func TestMimeToExtension_Deterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		if got := mimeToExtension("image/png"); got != ".png" {
			t.Fatalf("iteration %d: mimeToExtension(image/png) = %q, want .png", i, got)
		}
	}
}

func TestErrorResultPayload_DeterministicBytes(t *testing.T) {
	a := ErrorResultPayload()
	b := ErrorResultPayload()
	if string(a) != string(b) {
		t.Errorf("ErrorResultPayload bytes differ across calls:\n  a=%s\n  b=%s", a, b)
	}
}

func TestErrorResultPayload_OnlyExpectedKeys(t *testing.T) {
	// Any new top-level key is a wire-format change — surface it here.
	var parsed map[string]any
	if err := json.Unmarshal(ErrorResultPayload(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := map[string]bool{"type": true, "subtype": true, "is_error": true, "error": true}
	for k := range parsed {
		if !want[k] {
			t.Errorf("unexpected key in payload: %q", k)
		}
	}
	for k := range want {
		if _, ok := parsed[k]; !ok {
			t.Errorf("missing key in payload: %q", k)
		}
	}
}

func TestErrorResultPayload_CopyContainsTryAgain(t *testing.T) {
	var parsed map[string]any
	_ = json.Unmarshal(ErrorResultPayload(), &parsed)
	errText, _ := parsed["error"].(string)
	if !strings.Contains(errText, "try again") {
		t.Errorf("error copy missing 'try again' guidance; got %q", errText)
	}
}

func TestNormalizeCanvasAgentConversationIDRejectsTraversal(t *testing.T) {
	if _, err := NormalizeConversationID("../escape"); err == nil {
		t.Fatal("expected traversal conversationId to be rejected")
	}
	if got, err := NormalizeConversationID("550e8400-e29b-41d4-a716-446655440000"); err != nil || got != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("valid conversationId rejected: got=%q err=%v", got, err)
	}
}

func TestValidateCanvasAgentRequestRejectsOversizedEnvelope(t *testing.T) {
	req := CanvasAgentChatRequest{
		Messages: []CanvasAgentMessage{{
			Role:    "user",
			Content: strings.Repeat("x", maxCanvasAgentMessageChars+1),
		}},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("expected oversized message to be rejected")
	}
}

func TestValidateCanvasAgentRequestRejectsTooManyContextElements(t *testing.T) {
	req := CanvasAgentChatRequest{
		Messages: []CanvasAgentMessage{{Role: "user", Content: "hello"}},
		Context: CanvasAgentContext{
			Elements: make([]map[string]any, maxCanvasAgentContextElements+1),
		},
	}
	if err := ValidateRequest(req); err == nil {
		t.Fatal("expected oversized context elements to be rejected")
	}
}

func TestPrepareCanvasAttachments_EmptyNonNilSliceExitsWithoutMkdir(t *testing.T) {
	// Base test covers nil; pin empty non-nil slice (different in Go
	// but same len==0 observable).
	dir := t.TempDir()
	got := PrepareAttachments([]CanvasAgentAttachment{}, dir)
	if got != nil {
		t.Errorf("expected nil result for empty slice, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "uploads")); !os.IsNotExist(err) {
		t.Errorf("uploads/ must not be created for empty slice; stat err = %v", err)
	}
}

func TestPrepareCanvasAttachments_FilenameWithoutExtensionPreserved(t *testing.T) {
	// When the caller supplies a safe Filename, it's preserved — no
	// automatic extension from MimeType.
	dir := t.TempDir()
	payload := []byte("no-ext-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		Data:     encoded,
		MimeType: "image/png",
		Filename: "hero", // no extension
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	if got[0].Name != "hero" {
		t.Errorf("Name = %q, want %q (no auto-extension)", got[0].Name, "hero")
	}
}

func TestPrepareCanvasAttachments_PathTraversalFilenameIsBasename(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("safe-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		Data:     encoded,
		MimeType: "image/png",
		Filename: "../../outside.png",
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	if got[0].Name != "outside.png" {
		t.Errorf("Name = %q, want %q", got[0].Name, "outside.png")
	}
	wantDir := filepath.Join(dir, "uploads")
	if filepath.Dir(got[0].Path) != wantDir {
		t.Errorf("Path dir = %q, want uploads dir %q", filepath.Dir(got[0].Path), wantDir)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.png")); !os.IsNotExist(err) {
		t.Errorf("path traversal wrote outside uploads dir; stat err = %v", err)
	}
}

func TestPrepareCanvasAttachments_IndexingAsymmetryPinned(t *testing.T) {
	// Filename index is i+1 (1-based: attachment_1, attachment_2);
	// ID index is i (0-based: canvas_att_0, canvas_att_1).
	dir := t.TempDir()
	payload := []byte("bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	atts := []CanvasAgentAttachment{
		{Type: "image", Data: encoded, MimeType: "image/png"},
		{Type: "image", Data: encoded, MimeType: "image/jpeg"},
		{Type: "image", Data: encoded, MimeType: "image/webp"},
	}
	got := PrepareAttachments(atts, dir)
	if len(got) != 3 {
		t.Fatalf("expected 3 attachments, got %d", len(got))
	}
	// Filenames: 1-based
	wantNames := []string{"attachment_1.png", "attachment_2.jpg", "attachment_3.webp"}
	wantIDs := []string{"canvas_att_0", "canvas_att_1", "canvas_att_2"}
	for i := range got {
		if got[i].Name != wantNames[i] {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, wantNames[i])
		}
		if got[i].ID != wantIDs[i] {
			t.Errorf("got[%d].ID = %q, want %q", i, got[i].ID, wantIDs[i])
		}
	}
}

func TestPrepareCanvasAttachments_SynthesisedFilenameUnknownMimeIsBin(t *testing.T) {
	// Blank Filename + unknown MimeType → `attachment_1.bin`. Pin the
	// .bin-fallback path end-to-end (not just mimeToExtension).
	dir := t.TempDir()
	payload := []byte("anon")
	encoded := base64.StdEncoding.EncodeToString(payload)
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "file",
		Data:     encoded,
		MimeType: "application/octet-stream", // unknown to extMap
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	if got[0].Name != "attachment_1.bin" {
		t.Errorf("Name = %q, want %q", got[0].Name, "attachment_1.bin")
	}
}

func TestPrepareCanvasAttachments_EmptyURLAndEmptyDataSkipsSilently(t *testing.T) {
	// Header-only entry (neither URL nor Data) is a metadata placeholder
	// the server can't materialise — it should drop without crashing.
	dir := t.TempDir()
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		MimeType: "image/png",
		Filename: "phantom.png",
	}}, dir)
	if len(got) != 0 {
		t.Errorf("expected 0 attachments for header-only entry, got %d: %+v", len(got), got)
	}
}

func TestPrepareCanvasAttachments_MixedValidAndInvalidBatch(t *testing.T) {
	// Valid entry at index 0, invalid at index 1 — only valid survives;
	// its ID is `canvas_att_0` (pinned above). Pin that the skipped
	// index doesn't renumber the survivor.
	dir := t.TempDir()
	payload := []byte("ok-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)
	got := PrepareAttachments([]CanvasAgentAttachment{
		{Type: "image", Data: encoded, MimeType: "image/png", Filename: "ok.png"},
		{Type: "image", Data: "not!valid!base64!@@@", MimeType: "image/png"},
	}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 survivor, got %d", len(got))
	}
	if got[0].Name != "ok.png" {
		t.Errorf("survivor.Name = %q, want %q", got[0].Name, "ok.png")
	}
	if got[0].ID != "canvas_att_0" {
		t.Errorf("survivor.ID = %q, want %q (loop index retained)", got[0].ID, "canvas_att_0")
	}
}

func TestPrepareCanvasAttachments_TypeIsPassedThroughVerbatim(t *testing.T) {
	// Type is copied to entry.Type with no validation against MimeType —
	// a caller that sets Type="video" with a PDF mime still sees
	// Type="video" on the output.
	dir := t.TempDir()
	encoded := base64.StdEncoding.EncodeToString([]byte("x"))
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "video", // lies
		Data:     encoded,
		MimeType: "application/pdf",
		Filename: "doc.pdf",
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Type != "video" {
		t.Errorf("Type = %q, want %q (pass-through, no cross-check)", got[0].Type, "video")
	}
}
