package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"server/globals"
	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func TestCurrentSubscriptionProviderPriceContextHonorsBoundedContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previousDBs })

	order := model.Order{
		UID: 1, No: "ORDER-provider-context", ProductID: "pro",
		Status: model.STATUS_COMPLETE, OrderType: model.ORDER_TYPE_MEMBER,
		OrderMode: model.ORDER_MODE_SUBSCRIPTION, SubscriptionID: "sub_provider_context",
		ProviderPriceID: "price_provider_context", PayTime: time.Now().UTC(),
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed subscription order: %v", err)
	}

	bounded, cancelBounded := context.WithTimeout(context.Background(), time.Second)
	defer cancelBounded()
	priceID, err := (&AccountService{}).CurrentSubscriptionProviderPriceContext(
		bounded,
		order.SubscriptionID,
	)
	if err != nil || priceID != order.ProviderPriceID {
		t.Fatalf("provider price = %q, error = %v", priceID, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&AccountService{}).CurrentSubscriptionProviderPriceContext(
		canceled,
		order.SubscriptionID,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled lookup error = %v, want context canceled", err)
	}
}
