//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClientUserInfo_SendsBearerAndDecodes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CloudRouteOAuthUserInfo {
			t.Fatalf("path: got %q, want %q", r.URL.Path, CloudRouteOAuthUserInfo)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-123" {
			t.Fatalf("Authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(UserInfo{
			UserID:      "u_42",
			Email:       "creator@workmax.app",
			DisplayName: "Creator",
			Tier:        "pro",
			Quota:       UserInfoQuota{MonthUsed: 12, MonthLimit: 100},
		})
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()

	info, err := client.UserInfo(context.Background(), "access-123")
	if err != nil {
		t.Fatal(err)
	}
	if info.Email != "creator@workmax.app" || info.Tier != "pro" {
		t.Fatalf("decoded wrong info: %+v", info)
	}
	if info.Quota.MonthUsed != 12 || info.Quota.MonthLimit != 100 {
		t.Fatalf("decoded wrong quota: %+v", info.Quota)
	}
}

func TestClientUserInfo_Non200IsError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_token"}`))
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()

	_, err := client.UserInfo(context.Background(), "bad-token")
	if !errors.Is(err, ErrUserInfoAuthExpired) {
		t.Fatalf("expected ErrUserInfoAuthExpired, got %v", err)
	}
}

func TestClientUserInfo_RejectsOversizedChunkedResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, strings.Repeat(" ", (64<<10)+1))
	}))
	t.Cleanup(upstream.Close)

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	_, err := client.UserInfo(context.Background(), "access-token")
	if err == nil || err.Error() != "userinfo: invalid response body" {
		t.Fatalf("error = %v, want closed oversized-body error", err)
	}
}

func TestClientUserInfo_SessionChangeCancelsInflightRequest(t *testing.T) {
	started := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-releaseUpstream:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseUpstream) }) })

	client := NewClient(upstream.URL)
	client.HTTPClient = upstream.Client()
	ctx, cancel := context.WithCancelCause(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.UserInfo(ctx, "access-token")
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for userinfo request")
	}
	cancel(ErrSessionChanged)

	select {
	case err := <-result:
		if !errors.Is(err, ErrSessionChanged) {
			t.Fatalf("UserInfo error = %v, want ErrSessionChanged", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("userinfo request did not stop after session change")
	}
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("userinfo session change did not cancel upstream HTTP")
	}
}
