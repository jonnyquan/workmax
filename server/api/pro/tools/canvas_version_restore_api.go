package tools

// canvas_version_restore_api.go — C-09 restore endpoint
// (POST /api/tools/canvas/projects/:id/versions/:version/restore).
//
// Wraps canvasService.RestoreCanvasSnapshot. Body fields are
// optional:
//
//   expectedProjectUpdatedAt: RFC3339Nano timestamp the client last
//     observed; trips ErrCanvasVersionConflict on mismatch.
//
//   danglingAssets: "abort" (default) | "allow". Abort surfaces
//     dangling refs as 409 CANVAS_RESTORE_DANGLING_ASSETS and lets
//     the client present a confirmation step; allow proceeds with
//     the restore and returns the list in the success payload so the
//     caller can still warn the user.
//
// The dangling-list payload is intercepted before
// respondCanvasErrorFromError so the JSON envelope carries both the
// error code AND the dangling array — the matchCanvasSentinel entry
// for ErrCanvasRestoreDanglingAssets is the fallback for handlers
// that don't need that detail.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type restoreCanvasVersionRequest struct {
	ExpectedProjectUpdatedAt *string `json:"expectedProjectUpdatedAt,omitempty"`
	DanglingAssets           *string `json:"danglingAssets,omitempty"`
}

// RestoreVersion godoc
// @Summary Canvas — restore a historical snapshot as the live document
// @Description Copies w_canvas_snapshot.document of the named version
// @Description into project.document, allocates a new audit snapshot
// @Description (source="restored", message="Restored from vN"), and
// @Description bumps project.latest_version. The source snapshot is
// @Description never mutated. Asset references are validated against
// @Description live w_global_asset rows; the danglingAssets policy
// @Description controls whether unresolved refs abort or pass through.
// @Tags Tools:Canvas (Pro)
// @Accept json
// @Produce json
// @Param  id      path int true "Project ID"
// @Param  version path int true "Source snapshot_no to restore from"
// @Router /api/tools/canvas/projects/{id}/versions/{version}/restore [post]
func (a *CanvasApi) RestoreVersion(c *gin.Context) {
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
	versionNum, err := strconv.Atoi(c.Param("version"))
	if err != nil || versionNum <= 0 {
		respondCanvasError(c, "Invalid version")
		return
	}

	// Cap the body — restore payload is tiny (two optional fields).
	// The 4 KiB ceiling protects against a runaway POST holding a
	// connection while ShouldBindJSON streams the body.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	var req restoreCanvasVersionRequest
	// ShouldBindJSON tolerates an empty body — both fields are
	// optional, so a default-policy abort restore is the no-body call.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			respondCanvasError(c, "Invalid request: "+err.Error())
			return
		}
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

	policy := canvasService.DanglingAssetsAbort
	if req.DanglingAssets != nil {
		switch strings.TrimSpace(*req.DanglingAssets) {
		case "", string(canvasService.DanglingAssetsAbort):
			policy = canvasService.DanglingAssetsAbort
		case string(canvasService.DanglingAssetsAllow):
			policy = canvasService.DanglingAssetsAllow
		default:
			respondCanvasErrorWithCode(c, "Invalid danglingAssets policy", canvasAIErrorInvalidReq)
			return
		}
	}

	result, err := canvasService.RestoreCanvasSnapshot(
		c.Request.Context(),
		globals.GraDBs["system"],
		uidInt,
		projectID64,
		versionNum,
		canvasService.RestoreSnapshotInput{
			ExpectedProjectUpdatedAt: expectedProjectUpdatedAt,
			DanglingAssets:           policy,
		},
	)
	if err != nil {
		if errors.Is(err, canvasService.ErrCanvasRestoreDanglingAssets) {
			// Special-cased so the dangling list rides along with the
			// error code. Generic respondCanvasErrorFromError would
			// emit just the code + message — clients couldn't render
			// "these N assets won't resolve, retry with allow?".
			respondCanvasDanglingAssets(c, result)
			return
		}
		respondCanvasErrorFromError(c, err, "Restore version failed")
		return
	}

	response.OkWithData(result, c)
}

// respondCanvasDanglingAssets emits the 409 envelope for the
// ErrCanvasRestoreDanglingAssets path. Shape mirrors what
// respondCanvasErrorWithCode would produce, plus the dangling list +
// restoredFrom hint so the client can render a confirmation flow
// without a second round-trip.
func respondCanvasDanglingAssets(c *gin.Context, result canvasService.RestoreSnapshotResult) {
	message := "Restore source references assets that no longer exist. Retry with danglingAssets:\"allow\" to proceed anyway."
	body := gin.H{
		"code":    http.StatusConflict,
		"message": message,
		"data": gin.H{
			"success":        false,
			"errorCode":      "CANVAS_RESTORE_DANGLING_ASSETS",
			"message":        message,
			"restoredFrom":   result.RestoredFrom,
			"danglingAssets": result.DanglingAssets,
		},
		"errorCode": "CANVAS_RESTORE_DANGLING_ASSETS",
	}
	c.JSON(http.StatusConflict, body)
}
