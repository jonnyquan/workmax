package account

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v80"
	"gorm.io/gorm"
)

type fixedStripeSubscriptionClient struct {
	result            *stripe.Subscription
	err               error
	updates           int
	wantCancelAtEnd   bool
	lastSubscription  string
	configuredKeySeen string
}

func (client *fixedStripeSubscriptionClient) Get(_ string, _ *stripe.SubscriptionParams) (*stripe.Subscription, error) {
	return client.result, client.err
}

func (client *fixedStripeSubscriptionClient) Update(id string, params *stripe.SubscriptionParams) (*stripe.Subscription, error) {
	client.updates++
	client.lastSubscription = id
	if params == nil || params.CancelAtPeriodEnd == nil || *params.CancelAtPeriodEnd != client.wantCancelAtEnd {
		return nil, &unexpectedSubscriptionUpdateError{}
	}
	return client.result, client.err
}

type unexpectedSubscriptionUpdateError struct{}

func (*unexpectedSubscriptionUpdateError) Error() string { return "unexpected subscription update" }

func subscriptionStateRouter(uid uint, path string, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(path, func(c *gin.Context) {
		c.Set("claims", &systemReq.CustomClaims{BaseClaims: systemReq.BaseClaims{Id: uid}})
		handler(c)
	})
	return router
}

func installSubscriptionStateTestGlobals(t *testing.T, db *gorm.DB, client *fixedStripeSubscriptionClient) {
	t.Helper()
	previousDBs := globals.GraDBs
	previousConfig := globals.GraConf
	previousFactory := newStripeSubscriptionClient
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	globals.GraConf.Stripe.PrivateKey = "sk_test_subscription_state"
	newStripeSubscriptionClient = func(privateKey string) stripeSubscriptionClient {
		client.configuredKeySeen = privateKey
		return client
	}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
		globals.GraConf = previousConfig
		newStripeSubscriptionClient = previousFactory
	})
}

func TestReactivateSubscription_DBFailureAfterProviderSuccessReturns5xx(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := model.User{
		Email: "reactivate-owner@example.com", Nickname: "reactivate-owner",
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberSubscription: "canceled_sub_reactivate_exact",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_subscription_identity_update
		BEFORE UPDATE OF member_subscription ON w_user
		WHEN OLD.member_subscription LIKE 'reactivating_%'
		BEGIN SELECT RAISE(ABORT, 'forced subscription identity failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	client := &fixedStripeSubscriptionClient{
		result: &stripe.Subscription{ID: "sub_reactivate_exact", CancelAtPeriodEnd: false},
	}
	installSubscriptionStateTestGlobals(t, db, client)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/reactivate", nil)
	subscriptionStateRouter(user.Id, "/reactivate", (&StripeApi{}).ReactivateSubscription).
		ServeHTTP(recorder, request)
	if recorder.Code < 500 {
		t.Fatalf("reactivation DB failure status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if client.updates != 1 || client.lastSubscription != "sub_reactivate_exact" ||
		client.configuredKeySeen != globals.GraConf.Stripe.PrivateKey {
		t.Fatalf("provider update = calls:%d id:%q key:%q", client.updates, client.lastSubscription, client.configuredKeySeen)
	}
	var unchanged model.User
	if err := db.First(&unchanged, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.MemberSubscription != "reactivating_sub_reactivate_exact" {
		t.Fatalf("failed local commit changed identity to %q", unchanged.MemberSubscription)
	}
	if _, deletionErr := stageAccountDeletionIntent(db, user.Id, deleteAccountRequest{
		Confirmation: user.Email,
	}, time.Now()); !errors.Is(deletionErr, errAccountDeletionBilling) {
		t.Fatalf("pending reactivation deletion error = %v", deletionErr)
	}

	if err := db.Exec("DROP TRIGGER fail_subscription_identity_update").Error; err != nil {
		t.Fatal(err)
	}
	if err := updateSubscriptionIdentityCAS(
		db, user.Id, "reactivating_sub_reactivate_exact", "sub_reactivate_exact", time.Now(),
	); err != nil {
		t.Fatalf("repair after provider success: %v", err)
	}
	if err := updateSubscriptionIdentityCAS(
		db, user.Id, "reactivating_sub_reactivate_exact", "sub_reactivate_exact", time.Now(),
	); err != nil {
		t.Fatalf("exact local repair replay: %v", err)
	}
}

func TestCancelSubscription_DBFailureAfterProviderSuccessReturns5xx(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := model.User{
		Email: "cancel-owner@example.com", Nickname: "cancel-owner",
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberSubscription: "sub_cancel_exact",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_subscription_identity_update
		BEFORE UPDATE OF member_subscription ON w_user
		WHEN OLD.member_subscription LIKE 'canceling_%'
		BEGIN SELECT RAISE(ABORT, 'forced subscription identity failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	client := &fixedStripeSubscriptionClient{
		result:          &stripe.Subscription{ID: "sub_cancel_exact", CancelAtPeriodEnd: true},
		wantCancelAtEnd: true,
	}
	installSubscriptionStateTestGlobals(t, db, client)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/cancel", nil)
	subscriptionStateRouter(user.Id, "/cancel", (&StripeApi{}).CancelSubscription).
		ServeHTTP(recorder, request)
	if recorder.Code < 500 {
		t.Fatalf("cancellation DB failure status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if client.updates != 1 || client.lastSubscription != "sub_cancel_exact" {
		t.Fatalf("provider cancellation = calls:%d id:%q", client.updates, client.lastSubscription)
	}
	var unchanged model.User
	if err := db.First(&unchanged, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.MemberSubscription != "canceling_sub_cancel_exact" {
		t.Fatalf("failed local commit changed identity to %q", unchanged.MemberSubscription)
	}
}

func TestRegisterFreeSubscription_BlocksPlainProviderOwnerWithStaleLocalExpiry(t *testing.T) {
	db := testutil.NewTestDB(t)
	client := &fixedStripeSubscriptionClient{}
	installSubscriptionStateTestGlobals(t, db, client)
	user := model.User{
		Email: "free-stale-provider@example.com", Nickname: "free-stale-provider",
		Member: model.MEMBER_SUBSCRIPTION_PRO, MemberSubscription: "sub_provider_may_bill",
		MemberEndTime: time.Now().Add(-time.Hour),
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost, "/free", bytes.NewReader([]byte(`{"billingCycle":"monthly"}`)),
	)
	request.Header.Set("Content-Type", "application/json")
	subscriptionStateRouter(user.Id, "/free", (&StripeApi{}).RegisterFreeSubscription).
		ServeHTTP(recorder, request)
	if !strings.Contains(recorder.Body.String(), `"code":0`) {
		t.Fatalf("stale provider free registration response = %d %s", recorder.Code, recorder.Body.String())
	}
	var orders int64
	if err := db.Model(&model.Order{}).Where("uid = ?", user.Id).Count(&orders).Error; err != nil {
		t.Fatal(err)
	}
	if orders != 0 {
		t.Fatalf("blocked free registration created %d Orders", orders)
	}
	var unchanged model.User
	if err := db.First(&unchanged, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Member != model.MEMBER_SUBSCRIPTION_PRO || unchanged.MemberSubscription != "sub_provider_may_bill" {
		t.Fatalf("blocked free registration mutated user: %#v", unchanged)
	}
}
