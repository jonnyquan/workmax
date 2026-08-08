package callback

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	"server/service/commerce"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	stripeinvoice "github.com/stripe/stripe-go/v80/invoice"
	"github.com/stripe/stripe-go/v80/webhook"
	"gorm.io/gorm"
)

type fixedStripeInvoiceReader struct {
	invoice     *stripe.Invoice
	err         error
	getContext  context.Context
	listContext context.Context
}

type fixedStripePaymentIntentReader struct {
	intent *stripe.PaymentIntent
	err    error
	calls  int
	ctx    context.Context
}

func (reader *fixedStripePaymentIntentReader) Get(_ string, params *stripe.PaymentIntentParams) (*stripe.PaymentIntent, error) {
	reader.calls++
	if params == nil {
		return nil, &missingPaymentIntentExpansionError{}
	}
	reader.ctx = params.Context
	return reader.intent, reader.err
}

type missingPaymentIntentExpansionError struct{}

func (*missingPaymentIntentExpansionError) Error() string { return "missing payment intent expansion" }

func (reader *fixedStripeInvoiceReader) Get(_ string, params *stripe.InvoiceParams) (*stripe.Invoice, error) {
	if params != nil {
		reader.getContext = params.Context
	}
	return reader.invoice, reader.err
}

func (reader *fixedStripeInvoiceReader) ListLines(params *stripe.InvoiceListLinesParams) *stripeinvoice.LineItemIter {
	if params != nil {
		reader.listContext = params.Context
	}
	return nil
}

func recurringLine(id, priceID, subscriptionID string, start, end int64) *stripe.InvoiceLineItem {
	return &stripe.InvoiceLineItem{
		ID: id, Type: stripe.InvoiceLineItemTypeSubscription,
		Price: &stripe.Price{ID: priceID}, Subscription: &stripe.Subscription{ID: subscriptionID},
		Period: &stripe.Period{Start: start, End: end},
	}
}

func TestSelectRecurringInvoiceLine_SkipsProrationAndRejectsAmbiguity(t *testing.T) {
	previous := globals.GraConf
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"pro": {PriceID: "price_pro"},
	}
	t.Cleanup(func() { globals.GraConf = previous })

	proration := recurringLine("il_prorate", "price_old", "sub_1", 10, 20)
	proration.Proration = true
	canonical := recurringLine("il_recurring", "price_pro", "sub_1", 20, 30)
	unknown := recurringLine("il_unknown", "price_unknown", "sub_1", 20, 30)
	if _, err := selectRecurringInvoiceLine(
		&stripe.Invoice{Lines: &stripe.InvoiceLineItemList{Data: []*stripe.InvoiceLineItem{unknown}}},
		"sub_1", "",
	); err == nil {
		t.Fatal("single unknown recurring price must fail closed")
	}
	invoice := &stripe.Invoice{Lines: &stripe.InvoiceLineItemList{Data: []*stripe.InvoiceLineItem{proration, canonical}}}
	selected, err := selectRecurringInvoiceLine(invoice, "sub_1", "")
	if err != nil || selected.ID != canonical.ID {
		t.Fatalf("selected line = %#v, err=%v", selected, err)
	}

	addon := recurringLine("il_addon", "price_addon", "sub_1", 20, 30)
	invoice.Lines.Data = []*stripe.InvoiceLineItem{canonical, addon}
	selected, err = selectRecurringInvoiceLine(invoice, "sub_1", "")
	if err != nil || selected.ID != canonical.ID {
		t.Fatalf("configured membership selection = %#v, err=%v", selected, err)
	}

	globals.GraConf.Stripe.Plans["duplicate_pro"] = config.SubscriptionPlan{PriceID: "price_pro"}
	if _, err := selectRecurringInvoiceLine(
		&stripe.Invoice{Lines: &stripe.InvoiceLineItemList{Data: []*stripe.InvoiceLineItem{canonical}}},
		"sub_1", "",
	); err == nil {
		t.Fatal("price mapped to multiple configured plans must fail closed")
	}
	delete(globals.GraConf.Stripe.Plans, "duplicate_pro")

	globals.GraConf.Stripe.Plans["addon"] = config.SubscriptionPlan{PriceID: "price_addon"}
	if _, err := selectRecurringInvoiceLine(invoice, "sub_1", ""); err == nil {
		t.Fatal("multiple configured recurring lines must fail closed")
	}
	if _, err := selectRecurringInvoiceLine(&stripe.Invoice{}, "sub_1", ""); err == nil {
		t.Fatal("missing line collection must fail closed")
	}
}

func TestLoadRecurringCheckoutInvoiceSnapshot_BindsConfiguredKeyAndImmutableInvoicePeriod(t *testing.T) {
	previousConfig := globals.GraConf
	previousDBs := globals.GraDBs
	previousFactory := newStripeInvoiceReader
	db := testutil.NewTestDB(t)
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe.PrivateKey = "sk_test_exact_callback_key"
	globals.GraConf.Stripe.Plans = map[string]config.SubscriptionPlan{
		"drifted_plan": {Name: "Drifted Plan", PriceID: "price_config_drift", MonthlyCredits: 999},
	}
	t.Cleanup(func() {
		globals.GraConf = previousConfig
		globals.GraDBs = previousDBs
		newStripeInvoiceReader = previousFactory
	})

	const (
		invoiceID      = "in_checkout_exact"
		subscriptionID = "sub_checkout_exact"
		priceID        = "price_checkout_exact"
		periodStart    = int64(1786032000)
		periodEnd      = int64(1788710400)
		orderNo        = "ORDER-checkout-exact"
		checkoutID     = "cs_checkout_exact"
	)
	order := model.Order{
		No: orderNo, Status: model.STATUS_UNPAID, OrderType: model.ORDER_TYPE_MEMBER,
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, CheckoutSessionID: checkoutID,
		ProviderPriceID: priceID,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed frozen checkout Order: %v", err)
	}
	reader := &fixedStripeInvoiceReader{invoice: &stripe.Invoice{
		ID:           invoiceID,
		Status:       stripe.InvoiceStatusPaid,
		Subscription: &stripe.Subscription{ID: subscriptionID},
		Lines: &stripe.InvoiceLineItemList{Data: []*stripe.InvoiceLineItem{
			recurringLine("il_config_drift", "price_config_drift", subscriptionID, periodStart, periodEnd),
			recurringLine("il_checkout_exact", priceID, subscriptionID, periodStart, periodEnd),
		}},
	}}
	var boundKey string
	newStripeInvoiceReader = func(privateKey string) stripeInvoiceReader {
		boundKey = privateKey
		return reader
	}

	readContext, cancelRead := context.WithTimeout(context.Background(), time.Minute)
	defer cancelRead()
	gotPrice, gotStart, gotEnd, err := loadRecurringCheckoutInvoiceSnapshot(
		readContext, orderNo, checkoutID, invoiceID, subscriptionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if boundKey != globals.GraConf.Stripe.PrivateKey {
		t.Fatalf("invoice client key = %q, want exact configured key", boundKey)
	}
	if reader.getContext != readContext {
		t.Fatal("invoice retrieval did not inherit the bounded prepare context")
	}
	if gotPrice != priceID || !gotStart.Equal(time.Unix(periodStart, 0)) || !gotEnd.Equal(time.Unix(periodEnd, 0)) {
		t.Fatalf("snapshot = %q %v..%v", gotPrice, gotStart, gotEnd)
	}
	reader.invoice.Lines.Data = []*stripe.InvoiceLineItem{
		recurringLine("il_wrong_price", "price_config_drift", subscriptionID, periodStart, periodEnd),
	}
	if _, _, _, err := loadRecurringCheckoutInvoiceSnapshot(
		readContext, orderNo, checkoutID, invoiceID, subscriptionID,
	); err == nil {
		t.Fatal("invoice without the Order's exact frozen price must fail closed")
	}

	paginated := &stripe.Invoice{
		ID:    invoiceID,
		Lines: &stripe.InvoiceLineItemList{ListMeta: stripe.ListMeta{HasMore: true}},
	}
	if err := completeInvoiceLines(readContext, paginated, reader); err == nil {
		t.Fatal("nil invoice iterator unexpectedly succeeded")
	}
	if reader.listContext != readContext {
		t.Fatal("invoice line pagination did not inherit the bounded prepare context")
	}

	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := loadRecurringCheckoutInvoiceSnapshot(
		canceledContext, orderNo, checkoutID, invoiceID, subscriptionID,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled invoice snapshot error = %v, want context canceled", err)
	}
}

func TestStripeCallback_AmbiguousPaidRenewalReturns5xx(t *testing.T) {
	previous := globals.GraConf
	previousDBs := globals.GraDBs
	db := testutil.NewTestDB(t)
	const secret = "whsec_test_invoice_lines"
	globals.GraConf.Stripe.EndpointSecret = secret
	globals.GraConf.Stripe.Mode = "test"
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraConf = previous
		globals.GraDBs = previousDBs
	})
	if err := db.Create(&model.Order{
		UID: 1, No: "ORDER-ambiguous-renewal", ProductID: "pro", Status: model.STATUS_COMPLETE,
		OrderType: model.ORDER_TYPE_MEMBER, OrderMode: model.ORDER_MODE_SUBSCRIPTION,
		SubscriptionID: "sub_1", ProviderPriceID: "price_pro", PayTime: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	payload := []byte(strings.Replace(`{
		"id":"evt_ambiguous","object":"event","api_version":"__API_VERSION__",
		"created":1786032000,"livemode":false,"type":"invoice.payment_succeeded",
		"data":{"object":{
			"id":"in_ambiguous","object":"invoice","status":"paid",
			"billing_reason":"subscription_cycle","subscription":"sub_1",
			"amount_paid":1999,"currency":"usd","lines":{"object":"list","data":[]}
		}}
	}`, "__API_VERSION__", stripe.APIVersion, 1))
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: secret, Timestamp: time.Now(),
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/stripe", (&StripeCallbackApi{}).StripeCallback)
	request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(string(signed.Payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ambiguous paid renewal status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var durable model.CommerceProviderEvent
	if err := db.Where("event_id = ?", "evt_ambiguous").First(&durable).Error; err != nil {
		t.Fatal(err)
	}
	if durable.Status != model.CommerceProviderEventStatusRetryWait || durable.LastErrorCode != "invoice_line_ambiguous" {
		t.Fatalf("durable ambiguous event = status %q error %q", durable.Status, durable.LastErrorCode)
	}
}

func TestStripeCallback_OwnedCompletedCheckoutWithoutPaidStatusIsDurablyQuarantined(t *testing.T) {
	previous := globals.GraConf
	previousDBs := globals.GraDBs
	db := testutil.NewTestDB(t)
	const secret = "whsec_test_unpaid_checkout"
	globals.GraConf.Stripe.EndpointSecret = secret
	globals.GraConf.Stripe.Mode = "test"
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraConf = previous
		globals.GraDBs = previousDBs
	})

	payload := []byte(strings.Replace(`{
		"id":"evt_unpaid_checkout","object":"event","api_version":"__API_VERSION__",
		"created":1786032000,"livemode":false,"type":"checkout.session.completed",
		"data":{"object":{
			"id":"cs_unpaid_checkout","object":"checkout.session",
			"client_reference_id":"ORDER-owned-unpaid","payment_status":"unpaid",
			"mode":"subscription"
		}}
	}`, "__API_VERSION__", stripe.APIVersion, 1))
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: secret, Timestamp: time.Now(),
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/stripe", (&StripeCallbackApi{}).StripeCallback)
	request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(string(signed.Payload)))
	request.Header.Set("Stripe-Signature", signed.Header)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unpaid owned checkout status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var durable model.CommerceProviderEvent
	if err := db.Where("event_id = ?", "evt_unpaid_checkout").First(&durable).Error; err != nil {
		t.Fatal(err)
	}
	if durable.Status != model.CommerceProviderEventStatusManualReview || durable.LastErrorCode != "checkout_not_paid" {
		t.Fatalf("durable unpaid event = status %q error %q", durable.Status, durable.LastErrorCode)
	}
}

func TestStripeCallback_OneTimeIDOnlyPaymentIntentResolvesExactChargeReplay(t *testing.T) {
	previousConfig := globals.GraConf
	previousDBs := globals.GraDBs
	previousFactory := newStripePaymentIntentReader
	previousProcessorFactory := stripeProviderEventProcessorFactory
	db := testutil.NewTestDB(t)
	const secret = "whsec_test_one_time_payment"
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe.EndpointSecret = secret
	globals.GraConf.Stripe.PrivateKey = "sk_test_one_time_payment"
	globals.GraConf.Stripe.Mode = "test"
	stripeProviderEventProcessorFactory = func() commerce.ProviderEventProcessor {
		return &StripeProviderEventProcessor{}
	}
	t.Cleanup(func() {
		globals.GraConf = previousConfig
		globals.GraDBs = previousDBs
		newStripePaymentIntentReader = previousFactory
		stripeProviderEventProcessorFactory = previousProcessorFactory
	})

	user := model.User{Email: "one-time-owner@example.com", Nickname: "one-time-owner"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.Order{
		UID: int(user.Id), No: "ORDER-one-time-id-only", ProductID: "credits_50",
		Status: model.STATUS_UNPAID, PayMethod: "stripe", Amount: 999,
		Name: "50 Credits", OrderMode: model.ORDER_MODE_ONE_TIME,
		OrderType: model.ORDER_TYPE_CREDITS, CreditsAmount: 50,
		ProviderPriceID: "price_credits_50", CheckoutSessionID: "cs_id_only_exact",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reader := &fixedStripePaymentIntentReader{intent: &stripe.PaymentIntent{
		ID: "pi_id_only_exact", Status: stripe.PaymentIntentStatusSucceeded,
		Amount: 999, AmountReceived: 999, Currency: stripe.CurrencyUSD,
		Metadata: map[string]string{"order_no": order.No},
		LatestCharge: &stripe.Charge{
			ID: "ch_id_only_exact", Paid: true, Status: stripe.ChargeStatusSucceeded,
			Amount: 999, AmountCaptured: 999, Currency: stripe.CurrencyUSD,
			PaymentIntent: &stripe.PaymentIntent{ID: "pi_id_only_exact"},
		},
	}}
	boundKey := ""
	newStripePaymentIntentReader = func(privateKey string) stripePaymentIntentReader {
		boundKey = privateKey
		return reader
	}

	payload := []byte(strings.Replace(`{
		"id":"evt_one_time_id_only","object":"event","api_version":"__API_VERSION__",
		"created":1786032000,"livemode":false,"type":"checkout.session.completed",
		"data":{"object":{
			"id":"cs_id_only_exact","object":"checkout.session",
			"client_reference_id":"ORDER-one-time-id-only",
			"payment_status":"paid","status":"complete","mode":"payment",
			"amount_total":999,"currency":"usd","payment_intent":"pi_id_only_exact"
		}}
	}`, "__API_VERSION__", stripe.APIVersion, 1))
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload, Secret: secret, Timestamp: time.Now(),
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/stripe", (&StripeCallbackApi{}).StripeCallback)
	for replay := 0; replay < 2; replay++ {
		request := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(string(signed.Payload)))
		request.Header.Set("Stripe-Signature", signed.Header)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("one-time replay %d status = %d, body=%s", replay, recorder.Code, recorder.Body.String())
		}
	}
	if reader.calls != 1 || boundKey != globals.GraConf.Stripe.PrivateKey {
		t.Fatalf("payment intent calls/key = %d/%q", reader.calls, boundKey)
	}
	if reader.ctx == nil {
		t.Fatal("payment intent retrieval received no context")
	}
	deadline, hasDeadline := reader.ctx.Deadline()
	if !hasDeadline || !deadline.Before(time.Now().Add(commerce.DefaultProviderEventLeaseTTL)) {
		t.Fatalf("payment intent context deadline = %v, present=%t", deadline, hasDeadline)
	}
	var inboxCount, outboxCount, packCount int64
	if err := db.Model(&model.CommerceProviderEvent{}).Count(&inboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CommerceOutbox{}).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CreditsPack{}).Where("source_id = ?", order.No).Count(&packCount).Error; err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || outboxCount != 1 || packCount != 1 {
		t.Fatalf("durable replay counts inbox/outbox/pack = %d/%d/%d", inboxCount, outboxCount, packCount)
	}
	if err := db.First(&order, order.Id).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != model.STATUS_COMPLETE || order.TransID != "pi_id_only_exact" || order.ChargeID != "ch_id_only_exact" {
		t.Fatalf("settled order = status %q transaction %q charge %q", order.Status, order.TransID, order.ChargeID)
	}
}
