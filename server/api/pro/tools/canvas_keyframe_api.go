package tools

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"server/globals"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// canvas_keyframe_api.go — deterministic video keyframe extraction.
// Self-contained surface: one endpoint + two input-sanitization
// helpers that have no other call site. Lifted out of canvas_api.go
// on the b1-slim-down follow-up after the §13.3 hard target was
// already met; this slice was logged as "stylistic, not load-
// bearing" but adds <100 LOC of cohesion benefit and keeps the
// asset/upload neighbours in canvas_api.go focused on those
// responsibilities.

const maxCanvasKeyframeBodyBytes = 64 << 10 // 64 KiB

// ExtractKeyframe godoc
// @Summary Canvas AI Extract Video Keyframe
// @Description Extracts a specific frame from a video asset to be used as an image
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param request body struct{VideoURL string `json:"videoUrl" binding:"required"`; Timestamp float64 `json:"timestamp" binding:"required"`} true "Extract Options"
// @Success 200 {object} response.Response{data=TaskSubmitResponse}
// @Router /api/tools/canvas/extract-keyframe [post]
func (a *CanvasApi) ExtractKeyframe(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasKeyframeBodyBytes)
	var req struct {
		VideoURL        string  `json:"videoUrl" binding:"required"`
		Timestamp       float64 `json:"timestamp" binding:"required"`
		SourceElementID string  `json:"sourceElementId"`
		SourceTaskID    string  `json:"sourceTaskId"`
		SourceTitle     string  `json:"sourceTitle"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	normalized, err := canvasService.NormalizeKeyframeRequest(req.VideoURL, req.Timestamp)
	if err != nil {
		respondCanvasErrorFromError(c, err, "videoUrl is invalid")
		return
	}
	if _, err := sanitizeCanvasKeyframeVideoURL(normalized.VideoURL, uid); err != nil {
		respondCanvasErrorFromError(c, err, "videoUrl must be an uploaded canvas video asset")
		return
	}
	if headerElementID := strings.TrimSpace(c.GetHeader("X-Canvas-Element-Id")); headerElementID != "" && req.SourceElementID == "" {
		req.SourceElementID = headerElementID
	}
	if sourceElementID, err := canvasService.NormalizeCanvasStorageKey(req.SourceElementID, "sourceElementId", false); err == nil {
		normalized.SourceElementID = sourceElementID
	} else {
		respondCanvasErrorFromError(c, canvasService.ErrCanvasAssetInvalidInput, err.Error())
		return
	}
	if sourceTaskID, err := canvasService.NormalizeCanvasStorageKey(req.SourceTaskID, "sourceTaskId", false); err == nil {
		normalized.SourceTaskID = sourceTaskID
	} else {
		respondCanvasErrorFromError(c, canvasService.ErrCanvasAssetInvalidInput, err.Error())
		return
	}
	normalized.SourceTitle = normalizeCanvasKeyframeSourceTitle(req.SourceTitle)

	projectID := parseCanvasProjectHeader(c)
	if projectID == 0 {
		respondCanvasErrorFromError(c, canvasService.ErrCanvasAssetInvalidInput, "project id is required")
		return
	}

	asset, err := canvasService.ExtractKeyframeAsset(
		c.Request.Context(),
		globals.GraDBs["system"],
		canvasService.NewLocalAssetStorage(),
		int(uid),
		projectID,
		normalized,
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Extract keyframe failed")
		return
	}

	globals.Info(fmt.Sprintf("[Canvas] extracted deterministic keyframe, uid: %d, project: %d, time: %.2f", uid, projectID, normalized.Timestamp))
	response.OkWithData(gin.H{
		"success": true,
		"url":     asset.URL,
		"asset":   asset,
	}, c)
}

func normalizeCanvasKeyframeSourceTitle(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || hasControlRune(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > 120 {
		value = string(runes[:120])
	}
	return value
}

func sanitizeCanvasKeyframeVideoURL(raw string, uid uint) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || len(trimmed) > canvasChatMaxURLLength || hasControlRune(trimmed) || strings.HasPrefix(trimmed, "//") {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", canvasService.ErrCanvasInvalidVideoURL
		}
		if parsed.Host == "" || parsed.User != nil || !isAllowedVideoReferenceMediaHost(parsed.Hostname()) {
			return "", canvasService.ErrCanvasInvalidVideoURL
		}
	} else if !strings.HasPrefix(trimmed, "/") {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}

	pathValue := parsed.Path
	if pathValue == "" {
		pathValue = parsed.EscapedPath()
	}
	if hasUnsafeURLPath(pathValue) {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}
	if !(strings.HasPrefix(pathValue, "/uploads/canvas/uid/") ||
		strings.HasPrefix(pathValue, "/canvas/uid/") ||
		strings.HasPrefix(pathValue, "/uploads/generations/uid/") ||
		strings.HasPrefix(pathValue, "/generations/uid/")) {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}
	if !referenceURLPathMatchesUID(pathValue, uid) {
		return "", canvasService.ErrCanvasInvalidVideoURL
	}
	return trimmed, nil
}
