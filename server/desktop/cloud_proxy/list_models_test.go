//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newModelsTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	return client
}

func TestListModels_ParsesTheDesktopCatalogContract(t *testing.T) {
	client := newModelsTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CloudRouteModelsList {
			t.Errorf("path = %q, want %q", r.URL.Path, CloudRouteModelsList)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[
			{"modelId":"work-pro","displayName":"WorkMax Pro","description":"Everyday","requiredTier":"free","permissions":["use"],"default":true},
			{"modelId":"work-plus","displayName":"WorkMax Plus","requiredTier":"pro","permissions":[]}
		],"tier":"pro","tierExpiresAt":"2026-09-01T00:00:00Z"}`)
	})

	catalog, err := client.ListModels(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if catalog.Tier != "pro" || catalog.TierExpiresAt != "2026-09-01T00:00:00Z" {
		t.Fatalf("tier metadata = %+v", catalog)
	}
	if len(catalog.Items) != 2 {
		t.Fatalf("items = %+v", catalog.Items)
	}
	if !catalog.Items[0].Usable() || !catalog.Items[0].Default {
		t.Fatalf("first item = %+v", catalog.Items[0])
	}
	// Visible but locked: present in the list, not usable.
	if catalog.Items[1].Usable() || catalog.Items[1].RequiredTier != "pro" {
		t.Fatalf("locked item = %+v", catalog.Items[1])
	}
}

// A row with no id cannot be selected, stored, or sent back on a turn.
// Dropping it here spares every consumer from re-asking the same question, and
// a nil permissions array becomes an empty one so callers can index it.
func TestListModels_NormalizesUnusableRows(t *testing.T) {
	client := newModelsTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"items":[
			{"modelId":"","displayName":"Nameless"},
			{"modelId":"work-pro","displayName":"Pro"}
		],"tier":"free"}`)
	})
	catalog, err := client.ListModels(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 1 || catalog.Items[0].ModelID != "work-pro" {
		t.Fatalf("items = %+v", catalog.Items)
	}
	if catalog.Items[0].Permissions == nil {
		t.Fatal("permissions must never be nil — callers index it directly")
	}
}

func TestListModels_UnauthorizedIsItsOwnSentinel(t *testing.T) {
	client := newModelsTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, err := client.ListModels(context.Background(), "stale"); !errors.Is(err, ErrListModelsAuthExpired) {
		t.Fatalf("err = %v, want ErrListModelsAuthExpired so the caller can refresh once", err)
	}
}
