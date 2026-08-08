package callback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"server/globals"
	"server/model"
	"server/service/commerce"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	stripeinvoice "github.com/stripe/stripe-go/v80/invoice"
	stripepaymentintent "github.com/stripe/stripe-go/v80/paymentintent"
	"github.com/stripe/stripe-go/v80/webhook"
)

type StripeCallbackApi struct{}

type stripeInvoiceReader interface {
	Get(string, *stripe.InvoiceParams) (*stripe.Invoice, error)
	ListLines(*stripe.InvoiceListLinesParams) *stripeinvoice.LineItemIter
}

var newStripeInvoiceReader = func(privateKey string) stripeInvoiceReader {
	return stripeinvoice.Client{B: stripe.GetBackend(stripe.APIBackend), Key: privateKey}
}

type stripePaymentIntentReader interface {
	Get(string, *stripe.PaymentIntentParams) (*stripe.PaymentIntent, error)
}

var newStripePaymentIntentReader = func(privateKey string) stripePaymentIntentReader {
	return stripepaymentintent.Client{B: stripe.GetBackend(stripe.APIBackend), Key: privateKey}
}

func loadOneTimeCheckoutPaymentSnapshot(
	ctx context.Context,
	paymentIntentID string,
	orderNo string,
	amount int64,
	currency string,
) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("one-time checkout payment context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	paymentIntentID = strings.TrimSpace(paymentIntentID)
	orderNo = strings.TrimSpace(orderNo)
	currency = strings.ToLower(strings.TrimSpace(currency))
	if paymentIntentID == "" || orderNo == "" || amount <= 0 || currency == "" {
		return "", fmt.Errorf("one-time checkout payment identity is incomplete")
	}
	params := &stripe.PaymentIntentParams{}
	params.Context = ctx
	params.AddExpand("latest_charge")
	intent, err := newStripePaymentIntentReader(globals.GraConf.Stripe.PrivateKey).Get(paymentIntentID, params)
	if err != nil {
		return "", fmt.Errorf("retrieve checkout payment intent %s: %w", paymentIntentID, err)
	}
	if intent == nil || intent.ID != paymentIntentID || intent.Status != stripe.PaymentIntentStatusSucceeded ||
		intent.Amount != amount || intent.AmountReceived != amount || string(intent.Currency) != currency ||
		intent.Metadata["order_no"] != orderNo {
		return "", fmt.Errorf("checkout payment intent %s does not match the paid order", paymentIntentID)
	}
	charge := intent.LatestCharge
	if charge == nil || strings.TrimSpace(charge.ID) == "" || !charge.Paid ||
		charge.Status != stripe.ChargeStatusSucceeded || charge.Amount != amount ||
		charge.AmountCaptured != amount || string(charge.Currency) != currency ||
		charge.PaymentIntent == nil || charge.PaymentIntent.ID != paymentIntentID {
		return "", fmt.Errorf("checkout payment intent %s has no exact captured charge", paymentIntentID)
	}
	return charge.ID, nil
}

func loadRecurringCheckoutInvoiceSnapshot(
	ctx context.Context,
	orderNo string,
	checkoutSessionID string,
	invoiceID string,
	subscriptionID string,
) (string, time.Time, time.Time, error) {
	if ctx == nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("recurring checkout invoice context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	orderNo = strings.TrimSpace(orderNo)
	checkoutSessionID = strings.TrimSpace(checkoutSessionID)
	invoiceID = strings.TrimSpace(invoiceID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	if orderNo == "" || checkoutSessionID == "" || invoiceID == "" || subscriptionID == "" {
		return "", time.Time{}, time.Time{}, fmt.Errorf("checkout order, session, invoice and subscription ids are required")
	}
	db := globals.GraDBs["system"]
	if db == nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("system database is unavailable")
	}
	var order model.Order
	if err := db.WithContext(ctx).Select(
		"id", "no", "status", "order_type", "order_mode", "checkout_session_id", "provider_price_id",
	).Where("no = ?", orderNo).First(&order).Error; err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("load checkout Order %s: %w", orderNo, err)
	}
	frozenPriceID := strings.TrimSpace(order.ProviderPriceID)
	if order.No != orderNo || order.CheckoutSessionID != checkoutSessionID ||
		(order.Status != model.STATUS_UNPAID && order.Status != model.STATUS_COMPLETE) ||
		order.OrderType != model.ORDER_TYPE_MEMBER || order.OrderMode != model.ORDER_MODE_SUBSCRIPTION ||
		frozenPriceID == "" {
		return "", time.Time{}, time.Time{}, fmt.Errorf("checkout Order %s has no matching frozen subscription price", orderNo)
	}
	invoiceReader := newStripeInvoiceReader(globals.GraConf.Stripe.PrivateKey)
	invoiceParams := &stripe.InvoiceParams{}
	invoiceParams.Context = ctx
	invoice, err := invoiceReader.Get(invoiceID, invoiceParams)
	if err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("retrieve checkout invoice %s: %w", invoiceID, err)
	}
	if invoice == nil || invoice.ID != invoiceID || invoice.Status != stripe.InvoiceStatusPaid ||
		invoice.Subscription == nil || invoice.Subscription.ID != subscriptionID {
		return "", time.Time{}, time.Time{}, fmt.Errorf("checkout invoice %s does not match the paid subscription", invoiceID)
	}
	if err := completeInvoiceLines(ctx, invoice, invoiceReader); err != nil {
		return "", time.Time{}, time.Time{}, err
	}
	line, err := selectRecurringInvoiceLine(invoice, subscriptionID, frozenPriceID)
	if err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("resolve checkout invoice %s: %w", invoiceID, err)
	}
	return line.Price.ID, time.Unix(line.Period.Start, 0), time.Unix(line.Period.End, 0), nil
}

func completeInvoiceLines(ctx context.Context, invoice *stripe.Invoice, invoiceReader stripeInvoiceReader) error {
	if ctx == nil {
		return fmt.Errorf("invoice line context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if invoice == nil || invoice.Lines == nil {
		return fmt.Errorf("invoice line collection is missing")
	}
	if !invoice.Lines.HasMore {
		return nil
	}
	if strings.TrimSpace(invoice.ID) == "" {
		return fmt.Errorf("paginated invoice line collection has no invoice identity")
	}
	params := &stripe.InvoiceListLinesParams{Invoice: stripe.String(invoice.ID)}
	params.Context = ctx
	params.Limit = stripe.Int64(100)
	iterator := invoiceReader.ListLines(params)
	if iterator == nil {
		return fmt.Errorf("invoice %s line iterator is unavailable", invoice.ID)
	}
	lines := make([]*stripe.InvoiceLineItem, 0, len(invoice.Lines.Data)+16)
	for iterator.Next() {
		if len(lines) >= 1000 {
			return fmt.Errorf("invoice %s has too many lines to classify safely", invoice.ID)
		}
		lines = append(lines, iterator.InvoiceLineItem())
	}
	if err := iterator.Err(); err != nil {
		return fmt.Errorf("list all lines for invoice %s: %w", invoice.ID, err)
	}
	invoice.Lines.Data = lines
	invoice.Lines.HasMore = false
	return nil
}

// selectRecurringInvoiceLine rejects Stripe's proration/pending-item ordering
// as an entitlement signal. A paid invoice may contain many lines and the
// first one is explicitly not guaranteed to be the recurring membership line.
func selectRecurringInvoiceLine(invoice *stripe.Invoice, subscriptionID string, expectedPriceID string) (*stripe.InvoiceLineItem, error) {
	subscriptionID = strings.TrimSpace(subscriptionID)
	expectedPriceID = strings.TrimSpace(expectedPriceID)
	if invoice == nil || invoice.Lines == nil || invoice.Lines.HasMore || subscriptionID == "" {
		return nil, fmt.Errorf("subscription invoice has no line collection")
	}
	var candidates []*stripe.InvoiceLineItem
	for _, line := range invoice.Lines.Data {
		if line == nil || line.Proration || line.Type != stripe.InvoiceLineItemTypeSubscription ||
			line.Price == nil || strings.TrimSpace(line.Price.ID) == "" || line.Period == nil ||
			line.Period.Start <= 0 || line.Period.End <= line.Period.Start {
			continue
		}
		if line.Subscription == nil || line.Subscription.ID != subscriptionID {
			continue
		}
		candidates = append(candidates, line)
	}
	if expectedPriceID != "" {
		var exact []*stripe.InvoiceLineItem
		for _, candidate := range candidates {
			if candidate.Price.ID == expectedPriceID {
				exact = append(exact, candidate)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
		return nil, fmt.Errorf(
			"subscription invoice has %d recurring lines for frozen price %s",
			len(exact), expectedPriceID,
		)
	}
	var configured []*stripe.InvoiceLineItem
	for _, candidate := range candidates {
		matches := 0
		for _, plan := range globals.GraConf.Stripe.Plans {
			if plan.PriceID == candidate.Price.ID {
				matches++
			}
		}
		if matches > 1 {
			return nil, fmt.Errorf("recurring price %s maps to multiple membership plans", candidate.Price.ID)
		}
		if matches == 1 {
			configured = append(configured, candidate)
		}
	}
	if len(configured) == 1 {
		return configured[0], nil
	}
	return nil, fmt.Errorf(
		"subscription invoice has %d recurring candidates and %d uniquely configured membership lines",
		len(candidates), len(configured),
	)
}

type stripeCallbackResponse struct {
	Status string `json:"status"`
}

// StripeCallback authenticates and durably admits a Stripe event before any
// financial work starts. Once receipt is durable the provider is acknowledged;
// retry/manual-review state belongs to the Inbox reconciler, not Stripe's HTTP
// delivery loop.
func (a *StripeCallbackApi) StripeCallback(c *gin.Context) {
	const maxBodyBytes = int64(commerce.MaxProviderPayloadBytes)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)
	payload, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, stripeCallbackResponse{Status: "error: payload too large"})
			return
		}
		c.JSON(http.StatusBadRequest, stripeCallbackResponse{Status: "error: unreadable payload"})
		return
	}

	endpointSecret := globals.GraConf.Stripe.EndpointSecret
	signature := c.GetHeader("Stripe-Signature")
	if signature == "" {
		c.JSON(http.StatusBadRequest, stripeCallbackResponse{Status: "error: missing signature"})
		return
	}
	if endpointSecret == "" {
		globals.Error("Stripe webhook endpoint secret is not configured")
		c.JSON(http.StatusInternalServerError, stripeCallbackResponse{Status: "error: configuration"})
		return
	}
	event, err := webhook.ConstructEvent(payload, signature, endpointSecret)
	if err != nil {
		globals.Warn("Stripe webhook signature or API version verification failed")
		c.JSON(http.StatusBadRequest, stripeCallbackResponse{Status: "error: invalid signature"})
		return
	}

	objectID, err := stripeEventObjectID(event)
	if err != nil {
		c.JSON(http.StatusBadRequest, stripeCallbackResponse{Status: "error: invalid event"})
		return
	}
	configuredMode := strings.ToLower(strings.TrimSpace(globals.GraConf.Stripe.Mode))
	if configuredMode != "test" && configuredMode != "live" {
		globals.Error("Stripe webhook mode is not configured as test or live")
		c.JSON(http.StatusInternalServerError, stripeCallbackResponse{Status: "error: configuration"})
		return
	}
	if err := validateStripeMode(configuredMode, event.Livemode); err != nil {
		c.JSON(http.StatusBadRequest, stripeCallbackResponse{Status: "error: mode mismatch"})
		return
	}
	createdAt := time.Unix(event.Created, 0).UTC()
	input := commerce.ProviderEventInput{
		Provider:              stripeProviderName,
		ProviderAccountID:     stripeProviderAccount(event.Account),
		ProviderAPIVersion:    event.APIVersion,
		EventID:               event.ID,
		EventType:             string(event.Type),
		ObjectID:              objectID,
		LiveMode:              event.Livemode,
		ProviderCreatedAt:     &createdAt,
		VerificationKeyDigest: commerce.VerificationKeyDigest(endpointSecret),
		Payload:               payload,
	}
	db := globals.GraDBs["system"]
	eventService, err := commerce.NewProviderEventService(db, commerce.ProviderEventServiceOptions{
		WorkerID: "stripe-webhook-inline",
	})
	if err != nil {
		globals.Error("Stripe provider inbox is unavailable")
		c.JSON(http.StatusServiceUnavailable, stripeCallbackResponse{Status: "error: inbox unavailable"})
		return
	}
	ingested, err := eventService.Ingest(c.Request.Context(), input)
	if err != nil {
		if errors.Is(err, commerce.ErrProviderEventConflict) {
			globals.Error(fmt.Sprintf("Stripe event identity conflict id=%s type=%s object=%s", event.ID, event.Type, objectID))
			c.JSON(http.StatusConflict, stripeCallbackResponse{Status: "error: event conflict"})
			return
		}
		globals.Error(fmt.Sprintf("Stripe event receipt failed id=%s type=%s object=%s", event.ID, event.Type, objectID))
		c.JSON(http.StatusServiceUnavailable, stripeCallbackResponse{Status: "error: receipt failed"})
		return
	}

	result, processErr := eventService.ProcessEvent(
		c.Request.Context(),
		ingested.Event.Id,
		stripeProviderEventProcessorFactory(),
	)
	if processErr != nil {
		globals.Warn(fmt.Sprintf(
			"Stripe event durably accepted id=%s type=%s object=%s status=%s",
			event.ID, event.Type, objectID, result.Status,
		))
	} else {
		globals.Info(fmt.Sprintf(
			"Stripe event handled id=%s type=%s object=%s status=%s replay=%t",
			event.ID, event.Type, objectID, result.Status, ingested.Replay || result.Replay,
		))
	}
	c.JSON(http.StatusOK, stripeCallbackResponse{Status: "success"})
}

func validateStripeMode(mode string, liveMode bool) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "test":
		if liveMode {
			return fmt.Errorf("live Stripe event reached a test endpoint")
		}
	case "live":
		if !liveMode {
			return fmt.Errorf("test Stripe event reached a live endpoint")
		}
	default:
		return fmt.Errorf("unsupported Stripe mode")
	}
	return nil
}
