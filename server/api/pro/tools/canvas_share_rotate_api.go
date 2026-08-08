package tools

// canvas_share_rotate_api.go — C-11 owner-action endpoint.
//
// POST /api/tools/canvas/projects/:id/share/rotate
//   200 { shareToken: "<64-char hex>" }
//
// Generates a fresh 256-bit token, stores it on w_global_project,
// and returns it so the FE can rebuild the share URL. Any previously
// distributed link without (or with the old) ?t=<token> drops to
// 404 at the public lookup path on the next request.
//
// Owner-only (requireProjectManageAccess enforced in the service).
// No body — the action is parameter-free. The new token is the only
// useful return value, so the response is intentionally minimal.

import (
	"strconv"

	"server/globals"
	"server/model/common/response"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type rotateShareTokenResponse struct {
	ShareToken string `json:"shareToken"`
}

// RotateShareToken godoc
// @Summary Canvas — rotate share-link token (revoke old URLs)
// @Description Generates a fresh share_token for the project. Any
// @Description previously distributed share link drops to 404 on
// @Description next request — the new URL must include ?t=<token>
// @Description returned by this endpoint. Owner-only.
// @Tags Tools:Canvas (Pro)
// @Produce json
// @Param  id path int true "Project ID"
// @Success 200 {object} response.Response{data=rotateShareTokenResponse}
// @Router /api/tools/canvas/projects/{id}/share/rotate [post]
func (a *CanvasApi) RotateShareToken(c *gin.Context) {
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

	token, err := canvasService.RotateProjectShareToken(
		c.Request.Context(),
		globals.GraDBs["system"],
		int(uid),
		uint(projectID64),
	)
	if err != nil {
		respondCanvasErrorFromError(c, err, "Rotate share token failed")
		return
	}

	response.OkWithData(rotateShareTokenResponse{ShareToken: token}, c)
}
