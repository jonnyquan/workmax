package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "server/api"
	modelsapi "server/api/desktop/models"
	"server/config"
	"server/globals"
	oauthmodel "server/model/desktop/oauth"

	"github.com/gin-gonic/gin"
)

// The model catalog answers a per-caller entitlement question, so it must sit
// behind exactly the credential /api/desktop/sync/* sits behind: a Desktop
// OAuth Bearer, never a cookie, never a legacy Portal JWT, never another
// OAuth client. Weaker admission here would let one machine's session read
// another account's entitlements.
func TestDesktopModelsRouter_RequiresDesktopOAuthBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prevAPI := api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi
	api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi = &modelsapi.ModelCatalogApi{}
	t.Cleanup(func() {
		api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi = prevAPI
	})

	prevConf := globals.GraConf
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "desktop-models-router-test-secret"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() {
		globals.GraConf = prevConf
	})

	r := gin.New()
	DesktopModelsRouter{}.InitDesktopModelsRouter(r.Group(""))

	for _, tc := range []struct {
		name      string
		configure func(*http.Request)
	}{
		{name: "missing bearer"},
		{name: "cookie only", configure: func(req *http.Request) {
			req.AddCookie(&http.Cookie{Name: "access_token", Value: mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID)})
		}},
		{name: "legacy jwt", configure: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, ""))
		}},
		{name: "wrong oauth client", configure: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, "other-client"))
		}},
		{name: "malformed bearer", configure: func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer not-a-jwt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/desktop/models", nil)
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

// A valid Desktop credential must clear the middleware and reach the handler.
// The handler then reports its unconfigured DB — which is exactly the proof
// that admission happened rather than being refused at the gate.
func TestDesktopModelsRouter_AdmitsDesktopOAuthBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prevAPI := api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi
	api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi = &modelsapi.ModelCatalogApi{}
	t.Cleanup(func() {
		api.ApiGroupApp.DesktopApiGroup.ModelCatalogApi = prevAPI
	})

	prevConf := globals.GraConf
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "desktop-models-router-admit-secret"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() {
		globals.GraConf = prevConf
	})

	r := gin.New()
	DesktopModelsRouter{}.InitDesktopModelsRouter(r.Group(""))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/models", nil)
	req.Header.Set("Authorization", "Bearer "+mintDesktopSyncRouterToken(t, oauthmodel.DesktopClientID))
	r.ServeHTTP(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("valid Desktop credential was refused: %s", w.Body.String())
	}
}
