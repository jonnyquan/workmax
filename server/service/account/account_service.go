package account

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"server/globals"
	"server/model"
	"server/model/system/request"
	"server/utils"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stripe/stripe-go/v80"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AccountService struct{}

// hashString 创建字符串的简单哈希值
func hashString(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	// Ensure non-negative and fit into signed 32-bit INT for DB column
	v := h.Sum32() & 0x7fffffff
	return int(v)
}

// CountUsersByIDRange counts users within a specific ID range
func (s *AccountService) CountUsersByIDRange(minID, maxID int) (int64, error) {
	var count int64
	err := globals.GraDBs["system"].Model(&model.User{}).Where("id BETWEEN ? AND ?", minID, maxID).Count(&count).Error
	return count, err
}

// UserExistsByID checks if a user exists by ID
func (s *AccountService) UserExistsByID(userID int) (bool, error) {
	var count int64
	err := globals.GraDBs["system"].Model(&model.User{}).Where("id = ?", userID).Count(&count).Error
	return count > 0, err
}

// GetMemberStatsByIDRange gets member statistics for users in ID range
func (s *AccountService) GetMemberStatsByIDRange(minID, maxID int) (map[string]int, error) {
	type MemberCount struct {
		Member int   `json:"member"`
		Count  int64 `json:"count"`
	}

	var results []MemberCount
	err := globals.GraDBs["system"].Model(&model.User{}).
		Select("member, count(*) as count").
		Where("id BETWEEN ? AND ?", minID, maxID).
		Group("member").
		Scan(&results).Error

	if err != nil {
		return nil, err
	}

	stats := make(map[string]int)
	for _, result := range results {
		var memberType string
		switch result.Member {
		case 0:
			memberType = "free"
		case 1:
			memberType = "basic"
		case 2:
			memberType = "pro"
		case 3:
			memberType = "enterprise"
		default:
			memberType = fmt.Sprintf("unknown_%d", result.Member)
		}
		stats[memberType] = int(result.Count)
	}

	return stats, nil
}

// GetUserInfo 获取用户信息
func (s *AccountService) GetUserInfo(uid uint) (userInfo *model.User, err error) {
	var user model.User
	err = globals.GraDBs["system"].Select("*").First(&user, "id = ?", uid).Error
	return &user, err
}

// ChangePassword 修改密码
func (s *AccountService) ChangePassword(ID uint, u request.ChangePassword) (err error) {
	var user model.User
	err = globals.GraDBs["system"].Select("password").First(&user, "id = ?", ID).Error
	if err != nil {
		return err
	}

	newPassword := utils.CalculateMD5Hash(u.Password)
	err = globals.GraDBs["system"].Model(&user).Where("id = ?", ID).Update("password", newPassword).Error
	return err
}

// GetLoginHistory 获取登录历史
func (s *AccountService) GetLoginHistory(uid uint) (loginHistory []model.LoginHis, err error) {
	err = globals.GraDBs["system"].Where("uid = ?", uid).Find(&loginHistory).Error
	return loginHistory, err
}

// GetIdentityById 获取身份
func (s *AccountService) GetIdentityById(id uint) (identity model.Identity, err error) {
	err = globals.GraDBs["system"].First(&identity, "id = ?", id).Error
	return identity, err
}

// UpdateUserInfo 更新用户信息
func (s *AccountService) UpdateUserInfo(uid uint, u model.User) (user model.User, err error) {
	err = globals.GraDBs["system"].Model(&user).Where("id = ?", uid).Updates(u).Error
	return user, err
}

// GetOrderListByUserID 获取订单列表
func (s *AccountService) GetOrderListByUserID(uid uint) (orderList []model.Order, err error) {
	err = globals.GraDBs["system"].Where("uid = ?", uid).Order("id desc").Find(&orderList).Error
	return orderList, err
}

func (s *AccountService) PayOrder(orderNo string, amount_total int64, currency string, customerDetails *stripe.CheckoutSessionCustomerDetails, invoiceId string, transId string, subscriptionId string, chargeId string, orderMode string, checkoutSessionID string, providerPriceID string, billingPeriodStart time.Time, billingPeriodEnd time.Time) (err error) {
	customerSummary := ""
	if customerDetails != nil {
		customerSummary = customerDetails.Name + " " + customerDetails.Email
	}
	order, applied, err := s.applyPaidOrder(
		orderNo, amount_total, customerSummary, invoiceId, transId,
		subscriptionId, chargeId, orderMode, checkoutSessionID, providerPriceID,
		billingPeriodStart, billingPeriodEnd, time.Now(),
	)
	if err != nil {
		globals.Error(fmt.Sprintf("Error applying paid order %s: %v", orderNo, err))
		return err
	}
	if !applied {
		globals.Warn(fmt.Sprintf("Order %s already paid", orderNo))
		return nil
	}

	s.SendPaidOrderConfirmationAsync(order, amount_total, currency)
	return nil
}

// SendPaidOrderConfirmationAsync preserves the current best-effort mail
// behaviour for compatibility callers. Commerce Event processing invokes it
// only after the Inbox + Order/User/Pack + Outbox transaction commits.
func (s *AccountService) SendPaidOrderConfirmationAsync(order model.Order, amountTotal int64, currency string) {
	formattedAmount := formatInvoiceAmount(amountTotal, currency)
	go func(ord model.Order, amountStr string) {
		var user model.User
		if e := globals.GraDBs["system"].First(&user, "id = ?", ord.UID).Error; e != nil {
			globals.Warn(fmt.Sprintf("Could not load user for order %s: %v", ord.No, e))
			return
		}
		// Derive a display name
		displayName := user.Nickname
		if displayName == "" {
			// Fallback: left part of email
			if idx := strings.Index(user.Email, "@"); idx > 0 {
				displayName = user.Email[:idx]
			} else {
				displayName = user.Email
			}
		}
		if mailErr := utils.SendOrderEmail(user.Email, displayName, ord.Name, ord.No, time.Now(), amountStr); mailErr != nil {
			globals.Warn(fmt.Sprintf("Send order email failed for %s: %v", ord.No, mailErr))
		}
	}(order, formattedAmount)
}

// PaidOrderCommand is the transaction-local paid Checkout projection used by
// the Commerce Provider Event Inbox. CustomerSummary is already minimized by
// the provider-specific projector and is never used as an ownership signal.
type PaidOrderCommand struct {
	OrderNo            string
	AmountTotal        int64
	CustomerSummary    string
	InvoiceID          string
	TransactionID      string
	SubscriptionID     string
	ChargeID           string
	OrderMode          string
	CheckoutSessionID  string
	ProviderPriceID    string
	BillingPeriodStart time.Time
	BillingPeriodEnd   time.Time
}

// applyPaidOrder commits the financial owner, membership and Credits Pack as
// one unit. COMPLETE therefore means all durable paid-order side effects have
// committed; a callback retry can safely return an exact replay.
func (s *AccountService) applyPaidOrder(
	orderNo string,
	amountTotal int64,
	customerSummary string,
	invoiceID string,
	transID string,
	subscriptionID string,
	chargeID string,
	orderMode string,
	checkoutSessionID string,
	providerPriceID string,
	billingPeriodStart time.Time,
	billingPeriodEnd time.Time,
	now time.Time,
) (order model.Order, applied bool, err error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return model.Order{}, false, fmt.Errorf("system database is unavailable")
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		order, applied, applyErr = s.ApplyPaidOrderTx(tx, PaidOrderCommand{
			OrderNo: orderNo, AmountTotal: amountTotal, CustomerSummary: customerSummary,
			InvoiceID: invoiceID, TransactionID: transID, SubscriptionID: subscriptionID,
			ChargeID: chargeID, OrderMode: orderMode, CheckoutSessionID: checkoutSessionID,
			ProviderPriceID: providerPriceID, BillingPeriodStart: billingPeriodStart,
			BillingPeriodEnd: billingPeriodEnd,
		}, now)
		return applyErr
	})
	if err != nil {
		applied = false
	}
	return order, applied, err
}

// ApplyPaidOrderTx commits Checkout financial facts and entitlements on a
// caller-owned transaction. The caller must lock its Inbox owner first; this
// method then preserves the stable Order -> User -> Pack lock order.
func (s *AccountService) ApplyPaidOrderTx(
	tx *gorm.DB,
	command PaidOrderCommand,
	now time.Time,
) (order model.Order, applied bool, err error) {
	if tx == nil {
		return model.Order{}, false, fmt.Errorf("paid order transaction is required")
	}
	amount, err := checkedOrderAmount(command.AmountTotal)
	if err != nil {
		return model.Order{}, false, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("no = ?", command.OrderNo).
		First(&order).Error; err != nil {
		return model.Order{}, false, fmt.Errorf("lock paid order: %w", err)
	}
	command.CheckoutSessionID = strings.TrimSpace(command.CheckoutSessionID)
	if command.CheckoutSessionID == "" || order.CheckoutSessionID != command.CheckoutSessionID {
		return model.Order{}, false, fmt.Errorf("paid order %s does not own checkout session %s", order.No, command.CheckoutSessionID)
	}
	if order.Status == model.STATUS_COMPLETE {
		return order, false, validatePaidOrderReplay(
			order, amount, command.InvoiceID, command.TransactionID, command.SubscriptionID,
			command.ChargeID, command.OrderMode, command.ProviderPriceID,
			command.BillingPeriodStart, command.BillingPeriodEnd,
		)
	}
	if order.Status != model.STATUS_UNPAID {
		return model.Order{}, false, fmt.Errorf("paid order %s cannot transition from status %s", order.No, order.Status)
	}
	billingUser, err := lockCreditsOwnerUserTx(tx, order.UID)
	if err != nil {
		return model.Order{}, false, fmt.Errorf("lock paid order user: %w", err)
	}
	if billingUser.Ban {
		return model.Order{}, false, fmt.Errorf("paid order %s owner is not eligible for settlement", order.No)
	}
	if err := validateUnpaidPaidOrder(order, command.OrderMode, command.SubscriptionID); err != nil {
		return model.Order{}, false, err
	}
	if order.OrderType == model.ORDER_TYPE_MEMBER && command.OrderMode == model.ORDER_MODE_SUBSCRIPTION {
		command.ProviderPriceID = strings.TrimSpace(command.ProviderPriceID)
		if command.ProviderPriceID == "" || order.ProviderPriceID != command.ProviderPriceID {
			return model.Order{}, false, fmt.Errorf("member order %s provider price conflicts with checkout snapshot", order.No)
		}
		if command.BillingPeriodStart.IsZero() || command.BillingPeriodEnd.IsZero() ||
			command.BillingPeriodStart.After(now) || !command.BillingPeriodEnd.After(now) ||
			!command.BillingPeriodEnd.After(command.BillingPeriodStart) {
			return model.Order{}, false, fmt.Errorf("member order %s has an invalid provider billing period", order.No)
		}
		order.BillingPeriodStart = timePointer(command.BillingPeriodStart)
		order.BillingPeriodEnd = timePointer(command.BillingPeriodEnd)
	} else if !command.BillingPeriodStart.IsZero() || !command.BillingPeriodEnd.IsZero() || strings.TrimSpace(command.ProviderPriceID) != "" {
		return model.Order{}, false, fmt.Errorf("one-time order %s unexpectedly has recurring billing facts", order.No)
	}
	if order.Amount > 0 && amount < order.Amount {
		return model.Order{}, false, fmt.Errorf("paid order %s amount %d lower than frozen minimum %d", command.OrderNo, command.AmountTotal, order.Amount)
	}
	if order.OrderType == model.ORDER_TYPE_MEMBER && order.CreditsAmount <= 0 {
		order.CreditsAmount = NewCreditsPackService().getPlanCredits(order.ProductID)
		if order.CreditsAmount <= 0 {
			return model.Order{}, false, fmt.Errorf("positive subscription credits are not configured for plan %s", order.ProductID)
		}
	}

	order.Status = model.STATUS_COMPLETE
	order.Amount = amount
	order.UpdatedAt = now
	order.CustomerDetails = command.CustomerSummary
	order.TransID = command.TransactionID
	order.PayTime = now
	order.Invoice = command.InvoiceID
	order.SubscriptionID = command.SubscriptionID
	order.ChargeID = command.ChargeID
	order.OrderMode = command.OrderMode
	if err := tx.Model(&model.Order{}).Where("id = ?", order.Id).Updates(map[string]interface{}{
		"status":               order.Status,
		"amount":               order.Amount,
		"updated_at":           order.UpdatedAt,
		"customer_details":     order.CustomerDetails,
		"trans_id":             order.TransID,
		"pay_time":             order.PayTime,
		"invoice":              order.Invoice,
		"subscription_id":      order.SubscriptionID,
		"charge_id":            order.ChargeID,
		"order_mode":           order.OrderMode,
		"credits_amount":       order.CreditsAmount,
		"billing_period_start": order.BillingPeriodStart,
		"billing_period_end":   order.BillingPeriodEnd,
	}).Error; err != nil {
		return model.Order{}, false, fmt.Errorf("complete paid order: %w", err)
	}

	switch order.OrderType {
	case model.ORDER_TYPE_MEMBER:
		if err := s.updateUserMemberTx(tx, order, now, command.BillingPeriodStart, command.BillingPeriodEnd); err != nil {
			return model.Order{}, false, fmt.Errorf("apply member entitlement: %w", err)
		}
	case model.ORDER_TYPE_CREDITS:
		if _, err := s.grantCreditsTx(tx, order); err != nil {
			return model.Order{}, false, fmt.Errorf("apply purchased credits: %w", err)
		}
	}
	return order, true, nil
}

func validateUnpaidPaidOrder(order model.Order, orderMode, subscriptionID string) error {
	if order.UID <= 0 {
		return fmt.Errorf("paid order %s has no valid owner", order.No)
	}
	if strings.TrimSpace(order.ProductID) == "" {
		return fmt.Errorf("paid order %s has no durable product identity", order.No)
	}
	if order.PayMethod != "" && order.PayMethod != "stripe" {
		return fmt.Errorf("paid order %s is not owned by Stripe", order.No)
	}
	if order.OrderMode != orderMode {
		return fmt.Errorf("paid order %s mode %s conflicts with provider mode %s", order.No, order.OrderMode, orderMode)
	}
	switch order.OrderType {
	case model.ORDER_TYPE_MEMBER:
		if orderMode != model.ORDER_MODE_SUBSCRIPTION && orderMode != model.ORDER_MODE_ONE_TIME {
			return fmt.Errorf("member order %s has invalid paid mode %s", order.No, orderMode)
		}
		if orderMode == model.ORDER_MODE_SUBSCRIPTION && strings.TrimSpace(subscriptionID) == "" {
			return fmt.Errorf("recurring member order %s has no subscription identity", order.No)
		}
		if orderMode == model.ORDER_MODE_ONE_TIME && strings.TrimSpace(subscriptionID) != "" {
			return fmt.Errorf("one-time member order %s unexpectedly has a subscription identity", order.No)
		}
	case model.ORDER_TYPE_CREDITS:
		if orderMode != model.ORDER_MODE_ONE_TIME || strings.TrimSpace(subscriptionID) != "" {
			return fmt.Errorf("credits order %s must be a one-time payment", order.No)
		}
	default:
		return fmt.Errorf("paid order %s has unsupported order type %s", order.No, order.OrderType)
	}
	return nil
}

func validatePaidOrderReplay(
	order model.Order,
	amount int,
	invoiceID string,
	transID string,
	subscriptionID string,
	chargeID string,
	orderMode string,
	providerPriceID string,
	billingPeriodStart time.Time,
	billingPeriodEnd time.Time,
) error {
	if order.Amount != amount || order.Invoice != invoiceID || order.TransID != transID ||
		order.SubscriptionID != subscriptionID || order.ChargeID != chargeID || order.OrderMode != orderMode {
		return fmt.Errorf("paid order %s replay conflicts with the committed payment identity", order.No)
	}
	if orderMode == model.ORDER_MODE_SUBSCRIPTION &&
		(order.ProviderPriceID != strings.TrimSpace(providerPriceID) ||
			!sameOptionalTime(order.BillingPeriodStart, billingPeriodStart) ||
			!sameOptionalTime(order.BillingPeriodEnd, billingPeriodEnd)) {
		return fmt.Errorf("paid order %s replay conflicts with the committed billing period", order.No)
	}
	return nil
}

func checkedOrderAmount(amount int64) (int, error) {
	if amount < 0 {
		return 0, fmt.Errorf("paid amount must not be negative")
	}
	converted := int(amount)
	if int64(converted) != amount {
		return 0, fmt.Errorf("paid amount %d exceeds order storage range", amount)
	}
	return converted, nil
}

// UpdateUserMember is retained only as a fail-closed compatibility symbol.
// Membership cannot be safely replayed outside the transaction that changes
// its paid Order from UNPAID to COMPLETE; doing so can extend the same Order
// more than once. All production callers must use PayOrder or
// SubscriptionUpdate.
func (s *AccountService) UpdateUserMember(order model.Order) error {
	return fmt.Errorf("standalone membership mutation is disabled for order %s", order.No)
}

func (s *AccountService) updateUserMemberTx(
	tx *gorm.DB,
	order model.Order,
	now time.Time,
	providerPeriodStart time.Time,
	providerPeriodEnd time.Time,
) error {
	user, err := lockCreditsOwnerUserTx(tx, order.UID)
	if err != nil {
		return fmt.Errorf("lock member user: %w", err)
	}
	if user.Ban {
		return fmt.Errorf("member user %d is not eligible for settlement", order.UID)
	}
	if order.SubscriptionID != "" &&
		user.MemberSubscription != "" &&
		!subscriptionIdentityAllowsReplacement(user.MemberSubscription) &&
		user.MemberSubscription != order.SubscriptionID &&
		user.MemberEndTime.After(now) {
		return fmt.Errorf("active subscription already exists for user %d", order.UID)
	}

	// Determine the calendar entitlement period. Calendar-month arithmetic is
	// anchored/clamped (Jan 31 -> Feb 28/29 -> Mar 31), so renewal does not drift
	// and annual plans do not accidentally gain a 366th day.
	periodMonths := 1          // Default to monthly
	planKey := order.ProductID // Store for credits grant

	// Find the plan key from the productId (now stored as planKey string)
	stripeConfig := globals.GraConf.Stripe
	for key := range stripeConfig.Plans {
		if key == order.ProductID {
			planKey = key
			if strings.Contains(key, "lifetime") {
				periodMonths = 12 * 50
			} else if strings.Contains(key, "annual") || strings.Contains(key, "yearly") {
				periodMonths = 12
			}
			globals.Info(fmt.Sprintf("Found plan %s for productId %s, using %d calendar months", key, order.ProductID, periodMonths))
			break
		}
	}

	// If no matching plan found, use default based on order name or mode
	if periodMonths == 1 {
		// Fallback: try to detect from order name or other clues
		orderName := strings.ToLower(order.Name)
		if strings.Contains(orderName, "annual") || strings.Contains(orderName, "yearly") || strings.Contains(orderName, "year") {
			periodMonths = 12
		} else if strings.Contains(orderName, "lifetime") {
			periodMonths = 12 * 50
		}
		globals.Info(fmt.Sprintf("Using fallback entitlement period %d months for order %s", periodMonths, order.Name))
	}

	providerPeriod := !providerPeriodStart.IsZero() || !providerPeriodEnd.IsZero()
	if providerPeriod {
		if providerPeriodStart.IsZero() || providerPeriodEnd.IsZero() || !providerPeriodEnd.After(providerPeriodStart) {
			return fmt.Errorf("subscription order %s has an invalid provider billing period", order.No)
		}
		if !providerPeriodEnd.After(now) {
			return fmt.Errorf("subscription order %s provider billing period already ended", order.No)
		}
		if user.Member > model.MEMBER_SUBSCRIPTION_FREE &&
			user.MemberEndTime.After(providerPeriodEnd) &&
			!subscriptionIdentityAllowsReplacement(user.MemberSubscription) {
			return fmt.Errorf("subscription order %s is older than the committed member period", order.No)
		}
		user.Member = model.MEMBER_SUBSCRIPTION_PRO
		user.MemberStartTime = providerPeriodStart
		user.MemberEndTime = providerPeriodEnd
	} else {
		activePaidMember := user.Member > model.MEMBER_SUBSCRIPTION_FREE && user.MemberEndTime.After(now)
		if activePaidMember {
			user.MemberEndTime = addCalendarMonthsClamped(user.MemberEndTime, periodMonths)
		} else {
			// A future FREE trial is not a paid entitlement and must never keep the
			// user at tier 1 after payment. Only an already-paid active membership may
			// carry its remaining end date into a renewal.
			user.Member = model.MEMBER_SUBSCRIPTION_PRO
			user.MemberStartTime = now
			user.MemberEndTime = addCalendarMonthsClamped(now, periodMonths)
		}
	}
	user.MemberSubscription = order.SubscriptionID
	if err := tx.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"member":              user.Member,
		"member_start_time":   user.MemberStartTime,
		"member_end_time":     user.MemberEndTime,
		"member_subscription": user.MemberSubscription,
	}).Error; err != nil {
		return fmt.Errorf("update member user: %w", err)
	}

	if planKey != "" {
		// A subscription_update invoice changes provider membership facts inside
		// an already-funded period. Minting a full Pack here would stack the new
		// plan allowance on top of the still-spendable old Pack. Initial checkout
		// and subscription_cycle Orders use subscription mode and remain the only
		// full-cycle grant owners.
		if order.OrderMode != model.ORDER_MODE_SUBSCRIPTION_UPDATE {
			if err := s.grantSubscriptionCreditsTx(tx, user, order, planKey, now, providerPeriodEnd); err != nil {
				return err
			}
		}
	}
	return nil
}

func subscriptionIdentityAllowsReplacement(identity string) bool {
	return strings.HasPrefix(identity, "canceled_") || strings.HasPrefix(identity, "terminated_")
}

func generateOrderNumber() string {
	// w_order.no is varchar(32). Keep 104 random bits while preserving the
	// familiar prefix; the former UUID representation was 42 bytes and failed
	// under MySQL strict mode.
	random := strings.ReplaceAll(uuid.New().String(), "-", "")
	return "ORDER-" + random[:26]
}

func (s *AccountService) SubscriptionUpdate(customerDetails string, priceId string, invoiceId string, transId string, subscriptionId string, amountPaidCents int64, currency string, billingPeriodStart time.Time, renewalAt time.Time, paymentMethod string, billingReason string) (err error) {
	newOrder, applied, err := s.applySubscriptionUpdate(
		customerDetails, priceId, invoiceId, transId, subscriptionId, amountPaidCents,
		time.Now(), billingPeriodStart, renewalAt, billingReason,
	)
	if err != nil {
		globals.Error(fmt.Sprintf("Error applying subscription invoice %s: %v", invoiceId, err))
		return err
	}
	if !applied {
		globals.Warn(fmt.Sprintf("Subscription invoice %s already applied", invoiceId))
		return nil
	}

	s.SendSubscriptionRenewalConfirmationAsync(newOrder, amountPaidCents, currency, renewalAt, paymentMethod)
	return nil
}

// SendSubscriptionRenewalConfirmationAsync preserves the existing
// best-effort notification after the caller's durable transaction commits.
func (s *AccountService) SendSubscriptionRenewalConfirmationAsync(
	order model.Order,
	amountPaidCents int64,
	currency string,
	renewalAt time.Time,
	paymentMethod string,
) {
	go func(ord model.Order) {
		var user model.User
		if e := globals.GraDBs["system"].First(&user, "id = ?", ord.UID).Error; e != nil {
			globals.Warn(fmt.Sprintf("Could not load user for order %s: %v", ord.No, e))
			return
		}
		displayName := user.Nickname
		if displayName == "" {
			if idx := strings.Index(user.Email, "@"); idx > 0 {
				displayName = user.Email[:idx]
			} else {
				displayName = user.Email
			}
		}
		amountStr := formatInvoiceAmount(amountPaidCents, currency)
		renewalDate := "—"
		if !renewalAt.IsZero() {
			renewalDate = renewalAt.Format("January 2, 2006")
		}
		payMethod := paymentMethod
		if payMethod == "" {
			payMethod = "Card on file"
		}
		if mailErr := utils.SendSubscriptionRenewalEmail(user.Email, displayName, ord.Name, amountStr, renewalDate, payMethod); mailErr != nil {
			globals.Warn(fmt.Sprintf("Send renewal email failed for %s: %v", ord.No, mailErr))
		}
	}(order)
}

// CurrentSubscriptionProviderPrice returns the latest durable provider price
// used to classify a subscription_cycle invoice line before the webhook enters
// the settlement transaction. ApplySubscriptionInvoiceTx locks and rechecks
// the same latest membership snapshot, so this non-locking read is only a
// selector, never settlement authority. A blank result is retained for explicit
// legacy Orders and forces the caller onto unique-config reconciliation.
func (s *AccountService) CurrentSubscriptionProviderPrice(subscriptionID string) (string, error) {
	return s.CurrentSubscriptionProviderPriceContext(context.Background(), subscriptionID)
}

// CurrentSubscriptionProviderPriceContext is the bounded variant used while a
// durable Provider Event lease is held. The compatibility wrapper above keeps
// existing non-Inbox callers unchanged.
func (s *AccountService) CurrentSubscriptionProviderPriceContext(
	ctx context.Context,
	subscriptionID string,
) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("subscription price context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	subscriptionID = strings.TrimSpace(subscriptionID)
	if subscriptionID == "" {
		return "", fmt.Errorf("subscription id is required")
	}
	db := globals.GraDBs["system"]
	if db == nil {
		return "", fmt.Errorf("system database is unavailable")
	}
	var current model.Order
	if err := db.WithContext(ctx).Select("id", "subscription_id", "status", "order_type", "provider_price_id").Where(
		"subscription_id = ? AND order_type = ? AND status = ?",
		subscriptionID, model.ORDER_TYPE_MEMBER, model.STATUS_COMPLETE,
	).Order("pay_time DESC, id DESC").First(&current).Error; err != nil {
		return "", fmt.Errorf("resolve current subscription membership: %w", err)
	}
	if current.SubscriptionID != subscriptionID || current.Status != model.STATUS_COMPLETE ||
		current.OrderType != model.ORDER_TYPE_MEMBER {
		return "", fmt.Errorf("current subscription identity does not match provider input")
	}
	return strings.TrimSpace(current.ProviderPriceID), nil
}

// SubscriptionInvoiceCommand is the provider-neutral, transaction-local
// projection of one paid recurring invoice. Currency and notification fields
// deliberately stay outside this command because they are post-commit effects,
// not settlement authority.
type SubscriptionInvoiceCommand struct {
	CustomerDetails    string
	ProviderPriceID    string
	InvoiceID          string
	TransactionID      string
	SubscriptionID     string
	AmountPaidCents    int64
	BillingPeriodStart time.Time
	BillingPeriodEnd   time.Time
	BillingReason      string
}

// applySubscriptionUpdate is the compatibility transaction owner used by the
// existing callback path. New Inbox processing can lock its event owner first
// and invoke ApplySubscriptionInvoiceTx in the same transaction.
func (s *AccountService) applySubscriptionUpdate(
	customerDetails string,
	priceID string,
	invoiceID string,
	transID string,
	subscriptionID string,
	amountPaidCents int64,
	now time.Time,
	billingPeriodStart time.Time,
	billingPeriodEnd time.Time,
	billingReason string,
) (newOrder model.Order, applied bool, err error) {
	db := globals.GraDBs["system"]
	if db == nil {
		return model.Order{}, false, fmt.Errorf("system database is unavailable")
	}
	command := SubscriptionInvoiceCommand{
		CustomerDetails: customerDetails, ProviderPriceID: priceID,
		InvoiceID: invoiceID, TransactionID: transID, SubscriptionID: subscriptionID,
		AmountPaidCents: amountPaidCents, BillingPeriodStart: billingPeriodStart,
		BillingPeriodEnd: billingPeriodEnd, BillingReason: billingReason,
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		newOrder, applied, applyErr = s.ApplySubscriptionInvoiceTx(tx, command, now)
		return applyErr
	})
	if err != nil {
		applied = false
	}
	return newOrder, applied, err
}

// ApplySubscriptionInvoiceTx serializes every invoice on the oldest completed
// member Order inside a caller-owned transaction. The caller must lock an Inbox
// owner first when one exists; this method preserves Order -> latest Order ->
// User -> Pack locking and performs no notification or transaction commit.
func (s *AccountService) ApplySubscriptionInvoiceTx(
	tx *gorm.DB,
	command SubscriptionInvoiceCommand,
	now time.Time,
) (newOrder model.Order, applied bool, err error) {
	if tx == nil {
		return model.Order{}, false, fmt.Errorf("subscription invoice transaction is required")
	}
	customerDetails := command.CustomerDetails
	priceID := strings.TrimSpace(command.ProviderPriceID)
	invoiceID := strings.TrimSpace(command.InvoiceID)
	transID := command.TransactionID
	subscriptionID := strings.TrimSpace(command.SubscriptionID)
	amountPaidCents := command.AmountPaidCents
	billingPeriodStart := command.BillingPeriodStart
	billingPeriodEnd := command.BillingPeriodEnd
	billingReason := command.BillingReason
	if priceID == "" {
		return model.Order{}, false, fmt.Errorf("subscription price id is required")
	}
	if invoiceID == "" {
		return model.Order{}, false, fmt.Errorf("subscription invoice id is required")
	}
	if subscriptionID == "" {
		return model.Order{}, false, fmt.Errorf("subscription id is required")
	}
	if billingPeriodStart.IsZero() || billingPeriodEnd.IsZero() || !billingPeriodEnd.After(billingPeriodStart) {
		return model.Order{}, false, fmt.Errorf("subscription provider billing period is required")
	}
	invoiceOrderMode, err := subscriptionInvoiceOrderMode(billingReason)
	if err != nil {
		return model.Order{}, false, err
	}
	renewalAmount, err := checkedOrderAmount(amountPaidCents)
	if err != nil {
		return model.Order{}, false, err
	}
	applyErr := func() error {
		var owner model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("subscription_id = ? AND order_type = ? AND status = ?", subscriptionID, model.ORDER_TYPE_MEMBER, model.STATUS_COMPLETE).
			Order("id ASC").
			First(&owner).Error; err != nil {
			return fmt.Errorf("lock subscription owner %s: %w", subscriptionID, err)
		}
		if owner.SubscriptionID != subscriptionID || owner.Status != model.STATUS_COMPLETE ||
			owner.OrderType != model.ORDER_TYPE_MEMBER {
			return fmt.Errorf("locked subscription owner does not match provider identity")
		}

		var existing model.Order
		// Query the schema-owned binary normalized identity, not invoice's legacy
		// collation. Replay is validated only against durable provider facts; it
		// must not be reinterpreted through today's mutable plan configuration.
		err := tx.Where("invoice_idempotency_key = ?", invoiceID).First(&existing).Error
		if err == nil {
			if existing.UID != owner.UID || existing.SubscriptionID != subscriptionID ||
				existing.Status != model.STATUS_COMPLETE || existing.OrderType != model.ORDER_TYPE_MEMBER ||
				existing.Amount != renewalAmount || existing.TransID != transID ||
				existing.OrderMode != invoiceOrderMode ||
				existing.ProviderPriceID != priceID ||
				!sameOptionalTime(existing.BillingPeriodStart, billingPeriodStart) ||
				!sameOptionalTime(existing.BillingPeriodEnd, billingPeriodEnd) {
				return fmt.Errorf("subscription invoice %s replay conflicts with the committed payment identity", invoiceID)
			}
			newOrder = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check subscription invoice: %w", err)
		}
		// Liveness applies only to a new invoice. An exact replay remains valid
		// after its period has ended; rejecting it here would turn normal delayed
		// provider delivery into a permanent retry loop.
		if billingPeriodStart.After(now) {
			return fmt.Errorf("subscription provider billing period has not started")
		}
		if !billingPeriodEnd.After(now) {
			return fmt.Errorf("subscription provider billing period already ended")
		}

		// Every new invoice serializes on the oldest owner above. Once locked, the
		// latest completed Order is the durable current membership snapshot.
		var planKey, planName string
		var creditsAmount int
		if invoiceOrderMode == model.ORDER_MODE_SUBSCRIPTION {
			var current model.Order
			if err := tx.Where(
				"subscription_id = ? AND order_type = ? AND status = ?",
				subscriptionID, model.ORDER_TYPE_MEMBER, model.STATUS_COMPLETE,
			).Order("pay_time DESC, id DESC").First(&current).Error; err != nil {
				return fmt.Errorf("resolve current subscription membership: %w", err)
			}
			if current.SubscriptionID != subscriptionID || current.Status != model.STATUS_COMPLETE ||
				current.OrderType != model.ORDER_TYPE_MEMBER {
				return fmt.Errorf("current subscription membership does not match provider identity")
			}
			currentPriceID := strings.TrimSpace(current.ProviderPriceID)
			if currentPriceID != "" {
				// Normal cycle renewal is config-independent: the current Order already
				// froze the provider price, plan and allowance that are entitled to renew.
				if currentPriceID != priceID {
					return fmt.Errorf("subscription cycle price %s conflicts with current price %s", priceID, currentPriceID)
				}
				planKey = strings.TrimSpace(current.ProductID)
				planName = strings.TrimSpace(current.Name)
				creditsAmount = current.CreditsAmount
			} else {
				// Explicit legacy bridge: only a uniquely configured price may populate
				// the missing durable provider identity, and it must map back to the
				// legacy Order's existing product. Prefer any already-frozen allowance.
				mappedKey, mappedName, mappedCredits, err := resolveUniqueSubscriptionPlanByPriceID(priceID)
				if err != nil {
					return err
				}
				if strings.TrimSpace(current.ProductID) != mappedKey {
					return fmt.Errorf("legacy subscription cycle plan %s conflicts with current plan %s", mappedKey, current.ProductID)
				}
				planKey = mappedKey
				planName = strings.TrimSpace(current.Name)
				if planName == "" {
					planName = mappedName
				}
				creditsAmount = current.CreditsAmount
				if creditsAmount <= 0 {
					creditsAmount = mappedCredits
				}
			}
		} else {
			// A provider plan update is the only normal path that interprets a new
			// Price through today's configuration. Unique mapping is mandatory.
			var err error
			planKey, planName, creditsAmount, err = resolveUniqueSubscriptionPlanByPriceID(priceID)
			if err != nil {
				return err
			}
		}
		if planKey == "" {
			return fmt.Errorf("subscription invoice has no durable plan identity")
		}
		if planName == "" {
			planName = planKey
		}
		if creditsAmount <= 0 {
			return fmt.Errorf("positive subscription credits are not frozen for plan %s", planKey)
		}

		newOrder = model.Order{
			UID:                owner.UID,
			No:                 generateOrderNumber(),
			ProductID:          planKey,
			Status:             model.STATUS_COMPLETE,
			PayMethod:          "stripe",
			Amount:             renewalAmount,
			Name:               planName,
			IP:                 owner.IP,
			Invoice:            invoiceID,
			TransID:            transID,
			CustomerDetails:    customerDetails,
			PayTime:            now,
			OrderMode:          invoiceOrderMode,
			SubscriptionID:     subscriptionID,
			OrderType:          model.ORDER_TYPE_MEMBER,
			CreditsAmount:      creditsAmount,
			ProviderPriceID:    priceID,
			BillingPeriodStart: timePointer(billingPeriodStart),
			BillingPeriodEnd:   timePointer(billingPeriodEnd),
		}
		newOrder.CreatedAt = now
		newOrder.UpdatedAt = now
		if err := tx.Create(&newOrder).Error; err != nil {
			return fmt.Errorf("create subscription renewal order: %w", err)
		}
		if err := s.updateUserMemberTx(tx, newOrder, now, billingPeriodStart, billingPeriodEnd); err != nil {
			return fmt.Errorf("apply subscription renewal entitlement: %w", err)
		}
		applied = true
		return nil
	}()
	if applyErr != nil {
		return newOrder, false, applyErr
	}
	return newOrder, applied, nil
}

func subscriptionInvoiceOrderMode(billingReason string) (string, error) {
	switch billingReason {
	case "subscription_cycle":
		return model.ORDER_MODE_SUBSCRIPTION, nil
	case "subscription_update":
		return model.ORDER_MODE_SUBSCRIPTION_UPDATE, nil
	default:
		return "", fmt.Errorf("unsupported subscription invoice billing reason %q", billingReason)
	}
}

func resolveUniqueSubscriptionPlanByPriceID(priceID string) (planKey string, planName string, credits int, err error) {
	priceID = strings.TrimSpace(priceID)
	if priceID == "" {
		return "", "", 0, fmt.Errorf("subscription price id is required")
	}
	matches := 0
	for key, plan := range globals.GraConf.Stripe.Plans {
		if plan.PriceID != priceID {
			continue
		}
		matches++
		planKey = key
		planName = strings.TrimSpace(plan.Name)
		if planName == "" {
			planName = key
		}
		credits = plan.MonthlyCredits
		if credits <= 0 {
			credits = plan.Credits
		}
	}
	if matches != 1 || strings.TrimSpace(planKey) == "" {
		return "", "", 0, fmt.Errorf("subscription price %s maps to %d configured membership plans", priceID, matches)
	}
	return planKey, planName, credits, nil
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

func sameOptionalTime(value *time.Time, expected time.Time) bool {
	return value != nil && value.Equal(expected)
}

// formatInvoiceAmount formats a Stripe invoice amount (in minor units) as a
// human-readable, symbol-prefixed string. Falls back to "<amount> <CODE>" for
// currencies without a well-known symbol, and handles zero-decimal currencies.
func formatInvoiceAmount(amountMinor int64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	zeroDecimal := map[string]bool{
		"JPY": true, "KRW": true, "VND": true, "CLP": true, "BIF": true,
		"DJF": true, "GNF": true, "ISK": true, "KMF": true, "MGA": true,
		"PYG": true, "RWF": true, "UGX": true, "VUV": true, "XAF": true,
		"XOF": true, "XPF": true,
	}
	symbols := map[string]string{
		"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥",
		"KRW": "₩", "AUD": "A$", "CAD": "C$", "HKD": "HK$", "SGD": "S$",
	}
	symbol, hasSymbol := symbols[code]
	if zeroDecimal[code] {
		if hasSymbol {
			return fmt.Sprintf("%s%d", symbol, amountMinor)
		}
		if code == "" {
			return fmt.Sprintf("%d", amountMinor)
		}
		return fmt.Sprintf("%d %s", amountMinor, code)
	}
	major := float64(amountMinor) / 100.0
	if hasSymbol {
		return fmt.Sprintf("%s%.2f", symbol, major)
	}
	if code == "" {
		return fmt.Sprintf("$%.2f", major)
	}
	return fmt.Sprintf("%.2f %s", major, code)
}

// GrantCredits 发放 Credits (用于购买 Credits 包)
func (s *AccountService) GrantCredits(order model.Order) error {
	db := globals.GraDBs["system"]
	if db == nil {
		return fmt.Errorf("system database is unavailable")
	}
	var totalCredits int
	err := db.Transaction(func(tx *gorm.DB) error {
		var locked model.Order
		query := tx.Clauses(clause.Locking{Strength: "UPDATE"})
		if order.Id > 0 {
			query = query.Where("id = ?", order.Id)
		} else {
			query = query.Where("uid = ? AND no = ?", order.UID, order.No)
		}
		if err := query.First(&locked).Error; err != nil {
			return fmt.Errorf("lock credits purchase order: %w", err)
		}
		if locked.OrderType != model.ORDER_TYPE_CREDITS || locked.Status != model.STATUS_COMPLETE {
			return fmt.Errorf("credits grant order %s is not a completed Credits order", locked.No)
		}
		var err error
		totalCredits, err = s.grantCreditsTx(tx, locked)
		return err
	})
	if err != nil {
		return err
	}

	globals.Info(fmt.Sprintf("Ensured %d purchased credits for user %d (order: %s)", totalCredits, order.UID, order.No))
	return nil
}

func (s *AccountService) grantCreditsTx(tx *gorm.DB, order model.Order) (int, error) {
	credits := order.CreditsAmount
	if credits <= 0 {
		if pack, ok := globals.GraConf.Stripe.CreditPacks[order.ProductID]; ok {
			credits = pack.Credits
		}
	}
	if credits <= 0 {
		return 0, fmt.Errorf("credits amount not configured for order %s (product %s)", order.No, order.ProductID)
	}

	// The locked Order is the idempotency owner. Exact replay must preserve the
	// original immutable Pack, while mismatched historical data fails closed.
	var existing model.CreditsPack
	err := tx.Where("uid = ? AND source_type = ? AND source_id = ?", order.UID, model.CreditsSourcePurchase, order.No).
		Order("id ASC").First(&existing).Error
	if err == nil {
		if existing.CreditsTotal != credits {
			return 0, fmt.Errorf("credits purchase order %s Pack total is %d, want %d", order.No, existing.CreditsTotal, credits)
		}
		return credits, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}
	if err := NewCreditsPackService().CreatePackTx(tx, order.UID, model.CreditsSourcePurchase, order.No, credits, nil, "credits purchase"); err != nil {
		return 0, err
	}
	return credits, nil
}

// isFirstPurchase 检查用户是否为首次付费
func (s *AccountService) isFirstPurchase(uid int) bool {
	var count int64
	globals.GraDBs["system"].Model(&model.Order{}).
		Where("uid = ? AND status = ? AND order_type IN (?, ?)",
			uid, model.STATUS_COMPLETE, model.ORDER_TYPE_MEMBER, model.ORDER_TYPE_CREDITS).
		Count(&count)
	// 如果只有1个订单（当前订单），说明是首次付费
	return count <= 1
}

// GrantSubscriptionCredits 发放订阅的月度 Credits
func (s *AccountService) GrantSubscriptionCredits(uid int, planKey, grantKey string) error {
	db := globals.GraDBs["system"]
	if db == nil {
		return fmt.Errorf("system database is unavailable")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// Billing Order is the financial owner and idempotency anchor. Lock it
		// before Pack rows so duplicate webhook/application delivery cannot mint
		// two cycle Packs on legacy schemas without a source-tuple UNIQUE key.
		var grantOrder model.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("id", "uid", "no", "product_id", "status", "order_type", "order_mode", "credits_amount").
			Where("uid = ? AND no = ?", uid, grantKey).
			First(&grantOrder).Error; err != nil {
			return fmt.Errorf("lock subscription credit grant order: %w", err)
		}
		if grantOrder.OrderType != model.ORDER_TYPE_MEMBER || grantOrder.Status != model.STATUS_COMPLETE {
			return fmt.Errorf("subscription credit grant order %s is not a completed member order", grantKey)
		}
		if grantOrder.OrderMode == model.ORDER_MODE_SUBSCRIPTION_UPDATE {
			return fmt.Errorf("subscription update order %s cannot grant a full cycle Pack", grantKey)
		}
		if grantOrder.ProductID != planKey {
			return fmt.Errorf("subscription credit plan %s conflicts with order plan %s", planKey, grantOrder.ProductID)
		}
		user, err := lockCreditsOwnerUserTx(tx, uid)
		if err != nil {
			return err
		}
		return s.grantSubscriptionCreditsTx(tx, user, grantOrder, planKey, time.Now(), time.Time{})
	})
}

func (s *AccountService) grantSubscriptionCreditsTx(
	tx *gorm.DB,
	user model.User,
	grantOrder model.Order,
	planKey string,
	now time.Time,
	providerPeriodEnd time.Time,
) error {
	packService := NewCreditsPackService()
	credits := grantOrder.CreditsAmount
	if credits <= 0 {
		credits = packService.getPlanCredits(planKey)
	}
	if credits <= 0 {
		return fmt.Errorf("positive subscription credits are not configured for plan %s", planKey)
	}
	if grantOrder.UID != int(user.Id) || grantOrder.ProductID != planKey {
		return fmt.Errorf("subscription credit grant owner does not match locked user/plan")
	}

	if packService.isDeferredMonthlyCreditsPlan(planKey) {
		if err := lockExistingCreditsPacksTx(tx, grantOrder.UID); err != nil {
			return err
		}
		if err := packService.ensureCurrentSubscriptionCreditsForUserTx(tx, user, grantOrder.UID, planKey, credits, now); err != nil {
			return err
		}
		globals.Info(fmt.Sprintf("Ensured current monthly subscription credits for user %d (plan: %s)", grantOrder.UID, planKey))
		return nil
	}

	expiresAt := addCalendarMonthsClamped(now, 1)
	if !providerPeriodEnd.IsZero() {
		if !providerPeriodEnd.After(now) {
			return fmt.Errorf("subscription credit period for order %s already ended", grantOrder.No)
		}
		expiresAt = providerPeriodEnd
	}
	remark := fmt.Sprintf("subscription credits (%s)", planKey)
	if err := packService.createSubscriptionCyclePackTx(tx, grantOrder.UID, planKey, grantOrder.No, credits, &expiresAt, remark); err != nil {
		return err
	}
	globals.Info(fmt.Sprintf("Granted monthly cycle credits %d for user %d (plan: %s)", credits, grantOrder.UID, planKey))
	return nil
}

// inviteRewardCredits 读取配置中的邀请奖励数额；<=0 时退回 model.INVITE_CREDITS_REWARD
// 默认值，确保 YAML 漏配不会让邀请奖励变成 0。
func inviteRewardCredits() int {
	if n := globals.GraConf.Credits.Invite; n > 0 {
		return n
	}
	return model.INVITE_CREDITS_REWARD
}

// GrantInviteReward 发放邀请奖励 (双方各得相同数量 Credits，数额来自配置)
func (s *AccountService) GrantInviteReward(inviterUID, inviteeUID int) error {
	rewardAmount := inviteRewardCredits()

	// 给邀请人发放奖励
	if err := s.grantRewardCredits(inviterUID, rewardAmount, model.UserRewardsSourceTypeInvite, inviteeUID, "Invite reward"); err != nil {
		globals.Warn(fmt.Sprintf("Failed to grant invite reward to inviter %d: %v", inviterUID, err))
	}

	// 给被邀请人发放奖励
	if err := s.grantRewardCredits(inviteeUID, rewardAmount, model.UserRewardsSourceTypeInvite, inviterUID, "Welcome bonus from invitation"); err != nil {
		globals.Warn(fmt.Sprintf("Failed to grant invite reward to invitee %d: %v", inviteeUID, err))
	}

	globals.Info(fmt.Sprintf("Granted invite rewards: inviter %d and invitee %d each received %d credits", inviterUID, inviteeUID, rewardAmount))
	return nil
}

// grantRewardCredits 发放奖励 Credits 并记录
func (s *AccountService) grantRewardCredits(uid int, amount int, sourceType string, sourceID int, description string) error {
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		packService := NewCreditsPackService()
		expiresAt := rewardExpireAt()
		creditsSource := model.CreditsSourceBonus
		switch sourceType {
		case model.UserRewardsSourceTypeInvite:
			creditsSource = model.CreditsSourceInvite
		case model.UserRewardsSourceTypeCheckin:
			creditsSource = model.CreditsSourceCheckin
		case model.UserRewardsSourceTypeBonus:
			creditsSource = model.CreditsSourceBonus
		}

		if creditsSource == model.CreditsSourceInvite {
			if err := packService.AddToPackTx(tx, uid, creditsSource, "invite", amount, &expiresAt, description); err != nil {
				return err
			}
		} else if creditsSource == model.CreditsSourceCheckin {
			if err := packService.AddToPackTx(tx, uid, creditsSource, "checkin", amount, &expiresAt, description); err != nil {
				return err
			}
		} else {
			if err := packService.CreatePackTx(tx, uid, creditsSource, fmt.Sprintf("%d", sourceID), amount, &expiresAt, description); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 记录奖励
	reward := model.UserRewards{
		UID:         uid,
		SourceType:  sourceType,
		SourceID:    sourceID,
		Description: description,
		RewardItem:  model.UserRewardsItemTypeCredits,
		RewardNum:   amount,
		ExpireDate:  rewardExpireAt(),
	}
	globals.GraDBs["system"].Create(&reward)

	return nil
}

// DefaultSignupBonusCredits 当配置 credits.signup_bonus 缺省或非正数时的兜底数值。
// 真实生效值优先来自 globals.GraConf.Credits.SignupBonus（YAML 可调）。
const DefaultSignupBonusCredits = 100

// DefaultRewardExpireDays 当配置 credits.reward_expire_days 缺省或非正数时的兜底
// 数值——保持与历史行为一致的 365 天有效期，避免改配置出错把奖励变成永久或瞬时过期。
const DefaultRewardExpireDays = 365

// signupBonusCredits 读取配置中的注册赠送数量；<=0 时退回默认值，确保 YAML 漏配
// 不会让新用户拿到 0 credits。
func signupBonusCredits() int {
	if n := globals.GraConf.Credits.SignupBonus; n > 0 {
		return n
	}
	return DefaultSignupBonusCredits
}

// rewardExpireAt 计算注册/邀请/签到等奖励 credits 的过期时间，统一通过
// credits.reward_expire_days 配置项控制（<=0 时退回 DefaultRewardExpireDays）。
func rewardExpireAt() time.Time {
	days := globals.GraConf.Credits.RewardExpireDays
	if days <= 0 {
		days = DefaultRewardExpireDays
	}
	return time.Now().AddDate(0, 0, days)
}

// GrantSignupBonus 给新用户发放注册奖励 —— 建一个 source=bonus / source_id=signup 的
// pack，幂等（重复调不会多发）。PR4 起 credits 是唯一计费维度，不再需要次数周期起点。
func (s *AccountService) GrantSignupBonus(uid int) error {
	if uid <= 0 {
		return fmt.Errorf("invalid uid: %d", uid)
	}

	amount := signupBonusCredits()
	err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&model.CreditsPack{}).
			Where("uid = ? AND source_type = ? AND source_id = ?", uid, model.CreditsSourceBonus, "signup").
			Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		expiresAt := rewardExpireAt()
		return NewCreditsPackService().CreatePackTx(tx, uid, model.CreditsSourceBonus, "signup", amount, &expiresAt, "signup bonus")
	})
	if err != nil {
		return fmt.Errorf("grant signup bonus failed for uid %d: %w", uid, err)
	}
	return nil
}

// Dashboard file management methods removed - ChatExcel file functionality preserved in /service/chatExcel/file_service.go
