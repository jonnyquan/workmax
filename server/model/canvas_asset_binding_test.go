package model

import (
	"math"
	"testing"
)

// AssetBinding.Sanitize is the parse-time defense layer for the
// X-Canvas-Asset-Bindings header (and any other untrusted source that
// builds a binding from raw JSON). Callers downstream — the injector's
// weightFor and the prompt assembly path — also clamp at consumption,
// but Sanitize keeps malformed values out of the persisted
// TaskRequestData payload. Drift here is a silent data-hygiene
// regression that would surface only in stored task rows.

func TestAssetBindingSanitize(t *testing.T) {
	t.Run("nil receiver is a no-op", func(t *testing.T) {
		var b *AssetBinding
		// Must not panic. Sanitize is called from header parsers that
		// may have legitimately returned a nil binding.
		b.Sanitize()
	})

	t.Run("clamps out-of-range character weights", func(t *testing.T) {
		b := &AssetBinding{
			CharacterWeights: AssetWeightMap{
				"1": 0.05, // below floor
				"2": 5.0,  // above ceiling
				"3": 1.5,  // in range, untouched
			},
		}
		b.Sanitize()
		if got := b.CharacterWeights["1"]; got != AssetWeightMin {
			t.Fatalf("weights[1] = %v, want %v", got, AssetWeightMin)
		}
		if got := b.CharacterWeights["2"]; got != AssetWeightMax {
			t.Fatalf("weights[2] = %v, want %v", got, AssetWeightMax)
		}
		if got := b.CharacterWeights["3"]; got != 1.5 {
			t.Fatalf("weights[3] = %v, want 1.5 untouched", got)
		}
	})

	t.Run("drops non-finite weights (NaN, +Inf, -Inf)", func(t *testing.T) {
		// Non-finite values cannot be JSON-encoded as standard
		// numbers, but a client could craft a string like
		// `{"characterWeights": {"1": 1e9999}}` which parses to +Inf
		// on the Go side. Dropping rather than clamping is correct —
		// these values carry no real signal about intent.
		b := &AssetBinding{
			CharacterWeights: AssetWeightMap{
				"1": math.NaN(),
				"2": math.Inf(1),
				"3": math.Inf(-1),
				"4": 1.2,
			},
		}
		b.Sanitize()
		if _, ok := b.CharacterWeights["1"]; ok {
			t.Fatalf("NaN entry should be dropped")
		}
		if _, ok := b.CharacterWeights["2"]; ok {
			t.Fatalf("+Inf entry should be dropped")
		}
		if _, ok := b.CharacterWeights["3"]; ok {
			t.Fatalf("-Inf entry should be dropped")
		}
		if got := b.CharacterWeights["4"]; got != 1.2 {
			t.Fatalf("finite entry should survive, got %v", got)
		}
	})

	t.Run("drops non-integer keys (forged map keys)", func(t *testing.T) {
		// Valid keys are stringified ids — `strconv.Itoa(id)`. A
		// client that sends `"abc"` or `"1.5"` as a key cannot be
		// pointing at a real id row; the injector would ignore it
		// anyway. Drop now so persisted payloads stay clean.
		b := &AssetBinding{
			CharacterWeights: AssetWeightMap{
				"1":    1.0,
				"abc":  1.5,
				"1.5":  1.5,
				"":     1.5,
				"-2":   1.0, // negative ids are not produced by the FE,
				// but strconv.Atoi accepts them and the injector simply
				// won't match any row. Keep this for symmetry — see
				// `weightFor` which keys off strconv.Itoa(id).
			},
		}
		b.Sanitize()
		if _, ok := b.CharacterWeights["abc"]; ok {
			t.Fatalf("non-integer key 'abc' should be dropped")
		}
		if _, ok := b.CharacterWeights["1.5"]; ok {
			t.Fatalf("non-integer key '1.5' should be dropped")
		}
		if _, ok := b.CharacterWeights[""]; ok {
			t.Fatalf("empty key should be dropped")
		}
		if _, ok := b.CharacterWeights["1"]; !ok {
			t.Fatalf("integer key '1' should survive")
		}
		if _, ok := b.CharacterWeights["-2"]; !ok {
			t.Fatalf("integer key '-2' should survive (strconv.Atoi accepts negatives)")
		}
	})

	t.Run("empty maps after cleanup become nil (omitempty wire shape)", func(t *testing.T) {
		// JSON `omitempty` only treats nil maps as absent; an empty
		// non-nil map still serializes as `{}`. Sanitize must leave
		// the field nil when every entry was rejected so the
		// persisted task row does not store an empty `{}`.
		b := &AssetBinding{
			CharacterWeights: AssetWeightMap{
				"abc": 1.0,
				"def": math.NaN(),
			},
		}
		b.Sanitize()
		if b.CharacterWeights != nil {
			t.Fatalf("expected nil after all entries dropped, got %#v", b.CharacterWeights)
		}
	})

	t.Run("sanitizes brand and product weights symmetrically", func(t *testing.T) {
		// Brand/product channels are forward-compat parsers today,
		// but Sanitize must treat them the same as characters so
		// re-introducing those channels later does not need a second
		// hardening pass.
		b := &AssetBinding{
			BrandWeights:   AssetWeightMap{"1": 99.0},
			ProductWeights: AssetWeightMap{"1": math.NaN()},
		}
		b.Sanitize()
		if got := b.BrandWeights["1"]; got != AssetWeightMax {
			t.Fatalf("brand weight not clamped: got %v want %v", got, AssetWeightMax)
		}
		if b.ProductWeights != nil {
			t.Fatalf("product weights with only NaN should become nil, got %#v", b.ProductWeights)
		}
	})

	t.Run("leaves id slices and scope alone", func(t *testing.T) {
		// Sanitize is value-only — the id list is the authoritative
		// binding shape. A regression that filtered id slices here
		// would silently drop user-bound characters.
		projectID := uint64(42)
		b := &AssetBinding{
			Scope:           AssetScopeShot,
			CharacterIDs:    []int{1, 2, 3},
			BrandIDs:        []int{7},
			CanvasProjectID: &projectID,
		}
		b.Sanitize()
		if b.Scope != AssetScopeShot {
			t.Fatalf("scope mutated: %q", b.Scope)
		}
		if len(b.CharacterIDs) != 3 {
			t.Fatalf("character ids mutated: %#v", b.CharacterIDs)
		}
		if len(b.BrandIDs) != 1 {
			t.Fatalf("brand ids mutated: %#v", b.BrandIDs)
		}
		if b.CanvasProjectID == nil || *b.CanvasProjectID != 42 {
			t.Fatalf("project id mutated: %#v", b.CanvasProjectID)
		}
	})
}
