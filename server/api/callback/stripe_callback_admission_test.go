package callback

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server/globals"
	"server/model"
	"server/service/commerce"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	"github.com/stripe/stripe-go/v80/webhook"
	"gorm.io/gorm"
)

func TestStripeCallbackAdmissionRejectsMissingSignatureAndOversizeBody(t *testing.T) {
	previous := globals.GraConf
	globals.GraConf.Stripe.EndpointSecret = "whsec_admission"
	t.Cleanup(func() { globals.GraConf = previous })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/stripe", (&StripeCallbackApi{}).StripeCallback)

	missingSignature := httptest.NewRequest(http.MethodPost, "/stripe", strings.NewReader(`{"id":"evt"}`))
	missingRecorder := httptest.NewRecorder()
	router.ServeHTTP(missingRecorder, missingSignature)
	if missingRecorder.Code != http.StatusBadRequest {
		t.Fatalf("missing signature status = %d, body=%s", missingRecorder.Code, missingRecorder.Body.String())
	}

	oversize := bytes.Repeat([]byte("x"), commerce.MaxProviderPayloadBytes+1)
	oversizeRequest := httptest.NewRequest(http.MethodPost, "/stripe", bytes.NewReader(oversize))
	oversizeRequest.Header.Set("Stripe-Signature", "present-but-body-is-rejected-first")
	oversizeRecorder := httptest.NewRecorder()
	router.ServeHTTP(oversizeRecorder, oversizeRequest)
	if oversizeRecorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status = %d, body=%s", oversizeRecorder.Code, oversizeRecorder.Body.String())
	}
}

func TestStripeCallbackAdmissionRejectsModeMismatchBeforeInbox(t *testing.T) {
	previousConfig := globals.GraConf
	previousDBs := globals.GraDBs
	db := testutil.NewTestDB(t)
	const secret = "whsec_mode_mismatch"
	globals.GraConf.Stripe.EndpointSecret = secret
	globals.GraConf.Stripe.Mode = "test"
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraConf = previousConfig
		globals.GraDBs = previousDBs
	})

	payload := stripeAdmissionPayload("evt_live_mismatch", "customer.created", "cus_live_mismatch", true)
	recorder := performSignedStripeCallback(t, payload, secret)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("mode mismatch status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var count int64
	if err := db.Model(&model.CommerceProviderEvent{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("mode mismatch inbox rows = %d, want 0", count)
	}
}

func TestStripeCallbackDurablyIgnoresUnsupportedSignedEvent(t *testing.T) {
	previousConfig := globals.GraConf
	previousDBs := globals.GraDBs
	db := testutil.NewTestDB(t)
	const secret = "whsec_ignored_event"
	globals.GraConf.Stripe.EndpointSecret = secret
	globals.GraConf.Stripe.Mode = "test"
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraConf = previousConfig
		globals.GraDBs = previousDBs
	})

	payload := stripeAdmissionPayload("evt_customer_created", "customer.created", "cus_ignored", false)
	for replay := 0; replay < 2; replay++ {
		recorder := performSignedStripeCallback(t, payload, secret)
		if recorder.Code != http.StatusOK {
			t.Fatalf("ignored replay %d status = %d, body=%s", replay, recorder.Code, recorder.Body.String())
		}
	}
	var event model.CommerceProviderEvent
	if err := db.Where("event_id = ?", "evt_customer_created").First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Status != model.CommerceProviderEventStatusIgnored || event.OutcomeKind != "unsupported_stripe_event" || event.AttemptCount != 1 {
		t.Fatalf("ignored durable event = %+v", event)
	}
	var inboxCount, outboxCount int64
	if err := db.Model(&model.CommerceProviderEvent{}).Count(&inboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.CommerceOutbox{}).Count(&outboxCount).Error; err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || outboxCount != 0 {
		t.Fatalf("ignored durable counts inbox/outbox = %d/%d", inboxCount, outboxCount)
	}
}

func stripeAdmissionPayload(eventID, eventType, objectID string, live bool) []byte {
	return []byte(fmt.Sprintf(
		`{"id":%q,"object":"event","api_version":%q,"created":1786032000,"livemode":%t,"type":%q,"data":{"object":{"id":%q,"object":"test_object"}}}`,
		eventID,
		stripe.APIVersion,
		live,
		eventType,
		objectID,
	))
}

func performSignedStripeCallback(t *testing.T, payload []byte, secret string) *httptest.ResponseRecorder {
	t.Helper()
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   payload,
		Secret:    secret,
		Timestamp: time.Now(),
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/stripe", (&StripeCallbackApi{}).StripeCallback)
	request := httptest.NewRequest(http.MethodPost, "/stripe", bytes.NewReader(signed.Payload))
	request.Header.Set("Stripe-Signature", signed.Header)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
