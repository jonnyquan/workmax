package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "server/api"
	gatewayapi "server/api/desktop/modelgateway"
	"server/config"
	"server/globals"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// This endpoint spends PLATFORM money on a caller's behalf, so its admission
// bar has to be exactly the one the rest of the Desktop surface uses. A
// weaker gate here would let a cookie, a legacy Portal JWT, or another OAuth
// client's token buy inference against someone else's entitlement.

func newGatewayRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	prevAPI := api.ApiGroupApp.DesktopApiGroup.ModelGatewayApi
	api.ApiGroupApp.DesktopApiGroup.ModelGatewayApi = &gatewayapi.GatewayApi{}
	t.Cleanup(func() {
		api.ApiGroupApp.DesktopApiGroup.ModelGatewayApi = prevAPI
	})

	prevConf := globals.GraConf
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "desktop-model-gateway-router-test-secret"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() {
		globals.GraConf = prevConf
	})

	r := gin.New()
	DesktopModelGatewayRouter{}.InitDesktopModelGatewayRouter(r.Group(""))
	return r
}

var gatewayPaths = []string{
	"/api/desktop/model-gateway/anthropic/v1/messages",
	"/api/desktop/model-gateway/openai/v1/chat/completions",
}

func TestDesktopModelGatewayRouter_RequiresDesktopOAuthBearer(t *testing.T) {
	r := newGatewayRouter(t)

	for _, path := range gatewayPaths {
		for _, tc := range []struct {
			name      string
			configure func(*http.Request)
		}{
			{name: "missing credential"},
			{name: "cookie only", configure: func(req *http.Request) {
				req.AddCookie(&http.Cookie{Name: "access_token", Value: mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID)})
			}},
			{name: "legacy portal jwt", configure: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, ""))
			}},
			{name: "another oauth client", configure: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, "other-client"))
			}},
			{name: "malformed bearer", configure: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer not-a-jwt")
			}},
			{name: "x-api-key that is not a desktop token", configure: func(req *http.Request) {
				req.Header.Set("x-api-key", "sk-ant-some-providers-key")
			}},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"work-pro"}`))
				req.Header.Set("Content-Type", "application/json")
				if tc.configure != nil {
					tc.configure(req)
				}
				r.ServeHTTP(w, req)

				if w.Code != http.StatusUnauthorized {
					t.Fatalf("status: got %d, want 401 (body: %s)", w.Code, w.Body.String())
				}
				if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, `Bearer error="invalid_token"`) {
					t.Fatalf("WWW-Authenticate: got %q, want OAuth bearer invalid_token", got)
				}
			})
		}
	}
}

// A valid Desktop credential must clear the gate. The handler then reports
// its unconfigured gateway — which is the proof admission happened rather
// than being refused at the door.
func TestDesktopModelGatewayRouter_AdmitsDesktopOAuthBearer(t *testing.T) {
	r := newGatewayRouter(t)

	for _, path := range gatewayPaths {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"work-pro"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID))
		r.ServeHTTP(w, req)

		if w.Code == http.StatusUnauthorized {
			t.Fatalf("%s: a valid Desktop credential was refused: %s", path, w.Body.String())
		}
	}
}

// The packaged Anthropic engine sends its credential as `x-api-key`, because
// that is what the Messages API protocol says. Making the Desktop invent a
// bespoke transport just to rename one header would be a worse contract than
// accepting the alias — and the alias relaxes nothing, because the same OAuth
// validation still decides admission.
func TestDesktopModelGatewayRouter_AcceptsDesktopTokenViaAPIKeyHeader(t *testing.T) {
	r := newGatewayRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, gatewayPaths[0], strings.NewReader(`{"model":"work-pro"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID))
	r.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("a valid Desktop token in x-api-key was refused: %s", w.Body.String())
	}
}

// A request that carries both headers must be judged on the one it explicitly
// chose. Silently preferring the alias would let a stale x-api-key override a
// deliberate Authorization header.
func TestDesktopModelGatewayRouter_AuthorizationHeaderWinsOverAPIKey(t *testing.T) {
	r := newGatewayRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, gatewayPaths[0], strings.NewReader(`{"model":"work-pro"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, "other-client"))
	req.Header.Set("x-api-key", mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — the explicit Authorization header must decide", w.Code)
	}
}

// GET must not reach a handler that spends money.
func TestDesktopModelGatewayRouter_OnlyAcceptsPost(t *testing.T) {
	r := newGatewayRouter(t)

	for _, path := range gatewayPaths {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID))
		r.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s GET: status = %d, want 404/405", path, w.Code)
		}
	}
}
