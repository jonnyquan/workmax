package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	llmService "server/service/llm"
	toolsService "server/service/tools"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type VideoPromptRewriterApi struct{}

const (
	maxVideoPromptRewriteBodyBytes   = 128 * 1024
	maxVideoPromptRewriteIRBytes     = 64 * 1024
	maxVideoPromptRewritePromptRunes = 12000
	videoPromptRewriteCacheTTL       = 15 * time.Minute
	maxVideoPromptRewriteCacheItems  = 2048
)

type videoPromptRewriteRequest struct {
	Adapter        string `json:"adapter" binding:"required"`
	PositivePrompt string `json:"positivePrompt" binding:"required"`
	// IR is accepted for audit logging but not used by the rewriter — the
	// rendered positivePrompt already encodes every fact the LLM needs.
	IR map[string]any `json:"ir"`
}

type videoPromptRewriteResponse struct {
	Rewritten string `json:"rewritten"`
}

type cachedVideoPromptRewrite struct {
	response  videoPromptRewriteResponse
	expiresAt time.Time
}

var (
	videoPromptRewriteCacheMu sync.Mutex
	videoPromptRewriteCache   = map[string]cachedVideoPromptRewrite{}
)

func videoPromptRewriteIdempotencyCacheKey(c *gin.Context, uid uint) string {
	clientKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if clientKey == "" {
		clientKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}
	if clientKey == "" {
		return ""
	}
	return fmt.Sprintf("%d:video_prompt_rewrite:%s", uid, clientKey)
}

func getCachedVideoPromptRewrite(key string) (videoPromptRewriteResponse, bool) {
	if key == "" {
		return videoPromptRewriteResponse{}, false
	}
	now := time.Now()
	videoPromptRewriteCacheMu.Lock()
	defer videoPromptRewriteCacheMu.Unlock()
	cached, ok := videoPromptRewriteCache[key]
	if !ok {
		return videoPromptRewriteResponse{}, false
	}
	if now.After(cached.expiresAt) {
		delete(videoPromptRewriteCache, key)
		return videoPromptRewriteResponse{}, false
	}
	return cached.response, true
}

func setCachedVideoPromptRewrite(key string, resp videoPromptRewriteResponse) {
	if key == "" {
		return
	}
	videoPromptRewriteCacheMu.Lock()
	defer videoPromptRewriteCacheMu.Unlock()
	now := time.Now()
	for existingKey, cached := range videoPromptRewriteCache {
		if now.After(cached.expiresAt) {
			delete(videoPromptRewriteCache, existingKey)
		}
	}
	if len(videoPromptRewriteCache) >= maxVideoPromptRewriteCacheItems {
		for existingKey := range videoPromptRewriteCache {
			delete(videoPromptRewriteCache, existingKey)
			if len(videoPromptRewriteCache) < maxVideoPromptRewriteCacheItems {
				break
			}
		}
	}
	videoPromptRewriteCache[key] = cachedVideoPromptRewrite{
		response:  resp,
		expiresAt: now.Add(videoPromptRewriteCacheTTL),
	}
}

// RewriteVideoPrompt upgrades a rendered video prompt via the LLM gateway.
// Protected, 2-credit, idempotency-key aware to match sibling tools.
func (a *VideoPromptRewriterApi) RewriteVideoPrompt(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req videoPromptRewriteRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoPromptRewriteBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}

	req.Adapter = strings.TrimSpace(req.Adapter)
	if !isVideoPromptAdapter(req.Adapter) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid adapter", nil)
		return
	}

	req.PositivePrompt = strings.TrimSpace(req.PositivePrompt)
	if req.PositivePrompt == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "positivePrompt is empty", nil)
		return
	}
	if len([]rune(req.PositivePrompt)) > maxVideoPromptRewritePromptRunes {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "positivePrompt exceeds 12000 chars", nil)
		return
	}
	if req.IR != nil {
		irBytes, err := json.Marshal(req.IR)
		if err != nil {
			writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid ir", nil)
			return
		}
		if len(irBytes) > maxVideoPromptRewriteIRBytes {
			writePromptBuilderHTTPError(c, http.StatusBadRequest, "ir exceeds size limit", nil)
			return
		}
	}

	cacheKey := videoPromptRewriteIdempotencyCacheKey(c, uid)
	if cached, ok := getCachedVideoPromptRewrite(cacheKey); ok {
		response.OkWithData(cached, c)
		return
	}

	creditCost := 2
	reservationID, ok := reservePromptBuilderCredits(c, int(uid), "video_prompt_rewrite", creditCost)
	if !ok {
		return
	}
	db := globals.GraDBs["system"]

	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()

	userMessage := fmt.Sprintf("Target adapter: %s\n\nDraft prompt:\n%s", req.Adapter, req.PositivePrompt)

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: toolsService.VideoPromptRewriterSystemPrompt,
		UserMessage:  userMessage,
		Temperature:  0.7,
		MaxTokens:    2000,
		UserID:       uid,
		Service:      "video_prompt_rewrite",
		RequestID:    fmt.Sprintf("vpr_%d_%d", uid, time.Now().UnixNano()),
	}

	resp, err := llmService.GetService().ProcessRequest(ctx, llmReq)
	if err != nil || resp == nil || !resp.Success {
		msg := "Rewrite failed"
		if err != nil {
			msg = err.Error()
		} else if resp != nil && resp.Error != "" {
			msg = resp.Error
		}
		globals.Error(fmt.Sprintf("[VideoPromptRewriter] failed: %s", msg))
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Failed to rewrite prompt", nil)
		return
	}

	rewritten := strings.TrimSpace(resp.Content)
	if rewritten == "" {
		releasePromptBuilderReservation(reservationID)
		writePromptBuilderHTTPError(c, http.StatusBadGateway, "Empty rewrite returned", nil)
		return
	}

	finalizePromptBuilderReservation(reservationID, creditCost)
	_ = db.Transaction(func(tx *gorm.DB) error {
		return toolsService.CreateUsageRecordTx(tx, int(uid), model.TOOL_VIDEO_PROMPT_BUILDER, 0, creditCost, model.STATUS_SUCCESS, 0, &toolsService.UsageRecordMeta{
			ToolParams: map[string]interface{}{"action": "video_prompt_rewrite", "adapter": req.Adapter},
		})
	})

	out := videoPromptRewriteResponse{Rewritten: rewritten}
	setCachedVideoPromptRewrite(cacheKey, out)
	response.OkWithData(out, c)
}
