//go:build desktop

package desktop

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// maxAgentApproveBodyBytes bounds the approve payload: an id and a decision.
const maxAgentApproveBodyBytes = 512

// handleApproveAgentTurn delivers the user's answer to a pending tool
// approval. The pending set is keyed by (turn, approval id): an answer for a
// turn that already ended — timeout, cancel, completion — finds nothing and
// reports 404, which the renderer treats as "card expired", not an error.
func (s *Server) handleApproveAgentTurn(c *gin.Context) {
	if s.cfg.Approvals == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "approvals_unavailable"})
		return
	}
	turnUUID := c.Param("uuid")
	if _, err := cloudproxy.DesktopTurnRequestID(turnUUID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_turn_uuid"})
		return
	}
	var body struct {
		ApprovalID string `json:"approval_id"`
		Decision   string `json:"decision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxAgentApproveBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil ||
		body.ApprovalID == "" || len(body.ApprovalID) > 32 ||
		!agentruntime.ValidApprovalDecision(body.Decision) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_approval_body"})
		return
	}
	if !s.cfg.Approvals.Resolve(turnUUID, body.ApprovalID, agentruntime.ApprovalDecision(body.Decision)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "approval_not_pending"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"resolved": true})
}
