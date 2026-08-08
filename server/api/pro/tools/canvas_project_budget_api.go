package tools

// canvas_project_budget_api.go — P1 #6 slice 3.
//
// REST surface for the per-project credit budget feature
// (Team / Enterprise tier). Separate file from canvas_api.go
// because the feature is cross-tool (any platform service that
// charges credits against a project will surface here), not a
// canvas-specific concern. It mounts under the canvas routes
// today because that's where the project resource lives —
// when projects graduate to a dedicated /api/projects surface,
// these handlers move with them without rewriting.
//
// Endpoints:
//
//   GET /api/tools/canvas/projects/:id/budget
//        Returns {cap, used, remaining, exceeded} for the
//        project. Cap is null for uncapped projects; remaining
//        is -1 sentinel in that case.
//
//   PUT /api/tools/canvas/projects/:id/budget
//        Body: {cap: int | null}. null clears the cap (unlimited);
//        non-negative integer sets it; 0 is a "paused" project.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"server/model/common/response"
	"server/service/project"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// budgetStatusResponse is the wire shape both endpoints emit.
// Cap is **int so JSON can carry an explicit null distinct from
// "absent" — the picker UI needs that distinction to decide
// between "uncapped" and "I haven't loaded the field yet".
type budgetStatusResponse struct {
	Cap       *int `json:"cap"`
	Used      int  `json:"used"`
	Remaining int  `json:"remaining"`
	Exceeded  bool `json:"exceeded"`
}

func projectBudgetToWire(s *project.BudgetStatus) budgetStatusResponse {
	return budgetStatusResponse{
		Cap:       s.Cap,
		Used:      s.Used,
		Remaining: s.Remaining,
		Exceeded:  s.Exceeded,
	}
}

// GetProjectBudget returns the current cap + tally. Used by the
// project page's budget progress bar (slice 3 frontend) and any
// agent-rail surface that wants to render "credits used: N / M".
func (a *CanvasApi) GetProjectBudget(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	status, err := project.Default().GetBudgetStatusForOwner(uint(id64), uid)
	switch {
	case err == nil:
		response.OkWithData(projectBudgetToWire(status), c)
	case errors.Is(err, gorm.ErrRecordNotFound):
		// Match the IDOR pattern across the canvas API:
		// missing-row + cross-tenant collapse to one body.
		respondCanvasError(c, "Project not found")
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load project budget"})
	}
}

// setProjectBudgetRequest carries the PUT body. Cap is
// json.RawMessage because encoding/json's `**int` shape can't
// distinguish `{"cap": null}` from `{}` — both produce a nil
// outer pointer. RawMessage preserves the literal bytes so the
// handler can:
//   - len==0           → field absent  → reject as missing
//   - bytes == "null"  → explicit null → clear the cap (unlimited)
//   - parse as int     → set the cap
type setProjectBudgetRequest struct {
	Cap json.RawMessage `json:"cap"`
}

// SetProjectBudget writes the cap. Pass `null` to clear
// (unlimited); pass an integer ≥ 0 to set. Negative caps
// rejected.
func (a *CanvasApi) SetProjectBudget(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}

	id64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return
	}

	var body setProjectBudgetRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		respondCanvasError(c, "cap field is required (use null to clear)")
		return
	}
	if len(body.Cap) == 0 {
		respondCanvasError(c, "cap field is required (use null to clear)")
		return
	}

	// Three branches:
	//   1. literal "null" → clear (capVal stays nil)
	//   2. parse as int   → set
	//   3. anything else  → bad request
	var capVal *int
	if !bytes.Equal(bytes.TrimSpace(body.Cap), []byte("null")) {
		var n int
		if err := json.Unmarshal(body.Cap, &n); err != nil {
			respondCanvasError(c, "cap must be an integer or null")
			return
		}
		capVal = &n
	}

	if err := project.Default().SetBudgetCapForOwner(uint(id64), uid, capVal); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			respondCanvasError(c, "Project not found")
		default:
			// SetBudgetCapForOwner returns its own validation error
			// for negative caps; surface the message directly so the
			// frontend can show it.
			respondCanvasError(c, err.Error())
		}
		return
	}

	// Round-trip the new status so the picker can update its
	// progress bar without a second GET.
	status, err := project.Default().GetBudgetStatusForOwner(uint(id64), uid)
	if err != nil {
		// Unexpected: the SET just succeeded. Treat as 500.
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload budget"})
		return
	}
	response.OkWithData(projectBudgetToWire(status), c)
}
