package tools

// canvas_consistency_api.go — Task #9 endpoint for the pre-render
// MLLM consistency check. Soft-warn by design: the handler never
// returns a 4xx that would block submission; it returns a
// judgment with warnings the FE can use to ask the user "proceed
// anyway?". The user is always in control.
//
// Surface:
//
//   POST /api/tools/canvas/projects/:id/preflight-consistency
//        Body: { prompt, negativePrompt?, mentions? }
//        Returns: { ok, summary, warnings, model, durationMs,
//                   skippedReason }
//
// FE flow:
//
//   1. user fills the prompt + hits "Generate"
//   2. FE calls this endpoint
//   3. if response.ok → submit immediately
//   4. else → show modal listing warnings, "Proceed anyway" /
//             "Edit prompt"; on "Proceed anyway", submit
//
// Model is chosen by config.yaml (aihub.text — currently
// deepseek). No new dependency, no new credit lane: the same
// LLM client the prompt builder uses.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	"server/model/common/response"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type preflightConsistencyRequest struct {
	Prompt         string   `json:"prompt"`
	NegativePrompt string   `json:"negativePrompt,omitempty"`
	Mentions       []string `json:"mentions,omitempty"`
}

const preflightConsistencyMaxBodyBytes = 1 << 20 // 1 MiB — prompts can be long, but not THAT long.

// PreflightConsistency runs the consistency judge against the
// project's prior shots and returns the verdict. Always 200 on
// success: a "concerns" verdict is data, not an error.
//
// IDOR-collapse posture: missing or cross-tenant project both
// produce 404, same as the rest of the canvas API surface.
func (a *CanvasApi) PreflightConsistency(c *gin.Context) {
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

	// Edit gate — match the generation submit path. Editors can spend
	// credits / render shots for shared projects, so the preflight check
	// must not be stricter than the eventual generation request. Missing,
	// non-member, and view-only all collapse to the same 404-shaped
	// response to avoid an existence oracle.
	access, err := projectService.NewRepository(db).ResolveAccess(uint(projectID), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondCanvasError(c, "Project not found")
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load project"})
		return
	}
	if !access.CanEdit() {
		respondCanvasError(c, "Project not found")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, preflightConsistencyMaxBodyBytes)
	var req preflightConsistencyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasError(c, "Invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		// An empty prompt is a configuration error on the FE
		// side — not a project state we should silently green-
		// light. Surface as a 400 so the bug shows up loudly.
		respondCanvasError(c, "Prompt is required")
		return
	}

	verdict, err := canvasService.BuildVideoConsistencyJudgment(
		c.Request.Context(),
		db,
		uid,
		projectID,
		canvasService.ConsistencyJudgeInput{
			Prompt:         req.Prompt,
			NegativePrompt: req.NegativePrompt,
			Mentions:       req.Mentions,
		},
	)
	if err != nil {
		// Defense in depth: the judge's contract is "never error
		// hard" but if a future change breaks that, log it and
		// degrade to a green-light verdict so submission can
		// proceed. Don't turn a check failure into a UX block.
		globals.Warn("[Canvas Consistency] judge errored, degrading to green-light: " + err.Error())
		response.OkWithData(canvasService.ConsistencyJudgment{
			OK:            true,
			SkippedReason: "judge error",
		}, c)
		return
	}

	response.OkWithData(verdict, c)
}
