package account

import (
	"server/globals"
	"server/model/common/response"
	"server/service"
	"server/utils"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type ApiKeyApi struct{}

// GetApiKey 获取当前用户的 API Key
// @Tags API密钥
// @Summary 获取当前用户的 API Key
// @Produce application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response{data=model.User,msg=string} "获取成功"
// @Router /account/api-key [get]
func (a *ApiKeyApi) GetApiKey(c *gin.Context) {
	uid := utils.GetUserID(c)

	apiKeyInfo, err := service.GroupServiceApp.AccountServiceGroup.ApiKeyService.GetApiKey(uid)
	if err != nil {
		globals.GraLog.Error("获取 API Key 失败", zap.Error(err))
		response.FailWithMessage("获取 API Key 失败", c)
		return
	}

	response.OkWithData(apiKeyInfo, c)
}

// CreateApiKey 创建或重置 API Key
// @Tags API密钥
// @Summary 创建或重置 API Key
// @Produce application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response{data=map[string]interface{},msg=string} "创建成功"
// @Router /account/api-key [post]
func (a *ApiKeyApi) CreateApiKey(c *gin.Context) {
	uid := utils.GetUserID(c)

	// 生成新的 API Key
	apiKey, err := service.GroupServiceApp.AccountServiceGroup.ApiKeyService.CreateApiKey(uid)
	if err != nil {
		globals.GraLog.Error("生成 API Key 失败", zap.Error(err))
		response.FailWithMessage("生成 API Key 失败", c)
		return
	}

	// 返回完整的 API Key 信息
	apiKeyInfo := map[string]interface{}{
		"apiKey": map[string]interface{}{
			"id":        uid,
			"key":       apiKey,
			"apiKey":    apiKey,
			"createdAt": time.Now().Format(time.RFC3339),
			"status":    "active",
		},
	}

	response.OkWithData(apiKeyInfo, c)
}

// RevokeApiKey 撤销 API Key
// @Tags API密钥
// @Summary 撤销 API Key
// @Produce application/json
// @Security ApiKeyAuth
// @Success 200 {object} response.Response{msg=string} "撤销成功"
// @Router /account/api-key [delete]
func (a *ApiKeyApi) RevokeApiKey(c *gin.Context) {
	uid := utils.GetUserID(c)

	// 撤销 API Key
	err := service.GroupServiceApp.AccountServiceGroup.ApiKeyService.RevokeApiKey(uid)
	if err != nil {
		globals.GraLog.Error("撤销 API Key 失败", zap.Error(err))
		response.FailWithMessage("撤销 API Key 失败", c)
		return
	}

	response.OkWithMessage("撤销 API Key 成功", c)
}

// VerifyApiKey 验证 API Key 有效性
// @Tags API密钥
// @Summary 验证 API Key 有效性
// @Produce application/json
// @Param key body string true "API Key"
// @Success 200 {object} response.Response{data=map[string]interface{},msg=string} "验证成功"
// @Router /account/api-key/verify [post]
func (a *ApiKeyApi) VerifyApiKey(c *gin.Context) {
	var req struct {
		Key string `json:"key" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.FailWithMessage("无效的请求参数", c)
		return
	}

	// 验证 API Key
	valid, _ := service.GroupServiceApp.AccountServiceGroup.ApiKeyService.VerifyApiKey(req.Key)

	response.OkWithData(map[string]interface{}{"valid": valid}, c)
}
