package middleware

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

// Portal session JWTs and Desktop OAuth access tokens are signed with the same
// key. Before the audience gate, a Desktop access token — minted for a native
// client under a device-scoped grant — was accepted by every JWTAuth route,
// i.e. it was a full Portal session. These tests pin the split: JWTAuth refuses
// the Desktop audience, JWTAuthAcceptingDesktopAudience admits it.

func withJWTTestConfig(t *testing.T) {
	t.Helper()
	original := globals.GraConf.JWT
	globals.GraConf.JWT.SigningKey = "jwt-audience-test-key"
	globals.GraConf.JWT.Issuer = "workmax-test"
	globals.GraConf.JWT.ExpiresTime = "24h"
	globals.GraConf.JWT.BufferTime = "1h"
	t.Cleanup(func() { globals.GraConf.JWT = original })
}

func signTestToken(t *testing.T, claims request.CustomClaims) string {
	t.Helper()
	if claims.ExpiresAt == 0 {
		claims.ExpiresAt = time.Now().Add(time.Hour).Unix()
	}
	token, err := utils.NewJWT().CreateToken(claims)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func desktopAccessTokenClaims() request.CustomClaims {
	return request.CustomClaims{
		BaseClaims:      request.BaseClaims{Id: 42},
		OAuthClientID:   desktopoauth.DesktopClientID,
		OAuthScope:      desktopoauth.DesktopOAuthScopeWorkAgent,
		CredentialType:  desktopoauth.DesktopCredentialDeviceSession,
		DeviceID:        "0123456789abcdef0123456789abcdef",
		DeviceSessionID: "session-1",
		StandardClaims: jwt.StandardClaims{
			Audience: desktopoauth.DesktopResourceAudience,
			Issuer:   "workmax-test",
			Subject:  "u_42",
		},
	}
}

func portalSessionClaims() request.CustomClaims {
	return request.CustomClaims{
		BaseClaims: request.BaseClaims{Id: 42, Email: "portal@example.com"},
		StandardClaims: jwt.StandardClaims{
			Issuer:  "workmax-test",
			Subject: "u_42",
		},
	}
}

func runGuardedRequest(t *testing.T, guard gin.HandlerFunc, token string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/guarded", guard, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"reached": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func assertHandlerReached(t *testing.T, recorder *httptest.ResponseRecorder, want bool) {
	t.Helper()
	reached := strings.Contains(recorder.Body.String(), `"reached":true`)
	if reached != want {
		t.Fatalf("handler reached = %v, want %v; body = %s", reached, want, recorder.Body.String())
	}
}

func TestJWTAuthRejectsDesktopAudienceToken(t *testing.T) {
	withJWTTestConfig(t)

	recorder := runGuardedRequest(t, JWTAuth(), signTestToken(t, desktopAccessTokenClaims()))

	assertHandlerReached(t, recorder, false)
	if !strings.Contains(recorder.Body.String(), "audience") {
		t.Fatalf("rejection should name the audience, got %s", recorder.Body.String())
	}
}

func TestJWTAuthAcceptsPortalSessionToken(t *testing.T) {
	withJWTTestConfig(t)

	recorder := runGuardedRequest(t, JWTAuth(), signTestToken(t, portalSessionClaims()))

	assertHandlerReached(t, recorder, true)
}

func TestJWTAuthAcceptingDesktopAudienceAdmitsBothCredentials(t *testing.T) {
	withJWTTestConfig(t)

	t.Run("desktop token", func(t *testing.T) {
		recorder := runGuardedRequest(t, JWTAuthAcceptingDesktopAudience(), signTestToken(t, desktopAccessTokenClaims()))
		assertHandlerReached(t, recorder, true)
	})

	t.Run("portal token", func(t *testing.T) {
		recorder := runGuardedRequest(t, JWTAuthAcceptingDesktopAudience(), signTestToken(t, portalSessionClaims()))
		assertHandlerReached(t, recorder, true)
	})
}

// The audience alone must not be a bearer of trust: a token stamped with the
// Desktop audience but issued to some other (future) OAuth client is refused
// even on an opted-in route.
func TestJWTAuthAcceptingDesktopAudienceRequiresDesktopClientID(t *testing.T) {
	withJWTTestConfig(t)

	claims := desktopAccessTokenClaims()
	claims.OAuthClientID = "some-other-client"

	recorder := runGuardedRequest(t, JWTAuthAcceptingDesktopAudience(), signTestToken(t, claims))

	assertHandlerReached(t, recorder, false)
}

// A Desktop token rotates through /api/desktop/oauth/token. The Portal
// rebake path (new-token header + refreshed cookie) must never fire for one,
// or the OAuth flow silently acquires a second, unowned refresh channel.
func TestJWTAuthAcceptingDesktopAudienceDoesNotRebakeDesktopToken(t *testing.T) {
	withJWTTestConfig(t)

	claims := desktopAccessTokenClaims()
	// Force the Portal rebake condition: expiry inside the buffer window.
	claims.BufferTime = int64((24 * time.Hour).Seconds())
	claims.ExpiresAt = time.Now().Add(time.Minute).Unix()

	recorder := runGuardedRequest(t, JWTAuthAcceptingDesktopAudience(), signTestToken(t, claims))

	assertHandlerReached(t, recorder, true)
	if got := recorder.Header().Get("new-token"); got != "" {
		t.Fatalf("Desktop token was rebaked into a Portal session token: %q", got)
	}
}
