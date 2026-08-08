package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "server/api"
	syncapi "server/api/desktop/sync"
	"server/config"
	"server/globals"
	oauthmodel "server/model/desktop/oauth"
	request "server/model/system/request"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"
)

func TestDesktopSyncRouter_RequiresDesktopOAuthBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prevAPI := api.ApiGroupApp.DesktopApiGroup.SyncApi
	api.ApiGroupApp.DesktopApiGroup.SyncApi = &syncapi.SyncApi{}
	t.Cleanup(func() {
		api.ApiGroupApp.DesktopApiGroup.SyncApi = prevAPI
	})

	prevConf := globals.GraConf
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "desktop-sync-router-test-secret"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() {
		globals.GraConf = prevConf
	})

	r := gin.New()
	DesktopSyncRouter{}.InitDesktopSyncRouter(r.Group(""))

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
			req := httptest.NewRequest(http.MethodGet, "/api/desktop/sync/threads", nil)
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

func mintDesktopSyncRouterToken(t *testing.T, clientID string) string {
	t.Helper()
	now := time.Now().UTC()
	claims := request.CustomClaims{
		BaseClaims:    request.BaseClaims{Id: 42},
		OAuthClientID: clientID,
		BufferTime:    0,
		StandardClaims: jwt.StandardClaims{
			Id:        "desktop-sync-router-test",
			NotBefore: now.Add(-5 * time.Second).Unix(),
			IssuedAt:  now.Unix(),
			ExpiresAt: now.Add(15 * time.Minute).Unix(),
			Issuer:    globals.GraConf.JWT.Issuer,
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(globals.GraConf.JWT.SigningKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}
