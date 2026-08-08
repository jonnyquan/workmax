package admin

import (
	"net/http"
	"server/globals"
	"server/model"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type AdminFeedbackApi struct{}

// @Tags 管理员反馈管理
// @Summary 获取用户反馈列表
// @Description 获取用户反馈列表，支持分页、排序和筛选
// @Success 200 {object} response.Response{data=[]model.UserFeedback} "反馈列表"
// @Router /api/admin/feedback/getFeedbackList [get]
func (a *AdminFeedbackApi) GetFeedbackList(c *gin.Context) {
	pageIndex := c.DefaultQuery("pageIndex", "1")
	pageSize := c.DefaultQuery("pageSize", "10")
	sortOrder := c.DefaultQuery("sort[order]", "desc")
	sortKey := c.DefaultQuery("sort[key]", "created_at")
	query := c.DefaultQuery("query", "")
	filterStatus := c.DefaultQuery("filterData[status]", "all")
	filterType := c.DefaultQuery("filterData[type]", "all")

	feedbackList := []model.UserFeedback{}
	pageIndexInt, _ := strconv.Atoi(pageIndex)
	pageSizeInt, _ := strconv.Atoi(pageSize)

	queryObj := globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL")

	// 排序
	if sortKey != "" && sortOrder != "" {
		queryObj = queryObj.Order(sortKey + " " + sortOrder)
	} else {
		queryObj = queryObj.Order("created_at desc")
	}

	// 状态筛选
	if filterStatus != "all" && filterStatus != "" {
		queryObj = queryObj.Where("status = ?", filterStatus)
	}

	// 类型筛选
	if filterType != "all" && filterType != "" {
		queryObj = queryObj.Where("feedback_type = ?", filterType)
	}

	// 搜索
	if query != "" {
		queryObj = queryObj.Where("content LIKE ? OR admin_response LIKE ?", "%"+query+"%", "%"+query+"%")
	}

	total := int64(0)
	queryObj.Count(&total)

	queryObj.Limit(pageSizeInt).Offset((pageIndexInt - 1) * pageSizeInt).Find(&feedbackList)

	// 获取用户信息
	data := []map[string]interface{}{}
	for _, feedback := range feedbackList {
		var user model.User
		globals.GraDBs["system"].Where("id = ?", feedback.UID).First(&user)

		data = append(data, map[string]interface{}{
			"id":            feedback.Id,
			"uid":           feedback.UID,
			"userName":      user.Nickname,
			"userEmail":     user.Email,
			"feedbackType":  feedback.FeedbackType,
			"content":       feedback.Content,
			"rating":        feedback.Rating,
			"status":        feedback.Status,
			"adminResponse": feedback.AdminResponse,
			"responseTime":  feedback.ResponseTime,
			"createdAt":     feedback.CreatedAt,
			"updatedAt":     feedback.UpdatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": data, "total": total})
}

// @Tags 管理员反馈管理
// @Summary 获取反馈统计
// @Description 获取反馈统计信息
// @Success 200 {object} response.Response{data=map[string]interface{}} "反馈统计"
// @Router /api/admin/feedback/getFeedbackStatistic [get]
func (a *AdminFeedbackApi) GetFeedbackStatistic(c *gin.Context) {
	totalFeedback := int64(0)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL").Count(&totalFeedback)

	pendingFeedback := int64(0)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL AND status = ?", model.FEEDBACK_STATUS_PENDING).Count(&pendingFeedback)

	reviewedFeedback := int64(0)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL AND status = ?", model.FEEDBACK_STATUS_REVIEWED).Count(&reviewedFeedback)

	resolvedFeedback := int64(0)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL AND status = ?", model.FEEDBACK_STATUS_RESOLVED).Count(&resolvedFeedback)

	// 过去7天新增反馈
	newFeedback := int64(0)
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL AND created_at >= ?", time.Now().AddDate(0, 0, -7)).Count(&newFeedback)

	// 平均评分
	var avgRating float64
	globals.GraDBs["system"].Model(&model.UserFeedback{}).Where("deleted IS NULL").Select("AVG(rating)").Scan(&avgRating)

	data := map[string]interface{}{
		"totalFeedback": map[string]interface{}{
			"value": totalFeedback,
		},
		"pendingFeedback": map[string]interface{}{
			"value": pendingFeedback,
		},
		"reviewedFeedback": map[string]interface{}{
			"value": reviewedFeedback,
		},
		"resolvedFeedback": map[string]interface{}{
			"value": resolvedFeedback,
		},
		"newFeedback": map[string]interface{}{
			"value": newFeedback,
		},
		"avgRating": map[string]interface{}{
			"value": avgRating,
		},
	}

	c.JSON(http.StatusOK, gin.H{"data": data})
}

// @Tags 管理员反馈管理
// @Summary 更新反馈状态和回复
// @Description 更新反馈状态和管理员回复
// @Success 200 {object} response.Response "更新成功"
// @Router /api/admin/feedback/updateFeedback [post]
func (a *AdminFeedbackApi) UpdateFeedback(c *gin.Context) {
	type UpdateFeedbackRequest struct {
		ID            uint   `json:"id" binding:"required"`
		Status        string `json:"status"`
		AdminResponse string `json:"adminResponse"`
	}

	var req UpdateFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request", "error": err.Error()})
		return
	}

	var feedback model.UserFeedback
	if err := globals.GraDBs["system"].Where("id = ? AND deleted IS NULL", req.ID).First(&feedback).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "Feedback not found"})
		return
	}

	updates := map[string]interface{}{
		"updated_at": time.Now(),
	}

	if req.Status != "" {
		updates["status"] = req.Status
	}

	if req.AdminResponse != "" {
		updates["admin_response"] = req.AdminResponse
		updates["response_time"] = time.Now()
	}

	if err := globals.GraDBs["system"].Model(&feedback).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to update feedback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Feedback updated successfully", "data": feedback})
}

// @Tags 管理员反馈管理
// @Summary 删除反馈
// @Description 软删除反馈
// @Success 200 {object} response.Response "删除成功"
// @Router /api/admin/feedback/deleteFeedback [post]
func (a *AdminFeedbackApi) DeleteFeedback(c *gin.Context) {
	type DeleteFeedbackRequest struct {
		ID uint `json:"id" binding:"required"`
	}

	var req DeleteFeedbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request", "error": err.Error()})
		return
	}

	var feedback model.UserFeedback
	if err := globals.GraDBs["system"].Where("id = ? AND deleted IS NULL", req.ID).First(&feedback).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"message": "Feedback not found"})
		return
	}

	// 软删除
	if err := globals.GraDBs["system"].Model(&feedback).Update("deleted", time.Now()).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to delete feedback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Feedback deleted successfully"})
}

// @Tags 管理员反馈管理
// @Summary 批量更新反馈状态
// @Description 批量更新多个反馈的状态
// @Success 200 {object} response.Response "更新成功"
// @Router /api/admin/feedback/batchUpdateStatus [post]
func (a *AdminFeedbackApi) BatchUpdateStatus(c *gin.Context) {
	type BatchUpdateRequest struct {
		IDs    []uint `json:"ids" binding:"required"`
		Status string `json:"status" binding:"required"`
	}

	var req BatchUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"message": "Invalid request", "error": err.Error()})
		return
	}

	if err := globals.GraDBs["system"].Model(&model.UserFeedback{}).
		Where("id IN ? AND deleted IS NULL", req.IDs).
		Updates(map[string]interface{}{
			"status":     req.Status,
			"updated_at": time.Now(),
		}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"message": "Failed to update feedback"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Feedback updated successfully"})
}
