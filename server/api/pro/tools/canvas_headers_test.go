package tools

import (
	"net/http/httptest"
	"testing"

	"server/model"

	"github.com/gin-gonic/gin"
)

// Tests for parseCanvasAssetBindingsHeader — the shared header parser
// that canvas_generation_api.Img2Img and the generation handlers depend
// on to forward the active element's asset bindings to the injector.
// A drift here silently drops the binding context, so the invariants
// are pinned here. (parseCanvasProjectHeader is already covered by
// TestParseCanvasProjectHeader in canvas_api_test.go.)

func newCanvasHeaderCtx(key, value string) *gin.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest("GET", "/", nil)
	if key != "" {
		req.Header.Set(key, value)
	}
	ctx.Request = req
	return ctx
}

func TestParseCanvasAssetBindingsHeader(t *testing.T) {
	t.Run("absent header returns nil", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("", "")
		if got := parseCanvasAssetBindingsHeader(ctx); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("blank header returns nil", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", "   ")
		if got := parseCanvasAssetBindingsHeader(ctx); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("invalid JSON returns nil", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", "{not json")
		if got := parseCanvasAssetBindingsHeader(ctx); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	// An all-empty binding is indistinguishable from "no binding" —
	// the injector should treat it as a no-op rather than a signal
	// to clear existing bindings. Parser mirrors that by returning
	// nil so downstream code has a single empty case.
	t.Run("all-empty binding returns nil", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", `{"scope":"","characterIds":[],"brandIds":[],"productIds":[]}`)
		if got := parseCanvasAssetBindingsHeader(ctx); got != nil {
			t.Fatalf("expected nil for empty binding, got %#v", got)
		}
	})

	t.Run("scope-only binding parses", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", `{"scope":"shot"}`)
		got := parseCanvasAssetBindingsHeader(ctx)
		if got == nil {
			t.Fatalf("expected parsed binding, got nil")
		}
		if got.Scope != model.AssetScopeShot {
			t.Fatalf("expected scope=shot, got %q", got.Scope)
		}
	})

	t.Run("character binding parses", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", `{"characterIds":[1,2]}`)
		got := parseCanvasAssetBindingsHeader(ctx)
		if got == nil {
			t.Fatalf("expected parsed binding, got nil")
		}
		if len(got.CharacterIDs) != 2 || got.CharacterIDs[0] != 1 || got.CharacterIDs[1] != 2 {
			t.Fatalf("unexpected characters: %#v", got.CharacterIDs)
		}
	})

	// Sanity: the returned type is the same AssetBinding model the
	// injector consumes, so any future field additions (e.g. an
	// operation enum) flow through without new parser changes.
	t.Run("returned type matches model.AssetBinding", func(t *testing.T) {
		ctx := newCanvasHeaderCtx("X-Canvas-Asset-Bindings", `{"scope":"canvas"}`)
		var got *model.AssetBinding = parseCanvasAssetBindingsHeader(ctx)
		if got == nil {
			t.Fatalf("expected non-nil")
		}
	})

	// Parse-time sanitisation defense layer: a client cannot forge
	// out-of-range character weights into the persisted TaskRequestData.
	// The injector's weightFor still clamps at consumption, but those
	// untrusted values would otherwise survive into stored task rows
	// and any downstream consumer (analytics, dashboards, audit) that
	// did not re-clamp would see them.
	t.Run("out-of-range character weights are clamped at parse time", func(t *testing.T) {
		ctx := newCanvasHeaderCtx(
			"X-Canvas-Asset-Bindings",
			`{"scope":"element","characterIds":[1,2],"characterWeights":{"1":99,"2":0.001}}`,
		)
		got := parseCanvasAssetBindingsHeader(ctx)
		if got == nil {
			t.Fatalf("expected parsed binding, got nil")
		}
		if w := got.CharacterWeights["1"]; w != model.AssetWeightMax {
			t.Fatalf("weights[1] = %v, want %v (clamped to ceiling)", w, model.AssetWeightMax)
		}
		if w := got.CharacterWeights["2"]; w != model.AssetWeightMin {
			t.Fatalf("weights[2] = %v, want %v (clamped to floor)", w, model.AssetWeightMin)
		}
	})

	// Non-finite weight handling lives in the model-level unit
	// test (TestAssetBindingSanitize/drops non-finite weights):
	// Go's encoding/json refuses to unmarshal `1e9999` or other
	// IEEE-754-overflow literals, so a non-finite value cannot
	// physically reach the header parser. The Sanitize layer still
	// guards in case a different code path constructs a binding
	// with NaN/Inf in memory.

	t.Run("non-integer weight keys are dropped at parse time", func(t *testing.T) {
		// The injector keys character weight lookups by strconv.Itoa(id);
		// a key like "abc" cannot reference any real character row.
		// Drop now so the persisted task row does not carry the noise.
		ctx := newCanvasHeaderCtx(
			"X-Canvas-Asset-Bindings",
			`{"scope":"element","characterIds":[1],"characterWeights":{"1":1.2,"abc":1.5}}`,
		)
		got := parseCanvasAssetBindingsHeader(ctx)
		if got == nil {
			t.Fatalf("expected parsed binding, got nil")
		}
		if _, ok := got.CharacterWeights["abc"]; ok {
			t.Fatalf("non-integer key should have been dropped, got %#v", got.CharacterWeights)
		}
	})
}
