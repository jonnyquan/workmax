//go:build desktop

package cloud_proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_RevokeRefreshToken_HappyPath(t *testing.T) {
	var gotForm map[string]string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/desktop/oauth/revoke" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: got %q", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("content-type: %q", got)
		}
		if got := r.Header.Get("X-WorkMax-Client"); got != "desktop" {
			t.Errorf("X-WorkMax-Client missing")
		}
		_ = r.ParseForm()
		gotForm = map[string]string{
			"token":           r.PostFormValue("token"),
			"client_id":       r.PostFormValue("client_id"),
			"token_type_hint": r.PostFormValue("token_type_hint"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)

	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	if err := c.RevokeRefreshToken(context.Background(), "my-refresh-tok"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if gotForm["token"] != "my-refresh-tok" {
		t.Errorf("token field: %v", gotForm)
	}
	if gotForm["client_id"] != c.ClientID {
		t.Errorf("client_id field: got %q, want %q", gotForm["client_id"], c.ClientID)
	}
	if gotForm["token_type_hint"] != "refresh_token" {
		t.Errorf("token_type_hint: got %q, want refresh_token", gotForm["token_type_hint"])
	}
}

func TestClient_RevokeRefreshToken_RejectsEmptyToken(t *testing.T) {
	c := NewClient("http://nope")
	if err := c.RevokeRefreshToken(context.Background(), ""); err == nil {
		t.Error("empty token should error before hitting the network")
	}
}

func TestClient_RevokeRefreshToken_HTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"error":"server down"}`)
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	err := c.RevokeRefreshToken(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err message: %v", err)
	}
}

func TestClient_RevokeRefreshToken_RedactsHTTPErrorBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"refresh_token":"refresh-secret","error":"Authorization: Bearer bearer-secret https://user:pass@example.com/path?client_secret=client-secret X-Local-Token=local-secret"}`))
	}))
	t.Cleanup(upstream.Close)
	c := NewClient(upstream.URL)
	c.HTTPClient = upstream.Client()
	err := c.RevokeRefreshToken(context.Background(), "tok")
	if err == nil {
		t.Fatal("expected error on HTTP 502")
	}
	for _, secret := range []string{"refresh-secret", "bearer-secret", "user:pass", "client-secret", "local-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("revoke error leaked %q: %v", secret, err)
		}
	}
	if got, want := err.Error(), "revoke: HTTP 502"; got != want {
		t.Fatalf("error = %q, want body-independent %q", got, want)
	}
}

func TestClient_RevokeRefreshToken_2xxIsSuccess(t *testing.T) {
	cases := []int{200, 201, 204}
	for _, code := range cases {
		t.Run(http.StatusText(code), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer upstream.Close()
			c := NewClient(upstream.URL)
			c.HTTPClient = upstream.Client()
			if err := c.RevokeRefreshToken(context.Background(), "tok"); err != nil {
				t.Errorf("status %d should be success, got: %v", code, err)
			}
		})
	}
}

func TestClient_RevokeRefreshToken_NetworkError(t *testing.T) {
	c := NewClient("http://does-not-exist-workmax-test.invalid")
	c.HTTPClient = &http.Client{} // default transport, but bogus URL
	err := c.RevokeRefreshToken(context.Background(), "tok")
	if err == nil {
		t.Error("expected error for nonexistent host")
	}
}
