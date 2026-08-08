package tools

// Canvas quote subdomain — §10.1 B1 slice split from canvas_api.go.
// Houses Quote (server-signed quote token) plus its request/response
// envelopes and param coercion helpers. Shared error codes
// (canvasAIErrorUnauthorized, canvasAIErrorInvalidReq,
// canvasAIErrorInvalidParams) still live in canvas_generation_api.go;
// shared response helpers (respondCanvasChatHTTPError) remain in
// canvas_api.go.

import (
	"net/http"
	"strconv"
	"strings"

	"server/model"
	toolsService "server/service/tools"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type canvasQuoteRequest struct {
	Mode   string                 `json:"mode"`
	Model  string                 `json:"model"`
	Params map[string]interface{} `json:"params"`
}

type canvasQuoteBreakdown struct {
	NumberOfImages   int    `json:"numberOfImages,omitempty"`
	Resolution       string `json:"resolution,omitempty"`
	Duration         int    `json:"duration,omitempty"`
	AspectRatio      string `json:"aspectRatio,omitempty"`
	BillableDuration int    `json:"billableDuration,omitempty"`
	BillableRate     string `json:"billableRate,omitempty"`
}

type canvasQuoteResponse struct {
	QuoteID       string               `json:"quoteId"`
	Mode          string               `json:"mode"`
	Model         string               `json:"model"`
	Credits       int                  `json:"credits"`
	PricingStatus string               `json:"pricingStatus,omitempty"`
	Breakdown     canvasQuoteBreakdown `json:"breakdown"`
	ExpiresAt     int64                `json:"expiresAt"`
	TTLSeconds    int                  `json:"ttlSeconds"`
}

// Quote issues a server-signed credit quote for a pending generation. The
// returned quoteId is carried back on the actual generation request; the
// reservation layer re-verifies the fingerprint so pricing cannot be spoofed
// even if the client mutates params after quoting.
func (a *CanvasApi) Quote(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasChatHTTPError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized, true)
		return
	}

	var req canvasQuoteRequest
	if err := bindCanvasGenerationJSON(c, &req); err != nil {
		respondCanvasChatHTTPError(c, http.StatusBadRequest, "Invalid request format", canvasAIErrorInvalidReq, false)
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "image"
	}
	modelID := strings.TrimSpace(req.Model)
	params := req.Params
	if params == nil {
		params = map[string]interface{}{}
	}

	switch mode {
	case "video":
		if modelID == "" {
			modelID = model.KLING_2_6
		}
		opts := toolsService.NormalizeVideoQuoteOptions(modelID, toolsService.VideoQuoteOptions{
			Duration:          intFromQuoteParams(params, "duration"),
			Resolution:        stringFromQuoteParams(params, "resolution"),
			AspectRatio:       stringFromQuoteParams(params, "aspectRatio"),
			MotionMode:        stringFromQuoteParams(params, "motionMode"),
			MotionOrientation: stringFromQuoteParams(params, "motionOrientation"),
			HasStartFrame:     quoteParamPresent(params, "startFrameUrl") || quoteParamPresent(params, "startFrame"),
			HasEndFrame:       quoteParamPresent(params, "endFrameUrl") || quoteParamPresent(params, "endFrame"),
		})
		quote, err := toolsService.QuoteVideoCredits(modelID, opts)
		if err != nil {
			respondCanvasChatHTTPError(c, http.StatusBadRequest, "Unsupported video model", canvasAIErrorInvalidParams, false)
			return
		}

		canonical := toolsService.CanonicalVideoQuoteParams(opts)
		record := toolsService.IssueQuote(uid, mode, modelID, canonical, quote.Credits, string(quote.PricingStatus))
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "success",
			"data": canvasQuoteResponse{
				QuoteID:       record.ID,
				Mode:          mode,
				Model:         modelID,
				Credits:       quote.Credits,
				PricingStatus: string(quote.PricingStatus),
				Breakdown: canvasQuoteBreakdown{
					Duration:         opts.Duration,
					Resolution:       opts.Resolution,
					AspectRatio:      opts.AspectRatio,
					BillableDuration: quote.BillableDuration,
					BillableRate:     quote.BillableRate,
				},
				ExpiresAt:  record.ExpiresAt.UnixMilli(),
				TTLSeconds: int(toolsService.QuoteTTL.Seconds()),
			},
		})
		return
	case "image":
		if modelID == "" {
			modelID = model.NANO_BANANA_2
		}
		numberOfImages := intFromQuoteParams(params, "numberOfImages")
		if numberOfImages <= 0 {
			numberOfImages = 1
		}
		if numberOfImages > 4 {
			numberOfImages = 4
		}
		resolution := stringFromQuoteParams(params, "resolution")
		// Quality drives the GPT_IMAGE_2 multiplier matrix (other
		// models ignore it). Always-passed so the dispatcher can
		// route per-model; absent / non-string params resolve to "".
		quality := stringFromQuoteParams(params, "quality")

		quote, err := canvasService.QuoteCanvasImage(canvasService.CanvasImageQuoteInput{
			Op:             canvasService.CanvasOpImage,
			Model:          modelID,
			NumberOfImages: numberOfImages,
			Resolution:     resolution,
			Quality:        quality,
		})
		credits := quote.Credits
		if err != nil || credits <= 0 {
			credits = 3
		}

		canonical := toolsService.CanonicalImageQuoteParams(numberOfImages, resolution, quality)
		record := toolsService.IssueQuote(uid, mode, modelID, canonical, credits, "official")
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "success",
			"data": canvasQuoteResponse{
				QuoteID:       record.ID,
				Mode:          mode,
				Model:         modelID,
				Credits:       credits,
				PricingStatus: "official",
				Breakdown: canvasQuoteBreakdown{
					NumberOfImages: numberOfImages,
					Resolution:     resolution,
				},
				ExpiresAt:  record.ExpiresAt.UnixMilli(),
				TTLSeconds: int(toolsService.QuoteTTL.Seconds()),
			},
		})
		return
	case "upscale":
		// QuoteCanvasUpscale: 5 credits when scale=4 OR
		// enhanceFace is set, else 3. Matches the legacy
		// TOOL_IMAGE_UPSCALER pricing in tools/credit_cost.go
		// (pinned by pricing_catalog_settle_parity_test.go).
		scale := intFromQuoteParams(params, "scale")
		enhanceFace := boolFromQuoteParams(params, "enhanceFace")
		if !enhanceFace {
			// Legacy alias — older clients / DB rows used
			// enhanceQuality before the rename.
			enhanceFace = boolFromQuoteParams(params, "enhanceQuality")
		}
		quote := canvasService.QuoteCanvasUpscale(canvasService.CanvasUpscaleQuoteInput{
			Scale:       scale,
			EnhanceFace: enhanceFace,
		})
		canonical := map[string]interface{}{
			"scale":       scale,
			"enhanceFace": enhanceFace,
		}
		record := toolsService.IssueQuote(uid, mode, modelID, canonical, quote.Credits, "official")
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "success",
			"data": canvasQuoteResponse{
				QuoteID:       record.ID,
				Mode:          mode,
				Model:         modelID,
				Credits:       quote.Credits,
				PricingStatus: "official",
				ExpiresAt:     record.ExpiresAt.UnixMilli(),
				TTLSeconds:    int(toolsService.QuoteTTL.Seconds()),
			},
		})
		return
	case "remove-bg":
		// Flat 4 credits via QuoteCanvasFlat(CanvasOpRemoveBg).
		// No model-specific pricing — remove-bg is provider-
		// agnostic at the credit layer.
		quote, err := canvasService.QuoteCanvasFlat(canvasService.CanvasOpRemoveBg)
		if err != nil {
			respondCanvasChatHTTPError(c, http.StatusInternalServerError, "Failed to quote remove-bg", canvasAIErrorInvalidParams, false)
			return
		}
		record := toolsService.IssueQuote(uid, mode, modelID, map[string]interface{}{}, quote.Credits, "official")
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "success",
			"data": canvasQuoteResponse{
				QuoteID:       record.ID,
				Mode:          mode,
				Model:         modelID,
				Credits:       quote.Credits,
				PricingStatus: "official",
				ExpiresAt:     record.ExpiresAt.UnixMilli(),
				TTLSeconds:    int(toolsService.QuoteTTL.Seconds()),
			},
		})
		return
	case "vectorize":
		// QuoteCanvasVectorize: 4 credits for detailLevel="high",
		// else 3 (medium default). Matches legacy
		// TOOL_IMAGE_VECTORIZER pricing byte-equal — pinned by
		// pricing_catalog_settle_parity_test.go.
		detail := stringFromQuoteParams(params, "detailLevel")
		quote := canvasService.QuoteCanvasVectorize(canvasService.CanvasVectorizeQuoteInput{
			DetailLevel: detail,
		})
		canonical := map[string]interface{}{
			"detailLevel": detail,
		}
		record := toolsService.IssueQuote(uid, mode, modelID, canonical, quote.Credits, "official")
		c.JSON(http.StatusOK, gin.H{
			"code":    200,
			"message": "success",
			"data": canvasQuoteResponse{
				QuoteID:       record.ID,
				Mode:          mode,
				Model:         modelID,
				Credits:       quote.Credits,
				PricingStatus: "official",
				ExpiresAt:     record.ExpiresAt.UnixMilli(),
				TTLSeconds:    int(toolsService.QuoteTTL.Seconds()),
			},
		})
		return
	default:
		respondCanvasChatHTTPError(c, http.StatusBadRequest, "Invalid mode", canvasAIErrorInvalidParams, false)
	}
}

// boolFromQuoteParams pulls a boolean from the params map.
// Tolerant of two wire shapes: bool literal and string "true"/
// "false" (older clients sometimes stringify). Anything else is
// treated as false.
func boolFromQuoteParams(params map[string]interface{}, key string) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return false
	}
	switch val := v.(type) {
	case bool:
		return val
	case string:
		return strings.EqualFold(strings.TrimSpace(val), "true")
	}
	return false
}

func stringFromQuoteParams(params map[string]interface{}, key string) string {
	if v, ok := params[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func intFromQuoteParams(params map[string]interface{}, key string) int {
	if v, ok := params[key]; ok {
		switch val := v.(type) {
		case float64:
			return int(val)
		case float32:
			return int(val)
		case int:
			return val
		case int32:
			return int(val)
		case int64:
			return int(val)
		case string:
			trimmed := strings.TrimSpace(strings.TrimSuffix(val, "s"))
			if parsed, err := strconv.Atoi(trimmed); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func quoteParamPresent(params map[string]interface{}, key string) bool {
	v, ok := params[key]
	if !ok || v == nil {
		return false
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}
