package account

import (
	"strings"
	"testing"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func installPaymentTestGlobals(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDBs := globals.GraDBs
	previousConfig := globals.GraConf
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"monthly_pro": {
			Name: "Monthly Pro", PriceID: "price_monthly_pro", MonthlyCredits: 100,
		},
	}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
		globals.GraConf = previousConfig
	})
}

func seedPaymentUser(t *testing.T, db *gorm.DB, user model.User) model.User {
	t.Helper()
	if user.Email == "" {
		user.Email = "payment-test@example.com"
	}
	if user.Nickname == "" {
		user.Nickname = "payment-test"
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed payment user: %v", err)
	}
	return user
}

func installCreditsPackFailureTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TRIGGER fail_credit_pack_insert
		BEFORE INSERT ON w_credits_pack
		BEGIN
			SELECT RAISE(ABORT, 'injected Pack failure');
		END`).Error; err != nil {
		t.Fatalf("install Pack failure trigger: %v", err)
	}
}

func dropCreditsPackFailureTrigger(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec("DROP TRIGGER fail_credit_pack_insert").Error; err != nil {
		t.Fatalf("drop Pack failure trigger: %v", err)
	}
}

func TestApplyPaidOrder_MemberEntitlementIsAtomicAndReplaySafe(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	user := seedPaymentUser(t, db, model.User{Member: model.MEMBER_SUBSCRIPTION_FREE})
	order := model.Order{
		UID: int(user.Id), No: "ORDER-member-atomic", ProductID: "monthly_pro",
		Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: 1999,
		Name: "Monthly Pro", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CheckoutSessionID: "cs_member_atomic",
		ProviderPriceID: "price_monthly_pro",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed member order: %v", err)
	}

	installCreditsPackFailureTrigger(t, db)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	svc := &AccountService{}
	_, applied, err := svc.applyPaidOrder(
		order.No, 1999, "Test User test@example.com", "in_member", "tx_member",
		"sub_member", "ch_member", model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID,
		"price_monthly_pro", now, addCalendarMonthsClamped(now, 1), now,
	)
	if err == nil || applied {
		t.Fatalf("failed Pack insert result = applied:%v err:%v, want false/error", applied, err)
	}

	var rolledBackOrder model.Order
	if err := db.First(&rolledBackOrder, order.Id).Error; err != nil {
		t.Fatal(err)
	}
	if rolledBackOrder.Status != model.STATUS_UNPAID || rolledBackOrder.Invoice != "" ||
		rolledBackOrder.SubscriptionID != "" || !rolledBackOrder.PayTime.IsZero() {
		t.Fatalf("paid Order escaped rollback: %#v", rolledBackOrder)
	}
	var rolledBackUser model.User
	if err := db.First(&rolledBackUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if rolledBackUser.Member != model.MEMBER_SUBSCRIPTION_FREE ||
		rolledBackUser.MemberSubscription != "" || !rolledBackUser.MemberEndTime.IsZero() {
		t.Fatalf("member entitlement escaped rollback: %#v", rolledBackUser)
	}
	dropCreditsPackFailureTrigger(t, db)

	paidOrder, applied, err := svc.applyPaidOrder(
		order.No, 1999, "Test User test@example.com", "in_member", "tx_member",
		"sub_member", "ch_member", model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID,
		"price_monthly_pro", now, addCalendarMonthsClamped(now, 1), now,
	)
	if err != nil || !applied {
		t.Fatalf("retry result = applied:%v err:%v, want true/nil", applied, err)
	}
	if paidOrder.SubscriptionID != "sub_member" {
		t.Fatalf("in-memory paid Order subscription = %q, want sub_member", paidOrder.SubscriptionID)
	}
	if err := db.First(&rolledBackUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	memberEnd := rolledBackUser.MemberEndTime
	if rolledBackUser.Member != model.MEMBER_SUBSCRIPTION_PRO || rolledBackUser.MemberSubscription != "sub_member" {
		t.Fatalf("member after retry = %#v", rolledBackUser)
	}
	var packCount int64
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Count(&packCount).Error; err != nil {
		t.Fatal(err)
	}
	if packCount != 1 {
		t.Fatalf("subscription Pack count = %d, want 1", packCount)
	}

	_, applied, err = svc.applyPaidOrder(
		order.No, 1999, "Test User test@example.com", "in_member", "tx_member",
		"sub_member", "ch_member", model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID,
		"price_monthly_pro", now, addCalendarMonthsClamped(now, 1), now.Add(24*time.Hour),
	)
	if err != nil || applied {
		t.Fatalf("exact replay result = applied:%v err:%v, want false/nil", applied, err)
	}
	if err := db.First(&rolledBackUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !rolledBackUser.MemberEndTime.Equal(memberEnd) {
		t.Fatalf("exact replay extended member end from %v to %v", memberEnd, rolledBackUser.MemberEndTime)
	}
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Count(&packCount).Error; err != nil {
		t.Fatal(err)
	}
	if packCount != 1 {
		t.Fatalf("exact replay Pack count = %d, want 1", packCount)
	}
}

func TestApplyPaidOrder_UpgradesActiveFreeMembership(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
	freeEnd := now.AddDate(0, 1, 0)
	user := seedPaymentUser(t, db, model.User{
		Member: model.MEMBER_SUBSCRIPTION_FREE, MemberStartTime: now.AddDate(0, -1, 0),
		MemberEndTime: freeEnd,
	})
	order := model.Order{
		UID: int(user.Id), No: "ORDER-free-to-paid", ProductID: "monthly_pro",
		Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: 1999,
		Name: "Monthly Pro", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100, CheckoutSessionID: "cs_free_to_paid",
		ProviderPriceID: "price_monthly_pro",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed free-to-paid Order: %v", err)
	}

	_, applied, err := (&AccountService{}).applyPaidOrder(
		order.No, 1999, "", "in_free_to_paid", "tx_free_to_paid", "sub_free_to_paid", "",
		model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID,
		"price_monthly_pro", now, addCalendarMonthsClamped(now, 1), now,
	)
	if err != nil || !applied {
		t.Fatalf("free-to-paid result = applied:%v err:%v", applied, err)
	}
	var got model.User
	if err := db.First(&got, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.Member != model.MEMBER_SUBSCRIPTION_PRO || got.MemberSubscription != "sub_free_to_paid" {
		t.Fatalf("free-to-paid member = %#v", got)
	}
	wantEnd := addCalendarMonthsClamped(now, 1)
	if !got.MemberStartTime.Equal(now) || !got.MemberEndTime.Equal(wantEnd) {
		t.Fatalf("free-to-paid period = %v..%v, want %v..%v", got.MemberStartTime, got.MemberEndTime, now, wantEnd)
	}
	if got.MemberEndTime.Equal(addCalendarMonthsClamped(freeEnd, 1)) {
		t.Fatal("paid membership incorrectly extended the active FREE trial end")
	}
}

func TestApplyPaidOrder_CreditsPurchaseIsAtomicAndReplaySafe(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	user := seedPaymentUser(t, db, model.User{Email: "credits-payment@example.com"})
	order := model.Order{
		UID: int(user.Id), No: "ORDER-credits-atomic", ProductID: "credits_40",
		Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: 500,
		Name: "40 Credits", OrderMode: model.ORDER_MODE_ONE_TIME,
		OrderType: model.ORDER_TYPE_CREDITS, CreditsAmount: 40, CheckoutSessionID: "cs_credits_atomic",
		ProviderPriceID: "price_credits_40",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed Credits order: %v", err)
	}

	installCreditsPackFailureTrigger(t, db)
	svc := &AccountService{}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	_, applied, err := svc.applyPaidOrder(
		order.No, 500, "", "in_credits", "tx_credits", "", "ch_credits",
		model.ORDER_MODE_ONE_TIME, order.CheckoutSessionID, "", time.Time{}, time.Time{}, now,
	)
	if err == nil || applied {
		t.Fatalf("failed purchase result = applied:%v err:%v, want false/error", applied, err)
	}
	var gotOrder model.Order
	if err := db.First(&gotOrder, order.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotOrder.Status != model.STATUS_UNPAID {
		t.Fatalf("purchase Order status after rollback = %s, want UNPAID", gotOrder.Status)
	}
	dropCreditsPackFailureTrigger(t, db)

	_, applied, err = svc.applyPaidOrder(
		order.No, 500, "", "in_credits", "tx_credits", "", "ch_credits",
		model.ORDER_MODE_ONE_TIME, order.CheckoutSessionID, "", time.Time{}, time.Time{}, now,
	)
	if err != nil || !applied {
		t.Fatalf("purchase retry result = applied:%v err:%v, want true/nil", applied, err)
	}
	_, applied, err = svc.applyPaidOrder(
		order.No, 500, "", "in_credits", "tx_credits", "", "ch_credits",
		model.ORDER_MODE_ONE_TIME, order.CheckoutSessionID, "", time.Time{}, time.Time{}, now.Add(time.Hour),
	)
	if err != nil || applied {
		t.Fatalf("purchase replay result = applied:%v err:%v, want false/nil", applied, err)
	}
	_, applied, err = svc.applyPaidOrder(
		order.No, 500, "", "in_credits_conflict", "tx_credits", "", "ch_credits",
		model.ORDER_MODE_ONE_TIME, order.CheckoutSessionID, "", time.Time{}, time.Time{}, now.Add(2*time.Hour),
	)
	if err == nil || applied {
		t.Fatalf("conflicting purchase replay = applied:%v err:%v, want false/error", applied, err)
	}
	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ? AND source_id = ?", user.Id, model.CreditsSourcePurchase, order.No).
		Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].CreditsTotal != 40 {
		t.Fatalf("purchase Packs = %#v, want one immutable 40-credit Pack", packs)
	}
}

func TestApplyPaidOrder_MissingSubscriptionCreditsFailsClosed(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	globals.GraConf.Stripe.Plans["missing_allowance"] = config.SubscriptionPlan{
		Name: "Misconfigured Plan", PriceID: "price_missing_allowance",
	}
	user := seedPaymentUser(t, db, model.User{Member: model.MEMBER_SUBSCRIPTION_FREE})
	order := model.Order{
		UID: int(user.Id), No: "ORDER-missing-allowance", ProductID: "missing_allowance",
		Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: 999,
		Name: "Misconfigured Plan", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CheckoutSessionID: "cs_missing_allowance",
		ProviderPriceID: "price_missing_allowance",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed misconfigured member Order: %v", err)
	}

	_, applied, err := (&AccountService{}).applyPaidOrder(
		order.No, 999, "", "in_missing", "tx_missing", "sub_missing", "",
		model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID, "price_missing_allowance",
		time.Now().Add(-time.Minute), time.Now().AddDate(0, 1, 0), time.Now(),
	)
	if err == nil || applied {
		t.Fatalf("misconfigured plan result = applied:%v err:%v, want false/error", applied, err)
	}
	var gotOrder model.Order
	if err := db.First(&gotOrder, order.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotOrder.Status != model.STATUS_UNPAID {
		t.Fatalf("misconfigured plan committed Order status %s", gotOrder.Status)
	}
	var gotUser model.User
	if err := db.First(&gotUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if gotUser.Member != model.MEMBER_SUBSCRIPTION_FREE || gotUser.MemberSubscription != "" {
		t.Fatalf("misconfigured plan committed member entitlement: %#v", gotUser)
	}
}

func TestApplyPaidOrder_DoesNotReviveCanceledOrRefundedOrder(t *testing.T) {
	for _, status := range []string{model.STATUS_CANCEL, model.STATUS_REFUND} {
		t.Run(status, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installPaymentTestGlobals(t, db)
			user := seedPaymentUser(t, db, model.User{Member: model.MEMBER_SUBSCRIPTION_FREE})
			order := model.Order{
				UID: int(user.Id), No: "ORDER-terminal-" + strings.ToLower(status),
				ProductID: "monthly_pro", Status: status, PayMethod: "stripe", Amount: 999,
				Name: "Terminal Order", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
				OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100, CheckoutSessionID: "cs_terminal_" + strings.ToLower(status),
				ProviderPriceID: "price_monthly_pro",
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatalf("seed terminal Order: %v", err)
			}

			_, applied, err := (&AccountService{}).applyPaidOrder(
				order.No, 999, "", "in_late", "tx_late", "sub_late", "",
				model.ORDER_MODE_SUBSCRIPTION, order.CheckoutSessionID, "", time.Time{}, time.Time{}, time.Now(),
			)
			if err == nil || applied {
				t.Fatalf("late callback result = applied:%v err:%v, want false/error", applied, err)
			}
			var got model.Order
			if err := db.First(&got, order.Id).Error; err != nil {
				t.Fatal(err)
			}
			if got.Status != status || got.Invoice != "" {
				t.Fatalf("late callback revived terminal Order: %#v", got)
			}
			var packCount int64
			if err := db.Model(&model.CreditsPack{}).Count(&packCount).Error; err != nil {
				t.Fatal(err)
			}
			if packCount != 0 {
				t.Fatalf("late callback created %d Packs", packCount)
			}
		})
	}
}

func TestApplyPaidOrder_RejectsCorruptOrUnderpaidOwnerBeforeCompletion(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*model.Order)
		amount  int64
		mode    string
		session string
	}{
		{name: "unsupported order type", mutate: func(order *model.Order) { order.OrderType = "mystery" }, amount: 1999, mode: model.ORDER_MODE_SUBSCRIPTION, session: "cs_guard"},
		{name: "missing product identity", mutate: func(order *model.Order) { order.ProductID = "" }, amount: 1999, mode: model.ORDER_MODE_SUBSCRIPTION, session: "cs_guard"},
		{name: "provider mode conflicts", mutate: func(order *model.Order) {}, amount: 1999, mode: model.ORDER_MODE_ONE_TIME, session: "cs_guard"},
		{name: "member payment below frozen minimum", mutate: func(order *model.Order) {}, amount: 1998, mode: model.ORDER_MODE_SUBSCRIPTION, session: "cs_guard"},
		{name: "checkout session conflicts", mutate: func(order *model.Order) {}, amount: 1999, mode: model.ORDER_MODE_SUBSCRIPTION, session: "cs_other"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installPaymentTestGlobals(t, db)
			user := seedPaymentUser(t, db, model.User{Member: model.MEMBER_SUBSCRIPTION_FREE})
			order := model.Order{
				UID: int(user.Id), No: "ORDER-guard-" + strings.ReplaceAll(test.name, " ", "-"),
				ProductID: "monthly_pro", Status: model.STATUS_UNPAID, PayMethod: "stripe",
				Amount: 1999, Name: "Monthly Pro", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
				OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100,
				ProviderPriceID: "price_monthly_pro", CheckoutSessionID: "cs_guard",
			}
			test.mutate(&order)
			if err := db.Create(&order).Error; err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			_, applied, err := (&AccountService{}).applyPaidOrder(
				order.No, test.amount, "", "in_guard", "tx_guard", "sub_guard", "",
				test.mode, test.session, "price_monthly_pro", now,
				addCalendarMonthsClamped(now, 1), now,
			)
			if err == nil || applied {
				t.Fatalf("corrupt payment = applied:%v err:%v", applied, err)
			}
			var stored model.Order
			if err := db.First(&stored, order.Id).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != model.STATUS_UNPAID || stored.Invoice != "" {
				t.Fatalf("corrupt payment committed owner: %#v", stored)
			}
		})
	}
}

func TestApplySubscriptionUpdate_RollsBackAndReplaysByInvoice(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	memberEnd := now.AddDate(0, 0, 10)
	user := seedPaymentUser(t, db, model.User{
		Email: "renewal@example.com", Member: model.MEMBER_SUBSCRIPTION_PRO,
		MemberStartTime: now.AddDate(0, -1, 0), MemberEndTime: memberEnd,
		MemberSubscription: "sub_renewal",
	})
	owner := model.Order{
		UID: int(user.Id), No: "ORDER-renewal-owner", ProductID: "monthly_pro",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", Amount: 1999,
		PayTime: now.AddDate(0, -1, 0), Name: "Monthly Pro", Invoice: "in_initial",
		SubscriptionID: "sub_renewal", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100,
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed subscription owner: %v", err)
	}

	installCreditsPackFailureTrigger(t, db)
	svc := &AccountService{}
	periodStart := now
	periodEnd := addCalendarMonthsClamped(periodStart, 1)
	_, applied, err := svc.applySubscriptionUpdate(
		"renewal customer", "price_monthly_pro", "in_renewal", "tx_renewal",
		"sub_renewal", 1999, now, periodStart, periodEnd, "subscription_cycle",
	)
	if err == nil || applied {
		t.Fatalf("failed renewal result = applied:%v err:%v, want false/error", applied, err)
	}
	var invoiceCount int64
	if err := db.Model(&model.Order{}).Where("invoice = ?", "in_renewal").Count(&invoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if invoiceCount != 0 {
		t.Fatalf("failed renewal persisted %d invoice Orders, want 0", invoiceCount)
	}
	var gotUser model.User
	if err := db.First(&gotUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !gotUser.MemberEndTime.Equal(memberEnd) {
		t.Fatalf("failed renewal extended member end to %v, want %v", gotUser.MemberEndTime, memberEnd)
	}
	dropCreditsPackFailureTrigger(t, db)

	renewal, applied, err := svc.applySubscriptionUpdate(
		"renewal customer", "price_monthly_pro", "in_renewal", "tx_renewal",
		"sub_renewal", 1999, now, periodStart, periodEnd, "subscription_cycle",
	)
	if err != nil || !applied {
		t.Fatalf("renewal retry result = applied:%v err:%v, want true/nil", applied, err)
	}
	if len(renewal.No) != 32 || !strings.HasPrefix(renewal.No, "ORDER-") {
		t.Fatalf("renewal order number = %q (%d bytes), want ORDER- and 32 bytes", renewal.No, len(renewal.No))
	}
	if err := db.First(&gotUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	wantEnd := periodEnd
	if !gotUser.MemberEndTime.Equal(wantEnd) {
		t.Fatalf("renewal member end = %v, want %v", gotUser.MemberEndTime, wantEnd)
	}
	stableEnd := gotUser.MemberEndTime

	replayed, applied, err := svc.applySubscriptionUpdate(
		"renewal customer", "price_monthly_pro", "in_renewal", "tx_renewal",
		"sub_renewal", 1999, now.Add(24*time.Hour), periodStart, periodEnd, "subscription_cycle",
	)
	if err != nil || applied {
		t.Fatalf("renewal replay result = applied:%v err:%v, want false/nil", applied, err)
	}
	if replayed.Id != renewal.Id {
		t.Fatalf("renewal replay Order id = %d, want %d", replayed.Id, renewal.Id)
	}
	if err := db.First(&gotUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !gotUser.MemberEndTime.Equal(stableEnd) {
		t.Fatalf("invoice replay extended member end from %v to %v", stableEnd, gotUser.MemberEndTime)
	}
	if err := db.Model(&model.Order{}).Where("invoice = ?", "in_renewal").Count(&invoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if invoiceCount != 1 {
		t.Fatalf("renewal invoice Order count = %d, want 1", invoiceCount)
	}
	var packCount int64
	if err := db.Model(&model.CreditsPack{}).
		Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Count(&packCount).Error; err != nil {
		t.Fatal(err)
	}
	if packCount != 1 {
		t.Fatalf("renewal subscription Pack count = %d, want 1", packCount)
	}

	caseDistinct, applied, err := svc.applySubscriptionUpdate(
		"renewal customer", "price_monthly_pro", "IN_RENEWAL", "tx_renewal_case",
		"sub_renewal", 1999, periodEnd, periodEnd, addCalendarMonthsClamped(periodEnd, 1), "subscription_cycle",
	)
	if err != nil || !applied || caseDistinct.Id == renewal.Id {
		t.Fatalf("binary-distinct invoice result = order:%d applied:%v err:%v", caseDistinct.Id, applied, err)
	}
	for _, key := range []string{"in_renewal", "IN_RENEWAL"} {
		var exactCount int64
		if err := db.Model(&model.Order{}).Where("invoice_idempotency_key = ?", key).Count(&exactCount).Error; err != nil {
			t.Fatal(err)
		}
		if exactCount != 1 {
			t.Fatalf("binary invoice key %q count = %d, want 1", key, exactCount)
		}
	}
}

func TestApplySubscriptionInvoiceTx_UsesCallerOwnedTransaction(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	oldEnd := now.Add(24 * time.Hour)
	periodEnd := addCalendarMonthsClamped(now, 1)
	user := seedPaymentUser(t, db, model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberStartTime: now.AddDate(0, -1, 0),
		MemberEndTime: oldEnd, MemberSubscription: "sub_tx_owned",
	})
	owner := model.Order{
		UID: int(user.Id), No: "ORDER-tx-owned-owner", ProductID: "monthly_pro",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", Amount: 1999,
		PayTime: now.AddDate(0, -1, 0), Name: "Monthly Pro", Invoice: "in_tx_owner",
		SubscriptionID: "sub_tx_owned", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100,
		ProviderPriceID: "price_monthly_pro",
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	command := SubscriptionInvoiceCommand{
		CustomerDetails: "transaction customer", ProviderPriceID: "price_monthly_pro",
		InvoiceID: "in_tx_cycle", TransactionID: "tx_cycle", SubscriptionID: "sub_tx_owned",
		AmountPaidCents: 1999, BillingPeriodStart: now, BillingPeriodEnd: periodEnd,
		BillingReason: "subscription_cycle",
	}
	svc := &AccountService{}
	if _, applied, err := svc.ApplySubscriptionInvoiceTx(nil, command, now); err == nil || applied {
		t.Fatalf("nil transaction = applied:%v err:%v, want false/error", applied, err)
	}

	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	rolledBackOrder, applied, err := svc.ApplySubscriptionInvoiceTx(tx, command, now)
	if err != nil || !applied {
		_ = tx.Rollback().Error
		t.Fatalf("caller-owned apply = applied:%v err:%v", applied, err)
	}
	if rolledBackOrder.Invoice != command.InvoiceID {
		_ = tx.Rollback().Error
		t.Fatalf("transaction-local Order invoice = %q", rolledBackOrder.Invoice)
	}
	var txPackCount int64
	if err := tx.Model(&model.CreditsPack{}).Where("uid = ?", user.Id).Count(&txPackCount).Error; err != nil {
		_ = tx.Rollback().Error
		t.Fatal(err)
	}
	if txPackCount != 1 {
		_ = tx.Rollback().Error
		t.Fatalf("transaction-local Pack count = %d, want 1", txPackCount)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}

	var persistedInvoiceCount, persistedPackCount int64
	if err := db.Model(&model.Order{}).Where("invoice = ?", command.InvoiceID).Count(&persistedInvoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditsPack{}).Where("uid = ?", user.Id).Count(&persistedPackCount).Error; err != nil {
		t.Fatal(err)
	}
	if persistedInvoiceCount != 0 || persistedPackCount != 0 {
		t.Fatalf("caller rollback leaked invoice:%d Packs:%d", persistedInvoiceCount, persistedPackCount)
	}
	var rolledBackUser model.User
	if err := db.First(&rolledBackUser, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !rolledBackUser.MemberEndTime.Equal(oldEnd) {
		t.Fatalf("caller rollback changed member end to %v, want %v", rolledBackUser.MemberEndTime, oldEnd)
	}

	var committed model.Order
	err = db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		committed, applied, applyErr = svc.ApplySubscriptionInvoiceTx(tx, command, now)
		return applyErr
	})
	if err != nil || !applied {
		t.Fatalf("caller commit = applied:%v err:%v", applied, err)
	}
	var replayed model.Order
	err = db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		replayed, applied, applyErr = svc.ApplySubscriptionInvoiceTx(tx, command, now.Add(48*time.Hour))
		return applyErr
	})
	if err != nil || applied || replayed.Id != committed.Id {
		t.Fatalf("caller-owned replay = order:%d applied:%v err:%v", replayed.Id, applied, err)
	}
	if err := db.Model(&model.Order{}).Where("invoice = ?", command.InvoiceID).Count(&persistedInvoiceCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditsPack{}).Where("uid = ?", user.Id).Count(&persistedPackCount).Error; err != nil {
		t.Fatal(err)
	}
	if persistedInvoiceCount != 1 || persistedPackCount != 1 {
		t.Fatalf("commit/replay durable rows = invoice:%d Packs:%d, want 1/1", persistedInvoiceCount, persistedPackCount)
	}
}

func TestApplySubscriptionUpdate_PreservesCurrentPackAndOnlyCycleGrantsNextAllowance(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	globals.GraConf.Stripe.Plans["basic"] = config.SubscriptionPlan{
		Name: "Basic", PriceID: "price_basic", MonthlyCredits: 100,
	}
	globals.GraConf.Stripe.Plans["pro"] = config.SubscriptionPlan{
		Name: "Pro", PriceID: "price_pro", MonthlyCredits: 500,
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	user := seedPaymentUser(t, db, model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberStartTime: periodStart,
		MemberEndTime: periodEnd, MemberSubscription: "sub_upgrade",
	})
	owner := model.Order{
		UID: int(user.Id), No: "ORDER-basic-owner", ProductID: "basic",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", Amount: 999,
		PayTime: periodStart, Name: "Basic", Invoice: "in_basic",
		SubscriptionID: "sub_upgrade", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100, ProviderPriceID: "price_basic",
		BillingPeriodStart: timePointer(periodStart), BillingPeriodEnd: timePointer(periodEnd),
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("seed basic subscription owner: %v", err)
	}
	oldPack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourceSubscription, SourceID: owner.No,
		CreditsTotal: 100, CreditsUsed: 25, ExpiresAt: timePointer(periodEnd),
		Remark: "subscription credits (basic)",
	}
	if err := db.Create(&oldPack).Error; err != nil {
		t.Fatalf("seed basic cycle Pack: %v", err)
	}

	svc := &AccountService{}
	updateOrder, applied, err := svc.applySubscriptionUpdate(
		"upgrade customer", "price_pro", "in_upgrade", "tx_upgrade", "sub_upgrade", 2499, now,
		periodStart, periodEnd, "subscription_update",
	)
	if err != nil || !applied {
		t.Fatalf("same-cycle upgrade = applied:%v err:%v", applied, err)
	}
	if updateOrder.ProductID != "pro" || updateOrder.CreditsAmount != 500 || updateOrder.Amount != 2499 ||
		updateOrder.OrderMode != model.ORDER_MODE_SUBSCRIPTION_UPDATE {
		t.Fatalf("upgrade snapshot = product:%s credits:%d amount:%d mode:%s",
			updateOrder.ProductID, updateOrder.CreditsAmount, updateOrder.Amount, updateOrder.OrderMode)
	}
	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Id != oldPack.Id || packs[0].CreditsTotal != 100 ||
		packs[0].CreditsUsed != 25 || packs[0].SourceID != owner.No || packs[0].ExpiresAt == nil ||
		!packs[0].ExpiresAt.Equal(periodEnd) {
		t.Fatalf("same-cycle update changed or added Pack: %#v", packs)
	}
	if err := svc.GrantSubscriptionCredits(int(user.Id), "pro", updateOrder.No); err == nil {
		t.Fatal("subscription_update Order granted a full cycle Pack through compatibility API")
	}

	replayedUpdate, applied, err := svc.applySubscriptionUpdate(
		"upgrade customer", "price_pro", "in_upgrade", "tx_upgrade", "sub_upgrade", 2499, now.Add(time.Hour),
		periodStart, periodEnd, "subscription_update",
	)
	if err != nil || applied || replayedUpdate.Id != updateOrder.Id {
		t.Fatalf("exact update replay = order:%d applied:%v err:%v", replayedUpdate.Id, applied, err)
	}
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].Id != oldPack.Id || packs[0].CreditsTotal != 100 || packs[0].CreditsUsed != 25 {
		t.Fatalf("exact update replay changed Packs: %#v", packs)
	}

	_, applied, err = svc.applySubscriptionUpdate(
		"upgrade customer", "price_pro", "in_upgrade", "tx_upgrade", "sub_upgrade", 2499, now.Add(time.Hour),
		periodStart, periodEnd, "subscription_cycle",
	)
	if err == nil || applied {
		t.Fatalf("billing-reason-conflicting replay = applied:%v err:%v, want false/error", applied, err)
	}
	_, applied, err = svc.applySubscriptionUpdate(
		"upgrade customer", "price_pro", "in_upgrade", "tx_upgrade", "sub_upgrade", 2498, now.Add(time.Hour),
		periodStart, periodEnd, "subscription_update",
	)
	if err == nil || applied {
		t.Fatalf("amount-conflicting invoice replay = applied:%v err:%v, want false/error", applied, err)
	}

	cycleEnd := addCalendarMonthsClamped(periodEnd, 1)
	cycleOrder, applied, err := svc.applySubscriptionUpdate(
		"renewal customer", "price_pro", "in_pro_cycle", "tx_pro_cycle", "sub_upgrade", 2499, periodEnd,
		periodEnd, cycleEnd, "subscription_cycle",
	)
	if err != nil || !applied {
		t.Fatalf("next pro cycle = applied:%v err:%v", applied, err)
	}
	if cycleOrder.OrderMode != model.ORDER_MODE_SUBSCRIPTION || cycleOrder.CreditsAmount != 500 {
		t.Fatalf("cycle Order = mode:%s credits:%d", cycleOrder.OrderMode, cycleOrder.CreditsAmount)
	}
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).
		Order("id ASC").Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	cycleSourceID, err := subscriptionCyclePackSourceID("pro", cycleOrder.No)
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) != 2 || packs[0].Id != oldPack.Id || packs[0].CreditsTotal != 100 ||
		packs[0].CreditsUsed != 25 || packs[1].CreditsTotal != 500 ||
		packs[1].SourceID != cycleSourceID || packs[1].ExpiresAt == nil || !packs[1].ExpiresAt.Equal(cycleEnd) {
		t.Fatalf("cycle grant did not preserve old Pack and add one pro Pack: %#v", packs)
	}
}

func TestApplySubscriptionUpdate_RejectsUnprovablePriceAndBillingReason(t *testing.T) {
	tests := []struct {
		name          string
		plans         map[string]config.SubscriptionPlan
		ownerPlan     string
		ownerPrice    string
		incomingPrice string
		billingReason string
	}{
		{
			name: "unknown provider price",
			plans: map[string]config.SubscriptionPlan{
				"basic": {Name: "Basic", PriceID: "price_basic", MonthlyCredits: 100},
			},
			ownerPlan: "basic", ownerPrice: "price_basic", incomingPrice: "price_unknown",
			billingReason: "subscription_update",
		},
		{
			name: "duplicate configured price",
			plans: map[string]config.SubscriptionPlan{
				"pro_a": {Name: "Pro A", PriceID: "price_duplicate", MonthlyCredits: 500},
				"pro_b": {Name: "Pro B", PriceID: "price_duplicate", MonthlyCredits: 500},
			},
			ownerPlan: "pro_a", ownerPrice: "price_duplicate", incomingPrice: "price_duplicate",
			billingReason: "subscription_update",
		},
		{
			name: "cycle changes durable current price",
			plans: map[string]config.SubscriptionPlan{
				"basic": {Name: "Basic", PriceID: "price_basic", MonthlyCredits: 100},
				"pro":   {Name: "Pro", PriceID: "price_pro", MonthlyCredits: 500},
			},
			ownerPlan: "basic", ownerPrice: "price_basic", incomingPrice: "price_pro",
			billingReason: "subscription_cycle",
		},
		{
			name: "unsupported provider billing reason",
			plans: map[string]config.SubscriptionPlan{
				"basic": {Name: "Basic", PriceID: "price_basic", MonthlyCredits: 100},
			},
			ownerPlan: "basic", ownerPrice: "price_basic", incomingPrice: "price_basic",
			billingReason: "subscription_create",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			installPaymentTestGlobals(t, db)
			globals.GraConf.Stripe.Plans = test.plans
			now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
			periodStart := now.Add(-7 * 24 * time.Hour)
			periodEnd := addCalendarMonthsClamped(periodStart, 1)
			user := seedPaymentUser(t, db, model.User{
				Member: model.MEMBER_SUBSCRIPTION_PRO, MemberStartTime: periodStart,
				MemberEndTime: periodEnd, MemberSubscription: "sub_price_guard",
			})
			owner := model.Order{
				UID: int(user.Id), No: "ORDER-price-guard", ProductID: test.ownerPlan,
				Status: model.STATUS_COMPLETE, PayMethod: "stripe", Amount: 999,
				PayTime: periodStart, Name: "Current Plan", Invoice: "in_price_owner",
				SubscriptionID: "sub_price_guard", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
				OrderType: model.ORDER_TYPE_MEMBER, CreditsAmount: 100,
				ProviderPriceID: test.ownerPrice, BillingPeriodStart: timePointer(periodStart),
				BillingPeriodEnd: timePointer(periodEnd),
			}
			if err := db.Create(&owner).Error; err != nil {
				t.Fatal(err)
			}

			_, applied, err := (&AccountService{}).applySubscriptionUpdate(
				"guard customer", test.incomingPrice, "in_price_guard", "tx_price_guard",
				"sub_price_guard", 999, now, periodStart, periodEnd, test.billingReason,
			)
			if err == nil || applied {
				t.Fatalf("unprovable invoice = applied:%v err:%v, want false/error", applied, err)
			}
			var invoiceCount int64
			if err := db.Model(&model.Order{}).Where("invoice = ?", "in_price_guard").Count(&invoiceCount).Error; err != nil {
				t.Fatal(err)
			}
			if invoiceCount != 0 {
				t.Fatalf("unprovable invoice persisted %d Orders", invoiceCount)
			}
			var packCount int64
			if err := db.Model(&model.CreditsPack{}).Where("uid = ?", user.Id).Count(&packCount).Error; err != nil {
				t.Fatal(err)
			}
			if packCount != 0 {
				t.Fatalf("unprovable invoice minted %d Packs", packCount)
			}
		})
	}
}

func TestApplySubscriptionCycle_UsesDurableSnapshotAfterConfigDrift(t *testing.T) {
	db := testutil.NewTestDB(t)
	installPaymentTestGlobals(t, db)
	// Renewal of a frozen provider price must not depend on a plan still being
	// present under the same mutable configuration key.
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"unrelated": {Name: "Unrelated", PriceID: "price_unrelated", MonthlyCredits: 999},
	}
	periodStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := addCalendarMonthsClamped(periodStart, 1)
	user := seedPaymentUser(t, db, model.User{
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberStartTime: addCalendarMonthsClamped(periodStart, -1),
		MemberEndTime: periodStart, MemberSubscription: "sub_durable_cycle",
	})
	owner := model.Order{
		UID: int(user.Id), No: "ORDER-durable-cycle-owner", ProductID: "retired_plan_key",
		Status: model.STATUS_COMPLETE, PayMethod: "stripe", Amount: 1777,
		PayTime: addCalendarMonthsClamped(periodStart, -1), Name: "Durable Retired Plan",
		Invoice: "in_durable_owner", SubscriptionID: "sub_durable_cycle",
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, OrderType: model.ORDER_TYPE_MEMBER,
		CreditsAmount: 123, ProviderPriceID: "price_durable",
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}

	cycle, applied, err := (&AccountService{}).applySubscriptionUpdate(
		"durable customer", "price_durable", "in_durable_cycle", "tx_durable_cycle",
		"sub_durable_cycle", 1777, periodStart, periodStart, periodEnd, "subscription_cycle",
	)
	if err != nil || !applied {
		t.Fatalf("durable cycle = applied:%v err:%v", applied, err)
	}
	if cycle.ProductID != owner.ProductID || cycle.Name != owner.Name ||
		cycle.CreditsAmount != owner.CreditsAmount || cycle.ProviderPriceID != owner.ProviderPriceID {
		t.Fatalf("cycle reinterpreted durable snapshot: %#v", cycle)
	}
	var packs []model.CreditsPack
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].CreditsTotal != 123 {
		t.Fatalf("durable cycle Packs = %#v", packs)
	}

	nextEnd := addCalendarMonthsClamped(periodEnd, 1)
	_, applied, err = (&AccountService{}).applySubscriptionUpdate(
		"durable customer", "price_wrong", "in_wrong_cycle_price", "tx_wrong_cycle_price",
		"sub_durable_cycle", 1777, periodEnd, periodEnd, nextEnd, "subscription_cycle",
	)
	if err == nil || applied {
		t.Fatalf("wrong durable cycle price = applied:%v err:%v, want false/error", applied, err)
	}
	if err := db.Where("uid = ? AND source_type = ?", user.Id, model.CreditsSourceSubscription).Find(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if len(packs) != 1 || packs[0].CreditsTotal != 123 {
		t.Fatalf("wrong-price cycle changed Packs: %#v", packs)
	}
}

func TestGenerateOrderNumberFitsProductionColumn(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		orderNo := generateOrderNumber()
		if len(orderNo) != 32 || !strings.HasPrefix(orderNo, "ORDER-") {
			t.Fatalf("order number = %q (%d bytes), want ORDER- and 32 bytes", orderNo, len(orderNo))
		}
		if _, duplicate := seen[orderNo]; duplicate {
			t.Fatalf("duplicate generated order number %q", orderNo)
		}
		seen[orderNo] = struct{}{}
	}
}
