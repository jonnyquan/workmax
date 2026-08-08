package account

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	"gorm.io/gorm"
)

func installCheckoutTestGlobals(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDBs := globals.GraDBs
	previousConfig := globals.GraConf
	previousCreate := createStripeCheckoutSession
	previousGet := getStripeCheckoutSession
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe = config.Stripe{
		Mode: "test", TestDomain: "https://desktop.invalid", ReturnPath: "/checkout/return",
		Plans: map[string]config.SubscriptionPlan{
			"pro_monthly": {
				Name: "Pro Monthly", PriceID: "price_pro_monthly", Mode: "subscription",
				MonthlyPrice: 1999, MonthlyCredits: 100,
			},
		},
		CreditPacks: map[string]config.CreditPack{},
	}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
		globals.GraConf = previousConfig
		createStripeCheckoutSession = previousCreate
		getStripeCheckoutSession = previousGet
	})
}

func seedCheckoutUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()
	user := model.User{Email: "checkout@example.com", Nickname: "checkout"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed checkout user: %v", err)
	}
	return user
}

func monthlyCheckoutSpec() checkoutOrderSpec {
	return checkoutOrderSpec{
		ProductID: "pro_monthly", Name: "Pro Monthly", ProviderPriceID: "price_pro_monthly",
		StripeMode: "subscription", OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		OrderType: model.ORDER_TYPE_MEMBER, Amount: 1999, CreditsAmount: 100, IP: "127.0.0.1",
	}
}

func TestEnsureCheckoutOrder_ReusesOneFrozenOwner(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := seedCheckoutUser(t, db)
	spec := monthlyCheckoutSpec()
	now := time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC)

	const workers = 16
	orders := make(chan model.Order, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := ensureCheckoutOrder(db, user.Id, spec, now)
			orders <- order
			errs <- err
		}()
	}
	wg.Wait()
	close(orders)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent checkout admission: %v", err)
		}
	}
	owner := ""
	for order := range orders {
		if owner == "" {
			owner = order.No
		}
		if order.No != owner {
			t.Fatalf("checkout owners diverged: %s != %s", order.No, owner)
		}
	}
	var count int64
	if err := db.Model(&model.Order{}).Where("uid = ? AND status = ?", user.Id, model.STATUS_UNPAID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unpaid checkout owner count = %d, want 1", count)
	}

	drifted := spec
	drifted.ProviderPriceID = "price_changed"
	if _, err := ensureCheckoutOrder(db, user.Id, drifted, now.Add(time.Minute)); err == nil {
		t.Fatal("mutable config reinterpreted an existing checkout owner")
	}
}

func TestEnsureCheckoutOrder_BlocksIndefinitePaidMembershipWithoutSubscriptionOwner(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := seedCheckoutUser(t, db)
	if err := db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"member": model.MEMBER_SUBSCRIPTION_PRO, "member_end_time": time.Time{}, "member_subscription": "",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCheckoutOrder(db, user.Id, monthlyCheckoutSpec(), time.Now()); err == nil {
		t.Fatal("indefinite paid membership was replaced without a provider cancellation owner")
	}
	var count int64
	if err := db.Model(&model.Order{}).Where("uid = ?", user.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("blocked lifetime replacement left %d Orders", count)
	}
}

func TestEnsureCheckoutOrder_BlocksLiveSubscriptionWithoutMutatingProviderIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := seedCheckoutUser(t, db)
	now := time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC)
	if err := db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"member": model.MEMBER_SUBSCRIPTION_PRO, "member_end_time": now.AddDate(0, 1, 0),
		"member_subscription": "sub_old",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCheckoutOrder(db, user.Id, monthlyCheckoutSpec(), now); err == nil {
		t.Fatal("live subscription was admitted without a durable switch flow")
	}
	var got model.User
	if err := db.First(&got, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if got.MemberSubscription != "sub_old" {
		t.Fatalf("blocked checkout mutated provider identity to %q", got.MemberSubscription)
	}
	var count int64
	if err := db.Model(&model.Order{}).Where("uid = ?", user.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("blocked switch left %d checkout Orders", count)
	}
}

func TestEnsureCheckoutOrder_BlocksPlainProviderIdentityWhenLocalMembershipExpired(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := seedCheckoutUser(t, db)
	now := time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC)
	if err := db.Model(&model.User{}).Where("id = ?", user.Id).Updates(map[string]interface{}{
		"member": model.MEMBER_SUBSCRIPTION_PRO, "member_end_time": now.Add(-time.Hour),
		"member_subscription": "sub_provider_still_owns_billing",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ensureCheckoutOrder(db, user.Id, monthlyCheckoutSpec(), now); err == nil {
		t.Fatal("stale local expiry was treated as proof that the provider subscription ended")
	}
	var count int64
	if err := db.Model(&model.Order{}).Where("uid = ?", user.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ambiguous provider owner left %d checkout Orders", count)
	}
}

func checkoutTestRouter(uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/checkout", func(c *gin.Context) {
		c.Set("claims", &systemReq.CustomClaims{BaseClaims: systemReq.BaseClaims{Id: uid}})
		(&StripeApi{}).CreateCheckoutSession(c)
	})
	return router
}

func postCheckout(t *testing.T, router http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(`{"billingCycle":"monthly","mode":"subscription","planKey":"pro_monthly"}`)
	request := httptest.NewRequest(http.MethodPost, "/checkout", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestCreateCheckoutSession_PersistsAndRetrievesStableProviderSession(t *testing.T) {
	db := testutil.NewTestDB(t)
	installCheckoutTestGlobals(t, db)
	user := seedCheckoutUser(t, db)

	createCalls := 0
	getCalls := 0
	idempotencyKey := ""
	providerSession := &stripe.CheckoutSession{
		ID: "cs_checkout_stable", ClientSecret: "cs_secret_stable",
		Status: stripe.CheckoutSessionStatus("open"),
	}
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		createCalls++
		if params.IdempotencyKey == nil || *params.IdempotencyKey == "" {
			t.Fatal("Stripe create call has no idempotency key")
		}
		if len(params.PaymentMethodTypes) != 1 || params.PaymentMethodTypes[0] == nil ||
			*params.PaymentMethodTypes[0] != "card" {
			t.Fatalf("checkout payment methods = %#v, want exact card-only", params.PaymentMethodTypes)
		}
		idempotencyKey = *params.IdempotencyKey
		providerSession.ClientReferenceID = *params.ClientReferenceID
		return providerSession, nil
	}
	getStripeCheckoutSession = func(id string, _ *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		getCalls++
		if id != providerSession.ID {
			t.Fatalf("retrieved provider session %q, want %q", id, providerSession.ID)
		}
		return providerSession, nil
	}

	router := checkoutTestRouter(user.Id)
	for i := 0; i < 2; i++ {
		response := postCheckout(t, router)
		if response.Code != http.StatusOK {
			t.Fatalf("checkout response %d: %s", response.Code, response.Body.String())
		}
		var payload struct {
			ClientSecret string `json:"clientSecret"`
			OrderID      string `json:"orderId"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode checkout response: %v", err)
		}
		if payload.ClientSecret != providerSession.ClientSecret || payload.OrderID == "" {
			t.Fatalf("checkout response = %#v", payload)
		}
	}
	if createCalls != 1 || getCalls != 1 {
		t.Fatalf("provider calls create/get = %d/%d, want 1/1", createCalls, getCalls)
	}
	if idempotencyKey == "" {
		t.Fatal("stable provider idempotency key was not captured")
	}
	var order model.Order
	if err := db.Where("uid = ? AND status = ?", user.Id, model.STATUS_UNPAID).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	if order.CheckoutSessionID != providerSession.ID || order.ProviderPriceID != "price_pro_monthly" {
		t.Fatalf("durable checkout snapshot = %#v", order)
	}
}

func TestCreateCheckoutSession_OneTimeFreezesPaymentIntentOrderMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	installCheckoutTestGlobals(t, db)
	user := seedCheckoutUser(t, db)
	globals.GraConf.Stripe.CreditPacks["credits_50"] = config.CreditPack{
		Name: "50 Credits", PriceID: "price_credits_50", Price: 999, Credits: 50,
	}
	providerSession := &stripe.CheckoutSession{
		ID: "cs_one_time_metadata", ClientSecret: "cs_secret_one_time_metadata",
		Status: stripe.CheckoutSessionStatus("open"),
	}
	createStripeCheckoutSession = func(params *stripe.CheckoutSessionParams) (*stripe.CheckoutSession, error) {
		if params.Mode == nil || *params.Mode != "payment" || params.PaymentIntentData == nil {
			t.Fatalf("one-time checkout params have no PaymentIntent owner: %#v", params)
		}
		orderNo := ""
		if params.ClientReferenceID != nil {
			orderNo = *params.ClientReferenceID
		}
		if orderNo == "" || params.PaymentIntentData.Metadata["order_no"] != orderNo ||
			params.PaymentIntentData.Metadata["uid"] != strconv.FormatUint(uint64(user.Id), 10) {
			t.Fatalf("PaymentIntent metadata = %#v, order=%q", params.PaymentIntentData.Metadata, orderNo)
		}
		providerSession.ClientReferenceID = orderNo
		return providerSession, nil
	}

	body := []byte(`{"billingCycle":"payment","mode":"payment","planKey":"credits_50"}`)
	request := httptest.NewRequest(http.MethodPost, "/checkout", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	checkoutTestRouter(user.Id).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), providerSession.ClientSecret) {
		t.Fatalf("one-time checkout response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPersistCheckoutSessionID_IsExactAndAllowsOtherUnboundOrders(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := seedCheckoutUser(t, db)
	first := model.Order{UID: int(user.Id), No: "ORDER-checkout-first", Status: model.STATUS_UNPAID, OrderType: model.ORDER_TYPE_CREDITS, ProviderPriceID: "price_first"}
	second := model.Order{UID: int(user.Id), No: "ORDER-checkout-second", Status: model.STATUS_UNPAID, OrderType: model.ORDER_TYPE_CREDITS, ProviderPriceID: "price_second"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("multiple empty checkout session IDs must coexist: %v", err)
	}
	if err := persistCheckoutSessionID(db, first.Id, "cs_exact"); err != nil {
		t.Fatal(err)
	}
	if err := persistCheckoutSessionID(db, first.Id, "cs_exact"); err != nil {
		t.Fatalf("exact session replay: %v", err)
	}
	if err := persistCheckoutSessionID(db, first.Id, "cs_conflict"); err == nil {
		t.Fatal("conflicting provider session replaced the durable owner")
	}
}
