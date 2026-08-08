package account

import (
	"fmt"
	"time"

	api "server/api"
	"server/middleware"
	"server/utils"

	"github.com/gin-gonic/gin"
)

type AccountRouter struct {
}

func (s *AccountRouter) InitAccountRouter(router *gin.RouterGroup) {
	accountApi := api.ApiGroupApp.AccountApiGroup.AccountApi
	stripeApi := api.ApiGroupApp.AccountApiGroup.StripeApi
	apiKeyApi := api.ApiGroupApp.AccountApiGroup.ApiKeyApi
	feedbackApi := api.ApiGroupApp.AccountApiGroup.FeedbackApi
	checkinApi := api.ApiGroupApp.AccountApiGroup.CheckinApi

	// Per-IP rate limit for the email-verification endpoints. Pairs with
	// the per-email cooldown / attempt cap inside the handlers — defense
	// in depth: this throttles a single IP fanning out across many
	// accounts; the per-email checks throttle attacks against one account.
	verifyRateLimit := middleware.RateLimit(10, time.Minute)

	// Per-user rate limit for POST /api/account/checkin. The DB-level
	// unique index on (uid, checkin_date) is the *correctness* defense
	// against double-grants; this is a *cost* defense against an
	// authenticated client spamming the endpoint. 5 attempts/hour is
	// generous for a user who genuinely misclicks but stops a runaway
	// loop from hammering the DB.
	checkinRateLimit := middleware.RateLimitByKey(func(c *gin.Context) string {
		uid := utils.GetUserID(c)
		if uid == 0 {
			return ""
		}
		return fmt.Sprintf("checkin:%d", uid)
	}, 5, time.Hour)

	Router := router.Group("api/account")
	{
		// Account API endpoints
		Router.GET("/quota", accountApi.GetUserQuota)

		// Stripe API routes remain active
		Router.GET("/subscription/stripe/subject", stripeApi.GetStripeSubjectData)
		Router.GET("/subscription/stripe/plans", stripeApi.GetStripeConfigPlans)
		Router.POST("/subscription/stripe/register", stripeApi.RegisterFreeSubscription)
		Router.POST("/subscription/stripe/session/checkout", stripeApi.CreateCheckoutSession)
		Router.GET("/subscription/stripe/session/status", stripeApi.RetrieveCheckoutSession)
		Router.POST("/subscription/stripe/cancel", stripeApi.CancelSubscription)
		Router.POST("/subscription/stripe/reactivate", stripeApi.ReactivateSubscription)
		Router.GET("/subscription/stripe/invoice-url", stripeApi.GetInvoiceUrl)

		// API Key routes
		Router.GET("/api-key", apiKeyApi.GetApiKey)
		Router.POST("/api-key", apiKeyApi.CreateApiKey)
		Router.DELETE("/api-key", apiKeyApi.RevokeApiKey)
		Router.POST("/api-key/verify", apiKeyApi.VerifyApiKey)

		// Feedback routes
		Router.POST("/feedback/submit", feedbackApi.SubmitFeedback)
		Router.GET("/feedback/list", feedbackApi.GetFeedbackList)

		// Checkin routes
		Router.POST("/checkin", checkinRateLimit, checkinApi.DoCheckin)
		Router.GET("/checkin/status", checkinApi.GetCheckinStatus)
		Router.GET("/checkin/history", checkinApi.GetCheckinHistory)

		// Invite routes
		Router.GET("/invite/stats", accountApi.GetInviteStats)

		// Usage records
		Router.GET("/usage/records", accountApi.GetUsageRecords)
		Router.GET("/usage/summary", accountApi.GetUsageSummary)

		// Generation records (My Gallery)
		Router.GET("/generation/records", accountApi.GetGenerationRecords)

		// Rewards records
		Router.GET("/rewards/records", accountApi.GetRewardsRecords)

		// Orders (billing history)
		Router.GET("/subscription/billing", accountApi.GetOrderList)

		// Account deletion (self-service, anonymizes the row)
		Router.POST("/delete", accountApi.DeleteAccount)

		// Email verification (consume code + resend, both rate-limited)
		Router.POST("/verify-email", verifyRateLimit, accountApi.VerifyEmail)
		Router.POST("/resend-verify-email", verifyRateLimit, accountApi.ResendVerifyEmail)
	}
}

func (s *AccountRouter) InitJwtRouter(Router *gin.RouterGroup) {
	// AccountApi removed - logout functionality discontinued
	// accountApi := api.ApiGroupApp.AccountApiGroup.AccountApi
	// sysJwtRouter := Router.Group("api/account")
	// {
	// 	sysJwtRouter.POST("/logout", accountApi.Logout)
	// }
}
