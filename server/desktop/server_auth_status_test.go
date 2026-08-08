//go:build desktop

package desktop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
)

func TestAuthStatus_NoTokenStoreConfiguredIsUnauthenticated(t *testing.T) {
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		// No TokenStore: diagnostic/support boot should still let the renderer route to LoginPage.
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.listener.Close()
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	req.Header.Set("X-Local-Token", "tok")
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, want 200", rec.Code)
	}
	var got authStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "unauthenticated" {
		t.Fatalf("state: %q, want unauthenticated", got.State)
	}
	if got.UserID != nil || got.Tier != nil {
		t.Fatalf("expected no account fields, got user_id=%v tier=%v", got.UserID, got.Tier)
	}
	if _, err := time.Parse(time.RFC3339Nano, got.UpdatedAt); err != nil {
		t.Fatalf("updated_at is not RFC3339Nano: %q", got.UpdatedAt)
	}
}

func TestAuthStatus_EmptyAccessTokenIsUnauthenticated(t *testing.T) {
	db := openServerTestDB(t)
	store := cloudproxy.NewTokenStore(newMemKeychain())
	if err := store.Save(cloudproxy.TokenPair{
		AccessToken:      "",
		AccessExpiresAt:  time.Now().UTC().Add(time.Hour),
		RefreshToken:     "refresh-still-valid",
		RefreshExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		TokenStore:     store,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.listener.Close()
	})

	req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	req.Header.Set("X-Local-Token", "tok")
	rec := httptest.NewRecorder()

	srv.httpServer.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, want 200", rec.Code)
	}
	var got authStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "unauthenticated" {
		t.Fatalf("state: %q, want unauthenticated", got.State)
	}
}
