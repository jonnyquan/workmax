package workagent

// projects_api.go — §1 Project/Workspace 模型升级 Stage 1 HTTP layer.
//
// Three endpoints surface the workagent project layer to the FE:
//
//   GET  /api/work-agent/projects
//   POST /api/work-agent/projects
//   PUT  /api/work-agent/chat/conversation/:id/project
//
// All three are JWT-gated (the parent /chat router applies the
// middleware). Service-level ownership checks bake the uid predicate
// into every DB query, so a forgotten check at this layer can't
// accidentally serve another user's row.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/model/common/response"
	workagentModel "server/model/workagent"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ListProjects godoc
// @Summary  Work Agent — list user projects (Personal Workspace pinned first)
// @Description On first call, auto-creates the user's Personal Workspace
// @Description and migrates any unassigned (project_id=0) threads into
// @Description it. Returns the user's full project list with
// @Description {id, title, isPersonal, createdAt, updatedAt}.
// @Tags Tools:WorkAgent
// @Produce json
// @Router /api/work-agent/projects [get]
func (api *AIChatApiNew) ListProjects(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	items, err := workagentService.ListWorkagentProjectsForUser(
		c.Request.Context(),
		globals.GraDBs["system"],
		int(uid),
	)
	if err != nil {
		globals.Error("ListProjects: " + err.Error())
		response.FailWithMessage("Failed to list projects", c)
		return
	}
	response.OkWithData(map[string]interface{}{"items": items}, c)
}

type createProjectRequest struct {
	Title string `json:"title" binding:"required"`
}

// CreateProject godoc
// @Summary  Work Agent — create a new project
// @Description Creates a fresh project owned by the caller. Cannot be
// @Description used to create a second Personal Workspace (the literal
// @Description title is reserved for the system-managed default).
// @Tags Tools:WorkAgent
// @Accept  json
// @Produce json
// @Router /api/work-agent/projects [post]
func (api *AIChatApiNew) CreateProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Title is required", c)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		response.FailWithMessage("Title cannot be empty", c)
		return
	}
	if len(title) > 200 {
		response.FailWithMessage("Title too long", c)
		return
	}
	// Reserve the Personal Workspace title so a user can't accidentally
	// shadow the system default. (Title-based sentinel is the Stage 1
	// detection; Stage 1.5 may add a column for unambiguous marking.)
	if title == workagentService.PersonalWorkspaceTitle {
		response.FailWithMessage("Title is reserved", c)
		return
	}

	project, err := canvasService.CreateProject(
		c.Request.Context(),
		globals.GraDBs["system"],
		int(uid),
		canvasService.CreateProjectInput{Title: title},
	)
	if err != nil {
		globals.Error("CreateProject: " + err.Error())
		response.FailWithMessage("Failed to create project", c)
		return
	}
	response.OkWithData(workagentService.ProjectListItem{
		Id:         project.Id,
		Title:      project.Title,
		IsPersonal: false,
		CreatedAt:  project.CreatedAt,
		UpdatedAt:  project.UpdatedAt,
	}, c)
}

type projectSettingsResponse struct {
	Id           uint      `json:"id"`
	UUID         string    `json:"uuid,omitempty"`
	Title        string    `json:"title"`
	Visibility   int8      `json:"visibility"`
	ThumbnailURL string    `json:"thumbnailUrl"`
	IsPersonal   bool      `json:"isPersonal"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func workagentProjectSettingsToWire(project model.CanvasProject) projectSettingsResponse {
	return projectSettingsResponse{
		Id:           project.Id,
		UUID:         project.UUID,
		Title:        project.Title,
		Visibility:   project.Visibility,
		ThumbnailURL: project.ThumbnailURL,
		IsPersonal:   workagentService.IsPersonalWorkspace(project),
		CreatedAt:    project.CreatedAt,
		UpdatedAt:    project.UpdatedAt,
	}
}

// GetProjectSettings returns the WorkAgent settings DTO for the
// shared project row. The data still lives in Canvas' project table,
// but this route exposes only the fields the WorkAgent settings
// dialog owns.
func (api *AIChatApiNew) GetProjectSettings(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID64, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID64 == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}

	project, err := canvasService.GetProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("GetProjectSettings: " + err.Error())
		response.FailWithMessage("Failed to load project settings", c)
		return
	}
	response.OkWithData(workagentProjectSettingsToWire(project), c)
}

type updateProjectSettingsRequest struct {
	Title        *string `json:"title"`
	Visibility   *int8   `json:"visibility"`
	ThumbnailURL *string `json:"thumbnailUrl"`
}

// UpdateProjectSettings applies user-managed project settings from
// WorkAgent. Personal Workspace is intentionally read-only here:
// users can switch to it, but cannot rename/delete it through this
// surface.
func (api *AIChatApiNew) UpdateProjectSettings(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID64, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID64 == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	var req updateProjectSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request", c)
		return
	}

	existing, err := canvasService.GetProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("UpdateProjectSettings load: " + err.Error())
		response.FailWithMessage("Failed to update project settings", c)
		return
	}
	if workagentService.IsPersonalWorkspace(existing) {
		response.FailWithMessage("Personal Workspace settings are protected", c)
		return
	}

	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if trimmed == "" {
			response.FailWithMessage("Project name is required", c)
			return
		}
		if len(trimmed) > 200 {
			response.FailWithMessage("Project name too long", c)
			return
		}
		if trimmed == workagentService.PersonalWorkspaceTitle {
			response.FailWithMessage("Title is reserved", c)
			return
		}
		req.Title = &trimmed
	}
	if req.Visibility != nil && (*req.Visibility < 0 || *req.Visibility > 2) {
		response.FailWithMessage("Invalid visibility", c)
		return
	}
	if req.ThumbnailURL != nil {
		trimmed := strings.TrimSpace(*req.ThumbnailURL)
		req.ThumbnailURL = &trimmed
	}

	project, err := canvasService.UpdateProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64), canvasService.UpdateProjectInput{
		Title:        req.Title,
		Visibility:   req.Visibility,
		ThumbnailURL: req.ThumbnailURL,
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("UpdateProjectSettings: " + err.Error())
		response.FailWithMessage("Failed to update project settings", c)
		return
	}
	response.OkWithData(workagentProjectSettingsToWire(project), c)
}

type projectBudgetResponse struct {
	Cap       *int `json:"cap"`
	Used      int  `json:"used"`
	Remaining int  `json:"remaining"`
	Exceeded  bool `json:"exceeded"`
}

func workagentProjectBudgetToWire(status *projectService.BudgetStatus) projectBudgetResponse {
	return projectBudgetResponse{
		Cap:       status.Cap,
		Used:      status.Used,
		Remaining: status.Remaining,
		Exceeded:  status.Exceeded,
	}
}

// GetProjectBudget returns the project credit budget through the
// WorkAgent project surface.
func (api *AIChatApiNew) GetProjectBudget(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID64, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID64 == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}

	status, err := projectService.Default().GetBudgetStatusForOwner(uint(projectID64), uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("GetProjectBudget: " + err.Error())
		response.FailWithMessage("Failed to load project budget", c)
		return
	}
	response.OkWithData(workagentProjectBudgetToWire(status), c)
}

type setProjectBudgetRequest struct {
	Cap json.RawMessage `json:"cap"`
}

// SetProjectBudget updates the WorkAgent project's credit cap.
// Passing null clears the cap.
func (api *AIChatApiNew) SetProjectBudget(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID64, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID64 == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	var req setProjectBudgetRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Cap) == 0 {
		response.FailWithMessage("cap field is required", c)
		return
	}

	existing, err := canvasService.GetProject(c.Request.Context(), globals.GraDBs["system"], int(uid), uint(projectID64))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("SetProjectBudget load: " + err.Error())
		response.FailWithMessage("Failed to update project budget", c)
		return
	}
	if workagentService.IsPersonalWorkspace(existing) {
		response.FailWithMessage("Personal Workspace settings are protected", c)
		return
	}

	var capValue *int
	if !bytes.Equal(bytes.TrimSpace(req.Cap), []byte("null")) {
		var n int
		if err := json.Unmarshal(req.Cap, &n); err != nil {
			response.FailWithMessage("cap must be an integer or null", c)
			return
		}
		capValue = &n
	}

	if err := projectService.Default().SetBudgetCapForOwner(uint(projectID64), uid, capValue); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		response.FailWithMessage(err.Error(), c)
		return
	}
	status, err := projectService.Default().GetBudgetStatusForOwner(uint(projectID64), uid)
	if err != nil {
		globals.Error("SetProjectBudget reload: " + err.Error())
		response.FailWithMessage("Failed to reload project budget", c)
		return
	}
	response.OkWithData(workagentProjectBudgetToWire(status), c)
}

type bindThreadProjectRequest struct {
	ProjectID uint `json:"projectId" binding:"required"`
}

// BindThreadProject godoc
// @Summary  Work Agent — bind a thread to a project
// @Description Reassigns an existing thread to the caller-owned project.
// @Description Both the thread and the target project must belong to
// @Description the caller; 404 on either failure.
// @Tags Tools:WorkAgent
// @Accept  json
// @Produce json
// @Router /api/work-agent/chat/conversation/{id}/project [put]
func (api *AIChatApiNew) BindThreadProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	threadIDStr := c.Param("id")
	if threadIDStr == "" {
		response.FailWithMessage("Conversation ID is required", c)
		return
	}
	threadID64, err := strconv.ParseUint(threadIDStr, 10, 32)
	if err != nil || threadID64 == 0 {
		response.FailWithMessage("Invalid conversation ID", c)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentSmallJSONMaxBytes)
	var req bindThreadProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.ProjectID == 0 {
		response.FailWithMessage("projectId is required", c)
		return
	}

	err = workagentService.AssignThreadToProject(
		c.Request.Context(),
		globals.GraDBs["system"],
		int(uid),
		uint(threadID64),
		req.ProjectID,
	)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Conversation or project not found", c)
			return
		}
		globals.Error("BindThreadProject: " + err.Error())
		response.FailWithMessage("Failed to bind thread to project", c)
		return
	}
	response.OkWithData(map[string]interface{}{
		"threadId":  threadID64,
		"projectId": req.ProjectID,
	}, c)
}

// DeleteProject deletes a user-created WorkAgent project. The
// service removes general WorkAgent threads bound to the project,
// then delegates shared Canvas project cleanup.
func (api *AIChatApiNew) DeleteProject(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Unauthorized", c)
		return
	}
	projectID64, err := strconv.ParseUint(c.Param("projectId"), 10, 32)
	if err != nil || projectID64 == 0 {
		response.FailWithMessage("Invalid project ID", c)
		return
	}

	db := globals.GraDBs["system"]
	project, err := canvasService.GetProject(c.Request.Context(), db, int(uid), uint(projectID64))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.FailWithMessage("Project not found", c)
			return
		}
		globals.Error("DeleteProject load: " + err.Error())
		response.FailWithMessage("Failed to delete project", c)
		return
	}
	if workagentService.IsPersonalWorkspace(project) {
		response.FailWithMessage("Personal Workspace cannot be deleted", c)
		return
	}

	var threadIDs []uint
	if err := db.WithContext(c.Request.Context()).
		Model(&workagentModel.ChatThread{}).
		Where("project_id = ?", uint(projectID64)).
		Pluck("id", &threadIDs).Error; err != nil {
		globals.Error("DeleteProject: failed to load project thread ids: " + err.Error())
		response.FailWithMessage("Failed to delete project", c)
		return
	}

	releaseLocks := make([]func(), 0, len(threadIDs))
	for _, threadID := range threadIDs {
		releaseTurnLock, ok := acquireThreadLifecycleLock(c, uint64(threadID))
		if !ok {
			for i := len(releaseLocks) - 1; i >= 0; i-- {
				releaseLocks[i]()
			}
			return
		}
		releaseLocks = append(releaseLocks, releaseTurnLock)
	}
	defer func() {
		for i := len(releaseLocks) - 1; i >= 0; i-- {
			releaseLocks[i]()
		}
	}()

	for _, threadID := range threadIDs {
		threadIDStr := strconv.FormatUint(uint64(threadID), 10)
		for _, conn := range workagentService.GetGlobalSSEManager().GetConnectionsByThread(threadIDStr) {
			conn.Close()
		}
	}

	err = workagentService.DeleteWorkagentProject(
		c.Request.Context(),
		db,
		int(uid),
		uint(projectID64),
	)
	if err != nil {
		switch {
		case errors.Is(err, workagentService.ErrPersonalWorkspaceUndeletable):
			response.FailWithMessage("Personal Workspace cannot be deleted", c)
		case errors.Is(err, gorm.ErrRecordNotFound):
			response.FailWithMessage("Project not found", c)
		default:
			globals.Error("DeleteProject: " + err.Error())
			response.FailWithMessage("Failed to delete project", c)
		}
		return
	}
	response.OkWithData(map[string]interface{}{"deleted": true}, c)
}
