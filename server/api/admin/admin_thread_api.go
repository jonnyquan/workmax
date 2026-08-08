package admin

import (
	"fmt"
	"server/globals"
	"server/model/common/response"
	"server/model/workagent"
	"server/service/llm"
	"strconv"

	"github.com/gin-gonic/gin"
)

type AdminThreadApi struct{}

const defaultTextModelFilter = "__default_text_model__"

// adminMaxPaginationOffset mirrors the user-facing cap
// (workagent.maxPaginationOffset, canvasAgentMaxPaginationOffset). An
// admin requesting pageIndex=99999&pageSize=100 would otherwise issue
// an OFFSET 9.9M scan and stall every concurrent query — admin
// dashboards have no business paging that deep, and any cursor-based
// drill-down belongs in a dedicated endpoint.
const adminMaxPaginationOffset = 100_000

// adminMaxBatchDeleteIDs caps how many ids one batch-delete call can
// touch in a single transaction. `IN (?)` with tens of thousands of
// values blows past mysql's max_allowed_packet on the wire and pushes
// the optimizer into pathological plans even when it fits. 500 lines
// up with the public-share message cap from 9c447862 and is plenty
// for any real admin workflow.
const adminMaxBatchDeleteIDs = 500

// GetChatThreadListRequest 获取线程列表请求
type GetChatThreadListRequest struct {
	PageIndex int    `form:"pageIndex" binding:"required,min=1"`
	PageSize  int    `form:"pageSize" binding:"required,min=1,max=100"`
	UID       uint   `form:"uid"`
	Model     string `form:"model"`
	Query     string `form:"query"`
}

// GetChatThreadListResponse 线程列表响应
type GetChatThreadListResponse struct {
	Data  []*workagent.ChatThread `json:"data"`
	Total int64                   `json:"total"`
}

// @Tags Admin Thread
// @Summary Get chat thread list with pagination
// @Param pageIndex query int true "Page number"
// @Param pageSize query int true "Page size"
// @Param uid query int false "User ID"
// @Param model query string false "Model filter"
// @Param query query string false "Search keyword"
// @Success 200 {object} response.Response{data=GetChatThreadListResponse}
// @Router /api/admin/chat-threads [get]
func (a *AdminThreadApi) GetChatThreadList(c *gin.Context) {
	var req GetChatThreadListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	query := db.Model(&workagent.ChatThread{})

	// 筛选条件
	if req.UID > 0 {
		query = query.Where("uid = ?", req.UID)
	}
	if req.Model != "" {
		modelFilter := req.Model
		if modelFilter == defaultTextModelFilter {
			modelFilter = llm.GetModel()
		}
		query = query.Where("model = ?", modelFilter)
	}
	if req.Query != "" {
		query = query.Where("name LIKE ? OR uuid LIKE ?", "%"+req.Query+"%", "%"+req.Query+"%")
	}

	// 总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.FailWithMessage("Failed to count threads: "+err.Error(), c)
		return
	}

	// 分页查询
	offset := (req.PageIndex - 1) * req.PageSize
	if offset > adminMaxPaginationOffset {
		response.OkWithData(GetChatThreadListResponse{
			Data:  []*workagent.ChatThread{},
			Total: total,
		}, c)
		return
	}

	var threads []*workagent.ChatThread
	if err := query.Order("updated_at DESC").
		Offset(offset).
		Limit(req.PageSize).
		Find(&threads).Error; err != nil {
		response.FailWithMessage("Failed to get threads: "+err.Error(), c)
		return
	}

	response.OkWithData(GetChatThreadListResponse{
		Data:  threads,
		Total: total,
	}, c)
}

// @Tags Admin Thread
// @Summary Get thread detail
// @Param id path int true "Thread ID"
// @Success 200 {object} response.Response{data=workagent.ChatThread}
// @Router /api/admin/chat-threads/{id} [get]
func (a *AdminThreadApi) GetChatThreadDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	db := globals.GraDBs["system"]
	var thread workagent.ChatThread
	if err := db.First(&thread, id).Error; err != nil {
		response.FailWithMessage("Thread not found", c)
		return
	}

	response.OkWithData(thread, c)
}

// @Tags Admin Thread
// @Summary Delete thread
// @Param id path int true "Thread ID"
// @Success 200 {object} response.Response
// @Router /api/admin/chat-threads/{id} [delete]
func (a *AdminThreadApi) DeleteChatThread(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid thread ID", c)
		return
	}

	db := globals.GraDBs["system"]

	// 检查线程是否存在
	var thread workagent.ChatThread
	if err := db.First(&thread, id).Error; err != nil {
		response.FailWithMessage("Thread not found", c)
		return
	}

	// 开启事务删除线程及其消息
	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 删除线程的所有消息
	if err := tx.Where("thread_id = ?", id).Delete(&workagent.ChatMessage{}).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete thread messages: "+err.Error(), c)
		return
	}

	// 删除线程的所有文件
	if err := tx.Where("thread_id = ?", id).Delete(&workagent.ThreadFile{}).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete thread files: "+err.Error(), c)
		return
	}

	// 删除线程
	if err := tx.Delete(&thread).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete thread: "+err.Error(), c)
		return
	}

	if err := tx.Commit().Error; err != nil {
		response.FailWithMessage("Failed to commit transaction: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Deleted chat thread: %d (uuid: %s)", thread.Id, thread.UUID))
	response.OkWithMessage("Thread deleted successfully", c)
}

// BatchDeleteRequest 批量删除请求
type BatchDeleteRequest struct {
	IDs []uint64 `json:"ids" binding:"required,min=1"`
}

// @Tags Admin Thread
// @Summary Batch delete threads
// @Param request body BatchDeleteRequest true "Thread IDs"
// @Success 200 {object} response.Response
// @Router /api/admin/chat-threads/batch-delete [post]
func (a *AdminThreadApi) BatchDeleteChatThread(c *gin.Context) {
	var req BatchDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	if len(req.IDs) > adminMaxBatchDeleteIDs {
		response.FailWithMessage(fmt.Sprintf("Too many ids in one batch (max %d)", adminMaxBatchDeleteIDs), c)
		return
	}

	db := globals.GraDBs["system"]

	// 开启事务
	tx := db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 批量删除消息
	if err := tx.Where("thread_id IN ?", req.IDs).Delete(&workagent.ChatMessage{}).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete messages: "+err.Error(), c)
		return
	}

	// 批量删除文件
	if err := tx.Where("thread_id IN ?", req.IDs).Delete(&workagent.ThreadFile{}).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete files: "+err.Error(), c)
		return
	}

	// 批量删除线程
	if err := tx.Where("id IN ?", req.IDs).Delete(&workagent.ChatThread{}).Error; err != nil {
		tx.Rollback()
		response.FailWithMessage("Failed to delete threads: "+err.Error(), c)
		return
	}

	if err := tx.Commit().Error; err != nil {
		response.FailWithMessage("Failed to commit transaction: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Batch deleted %d chat threads", len(req.IDs)))
	response.OkWithMessage(fmt.Sprintf("Successfully deleted %d threads", len(req.IDs)), c)
}
