package tools

// canvas_prompt_guard_admin_api.go · admin-only hot-reload for the
// canvas PromptGuard sensitive-word list. Lets ops update the list
// without a server restart — closes the engineering side of D-01
// (the list value itself still comes from trust & safety / legal).
//
// The guard is initialised at process startup via
// initialize/outer.go → canvasService.Configure(cfg). This handler
// merges the current live config (PromptGuard.Snapshot()) with the
// caller-supplied delta and re-runs Configure() so the next Check
// call sees the new list. PromptGuard.Stats() counters are NOT
// reset — operational continuity across reloads.

import (
	"net/http"
	"strings"

	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

// ReloadPromptGuardRequest carries the partial update payload. Both
// fields are optional. Omitting `sensitiveWords` leaves the live
// list untouched (useful for a no-op probe); supplying it replaces
// the list wholesale (NOT additive — the wire shape is the new
// canonical list).
type ReloadPromptGuardRequest struct {
	SensitiveWords *[]string `json:"sensitiveWords,omitempty"`
}

// ReloadPromptGuardResponse echoes the post-reload counts so ops
// can confirm the change landed.
type ReloadPromptGuardResponse struct {
	SensitiveWordsCount  int `json:"sensitiveWordsCount"`
	MaxPromptLen         int `json:"maxPromptLen"`
	MaxNegativePromptLen int `json:"maxNegativePromptLen"`
	BlockedPatternsCount int `json:"blockedPatternsCount"`
}

// AdminReloadPromptGuard godoc
// @Summary Canvas admin — reload PromptGuard sensitive-word list
// @Description Hot-reloads the canvas PromptGuard config without a
// @Description server restart. Body is a partial update; omitted
// @Description fields preserve the live config. Returns the
// @Description post-reload counts.
// @Tags Tools:Canvas (Admin)
// @Accept json
// @Produce json
// @Router /api/tools/canvas/admin/prompt-guard/reload [post]
func (a *CanvasApi) AdminReloadPromptGuard(c *gin.Context) {
	// Cap the body to a generous 4 MiB. A real sensitive-words list
	// rarely exceeds tens of KiB; this is defense-in-depth against
	// a runaway POST that would otherwise hold a connection while
	// ShouldBindJSON streams the body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<20)

	var req ReloadPromptGuardRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request: " + err.Error()})
		return
	}

	current := canvasService.Default().Snapshot()
	next := current

	if req.SensitiveWords != nil {
		// Normalise: trim each entry; drop empties. The guard itself
		// re-normalises on New() (lowercase + trim) but doing it once
		// here means the count we return reflects the words the guard
		// will actually scan against.
		words := *req.SensitiveWords
		cleaned := make([]string, 0, len(words))
		for _, w := range words {
			w = strings.TrimSpace(w)
			if w != "" {
				cleaned = append(cleaned, w)
			}
		}
		next.SensitiveWords = cleaned
	}

	canvasService.Configure(next)

	post := canvasService.Default()
	c.JSON(http.StatusOK, ReloadPromptGuardResponse{
		SensitiveWordsCount:  post.SensitiveWordCount(),
		MaxPromptLen:         next.MaxPromptLen,
		MaxNegativePromptLen: next.MaxNegativePromptLen,
		BlockedPatternsCount: len(next.BlockedPatterns),
	})
}
