package account

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	"gorm.io/gorm"
)

type fixedStripeInvoiceGetter struct {
	invoice *stripe.Invoice
	err     error
	calls   *int
}

func (getter *fixedStripeInvoiceGetter) Get(_ string, _ *stripe.InvoiceParams) (*stripe.Invoice, error) {
	*getter.calls++
	return getter.invoice, getter.err
}

type fixedStripeChargeGetter struct {
	charge *stripe.Charge
	err    error
	calls  *int
}

func (getter *fixedStripeChargeGetter) Get(_ string, _ *stripe.ChargeParams) (*stripe.Charge, error) {
	*getter.calls++
	return getter.charge, getter.err
}

func invoiceURLTestRouter(uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/invoice", func(c *gin.Context) {
		c.Set("claims", &systemReq.CustomClaims{BaseClaims: systemReq.BaseClaims{Id: uid}})
		(&StripeApi{}).GetInvoiceUrl(c)
	})
	return router
}

func installInvoiceURLTestGlobals(t *testing.T, db *gorm.DB) {
	t.Helper()
	previousDBs := globals.GraDBs
	previousConfig := globals.GraConf
	previousInvoiceFactory := newStripeInvoiceGetter
	previousChargeFactory := newStripeChargeGetter
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe.PrivateKey = "sk_test_receipt_owner"
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
		globals.GraConf = previousConfig
		newStripeInvoiceGetter = previousInvoiceFactory
		newStripeChargeGetter = previousChargeFactory
	})
}

func TestGetInvoiceURL_RequiresLocalCompletedOrderOwnerBeforeProviderLookup(t *testing.T) {
	db := testutil.NewTestDB(t)
	installInvoiceURLTestGlobals(t, db)
	owner := model.User{Email: "receipt-owner@example.com", Nickname: "receipt-owner"}
	attacker := model.User{Email: "receipt-attacker@example.com", Nickname: "receipt-attacker"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&attacker).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Order{
		UID: int(owner.Id), No: "receipt-owner-invoice", Status: model.STATUS_COMPLETE,
		Invoice: "in_owner_exact",
	}).Error; err != nil {
		t.Fatal(err)
	}

	providerCalls := 0
	boundKey := ""
	newStripeInvoiceGetter = func(privateKey string) stripeInvoiceGetter {
		boundKey = privateKey
		return &fixedStripeInvoiceGetter{
			invoice: &stripe.Invoice{ID: "in_owner_exact", HostedInvoiceURL: "https://stripe.invalid/in_owner_exact"},
			calls:   &providerCalls,
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/invoice?invoiceId=in_owner_exact", nil)
	denied := httptest.NewRecorder()
	invoiceURLTestRouter(attacker.Id).ServeHTTP(denied, request)
	if providerCalls != 0 {
		t.Fatalf("cross-owner request reached Stripe %d times", providerCalls)
	}
	if strings.Contains(denied.Body.String(), "https://stripe.invalid") {
		t.Fatalf("cross-owner response leaked invoice URL: %s", denied.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/invoice?invoiceId=in_owner_exact", nil)
	allowed := httptest.NewRecorder()
	invoiceURLTestRouter(owner.Id).ServeHTTP(allowed, request)
	if providerCalls != 1 || boundKey != globals.GraConf.Stripe.PrivateKey {
		t.Fatalf("provider calls/key = %d/%q", providerCalls, boundKey)
	}
	if !strings.Contains(allowed.Body.String(), "https://stripe.invalid/in_owner_exact") {
		t.Fatalf("owner response = %s", allowed.Body.String())
	}
}

func TestGetInvoiceURL_ChargeRequiresExactProviderIdentityAndSingleLookupKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	installInvoiceURLTestGlobals(t, db)
	owner := model.User{Email: "charge-owner@example.com", Nickname: "charge-owner"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Order{
		UID: int(owner.Id), No: "receipt-owner-charge", Status: model.STATUS_COMPLETE,
		ChargeID: "ch_owner_exact",
	}).Error; err != nil {
		t.Fatal(err)
	}

	providerCalls := 0
	newStripeChargeGetter = func(privateKey string) stripeChargeGetter {
		if privateKey != globals.GraConf.Stripe.PrivateKey {
			t.Fatalf("charge client key = %q", privateKey)
		}
		return &fixedStripeChargeGetter{
			charge: &stripe.Charge{ID: "ch_different", ReceiptURL: "https://stripe.invalid/wrong"},
			calls:  &providerCalls,
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/invoice?chargeId=ch_owner_exact", nil)
	recorder := httptest.NewRecorder()
	invoiceURLTestRouter(owner.Id).ServeHTTP(recorder, request)
	if providerCalls != 1 || strings.Contains(recorder.Body.String(), "https://stripe.invalid/wrong") {
		t.Fatalf("mismatched provider identity response/calls = %s/%d", recorder.Body.String(), providerCalls)
	}

	request = httptest.NewRequest(http.MethodGet, "/invoice?invoiceId=in_any&chargeId=ch_owner_exact", nil)
	recorder = httptest.NewRecorder()
	invoiceURLTestRouter(owner.Id).ServeHTTP(recorder, request)
	if providerCalls != 1 {
		t.Fatalf("ambiguous lookup reached provider; calls=%d", providerCalls)
	}
}
