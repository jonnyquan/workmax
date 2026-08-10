package initialize

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"

	"server/globals"
	desktopoauth "server/model/desktop/oauth"
	request "server/model/system/request"
	"server/utils"
)

// A Desktop OAuth access token must reach exactly two non-/api/desktop routes
// — the ones server/desktop/cloud_proxy/cloud_routes.go points the sidecar at —
// and nothing else. Composing the assertion against the real Routers() is the
// point: a route that moves between mount groups changes its admitted
// credential, and this test is what notices.
//
// Admission is asserted through the middleware's rejection message rather than
// a status code: JWTAuth aborts with HTTP 200 + code 0 (the legacy Portal
// envelope), so the status alone cannot distinguish "refused at the gate" from
// "handler ran and failed" in a test process with no database.
const desktopAudienceRejection = "audience"

func desktopRoutingTestToken(t *testing.T) string {
	t.Helper()
	original := globals.GraConf.JWT
	globals.GraConf.JWT.SigningKey = "desktop-routing-test-key"
	globals.GraConf.JWT.Issuer = "workmax-test"
	globals.GraConf.JWT.ExpiresTime = "24h"
	globals.GraConf.JWT.BufferTime = "1h"
	t.Cleanup(func() { globals.GraConf.JWT = original })

	token, err := utils.NewJWT().CreateToken(request.CustomClaims{
		BaseClaims:      request.BaseClaims{Id: 42},
		OAuthClientID:   desktopoauth.DesktopClientID,
		OAuthScope:      desktopoauth.DesktopOAuthScopeWorkAgent,
		CredentialType:  desktopoauth.DesktopCredentialDeviceSession,
		DeviceID:        "0123456789abcdef0123456789abcdef",
		DeviceSessionID: "session-1",
		StandardClaims: jwt.StandardClaims{
			Audience:  desktopoauth.DesktopResourceAudience,
			Issuer:    "workmax-test",
			Subject:   "u_42",
			ExpiresAt: time.Now().Add(time.Hour).Unix(),
		},
	})
	if err != nil {
		t.Fatalf("sign desktop access token: %v", err)
	}
	return token
}

func callWithDesktopToken(t *testing.T, engine *gin.Engine, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+desktopRoutingTestToken(t))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func TestDesktopTokenIsRefusedOnPortalAndAgentRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	// One route per JWTAuth-protected surface, plus an Agent route that sits
	// right next to the two shared ones so a careless group move is caught.
	refused := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/account/quota"},                      // Portal authenticated
		{http.MethodGet, "/api/work-agent/design-systems"},          // Agent, not Desktop-shared
		{http.MethodGet, "/api/work-agent/chat/conversations"},      // Agent, not Desktop-shared
		{http.MethodGet, "/api/admin/dashboard/getBasicStatistics"}, // Admin
	}

	for _, route := range refused {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := callWithDesktopToken(t, engine, route.method, route.path)
			if !strings.Contains(recorder.Body.String(), desktopAudienceRejection) {
				t.Fatalf("Desktop token was not refused at the gate; body = %s", recorder.Body.String())
			}
		})
	}
}

func TestDesktopTokenIsAdmittedOnDesktopSharedAgentRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	// Both routes the sidecar calls without a /api/desktop prefix. The handlers
	// then fail on the absent test database — that is fine and deliberately not
	// asserted; what matters is that admission happened.
	admitted := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/work-agent/skills"},
		{http.MethodPost, "/api/work-agent/chat/agent"},
	}

	for _, route := range admitted {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := callWithDesktopToken(t, engine, route.method, route.path)
			if strings.Contains(recorder.Body.String(), desktopAudienceRejection) {
				t.Fatalf("Desktop token refused on a route the Desktop client depends on; body = %s",
					recorder.Body.String())
			}
		})
	}
}

// The /api/desktop/** surface keeps its own OAuth Bearer policy — unchanged by
// the audience gate, which only governs JWTAuth-mounted routes.
func TestDesktopTokenStillReachesDesktopResourceSurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	recorder := callWithDesktopToken(t, engine, http.MethodGet, "/api/desktop/sync/threads")

	if got := recorder.Header().Get("WWW-Authenticate"); got != "" {
		t.Fatalf("Desktop token rejected by the Desktop resource surface: %q", got)
	}
}
