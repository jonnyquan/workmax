//go:build desktop

package desktop

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"server/desktop/auth"
)

func TestServerLocalTokenMiddleware_CoversUnknownPathsAndWrongMethods(t *testing.T) {
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	cases := []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
	}{
		{
			name:       "missing token on unknown path is rejected before route discovery",
			method:     http.MethodGet,
			path:       "/not-a-sidecar-route",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong token on unknown path is rejected before route discovery",
			method:     http.MethodGet,
			path:       "/not-a-sidecar-route",
			token:      "wrong",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "correct token on unknown path reaches router 404",
			method:     http.MethodGet,
			path:       "/not-a-sidecar-route",
			token:      "tok",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing token on wrong method is rejected before method handling",
			method:     http.MethodPost,
			path:       "/health",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "missing token is rejected before trailing slash redirect",
			method:     http.MethodGet,
			path:       "/health/",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, base+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.token != "" {
				req.Header.Set(auth.HeaderLocalToken, tc.token)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

func TestServerLocalTokenMiddleware_CoversRegisteredRouteSurface(t *testing.T) {
	db := openServerTestDB(t)
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	base := "http://" + srv.listener.Addr().String()
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/health"},
		{method: http.MethodGet, path: "/auth/status"},
		{method: http.MethodGet, path: "/auth/userinfo"},
		{method: http.MethodPost, path: "/auth/start", body: `{}`},
		{method: http.MethodPost, path: "/auth/login-transaction"},
		{method: http.MethodGet, path: "/auth/login-transaction"},
		{method: http.MethodPost, path: "/auth/login-transaction/password", body: `{"email":"person@example.com","password":"x"}`},
		{method: http.MethodDelete, path: "/auth/login-transaction"},
		{method: http.MethodPost, path: "/auth/logout", body: `{}`},
		{method: http.MethodPost, path: "/agent/chat", body: `{}`},
		{method: http.MethodGet, path: "/agent/skills/catalog"},
		{method: http.MethodGet, path: "/system/network-state"},
		{method: http.MethodPost, path: "/system/log", body: `{"level":"error","message":"x"}`},
		{method: http.MethodGet, path: "/system/diagnostics"},
		{method: http.MethodGet, path: "/system/server-version"},
		{method: http.MethodPost, path: "/system/trigger-sync", body: `{}`},
		{method: http.MethodGet, path: "/agent/threads"},
		{method: http.MethodGet, path: "/agent/threads/thread-1/messages"},
	}

	for _, route := range routes {
		for _, tc := range []struct {
			name  string
			token string
		}{
			{name: "missing token"},
			{name: "wrong token", token: "wrong"},
		} {
			t.Run(route.method+" "+route.path+" "+tc.name, func(t *testing.T) {
				req, err := http.NewRequest(
					route.method,
					base+route.path,
					strings.NewReader(route.body),
				)
				if err != nil {
					t.Fatal(err)
				}
				if route.body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				if tc.token != "" {
					req.Header.Set(auth.HeaderLocalToken, tc.token)
				}

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()

				if resp.StatusCode != http.StatusForbidden {
					t.Fatalf("status: got %d, want %d", resp.StatusCode, http.StatusForbidden)
				}
			})
		}
	}
}
