package tools

// canvas_headers.go · canvas-specific request-header parsers extracted
// from canvas_generation_api.go on 2026-05-15. These are the wire-shape
// adapters that read X-Canvas-* headers and turn them into a typed
// binding context the generation pipeline can consume.
//
// Test surface:
//   - parseCanvasProjectHeader        → canvas_api_test.go::TestParseCanvasProjectHeader
//   - parseCanvasAssetBindingsHeader  → canvas_headers_test.go + _more_test.go
//   - resolveCanvasTaskBindingContext → canvas_headers_parse_test.go + _more_test.go
//
// Splitting these out keeps canvas_generation_api.go focused on the
// generation flow and the editing-handler bodies; the header parsers
// have their own contract surface, their own failure codes, and their
// own test files — the §13.3 file-size ceiling shouldn't host both.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	"server/model"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// parseCanvasProjectHeader reads the X-Canvas-Project-Id header and
// returns the project id as a uint. Zero means "not canvas-bound" —
// either the header is absent, malformed, or the caller is a
// non-canvas client reusing these endpoints.
func parseCanvasProjectHeader(c *gin.Context) uint {
	raw := strings.TrimSpace(c.GetHeader("X-Canvas-Project-Id"))
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

type canvasTaskBindingContext struct {
	ProjectID          uint
	ElementID          string
	GenerationRunID    string
	GenerationThreadID string
	PostCreate         func(tx *gorm.DB, tasks []*model.GenerationTask) error
}

func resolveCanvasTaskBindingContext(c *gin.Context, uid uint) (*canvasTaskBindingContext, int, string, string) {
	raw := strings.TrimSpace(c.GetHeader("X-Canvas-Project-Id"))
	if raw == "" {
		return nil, 0, "", ""
	}

	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || parsed == 0 {
		return nil, http.StatusBadRequest, "Invalid canvas project id", "INVALID_CANVAS_PROJECT"
	}
	projectID := uint(parsed)

	canEditProject, err := projectService.NewRepository(globals.GraDBs["system"]).CanEditProject(projectID, uint(uid))
	if err != nil {
		globals.Error(fmt.Sprintf("[Canvas AI] Failed to validate project %d for user %d: %v", projectID, uid, err))
		return nil, http.StatusInternalServerError, "Failed to validate canvas project", "CANVAS_PROJECT_VALIDATE_FAILED"
	}
	if !canEditProject {
		return nil, http.StatusForbidden, "Canvas project not found", "CANVAS_PROJECT_NOT_FOUND"
	}

	elementID, err := canvasService.NormalizeCanvasStorageKey(c.GetHeader("X-Canvas-Element-Id"), "canvas element id", false)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error(), "INVALID_CANVAS_ELEMENT"
	}
	generationRunID, err := canvasService.NormalizeCanvasStorageKey(c.GetHeader("X-Canvas-Generation-Run-Id"), "canvas generation run id", false)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error(), "INVALID_CANVAS_GENERATION_RUN"
	}
	generationThreadID, err := canvasService.NormalizeCanvasStorageKey(c.GetHeader("X-Canvas-Generation-Thread-Id"), "canvas generation thread id", false)
	if err != nil {
		return nil, http.StatusBadRequest, err.Error(), "INVALID_CANVAS_GENERATION_THREAD"
	}
	return &canvasTaskBindingContext{
		ProjectID:          projectID,
		ElementID:          elementID,
		GenerationRunID:    generationRunID,
		GenerationThreadID: generationThreadID,
		PostCreate: func(tx *gorm.DB, tasks []*model.GenerationTask) error {
			for _, task := range tasks {
				if task == nil || task.TaskID == "" {
					continue
				}
				binding := model.CanvasTaskBinding{
					UID:                uid,
					ProjectID:          projectID,
					TaskID:             task.TaskID,
					ElementID:          elementID,
					GenerationRunID:    generationRunID,
					GenerationThreadID: generationThreadID,
				}
				if err := tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "task_id"}},
					DoNothing: true,
				}).Create(&binding).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}, 0, "", ""
}

// parseCanvasAssetBindingsHeader reads the X-Canvas-Asset-Bindings
// header — a JSON-encoded model.AssetBinding — and returns a parsed
// binding. The frontend stashes the active element's binding there so
// generation handlers don't have to re-read the canvas document.
// Returns nil when the header is absent, blank, or unparseable; the
// injector then becomes a no-op rather than failing the generation.
//
// Sanitize() runs immediately after unmarshal so any client-forged
// out-of-range / NaN / non-integer-key entries in the weight maps are
// dropped or clamped before the binding is persisted into the task's
// request_data. The injector's weightFor still clamps at consumption
// time as defense in depth — Sanitize keeps the stored payload clean.
func parseCanvasAssetBindingsHeader(c *gin.Context) *model.AssetBinding {
	raw := strings.TrimSpace(c.GetHeader("X-Canvas-Asset-Bindings"))
	if raw == "" {
		return nil
	}
	var binding model.AssetBinding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return nil
	}
	if binding.Scope == "" && len(binding.CharacterIDs) == 0 && len(binding.BrandIDs) == 0 && len(binding.ProductIDs) == 0 {
		return nil
	}
	binding.Sanitize()
	return &binding
}
