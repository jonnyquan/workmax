package admin

import (
	"net/http"
	"server/globals"
	"server/model"
	"server/service"
	accountsvc "server/service/account"
	"server/utils"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AdminOrderApi struct{}

// @Tags 管理员订单管理
// @Summary 获取订单列表
// @Description 获取订单列表
// @Success 200 {object} response.Response{data=[]response.OrderListResponse} "订单列表"
// @Router /api/admin/order/getOrderList [get]
func (a *AdminOrderApi) GetOrderList(c *gin.Context) {
	type FilterData struct {
		Status string `json:"status" form:"status"`
	}

	type OrderListRequest struct {
		PageIndex  int        `json:"pageIndex" form:"pageIndex"`
		PageSize   int        `json:"pageSize" form:"pageSize"`
		SortOrder  string     `json:"sort[order]" form:"sort[order]"`
		SortKey    string     `json:"sort[key]" form:"sort[key]"`
		Query      string     `json:"query" form:"query"`
		FilterData FilterData `json:"filterData" form:"filterData"`
	}

	var request OrderListRequest
	if err := c.ShouldBindQuery(&request); err != nil {
		globals.Warn("Failed to bind query parameters:", err)
		// 如果绑定失败，使用默认值
		request.PageIndex = 1
		request.PageSize = 10
	}

	// 设置默认值
	if request.PageIndex == 0 {
		request.PageIndex = 1
	}
	if request.PageSize == 0 {
		request.PageSize = 10
	}
	if request.SortOrder == "" {
		request.SortOrder = "desc"
	}
	if request.SortKey == "" {
		request.SortKey = "created_at"
	}

	globals.Info("OrderList request:", request)

	orderList := []model.Order{}
	queryObj := globals.GraDBs["system"].Model(&model.Order{})

	// 处理排序
	if request.SortKey != "" && request.SortOrder != "" {
		queryObj = queryObj.Order(request.SortKey + " " + request.SortOrder)
	} else {
		queryObj = queryObj.Order("created_at desc")
	}

	// 处理状态过滤
	filterStatus := request.FilterData.Status
	if filterStatus != "" && filterStatus != "all" {
		queryObj = queryObj.Where("status = ?", filterStatus)
	}

	// 处理搜索查询 - 支持多字段搜索
	if request.Query != "" {
		searchTerm := "%" + request.Query + "%"
		queryObj = queryObj.Where(
			"name LIKE ? OR no LIKE ? OR id LIKE ?",
			searchTerm, searchTerm, searchTerm,
		)
	}

	// 获取总数
	total := int64(0)
	queryObj.Count(&total)

	// 分页查询
	queryObj.Limit(request.PageSize).Offset((request.PageIndex - 1) * request.PageSize).Find(&orderList)

	// 构建返回数据
	data := []map[string]interface{}{}
	for _, order := range orderList {
		orderUser, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uint(order.UID))
		if err != nil {
			globals.Warn("Failed to get user info for order:", order.Id, err)
			// 即使获取用户信息失败，也继续处理订单
			data = append(data, map[string]interface{}{
				"id":         order.Id,
				"name":       order.Name,
				"amount":     order.Amount,
				"status":     order.Status,
				"no":         order.No,
				"user":       "Unknown",
				"email":      "",
				"avatar":     "",
				"payTime":    order.PayTime.Unix(),
				"created_at": order.CreatedAt.Unix(),
				"address":    "",
				"mode":       order.OrderMode,
			})
			continue
		}

		address := utils.GetClientAddress(order.IP)
		address = strings.Split(address, "|")[0]
		data = append(data, map[string]interface{}{
			"id":         order.Id,
			"name":       order.Name,
			"amount":     order.Amount,
			"status":     order.Status,
			"no":         order.No,
			"user":       orderUser.Nickname,
			"email":      orderUser.Email,
			"avatar":     orderUser.Avatar,
			"payTime":    order.PayTime.Unix(),
			"created_at": order.CreatedAt.Unix(),
			"address":    address,
			"mode":       order.OrderMode,
		})
	}

	c.JSON(http.StatusOK, gin.H{"data": data, "total": total})
}

// @Tags 管理员订单管理
// @Summary 删除订单
// @Description 删除订单
// @Success 200 {object} response.Response{data=[]response.OrderListResponse} "订单列表"
// @Router /api/admin/order/deleteOrderList [delete]
func (a *AdminOrderApi) DeleteOrderList(c *gin.Context) {
	ids := c.QueryArray("id[]")
	globals.Info(ids)
	globals.GraDBs["system"].Model(&model.Order{}).Where("id IN (?)", ids).Delete(&model.Order{})
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// @Tags 管理员订单管理
// @Summary 获取订单详情
// @Description 获取单个订单的详细信息
// @Param id query string true "订单ID"
// @Success 200 {object} response.Response{data=response.OrderDetailResponse} "订单详情"
// @Router /api/admin/order/getOrderDetails [get]
func (a *AdminOrderApi) GetOrderDetails(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	order := model.Order{}
	result := globals.GraDBs["system"].Where("id = ?", id).First(&order)
	if result.Error != nil {
		globals.Error("Failed to get order details:", result.Error)
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	// 获取用户信息
	orderUser, err := service.GroupServiceApp.AccountServiceGroup.AccountService.GetUserInfo(uint(order.UID))
	var nickname, email, avatar string
	if err != nil {
		globals.Warn("Failed to get user info for order:", order.Id, err)
		nickname = "Unknown"
		email = ""
		avatar = ""
	} else {
		nickname = orderUser.Nickname
		email = orderUser.Email
		avatar = orderUser.Avatar
	}

	// 获取IP地址信息
	address := utils.GetClientAddress(order.IP)
	address = strings.Split(address, "|")[0]

	// 构建返回数据
	data := map[string]interface{}{
		"id":         order.Id,
		"name":       order.Name,
		"amount":     order.Amount,
		"status":     order.Status,
		"no":         order.No,
		"user":       nickname,
		"email":      email,
		"avatar":     avatar,
		"payTime":    order.PayTime.Unix(),
		"created_at": order.CreatedAt.Unix(),
		"address":    address,
		"mode":       order.OrderMode,
		"payMethod":  order.PayMethod,
	}

	c.JSON(http.StatusOK, gin.H{"data": data})
}

// @Tags 管理员订单管理
// @Summary 退款订单
// @Description 将订单状态改为退款状态,并扣减相应的会员时间
// @Param id query string true "订单ID"
// @Success 200 {object} response.Response{data=bool} "是否成功"
// @Router /api/admin/order/refundOrder [post]
func (a *AdminOrderApi) RefundOrder(c *gin.Context) {
	id := c.Query("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Order ID is required"})
		return
	}

	// 查找订单
	order := model.Order{}
	result := globals.GraDBs["system"].Where("id = ?", id).First(&order)
	if result.Error != nil {
		globals.Error("Failed to find order for refund:", result.Error)
		c.JSON(http.StatusNotFound, gin.H{"error": "Order not found"})
		return
	}

	// 检查订单状态，只有已完成的订单才能退款
	if order.Status != model.STATUS_COMPLETE {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only completed orders can be refunded"})
		return
	}

	packService := accountsvc.NewCreditsPackService()

	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		if order.OrderType == model.ORDER_TYPE_MEMBER {
			user := model.User{}
			if err := tx.First(&user, "id = ?", order.UID).Error; err != nil {
				return err
			}

			expireDays := 30
			stripeConfig := globals.GraConf.Stripe
			for key := range stripeConfig.Plans {
				if key == order.ProductID {
					if strings.Contains(key, "lifetime") {
						expireDays = 365 * 50
					} else if strings.Contains(key, "annual") || strings.Contains(key, "yearly") {
						expireDays = 365
					} else {
						expireDays = 30
					}
					break
				}
			}
			if expireDays == 30 {
				orderName := strings.ToLower(order.Name)
				if strings.Contains(orderName, "annual") || strings.Contains(orderName, "yearly") || strings.Contains(orderName, "year") {
					expireDays = 365
				} else if strings.Contains(orderName, "lifetime") {
					expireDays = 365 * 50
				}
			}

			newEndTime := time.Now()
			if !user.MemberEndTime.IsZero() && user.MemberEndTime.After(time.Now()) {
				newEndTime = user.MemberEndTime
				newEndTime = user.MemberEndTime.AddDate(0, 0, -expireDays)
				if newEndTime.Before(time.Now()) {
					newEndTime = time.Now()
					user.Member = model.MEMBER_SUBSCRIPTION_FREE
				}
				if err := tx.Model(&user).Where("id = ?", user.Id).Updates(map[string]interface{}{
					"member_end_time": newEndTime,
					"member":          user.Member,
				}).Error; err != nil {
					return err
				}
			}

			if err := packService.ClipSubscriptionCreditsTx(tx, order.UID, newEndTime); err != nil {
				return err
			}
		}

		if order.OrderType == model.ORDER_TYPE_CREDITS {
			if err := packService.RevokeOrderCreditsTx(tx, order.No); err != nil {
				return err
			}
		}

		return tx.Model(&model.Order{}).Where("id = ?", order.Id).Update("status", model.STATUS_REFUND).Error
	})
	if err != nil {
		globals.Error("Failed to refund order:", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to refund order"})
		return
	}

	globals.Info("Order refunded successfully:", order.Id)
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "Order refunded successfully"})
}
