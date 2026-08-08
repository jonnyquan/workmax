package admin

import (
	"fmt"
	"server/globals"
	"server/model/common/response"
	"server/model/workagent"
	"strconv"

	"github.com/gin-gonic/gin"
)

type AdminMessageApi struct{}

// GetChatMessageListRequest 获取消息列表请求
type GetChatMessageListRequest struct {
	PageIndex int    `form:"pageIndex" binding:"required,min=1"`
	PageSize  int    `form:"pageSize" binding:"required,min=1,max=100"`
	ThreadID  uint   `form:"threadId"`
	UID       uint   `form:"uid"`
	Query     string `form:"query"`
}

// GetChatMessageListResponse 消息列表响应
type GetChatMessageListResponse struct {
	Data  []*workagent.ChatMessage `json:"data"`
	Total int64                    `json:"total"`
}

// @Tags Admin Message
// @Summary Get chat message list with pagination
// @Param pageIndex query int true "Page number"
// @Param pageSize query int true "Page size"
// @Param threadId query int false "Thread ID filter"
// @Param uid query int false "User ID filter"
// @Param query query string false "Search keyword"
// @Success 200 {object} response.Response{data=GetChatMessageListResponse}
// @Router /api/admin/chat-messages [get]
func (a *AdminMessageApi) GetChatMessageList(c *gin.Context) {
	var req GetChatMessageListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	db := globals.GraDBs["system"]
	query := db.Model(&workagent.ChatMessage{})

	// 筛选条件
	if req.ThreadID > 0 {
		query = query.Where("thread_id = ?", req.ThreadID)
	}
	if req.UID > 0 {
		query = query.Where("uid = ?", req.UID)
	}
	if req.Query != "" {
		query = query.Where("user_text LIKE ? OR ai_text LIKE ?", "%"+req.Query+"%", "%"+req.Query+"%")
	}

	// 总数
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.FailWithMessage("Failed to count messages: "+err.Error(), c)
		return
	}

	// 分页查询。Cap effective offset to keep deep-page admin requests
	// from issuing OFFSET-N scans that lock w_workagent_message; same
	// rationale and threshold as admin_thread_api.adminMaxPaginationOffset.
	offset := (req.PageIndex - 1) * req.PageSize
	if offset > adminMaxPaginationOffset {
		response.OkWithData(GetChatMessageListResponse{
			Data:  []*workagent.ChatMessage{},
			Total: total,
		}, c)
		return
	}

	var messages []*workagent.ChatMessage
	if err := query.Order("created_at DESC").
		Offset(offset).
		Limit(req.PageSize).
		Find(&messages).Error; err != nil {
		response.FailWithMessage("Failed to get messages: "+err.Error(), c)
		return
	}

	response.OkWithData(GetChatMessageListResponse{
		Data:  messages,
		Total: total,
	}, c)
}

// @Tags Admin Message
// @Summary Get message detail
// @Param id path int true "Message ID"
// @Success 200 {object} response.Response{data=workagent.ChatMessage}
// @Router /api/admin/chat-messages/{id} [get]
func (a *AdminMessageApi) GetChatMessageDetail(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid message ID", c)
		return
	}

	db := globals.GraDBs["system"]
	var message workagent.ChatMessage
	if err := db.First(&message, id).Error; err != nil {
		response.FailWithMessage("Message not found", c)
		return
	}

	response.OkWithData(message, c)
}

// @Tags Admin Message
// @Summary Delete message
// @Param id path int true "Message ID"
// @Success 200 {object} response.Response
// @Router /api/admin/chat-messages/{id} [delete]
func (a *AdminMessageApi) DeleteChatMessage(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		response.FailWithMessage("Invalid message ID", c)
		return
	}

	db := globals.GraDBs["system"]

	// 检查消息是否存在
	var message workagent.ChatMessage
	if err := db.First(&message, id).Error; err != nil {
		response.FailWithMessage("Message not found", c)
		return
	}

	// 删除消息
	if err := db.Delete(&message).Error; err != nil {
		response.FailWithMessage("Failed to delete message: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Deleted chat message: %d", id))
	response.OkWithMessage("Message deleted successfully", c)
}

// BatchDeleteMessageRequest 批量删除消息请求
type BatchDeleteMessageRequest struct {
	IDs []uint64 `json:"ids" binding:"required,min=1"`
}

// @Tags Admin Message
// @Summary Batch delete messages
// @Param request body BatchDeleteMessageRequest true "Message IDs"
// @Success 200 {object} response.Response
// @Router /api/admin/chat-messages/batch-delete [post]
func (a *AdminMessageApi) BatchDeleteChatMessage(c *gin.Context) {
	var req BatchDeleteMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("Invalid request: "+err.Error(), c)
		return
	}

	if len(req.IDs) > adminMaxBatchDeleteIDs {
		response.FailWithMessage(fmt.Sprintf("Too many ids in one batch (max %d)", adminMaxBatchDeleteIDs), c)
		return
	}

	db := globals.GraDBs["system"]

	// 批量删除消息
	if err := db.Where("id IN ?", req.IDs).Delete(&workagent.ChatMessage{}).Error; err != nil {
		response.FailWithMessage("Failed to delete messages: "+err.Error(), c)
		return
	}

	globals.Info(fmt.Sprintf("[AdminAPI] Batch deleted %d chat messages", len(req.IDs)))
	response.OkWithMessage(fmt.Sprintf("Successfully deleted %d messages", len(req.IDs)), c)
}
