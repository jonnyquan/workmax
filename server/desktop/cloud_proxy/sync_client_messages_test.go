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

func TestClient_ListMessagesDelta_HappyPath(t *testing.T) {
	var gotPath, gotAuth, gotThreadID, gotSince, gotLimit string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotThreadID = r.URL.Query().Get("thread_id")
		gotSince = r.URL.Query().Get("since")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"items": [
				{"action":"upsert","cloud_message_id":"1","uuid":"m-1","thread_uuid":"thr-a",
				 "user_text":"hi","ai_text":"ok","chat_mode":"ppt",
				 "structured_content":"{\"blocks\":[]}","actions":"[]",
				 "metadata":"{\"plan\":\"go\"}","use_files":"file-1.pdf",
				 "user_rating":1,"user_feedback":"good",
				 "updated_at":"2026-05-17T22:00:00Z","created_at":"2026-05-17T21:00:00Z"}
			],
			"next_cursor": "abc",
			"has_more": false,
			"server_time": "2026-05-17T22:30:00Z"
		}`)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	page, err := c.ListMessagesDelta(context.Background(), "tok-123", 42, "cur-xyz", 50)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/desktop/sync/messages" {
		t.Errorf("path: %q", gotPath)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("auth: %q", gotAuth)
	}
	if gotThreadID != "42" {
		t.Errorf("thread_id query: %q", gotThreadID)
	}
	if gotSince != "cur-xyz" {
		t.Errorf("since: %q", gotSince)
	}
	if gotLimit != "50" {
		t.Errorf("limit: %q", gotLimit)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items: %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.UUID != "m-1" || item.ThreadUUID != "thr-a" {
		t.Errorf("item ids: %+v", item)
	}
	if item.UserText != "hi" || item.AIText != "ok" || item.ChatMode != "ppt" {
		t.Errorf("item core: %+v", item)
	}
	if item.StructuredContent != `{"blocks":[]}` || item.Metadata != `{"plan":"go"}` {
		t.Errorf("item JSON blobs: %+v", item)
	}
	if item.UserRating != 1 || item.UserFeedback != "good" {
		t.Errorf("rating fields: %+v", item)
	}
}

func TestClient_ListMessagesDelta_RejectsZeroThreadID(t *testing.T) {
	c := NewClient("http://nope")
	_, err := c.ListMessagesDelta(context.Background(), "tok", 0, "", 0)
	if err == nil {
		t.Error("cloud_thread_id=0 should error before hitting the network")
	}
}

func TestClient_ListMessagesDelta_HTTP401IsErrAuthExpired(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListMessagesDelta(context.Background(), "tok", 42, "", 0)
	if !errors.Is(err, ErrSyncAuthExpired) {
		t.Errorf("expected ErrSyncAuthExpired, got %v", err)
	}
}

func TestClient_ListMessagesDelta_HTTP404IsThreadNotOwnedOrMissing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"error":"not_found"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListMessagesDelta(context.Background(), "tok", 42, "", 0)
	if !errors.Is(err, ErrThreadNotOwnedOrMissing) {
		t.Errorf("expected ErrThreadNotOwnedOrMissing, got %v", err)
	}
}

func TestClient_ListMessagesDelta_HTTP5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	_, err := c.ListMessagesDelta(context.Background(), "tok", 42, "", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrSyncAuthExpired) || errors.Is(err, ErrThreadNotOwnedOrMissing) {
		t.Errorf("5xx should NOT map to known sentinels")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err message: %v", err)
	}
}

func TestClient_ListMessagesDelta_NilItemsCoercedToEmpty(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"items":null,"next_cursor":"","has_more":false,"server_time":"now"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	page, err := c.ListMessagesDelta(context.Background(), "tok", 42, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if page.Items == nil {
		t.Error("nil items from cloud should be coerced to empty slice")
	}
}
