package workagent

// agent_other_endpoints.go holds the sibling API endpoints that are
// mounted on AIChatApiNew but unrelated to the per-turn chat flow:
//
//   - GetSSEConnectionStats: admin-only readout of the SSE connection
//     manager's internal counters.
//   - DeleteMessage: full-or-partial delete of a chat_message row,
//     with the assistant/user split-format the frontend uses.
//   - RateMessage: the user's 👍/👎 + optional feedback on a specific
//     assistant message (P0 #3 critique loop).
//
// Pulled out of agent_api.go (B1 chunk 6) so that file can stay
// focused on HandleAgentChat. Same package; route wiring in
// aichat_router.go works unchanged.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	workagentModel "server/model/workagent"
	desktopsync "server/service/desktop/sync"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetSSEConnectionStats returns SSE connection statistics
func (api *AIChatApiNew) GetSSEConnectionStats(c *gin.Context) {
	manager := workagentService.GetGlobalSSEManager()
	stats := manager.GetStats()
	c.JSON(http.StatusOK, gin.H{"data": stats})
}

// DeleteMessage deletes a specific chat message or part of it
// Supports format: messageId-type (e.g., "980605-assistant" or "980605-user")
//
// IDOR-EXEMPT-FUNC: two-step load (message.LoadByID + thread.LoadByID)
// followed by manual `thread.UID != int(uid)` cross-tenant check (line
// ~110). The pattern is intentional: loading the row first lets us
// distinguish "missing message" from "cross-tenant message" in
// telemetry, while presenting both as a generic 404 to the caller
// (CWE-639 enumeration-oracle defence). A LoadByIDForOwner here would
// fold the two cases into one and lose the diagnostic signal. See the
// inline comment at the cross-tenant guard below for the full
// rationale + cross-references to the matching patterns in
// DeleteConversation and GetConversationDetail.
func (api *AIChatApiNew) DeleteMessage(c *gin.Context) {
	uid := utils.GetUserID(c)
	messageIdParam := c.Param("id")

	if messageIdParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message ID is required"})
		return
	}

	// Parse messageId format: "messageId-type"
	parts := strings.Split(messageIdParam, "-")
	if len(parts) != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message ID format. Expected format: messageId-type"})
		return
	}

	messageIdStr := parts[0]
	messageType := parts[1]

	// Validate message type
	if messageType != "assistant" && messageType != "user" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message type. Must be 'assistant' or 'user'"})
		return
	}

	// Convert messageId to uint
	messageIdUint, err := strconv.ParseUint(messageIdStr, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message ID"})
		return
	}

	// Get the message first to check ownership. Use errors.Is on the
	// sentinel (the previous \`err.Error() == "record not found"\` was
	// fragile against any GORM error-text change and missed wrapped
	// errors entirely).
	msgRepo := workagentService.DefaultMessageRepository()
	message, err := msgRepo.LoadByID(uint(messageIdUint))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Message not found"})
		} else {
			globals.Error(fmt.Sprintf("Failed to get message: %v", err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve message"})
		}
		return
	}

	// Get the thread to check ownership. LoadByID is the no-uid
	// variant — the uid match is performed below as part of the
	// generic-404 IDOR defence; loading the row first lets us see
	// whether to respond "not found" (cross-tenant) vs. 5xx (real DB
	// failure).
	threadPtr, err := workagentService.DefaultThreadRepository().LoadByID(uint(message.ThreadID))
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to get thread: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify permissions"})
		return
	}
	thread := *threadPtr

	// Cross-tenant guard: return the same generic "not found" the
	// unknown-id branch returns so we don't leak the existence of
	// another user's message (CWE-639). The previous shape returned
	// 403 "Permission denied", which gave an attacker a scan oracle:
	// any message ID returning 403 belongs to a real message owned
	// by a different user. Match the DeleteConversation pattern at
	// conversation_api.go:358 and the GetConversationDetail share-
	// link pattern at conversation_share.go:84-87 — generic 404.
	if thread.UID != int(uid) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Message not found"})
		return
	}

	// Decide partial-clear vs full-delete based on which field the
	// caller wants cleared AND whether the OTHER field is already
	// empty (a clear that empties both fields is a delete).
	var shouldDeleteRecord bool
	if messageType == "assistant" {
		shouldDeleteRecord = (message.UserText == "")
	} else { // messageType == "user"
		shouldDeleteRecord = (message.AIText == "")
	}

	if shouldDeleteRecord {
		// P1.A.5b: wrap delete + tombstone in one transaction so
		// they land atomically. Tombstone-without-delete OR delete-
		// without-tombstone would leave desktop sidecars desynced.
		err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("id = ?", messageIdUint).
				Delete(&workagentModel.ChatMessage{}).Error; err != nil {
				return fmt.Errorf("delete message: %w", err)
			}
			return desktopsync.InsertTombstone(tx, int(uid),
				workagentModel.TombstoneEntityTypeMessage,
				uint(messageIdUint), message.UUID)
		})
		if err != nil {
			globals.Error(fmt.Sprintf("Failed to delete message: %v", err))
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete message"})
			return
		}
		globals.Info(fmt.Sprintf("Successfully deleted entire message %d for user %d", messageIdUint, uid))

		if err := api.updateThreadStatistics(uint(message.ThreadID)); err != nil {
			globals.Error(fmt.Sprintf("Failed to update thread statistics after delete: %v", err))
		}

		c.JSON(http.StatusOK, gin.H{"message": "Message deleted completely"})
		return
	}

	// Partial clear — leave the other field intact.
	var clearErr error
	if messageType == "assistant" {
		clearErr = msgRepo.ClearAIText(uint(messageIdUint))
	} else {
		clearErr = msgRepo.ClearUserText(uint(messageIdUint))
	}
	if clearErr != nil {
		globals.Error(fmt.Sprintf("Failed to update message: %v", clearErr))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update message"})
		return
	}
	globals.Info(fmt.Sprintf("Successfully cleared %s text for message %d (user %d)", messageType, messageIdUint, uid))

	if err := api.updateThreadStatistics(uint(message.ThreadID)); err != nil {
		globals.Error(fmt.Sprintf("Failed to update thread statistics after update: %v", err))
	}

	// messageType is validated upstream as ASCII "user" / "assistant",
	// so capitalising the first byte is correct and avoids
	// strings.Title (deprecated; documented Unicode boundary bugs).
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("%s message cleared successfully", capitalizeASCII(messageType))})
}

// maxFeedbackBytes caps the user-typed critique to a budget that
// keeps the next-turn system prompt readable. 4 KiB easily covers
// a paragraph of concrete corrections ("less neon, more film-noir,
// keep the wide shot, swap the protagonist's jacket to navy") and
// rejects a 10 MB paste-disaster that would blow the agent's
// preflight token budget on this single field.
const maxFeedbackBytes = 4 * 1024

// rateMessageRequest is the POST /chat/message/:id/rate body shape.
// Rating uses an int8 pointer so the binding distinguishes "field
// omitted" from "rating: 0" — a missing key returns 400 rather
// than silently clearing the user's prior rating. Feedback is
// optional (no pointer) because empty-string is a legitimate
// value for thumbs-up / clear paths.
type rateMessageRequest struct {
	Rating   *int8  `json:"rating" binding:"required"`
	Feedback string `json:"feedback"`
}

// RateMessage records the user's 👍/👎 + optional critique on one
// assistant message. The repo's SetUserRatingForOwner does the
// owner-scoping in WHERE (uid-in-query); the handler only parses,
// validates the rating range upstream, and surfaces sentinel errors
// as 4xx-shaped responses.
//
// Why a dedicated endpoint instead of a generic PATCH /message/:id:
// PATCH would mix the critique-loop write with the (currently
// nonexistent) general "edit a message" surface, and the right
// boundary today is "rating is its own affordance" (the SDK
// session/turn state doesn't react to other PATCH fields).
//
// Rate-limiting: not gated here. The repo write is a single
// indexed UPDATE; abuse would have to go through the auth layer's
// existing per-uid rate limits, which already cover the wider
// chat surface area. A future per-message rate-cap (e.g. "can't
// re-rate the same message 100 times in 10s") would land at the
// middleware layer, not here.
func (api *AIChatApiNew) RateMessage(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	messageIdParam := c.Param("id")
	if messageIdParam == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message ID is required"})
		return
	}
	messageIdUint, err := strconv.ParseUint(messageIdParam, 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message ID"})
		return
	}

	var body rateMessageRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating is required"})
		return
	}

	// Pre-trim + cap. The repo will reject ratings outside [-1, 1]
	// via ErrInvalidRating, but the byte cap is a handler concern —
	// the repo doesn't know what "fits in a preflight budget".
	feedback := strings.TrimSpace(body.Feedback)
	if len(feedback) > maxFeedbackBytes {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("feedback exceeds %d bytes", maxFeedbackBytes),
		})
		return
	}

	err = workagentService.DefaultMessageRepository().
		SetUserRatingForOwner(uint(messageIdUint), uid, *body.Rating, feedback)
	switch {
	case err == nil:
		globals.Info(fmt.Sprintf("Rated message %d for user %d: rating=%d feedback-len=%d",
			messageIdUint, uid, *body.Rating, len(feedback)))
		c.JSON(http.StatusOK, gin.H{
			"id":       messageIdUint,
			"rating":   *body.Rating,
			"feedback": feedback,
		})
	case errors.Is(err, workagentService.ErrInvalidRating):
		// 4xx-shaped validation. The sentinel branch matches the
		// MCP connector validation refactor's pattern — errors.Is,
		// not string compare.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Single 404 covers both "message doesn't exist" and
		// "message exists under a different uid" — no oracle.
		c.JSON(http.StatusNotFound, gin.H{"error": "Message not found"})
	default:
		globals.Error(fmt.Sprintf("Failed to rate message %d for user %d: %v", messageIdUint, uid, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rate message"})
	}
}
