package tools

// canvas_decision_log_api.go — per-project audit-style read view.
//
// REST surface for the decision log feature (borrowable idea from
// calesthio/OpenMontage's checkpoint logs): a flat JSON timeline of
// every generation that ran against a canvas project — which model,
// what prompt, how many credits, success/failure, duration. Re-uses
// existing rows (w_canvas_task_binding × w_generation_task); no new
// columns, no write path.
//
// Endpoint:
//
//   GET /api/tools/canvas/projects/:id/decision-log
//        Returns {projectId, entries, totals, truncated}.
//        Capped at canvas.MaxDecisionLogEntries (500) — set
//        `truncated: true` so the UI can show "older history hidden".
//        Cross-tenant / missing-project both 404 (IDOR-collapse).
//
// The companion service layer (service/tools/canvas/decision_log.go)
// owns the join + projection. This file is just the HTTP shape and
// auth/ownership gate.

import (
	"errors"
	"net/http"
	"strconv"

	"server/globals"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetProjectDecisionLog returns the project's task history in
// decision-log form. Authentication is required (uid from JWT);
// ownership is enforced inside BuildProjectDecisionLog so missing
// and cross-tenant return the same gorm.ErrRecordNotFound and
// collapse to one 404-shaped response.
func (a *CanvasApi) GetProjectDecisionLog(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	projectID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	db := globals.GraDBs["system"]
	if db == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database unavailable"})
		return
	}

	log, err := canvasService.BuildProjectDecisionLog(c.Request.Context(), db, uid, projectID)
	switch {
	case err == nil:
		response.OkWithData(log, c)
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Same IDOR-collapse posture as the project-budget handler:
		// missing-row + cross-tenant both look like "Project not found".
		respondCanvasError(c, "Project not found")
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load decision log"})
	}
}
