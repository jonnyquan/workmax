package callback

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"server/globals"
	"server/model"
	"server/service/account"
	"server/service/commerce"

	"github.com/stripe/stripe-go/v80"
	"gorm.io/gorm"
)

const stripeProviderName = "stripe"

// StripeProviderEventProcessor projects a signature-verified, durable Stripe
// payload into provider-neutral Account mutations. Provider reads happen in
// Prepare; the returned Apply closure performs only database work inside the
// Inbox owner's transaction.
type StripeProviderEventProcessor struct {
	accountService account.AccountService
	sendPaidOrder  func(model.Order, int64, string)
	sendRenewal    func(model.Order, int64, string, time.Time, string)
}

func NewStripeProviderEventProcessor() *StripeProviderEventProcessor {
	processor := &StripeProviderEventProcessor{}
	processor.sendPaidOrder = processor.accountService.SendPaidOrderConfirmationAsync
	processor.sendRenewal = processor.accountService.SendSubscriptionRenewalConfirmationAsync
	return processor
}

var stripeProviderEventProcessorFactory = func() commerce.ProviderEventProcessor {
	return NewStripeProviderEventProcessor()
}

func (processor *StripeProviderEventProcessor) Prepare(
	ctx context.Context,
	snapshot commerce.ProviderEventSnapshot,
) (commerce.PreparedEvent, error) {
	if err := ctx.Err(); err != nil {
		return commerce.PreparedEvent{}, err
	}
	var event stripe.Event
	if err := json.Unmarshal(snapshot.Payload, &event); err != nil {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("stripe_payload_invalid", err)
	}
	objectID, err := stripeEventObjectID(event)
	if err != nil {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("stripe_object_invalid", err)
	}
	if err := validateStripeSnapshot(snapshot, event, objectID); err != nil {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("stripe_snapshot_conflict", err)
	}

	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		return processor.prepareCheckoutSession(ctx, event)
	case stripe.EventTypeInvoicePaymentSucceeded:
		return processor.preparePaidInvoice(ctx, event)
	case stripe.EventTypePaymentIntentSucceeded:
		return ignoredStripeEvent("payment_intent_observed"), nil
	default:
		return ignoredStripeEvent("unsupported_stripe_event"), nil
	}
}

func (processor *StripeProviderEventProcessor) prepareCheckoutSession(
	ctx context.Context,
	event stripe.Event,
) (commerce.PreparedEvent, error) {
	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("checkout_payload_invalid", err)
	}
	if strings.TrimSpace(session.ID) == "" {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("checkout_identity_missing", fmt.Errorf("checkout session id is required"))
	}
	orderNo := strings.TrimSpace(session.ClientReferenceID)
	if orderNo == "" {
		return ignoredStripeEvent("foreign_checkout_session"), nil
	}
	if session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		return commerce.PreparedEvent{}, commerce.ManualReviewError(
			"checkout_not_paid",
			fmt.Errorf("owned checkout session is not settled"),
		)
	}

	command := account.PaidOrderCommand{
		OrderNo:           orderNo,
		AmountTotal:       session.AmountTotal,
		CustomerSummary:   stripeCheckoutCustomerSummary(session.CustomerDetails),
		CheckoutSessionID: session.ID,
	}
	currency := strings.ToLower(strings.TrimSpace(string(session.Currency)))
	switch session.Mode {
	case stripe.CheckoutSessionModePayment:
		command.OrderMode = model.ORDER_MODE_ONE_TIME
		if session.PaymentIntent == nil || strings.TrimSpace(session.PaymentIntent.ID) == "" {
			return commerce.PreparedEvent{}, commerce.ManualReviewError(
				"payment_intent_missing",
				fmt.Errorf("one-time checkout has no payment intent"),
			)
		}
		command.TransactionID = session.PaymentIntent.ID
		chargeID, err := loadOneTimeCheckoutPaymentSnapshot(
			ctx,
			command.TransactionID,
			command.OrderNo,
			command.AmountTotal,
			currency,
		)
		if err != nil {
			return commerce.PreparedEvent{}, commerce.RetryableError("payment_snapshot_unavailable", err)
		}
		command.ChargeID = chargeID
	case stripe.CheckoutSessionModeSubscription:
		command.OrderMode = model.ORDER_MODE_SUBSCRIPTION
		if session.Subscription == nil || strings.TrimSpace(session.Subscription.ID) == "" ||
			session.Invoice == nil || strings.TrimSpace(session.Invoice.ID) == "" {
			return commerce.PreparedEvent{}, commerce.ManualReviewError(
				"subscription_identity_missing",
				fmt.Errorf("subscription checkout has no subscription or invoice identity"),
			)
		}
		command.SubscriptionID = session.Subscription.ID
		command.TransactionID = session.Subscription.ID
		command.InvoiceID = session.Invoice.ID
		priceID, periodStart, periodEnd, err := loadRecurringCheckoutInvoiceSnapshot(
			ctx,
			command.OrderNo,
			command.CheckoutSessionID,
			command.InvoiceID,
			command.SubscriptionID,
		)
		if err != nil {
			return commerce.PreparedEvent{}, commerce.RetryableError("invoice_snapshot_unavailable", err)
		}
		command.ProviderPriceID = priceID
		command.BillingPeriodStart = periodStart
		command.BillingPeriodEnd = periodEnd
	default:
		return commerce.PreparedEvent{}, commerce.ManualReviewError(
			"checkout_mode_unsupported",
			fmt.Errorf("unsupported checkout mode %q", session.Mode),
		)
	}

	return commerce.PreparedEvent{Apply: func(
		applyContext context.Context,
		tx *gorm.DB,
		now time.Time,
	) (commerce.EventOutcome, error) {
		if err := applyContext.Err(); err != nil {
			return commerce.EventOutcome{}, err
		}
		order, applied, err := processor.accountService.ApplyPaidOrderTx(tx, command, now)
		if err != nil {
			return commerce.EventOutcome{}, err
		}
		if !applied {
			return commerce.EventOutcome{
				Status: model.CommerceProviderEventStatusProcessed,
				Kind:   "checkout_paid_replay",
			}, nil
		}
		payload, err := commerceOrderOutboxPayload(event, "checkout_paid", order)
		if err != nil {
			return commerce.EventOutcome{}, err
		}
		return commerce.EventOutcome{
			Status: model.CommerceProviderEventStatusProcessed,
			Kind:   "checkout_paid",
			Outbox: []commerce.OutboxDraft{{
				Topic:     "commerce.order.completed.v1",
				DedupeKey: commerce.SHA256Key("commerce-order-completed-v1", order.No),
				Payload:   payload,
			}},
			AfterCommit: func() {
				if processor.sendPaidOrder != nil {
					processor.sendPaidOrder(order, command.AmountTotal, currency)
				}
			},
		}, nil
	}}, nil
}

func (processor *StripeProviderEventProcessor) preparePaidInvoice(
	ctx context.Context,
	event stripe.Event,
) (commerce.PreparedEvent, error) {
	var invoice stripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("invoice_payload_invalid", err)
	}
	if strings.TrimSpace(invoice.ID) == "" {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("invoice_identity_missing", fmt.Errorf("invoice id is required"))
	}
	if invoice.Status != stripe.InvoiceStatusPaid {
		return commerce.PreparedEvent{}, commerce.ManualReviewError("invoice_not_paid", fmt.Errorf("invoice is not paid"))
	}
	billingReason := strings.TrimSpace(string(invoice.BillingReason))
	switch billingReason {
	case "subscription_create":
		return ignoredStripeEvent("subscription_create_invoice"), nil
	case "subscription_cycle", "subscription_update":
	default:
		return ignoredStripeEvent("unsupported_invoice_reason"), nil
	}
	if invoice.Subscription == nil || strings.TrimSpace(invoice.Subscription.ID) == "" {
		return commerce.PreparedEvent{}, commerce.ManualReviewError(
			"invoice_subscription_missing",
			fmt.Errorf("paid recurring invoice has no subscription identity"),
		)
	}
	subscriptionID := invoice.Subscription.ID
	reader := newStripeInvoiceReader(globals.GraConf.Stripe.PrivateKey)
	if err := completeInvoiceLines(ctx, &invoice, reader); err != nil {
		return commerce.PreparedEvent{}, commerce.RetryableError("invoice_lines_unavailable", err)
	}
	expectedPriceID := ""
	if billingReason == "subscription_cycle" {
		priceID, err := processor.accountService.CurrentSubscriptionProviderPriceContext(ctx, subscriptionID)
		if err != nil {
			return commerce.PreparedEvent{}, commerce.RetryableError("subscription_owner_unavailable", err)
		}
		expectedPriceID = priceID
	}
	line, err := selectRecurringInvoiceLine(&invoice, subscriptionID, expectedPriceID)
	if err != nil {
		return commerce.PreparedEvent{}, commerce.RetryableError("invoice_line_ambiguous", err)
	}
	command := account.SubscriptionInvoiceCommand{
		CustomerDetails:    stripeInvoiceCustomerSummary(invoice),
		ProviderPriceID:    line.Price.ID,
		InvoiceID:          invoice.ID,
		TransactionID:      stripeInvoiceChargeID(invoice),
		SubscriptionID:     subscriptionID,
		AmountPaidCents:    invoice.AmountPaid,
		BillingPeriodStart: time.Unix(line.Period.Start, 0).UTC(),
		BillingPeriodEnd:   time.Unix(line.Period.End, 0).UTC(),
		BillingReason:      billingReason,
	}
	currency := strings.ToLower(strings.TrimSpace(string(invoice.Currency)))
	paymentMethod := stripeInvoicePaymentMethod(invoice)

	return commerce.PreparedEvent{Apply: func(
		applyContext context.Context,
		tx *gorm.DB,
		now time.Time,
	) (commerce.EventOutcome, error) {
		if err := applyContext.Err(); err != nil {
			return commerce.EventOutcome{}, err
		}
		order, applied, err := processor.accountService.ApplySubscriptionInvoiceTx(tx, command, now)
		if err != nil {
			return commerce.EventOutcome{}, err
		}
		if !applied {
			return commerce.EventOutcome{
				Status: model.CommerceProviderEventStatusProcessed,
				Kind:   "subscription_invoice_replay",
			}, nil
		}
		payload, err := commerceOrderOutboxPayload(event, "subscription_invoice_paid", order)
		if err != nil {
			return commerce.EventOutcome{}, err
		}
		return commerce.EventOutcome{
			Status: model.CommerceProviderEventStatusProcessed,
			Kind:   "subscription_invoice_paid",
			Outbox: []commerce.OutboxDraft{{
				Topic:     "commerce.order.completed.v1",
				DedupeKey: commerce.SHA256Key("commerce-subscription-invoice-v1", command.InvoiceID),
				Payload:   payload,
			}},
			AfterCommit: func() {
				if processor.sendRenewal != nil {
					processor.sendRenewal(
						order,
						command.AmountPaidCents,
						currency,
						command.BillingPeriodEnd,
						paymentMethod,
					)
				}
			},
		}, nil
	}}, nil
}

func ignoredStripeEvent(kind string) commerce.PreparedEvent {
	return commerce.PreparedEvent{Apply: func(context.Context, *gorm.DB, time.Time) (commerce.EventOutcome, error) {
		return commerce.EventOutcome{
			Status: model.CommerceProviderEventStatusIgnored,
			Kind:   kind,
		}, nil
	}}
}

func validateStripeSnapshot(snapshot commerce.ProviderEventSnapshot, event stripe.Event, objectID string) error {
	createdAt := time.Unix(event.Created, 0).UTC()
	if snapshot.Provider != stripeProviderName ||
		snapshot.ProviderAccountID != stripeProviderAccount(event.Account) ||
		snapshot.ProviderAPIVersion != event.APIVersion ||
		snapshot.EventID != event.ID || snapshot.EventType != string(event.Type) ||
		snapshot.ObjectID != objectID || snapshot.LiveMode != event.Livemode ||
		snapshot.ProviderCreatedAt == nil || !snapshot.ProviderCreatedAt.Equal(createdAt) {
		return fmt.Errorf("durable Stripe identity does not match its payload")
	}
	return nil
}

func stripeEventObjectID(event stripe.Event) (string, error) {
	if event.Object != "event" || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(string(event.Type)) == "" ||
		event.Created <= 0 || event.Data == nil || len(event.Data.Raw) == 0 {
		return "", fmt.Errorf("Stripe event envelope is incomplete")
	}
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data.Raw, &object); err != nil {
		return "", fmt.Errorf("parse Stripe event object identity: %w", err)
	}
	object.ID = strings.TrimSpace(object.ID)
	if object.ID == "" {
		return "", fmt.Errorf("Stripe event object id is required")
	}
	return object.ID, nil
}

func stripeProviderAccount(accountID string) string {
	if accountID = strings.TrimSpace(accountID); accountID != "" {
		return accountID
	}
	return "platform"
}

func stripeCheckoutCustomerSummary(details *stripe.CheckoutSessionCustomerDetails) string {
	if details == nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(details.Name) + " " + strings.TrimSpace(details.Email))
}

func stripeInvoiceCustomerSummary(invoice stripe.Invoice) string {
	return strings.TrimSpace(strings.TrimSpace(invoice.CustomerEmail) + " " + strings.TrimSpace(invoice.CustomerName))
}

func stripeInvoiceChargeID(invoice stripe.Invoice) string {
	if invoice.Charge == nil {
		return ""
	}
	return strings.TrimSpace(invoice.Charge.ID)
}

func stripeInvoicePaymentMethod(invoice stripe.Invoice) string {
	if invoice.Charge == nil || invoice.Charge.PaymentMethodDetails == nil {
		return ""
	}
	details := invoice.Charge.PaymentMethodDetails
	if details.Card != nil && details.Card.Brand != "" {
		brand := strings.TrimSpace(string(details.Card.Brand))
		if brand != "" {
			brand = strings.ToUpper(brand[:1]) + brand[1:]
		}
		return strings.TrimSpace(fmt.Sprintf("%s ****%s", brand, details.Card.Last4))
	}
	return strings.TrimSpace(string(details.Type))
}

type commerceOrderEventPayload struct {
	SchemaVersion   int    `json:"schemaVersion"`
	Provider        string `json:"provider"`
	ProviderEventID string `json:"providerEventId"`
	EventType       string `json:"eventType"`
	Outcome         string `json:"outcome"`
	OrderNo         string `json:"orderNo"`
	UID             int    `json:"uid"`
}

func commerceOrderOutboxPayload(event stripe.Event, outcome string, order model.Order) (json.RawMessage, error) {
	payload, err := json.Marshal(commerceOrderEventPayload{
		SchemaVersion:   1,
		Provider:        stripeProviderName,
		ProviderEventID: event.ID,
		EventType:       string(event.Type),
		Outcome:         outcome,
		OrderNo:         order.No,
		UID:             order.UID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode commerce order outbox: %w", err)
	}
	return payload, nil
}
