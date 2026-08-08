package canvas

// types_test.go pins SSE constants and the headline wire-tag matrix
// (request roundtrip, omitempty for optional fields, lowercase
// timestamp casing). These fill the quieter invariants a silent
// regression would slip past:
//
//   • SSE constant values are all-lowercase canonical SSE strings.
//     Pin so a refactor to PascalCase ("Done"/"Error") would surface
//     — the frontend SSE reader switches on raw string equality.
//
//   • SSE constants are non-empty strings. Empty-string drift would
//     route every event into the default branch silently — the SSE
//     stream would render nothing in the UI without a single error.
//
//   • Messages field has NO omitempty — a nil Messages slice marshals
//     as `"messages":null`, NOT omitted. Pin since a refactor that
//     added omitempty would change the on-wire envelope shape and
//     break a strict frontend that expects the key to always exist.
//
//   • Elements/SelectedIDs/Viewport in CanvasAgentContext also have
//     no omitempty. An empty `[]CanvasAgentMessage{}` marshals as
//     `[]`, NOT null. Pin both the nil-vs-empty distinction and
//     the always-present-key contract.
//
//   • CanvasAgentMessage.Timestamp has omitempty BUT the zero-value
//     time.Time is NOT omitted (encoding/json's omitempty does not
//     recognise a struct's zero value — only typed-nil pointers,
//     zero-len slices/maps, falsy primitives). Pin this Go-stdlib
//     quirk so a refactor flipping Timestamp to *time.Time would
//     surface (the new behavior — empty pointer omitted — is a
//     deliberate change, but should be visible).
//
//   • Numeric values in Viewport / Metadata round-trip as float64
//     (encoding/json's default for `any`). Pin so a refactor that
//     replaced map[string]any with a strongly-typed Viewport struct
//     would show up here as a different concrete type after decode.
//
//   • Unknown JSON fields are tolerated on unmarshal (Go default;
//     no DisallowUnknownFields). Pin so a refactor enabling strict
//     mode would surface — the frontend may add forward-compatible
//     fields, and the Go side must keep accepting them silently.
//
//   • Non-ASCII content (Chinese, emoji) survives round-trip
//     verbatim. Pin so a refactor that added an HTML-escape pass
//     (json.Encoder.SetEscapeHTML(false) is the OPT-OUT, not the
//     default) wouldn't slip in mojibake without surfacing here.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSSEType_AllLowercase(t *testing.T) {
	for _, v := range []string{SSETypeMessage, SSETypeBlock, SSETypeFile, SSETypeDone, SSETypeError} {
		if v != strings.ToLower(v) {
			t.Errorf("SSE constant %q is not lowercase — protocol break risk", v)
		}
	}
}

func TestSSEType_AllNonEmpty(t *testing.T) {
	// Empty-string drift would silently route the event into the
	// frontend's default branch — visible only as "no UI update".
	for name, v := range map[string]string{
		"message": SSETypeMessage,
		"block":   SSETypeBlock,
		"file":    SSETypeFile,
		"done":    SSETypeDone,
		"error":   SSETypeError,
	} {
		if v == "" {
			t.Errorf("SSE constant %s is empty", name)
		}
	}
}

func TestCanvasAgentChatRequest_NilMessagesMarshalsAsNull(t *testing.T) {
	// `Messages` has json:"messages" with NO omitempty. Nil slice → "null".
	req := CanvasAgentChatRequest{
		Messages: nil,
		Context:  CanvasAgentContext{},
	}
	blob, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(blob), `"messages":null`) {
		t.Errorf(`expected "messages":null for nil slice; got: %s`, blob)
	}
}

func TestCanvasAgentChatRequest_EmptyMessagesMarshalsAsEmptyArray(t *testing.T) {
	// `Messages: []CanvasAgentMessage{}` is empty (not nil) → "[]".
	req := CanvasAgentChatRequest{
		Messages: []CanvasAgentMessage{},
		Context:  CanvasAgentContext{},
	}
	blob, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(blob), `"messages":[]`) {
		t.Errorf(`expected "messages":[] for empty slice; got: %s`, blob)
	}
}

func TestCanvasAgentContext_RequiredFieldsAlwaysShipped(t *testing.T) {
	// elements / selectedIds / viewport have NO omitempty — the
	// keys must appear even when nil/empty.
	ctx := CanvasAgentContext{}
	blob, err := json.Marshal(ctx)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	for _, tag := range []string{`"elements":`, `"selectedIds":`, `"viewport":`} {
		if !strings.Contains(string(blob), tag) {
			t.Errorf("expected required tag %s to be present; got: %s", tag, blob)
		}
	}
}

func TestCanvasAgentMessage_ZeroTimestampStillShips(t *testing.T) {
	// time.Time zero-value is NOT recognised by omitempty (encoding/json
	// does not check struct zero-values). The wire emits "0001-01-01T00:00:00Z".
	// Pin the stdlib quirk so a refactor flipping Timestamp to *time.Time
	// would surface as a deliberate behavioral change.
	msg := CanvasAgentMessage{Role: "user", Content: "hi"}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(blob), `"timestamp":"0001-01-01T00:00:00Z"`) {
		t.Errorf("expected zero-time stamp to STILL ship (omitempty does not catch struct zero); got: %s", blob)
	}
}

func TestCanvasAgentContext_NumericValuesDecodeAsFloat64(t *testing.T) {
	// JSON numbers in map[string]any decode as float64 by default.
	// Pin so a refactor switching to a strongly-typed Viewport would
	// surface as a type-mismatch on the existing accessor.
	src := `{"elements":[],"selectedIds":[],"viewport":{"x":42,"zoom":1.5}}`
	var ctx CanvasAgentContext
	if err := json.Unmarshal([]byte(src), &ctx); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	v, ok := ctx.Viewport["x"].(float64)
	if !ok {
		t.Fatalf("expected viewport.x to decode as float64, got %T", ctx.Viewport["x"])
	}
	if v != 42 {
		t.Errorf("viewport.x = %v, want 42", v)
	}
	z, ok := ctx.Viewport["zoom"].(float64)
	if !ok || z != 1.5 {
		t.Errorf("viewport.zoom = %v (%T), want 1.5 (float64)", ctx.Viewport["zoom"], ctx.Viewport["zoom"])
	}
}

func TestCanvasAgentChatRequest_UnknownFieldsTolerated(t *testing.T) {
	// Forward-compat: a frontend that ships a future "telemetry" key
	// must not break the Go decoder. Default json.Unmarshal ignores
	// unknown fields; pin so a refactor enabling strict mode would
	// surface here.
	src := `{"messages":[{"role":"user","content":"hi"}],"context":{},"futureKey":"value"}`
	var req CanvasAgentChatRequest
	if err := json.Unmarshal([]byte(src), &req); err != nil {
		t.Fatalf("unknown field caused decode failure (strict mode regression?): %v", err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Content != "hi" {
		t.Errorf("known fields lost on decode: %+v", req)
	}
}

func TestCanvasAgentMessage_NonASCIIRoundtripsVerbatim(t *testing.T) {
	// Encoder default keeps UTF-8 verbatim; HTML-escape would mangle
	// `<`/`>`/`&` but doesn't touch CJK. Pin Chinese + emoji round-trip
	// to lock the existing behavior — a refactor flipping the encoder
	// would surface immediately.
	msg := CanvasAgentMessage{
		Role:      "assistant",
		Content:   "你好 🎬 世界",
		Timestamp: time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC),
	}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var round CanvasAgentMessage
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if round.Content != msg.Content {
		t.Errorf("non-ASCII content round-trip lost: got %q, want %q", round.Content, msg.Content)
	}
}

func TestCanvasAgentChatRequest_HTMLSpecialCharsAreEscaped(t *testing.T) {
	// Default encoder DOES escape HTML special chars (<, >, &) for
	// safety against script injection in HTML contexts. Pin the
	// default so a refactor that opted INTO SetEscapeHTML(false) for
	// "raw output" would surface here as a deliberate change.
	msg := CanvasAgentMessage{Role: "user", Content: "<script>&"}
	blob, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	// Default encoder emits \u003c/\u003e/\u0026 (Unicode escapes) — the
	// raw <, >, & must NOT appear in the output.
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if !bytes.Contains(blob, []byte(esc)) {
			t.Errorf("expected unicode-escape %s in output; got: %s", esc, blob)
		}
	}
	for _, raw := range []string{"<", ">", "&"} {
		if bytes.Contains(blob, []byte(raw)) {
			t.Errorf("expected raw %q to be escaped; got: %s", raw, blob)
		}
	}
}
