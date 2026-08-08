package config

import "testing"

func TestCommerceEventReconcilerDefaultsClosed(t *testing.T) {
	if (System{}).Cron.CommerceEventReconciler {
		t.Fatal("commerce event reconciler must remain opt-in by default")
	}
}
