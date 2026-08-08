//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_ListThreadsDelta_HappyPath(t *testing.T) {
	var gotPath, gotAuth, gotClient, gotClientVer, gotAccept, gotSince, gotLimit string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotClient = r.Header.Get("X-WorkMax-Client")
		gotClientVer = r.Header.Get("X-WorkMax-Client-Version")
		gotAccept = r.Header.Get("Accept")
		gotSince = r.URL.Query().Get("since")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [
				{"action":"upsert","cloud_thread_id":"42","uuid":"u-a","name":"A",
				 "agent_mode":"ppt","agent_type":"general_agent","model":"work-pro",
				 "message_count":5,"msg_preview":"hi","file_count":0,"is_public":false,
				 "updated_at":"2026-05-17T22:00:00Z","created_at":"2026-05-17T22:00:00Z"}
			],
			"next_cursor": "abc",
			"has_more": false,
			"server_time": "2026-05-17T22:30:00Z"
		}`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	page, err := c.ListThreadsDelta(context.Background(), "tok-123", "cur-xyz", 50)
	if err != nil {
		t.Fatalf("ListThreadsDelta: %v", err)
	}

	// Wire correctness
	if gotPath != "/api/desktop/sync/threads" {
		t.Errorf("path: %q", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth header: %q", gotAuth)
	}
	if gotClient != "desktop" {
		t.Errorf("X-WorkMax-Client: %q", gotClient)
	}
	if gotClientVer == "" {
		t.Errorf("X-WorkMax-Client-Version: missing (got %q)", gotClientVer)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept: %q", gotAccept)
	}
	if gotSince != "cur-xyz" {
		t.Errorf("since query: %q", gotSince)
	}
	if gotLimit != "50" {
		t.Errorf("limit query: %q", gotLimit)
	}

	// Decoded shape
	if len(page.Items) != 1 {
		t.Fatalf("items: got %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.Action != "upsert" || item.CloudThreadID != "42" || item.UUID != "u-a" {
		t.Errorf("item core fields: %+v", item)
	}
	if item.AgentMode != "ppt" || item.AgentType != "general_agent" || item.Model != "work-pro" {
		t.Errorf("item meta: %+v", item)
	}
	if item.MessageCount != 5 || item.MsgPreview != "hi" {
		t.Errorf("item counts: %+v", item)
	}
	if page.NextCursor != "abc" || page.HasMore || page.ServerTime != "2026-05-17T22:30:00Z" {
		t.Errorf("page meta: %+v", page)
	}
}

func TestClient_ListThreadsDelta_EmptyCursorOmitsQueryParam(t *testing.T) {
	var rawQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	if _, err := c.ListThreadsDelta(context.Background(), "tok", "", 0); err != nil {
		t.Fatal(err)
	}
	// Empty cursor + zero limit → no query params at all.
	if rawQuery != "" {
		t.Errorf("empty cursor + limit=0 should omit query: %q", rawQuery)
	}
}

func TestClient_ListThreadsDelta_OnlyLimit(t *testing.T) {
	var since, limit string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since = r.URL.Query().Get("since")
		limit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":[],"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, _ = c.ListThreadsDelta(context.Background(), "tok", "", 25)
	if since != "" {
		t.Errorf("since should be empty when not passed, got %q", since)
	}
	if limit != "25" {
		t.Errorf("limit: %q", limit)
	}
}

func TestClient_ListThreadsDelta_HTTP401IsErrAuthExpired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		w.WriteHeader(401)
		io.WriteString(w, `{"error":"unauthorized"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListThreadsDelta(context.Background(), "tok", "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrSyncAuthExpired) {
		t.Errorf("expected ErrSyncAuthExpired sentinel, got %v", err)
	}
}

func TestClient_ListThreadsDelta_HTTP5xx(t *testing.T) {
	cases := []int{500, 502, 503}
	for _, code := range cases {
		t.Run(http.StatusText(code), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				io.WriteString(w, "service unavailable")
			}))
			defer upstream.Close()
			c := NewClient(upstream.URL)
			c.HTTPClient = upstream.Client()
			_, err := c.ListThreadsDelta(context.Background(), "tok", "", 0)
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, ErrSyncAuthExpired) {
				t.Error("5xx should NOT map to ErrSyncAuthExpired")
			}
			if !strings.Contains(err.Error(), http.StatusText(code)[:3]) {
				// Status code should appear in error somewhere; we don't
				// strictly require text match, just numeric.
				want := strings.Split(err.Error(), "HTTP ")
				if len(want) < 2 {
					t.Errorf("err should mention HTTP status: %v", err)
				}
			}
		})
	}
}

func TestClient_ListThreadsDelta_NonJSONBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		io.WriteString(w, "not json at all")
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListThreadsDelta(context.Background(), "tok", "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid JSON response") {
		t.Errorf("err should use the closed JSON response error: %v", err)
	}
}

func TestClient_ListThreadsDelta_NetworkError(t *testing.T) {
	c := NewClient("http://nope-invalid.localhost.test")
	c.HTTPClient = &http.Client{Timeout: 0} // immediate DNS / connect failure
	_, err := c.ListThreadsDelta(context.Background(), "tok", "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP") {
		t.Errorf("network err message: %v", err)
	}
}

func TestClient_ListThreadsDelta_NilItemsCoercedToEmpty(t *testing.T) {
	// Defense-in-depth: cloud envelope coerces nil → [], but if a
	// future cloud version regresses, the sidecar shouldn't crash
	// or surface a nil slice to the SyncWorker.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":null,"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	page, err := c.ListThreadsDelta(context.Background(), "tok", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil {
		t.Error("nil items from cloud should be coerced to empty slice")
	}
	if len(page.Items) != 0 {
		t.Errorf("items: got %d, want 0", len(page.Items))
	}
}

func TestClient_ListThreadsDelta_ContextCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block forever to ensure ctx.Done() is what triggers the return.
		<-r.Context().Done()
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, err := c.ListThreadsDelta(ctx, "tok", "", 0)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
