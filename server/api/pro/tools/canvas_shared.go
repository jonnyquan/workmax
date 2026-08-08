package tools

// Canvas-shared helpers — extracted from canvas_chat_api.go on the
// 2026-05-15 chat-handler retirement (Task #15). These helpers
// originated in the chat handler but graduated into general
// canvas-stack utilities consumed by sibling handlers:
//
//   - canvasIdempotencyKey: canvas_optimize_prompt_api.go,
//     canvas_agent_api.go (indirectly via the X-Canvas-Request-Id
//     posture pinned in canvas_idempotency_test.go)
//   - applyCanvasPromptGuard: canvas_optimize_prompt_api.go
//   - canvasChatMaxMessageChars: canvas_agent_thread_api.go
//
// The name `canvasChatMaxMessageChars` retains its historical
// prefix even though it now lives outside the chat handler — it's
// the message-length policy shared across every canvas chat-shaped
// surface (agent, agent_thread, optimize_prompt), and renaming it
// in this refactor would mean churning three more files for no
// behavioural gain.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

// canvasChatMaxMessageChars caps per-message text length across
// every canvas chat-shaped surface (agent, agent_thread,
// optimize_prompt). 6000 runes matches the historical chat
// handler's limit; raising it would invalidate
// canvas_agent_thread_api.go's pre-write guards too.
const canvasChatMaxMessageChars = 6000

// canvasChatMaxURLLength caps any user-supplied URL accepted by
// canvas endpoints (attachment URLs, keyframe video references,
// etc.). 2048 matches the historical chat handler's cap; kept
// at the original name because canvas_api.go reads it for
// keyframe URL validation.
const canvasChatMaxURLLength = 2048

// canvasIdempotencyKey picks a stable key for the reservation layer. Clients
// that retry a mutating request should send X-Canvas-Request-Id so the retry
// lands on the same reservation row (no double-charge). If absent, we fall
// back to a server-generated key — unique per call, so no cross-retry dedup,
// but the reservation bookkeeping still works for the single call.
//
// The returned key is always prefixed with `tool:` so the same client-supplied
// header sent against canvas_optimize_prompt / canvas_agent can't collide on
// the (uid, idempotency_key) unique index. Without the scope, a buggy client
// reusing one X-Canvas-Request-Id across two endpoints would short-circuit
// the second call onto the first reservation — wrong tool, wrong cost,
// possibly a terminal row that 402s the user despite a valid request.
func canvasIdempotencyKey(c *gin.Context, tool string, uid int) string {
	if key := strings.TrimSpace(c.GetHeader("X-Canvas-Request-Id")); key != "" {
		return fmt.Sprintf("%s:%s", tool, key)
	}
	return fmt.Sprintf("%s_%d_%d", tool, uid, time.Now().UnixNano())
}

// applyCanvasPromptGuard runs the §12.3 PromptGuard against the supplied
// prompt/negative pair. On reject, it writes a 422 response and returns
// ok=false so the caller can abort before reserving credits. On allow
// or truncate, it returns the cleaned strings; the caller forwards
// those to the LLM instead of the raw input. Warnings are surfaced in
// the reject body but are otherwise swallowed — callers that want to
// forward them to SSE can read res.Warnings themselves if needed.
func applyCanvasPromptGuard(c *gin.Context, prompt, negativePrompt string) (cleanedPrompt, cleanedNegative string, warnings []string, ok bool) {
	res := canvasService.Default().Check(canvasService.GuardInput{
		Prompt:         prompt,
		NegativePrompt: negativePrompt,
	})
	if res.Action == canvasService.GuardActionReject {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"code":    http.StatusUnprocessableEntity,
			"message": "Prompt blocked by content guard",
			"error": gin.H{
				"code":   res.RejectReason,
				"reason": res.RejectReason,
			},
		})
		return "", "", nil, false
	}
	return res.CleanedPrompt, res.CleanedNegativePrompt, res.Warnings, true
}
