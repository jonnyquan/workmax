package tools

import (
	"fmt"
	"server/model"
	"server/service/pricing"
	"server/service/tools/provider"
	"strings"
)

// VideoModelPricingStatus, VideoQuoteOptions, VideoCreditQuote, and
// the VideoPricing* constants are type aliases over the unified
// pricing-package types. Callers in this package and downstream
// (api/pro/tools/*) keep working unchanged — Go aliases preserve
// type identity so e.g. tools.VideoQuoteOptions IS pricing
// .VideoQuoteOptions at the language level.
//
// Migrated 2026-05-15 (pricing unification step 4/N). The pricing
// math + per-model rate tables moved to server/service/pricing/
// video.go; this file retains the capability registry (FE wire +
// validation concern) plus NormalizeVideoQuoteOptions /
// ValidateVideoRequest, both of which read the capability registry.
type VideoModelPricingStatus = pricing.VideoModelPricingStatus

const (
	VideoPricingOfficial  = pricing.VideoPricingOfficial
	VideoPricingEstimated = pricing.VideoPricingEstimated
)

type VideoModelCapabilities struct {
	Model                               string                  `json:"model"`
	ProviderType                        string                  `json:"providerType,omitempty"`
	ProviderAvailable                   bool                    `json:"providerAvailable"`
	PricingStatus                       VideoModelPricingStatus `json:"pricingStatus"`
	SupportsAspectRatio                 bool                    `json:"supportsAspectRatio"`
	AspectRatios                        []string                `json:"aspectRatios,omitempty"`
	DefaultAspectRatio                  string                  `json:"defaultAspectRatio,omitempty"`
	SupportsResolution                  bool                    `json:"supportsResolution"`
	Resolutions                         []string                `json:"resolutions,omitempty"`
	DefaultResolution                   string                  `json:"defaultResolution,omitempty"`
	Durations                           []int                   `json:"durations,omitempty"`
	DefaultDuration                     int                     `json:"defaultDuration"`
	SupportsStartFrame                  bool                    `json:"supportsStartFrame"`
	SupportsEndFrame                    bool                    `json:"supportsEndFrame"`
	SupportsReferenceImages             bool                    `json:"supportsReferenceImages,omitempty"`
	MaxGenericReferenceImages           int                     `json:"maxGenericReferenceImages,omitempty"`
	SupportsGenericReferencesWithFrames bool                    `json:"supportsGenericReferencesWithFrames"`
	SupportsMotionControl               bool                    `json:"supportsMotionControl"`
	MotionModes                         []string                `json:"motionModes,omitempty"`
	DefaultMotionMode                   string                  `json:"defaultMotionMode,omitempty"`
	MotionOrientations                  []string                `json:"motionOrientations,omitempty"`
	DefaultMotionOrientation            string                  `json:"defaultMotionOrientation,omitempty"`
	SupportsAudioGeneration             bool                    `json:"supportsAudioGeneration,omitempty"`
	SupportsReferenceVideo              bool                    `json:"supportsReferenceVideo,omitempty"`
	SupportsReferenceAudio              bool                    `json:"supportsReferenceAudio,omitempty"`
	DefaultGenerateAudio                bool                    `json:"defaultGenerateAudio,omitempty"`
	SupportsSeed                        bool                    `json:"supportsSeed,omitempty"`
	Notes                               []string                `json:"notes,omitempty"`
}

// VideoQuoteOptions and VideoCreditQuote are aliases over the
// unified pricing-package types. Same memory layout, same JSON
// wire (preserved by JSON tags on the underlying pricing struct).
type VideoQuoteOptions = pricing.VideoQuoteOptions
type VideoCreditQuote = pricing.VideoCreditQuote

var videoModelCapabilityRegistry = map[string]VideoModelCapabilities{
	model.KLING_2_6: {
		Model:                     model.KLING_2_6,
		PricingStatus:             VideoPricingOfficial,
		SupportsAspectRatio:       true,
		AspectRatios:              []string{"16:9", "9:16", "1:1"},
		DefaultAspectRatio:        "16:9",
		SupportsResolution:        false,
		Durations:                 []int{5, 10},
		DefaultDuration:           5,
		SupportsStartFrame:        true,
		SupportsEndFrame:          true,
		SupportsReferenceImages:   true,
		MaxGenericReferenceImages: 2,
		SupportsMotionControl:     true,
		MotionModes:               []string{"std", "pro"},
		DefaultMotionMode:         "std",
		MotionOrientations:        []string{"image"},
		DefaultMotionOrientation:  "image",
		Notes:                     []string{"Pricing depends on motion mode and whether image references are used."},
	},
	model.SORA_2: {
		Model:                 model.SORA_2,
		PricingStatus:         VideoPricingOfficial,
		SupportsAspectRatio:   true,
		AspectRatios:          []string{"16:9", "9:16", "1:1"},
		DefaultAspectRatio:    "16:9",
		SupportsResolution:    false,
		Durations:             []int{4, 8, 12},
		DefaultDuration:       8,
		SupportsStartFrame:    false,
		SupportsEndFrame:      false,
		SupportsMotionControl: false,
	},
	// Veo 3.1 — Google's flagship cinematic video model. Per Vertex AI
	// docs (cloud.google.com/vertex-ai/.../veo/3-1-generate): supports
	// 720p/1080p/4K, durations 4/6/8s, native audio, seed, start/end
	// frame, up to 3 reference images. 8s is mandatory when output is
	// 1080p / 4K or any reference frame is attached — the Veo duration
	// normalizers in this file enforce that.
	model.VEO_31: {
		Model:                     model.VEO_31,
		PricingStatus:             VideoPricingOfficial,
		SupportsAspectRatio:       true,
		AspectRatios:              []string{"16:9", "9:16"},
		DefaultAspectRatio:        "16:9",
		SupportsResolution:        true,
		Resolutions:               []string{"720p", "1080p", "4k"},
		DefaultResolution:         "720p",
		Durations:                 []int{4, 6, 8},
		DefaultDuration:           8,
		SupportsStartFrame:        true,
		SupportsEndFrame:          true,
		SupportsReferenceImages:   true,
		MaxGenericReferenceImages: 3,
		SupportsMotionControl:     false,
		SupportsAudioGeneration:   true,
		DefaultGenerateAudio:      true,
		SupportsSeed:              true,
		Notes: []string{
			"1080p, 4K, and reference-image runs are forced to an 8-second clip.",
			"Audio is generated natively alongside the video.",
			"4K uses a higher upstream rate.",
		},
	},
	// Veo 3.1 Fast — quicker / cheaper variant. Per Vertex AI docs the
	// capability surface is identical to standard Veo 3.1 (same
	// resolutions including 4K, same durations, same frame and
	// reference-image support, same native audio); only the upstream
	// rate differs, which QuoteVideoCredits handles.
	model.VEO_31_FAST: {
		Model:                     model.VEO_31_FAST,
		PricingStatus:             VideoPricingOfficial,
		SupportsAspectRatio:       true,
		AspectRatios:              []string{"16:9", "9:16"},
		DefaultAspectRatio:        "16:9",
		SupportsResolution:        true,
		Resolutions:               []string{"720p", "1080p", "4k"},
		DefaultResolution:         "720p",
		Durations:                 []int{4, 6, 8},
		DefaultDuration:           8,
		SupportsStartFrame:        true,
		SupportsEndFrame:          true,
		SupportsReferenceImages:   true,
		MaxGenericReferenceImages: 3,
		SupportsMotionControl:     false,
		SupportsAudioGeneration:   true,
		DefaultGenerateAudio:      true,
		SupportsSeed:              true,
		Notes: []string{
			"Fast variant — lower upstream rate at the same capability set as Veo 3.1.",
			"1080p, 4K, and reference-image runs are forced to an 8-second clip.",
		},
	},
	model.SEEDANCE: {
		Model:                     model.SEEDANCE,
		PricingStatus:             VideoPricingOfficial,
		SupportsAspectRatio:       true,
		AspectRatios:              []string{"16:9", "9:16", "1:1"},
		DefaultAspectRatio:        "16:9",
		SupportsResolution:        true,
		Resolutions:               []string{"480p", "720p", "1080p"},
		DefaultResolution:         "720p",
		Durations:                 []int{5, 10},
		DefaultDuration:           5,
		SupportsStartFrame:        true,
		SupportsEndFrame:          true,
		SupportsReferenceImages:   true,
		MaxGenericReferenceImages: 2,
		SupportsMotionControl:     false,
		Notes:                     []string{"Credits are aligned to the official no-audio online pricing path."},
	},
	model.SEEDANCE_2: {
		Model:                               model.SEEDANCE_2,
		PricingStatus:                       VideoPricingOfficial,
		SupportsAspectRatio:                 true,
		AspectRatios:                        []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"},
		DefaultAspectRatio:                  "16:9",
		SupportsResolution:                  true,
		Resolutions:                         []string{"480p", "720p"},
		DefaultResolution:                   "720p",
		Durations:                           []int{5, 6, 8, 10, 12, 15},
		DefaultDuration:                     5,
		SupportsStartFrame:                  true,
		SupportsEndFrame:                    true,
		SupportsReferenceImages:             true,
		MaxGenericReferenceImages:           4,
		SupportsGenericReferencesWithFrames: true,
		SupportsMotionControl:               false,
		SupportsAudioGeneration:             true,
		SupportsReferenceVideo:              true,
		SupportsReferenceAudio:              true,
		DefaultGenerateAudio:                true,
		SupportsSeed:                        true,
		Notes: []string{
			"Seedance 2.0 (doubao-seedance-2-0-260128) routes to Volcengine Ark v3. 1080p and camera_fixed are not supported; request up to 15s per task.",
			"Credits follow Volcengine's official pricing of 46 RMB / 1M output tokens, benchmarked to ~1 RMB/s at 720p. 480p is scaled by relative pixel count.",
			"Native audio generation is on by default; disable when pairing with external voice tracks.",
		},
	},
	model.SEEDANCE_2_FAST: {
		Model:                               model.SEEDANCE_2_FAST,
		PricingStatus:                       VideoPricingEstimated,
		SupportsAspectRatio:                 true,
		AspectRatios:                        []string{"16:9", "9:16", "1:1", "4:3", "3:4", "21:9"},
		DefaultAspectRatio:                  "16:9",
		SupportsResolution:                  true,
		Resolutions:                         []string{"480p", "720p"},
		DefaultResolution:                   "720p",
		Durations:                           []int{5, 6, 8, 10, 12, 15},
		DefaultDuration:                     5,
		SupportsStartFrame:                  true,
		SupportsEndFrame:                    true,
		SupportsReferenceImages:             true,
		MaxGenericReferenceImages:           4,
		SupportsGenericReferencesWithFrames: true,
		SupportsMotionControl:               false,
		SupportsAudioGeneration:             true,
		SupportsReferenceVideo:              true,
		SupportsReferenceAudio:              true,
		DefaultGenerateAudio:                true,
		SupportsSeed:                        true,
		Notes: []string{
			"Seedance 2.0 Fast (doubao-seedance-2-0-fast-260128) shares capabilities with Seedance 2 but trades a bit of fidelity for roughly half the runtime cost.",
			"Pricing is estimated at ~0.55 RMB/s at 720p until Volcengine publishes an official rate.",
		},
	},
	model.MINIMAX_VIDEO: {
		Model:                     model.MINIMAX_VIDEO,
		PricingStatus:             VideoPricingOfficial,
		SupportsAspectRatio:       false,
		SupportsResolution:        true,
		Resolutions:               []string{"512p", "768p", "1080p"},
		DefaultResolution:         "768p",
		Durations:                 []int{6, 10},
		DefaultDuration:           6,
		SupportsStartFrame:        true,
		SupportsEndFrame:          true,
		SupportsReferenceImages:   true,
		MaxGenericReferenceImages: 2,
		SupportsMotionControl:     false,
		Notes:                     []string{"Credits are aligned to MiniMax Hailuo 02 point consumption."},
	},
}

func GetVideoModelCapabilities(modelID string) (VideoModelCapabilities, bool) {
	cap, ok := videoModelCapabilityRegistry[strings.TrimSpace(modelID)]
	return cap, ok
}

func SupportsVideoReferenceMedia(modelID string) bool {
	cap, ok := GetVideoModelCapabilities(modelID)
	return ok && cap.SupportsReferenceVideo
}

func SupportsAudioReferenceMedia(modelID string) bool {
	cap, ok := GetVideoModelCapabilities(modelID)
	return ok && cap.SupportsReferenceAudio
}

func SupportsVideoReferenceImages(modelID string) bool {
	cap, ok := GetVideoModelCapabilities(modelID)
	return ok && cap.SupportsReferenceImages
}

func MaxGenericVideoReferenceImages(modelID string) int {
	cap, ok := GetVideoModelCapabilities(modelID)
	if !ok || !cap.SupportsReferenceImages {
		return 0
	}
	return cap.MaxGenericReferenceImages
}

func SupportsGenericVideoReferencesWithFrames(modelID string) bool {
	cap, ok := GetVideoModelCapabilities(modelID)
	return ok && cap.SupportsGenericReferencesWithFrames
}

func IsVideoModelProviderAvailable(modelID string) bool {
	providerType := providerTypeByModel(modelID)
	if strings.TrimSpace(providerType) == "" {
		return provider.GetDefaultProviderByMediaType(model.MediaTypeVideo, "", "") != nil
	}
	return provider.GetDefaultProviderForModel(model.MediaTypeVideo, providerType, modelID, "") != nil
}

func ListVideoModelCapabilities() []VideoModelCapabilities {
	ordered := VideoModelOrder()
	out := make([]VideoModelCapabilities, 0, len(ordered))
	for _, id := range ordered {
		if cap, ok := videoModelCapabilityRegistry[id]; ok {
			cap.ProviderType = providerTypeByModel(id)
			cap.ProviderAvailable = IsVideoModelProviderAvailable(id)
			out = append(out, cap)
		}
	}
	return out
}

func NormalizeVideoQuoteOptions(modelID string, opts VideoQuoteOptions) VideoQuoteOptions {
	cap, ok := GetVideoModelCapabilities(modelID)
	if !ok {
		return opts
	}
	if opts.Duration <= 0 {
		opts.Duration = cap.DefaultDuration
	}
	if len(cap.Durations) > 0 && !containsInt(cap.Durations, opts.Duration) {
		opts.Duration = nearestInt(cap.Durations, opts.Duration, cap.DefaultDuration)
	}
	if cap.SupportsResolution {
		if strings.TrimSpace(opts.Resolution) == "" {
			opts.Resolution = cap.DefaultResolution
		}
		if len(cap.Resolutions) > 0 && !containsString(cap.Resolutions, opts.Resolution) {
			opts.Resolution = cap.DefaultResolution
		}
	} else {
		opts.Resolution = ""
	}
	if cap.SupportsAspectRatio {
		if strings.TrimSpace(opts.AspectRatio) == "" {
			opts.AspectRatio = cap.DefaultAspectRatio
		}
		if len(cap.AspectRatios) > 0 && !containsString(cap.AspectRatios, opts.AspectRatio) {
			opts.AspectRatio = cap.DefaultAspectRatio
		}
	} else {
		opts.AspectRatio = ""
	}
	if cap.SupportsMotionControl {
		if strings.TrimSpace(opts.MotionMode) == "" {
			opts.MotionMode = cap.DefaultMotionMode
		}
		if len(cap.MotionModes) > 0 && !containsString(cap.MotionModes, opts.MotionMode) {
			opts.MotionMode = cap.DefaultMotionMode
		}
		if strings.TrimSpace(opts.MotionOrientation) == "" {
			opts.MotionOrientation = cap.DefaultMotionOrientation
		}
		if len(cap.MotionOrientations) > 0 && !containsString(cap.MotionOrientations, opts.MotionOrientation) {
			opts.MotionOrientation = cap.DefaultMotionOrientation
		}
	} else {
		opts.MotionMode = ""
		opts.MotionOrientation = ""
	}
	if !cap.SupportsStartFrame {
		opts.HasStartFrame = false
	}
	if !cap.SupportsEndFrame {
		opts.HasEndFrame = false
	}
	if modelID == model.VEO_31 || modelID == model.VEO_31_FAST {
		// Veo runtime duration mirrors the billable-duration tier
		// table from pricing — kept in pricing because it's the
		// rate-card axis. tools normalizes its capability-side
		// "what duration should the user actually send to the
		// provider" through the same function.
		opts.Duration = pricing.NormalizeVeoBillableDuration(opts.Duration, opts.Resolution, opts.HasStartFrame || opts.HasEndFrame)
	}
	return opts
}

// QuoteVideoCredits is now a thin adapter over pricing.QuoteVideo.
// Normalizes input via NormalizeVideoQuoteOptions (which reads
// the capability registry kept here in tools), then delegates the
// per-model rate math to pricing. Capability + pricing live in
// separate packages now but compose through this wrapper —
// callers don't change.
func QuoteVideoCredits(modelID string, opts VideoQuoteOptions) (VideoCreditQuote, error) {
	if _, ok := GetVideoModelCapabilities(modelID); !ok {
		return VideoCreditQuote{}, fmt.Errorf("unsupported video model: %s", modelID)
	}
	opts = NormalizeVideoQuoteOptions(modelID, opts)
	return pricing.QuoteVideo(modelID, opts)
}

func ValidateVideoRequest(modelID string, opts VideoQuoteOptions, generationMethod string, hasStartFrame bool, hasEndFrame bool) error {
	cap, ok := GetVideoModelCapabilities(modelID)
	if !ok {
		return fmt.Errorf("unsupported video model")
	}
	normalized := NormalizeVideoQuoteOptions(modelID, opts)
	if opts.Duration > 0 && normalized.Duration != opts.Duration {
		return fmt.Errorf("invalid duration")
	}
	if cap.SupportsResolution && strings.TrimSpace(opts.Resolution) != "" && normalized.Resolution != strings.TrimSpace(opts.Resolution) {
		return fmt.Errorf("invalid resolution")
	}
	if cap.SupportsAspectRatio && strings.TrimSpace(opts.AspectRatio) != "" && normalized.AspectRatio != strings.TrimSpace(opts.AspectRatio) {
		return fmt.Errorf("invalid aspectRatio")
	}
	if !cap.SupportsStartFrame && hasStartFrame {
		return fmt.Errorf("start frame is not supported")
	}
	if !cap.SupportsEndFrame && hasEndFrame {
		return fmt.Errorf("end frame is not supported")
	}
	if (modelID == model.VEO_31 || modelID == model.VEO_31_FAST) && hasEndFrame && !hasStartFrame {
		return fmt.Errorf("end frame requires start frame")
	}
	method := strings.TrimSpace(generationMethod)
	if method == "" {
		method = "frames"
	}
	if method != "frames" && method != "motion" {
		return fmt.Errorf("invalid generationMethod")
	}
	if method == "motion" && !cap.SupportsMotionControl {
		return fmt.Errorf("motion control is not supported")
	}
	if method == "motion" && cap.SupportsMotionControl {
		requestedOrientation := strings.TrimSpace(opts.MotionOrientation)
		if requestedOrientation != "" && len(cap.MotionOrientations) > 0 && normalized.MotionOrientation != requestedOrientation {
			return fmt.Errorf("invalid motionOrientation")
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(strings.ToLower(target))
	for _, v := range values {
		if strings.TrimSpace(strings.ToLower(v)) == target {
			return true
		}
	}
	return false
}

func containsInt(values []int, target int) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func nearestInt(values []int, target, fallback int) int {
	if len(values) == 0 {
		return fallback
	}
	closest := values[0]
	bestDiff := absInt(values[0] - target)
	for _, v := range values[1:] {
		if diff := absInt(v - target); diff < bestDiff {
			closest = v
			bestDiff = diff
		}
	}
	return closest
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Per-model pricing helpers (seedancePriceRMB, seedance2PriceRMB,
// seedance2FastPriceRMB, minimaxPoints, usdToCredits, rmbToCredits,
// minimaxPointsToCredits, effectiveVideoCostPerCreditUSD,
// lowestCreditPackUnitPriceUSD, normalizeQuotedVideoCredits,
// normalizeVeoBillableDuration, normalizeVeoRuntimeDuration) all
// moved to server/service/pricing/video.go in the 2026-05-15
// unification refactor. They live next to QuoteVideo there, which
// is where the per-model rate tables are kept.
