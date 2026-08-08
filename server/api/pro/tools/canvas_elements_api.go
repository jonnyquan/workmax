package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model/common/response"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// canvas_elements_api.go — element-patch + shot-sync handlers
// for the canvas surface. Lifted out of canvas_api.go on the
// b1-slim-down "Route to M4-class gains" slice; both endpoints
// share an idempotent-reconciliation shape (element-id keyed
// patches / local-card-id keyed shots) and were the next
// cohesive cluster after the upload-limit helper extraction.
//
// Service layer untouched — canvasService.PatchElements +
// canvasService.SyncShots already own the business logic; this
// file only owns request parsing, validation, and the HTTP
// envelope.

type updateCanvasElementsRequest struct {
	Version                  *int                     `json:"version,omitempty"`
	ExpectedProjectUpdatedAt *string                  `json:"expectedProjectUpdatedAt,omitempty"`
	Elements                 []map[string]interface{} `json:"elements" binding:"required"`
}

// syncCanvasShotsRequest is the POST body for the shot-sync endpoint.
// `shots` is the ShotLink[] the client derived from its current canvas
// document — we treat the client as the source of truth for card order
// and timeline placement and reconcile the w_canvas_shot table to match.
type syncCanvasShotsRequest struct {
	Shots []canvasService.ShotLink `json:"shots"`
}

const (
	maxCanvasElementsPatchBodyBytes = 2 << 20 // 2 MiB
	maxCanvasElementPatches         = 500
	maxCanvasElementPatchBytes      = 64 << 10 // 64 KiB per patch object
	maxCanvasShotSyncBodyBytes      = 2 << 20  // 2 MiB
)

func validateCanvasElementPatchRequest(patches []map[string]interface{}) error {
	if len(patches) > maxCanvasElementPatches {
		return fmt.Errorf("too many element patches (max %d)", maxCanvasElementPatches)
	}
	for i, patch := range patches {
		if patch == nil {
			return fmt.Errorf("elements[%d] must be an object", i)
		}
		id, ok := patch["id"].(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return fmt.Errorf("elements[%d].id is required", i)
		}
		if len(id) > 128 || strings.ContainsAny(id, "\x00\r\n\t/\\") {
			return fmt.Errorf("elements[%d].id is invalid", i)
		}
		if len(patch) > 64 {
			return fmt.Errorf("elements[%d] has too many fields", i)
		}
		encoded, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("elements[%d] is invalid", i)
		}
		if len(encoded) > maxCanvasElementPatchBytes {
			return fmt.Errorf("elements[%d] is too large", i)
		}
	}
	return nil
}

// UpdateElements godoc
// @Summary Partial Update of Canvas Elements
// @Description Updates specific elements within the current version's document based on their ID without requiring a full document resync
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param id path int true "Project ID"
// @Param request body updateCanvasElementsRequest true "Elements to patch"
// @Success 200 {object} response.Response{data=object}
// @Router /api/tools/canvas/projects/{id}/elements [patch]
func (a *CanvasApi) UpdateElements(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasElementsPatchBodyBytes)
	var req updateCanvasElementsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	if len(req.Elements) == 0 {
		response.OkWithMessage("No elements to update", c)
		return
	}
	if err := validateCanvasElementPatchRequest(req.Elements); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}
	var expectedProjectUpdatedAt *time.Time
	if req.ExpectedProjectUpdatedAt != nil && strings.TrimSpace(*req.ExpectedProjectUpdatedAt) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*req.ExpectedProjectUpdatedAt))
		if err != nil {
			respondCanvasErrorWithCode(c, "Invalid expectedProjectUpdatedAt", "INVALID_PROJECT_REVISION")
			return
		}
		expectedProjectUpdatedAt = &parsed
	}

	result, err := canvasService.PatchElements(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64), canvasService.PatchElementsInput{
		Version:                  req.Version,
		ExpectedProjectUpdatedAt: expectedProjectUpdatedAt,
		Patches:                  req.Elements,
	})
	if err != nil {
		respondCanvasErrorFromError(c, err, "Failed to patch elements")
		return
	}
	if !result.Patched {
		response.OkWithMessage("No changes", c)
		return
	}

	response.OkWithData(gin.H{
		"patched": true,
		"version": result.Version,
	}, c)
}

// SyncShots reconciles the w_canvas_shot table with the ShotLinks in the
// request body. The endpoint is idempotent: replaying the same shots
// twice produces no net changes. See canvas-tool-design.md §13 M3-W7-02.
func (a *CanvasApi) SyncShots(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasShotSyncBodyBytes)
	var req syncCanvasShotsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}
	if err := canvasService.ValidateShotLinksForSync(req.Shots); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}

	db := globals.GraDBs["system"]

	canEditProject, err := projectService.NewRepository(db).CanEditProject(uint(projectID64), uint(uidInt))
	if err != nil {
		respondCanvasErrorFromError(c, err, "Sync shots failed")
		return
	}
	if !canEditProject {
		respondCanvasError(c, "Project not found")
		return
	}

	plan, err := canvasService.SyncShots(c.Request.Context(), db, uidInt, uint(projectID64), req.Shots)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Sync shots failed")
		return
	}

	response.OkWithData(gin.H{
		"created":   len(plan.Created),
		"updated":   len(plan.Updated),
		"deleted":   len(plan.Deleted),
		"unchanged": plan.Unchanged,
		"entries":   plan.Entries,
	}, c)
}
