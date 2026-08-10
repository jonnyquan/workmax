//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCurrentSidecarRoutePoliciesMatchDesktopBoundaryManifest(t *testing.T) {
	data, err := os.ReadFile("../../desktop/contracts/desktop-boundaries.v0.json")
	if err != nil {
		t.Fatalf("read Desktop boundary manifest: %v", err)
	}
	var manifest struct {
		LoopbackRoutes []SidecarRoutePolicy `json:"loopbackRoutes"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode Desktop boundary manifest: %v", err)
	}

	policies := CurrentSidecarRoutePolicies()
	if err := validateSidecarRoutePolicies(policies); err != nil {
		t.Fatalf("current Sidecar policies are invalid: %v", err)
	}
	if len(policies) != len(manifest.LoopbackRoutes) {
		t.Fatalf("policy count: got %d, manifest has %d", len(policies), len(manifest.LoopbackRoutes))
	}

	byID := make(map[string]SidecarRoutePolicy, len(policies))
	for _, policy := range policies {
		byID[policy.ID] = policy
	}
	for _, want := range manifest.LoopbackRoutes {
		got, ok := byID[want.ID]
		if !ok {
			t.Errorf("manifest route %q is missing from runtime policy table", want.ID)
			continue
		}
		if got.Method != want.Method || got.Path != want.Path || got.Credential != want.Credential ||
			got.CredentialScheme != want.CredentialScheme || got.RendererAccess != want.RendererAccess {
			t.Errorf("route %q identity/credential: got %+v, want %+v", want.ID, got, want)
		}
		if got.RequestPolicy.Origin != want.RequestPolicy.Origin ||
			got.RequestPolicy.Body != want.RequestPolicy.Body ||
			got.RequestPolicy.MaxBodyBytes != want.RequestPolicy.MaxBodyBytes ||
			got.RequestPolicy.BodyTooLargeError != want.RequestPolicy.BodyTooLargeError ||
			!slices.Equal(got.RequestPolicy.ContentTypes, want.RequestPolicy.ContentTypes) {
			t.Errorf("route %q request policy: got %+v, want %+v", want.ID, got.RequestPolicy, want.RequestPolicy)
		}
	}
}

func TestCurrentSidecarRoutePoliciesReturnsDefensiveCopy(t *testing.T) {
	first := CurrentSidecarRoutePolicies()
	first[0].ID = "mutated"
	first[3].RequestPolicy.ContentTypes[0] = "text/plain"

	second := CurrentSidecarRoutePolicies()
	if second[0].ID == "mutated" {
		t.Fatal("route policy slice was not defensively copied")
	}
	if slices.Contains(second[3].RequestPolicy.ContentTypes, "text/plain") {
		t.Fatal("route policy content types were not defensively copied")
	}
}

func TestSidecarRoutePoliciesRejectBrowserOriginOnEveryRoute(t *testing.T) {
	baseURL, token, gateway := bootRoutePolicyServer(t)
	for _, policy := range CurrentSidecarRoutePolicies() {
		policy := policy
		t.Run(policy.ID, func(t *testing.T) {
			path := strings.ReplaceAll(policy.Path, ":uuid", "thread-1")
			var body string
			if policy.RequestPolicy.Body == SidecarBodyRequired {
				body = `{}`
			}
			req, err := http.NewRequest(policy.Method, baseURL+path, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			// Each route gets the credential it actually takes, so the Origin
			// rule is what is being measured rather than the credential check
			// firing first.
			switch policy.CredentialScheme {
			case SidecarCredentialSchemeAnthropicAPIKey:
				req.Header.Set("x-api-key", gateway.Token())
			case SidecarCredentialSchemeBearer:
				req.Header.Set("Authorization", "Bearer "+gateway.Token())
			default:
				req.Header.Set("X-Local-Token", token)
			}
			req.Header.Set("Origin", "https://attacker.example")
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
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

func TestSidecarDoesNotRedirectPrivilegedPathLookalikes(t *testing.T) {
	baseURL, token, _ := bootRoutePolicyServer(t)
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("sidecar path redirect must not occur")
		},
	}
	for _, path := range []string{
		"/auth/start/",
		"/AUTH/START",
		"/auth//start",
	} {
		req, err := http.NewRequest(http.MethodPost, baseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Local-Token", token)
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound || response.Header.Get("Location") != "" {
			t.Fatalf("%s: status=%d Location=%q, want 404 without redirect", path, response.StatusCode, response.Header.Get("Location"))
		}
	}
}

func TestSidecarRouteRequestPolicyEnforcement(t *testing.T) {
	baseURL, token, _ := bootRoutePolicyServer(t)
	tests := []struct {
		name         string
		method       string
		path         string
		body         string
		contentTypes []string
		wantStatus   int
	}{
		{
			name:         "GET route rejects a body",
			method:       http.MethodGet,
			path:         "/health",
			body:         `{}`,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "chat requires a body",
			method:       http.MethodPost,
			path:         "/agent/chat",
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "chat rejects non JSON",
			method:       http.MethodPost,
			path:         "/agent/chat",
			body:         `{}`,
			contentTypes: []string{"text/plain"},
			wantStatus:   http.StatusUnsupportedMediaType,
		},
		{
			name:         "chat rejects duplicate Content-Type",
			method:       http.MethodPost,
			path:         "/agent/chat",
			body:         `{}`,
			contentTypes: []string{"application/json", "application/json"},
			wantStatus:   http.StatusUnsupportedMediaType,
		},
		{
			name:         "chat accepts JSON charset before handler",
			method:       http.MethodPost,
			path:         "/agent/chat",
			body:         `{}`,
			contentTypes: []string{"application/json; charset=utf-8"},
			wantStatus:   http.StatusServiceUnavailable,
		},
		{
			name:         "chat enforces one MiB limit",
			method:       http.MethodPost,
			path:         "/agent/chat",
			body:         strings.Repeat("x", maxAgentChatBodyBytes+1),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusRequestEntityTooLarge,
		},
		{
			name:         "thread PUT requires a body",
			method:       http.MethodPut,
			path:         "/agent/threads/de305d54-75b4-431b-adb2-eb6b9e546014",
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusBadRequest,
		},
		{
			name:         "thread PUT rejects non JSON",
			method:       http.MethodPut,
			path:         "/agent/threads/de305d54-75b4-431b-adb2-eb6b9e546014",
			body:         `{"name":"N","agent_mode":"ppt"}`,
			contentTypes: []string{"text/plain"},
			wantStatus:   http.StatusUnsupportedMediaType,
		},
		{
			name:         "thread PUT accepts JSON charset before handler",
			method:       http.MethodPut,
			path:         "/agent/threads/de305d54-75b4-431b-adb2-eb6b9e546014",
			body:         `{"name":"N","agent_mode":"ppt"}`,
			contentTypes: []string{"application/json; charset=utf-8"},
			wantStatus:   http.StatusServiceUnavailable,
		},
		{
			name:         "thread PUT enforces four KiB limit",
			method:       http.MethodPut,
			path:         "/agent/threads/de305d54-75b4-431b-adb2-eb6b9e546014",
			body:         strings.Repeat("x", maxAgentThreadPutBodyBytes+1),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusRequestEntityTooLarge,
		},
		{
			name:         "auth start accepts form before handler",
			method:       http.MethodPost,
			path:         "/auth/start",
			body:         "scope=workagent",
			contentTypes: []string{"application/x-www-form-urlencoded"},
			wantStatus:   http.StatusServiceUnavailable,
		},
		{
			name:       "auth start rejects untyped body",
			method:     http.MethodPost,
			path:       "/auth/start",
			body:       "scope=workagent",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:         "auth start enforces control body limit",
			method:       http.MethodPost,
			path:         "/auth/start",
			body:         strings.Repeat("x", maxOAuthControlBodyBytes+1),
			contentTypes: []string{"application/x-www-form-urlencoded"},
			wantStatus:   http.StatusRequestEntityTooLarge,
		},
		{
			name:         "legacy logout JSON object remains accepted",
			method:       http.MethodPost,
			path:         "/auth/logout",
			body:         `{}`,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusServiceUnavailable,
		},
		{
			name:         "renderer log enforces 64 KiB limit",
			method:       http.MethodPost,
			path:         "/system/log",
			body:         strings.Repeat("x", maxRendererLogBodyBytes+1),
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusRequestEntityTooLarge,
		},
		{
			name:         "legacy manual sync JSON object remains accepted",
			method:       http.MethodPost,
			path:         "/system/trigger-sync",
			body:         `{}`,
			contentTypes: []string{"application/json"},
			wantStatus:   http.StatusServiceUnavailable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, baseURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Local-Token", token)
			for _, contentType := range tc.contentTypes {
				req.Header.Add("Content-Type", contentType)
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

func bootRoutePolicyServer(t *testing.T) (baseURL string, token string, gateway *ModelGateway) {
	t.Helper()
	const localToken = "route-policy-token"
	gateway, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "route-policy-test",
		LocalToken:     localToken,
		DB:             openServerTestDB(t),
		ModelGateway:   gateway,
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
	return "http://" + srv.listener.Addr().String(), localToken, gateway
}
