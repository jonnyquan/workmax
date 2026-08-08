package use_case

import (
	"server/model"
	"server/model/common/request"
	"server/model/common/response"
	"server/service"

	"github.com/gin-gonic/gin"
)

type UseCaseApi struct{}

var useCaseService = service.GroupServiceApp.MarketingServiceGroup.UseCaseService

func (u *UseCaseApi) CreateUseCase(c *gin.Context) {
	var useCase model.UseCase
	err := c.ShouldBindJSON(&useCase)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	err = useCaseService.CreateUseCase(useCase)
	if err != nil {
		response.FailWithMessage("Create failed", c)
		return
	}
	response.OkWithMessage("Create successfully", c)
}

func (u *UseCaseApi) DeleteUseCase(c *gin.Context) {
	var useCase model.UseCase
	err := c.ShouldBindJSON(&useCase)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	err = useCaseService.DeleteUseCase(useCase)
	if err != nil {
		response.FailWithMessage("Delete failed", c)
		return
	}
	response.OkWithMessage("Delete successfully", c)
}

func (u *UseCaseApi) DeleteUseCaseByIds(c *gin.Context) {
	var ids request.IdsReq
	err := c.ShouldBindJSON(&ids)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	err = useCaseService.DeleteUseCaseByIds(ids.Ids)
	if err != nil {
		response.FailWithMessage("Delete failed", c)
		return
	}
	response.OkWithMessage("Delete successfully", c)
}

func (u *UseCaseApi) UpdateUseCase(c *gin.Context) {
	var useCase model.UseCase
	err := c.ShouldBindJSON(&useCase)
	if err != nil {
		response.FailWithMessage(err.Error(), c)
		return
	}
	err = useCaseService.UpdateUseCase(useCase)
	if err != nil {
		response.FailWithMessage("Update failed", c)
		return
	}
	response.OkWithMessage("Update successfully", c)
}

func (u *UseCaseApi) GetUseCase(c *gin.Context) {
	var idInfo request.GetById
	_ = c.ShouldBindQuery(&idInfo)
	if idInfo.Id == 0 {
		_ = c.ShouldBindJSON(&idInfo)
	}

	useCase, err := useCaseService.GetUseCase(idInfo.Id)
	if err != nil {
		response.FailWithMessage("Get failed", c)
		return
	}
	response.OkWithData(useCase, c)
}

func (u *UseCaseApi) GetUseCaseList(c *gin.Context) {
	var pageInfo request.PageInfo
	_ = c.ShouldBindJSON(&pageInfo)
	if pageInfo.Page == 0 {
		_ = c.ShouldBindQuery(&pageInfo)
	}
	if pageInfo.Page == 0 {
		pageInfo.Page = 1
	}
	if pageInfo.PageSize == 0 {
		pageInfo.PageSize = 10
	}

	list, total, err := useCaseService.GetUseCaseList(pageInfo, "", pageInfo.Keyword, "")
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
