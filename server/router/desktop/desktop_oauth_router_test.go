package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "server/api"
	desktopapi "server/api/desktop/oauth"

	"github.com/gin-gonic/gin"
)

func TestDesktopOauthRouter_RateLimitsTokenAndRevoke(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prev := api.ApiGroupApp.DesktopApiGroup.OauthApi
	api.ApiGroupApp.DesktopApiGroup.OauthApi = &desktopapi.OauthApi{}
	t.Cleanup(func() {
		api.ApiGroupApp.DesktopApiGroup.OauthApi = prev
	})

	r := gin.New()
	DesktopOauthRouter{}.InitDesktopOauthRouter(r.Group(""))

	for _, path := range []string{
		"/api/desktop/oauth/token",
		"/api/desktop/oauth/revoke",
	} {
		t.Run(path, func(t *testing.T) {
			for i := 0; i < 10; i++ {
				w := postEmptyForm(r, path)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("request %d status: got %d, want 400", i+1, w.Code)
				}
			}
			w := postEmptyForm(r, path)
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("request 11 status: got %d, want 429", w.Code)
			}
		})
	}
}

func postEmptyForm(r http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.10:12345"
	r.ServeHTTP(w, req)
	return w
}
