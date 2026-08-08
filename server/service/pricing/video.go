// Video pricing — types + per-model formulas + currency conversions.
// Migrated from server/service/tools/video_models.go in the
// 2026-05-15 pricing unification (step 4/N).
//
// Why this lives here (and not in tools alongside the capability
// registry): pricing math is the SAME concept whether it's image or
// video — both reduce to (model + params → credits). Keeping image
// pricing in pricing/ but video pricing in tools/ was a historical
// accident, not a principled design. The 2026-05-15 refactor
// removes that asymmetry.
//
// What this file does NOT own:
//
//   - VideoModelCapabilities struct + the capability registry stay
//     in tools. Capabilities are an FE-wire concern (the API
//     handler ListVideoModelCapabilities serialises them); pricing
//     reads at most the per-model PricingStatus (which lives here)
//     for its return value.
//
//   - NormalizeVideoQuoteOptions stays in tools because it reads
//     the capability registry (durations / resolutions / motion
//     modes — UI/capability concerns). The tools-side
//     QuoteVideoCredits wrapper normalises BEFORE calling
//     pricing.QuoteVideo, so this package never has to touch
//     capabilities.
//
//   - ValidateVideoRequest stays in tools — pure capability
//     validation, not pricing.

package pricing

import (
	"fmt"
	"math"
	"strings"

	"server/globals"
	"server/model"
)

const (
	defaultCreditPackUnitPriceUSD = 0.02
	videoReservedProfitMargin     = 0.30
	legacyMinimaxCreditsPerPoint  = 4.0
)

// VideoModelPricingStatus tags a video model's pricing as canonical
// or estimated. "Official" means the rate was sourced from the
// provider's public pricing page; "estimated" means we extrapolated
// (e.g. Seedance 2 Fast — Volcengine hadn't published a dedicated
// rate when the model launched, so we hold a conservative number
// until they do).
//
// The capability registry's `PricingStatus` field on
// VideoModelCapabilities uses this type and pulls its values from
// videoModelPricingStatus below — single source of truth.
type VideoModelPricingStatus string

const (
	VideoPricingOfficial  VideoModelPricingStatus = "official"
	VideoPricingEstimated VideoModelPricingStatus = "estimated"
)

// videoModelPricingStatus is the per-model pricing-status table.
// Read by VideoPricingStatusFor and by the tools-side capability
// registry. Adding a new video model registers an entry here AND
// adds a switch arm in QuoteVideo below.
var videoModelPricingStatus = map[string]VideoModelPricingStatus{
	model.KLING_2_6:       VideoPricingOfficial,
	model.SORA_2:          VideoPricingOfficial,
	model.VEO_31:          VideoPricingOfficial,
	model.VEO_31_FAST:     VideoPricingOfficial,
	model.SEEDANCE:        VideoPricingOfficial,
	model.SEEDANCE_2:      VideoPricingOfficial,
	model.SEEDANCE_2_FAST: VideoPricingEstimated,
	model.MINIMAX_VIDEO:   VideoPricingOfficial,
}

// VideoPricingStatusFor returns the registered pricing-status for
// a model. Unknown models fall back to "estimated" — the
// conservative default ensures we never claim "official" pricing
// for a model whose rate we haven't actually sourced.
func VideoPricingStatusFor(modelID string) VideoModelPricingStatus {
	if status, ok := videoModelPricingStatus[modelID]; ok {
		return status
	}
	return VideoPricingEstimated
}

// VideoQuoteOptions is the input shape for QuoteVideo. Mirrors the
// historical tools.VideoQuoteOptions byte-for-byte — tools keeps a
// type alias so existing call sites work without edits.
type VideoQuoteOptions struct {
	Duration          int
	Resolution        string
	AspectRatio       string
	MotionMode        string
	MotionOrientation string
	HasStartFrame     bool
	HasEndFrame       bool
}

// VideoCreditQuote is the outcome of QuoteVideo. JSON tags preserved
// from the historical tools.VideoCreditQuote so the wire shape is
// unchanged for any handler that serialises this struct directly.
type VideoCreditQuote struct {
	Model            string                  `json:"model"`
	Duration         int                     `json:"duration"`
	Resolution       string                  `json:"resolution,omitempty"`
	Credits          int                     `json:"credits"`
	PricingStatus    VideoModelPricingStatus `json:"pricingStatus"`
	BillableDuration int                     `json:"billableDuration,omitempty"`
	BillableRate     string                  `json:"billableRate,omitempty"`
}

// QuoteVideo prices a video generation. Caller must pass already-
// normalised options (use tools.NormalizeVideoQuoteOptions; that
// helper lives next to the capability registry it reads from).
//
// Returns an error only for unrecognised models — every other shape
// (zero duration, missing resolution, etc.) was supposed to be
// fixed by Normalize; if the caller skipped that step we
// gracefully default through the per-model formulas.
//
// Implementations mirror the legacy tools.QuoteVideoCredits
// byte-equal. Unit tests in pricing/video_test.go pin the per-
// model formulas; the parity test (executed via the canvas test
// surface) covers the cross-implementation boundary.
func QuoteVideo(modelID string, opts VideoQuoteOptions) (VideoCreditQuote, error) {
	status, knownModel := videoModelPricingStatus[modelID]
	if !knownModel {
		// Unknown model — surface as error so callers don't
		// silently produce a default-rate quote.
		return VideoCreditQuote{}, fmt.Errorf("unsupported video model: %s", modelID)
	}
	quote := VideoCreditQuote{
		Model:         modelID,
		Duration:      opts.Duration,
		Resolution:    opts.Resolution,
		PricingStatus: status,
	}

	switch modelID {
	case model.KLING_2_6:
		rate := 0.084
		if strings.EqualFold(opts.MotionMode, "pro") {
			rate = 0.112
		}
		if opts.HasStartFrame || opts.HasEndFrame {
			if strings.EqualFold(opts.MotionMode, "pro") {
				rate = 0.168
			} else {
				rate = 0.126
			}
		}
		quote.BillableDuration = opts.Duration
		quote.BillableRate = fmt.Sprintf("$%.3f/s", rate)
		quote.Credits = usdToCredits(rate * float64(opts.Duration))
	case model.SORA_2:
		rate := 0.10
		quote.BillableDuration = opts.Duration
		quote.BillableRate = "$0.10/s"
		quote.Credits = usdToCredits(rate * float64(opts.Duration))
	case model.VEO_31, model.VEO_31_FAST:
		billableDuration := normalizeVeoBillableDuration(opts.Duration, opts.Resolution, opts.HasStartFrame || opts.HasEndFrame)
		// Veo 3.1 Fast is the cheaper upstream variant (same capability
		// surface, lower rate). Both tiers double on 4K. Standard rate
		// table mirrors Vertex AI public pricing as of 2026-05.
		isFast := modelID == model.VEO_31_FAST
		is4K := strings.EqualFold(opts.Resolution, "4k")
		var rate float64
		switch {
		case isFast && is4K:
			rate = 0.20
		case isFast:
			rate = 0.10
		case is4K:
			rate = 0.40
		default:
			rate = 0.20
		}
		quote.BillableDuration = billableDuration
		quote.BillableRate = fmt.Sprintf("$%.2f/s", rate)
		quote.Credits = usdToCredits(rate * float64(billableDuration))
	case model.SEEDANCE:
		priceRMB := seedancePriceRMB(opts.Duration, opts.Resolution)
		quote.BillableDuration = opts.Duration
		quote.BillableRate = fmt.Sprintf("¥%.3f/s", priceRMB/float64(opts.Duration))
		quote.Credits = rmbToCredits(priceRMB)
	case model.SEEDANCE_2:
		priceRMB := seedance2PriceRMB(opts.Duration, opts.Resolution)
		quote.BillableDuration = opts.Duration
		quote.BillableRate = fmt.Sprintf("¥%.3f/s", priceRMB/float64(opts.Duration))
		quote.Credits = rmbToCredits(priceRMB)
	case model.SEEDANCE_2_FAST:
		priceRMB := seedance2FastPriceRMB(opts.Duration, opts.Resolution)
		quote.BillableDuration = opts.Duration
		quote.BillableRate = fmt.Sprintf("¥%.3f/s", priceRMB/float64(opts.Duration))
		quote.Credits = rmbToCredits(priceRMB)
	case model.MINIMAX_VIDEO:
		points := minimaxPoints(opts.Duration, opts.Resolution)
		quote.BillableDuration = opts.Duration
		quote.BillableRate = fmt.Sprintf("%.1f points", points)
		quote.Credits = minimaxPointsToCredits(points)
	default:
		// Belt-and-suspenders: should be impossible given the
		// known-model gate above, but defend against
		// videoModelPricingStatus and the switch drifting.
		quote.Credits = 35
	}
	quote.Credits = normalizeQuotedVideoCredits(quote.Credits)
	if quote.Credits < 1 {
		quote.Credits = 1
	}
	return quote, nil
}

// NormalizeVeoBillableDuration is exported for the tools-side
// normalize function to share with pricing. Both
// NormalizeVideoQuoteOptions (in tools) and QuoteVideo (here) need
// the same duration tier table; living in pricing keeps it next to
// the per-model rate it influences.
func NormalizeVeoBillableDuration(duration int, resolution string, hasReference bool) int {
	return normalizeVeoBillableDuration(duration, resolution, hasReference)
}

func normalizeVeoBillableDuration(duration int, resolution string, hasReference bool) int {
	if hasReference || strings.EqualFold(resolution, "4k") || strings.EqualFold(resolution, "1080p") {
		return 8
	}
	switch duration {
	case 4:
		return 4
	case 5, 6:
		return 6
	case 8, 10:
		return 8
	default:
		if duration < 5 {
			return 4
		}
		if duration < 8 {
			return 6
		}
		return 8
	}
}

func seedancePriceRMB(duration int, resolution string) float64 {
	perFiveSeconds := map[string]float64{
		"480p":  0.40,
		"720p":  0.86,
		"1080p": 1.94,
	}
	unit, ok := perFiveSeconds[strings.ToLower(strings.TrimSpace(resolution))]
	if !ok {
		unit = perFiveSeconds["720p"]
	}
	factor := float64(duration) / 5.0
	return unit * factor
}

// seedance2PriceRMB implements the official Seedance 2.0 API pricing:
// 46 RMB per million output tokens, which Volcengine benchmarks to
// ~1 RMB/s at 720p pure text-to-video. 480p is scaled by relative
// pixel count (409,920 / 921,600 ≈ 0.445) since Volcengine has not
// published a separate per-resolution rate. 1080p is not supported
// by the model.
func seedance2PriceRMB(duration int, resolution string) float64 {
	perSecond := map[string]float64{
		"480p": 0.45,
		"720p": 1.00,
	}
	unit, ok := perSecond[strings.ToLower(strings.TrimSpace(resolution))]
	if !ok {
		unit = perSecond["720p"]
	}
	if duration <= 0 {
		duration = 5
	}
	return unit * float64(duration)
}

// seedance2FastPriceRMB estimates Seedance 2 Fast at roughly half
// the runtime cost of the standard model. Volcengine has not
// published a dedicated rate; this is a conservative estimate that
// keeps credit quotes honest until official pricing is available.
func seedance2FastPriceRMB(duration int, resolution string) float64 {
	perSecond := map[string]float64{
		"480p": 0.25,
		"720p": 0.55,
	}
	unit, ok := perSecond[strings.ToLower(strings.TrimSpace(resolution))]
	if !ok {
		unit = perSecond["720p"]
	}
	if duration <= 0 {
		duration = 5
	}
	return unit * float64(duration)
}

func minimaxPoints(duration int, resolution string) float64 {
	normalized := strings.ToLower(strings.TrimSpace(resolution))
	switch {
	case normalized == "512p" && duration <= 6:
		return 0.3
	case normalized == "512p":
		return 0.5
	case normalized == "768p" && duration <= 6:
		return 1.0
	case normalized == "768p":
		return 2.0
	default:
		return 2.0
	}
}

func usdToCredits(usd float64) int {
	usdPerCredit := effectiveVideoCostPerCreditUSD()
	return int(math.Ceil(usd / usdPerCredit))
}

func rmbToCredits(rmb float64) int {
	const usdCny = 7.2
	return usdToCredits(rmb / usdCny)
}

func minimaxPointsToCredits(points float64) int {
	multiplier := legacyMinimaxCreditsPerPoint / (1 - videoReservedProfitMargin)
	return int(math.Ceil(points * multiplier))
}

// effectiveVideoCostPerCreditUSD reads the lowest credit-pack
// unit price from globals.GraConf.Stripe.CreditPacks and applies
// the reserved profit margin. This is config-aware in the same
// way image's BaseImageCredits is — admin-side credit pack
// configuration shifts video pricing automatically.
func effectiveVideoCostPerCreditUSD() float64 {
	unitPrice := lowestCreditPackUnitPriceUSD()
	effective := unitPrice * (1 - videoReservedProfitMargin)
	if effective <= 0 {
		return defaultCreditPackUnitPriceUSD * (1 - videoReservedProfitMargin)
	}
	return effective
}

func lowestCreditPackUnitPriceUSD() float64 {
	lowest := math.MaxFloat64
	for _, pack := range globals.GraConf.Stripe.CreditPacks {
		if pack.Price <= 0 || pack.Credits <= 0 {
			continue
		}
		unitPrice := float64(pack.Price) / 100.0 / float64(pack.Credits)
		if unitPrice > 0 && unitPrice < lowest {
			lowest = unitPrice
		}
	}
	if lowest == math.MaxFloat64 {
		return defaultCreditPackUnitPriceUSD
	}
	return lowest
}

// normalizeQuotedVideoCredits rounds the final credit number to
// nicer increments so the UI shows "20 / 25 / 30" rather than
// "21 / 24 / 28". The rounding tiers were chosen to match the
// credit pack denominations users buy in.
func normalizeQuotedVideoCredits(credits int) int {
	switch {
	case credits >= 100:
		return int(math.Round(float64(credits)/10.0)) * 10
	case credits >= 20:
		return int(math.Round(float64(credits)/5.0)) * 5
	default:
		return credits
	}
}
