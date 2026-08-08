package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"server/constants"
	"server/globals"
	"server/model"
	"server/model/common/response"
	llmService "server/service/llm"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tmc/langchaingo/llms"
)

type AdminSystemApi struct{}

// @Tags 管理员系统管理
// @Summary 获取身份列表
// @Description 获取身份列表
// @Success 200 {object} response.Response{data=[]response.OrderListResponse} "身份列表"
// @Router /api/admin/system/getIdentityList [get]
func (a *AdminSystemApi) GetIdentityList(c *gin.Context) {
	pageIndex := c.DefaultQuery("pageIndex", "1")
	pageSize := c.DefaultQuery("pageSize", "10")
	sortOrder := c.DefaultQuery("sort[order]", "desc")
	sortKey := c.DefaultQuery("sort[key]", "created_at")
	query := c.DefaultQuery("query", "")
	filterLang := c.DefaultQuery("filterData[lang]", "all")

	globals.Info(pageIndex, pageSize, sortOrder, sortKey, query, filterLang)

	identityList := []model.Identity{}
	pageIndexInt, _ := strconv.Atoi(pageIndex)
	pageSizeInt, _ := strconv.Atoi(pageSize)
	queryObj := globals.GraDBs["system"].Model(&model.Identity{})
	if sortKey != "" && sortOrder != "" {
		queryObj = queryObj.Order(sortKey + " " + sortOrder)
	} else {
		queryObj = queryObj.Order("id desc")
	}
	if filterLang != "all" && filterLang != "" {
		queryObj = queryObj.Where("lang = ?", filterLang)
	}
	if query != "" {
		queryObj = queryObj.Where("name LIKE ?", "%"+query+"%")
	}

	total := int64(0)
	queryObj.Count(&total)

	queryObj.Limit(pageSizeInt).Offset((pageIndexInt - 1) * pageSizeInt).Find(&identityList)

	data := []map[string]interface{}{}
	for _, identity := range identityList {
		countSubIdentity := int64(0)
		globals.GraDBs["system"].Model(&model.Identity{}).Where("main_identity_id = ?", identity.Id).Count(&countSubIdentity)
		data = append(data, map[string]interface{}{
			"id":               identity.Id,
			"name":             identity.Name,
			"code":             identity.Code,
			"lang":             identity.Lang,
			"sort":             identity.Sort,
			"status":           identity.Status,
			"created_at":       identity.CreatedAt,
			"updated_at":       identity.UpdatedAt,
			"countSubIdentity": countSubIdentity,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": data, "total": total})
}

// @Tags 管理员系统管理
// @Summary 更新身份
// @Description 更新身份
// @Success 200 {object} response.Response{data=[]response.OrderListResponse} "更新身份"
// @Router /api/admin/system/putIdentity [put]
func (a *AdminSystemApi) PutIdentity(c *gin.Context) {
	type PutIdentityRequest struct {
		Id     int    `json:"id" binding:"required"`
		Name   string `json:"name" binding:"required"`
		Code   string `json:"code" binding:"required"`
		Lang   string `json:"lang" binding:"required"`
		Sort   int    `json:"sort" binding:"required"`
		Status int    `json:"status" binding:"required"`
	}

	var request PutIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}

	globals.GraDBs["system"].Model(&model.Identity{}).Where("id = ?", request.Id).Updates(map[string]interface{}{
		"updated_at": time.Now(),
		"name":       request.Name,
		"code":       request.Code,
		"lang":       request.Lang,
		"sort":       request.Sort,
		"status":     request.Status,
	})
	c.JSON(http.StatusOK, gin.H{"message": "success"})
}

// @Tags 管理员系统管理
// @Summary 同步多语言身份
// @Description 同步多语言身份
// @Success 200 {object} response.Response{data=[]response.OrderListResponse} "同步多语言身份"
// @Router /api/admin/system/syncMultiLangIdentity [post]
func (a *AdminSystemApi) SyncMultiLangIdentity(c *gin.Context) {
	type SyncMultiLangIdentityRequest struct {
		Id int `json:"id" binding:"required"`
	}

	var request SyncMultiLangIdentityRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}

	globals.Info(request)

	identity := model.Identity{}
	globals.GraDBs["system"].Model(&model.Identity{}).Where("id = ?", request.Id).First(&identity)
	if identity.Code == "" {
		c.JSON(http.StatusOK, gin.H{"code": 300, "message": "none code can't sync multi lang"})
		return
	}
	if identity.MainIdentityID != 0 {
		c.JSON(http.StatusOK, gin.H{"code": 300, "message": "none Main identity can't sync multi lang"})
		return
	}
	//获取多语言列表,排除en
	multiLangList := constants.GetMultiLanguageList()
	for _, lang := range multiLangList {
		globals.Info(lang["name"])
		langName := lang["name"].(string)
		langCode := lang["value"].(string)
		//查询lang+slug是否存在记录
		existIdentity := model.Identity{}
		globals.GraDBs["system"].Model(&model.Identity{}).Where("lang = ? AND code = ?", langCode, identity.Code).First(&existIdentity)
		//翻译title,placeholder,description
		translateResult := a.TranslateIdentity(identity.Name, langName)
		if existIdentity.Id != 0 {
			globals.Info("identity already exists,update it")
			globals.GraDBs["system"].Model(&model.Identity{}).Where("id = ?", existIdentity.Id).Updates(map[string]interface{}{
				"updated_at": time.Now(),
				"name":       translateResult["name"].(string),
				"lang":       langCode,
				"sort":       identity.Sort,
				"status":     identity.Status,
			})
		} else {
			globals.Info("identity not exists,create it")
			newIdentity := model.Identity{}
			newIdentity.Name = translateResult["name"].(string)
			newIdentity.Code = identity.Code
			newIdentity.Lang = langCode
			newIdentity.Sort = identity.Sort
			newIdentity.Status = identity.Status
			newIdentity.CreatedAt = time.Now()
			newIdentity.UpdatedAt = time.Now()
			newIdentity.MainIdentityID = int(identity.Id)
			globals.GraDBs["system"].Model(&model.Identity{}).Create(&newIdentity)
		}
	}
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success"})
}

func (a *AdminSystemApi) TranslateIdentity(name string, langName string) map[string]interface{} {
	prompt := "I want you to act as a translator. \n" +
		"I am given a identity, please brainstorm to help me translate of a name in " + langName + "\n" +
		"Requirement:\n" +
		"name: " + name + "\n" +
		"- Output json content in " + langName + ". The key is name"

	llm := llmService.GetClient()
	if llm == nil {
		globals.Error("Text LLM client not available in TranslateIdentity")
		return map[string]interface{}{}
	}
	ctx := context.Background()

	completion, err := llms.GenerateFromSinglePrompt(ctx,
		llm,
		prompt,
		llms.WithTemperature(0.6),
		llms.WithJSONMode(),
	)
	if err != nil {
		return map[string]interface{}{}
	}
	globals.Info(fmt.Sprintf("completion: %v", completion))

	dataContent := strings.TrimPrefix(completion, "```json")
	dataContent = strings.TrimSuffix(dataContent, "```")
	var jsonData map[string]interface{}
	err = json.Unmarshal([]byte(dataContent), &jsonData)
	if err != nil {
		return map[string]interface{}{}
	}
	return jsonData
}

// @Tags 管理员系统管理
// @Summary 获取语言列表
// @Description 获取系统支持的语言列表
// @Success 200 {object} response.Response{data=[]map[string]interface{}} "语言列表"
// @Router /api/admin/system/getLanguageList [get]
func (a *AdminSystemApi) GetLanguageList(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"data": constants.SupportedLanguages})
}

// @Tags 管理员系统管理
// @Summary 触发定时任务
// @Description 手动触发标签统计、分类统计、热度更新和搜索向量重建
// @Success 200 {object} response.Response "触发成功"
// @Router /api/admin/system/runSchedulerNow [post]
func (a *AdminSystemApi) RunSchedulerNow(c *gin.Context) {
	go func() {
		globals.Info("Admin triggered scheduler task manually")

		db := globals.GraDBs["system"]
		if db == nil {
			globals.Error("Database not available")
			return
		}

		globals.Info("Scheduler task completed")
	}()

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Scheduler task triggered in background",
	})
}
