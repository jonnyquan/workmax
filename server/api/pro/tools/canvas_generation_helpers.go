package tools

// canvas_generation_helpers.go · pure helpers extracted from
// canvas_generation_api.go on 2026-05-15 to land the planned split that
// canvas_generation_helpers_test.go + _more_test.go already pinned.
// These functions feed the 6 canvas editing handlers (img2img /
// outpaint / inpaint / mockup / edit-text / split-layers) plus
// submitCanvasGenerationTask in canvas_generation_api.go. Splitting
// them out keeps the main file under the §13.3 800-LOC ceiling.
//
// Helpers here are pure (no DB, no HTTP write) except where noted:
//   • authorizeCanvasModelCandidates  — writes 403 + returns false
//     when a Pro model is used without entitlement
//   • normalizeCanvasGenerationSourceURL — DB lookup via
//     canvasService.NormalizeOwnedCanvasReferenceURL

import (
	"net/http"

	"server/globals"
	"server/model"
	"server/service"
	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

// maxCanvasGenerationJSONBytes caps the JSON body for every generation
// handler in canvas_generation_api.go. 1 MiB is enough to carry the
// largest realistic request payload (mockup with template + style preset
// + 6-item modelCandidates list) without admitting attacker-shaped bodies.
const maxCanvasGenerationJSONBytes int64 = 1 << 20 // 1 MiB

func normalizeCanvasImageModel(raw string) string {
	modelID := model.NormalizeModelID(raw)
	if modelID == "" {
		return model.NANO_BANANA_2
	}
	return modelID
}

func normalizeCanvasModelCandidates(primary string, candidates []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(candidates)+1)
	appendIfValid := func(raw string) {
		modelID := model.NormalizeModelID(raw)
		if modelID == "" {
			return
		}
		if _, exists := seen[modelID]; exists {
			return
		}
		seen[modelID] = struct{}{}
		normalized = append(normalized, modelID)
	}

	appendIfValid(primary)
	for _, candidate := range candidates {
		appendIfValid(candidate)
	}
	return normalized
}

func filterCanvasModelCandidates(candidates []string, blocklist map[string]struct{}) []string {
	if len(candidates) == 0 || len(blocklist) == 0 {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, blocked := blocklist[candidate]; blocked {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func authorizeCanvasModelCandidates(c *gin.Context, uid uint, primary string, candidates []string) ([]string, bool) {
	normalized := normalizeCanvasModelCandidates(primary, candidates)
	hasProModel := false
	for _, candidate := range normalized {
		if candidate == model.NANO_BANANA_PRO {
			hasProModel = true
			break
		}
	}
	if !hasProModel {
		return normalized, true
	}

	permService := service.GroupServiceApp.AccountServiceGroup.PermissionService
	canUseProModel := permService.CanUseProModel(int(uid))
	if canUseProModel {
		return normalized, true
	}
	if primary == model.NANO_BANANA_PRO {
		respondCanvasAIError(c, http.StatusForbidden, "Upgrade to Pro plan to use Pro model", canvasAIErrorProRequired)
		return nil, false
	}

	filtered := filterCanvasModelCandidates(normalized, map[string]struct{}{model.NANO_BANANA_PRO: {}})
	if len(filtered) == 0 {
		filtered = []string{model.NANO_BANANA_2}
	}
	return filtered, true
}

func buildCanvasReferenceImages(sourceImageURL string) []map[string]interface{} {
	return []map[string]interface{}{
		{"id": "canvas-source-1", "url": sourceImageURL, "weight": 1},
	}
}

func bindCanvasGenerationJSON(c *gin.Context, dst interface{}) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasGenerationJSONBytes)
	return c.ShouldBindJSON(dst)
}

func normalizeCanvasGenerationSourceURL(c *gin.Context, uid uint, rawURL string) (string, error) {
	projectID := parseCanvasProjectHeader(c)
	if projectID == 0 {
		return "", canvasService.ErrCanvasAssetInvalidInput
	}
	return canvasService.NormalizeOwnedCanvasReferenceURL(
		c.Request.Context(),
		globals.GraDBs["system"],
		int(uid),
		projectID,
		rawURL,
		true,
	)
}

func setCanvasOperationMeta(requestData model.JSONMap, operation string, meta model.JSONMap) {
	if requestData == nil {
		return
	}
	requestData["source"] = "canvas"
	requestData["canvasOperation"] = operation
	if len(meta) == 0 {
		return
	}
	requestData["canvasOperationMeta"] = meta
}
