package desktop

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "server/api"
	agentapi "server/api/desktop/agent"
	"server/config"
	"server/globals"
	oauthmodel "server/model/desktop/oauth"
	systemrequest "server/model/system/request"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"
)

func TestDesktopAgentRouterRequiresDesktopOAuthBearer(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousAPI := api.ApiGroupApp.DesktopApiGroup.AgentApi
	api.ApiGroupApp.DesktopApiGroup.AgentApi = agentapi.NewThreadApi(nil)
	t.Cleanup(func() { api.ApiGroupApp.DesktopApiGroup.AgentApi = previousAPI })

	previousConfig := globals.GraConf
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "desktop-agent-router-test-secret"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() { globals.GraConf = previousConfig })

	router := gin.New()
	DesktopAgentRouter{}.InitDesktopAgentRouter(router.Group(""))

	const path = "/api/desktop/agent/threads/123e4567-e89b-42d3-a456-426614174000"
	for _, test := range []struct {
		name      string
		configure func(*http.Request)
	}{
		{name: "missing bearer"},
		{name: "cookie only", configure: func(request *http.Request) {
			request.AddCookie(&http.Cookie{Name: "access_token", Value: mintDesktopAgentRouterToken(t, oauthmodel.DesktopClientID, true)})
		}},
		{name: "generic JWT", configure: func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer "+mintDesktopAgentRouterToken(t, "", false))
		}},
		{name: "wrong OAuth client", configure: func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer "+mintDesktopAgentRouterToken(t, "other-client", true))
		}},
		{name: "legacy Desktop token without resource envelope", configure: func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer "+mintDesktopAgentRouterToken(t, oauthmodel.DesktopClientID, false))
		}},
		{name: "malformed bearer", configure: func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer not-a-jwt")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"name":"Deck","agent_mode":"ppt"}`))
			request.Header.Set("Content-Type", "application/json")
			if test.configure != nil {
				test.configure(request)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("WWW-Authenticate"); !strings.Contains(got, `Bearer error="invalid_token"`) {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}

	valid := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"name":"Deck","agent_mode":"ppt"}`))
	valid.Header.Set("Content-Type", "application/json")
	valid.Header.Set("Authorization", "Bearer "+mintDesktopAgentRouterToken(t, oauthmodel.DesktopClientID, true))
	validResponse := httptest.NewRecorder()
	router.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid Desktop bearer status = %d, want handler's 503; body=%s", validResponse.Code, validResponse.Body.String())
	}
}

func TestDesktopAgentRouterRegistersFixedPutResourcePath(t *testing.T) {
	previousAPI := api.ApiGroupApp.DesktopApiGroup.AgentApi
	api.ApiGroupApp.DesktopApiGroup.AgentApi = agentapi.NewThreadApi(nil)
	t.Cleanup(func() { api.ApiGroupApp.DesktopApiGroup.AgentApi = previousAPI })

	router := gin.New()
	DesktopAgentRouter{}.InitDesktopAgentRouter(router.Group(""))
	routes := router.Routes()
	if len(routes) != 1 || routes[0].Method != http.MethodPut || routes[0].Path != "/api/desktop/agent/threads/:uuid" {
		t.Fatalf("routes = %+v", routes)
	}
}

func mintDesktopAgentRouterToken(t *testing.T, clientID string, resourceEnvelope bool) string {
	t.Helper()
	now := time.Now().UTC()
	claims := systemrequest.CustomClaims{
		BaseClaims:    systemrequest.BaseClaims{Id: 42},
		OAuthClientID: clientID,
		StandardClaims: jwt.StandardClaims{
			Id:        "desktop-agent-router-test",
			NotBefore: now.Add(-5 * time.Second).Unix(),
			IssuedAt:  now.Unix(),
			ExpiresAt: now.Add(15 * time.Minute).Unix(),
			Issuer:    globals.GraConf.JWT.Issuer,
		},
	}
	if resourceEnvelope {
		claims.OAuthScope = oauthmodel.DesktopOAuthScopeWorkAgent
		claims.CredentialType = oauthmodel.DesktopCredentialDeviceSession
		claims.DeviceID = "desktop-agent-router-device"
		claims.DeviceSessionID = "desktop-agent-router-session"
		claims.Audience = oauthmodel.DesktopResourceAudience
		claims.Subject = "u_42"
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(globals.GraConf.JWT.SigningKey))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}
