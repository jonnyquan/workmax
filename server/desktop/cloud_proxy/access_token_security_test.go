//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAcquireAccessTokenClearIfRevisionFailureReturnsClosedNoSession(t *testing.T) {
	const secretMarker = "keychain-delete-private-marker"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"upstream-private-marker"}`))
	}))
	t.Cleanup(upstream.Close)

	keychain := newFakeKeychain()
	store := NewTokenStore(keychain)
	if err := store.Save(TokenPair{
		AccessToken:      "expired-access",
		AccessExpiresAt:  time.Now().UTC().Add(-time.Minute),
		RefreshToken:     "consumable-refresh",
		RefreshExpiresAt: time.Now().UTC().Add(time.Hour),
		Scope:            "workagent",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	keychain.deleteErr = errors.New(secretMarker)
	cloud := NewClient(upstream.URL)
	cloud.HTTPClient = upstream.Client()

	pair, err := AcquireAccessToken(context.Background(), store, cloud)
	if pair != nil {
		t.Fatalf("pair = %+v, want nil", pair)
	}
	if err != ErrSessionPersistence {
		t.Fatalf("error = %v, want exact ErrSessionPersistence", err)
	}
	for _, forbidden := range []string{secretMarker, "upstream-private-marker", "invalid_grant"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("closed refresh error leaked %q: %v", forbidden, err)
		}
	}
	if _, getErr := store.Get(); !errors.Is(getErr, ErrNoSession) {
		t.Fatalf("ambiguous refresh remained usable after failed delete: %v", getErr)
	}
}
