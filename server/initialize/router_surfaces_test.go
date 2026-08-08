package initialize

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRoutersRegistersEverySurface(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	registered := make(map[string]struct{})
	for _, route := range engine.Routes() {
		key := route.Method + " " + route.Path
		if _, duplicate := registered[key]; duplicate {
			t.Fatalf("duplicate route registration %s", key)
		}
		registered[key] = struct{}{}
	}

	expected := []string{
		"GET /api/health",
		"POST /api/auth/sign-in",
		"POST /api/callback/subscription/stripe",
		"POST /api/v1/desktop/identity/login-transactions",
		"GET /api/desktop/version",
		"PUT /api/desktop/agent/threads/:uuid",
		"GET /api/desktop/sync/threads",
		"GET /api/work-agent/skills",
		"GET /api/work-agent/conversations/:threadId",
		"GET /api/account/quota",
		"GET /api/internal/monitor/summary",
		"GET /api/admin/dashboard/getBasicStatistics",
	}
	for _, route := range expected {
		if _, ok := registered[route]; !ok {
			t.Errorf("surface route not registered: %s", route)
		}
	}
	for _, retired := range []string{
		"POST /api/admin/order/refundOrder",
		"DELETE /api/admin/order/deleteOrderList",
	} {
		if _, ok := registered[retired]; ok {
			t.Errorf("retired financial mutation route remains registered: %s", retired)
		}
	}
}

func TestDesktopAgentSurfaceRejectsMissingBearerBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/desktop/agent/threads/123e4567-e89b-42d3-a456-426614174000",
		strings.NewReader(`{"name":"Deck","agent_mode":"ppt"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 before nil-DB handler; body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("WWW-Authenticate"); !strings.Contains(got, `Bearer error="invalid_token"`) {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
}

func TestRoutersDoNotTrustRetiredBrowserClientOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	for _, origin := range []string{
		"https://workmax.app",
		"https://www.workmax.app",
		"https://admin.workmax.app",
		"http://localhost:3000",
	} {
		t.Run(origin, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			req.Header.Set("Origin", origin)
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, req)

			if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Fatalf("retired browser origin %q was admitted as %q", origin, got)
			}
		})
	}
}

func TestRoutersDoNotTrustForwardedForOnDesktopLoginBootstrap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Routers()

	var response *httptest.ResponseRecorder
	for attempt := 0; attempt < 11; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/identity/login-transactions", nil)
		request.Header.Set("X-Forwarded-For", "198.51.100."+strconv.Itoa(attempt+1))
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, request)
	}
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For bypassed login rate limit: status = %d, body = %s", response.Code, response.Body.String())
	}
}
