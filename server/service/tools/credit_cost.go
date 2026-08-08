package tools

import (
	"reflect"
	"server/model"
	"server/service/pricing"
	"strconv"
	"strings"
)

// CreditCostParams defines inputs for credit cost calculation by feature key.
type CreditCostParams struct {
	ToolID         string
	Model          string
	NumberOfImages int
	Resolution     string
	Upscale        bool
	Scale          int
	EnhanceFace    bool
	DetailLevel    string
	Params         map[string]interface{}
}

// GetCreditCostByToolID calculates credit cost based on feature key (tool id).
// Model-specific adjustments are only applied when needed (e.g. image_generator).
func GetCreditCostByToolID(p CreditCostParams) int {
	toolID := FeatureTypeForToolID(p.ToolID)

	switch toolID {
	case model.TOOL_LORA_STUDIO:
		return 4
	case model.TOOL_AVATAR_STUDIO:
		numberOfImages := p.NumberOfImages
		if numberOfImages <= 0 {
			numberOfImages = getIntFromParams(p.Params, "numberOfImages")
		}
		if numberOfImages <= 0 {
			numberOfImages = 1
		}
		return 4 * numberOfImages
	case model.TOOL_VIDEO_GENERATOR:
		return getVideoCreditCost(p)
	case model.TOOL_IMAGE_GENERATOR:
		return getGenerateCreditCostByFeature(p)
	case model.TOOL_IMAGE_UPSCALER:
		// Post 2026-05-15 unification: delegate to pricing.QuoteUpscale.
		// Byte-equal with legacy; pinned by parity test.
		scale := p.Scale
		if scale == 0 {
			scale = getIntFromParams(p.Params, "scale")
		}
		enhanceFace := p.EnhanceFace
		if !enhanceFace {
			enhanceFace = getBoolFromParams(p.Params, "enhanceFace")
		}
		if !enhanceFace {
			// Legacy DB rows / old clients only stored the "enhanceQuality"
			// alias — keep reading it so historical pricing stays correct.
			enhanceFace = getBoolFromParams(p.Params, "enhanceQuality")
		}
		return pricing.QuoteUpscale(pricing.UpscaleQuoteInput{
			Scale:       scale,
			EnhanceFace: enhanceFace,
		}).Credits
	case model.TOOL_BACKGROUND_REMOVER:
		// Post 2026-05-15 unification: delegate to pricing.QuoteFlat.
		// Defensive literal fallback in case the FlatOp table is
		// reorganised and the lookup misses; preserves the 4-credit
		// floor either way.
		if q, err := pricing.QuoteFlat(pricing.FlatOpRemoveBg); err == nil {
			return q.Credits
		}
		return 4
	case model.TOOL_IMAGE_VECTORIZER:
		// Post 2026-05-15 unification: delegate to pricing.QuoteVectorize.
		// The previous "default empty → medium → returns 3" branch
		// collapses naturally: pricing.QuoteVectorize compares
		// trimmed input exactly to "high"; empty / medium / anything
		// else falls through to the 3-credit tier byte-equal.
		detail := p.DetailLevel
		if detail == "" {
			detail = getStringFromParams(p.Params, "detailLevel")
		}
		return pricing.QuoteVectorize(pricing.VectorizeQuoteInput{
			DetailLevel: detail,
		}).Credits
	case model.TOOL_PROMPT_BUILDER:
		action := getStringFromParams(p.Params, "action")
		switch action {
		case "enhance", "parse":
			return 2
		case "suggest":
			return 1
		default:
			return 1
		}
	case model.TOOL_VIDEO_PROMPT_BUILDER:
		action := getStringFromParams(p.Params, "action")
		switch action {
		case "rewrite":
			return 2
		default:
			return 1
		}
	default:
		return 1
	}
}

func getVideoCreditCost(p CreditCostParams) int {
	modelID := p.Model
	if modelID == "" || modelID == model.TOOL_VIDEO_GENERATOR {
		modelID = getStringFromParams(p.Params, "model")
	}
	if modelID == "" {
		modelID = model.KLING_2_6
	}

	duration := getIntFromParams(p.Params, "duration")
	if duration <= 0 {
		rawDuration := strings.TrimSpace(strings.TrimSuffix(getStringFromParams(p.Params, "duration"), "s"))
		if parsed, err := strconv.Atoi(rawDuration); err == nil && parsed > 0 {
			duration = parsed
		}
	}
	resolution := p.Resolution
	if resolution == "" {
		resolution = getStringFromParams(p.Params, "resolution")
	}
	aspectRatio := getStringFromParams(p.Params, "aspectRatio")
	motionMode := getStringFromParams(p.Params, "motionMode")
	motionOrientation := getStringFromParams(p.Params, "motionOrientation")
	hasStartFrame := strings.TrimSpace(getStringFromParams(p.Params, "startFrame")) != "" ||
		strings.TrimSpace(getStringFromParams(p.Params, "startFrameUrl")) != ""
	hasEndFrame := strings.TrimSpace(getStringFromParams(p.Params, "endFrame")) != "" ||
		strings.TrimSpace(getStringFromParams(p.Params, "endFrameUrl")) != ""
	if !hasStartFrame && getArrayLenFromParams(p.Params, "referenceImages") > 0 {
		hasStartFrame = true
	}

	quote, err := QuoteVideoCredits(modelID, VideoQuoteOptions{
		Duration:          duration,
		Resolution:        resolution,
		AspectRatio:       aspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasStartFrame,
		HasEndFrame:       hasEndFrame,
	})
	if err != nil {
		return 35
	}
	return quote.Credits
}

// getGenerateCreditCostByFeature is now a thin adapter around
// pricing.QuoteImage. The per-model pricing math (NANO_BANANA_PRO
// 4k multiplier, GPT_IMAGE_2 resolution × quality matrix, etc.)
// moved to server/service/pricing/image.go in the 2026-05-15
// unification refactor; this function only extracts the loose
// CreditCostParams into the strongly-typed ImageQuoteInput.
//
// Unknown models fall back to the legacy "default to 1 credit"
// posture so non-canvas tools (lora / avatar / prompt builder)
// routing through here keep their old behaviour.
func getGenerateCreditCostByFeature(p CreditCostParams) int {
	modelID := p.Model
	if modelID == "" || modelID == model.TOOL_IMAGE_GENERATOR || modelID == model.TOOL_VIDEO_GENERATOR || modelID == model.TOOL_AVATAR_STUDIO {
		modelID = getStringFromParams(p.Params, "model")
	}
	if modelID == "" {
		modelID = model.NANO_BANANA
	}

	numberOfImages := p.NumberOfImages
	if numberOfImages <= 0 {
		numberOfImages = getIntFromParams(p.Params, "numberOfImages")
	}
	if numberOfImages <= 0 {
		numberOfImages = 1
	}

	resolution := p.Resolution
	if resolution == "" {
		resolution = getStringFromParams(p.Params, "resolution")
	}

	upscale := p.Upscale
	if !upscale {
		upscale = getBoolFromParams(p.Params, "upscale")
	}

	quote, err := pricing.QuoteImage(pricing.ImageQuoteInput{
		Model:          modelID,
		NumberOfImages: numberOfImages,
		Resolution:     resolution,
		Upscale:        upscale,
		Quality:        getStringFromParams(p.Params, "quality"),
	})
	if err != nil {
		// Unknown model — fall back to BaseImageCredits which
		// applies the legacy "default 1 for unknown" posture.
		// Preserves behaviour for whatever non-image-model
		// payloads might still route through here.
		return BaseImageCredits(modelID)
	}
	return quote.Credits
}

func getParamValue(params map[string]interface{}, key string) (interface{}, bool) {
	if params == nil {
		return nil, false
	}
	if v, ok := params[key]; ok {
		return v, true
	}
	if nestedRaw, ok := params["params"]; ok {
		switch nested := nestedRaw.(type) {
		case map[string]interface{}:
			if v, ok := nested[key]; ok {
				return v, true
			}
		case model.JSONMap:
			if v, ok := nested[key]; ok {
				return v, true
			}
		}
	}
	return nil, false
}

func getStringFromParams(params map[string]interface{}, key string) string {
	if v, ok := getParamValue(params, key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntFromParams(params map[string]interface{}, key string) int {
	if v, ok := getParamValue(params, key); ok {
		switch val := v.(type) {
		case int:
			return val
		case int8:
			return int(val)
		case int32:
			return int(val)
		case int64:
			return int(val)
		case float32:
			return int(val)
		case float64:
			return int(val)
		}
	}
	return 0
}

func getArrayLenFromParams(params map[string]interface{}, key string) int {
	if v, ok := getParamValue(params, key); ok {
		switch val := v.(type) {
		case []interface{}:
			return len(val)
		case []map[string]interface{}:
			return len(val)
		case []model.ReferenceImageParam:
			return len(val)
		}
		rv := reflect.ValueOf(v)
		if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			return rv.Len()
		}
	}
	return 0
}

func getBoolFromParams(params map[string]interface{}, key string) bool {
	if v, ok := getParamValue(params, key); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}
