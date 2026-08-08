package tools

// canvas_orphan_assets_admin_api.go · admin-only preview for the C-06
// element-deletion orphan-asset surface. READ-ONLY. Calls
// canvasService.ScanOrphanElementAssets to walk every active project,
// compute the per-project orphan count, and return the aggregated
// distribution + top-K worst offenders.
//
// Stage 1 of C-06. The numbers this endpoint exposes are the gating signal for Stage 2
// (admin sweep + storage GC) — median ≥ 10 or p99 ≥ 200 is the
// promote threshold the owner ratified.
//
// AdminAuth-gated. Identical posture to /admin/stats and
// /admin/prompt-guard/reload: ops surface, not user-facing.

import (
	"net/http"
	"strconv"

	"server/globals"
	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
)

// AdminPreviewOrphanElementAssets godoc
// @Summary  Canvas admin — element-orphan asset preview (C-06 Stage 1)
// @Description Walks every active canvas project, counts orphan element
// @Description assets per project, and returns the distribution stats
// @Description plus the top-K worst offenders. Read-only; no DB writes.
// @Tags Tools:Canvas (Admin)
// @Param   topK         query int false "Number of worst-offender rows to return (default 20)"
// @Param   pageSize     query int false "Project pagination batch size (default 100)"
// @Param   maxProjects  query int false "Hard cap on projects scanned in one call (default 0 = no cap)"
// @Produce json
// @Router /api/tools/canvas/admin/orphan-element-assets/preview [get]
func (a *CanvasApi) AdminPreviewOrphanElementAssets(c *gin.Context) {
	opts := canvasService.ScanOrphanElementAssetsOpts{
		TopK:        parsePositiveQueryInt(c, "topK", 20),
		PageSize:    parsePositiveQueryInt(c, "pageSize", 100),
		MaxProjects: parsePositiveQueryInt(c, "maxProjects", 0),
	}

	report, err := canvasService.ScanOrphanElementAssets(
		c.Request.Context(),
		globals.GraDBs["system"],
		opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, report)
}

// parsePositiveQueryInt reads a query param as a positive int, falling
// back to the default on missing / empty / unparseable / non-positive
// input. Local helper — keeping the admin handler dependency-free.
func parsePositiveQueryInt(c *gin.Context, key string, fallback int) int {
	raw := c.Query(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
