package marketing

import (
	"server/model"
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type EmailTemplateApi struct{}

var emailTemplateService = service.GroupServiceApp.MarketingServiceGroup.EmailTemplateService

// GetTemplateList 获取模板列表
// @Summary 获取邮件模板列表
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param page query int false "页码"
// @Param pageSize query int false "每页数量"
// @Param category query string false "分类"
// @Param status query int false "状态"
// @Param keyword query string false "关键词"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates [get]
func (a *EmailTemplateApi) GetTemplateList(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	category := c.Query("category")
	statusStr := c.Query("status")
	keyword := c.Query("keyword")

	var status *int
	if statusStr != "" {
		s, _ := strconv.Atoi(statusStr)
		status = &s
	}

	templates, total, err := emailTemplateService.GetTemplateList(page, pageSize, category, status, keyword)
	if err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"list":     templates,
			"total":    total,
			"page":     page,
			"pageSize": pageSize,
		},
	})
}

// GetTemplateByID 获取模板详情
// @Summary 获取邮件模板详情
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param id path int true "模板ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates/:id [get]
func (a *EmailTemplateApi) GetTemplateByID(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	template, err := emailTemplateService.GetTemplateByID(id)
	if err != nil {
		c.JSON(404, gin.H{"code": 404, "message": "Template not found"})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "success",
		"data":    template,
	})
}

// CreateTemplate 创建模板
// @Summary 创建邮件模板
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param template body model.EmailTemplate true "模板信息"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates [post]
func (a *EmailTemplateApi) CreateTemplate(c *gin.Context) {
	var template model.EmailTemplate
	if err := c.ShouldBindJSON(&template); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailTemplateService.CreateTemplate(&template); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Template created successfully",
		"data":    template,
	})
}

// UpdateTemplate 更新模板
// @Summary 更新邮件模板
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param id path int true "模板ID"
// @Param template body model.EmailTemplate true "模板信息"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates/:id [put]
func (a *EmailTemplateApi) UpdateTemplate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var template model.EmailTemplate
	if err := c.ShouldBindJSON(&template); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	template.Id = uint(id)
	if err := emailTemplateService.UpdateTemplate(&template); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Template updated successfully",
	})
}

// DeleteTemplate 删除模板
// @Summary 删除邮件模板
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param id path int true "模板ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates/:id [delete]
func (a *EmailTemplateApi) DeleteTemplate(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	if err := emailTemplateService.DeleteTemplate(id); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Template deleted successfully",
	})
}

// UpdateTemplateStatus 更新模板状态
// @Summary 更新邮件模板状态
// @Tags EmailMarketing
// @Accept json
// @Produce json
// @Param id path int true "模板ID"
// @Param body body map[string]int true "状态"
// @Success 200 {object} map[string]interface{}
// @Router /api/admin/email/templates/:id/status [put]
func (a *EmailTemplateApi) UpdateTemplateStatus(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var req struct {
		Status int `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"code": 400, "message": err.Error()})
		return
	}

	if err := emailTemplateService.UpdateTemplateStatus(id, req.Status); err != nil {
		c.JSON(500, gin.H{"code": 500, "message": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"code":    200,
		"message": "Status updated successfully",
	})
}
