package canvas

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// types.go defines the wire format the canvas agent endpoint parses
// (CanvasAgentChatRequest family) and the SSE event-type labels it
// emits. Both are cross-layer contracts — the frontend chat hook reads
// the same tags on decode and dispatches on the same SSE type values.
// Drift here breaks the chat stream in a way the unit tests downstream
// would not catch because they mock the transport.

func TestSSEType_ConstantValues(t *testing.T) {
	// The frontend SSE reader switches on these exact strings. A
	// rename here is a protocol break; a typo silently routes the
	// event into the default branch and vanishes from the UI.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"message", SSETypeMessage, "message"},
		{"block", SSETypeBlock, "block"},
		{"file", SSETypeFile, "file"},
		{"done", SSETypeDone, "done"},
		{"error", SSETypeError, "error"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestSSEType_ConstantsAreDistinct(t *testing.T) {
	// A typo that collapses two constants onto the same string
	// (e.g. SSETypeDone and SSETypeError both emitting "done") would
	// mis-classify terminal events. Detect at build time.
	seen := map[string]string{}
	for name, v := range map[string]string{
		"message": SSETypeMessage,
		"block":   SSETypeBlock,
		"file":    SSETypeFile,
		"done":    SSETypeDone,
		"error":   SSETypeError,
	} {
		if dup, ok := seen[v]; ok {
			t.Errorf("SSE type value %q is shared by %s and %s", v, dup, name)
		}
		seen[v] = name
	}
}

func TestCanvasAgentChatRequest_JSONRoundtripsWireTags(t *testing.T) {
	// The field casing matters — the frontend emits camelCase, the
	// Go tags must match. Marshal then unmarshal to the same value,
	// and sanity-check the on-wire keys.
	orig := CanvasAgentChatRequest{
		Messages: []CanvasAgentMessage{
			{Role: "user", Content: "hi"},
		},
		ConversationID: "conv-1",
		Context: CanvasAgentContext{
			Elements:    []map[string]any{{"id": "e1"}},
			SelectedIDs: []string{"e1"},
			Viewport:    map[string]any{"x": float64(0)},
			Skill:       "designer",
			Model:       "gpt-4",
			ProjectID:   "p-42",
		},
		Attachments: []CanvasAgentAttachment{
			{Type: "image", URL: "https://cdn/x.png", MimeType: "image/png", Filename: "x.png"},
		},
		Metadata: map[string]any{"foo": "bar"},
	}
	blob, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// Required wire tags — pin each so a Go-side rename without tag
	// update is caught here instead of in a silent production break.
	for _, tag := range []string{
		`"messages":`, `"conversationId":`, `"context":`,
		`"attachments":`, `"metadata":`,
		`"selectedIds":`, `"viewport":`, `"skill":`, `"model":`, `"projectId":`,
		`"type":"image"`, `"url":"https://cdn/x.png"`, `"mimeType":"image/png"`, `"filename":"x.png"`,
	} {
		if !strings.Contains(string(blob), tag) {
			t.Errorf("expected wire tag %s in payload; got:\n%s", tag, blob)
		}
	}

	var round CanvasAgentChatRequest
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if round.ConversationID != "conv-1" {
		t.Errorf("ConversationID round-trip lost: got %q", round.ConversationID)
	}
	if round.Context.Skill != "designer" {
		t.Errorf("Context.Skill round-trip lost: got %q", round.Context.Skill)
	}
	if len(round.Attachments) != 1 || round.Attachments[0].Filename != "x.png" {
		t.Errorf("Attachments round-trip lost: %+v", round.Attachments)
	}
}

func TestCanvasAgentChatRequest_OptionalFieldsOmitEmpty(t *testing.T) {
	// conversationId / attachments / metadata are omitempty. A minimal
	// request from a brand-new conversation must not ship empty arrays
	// or strings — some downstream consumers reject null-valued keys.
	minimal := CanvasAgentChatRequest{
		Messages: []CanvasAgentMessage{{Role: "user", Content: "hi"}},
		Context:  CanvasAgentContext{},
	}
	blob, err := json.Marshal(minimal)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, tag := range []string{
		`"conversationId"`, `"attachments"`, `"metadata"`,
	} {
		if strings.Contains(string(blob), tag) {
			t.Errorf("expected %s to be omitted when empty; got:\n%s", tag, blob)
		}
	}
}

func TestCanvasAgentContext_OptionalFieldsOmitEmpty(t *testing.T) {
	// skill/model/projectId are omitempty. A request without a
	// skill persona must not ship "skill":"" and accidentally route
	// through the default branch of skillPersona.
	ctx := CanvasAgentContext{
		Elements:    []map[string]any{},
		SelectedIDs: []string{},
	}
	blob, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, tag := range []string{`"skill"`, `"model"`, `"projectId"`} {
		if strings.Contains(string(blob), tag) {
			t.Errorf("expected %s to be omitted when empty; got:\n%s", tag, blob)
		}
	}
}

func TestCanvasAgentAttachment_OptionalFieldsOmitEmpty(t *testing.T) {
	// data/url/filename omitempty — an in-flight upload may ship data
	// only; a post-upload reference ships url only. Never both as empty.
	att := CanvasAgentAttachment{
		Type:     "image",
		MimeType: "image/png",
	}
	blob, err := json.Marshal(att)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, tag := range []string{`"data"`, `"url"`, `"filename"`} {
		if strings.Contains(string(blob), tag) {
			t.Errorf("expected %s to be omitted when empty; got:\n%s", tag, blob)
		}
	}
	// Required fields must always be present.
	for _, tag := range []string{`"type":"image"`, `"mimeType":"image/png"`} {
		if !strings.Contains(string(blob), tag) {
			t.Errorf("expected required tag %s; got:\n%s", tag, blob)
		}
	}
}

func TestCanvasAgentMessage_TimestampWireTagCasing(t *testing.T) {
	// Timestamp uses "timestamp" on the wire (lower-case). Pin the
	// casing so a refactor that upper-cases the Go field doesn't
	// regress the JSON tag. Zero-value time.Time still emits via
	// encoding/json — that's the runtime's behavior, not a bug we
	// need to fix, but callers relying on "omitempty" should know
	// the timestamp always ships.
	msg := CanvasAgentMessage{
		Role:      "assistant",
		Content:   "sure",
		Timestamp: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
	}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(blob), `"timestamp":"2026-04-23T12:00:00Z"`) {
		t.Errorf("expected lowercase timestamp tag with RFC3339 value; got:\n%s", blob)
	}
}

func TestCanvasAgentMessage_AttachmentsRoundTrip(t *testing.T) {
	msg := CanvasAgentMessage{
		Role:    "user",
		Content: "see attached reference",
		Attachments: []CanvasAgentAttachment{
			{Type: "image", URL: "/uploads/ref.png", MimeType: "image/png", Filename: "ref.png"},
		},
	}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, tag := range []string{`"attachments":`, `"url":"/uploads/ref.png"`, `"filename":"ref.png"`} {
		if !strings.Contains(string(blob), tag) {
			t.Errorf("expected attachment tag %s in payload; got:\n%s", tag, blob)
		}
	}
	var round CanvasAgentMessage
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(round.Attachments) != 1 || round.Attachments[0].Filename != "ref.png" {
		t.Errorf("attachments round-trip lost: %+v", round.Attachments)
	}
}
