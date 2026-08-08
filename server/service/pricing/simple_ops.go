// Simple-shape pricing functions for ops that don't need per-model
// dispatch. Each one is small enough (5-15 LOC of math) that the
// per-model registry pattern from image.go would be overkill —
// these all collapse to a one-formula function.
//
// Group:
//   - upscale  : scale=4 OR enhanceFace → 5, else 3
//   - flat     : remove-bg (and any future flat-priced op) reads
//                from a constant table
//   - vectorize: detailLevel=="high" → 4, else 3 (case-sensitive,
//                matches legacy)
//
// All three are unified here so a future re-pricing for one
// doesn't drift across surfaces. Same posture as image.go: canvas
// and tools both call into this package.

package pricing

import (
	"errors"
	"strings"
)

// ──────────────────────────────────────────────────────────────────
// Upscale
// ──────────────────────────────────────────────────────────────────

// UpscaleQuoteInput is the input for QuoteUpscale.
type UpscaleQuoteInput struct {
	Scale       int
	EnhanceFace bool
}

// UpscaleQuote is the outcome of QuoteUpscale.
type UpscaleQuote struct {
	Credits int
}

// QuoteUpscale prices an image-upscale op: 5 credits when scale=4
// or enhanceFace is set, else 3. Matches legacy
// TOOL_IMAGE_UPSCALER behaviour byte-equal — pinned by
// pricing_catalog_settle_parity_test.go.
func QuoteUpscale(in UpscaleQuoteInput) UpscaleQuote {
	if in.Scale == 4 || in.EnhanceFace {
		return UpscaleQuote{Credits: 5}
	}
	return UpscaleQuote{Credits: 3}
}

// ──────────────────────────────────────────────────────────────────
// Flat ops (remove-bg + any future fixed-price op)
// ──────────────────────────────────────────────────────────────────

// FlatOp enumerates ops with a fixed credit cost independent of
// model / resolution / quality / etc. Today only remove-bg lives
// here; future flat-priced ops (e.g. "tile pattern", "color
// palette extract") would register a new entry.
type FlatOp string

const (
	FlatOpRemoveBg FlatOp = "remove-bg"
)

// flatOpCredits is the fixed-price table. Keep entries in the
// same place so a re-pricing is a one-line change.
var flatOpCredits = map[FlatOp]int{
	FlatOpRemoveBg: 4,
}

// ErrUnknownFlatOp surfaces when QuoteFlat is called with an op
// not registered above. Caller decides whether to 400 or default.
var ErrUnknownFlatOp = errors.New("pricing: unknown flat op")

// FlatQuote is the outcome of QuoteFlat.
type FlatQuote struct {
	Credits int
}

// QuoteFlat returns the registered flat credit cost for op.
// Returns ErrUnknownFlatOp for unregistered ops so callers can
// classify rather than silently coercing to 0.
func QuoteFlat(op FlatOp) (FlatQuote, error) {
	credits, ok := flatOpCredits[op]
	if !ok {
		return FlatQuote{}, ErrUnknownFlatOp
	}
	return FlatQuote{Credits: credits}, nil
}

// ──────────────────────────────────────────────────────────────────
// Vectorize
// ──────────────────────────────────────────────────────────────────

// VectorizeQuoteInput is the input for QuoteVectorize.
//
// DetailLevel is a free-form string ("low" / "medium" / "high");
// only "high" is priced specially. Empty/missing defaults to the
// medium tier — same as legacy TOOL_IMAGE_VECTORIZER which
// explicitly defaults `detail := "medium"` before comparing.
type VectorizeQuoteInput struct {
	DetailLevel string
}

// VectorizeQuote is the outcome of QuoteVectorize.
type VectorizeQuote struct {
	Credits int
}

// QuoteVectorize prices a vectorize op: 4 credits for
// detailLevel=="high", else 3. Case-sensitive comparison (legacy
// compares `detail == "high"` exactly; only trim is applied, so
// " high " hits the 4-credit tier but "HIGH" rolls down to 3).
// Pinned by pricing_catalog_settle_parity_test.go.
func QuoteVectorize(in VectorizeQuoteInput) VectorizeQuote {
	if strings.TrimSpace(in.DetailLevel) == "high" {
		return VectorizeQuote{Credits: 4}
	}
	return VectorizeQuote{Credits: 3}
}
