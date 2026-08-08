package workagent

import (
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

func newCtxWithHeader(name, value string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest("POST", "/", nil)
	if name != "" {
		req.Header.Set(name, value)
	}
	c.Request = req
	return c
}

func TestMintIdempotencyKey_PrefixedHeader(t *testing.T) {
	// Canvas surface: header present + prefix → "canvas_agent:HEADER".
	// Pin the prefix application so a refactor that inverted the
	// prefix order (HEADER:canvas_agent) would surface immediately.
	surface := stubSurface{tool: "canvas_agent", mode: "canvas", typ: "canvas"}
	stubWithPrefix := surfaceWithIdempotency{stubSurface: surface, header: "X-Canvas-Request-Id", prefix: "canvas_agent:"}
	c := newCtxWithHeader("X-Canvas-Request-Id", "abc123")

	var seq atomic.Uint64
	got := MintIdempotencyKey(c, stubWithPrefix, 42, &seq)
	want := "canvas_agent:abc123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMintIdempotencyKey_PlainHeader(t *testing.T) {
	// Workagent-style surface: header present + empty prefix →
	// header used raw. Matches the legacy workagent contract where
	// the X-Agent-Request-Id header is unique to that handler.
	surface := stubSurface{tool: "workagent", mode: "general", typ: "general_agent"}
	stubNoPrefix := surfaceWithIdempotency{stubSurface: surface, header: "X-Agent-Request-Id", prefix: ""}
	c := newCtxWithHeader("X-Agent-Request-Id", "raw-key-789")

	var seq atomic.Uint64
	got := MintIdempotencyKey(c, stubNoPrefix, 42, &seq)
	if got != "raw-key-789" {
		t.Errorf("got %q, want %q", got, "raw-key-789")
	}
}

func TestMintIdempotencyKey_MissingHeaderFallback(t *testing.T) {
	// No header → fallback shape "{tool}_{uid}_{nano}_{seq}". Pin the
	// shape so a regression that changed the separator (e.g. dashes)
	// is caught — the fallback key is parsed by the credit-reservation
	// audit pipeline.
	surface := stubSurface{tool: "canvas_agent", mode: "canvas", typ: "canvas"}
	stubWithPrefix := surfaceWithIdempotency{stubSurface: surface, header: "X-Canvas-Request-Id", prefix: "canvas_agent:"}
	c := newCtxWithHeader("", "")

	var seq atomic.Uint64
	got := MintIdempotencyKey(c, stubWithPrefix, 42, &seq)
	if !strings.HasPrefix(got, "canvas_agent_42_") {
		t.Errorf("fallback key should start with %q, got %q", "canvas_agent_42_", got)
	}
	// Two trailing tokens after the {tool}_{uid}_ prefix: nano + seq.
	// (Tool name may contain underscores — canvas_agent does.)
	suffix := strings.TrimPrefix(got, "canvas_agent_42_")
	suffixParts := strings.Split(suffix, "_")
	if len(suffixParts) != 2 {
		t.Errorf("fallback key suffix should have 2 underscore-separated tokens (nano, seq); got %d in %q", len(suffixParts), suffix)
	}
}

func TestMintIdempotencyKey_FallbackSeqMonotonic(t *testing.T) {
	// Two consecutive fallback mints in the same nanosecond must
	// produce different keys — that's why fallbackSeq.Add(1) is in
	// the format. Without it, same-nanosecond burst loses
	// reservations.
	surface := stubSurface{tool: "tool-x", mode: "x", typ: "x"}
	stubX := surfaceWithIdempotency{stubSurface: surface, header: "X-X", prefix: ""}
	c := newCtxWithHeader("", "")

	var seq atomic.Uint64
	a := MintIdempotencyKey(c, stubX, 1, &seq)
	b := MintIdempotencyKey(c, stubX, 1, &seq)
	if a == b {
		t.Errorf("two mints with same uid+seq counter should differ; both got %q", a)
	}
}

// surfaceWithIdempotency wraps stubSurface to add the idempotency
// methods without complicating the existing registry tests.
type surfaceWithIdempotency struct {
	stubSurface
	header string
	prefix string
}

func (s surfaceWithIdempotency) IdempotencyHeaderName() string { return s.header }
func (s surfaceWithIdempotency) IdempotencyKeyPrefix() string  { return s.prefix }
