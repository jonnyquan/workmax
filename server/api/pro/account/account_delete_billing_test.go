package account

import (
	"errors"
	"testing"
	"time"

	"server/globals"
	"server/model"
	accountsvc "server/service/account"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func installDeleteBillingTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
}

func TestStageAccountDeletionIntent_RejectsOpenStripeCheckout(t *testing.T) {
	db := testutil.NewTestDB(t)
	user := model.User{Email: "delete-open-checkout@example.com", Nickname: "delete-open-checkout"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.Order{
		UID: int(user.Id), No: "ORDER-delete-open-checkout", Status: model.STATUS_UNPAID,
		PayMethod: "stripe", ProductID: "credits_50", Name: "50 Credits",
		OrderMode: model.ORDER_MODE_ONE_TIME, OrderType: model.ORDER_TYPE_CREDITS,
		Amount: 999, CreditsAmount: 50, ProviderPriceID: "price_credits_50",
		CheckoutSessionID: "cs_delete_open_checkout",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	_, err := stageAccountDeletionIntent(db, user.Id, deleteAccountRequest{
		Confirmation: user.Email,
	}, time.Now())
	if !errors.Is(err, errAccountDeletionBilling) {
		t.Fatalf("open Checkout deletion error = %v", err)
	}
	var unchanged model.User
	if err := db.First(&unchanged, user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Ban {
		t.Fatal("rejected deletion left a deletion intent")
	}
}

func TestAccountDeletionIntent_BlocksNewCheckoutAndUnpaidSettlement(t *testing.T) {
	db := testutil.NewTestDB(t)
	installDeleteBillingTestDB(t, db)
	user := model.User{Email: "delete-fence@example.com", Nickname: "delete-fence"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	intent, err := stageAccountDeletionIntent(db, user.Id, deleteAccountRequest{
		Confirmation: user.Email,
	}, time.Now())
	if err != nil || !intent.NewlyStaged {
		t.Fatalf("stage deletion intent = %#v, %v", intent, err)
	}

	creditSpec := checkoutOrderSpec{
		ProductID: "credits_50", Name: "50 Credits", ProviderPriceID: "price_credits_50",
		StripeMode: "payment", OrderMode: model.ORDER_MODE_ONE_TIME,
		OrderType: model.ORDER_TYPE_CREDITS, Amount: 999, CreditsAmount: 50,
	}
	if _, err := ensureCheckoutOrder(db, user.Id, creditSpec, time.Now()); err == nil {
		t.Fatal("deletion intent admitted a new Checkout Order")
	}

	// Seed a provider-owned row directly to emulate an already-delivered event
	// against drifted history. PayOrder must still reject before COMPLETE/Pack.
	order := model.Order{
		UID: int(user.Id), No: "ORDER-delete-fenced-payment", Status: model.STATUS_UNPAID,
		PayMethod: "stripe", ProductID: "credits_50", Name: "50 Credits",
		OrderMode: model.ORDER_MODE_ONE_TIME, OrderType: model.ORDER_TYPE_CREDITS,
		Amount: 999, CreditsAmount: 50, ProviderPriceID: "price_credits_50",
		CheckoutSessionID: "cs_delete_fenced_payment",
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	service := accountsvc.AccountService{}
	if err := service.PayOrder(
		order.No, 999, "usd", nil, "", "pi_delete_fenced", "", "ch_delete_fenced",
		model.ORDER_MODE_ONE_TIME, order.CheckoutSessionID, "", time.Time{}, time.Time{},
	); err == nil {
		t.Fatal("deletion intent accepted an unpaid settlement")
	}
	var unchanged model.Order
	if err := db.First(&unchanged, order.Id).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != model.STATUS_UNPAID {
		t.Fatalf("fenced payment status = %s", unchanged.Status)
	}
	var packs int64
	if err := db.Model(&model.CreditsPack{}).Where("uid = ?", user.Id).Count(&packs).Error; err != nil {
		t.Fatal(err)
	}
	if packs != 0 {
		t.Fatalf("fenced settlement created %d Packs", packs)
	}
}
