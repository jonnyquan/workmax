package account

import (
	"errors"
	"fmt"
	"net/http"
	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/utils"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/checkout/session"
	"github.com/stripe/stripe-go/v80/subscription"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type StripeApi struct{}

type stripeSubscriptionClient interface {
	Get(string, *stripe.SubscriptionParams) (*stripe.Subscription, error)
	Update(string, *stripe.SubscriptionParams) (*stripe.Subscription, error)
}

var (
	createStripeCheckoutSession = session.New
	getStripeCheckoutSession    = session.Get
	newStripeSubscriptionClient = func(privateKey string) stripeSubscriptionClient {
		return subscription.Client{B: stripe.GetBackend(stripe.APIBackend), Key: privateKey}
	}
)

// TODO 获取stripe主体数据
func (a *StripeApi) GetStripeSubjectData(c *gin.Context) {
	stripeConfig := globals.GraConf.Stripe
	c.JSON(200, gin.H{"stripeCorpInfo": map[string]interface{}{
		"publicKey": stripeConfig.PublicKey,
	}})
}

// GetStripeConfigPlans 获取配置文件中的套餐信息
func (a *StripeApi) GetStripeConfigPlans(c *gin.Context) {
	stripeConfig := globals.GraConf.Stripe
	c.JSON(200, gin.H{
		"plans": stripeConfig.Plans,
		"stripeCorpInfo": map[string]interface{}{
			"publicKey": stripeConfig.PublicKey,
		},
	})
}

func generateOrderNumber() string {
	random := strings.ReplaceAll(uuid.New().String(), "-", "")
	return "ORDER-" + random[:26]
}

func addCheckoutCalendarMonthsClamped(anchor time.Time, months int) time.Time {
	year, month, day := anchor.Date()
	hour, minute, second := anchor.Clock()
	targetFirst := time.Date(year, month+time.Month(months), 1, hour, minute, second, anchor.Nanosecond(), anchor.Location())
	lastDay := time.Date(targetFirst.Year(), targetFirst.Month()+1, 0, hour, minute, second, anchor.Nanosecond(), anchor.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(targetFirst.Year(), targetFirst.Month(), day, hour, minute, second, anchor.Nanosecond(), anchor.Location())
}

func hasActivePaidMembership(user model.User, now time.Time) bool {
	return user.Member > model.MEMBER_SUBSCRIPTION_FREE &&
		(user.MemberEndTime.IsZero() || user.MemberEndTime.After(now))
}

func providerSubscriptionMayStillBill(user model.User, now time.Time) bool {
	identity := strings.TrimSpace(user.MemberSubscription)
	if identity == "" || strings.HasPrefix(identity, "terminated_") {
		return false
	}
	return !strings.HasPrefix(identity, "canceled_") || hasActivePaidMembership(user, now)
}

func canonicalStripeSubscriptionID(raw string) (string, bool) {
	id := strings.TrimSpace(raw)
	return id, id == raw && strings.HasPrefix(id, "sub_") && len(id) > len("sub_") && !strings.ContainsAny(id, " \t\r\n")
}

func updateSubscriptionIdentityCAS(db *gorm.DB, uid uint, expected, next string, now time.Time) error {
	if db == nil {
		return fmt.Errorf("system database is unavailable")
	}
	result := db.Model(&model.User{}).
		Where("id = ? AND ban = ? AND member_subscription = ?", uid, false, expected).
		Updates(map[string]interface{}{"member_subscription": next, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("persist subscription identity: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var current model.User
	if err := db.Select("id", "member_subscription").First(&current, "id = ?", uid).Error; err != nil {
		return fmt.Errorf("verify subscription identity: %w", err)
	}
	if current.MemberSubscription == next {
		return nil
	}
	return fmt.Errorf("subscription identity owner changed")
}

func (a *StripeApi) RegisterFreeSubscription(c *gin.Context) {
	type RegisterFreeSubscriptionRequest struct {
		BillingCycle string `json:"billingCycle" binding:"required"`
	}

	var request RegisterFreeSubscriptionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}
	cycle := strings.ToLower(strings.TrimSpace(request.BillingCycle))
	if cycle == "yearly" {
		cycle = "annual"
	}
	if cycle != "monthly" && cycle != "annual" {
		response.FailWithDetailed("Unsupported free-plan billing cycle", "Invalid billing cycle", c)
		return
	}

	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithDetailed("Authenticated user is required", "Invalid subscription owner", c)
		return
	}
	db := globals.GraDBs["system"]
	if db == nil {
		response.FailWithDetailed("System database is unavailable", "Failed to register free plan", c)
		return
	}
	now := time.Now()
	orderID := ""
	err := db.Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", uid).Error; err != nil {
			return fmt.Errorf("lock free-plan user: %w", err)
		}
		// A paid entitlement, including lifetime (which intentionally has no
		// subscription ID), can never be overwritten by the free-plan endpoint.
		if user.Ban || hasActivePaidMembership(user, now) || providerSubscriptionMayStillBill(user, now) {
			return fmt.Errorf("active paid membership exists")
		}

		var existing model.Order
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ? AND status = ? AND product_id = ? AND order_type = ?", uid, model.STATUS_COMPLETE, "free", model.ORDER_TYPE_MEMBER).
			Order("id ASC").First(&existing).Error
		if err == nil {
			// Free registration is a once-only grant. Replays return the durable
			// owner without sliding the trial window forward indefinitely.
			orderID = existing.No
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check free-plan owner: %w", err)
		}

		periodMonths := 1
		if cycle == "annual" {
			periodMonths = 12
		}
		end := addCheckoutCalendarMonthsClamped(now, periodMonths)
		order := model.Order{
			UID: int(uid), No: generateOrderNumber(), ProductID: "free",
			Status: model.STATUS_COMPLETE, PayMethod: "platform", Amount: 0,
			Name: "Free Plan", IP: utils.GetClientIP(c.Request),
			OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		}
		order.CreatedAt = now
		order.UpdatedAt = now
		if err := tx.Create(&order).Error; err != nil {
			return fmt.Errorf("create free-plan owner: %w", err)
		}
		if err := tx.Model(&model.User{}).Where("id = ?", uid).Updates(map[string]interface{}{
			"member": model.MEMBER_SUBSCRIPTION_FREE, "member_start_time": now,
			"member_end_time": end, "updated_at": now,
		}).Error; err != nil {
			return fmt.Errorf("apply free-plan entitlement: %w", err)
		}
		orderID = order.No
		return nil
	})
	if err != nil {
		response.FailWithDetailed(err.Error(), "Failed to register free plan", c)
		return
	}
	c.JSON(200, gin.H{"orderId": orderID})
}

type checkoutOrderSpec struct {
	ProductID       string
	Name            string
	ProviderPriceID string
	StripeMode      string
	OrderMode       string
	OrderType       string
	Amount          int
	CreditsAmount   int
	IP              string
}

func validateCheckoutBillingCycle(raw, planKey, planName, stripeMode string) error {
	cycle := strings.ToLower(strings.TrimSpace(raw))
	switch cycle {
	case "yearly":
		cycle = "annual"
	case "once", "payment":
		cycle = "one_time"
	}
	descriptor := strings.ToLower(planKey + " " + planName)
	expected := ""
	switch {
	case strings.Contains(descriptor, "lifetime") || stripeMode == "payment":
		expected = "one_time"
	case strings.Contains(descriptor, "annual") || strings.Contains(descriptor, "yearly"):
		expected = "annual"
	case strings.Contains(descriptor, "monthly"):
		expected = "monthly"
	}
	if expected != "" && cycle != expected {
		return fmt.Errorf("billing cycle %q does not match %s", raw, expected)
	}
	if expected == "" && cycle != "monthly" && cycle != "annual" {
		return fmt.Errorf("unsupported billing cycle %q", raw)
	}
	return nil
}

func normalizeCheckoutOrderMode(stripeMode string) (string, error) {
	switch strings.TrimSpace(stripeMode) {
	case "subscription":
		return model.ORDER_MODE_SUBSCRIPTION, nil
	case "payment":
		return model.ORDER_MODE_ONE_TIME, nil
	default:
		return "", fmt.Errorf("unsupported Stripe checkout mode %q", stripeMode)
	}
}

func validateCheckoutOrderSnapshot(order model.Order, spec checkoutOrderSpec) error {
	if order.ProductID != spec.ProductID || order.Name != spec.Name ||
		order.ProviderPriceID != spec.ProviderPriceID || order.OrderMode != spec.OrderMode ||
		order.OrderType != spec.OrderType || order.Amount != spec.Amount ||
		order.CreditsAmount != spec.CreditsAmount || order.PayMethod != "stripe" {
		return fmt.Errorf("unpaid order %s conflicts with the frozen checkout snapshot", order.No)
	}
	return nil
}

var errCheckoutOwnerAppeared = errors.New("checkout owner appeared while acquiring admission lock")

func unpaidCheckoutOrders(db *gorm.DB, uid uint, spec checkoutOrderSpec) ([]model.Order, error) {
	query := db.Where("uid = ? AND status = ? AND order_type = ?", uid, model.STATUS_UNPAID, spec.OrderType)
	if spec.OrderType == model.ORDER_TYPE_CREDITS {
		query = query.Where("product_id = ?", spec.ProductID)
	}
	var orders []model.Order
	err := query.Order("id ASC").Find(&orders).Error
	return orders, err
}

func validateCheckoutMembershipAdmission(user model.User, spec checkoutOrderSpec, now time.Time) error {
	if user.Ban {
		return fmt.Errorf("account is not eligible for checkout")
	}
	if spec.OrderType != model.ORDER_TYPE_MEMBER {
		return nil
	}
	if providerSubscriptionMayStillBill(user, now) || hasActivePaidMembership(user, now) {
		// Replacing a live provider subscription is a multi-system saga. Canceling
		// it before the replacement payment commits can strand the user, while a
		// retried Desktop command can cancel the newly-purchased subscription. Until
		// a durable switch intent/event ledger owns that saga, fail closed here. A
		// plain/canceling provider identity also blocks when the local entitlement
		// end is stale: local expiry is not proof that Stripe stopped billing.
		return fmt.Errorf("active paid membership requires the subscription switch flow")
	}
	return nil
}

// ensureCheckoutOrder uses the same Order -> User lock order as webhook
// settlement whenever an Order already exists. The absent-owner path locks only
// User, creates the Order, and never waits on an Order row. If an owner appears
// during admission, it releases User and retries through Order -> User.
func ensureCheckoutOrder(db *gorm.DB, uid uint, spec checkoutOrderSpec, now time.Time) (model.Order, error) {
	if db == nil {
		return model.Order{}, fmt.Errorf("system database is unavailable")
	}
	for attempt := 0; attempt < 3; attempt++ {
		unpaid, err := unpaidCheckoutOrders(db, uid, spec)
		if err != nil {
			return model.Order{}, fmt.Errorf("find unpaid checkout owner: %w", err)
		}
		if len(unpaid) > 1 {
			return model.Order{}, fmt.Errorf("multiple unpaid checkout owners require reconciliation")
		}
		if len(unpaid) == 1 {
			var selected model.Order
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&selected, unpaid[0].Id).Error; err != nil {
					return fmt.Errorf("lock unpaid checkout owner: %w", err)
				}
				if selected.UID != int(uid) || selected.Status != model.STATUS_UNPAID || selected.OrderType != spec.OrderType {
					return fmt.Errorf("checkout owner changed")
				}
				if err := validateCheckoutOrderSnapshot(selected, spec); err != nil {
					return err
				}
				var user model.User
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
					Select("id", "ban", "ban_note", "member", "member_end_time", "member_subscription").Where("id = ?", uid).First(&user).Error; err != nil {
					return fmt.Errorf("lock checkout user: %w", err)
				}
				return validateCheckoutMembershipAdmission(user, spec, now)
			})
			return selected, err
		}

		var selected model.Order
		err = db.Transaction(func(tx *gorm.DB) error {
			var user model.User
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Select("id", "ban", "ban_note", "member", "member_end_time", "member_subscription").Where("id = ?", uid).First(&user).Error; err != nil {
				return fmt.Errorf("lock checkout user: %w", err)
			}
			// This is intentionally a non-locking detection read. If another
			// admission committed while User was being acquired, release User and
			// retry with the canonical Order -> User protocol.
			appeared, err := unpaidCheckoutOrders(tx, uid, spec)
			if err != nil {
				return err
			}
			if len(appeared) != 0 {
				return errCheckoutOwnerAppeared
			}
			selected = model.Order{
				UID: int(uid), No: generateOrderNumber(), ProductID: spec.ProductID,
				Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: spec.Amount,
				Name: spec.Name, IP: spec.IP, OrderMode: spec.OrderMode,
				OrderType: spec.OrderType, CreditsAmount: spec.CreditsAmount,
				ProviderPriceID: spec.ProviderPriceID,
			}
			selected.CreatedAt = now
			selected.UpdatedAt = now
			if err := tx.Create(&selected).Error; err != nil {
				return fmt.Errorf("create checkout order: %w", err)
			}
			return validateCheckoutMembershipAdmission(user, spec, now)
		})
		if errors.Is(err, errCheckoutOwnerAppeared) {
			continue
		}
		return selected, err
	}
	return model.Order{}, fmt.Errorf("checkout owner admission did not converge")
}

func persistCheckoutSessionID(db *gorm.DB, orderID uint, providerSessionID string) error {
	providerSessionID = strings.TrimSpace(providerSessionID)
	if providerSessionID == "" {
		return fmt.Errorf("Stripe checkout session id is empty")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var order model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, orderID).Error; err != nil {
			return fmt.Errorf("lock checkout session owner: %w", err)
		}
		if order.Status != model.STATUS_UNPAID {
			return fmt.Errorf("checkout order %s is no longer unpaid", order.No)
		}
		if order.CheckoutSessionID != "" {
			if order.CheckoutSessionID != providerSessionID {
				return fmt.Errorf("checkout order %s already owns another provider session", order.No)
			}
			return nil
		}
		result := tx.Model(&model.Order{}).
			Where("id = ? AND status = ? AND checkout_session_id = ''", order.Id, model.STATUS_UNPAID).
			Update("checkout_session_id", providerSessionID)
		if result.Error != nil {
			return fmt.Errorf("persist checkout session owner: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("checkout session owner changed")
		}
		return nil
	})
}

func expireCheckoutOrder(db *gorm.DB, orderID uint, providerSessionID string, now time.Time) error {
	result := db.Model(&model.Order{}).
		Where("id = ? AND status = ? AND checkout_session_id = ?", orderID, model.STATUS_UNPAID, providerSessionID).
		Updates(map[string]interface{}{"status": model.STATUS_CANCEL, "updated_at": now})
	if result.Error != nil {
		return fmt.Errorf("expire checkout order: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("expired checkout owner changed")
	}
	return nil
}

func (a *StripeApi) CreateCheckoutSession(c *gin.Context) {
	type StripeCheckoutRequest struct {
		BillingCycle string `json:"billingCycle" binding:"required"`
		Mode         string `json:"mode" binding:"required"`
		PlanKey      string `json:"planKey" binding:"required"`
	}

	var request StripeCheckoutRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to retrieve input", c)
		return
	}

	stripeConfig := globals.GraConf.Stripe

	// 先检查积分包配置，再检查会员订阅配置
	var priceId string
	var orderName string
	var orderPrice int
	var orderType string // "member" 或 "credits"
	var stripeMode string
	creditsAmount := 0
	// 首先检查是否为积分包
	if creditPack, exists := stripeConfig.CreditPacks[request.PlanKey]; exists {
		priceId = creditPack.PriceID
		orderName = creditPack.Name
		orderPrice = creditPack.Price
		orderType = model.ORDER_TYPE_CREDITS
		stripeMode = "payment"
		creditsAmount = creditPack.Credits
	} else if plan, exists := stripeConfig.Plans[request.PlanKey]; exists {
		// 会员订阅套餐
		priceId = plan.PriceID
		orderName = plan.Name
		orderPrice = plan.MonthlyPrice
		orderType = model.ORDER_TYPE_MEMBER
		stripeMode = strings.TrimSpace(plan.Mode)
		creditsAmount = plan.MonthlyCredits
		if creditsAmount <= 0 {
			creditsAmount = plan.Credits
		}
		if creditsAmount <= 0 {
			response.FailWithDetailed("Subscription credits are not configured for plan: "+request.PlanKey, "Invalid plan configuration", c)
			return
		}
	} else {
		response.FailWithDetailed("Plan not found: "+request.PlanKey, "Invalid plan key", c)
		return
	}
	if strings.TrimSpace(priceId) == "" || strings.TrimSpace(orderName) == "" || orderPrice <= 0 || creditsAmount <= 0 {
		response.FailWithDetailed("Checkout product snapshot is incomplete", "Invalid plan configuration", c)
		return
	}
	if strings.TrimSpace(request.Mode) != stripeMode {
		response.FailWithDetailed("Checkout mode does not match the configured product", "Invalid checkout mode", c)
		return
	}
	if err := validateCheckoutBillingCycle(request.BillingCycle, request.PlanKey, orderName, stripeMode); err != nil {
		response.FailWithDetailed(err.Error(), "Invalid billing cycle", c)
		return
	}
	orderMode, err := normalizeCheckoutOrderMode(stripeMode)
	if err != nil {
		response.FailWithDetailed(err.Error(), "Invalid plan configuration", c)
		return
	}

	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithDetailed("Authenticated user is required", "Invalid checkout owner", c)
		return
	}
	now := time.Now()
	order, err := ensureCheckoutOrder(globals.GraDBs["system"], uid, checkoutOrderSpec{
		ProductID: request.PlanKey, Name: orderName, ProviderPriceID: priceId,
		StripeMode: stripeMode, OrderMode: orderMode, OrderType: orderType,
		Amount: orderPrice, CreditsAmount: creditsAmount, IP: utils.GetClientIP(c.Request),
	}, now)
	if err != nil {
		response.FailWithDetailed(err.Error(), "Failed to prepare checkout", c)
		return
	}

	stripe.Key = stripeConfig.PrivateKey

	var domain string
	if stripeConfig.Mode == "live" {
		domain = stripeConfig.Domain
	} else {
		domain = stripeConfig.TestDomain
	}
	var checkoutSession *stripe.CheckoutSession
	if order.CheckoutSessionID != "" {
		checkoutSession, err = getStripeCheckoutSession(order.CheckoutSessionID, nil)
	} else {
		params := &stripe.CheckoutSessionParams{
			ClientReferenceID: stripe.String(order.No),
			UIMode:            stripe.String("embedded"),
			ReturnURL:         stripe.String(domain + stripeConfig.ReturnPath + "?session_id={CHECKOUT_SESSION_ID}"),
			// The webhook currently grants entitlement only from a synchronously
			// paid checkout.session.completed event. Restrict the Session to card so
			// delayed methods cannot be acknowledged without a matching async state
			// machine and leave a paid customer without entitlement.
			PaymentMethodTypes: []*string{stripe.String("card")},
			LineItems: []*stripe.CheckoutSessionLineItemParams{
				{
					Price:    stripe.String(priceId),
					Quantity: stripe.Int64(1),
				},
			},

			Metadata: map[string]string{
				"uid": strconv.FormatUint(uint64(uid), 10),
			},
			Mode:         stripe.String(stripeMode),
			AutomaticTax: &stripe.CheckoutSessionAutomaticTaxParams{Enabled: stripe.Bool(true)},
		}
		if orderMode == model.ORDER_MODE_ONE_TIME {
			params.PaymentIntentData = &stripe.CheckoutSessionPaymentIntentDataParams{
				Metadata: map[string]string{
					"order_no": order.No,
					"uid":      strconv.FormatUint(uint64(uid), 10),
				},
			}
		}
		params.SetIdempotencyKey("checkout-session:" + order.No)
		checkoutSession, err = createStripeCheckoutSession(params)
	}
	if err != nil {
		globals.Error(fmt.Sprintf("Stripe Checkout Session failed for order %s: %v", order.No, err))
		response.FailWithDetailed(err.Error(), "Failed to create checkout session", c)
		return
	}
	if checkoutSession == nil || strings.TrimSpace(checkoutSession.ID) == "" {
		response.FailWithDetailed("Stripe returned an incomplete checkout session", "Failed to create checkout session", c)
		return
	}
	if checkoutSession.ClientReferenceID != order.No {
		response.FailWithDetailed("Stripe checkout owner does not match the order", "Checkout session conflict", c)
		return
	}
	if err := persistCheckoutSessionID(globals.GraDBs["system"], order.Id, checkoutSession.ID); err != nil {
		response.FailWithDetailed(err.Error(), "Failed to persist checkout session", c)
		return
	}
	if string(checkoutSession.Status) == "expired" {
		if err := expireCheckoutOrder(globals.GraDBs["system"], order.Id, checkoutSession.ID, time.Now()); err != nil {
			response.FailWithDetailed(err.Error(), "Failed to retire expired checkout", c)
			return
		}
		response.FailWithDetailed("Checkout session expired; retry to create a replacement", "Checkout session expired", c)
		return
	}
	if strings.TrimSpace(checkoutSession.ClientSecret) == "" {
		response.FailWithDetailed("Stripe returned no client secret for an open checkout session", "Failed to create checkout session", c)
		return
	}

	c.JSON(200, struct {
		ClientSecret string `json:"clientSecret"`
		OrderId      string `json:"orderId"`
	}{
		ClientSecret: checkoutSession.ClientSecret,
		OrderId:      order.No,
	})
}

// RetrieveCheckoutSession 获取checkout session
func (a *StripeApi) RetrieveCheckoutSession(c *gin.Context) {
	uid := utils.GetUserID(c)
	sessionID := strings.TrimSpace(c.Query("session_id"))
	if uid == 0 || sessionID == "" {
		response.FailWithDetailed("Checkout owner and session id are required", "Invalid checkout session", c)
		return
	}
	var order model.Order
	if err := globals.GraDBs["system"].
		Select("id", "uid", "no", "checkout_session_id").
		Where("uid = ? AND checkout_session_id = ?", uid, sessionID).
		First(&order).Error; err != nil {
		response.FailWithDetailed("Checkout session not found for the current user", "Checkout session not found", c)
		return
	}
	stripeConfig := globals.GraConf.Stripe
	stripe.Key = stripeConfig.PrivateKey
	s, err := getStripeCheckoutSession(sessionID, nil)
	if err != nil || s == nil || s.ID != sessionID || s.ClientReferenceID != order.No {
		response.FailWithDetailed("Unable to verify checkout session ownership", "Failed to retrieve checkout session", c)
		return
	}
	customerEmail := ""
	if s.CustomerDetails != nil {
		customerEmail = s.CustomerDetails.Email
	}

	c.JSON(200, struct {
		Status        string `json:"status"`
		CustomerEmail string `json:"customer_email"`
	}{
		Status:        string(s.Status),
		CustomerEmail: customerEmail,
	})
}

// cancel subscription
func (a *StripeApi) CancelSubscription(c *gin.Context) {
	uid := utils.GetUserID(c)
	db := globals.GraDBs["system"]
	if uid == 0 || db == nil {
		response.FailWithDetailed("Authenticated subscription owner is required", "Failed to retrieve user data", c)
		return
	}
	var user model.User
	subscriptionID := ""
	alreadyCanceled := false
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", uid).Error; err != nil {
			return err
		}
		if user.Ban {
			return fmt.Errorf("account is not eligible for subscription mutation")
		}
		identity := user.MemberSubscription
		if strings.HasPrefix(identity, "canceled_") {
			alreadyCanceled = true
			return nil
		}
		if strings.HasPrefix(identity, "canceling_") {
			var ok bool
			subscriptionID, ok = canonicalStripeSubscriptionID(strings.TrimPrefix(identity, "canceling_"))
			if !ok {
				return fmt.Errorf("pending cancellation identity is invalid")
			}
			return nil
		}
		var ok bool
		subscriptionID, ok = canonicalStripeSubscriptionID(identity)
		if !ok {
			return fmt.Errorf("no canonical active subscription")
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND ban = ? AND member_subscription = ?", uid, false, identity).
			Update("member_subscription", "canceling_"+subscriptionID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("subscription cancellation owner changed")
		}
		user.MemberSubscription = "canceling_" + subscriptionID
		return nil
	})
	if err != nil {
		response.FailWithDetailed(err.Error(), "Failed to stage subscription cancellation", c)
		return
	}
	if alreadyCanceled {
		response.OkWithMessage("Subscription already canceled", c)
		return
	}
	nickname := user.Nickname
	if nickname == "" {
		parts := strings.Split(user.Email, "@")
		nickname = parts[0]
	}

	// 关闭自动续费：订阅在当前计费周期结束时自动取消，期间用户保留访问权限
	params := &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(true),
	}
	result, err := newStripeSubscriptionClient(globals.GraConf.Stripe.PrivateKey).Update(subscriptionID, params)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Failed to cancel subscription"})
		return
	}
	if result == nil || result.ID != subscriptionID || !result.CancelAtPeriodEnd {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Stripe cancellation outcome could not be verified"})
		return
	}
	globals.Info(fmt.Sprintf("Stripe subscription set to cancel at period end: %v", result.ID))
	cancellationStatus := "canceled_" + subscriptionID
	if err := updateSubscriptionIdentityCAS(db, uid, "canceling_"+subscriptionID, cancellationStatus, time.Now()); err != nil {
		globals.Error(fmt.Sprintf("Failed to persist cancellation for subscription %s: %v", subscriptionID, err))
		// The provider mutation may already have committed. A non-2xx response is
		// required so Desktop retries instead of treating local drift as success.
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Cancellation requires reconciliation"})
		return
	}

	var expiryDate string
	if result.CurrentPeriodEnd > 0 {
		expiryDate = time.Unix(result.CurrentPeriodEnd, 0).Format("2006-01-02")
	}

	err = utils.SendSubscriptionCancellationEmail(
		user.Email,
		nickname,
		expiryDate,
	)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to send cancellation email: %v", err))
	} else {
		globals.Info(fmt.Sprintf("Cancellation email sent to: %s", user.Email))
	}

	response.OkWithMessage("Subscription canceled successfully", c)
}

// reactivate subscription
func (a *StripeApi) ReactivateSubscription(c *gin.Context) {
	uid := utils.GetUserID(c)
	db := globals.GraDBs["system"]
	if uid == 0 || db == nil {
		response.FailWithDetailed("Authenticated subscription owner is required", "Failed to retrieve user data", c)
		return
	}
	var user model.User
	subscriptionID := ""
	alreadyActive := false
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", uid).Error; err != nil {
			return err
		}
		if user.Ban {
			return fmt.Errorf("account is not eligible for subscription mutation")
		}
		identity := user.MemberSubscription
		if activeID, ok := canonicalStripeSubscriptionID(identity); ok && activeID == identity {
			alreadyActive = true
			return nil
		}
		if strings.HasPrefix(identity, "reactivating_") {
			var ok bool
			subscriptionID, ok = canonicalStripeSubscriptionID(strings.TrimPrefix(identity, "reactivating_"))
			if !ok {
				return fmt.Errorf("pending reactivation identity is invalid")
			}
			return nil
		}
		if !strings.HasPrefix(identity, "canceled_") {
			return fmt.Errorf("no canceled subscription to reactivate")
		}
		var ok bool
		subscriptionID, ok = canonicalStripeSubscriptionID(strings.TrimPrefix(identity, "canceled_"))
		if !ok {
			return fmt.Errorf("canceled subscription identity is invalid")
		}
		result := tx.Model(&model.User{}).
			Where("id = ? AND ban = ? AND member_subscription = ?", uid, false, identity).
			Update("member_subscription", "reactivating_"+subscriptionID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("subscription reactivation owner changed")
		}
		user.MemberSubscription = "reactivating_" + subscriptionID
		return nil
	})
	if err != nil {
		response.FailWithDetailed(err.Error(), "Failed to stage subscription reactivation", c)
		return
	}
	if alreadyActive {
		response.OkWithMessage("Subscription already active", c)
		return
	}
	nickname := user.Nickname
	if nickname == "" {
		parts := strings.Split(user.Email, "@")
		nickname = parts[0]
	}

	params := &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(false),
	}
	result, err := newStripeSubscriptionClient(globals.GraConf.Stripe.PrivateKey).Update(subscriptionID, params)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Failed to reactivate subscription"})
		return
	}
	if result == nil || result.ID != subscriptionID || result.CancelAtPeriodEnd {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Stripe reactivation outcome could not be verified"})
		return
	}
	globals.Info(fmt.Sprintf("Subscription reactivated successfully: %v", result.ID))
	if err := updateSubscriptionIdentityCAS(db, uid, "reactivating_"+subscriptionID, subscriptionID, time.Now()); err != nil {
		globals.Error(fmt.Sprintf("Failed to persist reactivation for subscription %s: %v", subscriptionID, err))
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "error", "message": "Reactivation requires reconciliation"})
		return
	}

	err = utils.SendWelcomeEmail(user.Email, nickname)
	if err != nil {
		globals.Error(fmt.Sprintf("Failed to send reactivation email: %v", err))
	} else {
		globals.Info(fmt.Sprintf("Reactivation email sent to: %s", user.Email))
	}

	response.OkWithMessage("Subscription reactivated successfully", c)
}
