package marketing

import (
	"server/globals"
	"server/model"
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type EmailAutomationApi struct{}

var emailAutomationService = service.GroupServiceApp.MarketingServiceGroup.EmailAutomationService

// GetAutomationRuleList 获取自动化规则列表
func (a *EmailAutomationApi) GetAutomationRuleList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	statusStr := c.Query("status")
	triggerType := c.Query("triggerType")

	var status *int
	if statusStr != "" {
		s, _ := strconv.Atoi(statusStr)
		status = &s
	}

	rules, total, err := emailAutomationService.GetAutomationRuleList(page, pageSize, status, triggerType)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     rules,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}

// GetAutomationRuleByID 获取自动化规则详情
func (a *EmailAutomationApi) GetAutomationRuleByID(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	rule, err := emailAutomationService.GetAutomationRuleByID(id)
	if err != nil {
		c.JSON(404, gin.H{"code": 404, "message": "Rule not found"})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data":    rule,
	})
}

// CreateAutomationRule 创建自动化规则
func (a *EmailAutomationApi) CreateAutomationRule(c *gin.Context) {
	var rule model.EmailAutomationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailAutomationService.CreateAutomationRule(&rule); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Rule created successfully",
		"data":    rule,
	})
}

// UpdateAutomationRule 更新自动化规则
func (a *EmailAutomationApi) UpdateAutomationRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var rule model.EmailAutomationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	rule.Id = uint(id)
	if err := emailAutomationService.UpdateAutomationRule(&rule); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Rule updated successfully",
	})
}

// DeleteAutomationRule 删除自动化规则
func (a *EmailAutomationApi) DeleteAutomationRule(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailAutomationService.DeleteAutomationRule(id); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Rule deleted successfully",
	})
}

// UpdateAutomationRuleStatus 更新自动化规则状态
func (a *EmailAutomationApi) UpdateAutomationRuleStatus(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailAutomationService.UpdateAutomationRuleStatus(id, req.Status); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Status updated successfully",
	})
}

// CheckInactiveUsers 检测并触发不活跃用户召回
// POST /api/admin/email/automation/check-inactive
// Body: {"inactiveDays": 30}
func (a *EmailAutomationApi) CheckInactiveUsers(c *gin.Context) {
	var req struct {
		InactiveDays int `json:"inactiveDays" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "Invalid request: " + err.Error()})
		return
	}

	// 执行检测
	count, err := emailAutomationService.CheckAndTriggerInactiveUsers(req.InactiveDays)
	if err != nil {
		globals.GraLog.Error("Failed to check inactive users",
			zap.Int("inactiveDays", req.InactiveDays),
			zap.Error(err))
		c.JSON(500, gin.H{"code": 500, "message": "Failed to check inactive users: " + err.Error()})
		return
	}

	globals.GraLog.Info("Inactive users check completed",
		zap.Int("inactiveDays", req.InactiveDays),
		zap.Int("emailsSent", count))

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Inactive users check completed",
		"data": gin.H{
			"inactiveDays": req.InactiveDays,
			"emailsSent":   count,
		},
	})
}

// BatchCheckInactiveUsers 批量检测不同级别的不活跃用户
// POST /api/admin/email/automation/batch-check-inactive
// Body: {"inactiveDaysList": [7, 30, 90]}
func (a *EmailAutomationApi) BatchCheckInactiveUsers(c *gin.Context) {
	var req struct {
		InactiveDaysList []int `json:"inactiveDaysList" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": "Invalid request: " + err.Error()})
		return
	}

	// 执行批量检测
	results, err := emailAutomationService.BatchCheckInactiveUsers(req.InactiveDaysList)
	if err != nil {
		globals.GraLog.Error("Failed to batch check inactive users",
			zap.Any("inactiveDaysList", req.InactiveDaysList),
			zap.Error(err))
		c.JSON(500, gin.H{"code": 500, "message": "Failed to batch check inactive users: " + err.Error()})
		return
	}

	totalEmails := 0
	for _, count := range results {
		totalEmails += count
	}

	globals.GraLog.Info("Batch inactive users check completed",
		zap.Any("results", results),
		zap.Int("totalEmailsSent", totalEmails))

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Batch inactive users check completed",
		"data": gin.H{
			"results":         results,
			"totalEmailsSent": totalEmails,
		},
	})
}
