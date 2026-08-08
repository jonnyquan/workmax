package use_case

import (
	"server/model/common/request"
	"server/model/common/response"
	"server/service"
	"strconv"

	"github.com/gin-gonic/gin"
)

type UseCaseApi struct{}

var useCaseService = service.GroupServiceApp.MarketingServiceGroup.UseCaseService

// GetUseCaseList Get list of use cases
// GET /api/use-cases/list?lang=en&app=ppt-maker&keyword=xxx&page=1&pageSize=10
func (u *UseCaseApi) GetUseCaseList(c *gin.Context) {
	var pageInfo request.PageInfo
	_ = c.ShouldBindQuery(&pageInfo)
	if pageInfo.Page == 0 {
		pageInfo.Page = 1
	}
	if pageInfo.PageSize == 0 {
		pageInfo.PageSize = 10
	}

	lang := c.Query("lang")
	keyword := c.Query("keyword")
	appSlug := c.Query("app")

	list, total, err := useCaseService.GetUseCaseList(pageInfo, lang, keyword, appSlug)
	if err != nil {
		response.FailWithMessage("Get list failed", c)
		return
	}
	response.OkWithDetailed(response.PageResult{
		List:     list,
		Total:    total,
		Page:     pageInfo.Page,
		PageSize: pageInfo.PageSize,
	}, "Get list successfully", c)
}

// GetUseCase Get use case by slug
// GET /api/use-cases/:slug?lang=en
func (u *UseCaseApi) GetUseCase(c *gin.Context) {
	slug := c.Param("slug")
	lang := c.Query("lang")
	if lang == "" {
		lang = "en"
	}
	useCase, err := useCaseService.GetUseCaseBySlug(slug, lang)
	if err != nil {
		response.FailWithMessage("Get use case failed", c)
		return
	}
	response.OkWithData(useCase, c)
}

// GetUseCasesByApp Get use cases for a specific app
// GET /api/use-cases/by-app/:appSlug?lang=en&limit=6
func (u *UseCaseApi) GetUseCasesByApp(c *gin.Context) {
	appSlug := c.Param("appSlug")
	lang := c.Query("lang")
	limitStr := c.DefaultQuery("limit", "6")
	limit, _ := strconv.Atoi(limitStr)
	if limit <= 0 {
		limit = 6
	}

	useCases, err := useCaseService.GetUseCasesByAppSlug(appSlug, lang, limit)
	if err != nil {
		response.FailWithMessage("Get use cases failed", c)
		return
	}
	response.OkWithData(useCases, c)
}
