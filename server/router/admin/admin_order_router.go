package admin

import (
	api "server/api"

	"github.com/gin-gonic/gin"
)

type AdminOrderRouter struct {
}

func (s *AdminOrderRouter) InitAdminOrderRouter(router *gin.RouterGroup) {
	adminOrderRouter := router.Group("api")
	adminOrderApi := api.ApiGroupApp.AdminApiGroup.AdminOrderApi
	{
		adminOrderRouter.GET("/admin/order/getOrderList", adminOrderApi.GetOrderList)
		adminOrderRouter.GET("/admin/order/getOrderDetails", adminOrderApi.GetOrderDetails)
		// Paid Orders are durable financial/idempotency owners for webhook replay,
		// subscription-cycle plan/anchor resolution and Credits Pack grants. The
		// retired Admin client must not expose mutation routes that can reverse the
		// Order->User->Pack lock order or physically delete that evidence. A future
		// operator workflow must use an append-only audited outcome/reconciliation
		// ledger rather than re-registering these legacy handlers.
	}
}
