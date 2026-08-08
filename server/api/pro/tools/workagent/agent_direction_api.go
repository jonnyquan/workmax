package workagent

// agent_direction_api.go owns the M2 directions-picker submission
// endpoint. The flow:
//
//   1. Server emits a directions_picker block (DirectionsPickerDispatcher)
//   2. Frontend renders DirectionsPickerBlockRenderer with 5 cards
//   3. User clicks → onSelect → POST here with direction_id
//   4. We persist a synthetic chat_message row carrying
//      visual_direction_selected metadata
//   5. The next agent turn's preflight reads that row and bakes
//      OKLch + font_stack into SystemAdditions
//
// IDOR defence mirrors DeleteMessage: thread is loaded with
// LoadByIDForOwner so a cross-tenant POST gets a generic 404 rather
// than an existence oracle.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"server/globals"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// directionSelectionRequest is the POST body. direction_id MUST match
// an id in skills/_shared/visual-directions.yaml (validated server-
// side via PersistSelectedDirection's FindDirection guard). source
// is optional — the server defaults to "user_picker" so the analytics
// dashboard doesn't have to special-case missing values.
type directionSelectionRequest struct {
	DirectionID string `json:"direction_id" binding:"required"`
	Source      string `json:"source"`
}

// SubmitDirectionSelection handles POST /api/work-agent/threads/:id/direction.
//
// Returns:
//   - 200 OK + {"data":{"persisted":true}} on success
//   - 400 on missing/invalid direction_id (binding failure or unknown id)
//   - 404 when the thread doesn't belong to the caller (IDOR defence)
//   - 500 on persist failure (still rare — repo path is in-process)
//
// The persistence call is fail-soft inside PersistSelectedDirection
// (logs + skips on bad inputs), so the surface contract is "if you
// got 200, the next turn will see the selection." Operators inspect
// chat_message metadata if they need to verify after the fact.
func (api *AIChatApiNew) SubmitDirectionSelection(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	threadIDParam := c.Param("id")
	threadIDU64, err := strconv.ParseUint(threadIDParam, 10, 32)
	if err != nil || threadIDU64 == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid thread id"})
		return
	}
	threadID := uint(threadIDU64)

	var req directionSelectionRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body: direction_id required"})
		return
	}

	// IDOR guard: verify the thread belongs to the caller. Both
	// "thread doesn't exist" and "thread belongs to someone else"
	// collapse to the same 404 so a malicious client can't enumerate
	// thread ids by probing this endpoint.
	threadRepo := workagentService.DefaultThreadRepository()
	if _, err := threadRepo.LoadByIDForOwner(threadID, uid); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Thread not found"})
			return
		}
		globals.Error(fmt.Sprintf("[Direction API] thread lookup failed: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify thread ownership"})
		return
	}

	// PersistSelectedDirection swallows unknown direction ids with a
	// warn log. We re-validate up front to give the client a 400
	// instead of a silently no-op 200 — direction ids are a fixed
	// catalog (visual-directions.yaml) and the picker should never
	// emit anything outside it, so a bad id here means a client bug
	// worth surfacing.
	source := req.Source
	if source == "" {
		source = "user_picker"
	}

	if ok := workagentService.PersistSelectedDirection(uid, threadID, req.DirectionID, source); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Direction selection rejected (invalid direction id?)"})
		return
	}
	workagentService.PersistPassMode(uid, threadID, workagentService.WorkAgentPassModeFinalize, "direction_picker")

	resp := workagentService.SelectedDirectionResponse(req.DirectionID)
	resp["pass_mode"] = string(workagentService.WorkAgentPassModeFinalize)
	c.JSON(http.StatusOK, gin.H{"data": resp})
}
