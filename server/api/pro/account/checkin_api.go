package account

import (
	"fmt"
	"net/http"
	"server/service"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type CheckinApi struct{}

// DoCheckin 执行每日签到
// @Summary 每日签到
// @Tags Account
// @Accept json
// @Produce json
// @Success 200 {object} model.CheckinResult
// @Router /api/account/checkin [post]
func (a *CheckinApi) DoCheckin(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    401,
			"message": "Unauthorized",
		})
		return
	}

	checkinService := service.GroupServiceApp.AccountServiceGroup.CheckinService
	result, err := checkinService.DoCheckin(int(uid))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

// GetCheckinStatus 获取签到状态
// @Summary 获取签到状态
// @Tags Account
// @Accept json
// @Produce json
// @Success 200 {object} model.CheckinStatus
// @Router /api/account/checkin/status [get]
func (a *CheckinApi) GetCheckinStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    401,
			"message": "Unauthorized",
		})
		return
	}

	checkinService := service.GroupServiceApp.AccountServiceGroup.CheckinService
	status, err := checkinService.GetCheckinStatus(int(uid))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    status,
	})
}

// GetCheckinHistory 获取签到历史
// @Summary 获取签到历史
// @Tags Account
// @Accept json
// @Produce json
// @Param days query int false "历史天数" default(30)
// @Success 200 {array} model.UserCheckin
// @Router /api/account/checkin/history [get]
func (a *CheckinApi) GetCheckinHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":    401,
			"message": "Unauthorized",
		})
		return
	}

	days := 30
	if d := c.Query("days"); d != "" {
		if _, err := fmt.Sscanf(d, "%d", &days); err != nil || days < 1 || days > 365 {
			days = 30
		}
	}

	checkinService := service.GroupServiceApp.AccountServiceGroup.CheckinService
	history, err := checkinService.GetCheckinHistory(int(uid), days)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"code":    500,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    history,
	})
}
