package middleware

import (
	"context"
	"errors"
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
)

type deviceSessionCheckerFunc func(context.Context, uint, string, string, string, string) error

func (f deviceSessionCheckerFunc) ValidateAccessSession(ctx context.Context, uid uint, clientID, deviceID, sessionID, scope string) error {
	return f(ctx, uid, clientID, deviceID, sessionID, scope)
}

func TestDesktopResourceBearerPolicyValidatesCredentialBoundary(t *testing.T) {
	originalIssuer := globals.GraConf.JWT.Issuer
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() { globals.GraConf.JWT.Issuer = originalIssuer })

	valid := request.CustomClaims{
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
	policy := DesktopResourceBearerPolicy(desktopoauth.DesktopClientID)
	if err := validateOAuthClaims(&valid, policy); err != nil {
		t.Fatalf("valid Desktop credential rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*request.CustomClaims)
	}{
		{name: "zero user", mutate: func(c *request.CustomClaims) { c.BaseClaims.Id = 0 }},
		{name: "wrong client", mutate: func(c *request.CustomClaims) { c.OAuthClientID = "other" }},
		{name: "wrong audience", mutate: func(c *request.CustomClaims) { c.Audience = "workmax.portal" }},
		{name: "wrong issuer", mutate: func(c *request.CustomClaims) { c.Issuer = "other" }},
		{name: "wrong subject", mutate: func(c *request.CustomClaims) { c.Subject = "u_7" }},
		{name: "generic credential", mutate: func(c *request.CustomClaims) { c.CredentialType = "portal-session" }},
		{name: "missing scope", mutate: func(c *request.CustomClaims) { c.OAuthScope = "" }},
		{name: "scope substring", mutate: func(c *request.CustomClaims) { c.OAuthScope = "workagent.admin" }},
		{name: "missing device", mutate: func(c *request.CustomClaims) { c.DeviceID = "" }},
		{name: "missing device session", mutate: func(c *request.CustomClaims) { c.DeviceSessionID = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			tc.mutate(&candidate)
			if err := validateOAuthClaims(&candidate, policy); err == nil {
				t.Fatal("invalid Desktop credential was accepted")
			}
		})
	}
}

func TestOAuthBearerAuthShadowDoesNotSwitchCurrentAdmission(t *testing.T) {
	originalJWT := globals.GraConf.JWT
	globals.GraConf.JWT.SigningKey = "oauth-shadow-test-key"
	globals.GraConf.JWT.Issuer = "workmax-test"
	t.Cleanup(func() { globals.GraConf.JWT = originalJWT })

	legacyOAuth := request.CustomClaims{
		BaseClaims:    request.BaseClaims{Id: 42},
		OAuthClientID: desktopoauth.DesktopClientID,
		StandardClaims: jwt.StandardClaims{
			Issuer:    "workmax-test",
			ExpiresAt: time.Now().Add(time.Minute).Unix(),
		},
	}
	rawToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, legacyOAuth).
		SignedString([]byte(globals.GraConf.JWT.SigningKey))
	if err != nil {
		t.Fatalf("sign legacy OAuth token: %v", err)
	}

	gin.SetMode(gin.TestMode)
	var got OAuthResourceCredentialEvaluation
	var gotEvaluation bool
	router := gin.New()
	router.GET("/resource", OAuthBearerAuth(desktopoauth.DesktopClientID), func(c *gin.Context) {
		got, gotEvaluation = OAuthResourceCredentialResult(c)
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/resource", nil)
	request.Header.Set("Authorization", "Bearer "+rawToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("rollover token status = %d, want 204", response.Code)
	}
	if !gotEvaluation || got.Compliant || got.FailureCode != OAuthCredentialAudienceMismatch {
		t.Fatalf("shadow evaluation = %+v, present=%v", got, gotEvaluation)
	}
}

func TestOAuthCredentialShadowReasonsAreBounded(t *testing.T) {
	policy := DesktopResourceBearerPolicy(desktopoauth.DesktopClientID)
	claims := request.CustomClaims{
		BaseClaims:    request.BaseClaims{Id: 42},
		OAuthClientID: "received-client-value",
	}
	err := validateOAuthClaims(&claims, policy)
	if got := credentialFailureCode(err); got != OAuthCredentialClientMismatch {
		t.Fatalf("failure code = %q, want %q", got, OAuthCredentialClientMismatch)
	}
	if strings.Contains(err.Error(), claims.OAuthClientID) {
		t.Fatal("credential failure leaked a received claim")
	}
	if got := credentialFailureCode(errors.New("unexpected internal detail")); got != "policy_error" {
		t.Fatalf("fallback failure code = %q", got)
	}
}

func TestHasAllScopesUsesExactTokens(t *testing.T) {
	if !hasAllScopes("history.read workagent", []string{"workagent"}) {
		t.Fatal("exact required scope was not found")
	}
	if hasAllScopes("workagent.admin", []string{"workagent"}) {
		t.Fatal("scope prefix must not satisfy exact scope")
	}
}

func TestCurrentOAuthAdmissionAllowsRolloverTokenWhileTargetFailsClosed(t *testing.T) {
	legacyOAuth := request.CustomClaims{
		BaseClaims:    request.BaseClaims{Id: 42},
		OAuthClientID: desktopoauth.DesktopClientID,
	}
	if err := validateCurrentOAuthClaims(&legacyOAuth, desktopoauth.DesktopClientID); err != nil {
		t.Fatalf("current OAuth credential unexpectedly rejected: %v", err)
	}
	if err := validateOAuthClaims(&legacyOAuth, DesktopResourceBearerPolicy(desktopoauth.DesktopClientID)); err == nil {
		t.Fatal("target policy accepted a credential without resource and device claims")
	}
	if err := validateCurrentOAuthClaims(&legacyOAuth, "other-client"); err == nil {
		t.Fatal("current admission accepted the wrong OAuth client")
	}
}

func TestStrictDesktopPolicyRequiresActiveDeviceSession(t *testing.T) {
	claims := request.CustomClaims{
		BaseClaims:      request.BaseClaims{Id: 42},
		OAuthClientID:   desktopoauth.DesktopClientID,
		DeviceID:        "device-1",
		DeviceSessionID: "session-1",
	}
	called := false
	checker := deviceSessionCheckerFunc(func(_ context.Context, uid uint, clientID, deviceID, sessionID, scope string) error {
		called = true
		if uid != 42 || clientID != desktopoauth.DesktopClientID || deviceID != "device-1" || sessionID != "session-1" || scope != "" {
			t.Fatalf("unexpected binding: uid=%d client=%q device=%q session=%q scope=%q", uid, clientID, deviceID, sessionID, scope)
		}
		return nil
	})
	policy := StrictDesktopResourceBearerPolicy(desktopoauth.DesktopClientID, checker)
	if err := validateActiveDeviceSession(context.Background(), &claims, policy); err != nil {
		t.Fatalf("active device session rejected: %v", err)
	}
	if !called {
		t.Fatal("stateful device session checker was not called")
	}

	policy.DeviceSessions = deviceSessionCheckerFunc(func(context.Context, uint, string, string, string, string) error {
		return errors.New("revoked")
	})
	if err := validateActiveDeviceSession(context.Background(), &claims, policy); err == nil {
		t.Fatal("revoked device session was accepted")
	}
	policy.DeviceSessions = nil
	if err := validateActiveDeviceSession(context.Background(), &claims, policy); err == nil {
		t.Fatal("strict policy without stateful checker was accepted")
	}
}
