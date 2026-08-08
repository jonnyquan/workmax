package v1

import (
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"
)

func TestValidatorAcceptsEveryTypedCredentialProfile(t *testing.T) {
	validator := contractValidator(t)
	for _, policy := range []Policy{
		PolicyPortalSession,
		PolicyAdminSession,
		PolicyAgentResource,
		PolicyDeviceSession,
	} {
		t.Run(string(policy), func(t *testing.T) {
			claims := validClaimsFor(t, policy)
			principal, err := validator.Validate(claims, Expectation{Policy: policy})
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if principal.Policy != policy || principal.CredentialType != claims.CredentialType || principal.Subject != "u_42" {
				t.Fatalf("unexpected principal: %+v", principal)
			}
			profile, _ := ProfileFor(policy)
			if principal.Audience != profile.Audience || !principal.HasScope(profile.RequiredScopes[0]) {
				t.Fatalf("principal did not preserve profile policy: %+v", principal)
			}
			if principal.IssuedAt != contractTestNow || principal.ExpiresAt != contractTestNow.Add(15*time.Minute) {
				t.Fatalf("unexpected principal times: %+v", principal)
			}
		})
	}
}

func TestOneDesktopCredentialCanSatisfyNarrowAgentAndDesktopPolicies(t *testing.T) {
	validator := contractValidator(t)
	claims := validClaimsFor(t, PolicyDeviceSession)
	claims.Scope = "desktop.session agent.run workspace.primary"

	desktopPrincipal, err := validator.Validate(claims, Expectation{Policy: PolicyDeviceSession})
	if err != nil {
		t.Fatalf("Desktop policy: %v", err)
	}
	agentPrincipal, err := validator.Validate(claims, Expectation{
		Policy:         PolicyAgentResource,
		RequiredScopes: []Scope{"workspace.primary"},
	})
	if err != nil {
		t.Fatalf("Agent policy: %v", err)
	}
	if desktopPrincipal.CredentialType != CredentialTypeDeviceSession || agentPrincipal.CredentialType != CredentialTypeDeviceSession {
		t.Fatalf("policies did not share the Device Session credential: desktop=%+v agent=%+v", desktopPrincipal, agentPrincipal)
	}
	if desktopPrincipal.Audience != AudienceDesktop || agentPrincipal.Audience != AudienceDesktop {
		t.Fatalf("policies did not share the Desktop audience: desktop=%+v agent=%+v", desktopPrincipal, agentPrincipal)
	}
}

func TestReservedAgentTokenExchangeAudienceIsNotCurrentlyAccepted(t *testing.T) {
	claims := validClaimsFor(t, PolicyAgentResource)
	claims.Audience = jwt.ClaimStrings{string(AudienceAgentTokenExchange)}
	_, err := contractValidator(t).Validate(claims, Expectation{Policy: PolicyAgentResource})
	if !HasErrorCode(err, CodeAudience) {
		t.Fatalf("reserved Agent audience: got %v, want audience rejection", err)
	}
}

func TestValidatorFailsClosedForLegacyAndCompatibilityClaims(t *testing.T) {
	validator := contractValidator(t)
	baseTimes := jwt.RegisteredClaims{
		Issuer:    "https://issuer.workmax.test",
		Subject:   "u_42",
		ExpiresAt: jwt.NewNumericDate(contractTestNow.Add(15 * time.Minute)),
		IssuedAt:  jwt.NewNumericDate(contractTestNow),
	}

	legacy := Claims{RegisteredClaims: baseTimes}
	if _, err := validator.Validate(legacy, Expectation{Policy: PolicyDeviceSession}); !HasErrorCode(err, CodeCredentialType) {
		t.Fatalf("legacy generic JWT: got %v, want credential type rejection", err)
	}

	compatibility := validClaimsFor(t, PolicyDeviceSession)
	compatibility.Scope = "workagent"
	if _, err := validator.Validate(compatibility, Expectation{Policy: PolicyDeviceSession}); !HasErrorCode(err, CodeScopeRequired) {
		t.Fatalf("legacy workagent scope: got %v, want target scope rejection", err)
	}

	portal := validClaimsFor(t, PolicyPortalSession)
	if _, err := validator.Validate(portal, Expectation{Policy: PolicyAgentResource}); !HasErrorCode(err, CodeCredentialType) {
		t.Fatalf("Portal credential on Agent policy: got %v", err)
	}
}

func TestValidatorRejectsCrossSurfaceClaims(t *testing.T) {
	validator := contractValidator(t)

	tests := []struct {
		name        string
		claims      func() Claims
		expectation Expectation
		code        ErrorCode
	}{
		{
			name: "wrong credential type",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.CredentialType = CredentialTypePortalSession
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeCredentialType,
		},
		{
			name: "wrong audience",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.Audience = jwt.ClaimStrings{string(AudiencePortal)}
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeAudience,
		},
		{
			name: "multiple audiences including expected",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.Audience = jwt.ClaimStrings{string(AudienceDesktop), string(AudiencePortal)}
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeAudience,
		},
		{
			name: "foreign base scope",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.Scope = "agent.run portal.session"
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeScopeForbidden,
		},
		{
			name: "missing profile scope",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.Scope = "workspace.primary"
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeScopeRequired,
		},
		{
			name:        "missing route scope",
			claims:      func() Claims { return validClaimsFor(t, PolicyAgentResource) },
			expectation: Expectation{Policy: PolicyAgentResource, RequiredScopes: []Scope{"workspace.primary"}},
			code:        CodeScopeRequired,
		},
		{
			name: "Agent missing device session",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.DeviceSessionID = ""
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeClaimRequired,
		},
		{
			name: "Portal carries device session",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyPortalSession)
				claims.DeviceSessionID = "abcdef0123456789abcdef0123456789"
				return claims
			},
			expectation: Expectation{Policy: PolicyPortalSession},
			code:        CodeClaimForbidden,
		},
		{
			name: "Portal carries device id",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyPortalSession)
				claims.DeviceID = "0123456789abcdef0123456789abcdef"
				return claims
			},
			expectation: Expectation{Policy: PolicyPortalSession},
			code:        CodeClaimForbidden,
		},
		{
			name: "malformed optional device id",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.DeviceID = "../../device"
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeClaimMalformed,
		},
		{
			name: "malformed subject",
			claims: func() Claims {
				claims := validClaimsFor(t, PolicyAgentResource)
				claims.Subject = "u_42\nadmin"
				return claims
			},
			expectation: Expectation{Policy: PolicyAgentResource},
			code:        CodeClaimMalformed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validator.Validate(test.claims(), test.expectation)
			if !HasErrorCode(err, test.code) {
				t.Fatalf("Validate: got %v, want code %s", err, test.code)
			}
		})
	}
}

func TestValidatorAppliesNarrowerRouteBindings(t *testing.T) {
	validator := contractValidator(t)
	claims := validClaimsFor(t, PolicyAgentResource)
	claims.Scope = "agent.run workspace.primary artifact.read"
	expectation := Expectation{
		Policy:               PolicyAgentResource,
		RequiredScopes:       []Scope{"workspace.primary"},
		BoundSubject:         claims.Subject,
		BoundDeviceID:        claims.DeviceID,
		BoundDeviceSessionID: claims.DeviceSessionID,
	}
	principal, err := validator.Validate(claims, expectation)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !principal.HasScope("workspace.primary") || !principal.HasScope("artifact.read") {
		t.Fatalf("normalized principal scopes: %v", principal.Scopes)
	}

	tests := []struct {
		name   string
		mutate func(*Expectation)
		code   ErrorCode
	}{
		{"subject", func(expectation *Expectation) { expectation.BoundSubject = "u_7" }, CodeSubjectBinding},
		{"device", func(expectation *Expectation) { expectation.BoundDeviceID = "99999999999999999999999999999999" }, CodeDeviceBinding},
		{"device session", func(expectation *Expectation) { expectation.BoundDeviceSessionID = "99999999999999999999999999999999" }, CodeDeviceSessionBinding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wrong := expectation
			test.mutate(&wrong)
			_, err := validator.Validate(claims, wrong)
			if !HasErrorCode(err, test.code) {
				t.Fatalf("Validate: got %v, want %s", err, test.code)
			}
			if strings.Contains(err.Error(), "999999") || strings.Contains(err.Error(), "u_7") {
				t.Fatalf("validation error leaked received/bound identity: %v", err)
			}
		})
	}
}

func TestValidatorPinsIssuerAndTimeWindow(t *testing.T) {
	validator := contractValidator(t)
	tests := []struct {
		name   string
		mutate func(*Claims)
		code   ErrorCode
	}{
		{"issuer", func(claims *Claims) { claims.Issuer = "https://other.example" }, CodeIssuer},
		{"issued at missing", func(claims *Claims) { claims.IssuedAt = nil }, CodeClaimRequired},
		{"expiry missing", func(claims *Claims) { claims.ExpiresAt = nil }, CodeClaimRequired},
		{"issued in future", func(claims *Claims) { claims.IssuedAt = jwt.NewNumericDate(contractTestNow.Add(time.Minute)) }, CodeTokenIssuedInFuture},
		{"expired", func(claims *Claims) { claims.ExpiresAt = jwt.NewNumericDate(contractTestNow) }, CodeTokenExpired},
		{"not active", func(claims *Claims) { claims.NotBefore = jwt.NewNumericDate(contractTestNow.Add(time.Minute)) }, CodeTokenNotActive},
		{"expiry before issue", func(claims *Claims) { claims.ExpiresAt = jwt.NewNumericDate(contractTestNow.Add(-time.Minute)) }, CodeTokenExpired},
		{"expiry before not-before", func(claims *Claims) {
			claims.NotBefore = jwt.NewNumericDate(contractTestNow.Add(-time.Minute))
			claims.ExpiresAt = jwt.NewNumericDate(contractTestNow.Add(-30 * time.Second))
		}, CodeTokenExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := validClaimsFor(t, PolicyDeviceSession)
			test.mutate(&claims)
			_, err := validator.Validate(claims, Expectation{Policy: PolicyDeviceSession})
			if !HasErrorCode(err, test.code) {
				t.Fatalf("Validate: got %v, want %s", err, test.code)
			}
		})
	}
}

func TestValidatorLeewayIsBoundedAndApplied(t *testing.T) {
	validator := contractValidator(t, WithLeeway(30*time.Second))
	futureClaims := validClaimsFor(t, PolicyDeviceSession)
	futureClaims.IssuedAt = jwt.NewNumericDate(contractTestNow.Add(20 * time.Second))
	futureClaims.NotBefore = jwt.NewNumericDate(contractTestNow.Add(20 * time.Second))
	if _, err := validator.Validate(futureClaims, Expectation{Policy: PolicyDeviceSession}); err != nil {
		t.Fatalf("expected future claims inside leeway to pass: %v", err)
	}
	expiredClaims := validClaimsFor(t, PolicyDeviceSession)
	expiredClaims.IssuedAt = jwt.NewNumericDate(contractTestNow.Add(-15 * time.Minute))
	expiredClaims.NotBefore = jwt.NewNumericDate(contractTestNow.Add(-15 * time.Minute))
	expiredClaims.ExpiresAt = jwt.NewNumericDate(contractTestNow.Add(-20 * time.Second))
	if _, err := validator.Validate(expiredClaims, Expectation{Policy: PolicyDeviceSession}); err != nil {
		t.Fatalf("expected expiry inside leeway to pass: %v", err)
	}
	if _, err := NewValidator("issuer", WithLeeway(10*time.Minute+time.Second)); err == nil {
		t.Fatal("unbounded leeway was accepted")
	}
}

func TestNewValidatorRejectsUnsafeConfiguration(t *testing.T) {
	for _, issuer := range []string{"", " issuer", "issuer ", strings.Repeat("x", 513)} {
		if _, err := NewValidator(issuer); err == nil {
			t.Fatalf("NewValidator(%q) unexpectedly succeeded", issuer)
		}
	}
	if _, err := NewValidator("issuer", WithClock(nil)); err == nil {
		t.Fatal("nil clock unexpectedly succeeded")
	}
	if _, err := NewValidator("issuer", nil); err == nil {
		t.Fatal("nil option unexpectedly succeeded")
	}
}
