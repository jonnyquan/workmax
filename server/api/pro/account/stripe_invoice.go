package account

import (
	"fmt"
	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/utils"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/charge"
	"github.com/stripe/stripe-go/v80/invoice"
)

type stripeInvoiceGetter interface {
	Get(string, *stripe.InvoiceParams) (*stripe.Invoice, error)
}

type stripeChargeGetter interface {
	Get(string, *stripe.ChargeParams) (*stripe.Charge, error)
}

var newStripeInvoiceGetter = func(privateKey string) stripeInvoiceGetter {
	return invoice.Client{B: stripe.GetBackend(stripe.APIBackend), Key: privateKey}
}

var newStripeChargeGetter = func(privateKey string) stripeChargeGetter {
	return charge.Client{B: stripe.GetBackend(stripe.APIBackend), Key: privateKey}
}

// GetInvoiceUrl 获取Stripe发票或收据URL
// 支持两种情况：
// 1. subscription模式：使用invoiceId获取invoice URL
// 2. payment模式：使用chargeId获取receipt URL
func (a *StripeApi) GetInvoiceUrl(c *gin.Context) {
	invoiceId := strings.TrimSpace(c.Query("invoiceId"))
	chargeId := strings.TrimSpace(c.Query("chargeId"))

	if (invoiceId == "") == (chargeId == "") {
		response.FailWithMessage("Exactly one Invoice ID or Charge ID is required", c)
		return
	}
	uid := utils.GetUserID(c)
	if uid == 0 {
		response.FailWithMessage("Authenticated order owner is required", c)
		return
	}

	stripeConfig := globals.GraConf.Stripe

	// Priority 1: Try invoice (for subscription)
	if invoiceId != "" {
		var order model.Order
		if err := globals.GraDBs["system"].
			Select("id", "uid", "invoice").
			Where("uid = ? AND status = ? AND invoice_idempotency_key = ?", uid, model.STATUS_COMPLETE, invoiceId).
			First(&order).Error; err != nil {
			response.FailWithMessage("Invoice not found for the current user", c)
			return
		}
		inv, err := newStripeInvoiceGetter(stripeConfig.PrivateKey).Get(invoiceId, nil)
		if err != nil {
			globals.Error(fmt.Sprintf("Failed to get invoice: %v", err))
			response.FailWithDetailed(err.Error(), "Failed to retrieve invoice", c)
			return
		}
		if inv == nil || inv.ID != invoiceId {
			response.FailWithMessage("Invoice ownership could not be verified", c)
			return
		}

		if inv.HostedInvoiceURL == "" {
			response.FailWithMessage("Invoice URL not available", c)
			return
		}

		response.OkWithData(gin.H{
			"invoiceUrl": inv.HostedInvoiceURL,
			"type":       "invoice",
		}, c)
		return
	}

	// Priority 2: Try charge receipt (for one-time payment)
	if chargeId != "" {
		var order model.Order
		if err := globals.GraDBs["system"].
			Select("id", "uid", "charge_id").
			Where("uid = ? AND status = ? AND charge_id = ?", uid, model.STATUS_COMPLETE, chargeId).
			First(&order).Error; err != nil {
			response.FailWithMessage("Receipt not found for the current user", c)
			return
		}
		ch, err := newStripeChargeGetter(stripeConfig.PrivateKey).Get(chargeId, nil)
		if err != nil {
			globals.Error(fmt.Sprintf("Failed to get charge: %v", err))
			response.FailWithDetailed(err.Error(), "Failed to retrieve receipt", c)
			return
		}
		if ch == nil || ch.ID != chargeId {
			response.FailWithMessage("Receipt ownership could not be verified", c)
			return
		}

		if ch.ReceiptURL == "" {
			response.FailWithMessage("Receipt URL not available", c)
			return
		}

		response.OkWithData(gin.H{
			"invoiceUrl": ch.ReceiptURL, // Using same field name for consistency
			"type":       "receipt",
		}, c)
		return
	}
}
