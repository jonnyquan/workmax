package tools

// Canvas OptimizePrompt — /api/tools/canvas/optimize-prompt.
// Takes a short user prompt and expands it into a detailed image or
// video generation prompt via the LLM service. Reuses the §12.3
// PromptGuard (applyCanvasPromptGuard) and the canvas idempotency
// reservation pattern.
//
// Sibling files: canvas_shared.go (applyCanvasPromptGuard +
// canvasIdempotencyKey, preserved from the retired canvas_chat_api.go),
// canvas_agent_thread_api.go (thread CRUD), canvas_agent_api.go
// (SSE AgentChat handler).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service/account"
	llmService "server/service/llm"
	projectService "server/service/project"
	toolsService "server/service/tools"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const canvasOptimizePromptMaxBodyBytes = 1 * 1024 * 1024

// OptimizePrompt godoc
// @Summary Enhance a user's prompt using LLM
// @Description Takes a short prompt and expands it into a detailed image or video generation prompt.
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param request body struct{Prompt string `json:"prompt" binding:"required"`} true "Prompt to optimize"
// @Success 200 {object} response.Response{data=object}
// @Router /api/tools/canvas/optimize-prompt [post]
func (a *CanvasApi) OptimizePrompt(c *gin.Context) {
	startTime := time.Now()
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, canvasOptimizePromptMaxBodyBytes)

	var req struct {
		Prompt string `json:"prompt" binding:"required"`
		Mode   string `json:"mode,omitempty"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "image"
	}
	if mode != "image" && mode != "video" {
		respondCanvasErrorWithCode(c, "Unsupported optimization mode", "INVALID_OPTIMIZE_PROMPT_MODE")
		return
	}

	cleanedPrompt, _, _, guardOK := applyCanvasPromptGuard(c, req.Prompt, "")
	if !guardOK {
		return
	}
	req.Prompt = cleanedPrompt

	systemPrompt := canvasOptimizePromptSystemPrompt(mode)

	creditCost := toolsService.CanvasOptimizePromptCost()
	db := globals.GraDBs["system"]
	reservationSvc := account.NewCreditReservationService()
	// Reuse the FE-supplied X-Canvas-Request-Id when present so logs
	// across canvas → llmService → provider share one trace key (B5).
	requestID := strings.TrimSpace(c.GetHeader("X-Canvas-Request-Id"))
	if requestID == "" {
		requestID = fmt.Sprintf("opt_%d_%d", uid, time.Now().UnixNano())
	}
	idempotencyKey := canvasIdempotencyKey(c, "canvas_optimize_prompt", int(uid))
	// P1 #6 slice 2 — gate the canvas project budget cap when the call
	// arrives from a canvas surface. 0 = non-canvas caller and the gate
	// becomes a no-op.
	canvasProjectID := parseCanvasProjectHeader(c)
	var reservationID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := reservationSvc.Reserve(tx, account.ReservationRequest{
			UID:            int(uid),
			Tool:           "canvas_optimize_prompt",
			IdempotencyKey: idempotencyKey,
			Reserved:       creditCost,
			Remark:         "canvas optimize prompt",
			ProjectID:      canvasProjectID,
		})
		if err != nil {
			return err
		}
		if !res.Created {
			if res.Reservation.IsTerminal() {
				return account.ErrReservationAlreadyProcessed
			}
			return account.ErrReservationInProgress
		}
		reservationID = res.Reservation.Id
		return nil
	}); err != nil {
		if errors.Is(err, account.ErrInsufficientCredits) {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"code":    response.ERROR,
				"message": "Insufficient credits",
				"data": gin.H{
					"creditsRequired": creditCost,
					"errorCode":       "INSUFFICIENT_CREDITS",
				},
			})
			return
		}
		if errors.Is(err, projectService.ErrBudgetExceeded) {
			c.JSON(http.StatusPaymentRequired, gin.H{
				"code":    response.ERROR,
				"message": "Canvas project budget cap reached",
				"data": gin.H{
					"creditsRequired": creditCost,
					"errorCode":       "BUDGET_EXCEEDED",
				},
			})
			return
		}
		if errors.Is(err, account.ErrReservationInProgress) {
			c.JSON(http.StatusConflict, gin.H{
				"code": response.ERROR, "message": "Request already in progress",
				"data": gin.H{"errorCode": "RESERVATION_IN_PROGRESS", "retryable": true},
			})
			return
		}
		if errors.Is(err, account.ErrReservationAlreadyProcessed) || errors.Is(err, account.ErrReservationReplayConflict) {
			c.JSON(http.StatusConflict, gin.H{
				"code": response.ERROR, "message": "Idempotency key already used",
				"data": gin.H{"errorCode": "RESERVATION_REPLAY_CONFLICT", "retryable": false},
			})
			return
		}
		globals.Error(fmt.Sprintf("[Canvas API] Reserve failed for optimize prompt (user %d, cost %d): %v", uid, creditCost, err))
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    response.ERROR,
			"message": "Server error",
		})
		return
	}

	// Mirror the Chat / AgentChat reservation lifecycle: closures keep
	// finalize/release idempotent, the defer is a catch-all so any
	// future return path that forgets to flip shouldFinalize still
	// releases the hold (instead of leaking the reserved credits until
	// the next nightly sweep).
	reservationSettled := false
	releaseReservation := func(reason string) {
		if reservationSettled {
			return
		}
		reservationSettled = true
		if err := db.Transaction(func(tx *gorm.DB) error {
			return reservationSvc.Release(tx, reservationID)
		}); err != nil {
			globals.Error(fmt.Sprintf("[Canvas API] Failed to release reservation for optimize prompt (user %d, reason=%s): %v", uid, reason, err))
		}
	}
	finalizeReservation := func() {
		if reservationSettled {
			return
		}
		reservationSettled = true
		if err := db.Transaction(func(tx *gorm.DB) error {
			return reservationSvc.Finalize(tx, reservationID, creditCost)
		}); err != nil {
			globals.Error(fmt.Sprintf("[Canvas API] Failed to finalize reservation for optimize prompt (user %d): %v", uid, err))
		}
	}
	shouldFinalize := false
	defer func() {
		if shouldFinalize {
			finalizeReservation()
		} else {
			releaseReservation("handler_unhandled_return")
		}
	}()

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  fmt.Sprintf("Optimize this %s generation prompt: %s", mode, req.Prompt),
		Temperature:  0.7,
		MaxTokens:    1000,
		UserID:       uid,
		Service:      "canvas-optimizer",
		RequestID:    requestID,
	}

	llmSvc := llmService.GetService()
	llmCtx, cancelLLM := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancelLLM()
	resp, err := llmSvc.ProcessRequest(llmCtx, llmReq)
	if err != nil || resp == nil || !resp.Success {
		errMsg := "unknown LLM error"
		if err != nil {
			errMsg = err.Error()
		} else if resp != nil && resp.Error != "" {
			errMsg = resp.Error
		}
		globals.Error(fmt.Sprintf("[Canvas API] Prompt optimization failed for User %d: %v", uid, errMsg))
		releaseReservation("llm_failed")
		respondCanvasErrorWithCode(c, "Failed to optimize prompt", "OPTIMIZE_PROMPT_FAILED")
		return
	}

	shouldFinalize = true
	finalizeReservation()

	optimizedPrompt := strings.TrimSpace(resp.Content)

	globals.Info(fmt.Sprintf("[Canvas API] Successfully optimized %s prompt for User %d", mode, uid))
	if err := toolsService.CreateUsageRecordTx(db, int(uid), "canvas_optimize_prompt", 0, creditCost, model.STATUS_SUCCESS, int(time.Since(startTime).Seconds()), &toolsService.UsageRecordMeta{
		IP:         utils.GetClientIP(c.Request),
		DeviceInfo: c.GetHeader("User-Agent"),
		ToolParams: map[string]interface{}{
			"mode":         mode,
			"promptLength": len(strings.TrimSpace(req.Prompt)),
		},
		ResultMetadata: map[string]interface{}{
			"optimizedPromptLength": len(optimizedPrompt),
		},
	}); err != nil {
		globals.Error(fmt.Sprintf("[Canvas API] Failed to create optimize prompt usage record: %v", err))
	}

	response.OkWithData(gin.H{
		"mode":            mode,
		"optimizedPrompt": optimizedPrompt,
	}, c)
}

func canvasOptimizePromptSystemPrompt(mode string) string {
	if mode == "video" {
		return `You are an expert AI video generation prompt engineer. Your task is to take the user's short input and expand it into a clear, executable video generation prompt for modern AI video models.
Your output should strictly be the optimized prompt text alone without any introductory or conversational filler.
Include the subject, action, scene setting, camera movement, visual motion, pacing, mood, lighting, and composition. Keep it practical for a short generated clip; do not add image-only terms unless they help the video style.`
	}
	return `You are an expert AI image generation prompt engineer. Your task is to take the user's short input and expand it into a highly detailed, professional prompt suitable for models like Midjourney or Flux.
Your output should strictly be the optimized prompt text alone without any introductory or conversational filler.
Include relevant artistic styles, lighting conditions (e.g., cinematic lighting, volumetric, golden hour), camera/lens details (e.g., 35mm lens, depth of field, 8k resolution), and composition instructions to ensure the highest quality output.`
}
