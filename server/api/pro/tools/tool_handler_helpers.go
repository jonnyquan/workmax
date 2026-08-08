package tools

// Shared helpers for the per-tool API surface (avatars, remover,
// upscaler, vectorizer, and any future tool with the same CRUD shape).
//
// Each of those tools exposes the same set of endpoints —
// GetTasks / GetActiveTasks / GetHistory / DeleteHistoryRecord /
// CancelTask / RetryTask — and the only per-tool variation is the
// list of tool_id aliases plus a feature-type membership check.
// Without this file, every tool used to copy-paste ~150 LOC of
// identical handler bodies; the only point of drift was usually a
// stale tool_id alias (or the "Task does not belong to X" message).

import (
	"net/http"
	"server/globals"
	"server/model"
	"server/service"
	toolsService "server/service/tools"
	"server/utils"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ToolHandlerConfig captures the per-tool wiring needed by the
// generic CRUD handlers below. Each tool API constructs one of
// these once (typically as a package-level var) and delegates its
// handler methods to the helpers.
type ToolHandlerConfig struct {
	// ToolIDs is the union of legacy + canonical tool_id values that
	// should match this tool in WHERE clauses. Multi-value because
	// some tools shipped under more than one canonical id.
	ToolIDs []string
	// FeatureType is the canonical TOOL_* constant. Cancel/Retry
	// uses it to confirm the task being acted on belongs to this
	// tool — defends against URL stuffing where uid matches but
	// tool doesn't.
	FeatureType string
	// CancelForbiddenMessage is shown when a Cancel/Retry call
	// targets a task from a different tool. Phrased like
	// "Task does not belong to avatars".
	CancelForbiddenMessage string
}

func (cfg *ToolHandlerConfig) isTaskOfTool(toolID string) bool {
	return toolsService.FeatureTypeForToolID(toolID) == cfg.FeatureType
}

// writeToolError sends the standard JSON error envelope used by every
// tool API in this package. The shape is `{code, message}` plus an
// optional `{data: {errorCode}}` when an explicit code is supplied;
// the frontend's getErrorCodeFromEnvelope helper depends on this exact
// layout.
func writeToolError(c *gin.Context, statusCode int, code int, message string, errorCode string) {
	resp := gin.H{
		"code":    code,
		"message": message,
	}
	if strings.TrimSpace(errorCode) != "" {
		resp["data"] = gin.H{
			"errorCode": errorCode,
		}
	}
	c.JSON(statusCode, resp)
}

// HandleGetTasks serves the recent-tasks list for a tool.
// Mirrors GET /api/tools/{tool}/tasks.
func (cfg *ToolHandlerConfig) HandleGetTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writeToolError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	tasks, err := toolsService.GetRecentTasksByUIDAndTool(int(uid), cfg.ToolIDs, limit)
	if err != nil {
		writeToolError(c, http.StatusInternalServerError, 500, "Failed to get tasks", "INTERNAL_ERROR")
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		dto := toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task)
		if task.RequestData != nil {
			dto["requestData"] = task.RequestData
		}
		result = append(result, dto)
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

// HandleGetActiveTasks serves the pending/processing-only task list.
// Mirrors GET /api/tools/{tool}/tasks/active.
func (cfg *ToolHandlerConfig) HandleGetActiveTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writeToolError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	tasks, err := toolsService.GetActiveTasksByUIDAndTool(int(uid), cfg.ToolIDs)
	if err != nil {
		writeToolError(c, http.StatusInternalServerError, 500, "Failed to get active tasks", "INTERNAL_ERROR")
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task))
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

// HandleGetHistory serves paginated generation history for a tool.
// Mirrors GET /api/tools/{tool}/history.
func (cfg *ToolHandlerConfig) HandleGetHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writeToolError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	withTotal := c.DefaultQuery("withTotal", "1") != "0"

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	records, total, err := genService.GetGenerationHistoryByToolID(uid, page, limit, cfg.ToolIDs, withTotal)
	if err != nil {
		writeToolError(c, http.StatusInternalServerError, 500, "Failed to get history", "INTERNAL_ERROR")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"items": records,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

// HandleDeleteHistoryRecord deletes a single generation record + its
// associated assets in one transaction. Mirrors
// DELETE /api/tools/{tool}/history/:id.
func (cfg *ToolHandlerConfig) HandleDeleteHistoryRecord(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writeToolError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		writeToolError(c, http.StatusBadRequest, 400, "Record ID is required", "INVALID_REQUEST")
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id IN ?", recordID, uid, cfg.ToolIDs).
		First(&record).Error; err != nil {
		writeToolError(c, http.StatusNotFound, 404, "Record not found", "NOT_FOUND")
		return
	}

	if err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return (&toolsService.GenerationObjectService{}).DeleteGenerationRecordsWithAssets(c.Request.Context(), tx, []model.GenerationRecord{record})
	}); err != nil {
		writeToolError(c, http.StatusInternalServerError, 500, "Failed to delete record", "INTERNAL_ERROR")
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": gin.H{"id": recordID}})
}

// authorizeTaskAction validates ownership + tool membership for
// Cancel/Retry. Returns (task, true) when the caller may proceed and
// (nil, false) when a response has already been written. The retry
// path additionally requires `task.Status == failed`, so it passes
// requireFailed=true.
func (cfg *ToolHandlerConfig) authorizeTaskAction(c *gin.Context, requireFailed bool) (*model.GenerationTask, bool) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writeToolError(c, http.StatusUnauthorized, 401, "Unauthorized", "UNAUTHORIZED")
		return nil, false
	}

	taskID := c.Param("taskId")
	if taskID == "" {
		writeToolError(c, http.StatusBadRequest, 400, "Task ID is required", "INVALID_REQUEST")
		return nil, false
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		writeToolError(c, http.StatusNotFound, 404, "Task not found", "NOT_FOUND")
		return nil, false
	}
	if task.UID != int(uid) {
		writeToolError(c, http.StatusForbidden, 403, "Access denied", "FORBIDDEN")
		return nil, false
	}
	if !cfg.isTaskOfTool(task.ToolID) {
		writeToolError(c, http.StatusForbidden, 403, cfg.CancelForbiddenMessage, "FORBIDDEN")
		return nil, false
	}

	if requireFailed && task.Status != model.TaskStatusFailed {
		writeToolError(c, http.StatusBadRequest, 400, "Only failed tasks can be retried", "INVALID_STATE")
		return nil, false
	}

	return task, true
}

// HandleCancelTask validates ownership and delegates to the shared
// GeneratorApi.CancelTask. Mirrors POST /api/tools/{tool}/task/:taskId/cancel.
func (cfg *ToolHandlerConfig) HandleCancelTask(c *gin.Context) {
	if _, ok := cfg.authorizeTaskAction(c, false); !ok {
		return
	}
	(&GeneratorApi{}).CancelTask(c)
}

// HandleRetryTask validates ownership + failed-state precondition and
// delegates to the shared GeneratorApi.RetryTask.
// Mirrors POST /api/tools/{tool}/task/:taskId/retry.
func (cfg *ToolHandlerConfig) HandleRetryTask(c *gin.Context) {
	if _, ok := cfg.authorizeTaskAction(c, true); !ok {
		return
	}
	(&GeneratorApi{}).RetryTask(c)
}
