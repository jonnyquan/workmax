package tools

import (
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	toolsService "server/service/tools"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// canvas_tasks_api.go — task-recovery list endpoint + its DTO
// builders. Lifted out of canvas_api.go on the b1-slim-down Tasks
// slice; this cluster is one cohesive surface ("the page reloaded
// mid-generation, give me back the tasks I lost track of") and
// owns its own join + DTO shape. The two helper builders only
// have one call site (ListCanvasTasks) so they stay package-
// private here.

// CanvasTaskDTO is the shape returned by ListCanvasTasks. It flattens
// the (binding, task) join into one client-friendly record. We
// deliberately leave raw request/result payloads off the list response. The
// recovery endpoint still returns normalized terminal media URLs so the canvas
// can hydrate completed tasks after a refresh race.
type CanvasTaskDTO struct {
	TaskID             string        `json:"taskId"`
	ElementID          string        `json:"elementId,omitempty"`
	ProjectID          uint          `json:"projectId"`
	GenerationRunID    string        `json:"generationRunId,omitempty"`
	GenerationThreadID string        `json:"generationThreadId,omitempty"`
	CanvasOperation    string        `json:"canvasOperation,omitempty"`
	Status             string        `json:"status"`
	Progress           int           `json:"progress"`
	Tool               string        `json:"tool"`
	Model              string        `json:"model"`
	CreditsUsed        int           `json:"creditsUsed"`
	Recovery           model.JSONMap `json:"recovery,omitempty"`
	CreatedAt          time.Time     `json:"createdAt"`
	UpdatedAt          time.Time     `json:"updatedAt"`
	StartedAt          *time.Time    `json:"startedAt,omitempty"`
	CompletedAt        *time.Time    `json:"completedAt,omitempty"`
	ErrorMsg           string        `json:"errorMsg,omitempty"`
	ImageURLs          []string      `json:"imageUrls,omitempty"`
	VideoURLs          []string      `json:"videoUrls,omitempty"`
	OutputURLs         []string      `json:"outputUrls,omitempty"`
	ThumbnailURL       string        `json:"thumbnailUrl,omitempty"`
}

func buildCanvasTaskRecoveryMeta(requestData model.JSONMap) model.JSONMap {
	if requestData == nil {
		return nil
	}
	meta := model.JSONMap{}
	copyString := func(dstKey, srcKey string, max int) {
		raw, ok := requestData[srcKey].(string)
		if !ok {
			return
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			return
		}
		if max > 0 && len([]rune(value)) > max {
			value = string([]rune(value)[:max])
		}
		meta[dstKey] = value
	}
	copyString("prompt", "prompt", 240)
	copyString("negativePrompt", "negativePrompt", 240)
	copyString("aspectRatio", "aspectRatio", 32)
	copyString("resolution", "resolution", 32)
	copyString("model", "model", 100)
	copyString("source", "source", 64)
	copyString("canvasOperation", "canvasOperation", 64)

	var params map[string]interface{}
	switch typed := requestData["params"].(type) {
	case map[string]interface{}:
		params = typed
	case model.JSONMap:
		params = typed
	}
	if params != nil {
		// String-typed recovery fields. mediaType / source are categorical.
		for _, key := range []string{"mediaType", "source"} {
			if value, exists := params[key]; exists {
				if typed, ok := value.(string); ok {
					if trimmed := strings.TrimSpace(typed); trimmed != "" && len([]rune(trimmed)) <= 100 {
						meta[key] = trimmed
					}
				}
			}
		}
		// Numeric recovery fields. params[key] may arrive as either a JSON
		// number (float64) or a string-encoded number from clients that
		// quote numeric form fields. Coerce string → float64 before
		// emitting so the FE (CanvasRecoveredTask.{duration,width,height}: number)
		// gets a number type, not "1024" — silent string concat in
		// arithmetic was a documented wire-shape drift (P1-⑦).
		for _, key := range []string{"duration", "width", "height"} {
			value, exists := params[key]
			if !exists {
				continue
			}
			switch typed := value.(type) {
			case float64:
				meta[key] = typed
			case int:
				meta[key] = float64(typed)
			case int64:
				meta[key] = float64(typed)
			case uint:
				meta[key] = float64(typed)
			case uint64:
				meta[key] = float64(typed)
			case string:
				trimmed := strings.TrimSpace(typed)
				if trimmed == "" {
					continue
				}
				if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
					meta[key] = parsed
				}
			}
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return meta
}

func buildCanvasTaskResultMedia(ctx *gin.Context, task model.GenerationTask) (imageURLs []string, videoURLs []string, outputURLs []string, thumbnailURL string) {
	if task.Status != model.TaskStatusCompleted || task.ResultData == nil {
		return nil, nil, nil, ""
	}
	(&toolsService.GenerationObjectService{}).ResolveTaskDownloadURLs(ctx.Request.Context(), &task)
	imageURLs = toolsService.ParseImageURLs(task.ResultData)
	videoURLs = toolsService.ParseVideoURLs(task.ResultData)
	outputURLs = toolsService.ParseOutputURLs(task.ResultData)
	thumbnailURL = toolsService.ParseThumbnailURL(task.ResultData)
	return imageURLs, videoURLs, outputURLs, thumbnailURL
}

// ListCanvasTasks is the recovery endpoint (canvas-tool-design M2-W4-01).
// The frontend calls it on project open / tab refocus to rediscover
// tasks it lost track of — typically because the page refreshed while
// a generation was still in flight.
//
// Query params:
//   - projectId (optional): if set, restrict to one canvas project
//   - status (optional): "active" (default, = pending+processing) or
//     "all" (include terminal states so clients can backfill UI)
//   - limit (optional, default 50, max 200)
//
// The binding table is authoritative for project membership: only
// tasks a canvas handler enqueued with X-Canvas-Project-Id will appear
// here. Legacy tasks without a binding are invisible by design —
// nothing here thinks it owns them.
func (a *CanvasApi) ListCanvasTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasUnauthorized(c)
		return
	}
	uidInt := int(uid)

	var projectFilter *uint
	if raw := strings.TrimSpace(c.Query("projectId")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			respondCanvasErrorWithCode(c, "Invalid projectId", "INVALID_PROJECT_ID")
			return
		}
		pid := uint(id)
		projectFilter = &pid
	}

	statusFilter := strings.ToLower(strings.TrimSpace(c.DefaultQuery("status", "active")))
	switch statusFilter {
	case "", "active", "all":
		// ok
	default:
		respondCanvasErrorWithCode(c, "Invalid status filter", "INVALID_TASK_STATUS_FILTER")
		return
	}

	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			if n < 1 {
				n = 1
			}
			if n > 200 {
				n = 200
			}
			limit = n
		}
	}
	page := 1
	if raw := strings.TrimSpace(c.Query("page")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			page = n
		}
	}
	noCount := strings.TrimSpace(c.Query("noCount")) == "1" || strings.EqualFold(strings.TrimSpace(c.Query("noCount")), "true")

	db := globals.GraDBs["system"]
	baseQuery := db.Table("w_canvas_task_binding AS b").
		Joins("JOIN w_global_project AS p ON p.id = b.project_id AND p.deleted_at IS NULL").
		Joins("JOIN w_generation_task AS t ON t.task_id = b.task_id").
		Where(`p.uid = ? OR EXISTS (
			SELECT 1 FROM w_global_project_member m
			WHERE m.project_id = p.id
			  AND m.uid = ?
			  AND m.deleted_at IS NULL
			  AND m.role IN ?
		)`, uidInt, uidInt, []string{
			model.GlobalProjectRoleOwner,
			model.GlobalProjectRoleEditor,
			model.GlobalProjectRoleViewer,
			model.GlobalProjectRoleCommenter,
		})

	if projectFilter != nil {
		baseQuery = baseQuery.Where("b.project_id = ?", *projectFilter)
	}

	if statusFilter == "" || statusFilter == "active" {
		baseQuery = baseQuery.Where("t.status IN ?", []model.TaskStatus{
			model.TaskStatusPending,
			model.TaskStatusProcessing,
		})
	}

	type row struct {
		ProjectID          uint
		TaskID             string
		ElementID          string
		GenerationRunID    string
		GenerationThreadID string
		RequestData        model.JSONMap
		ResultData         model.JSONMap
		RecordID           uint
		Status             model.TaskStatus
		Progress           int
		ToolID             string
		Model              string
		CreditsUsed        int
		CreatedAt          time.Time
		UpdatedAt          time.Time
		StartedAt          *time.Time
		CompletedAt        *time.Time
		ErrorMsg           string
	}

	var total int64
	if !noCount {
		if err := baseQuery.Count(&total).Error; err != nil {
			respondCanvasError(c, "Count tasks failed: "+err.Error())
			return
		}
	}

	var rows []row
	query := baseQuery.Select("b.project_id, b.task_id, b.element_id, b.generation_run_id, b.generation_thread_id, t.request_data, t.result_data, t.record_id, t.status, t.progress, t.tool_id, t.model, t.credits_used, t.created_at, t.updated_at, t.started_at, t.completed_at, t.error_msg")
	if err := query.Order("t.created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&rows).Error; err != nil {
		respondCanvasError(c, "List tasks failed: "+err.Error())
		return
	}

	items := make([]CanvasTaskDTO, 0, len(rows))
	for _, r := range rows {
		canvasOperation := ""
		if r.RequestData != nil {
			if v, ok := r.RequestData["canvasOperation"].(string); ok {
				canvasOperation = strings.TrimSpace(v)
			}
		}
		imageURLs, videoURLs, outputURLs, thumbnailURL := buildCanvasTaskResultMedia(c, model.GenerationTask{
			TaskID:     r.TaskID,
			ToolID:     r.ToolID,
			Status:     r.Status,
			ResultData: r.ResultData,
			RecordID:   r.RecordID,
		})
		items = append(items, CanvasTaskDTO{
			TaskID:             r.TaskID,
			ElementID:          r.ElementID,
			ProjectID:          r.ProjectID,
			GenerationRunID:    r.GenerationRunID,
			GenerationThreadID: r.GenerationThreadID,
			CanvasOperation:    canvasOperation,
			Status:             r.Status.String(),
			Progress:           r.Progress,
			Tool:               r.ToolID,
			Model:              r.Model,
			CreditsUsed:        r.CreditsUsed,
			Recovery:           buildCanvasTaskRecoveryMeta(r.RequestData),
			CreatedAt:          r.CreatedAt,
			UpdatedAt:          r.UpdatedAt,
			StartedAt:          r.StartedAt,
			CompletedAt:        r.CompletedAt,
			ErrorMsg:           r.ErrorMsg,
			ImageURLs:          imageURLs,
			VideoURLs:          videoURLs,
			OutputURLs:         outputURLs,
			ThumbnailURL:       thumbnailURL,
		})
	}

	payload := gin.H{
		"items": items,
		"count": len(items),
		"page":  page,
		"limit": limit,
	}
	if !noCount {
		payload["total"] = total
	}
	response.OkWithData(payload, c)
}
