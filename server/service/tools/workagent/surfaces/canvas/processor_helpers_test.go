package canvas

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canvas_agent_processor.go is dominated by HandleAgentChat, which owns
// the gin request + goroutine-based SSE pump + SDK round-trip, and is
// only testable via a full request scaffold. These tests pin the pure
// helpers that sit alongside it — regressions in any of these corrupt
// a user-visible surface (truncated system-prompt tail, wrong file
// extension in the workspace, or a structurally broken error event):
//
//   TruncateString         → system-prompt trimming before sending to SDK
//   mimeToExtension        → attachment filenames written to workspace
//   ErrorResultPayload     → the SSE error event the client decodes
//   PrepareAttachments (base64 branch only — URL branch needs net)

func TestTruncateString(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"short string passes through", "hello", 10, "hello"},
		{"exact length passes through without ellipsis", "12345", 5, "12345"},
		{"trims and appends triple-dot ellipsis", "abcdefghij", 4, "abcd..."},
		// `maxLen` is counted in runes, not bytes — a CJK-heavy system
		// prompt would otherwise get cut mid-codepoint and emit broken
		// UTF-8 into the SDK. The ellipsis is still literal "...".
		{"counts runes not bytes for CJK", "你好世界朋友", 3, "你好世..."},
		// maxLen=0 must collapse to just "..." (not a panic or empty
		// string that would silently hide the truncation).
		{"zero maxLen emits just ellipsis", "whatever", 0, "..."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TruncateString(c.in, c.maxLen); got != c.want {
				t.Errorf("TruncateString(%q, %d) = %q, want %q", c.in, c.maxLen, got, c.want)
			}
		})
	}
}

func TestMimeToExtension(t *testing.T) {
	// The map is load-bearing for attachment workspace writes: an unknown
	// mime would otherwise land as a filename without an extension, and
	// downstream tools (image viewers, the SDK's file-type sniffer) rely
	// on the suffix. Pin every entry so a silent map-trim surfaces here
	// before it ships as "image reads as .bin on the Claude side".
	cases := []struct {
		mime string
		want string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"image/svg+xml", ".svg"},
		{"video/mp4", ".mp4"},
		{"video/webm", ".webm"},
		{"application/pdf", ".pdf"},
		// Unknown and empty both fall through to the .bin default — the
		// SDK still accepts the file but labels it as binary.
		{"application/octet-stream", ".bin"},
		{"", ".bin"},
		// Case-sensitivity matters here: HTTP mime types are conventionally
		// lower-cased, but a client could send a capitalised one. Pin the
		// current behaviour (case-sensitive → .bin) so a future "normalise"
		// fix is a deliberate choice, not an accidental loosening.
		{"Image/PNG", ".bin"},
	}
	for _, c := range cases {
		t.Run(c.mime, func(t *testing.T) {
			if got := mimeToExtension(c.mime); got != c.want {
				t.Errorf("mimeToExtension(%q) = %q, want %q", c.mime, got, c.want)
			}
		})
	}
}

func TestErrorResultPayload_ShapeAndFlags(t *testing.T) {
	// The SSE client decodes this payload as the "result" block on error.
	// Two flags are load-bearing: is_error=true gates the UI error toast,
	// and subtype=error lets the history replayer skip this turn. If
	// either drifts the client either shows no error or re-renders the
	// stale assistant text.
	raw := ErrorResultPayload()
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("ErrorResultPayload produced invalid JSON: %v", err)
	}
	if parsed["type"] != "result" {
		t.Errorf("type = %v, want %q", parsed["type"], "result")
	}
	if parsed["subtype"] != "error" {
		t.Errorf("subtype = %v, want %q", parsed["subtype"], "error")
	}
	// JSON booleans decode as the bool kind, not a string.
	if parsed["is_error"] != true {
		t.Errorf("is_error = %v, want true", parsed["is_error"])
	}
	// Error copy is shown in the client — pin the generic prefix so a
	// future typo fix has to update this assertion on purpose.
	errText, _ := parsed["error"].(string)
	if !strings.Contains(errText, "canvas") {
		t.Errorf("error copy should mention canvas; got %q", errText)
	}
}

func TestPrepareCanvasAttachments_Empty(t *testing.T) {
	// No attachments → nil result, and no uploads/ directory created.
	// The second half matters: the uploads dir is created lazily, and a
	// stray mkdir on the hot path would balloon per-request latency on
	// a chatty panel with no attachments.
	dir := t.TempDir()
	got := PrepareAttachments(nil, dir)
	if got != nil {
		t.Errorf("expected nil for no attachments, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "uploads")); !os.IsNotExist(err) {
		t.Errorf("uploads/ dir must not be created for empty attachments (stat err = %v)", err)
	}
}

func TestPrepareCanvasAttachments_Base64Decodes(t *testing.T) {
	// Base64 fallback path: no URL, just inline data. The bytes must
	// land on disk verbatim, the returned AgentFileInfo must point at
	// the workspace path, and the filename must honour the caller's
	// `Filename` when provided.
	dir := t.TempDir()
	payload := []byte("canvas-pixel-bytes")
	encoded := base64.StdEncoding.EncodeToString(payload)

	attachments := []CanvasAgentAttachment{
		{
			Type:     "image",
			Data:     encoded,
			MimeType: "image/png",
			Filename: "hero.png",
		},
	}
	got := PrepareAttachments(attachments, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	entry := got[0]
	if entry.Name != "hero.png" {
		t.Errorf("Name = %q, want %q", entry.Name, "hero.png")
	}
	if entry.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", entry.Size, len(payload))
	}
	if entry.Type != "image" {
		t.Errorf("Type = %q, want %q", entry.Type, "image")
	}
	// ID is prefixed so the SDK can distinguish canvas attachments in
	// its tool-call history; pin the prefix so a rename surfaces here.
	if !strings.HasPrefix(entry.ID, "canvas_att_") {
		t.Errorf("ID = %q, want prefix canvas_att_", entry.ID)
	}

	onDisk, err := os.ReadFile(entry.Path)
	if err != nil {
		t.Fatalf("reading written attachment: %v", err)
	}
	if string(onDisk) != string(payload) {
		t.Errorf("on-disk bytes = %q, want %q", string(onDisk), string(payload))
	}
}

func TestPrepareCanvasAttachments_Base64StripsDataURIPrefix(t *testing.T) {
	// Clients sometimes pass the full `data:image/png;base64,<data>` URI
	// form from FileReader.readAsDataURL. The processor must strip the
	// prefix before decoding — otherwise base64.StdEncoding returns a
	// CorruptInputError and the attachment silently drops. Pin the
	// prefix-strip path so a future refactor that moves to a stricter
	// parser has to update this assertion deliberately.
	dir := t.TempDir()
	payload := []byte("stripped-prefix-bytes")
	encoded := "data:image/png;base64," + base64.StdEncoding.EncodeToString(payload)

	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		Data:     encoded,
		MimeType: "image/png",
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment after prefix strip, got %d", len(got))
	}
	if got[0].Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d (prefix not stripped?)", got[0].Size, len(payload))
	}
}

func TestPrepareCanvasAttachments_MissingFilenameSynthesisedFromMime(t *testing.T) {
	// Blank Filename + known MimeType → attachment_<n><ext>. The index
	// is 1-based so the first file is `attachment_1.png`, not `_0.png`
	// (the UI users see lists files in order and `_0` looks off-by-one).
	dir := t.TempDir()
	payload := []byte("anon")
	encoded := base64.StdEncoding.EncodeToString(payload)

	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		Data:     encoded,
		MimeType: "image/webp",
	}}, dir)
	if len(got) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got))
	}
	if got[0].Name != "attachment_1.webp" {
		t.Errorf("Name = %q, want %q", got[0].Name, "attachment_1.webp")
	}
}

func TestPrepareCanvasAttachments_InvalidBase64SilentlySkipsButDoesNotPanic(t *testing.T) {
	// An attachment with non-decodable base64 must not crash the request.
	// The processor logs a warn and omits the entry from the result so
	// the SDK still sees the rest of the batch. Pin the "empty result,
	// no panic" contract here; the log line is incidental.
	dir := t.TempDir()
	got := PrepareAttachments([]CanvasAgentAttachment{{
		Type:     "image",
		Data:     "not!valid!base64!@@@",
		MimeType: "image/png",
	}}, dir)
	if len(got) != 0 {
		t.Errorf("expected 0 attachments when base64 is invalid, got %d", len(got))
	}
}
