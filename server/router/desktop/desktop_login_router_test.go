package desktop

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	api "server/api"
	loginapi "server/api/desktop/login"
)

func TestDesktopLoginRouterRegistersTransactionRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := api.ApiGroupApp.DesktopApiGroup.LoginApi
	api.ApiGroupApp.DesktopApiGroup.LoginApi = &loginapi.LoginApi{}
	t.Cleanup(func() { api.ApiGroupApp.DesktopApiGroup.LoginApi = previous })

	engine := gin.New()
	DesktopLoginRouter{}.InitDesktopLoginRouter(engine.Group(""))

	registered := make(map[string]struct{})
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}
	for _, expected := range []string{
		"POST /api/v1/desktop/identity/login-transactions",
		"GET /api/v1/desktop/identity/login-transactions/:id",
		"POST /api/v1/desktop/identity/login-transactions/:id/password",
		"POST /api/v1/desktop/identity/login-transactions/:id/exchange",
	} {
		if _, ok := registered[expected]; !ok {
			t.Errorf("Desktop login route not registered: %s", expected)
		}
	}
	if len(registered) != 4 {
		t.Fatalf("registered route count = %d, want 4", len(registered))
	}
}

func TestDesktopLoginRouterFailsClosedAndSetsSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := api.ApiGroupApp.DesktopApiGroup.LoginApi
	api.ApiGroupApp.DesktopApiGroup.LoginApi = &loginapi.LoginApi{}
	t.Cleanup(func() { api.ApiGroupApp.DesktopApiGroup.LoginApi = previous })

	engine := gin.New()
	DesktopLoginRouter{}.InitDesktopLoginRouter(engine.Group(""))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/identity/login-transactions", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	for header, expected := range map[string]string{
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := response.Header().Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}
}

func TestDesktopLoginRouterRateLimitResponseIsNotCacheable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := api.ApiGroupApp.DesktopApiGroup.LoginApi
	api.ApiGroupApp.DesktopApiGroup.LoginApi = &loginapi.LoginApi{}
	t.Cleanup(func() { api.ApiGroupApp.DesktopApiGroup.LoginApi = previous })

	engine := gin.New()
	DesktopLoginRouter{}.InitDesktopLoginRouter(engine.Group(""))
	var response *httptest.ResponseRecorder
	for attempt := 0; attempt < 11; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/identity/login-transactions", nil)
		response = httptest.NewRecorder()
		engine.ServeHTTP(response, request)
	}

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusTooManyRequests, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
}
