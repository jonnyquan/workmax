package tools

// Pins canvasIdempotencyKey behavior. Feeds the reservation layer
// for canvas_optimize_prompt and canvas_agent — behaviour that isn't
// covered by the big canvas_api_test.go end-to-end suite because
// the SSE / agent paths are mocked above it.
//
// canvasIdempotencyKey contract: X-Canvas-Request-Id wins when non-blank
// (so a client retry lands on the same reservation), but the returned key
// is always prefixed with `tool:` so the same header sent against
// canvas_optimize_prompt / canvas_agent can't collide on the
// (uid, idempotency_key) unique index. When the header is absent we fall
// back to a server-generated key that encodes tool + uid + wall clock.

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newIdempotencyCtx(headerValue string) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/test", nil)
	if headerValue != "" {
		c.Request.Header.Set("X-Canvas-Request-Id", headerValue)
	}
	return c
}

func TestCanvasIdempotencyKey_HeaderScopedByTool(t *testing.T) {
	// Header is honored but always prefixed with `tool:` so the (uid, key)
	// unique index can't be punctured by a client reusing one
	// X-Canvas-Request-Id across two endpoints.
	c := newIdempotencyCtx("client-retry-abc-123")
	got := canvasIdempotencyKey(c, "canvas_img2img", 42)
	if got != "canvas_img2img:client-retry-abc-123" {
		t.Errorf("got %q, want %q", got, "canvas_img2img:client-retry-abc-123")
	}
}

func TestCanvasIdempotencyKey_HeaderTrimmed(t *testing.T) {
	// Leading/trailing whitespace is trimmed so a client accidentally
	// padding the header doesn't desync retries.
	c := newIdempotencyCtx("  padded-key  ")
	if got := canvasIdempotencyKey(c, "tool", 1); got != "tool:padded-key" {
		t.Errorf("got %q, want %q", got, "tool:padded-key")
	}
}

func TestCanvasIdempotencyKey_DifferentToolsSameHeaderDoNotCollide(t *testing.T) {
	// The reason this prefixing exists: if the same X-Canvas-Request-Id
	// reaches canvas_chat and canvas_optimize_prompt (both real call sites
	// in canvas_chat_agent_api.go), the second Reserve would idempotency-hit
	// the first reservation and short-circuit on the wrong tool / cost.
	c := newIdempotencyCtx("shared-id")
	chat := canvasIdempotencyKey(c, "canvas_chat", 7)
	opt := canvasIdempotencyKey(c, "canvas_optimize_prompt", 7)
	if chat == opt {
		t.Errorf("two tools with the same header produced the same key %q — would collide on (uid, idempotency_key)", chat)
	}
}

func TestCanvasIdempotencyKey_BlankHeaderFallsBack(t *testing.T) {
	// Whitespace-only or missing header → server-generated fallback.
	// The fallback encodes tool + uid + nanosecond timestamp so two
	// concurrent calls for the same user never collide.
	c1 := newIdempotencyCtx("   ")
	got1 := canvasIdempotencyKey(c1, "canvas_optimize_prompt", 42)
	prefix := "canvas_optimize_prompt_42_"
	if !strings.HasPrefix(got1, prefix) {
		t.Errorf("got %q, want prefix %q", got1, prefix)
	}

	c2 := newIdempotencyCtx("")
	got2 := canvasIdempotencyKey(c2, "canvas_optimize_prompt", 42)
	if got2 == got1 {
		t.Errorf("fallback keys should differ per call, got identical %q", got1)
	}
	if !strings.HasPrefix(got2, prefix) {
		t.Errorf("got %q, want prefix %q", got2, prefix)
	}
}

func TestCanvasIdempotencyKey_FallbackEncodesToolAndUid(t *testing.T) {
	// Changing tool or uid must produce a different prefix even if
	// headers are absent — the reservation layer uses the key verbatim.
	c := newIdempotencyCtx("")
	a := canvasIdempotencyKey(c, "tool-a", 1)
	b := canvasIdempotencyKey(c, "tool-b", 1)
	cc := canvasIdempotencyKey(c, "tool-a", 2)

	if !strings.HasPrefix(a, "tool-a_1_") {
		t.Errorf("a prefix = %q", a)
	}
	if !strings.HasPrefix(b, "tool-b_1_") {
		t.Errorf("b prefix = %q", b)
	}
	if !strings.HasPrefix(cc, "tool-a_2_") {
		t.Errorf("cc prefix = %q", cc)
	}
}

// Make sure the fallback format stays stable — a change to the format
// string would break any external system that logs/audits the key and
// parses it back out.
func TestCanvasIdempotencyKey_FallbackFormat(t *testing.T) {
	c := newIdempotencyCtx("")
	got := canvasIdempotencyKey(c, "foo", 7)
	parts := strings.Split(got, "_")
	if len(parts) < 3 {
		t.Fatalf("got %q, want at least 3 underscore-separated parts", got)
	}
	// Trailing parts are a nanosecond stamp, not asserting the value —
	// just the shape.
	if fmt.Sprintf("%s_%s", parts[0], parts[1]) != "foo_7" {
		t.Errorf("got prefix %q_%q, want foo_7", parts[0], parts[1])
	}
}
