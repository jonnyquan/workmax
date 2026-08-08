package v1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"
)

var contractTestNow = time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)

func TestCredentialProfilesPinAudienceScopeAndBindings(t *testing.T) {
	tests := []struct {
		policy         Policy
		credentialType CredentialType
		audience       Audience
		scope          Scope
		deviceID       Presence
		deviceSession  Presence
		transport      Transport
	}{
		{PolicyPortalSession, CredentialTypePortalSession, AudiencePortal, ScopePortalSession, PresenceForbidden, PresenceForbidden, TransportServerSession},
		{PolicyAdminSession, CredentialTypeAdminSession, AudienceAdmin, ScopeAdminSession, PresenceForbidden, PresenceForbidden, TransportServerSession},
		{PolicyAgentResource, CredentialTypeDeviceSession, AudienceDesktop, ScopeAgentRun, PresenceOptional, PresenceRequired, TransportOAuthBearer},
		{PolicyDeviceSession, CredentialTypeDeviceSession, AudienceDesktop, ScopeDesktopSession, PresenceOptional, PresenceRequired, TransportOAuthBearer},
	}

	for _, test := range tests {
		t.Run(string(test.policy), func(t *testing.T) {
			profile, ok := ProfileFor(test.policy)
			if !ok {
				t.Fatal("profile is missing")
			}
			if profile.Policy != test.policy || profile.CredentialType != test.credentialType || profile.Audience != test.audience {
				t.Fatalf("unexpected identity: %+v", profile)
			}
			if !reflect.DeepEqual(profile.RequiredScopes, []Scope{test.scope}) {
				t.Fatalf("required scopes: got %v, want %v", profile.RequiredScopes, []Scope{test.scope})
			}
			if profile.Subject != PresenceRequired || profile.DeviceID != test.deviceID || profile.DeviceSession != test.deviceSession || profile.Transport != test.transport {
				t.Fatalf("unexpected presence policy: %+v", profile)
			}
			if !test.policy.Valid() || !test.credentialType.Valid() {
				t.Fatal("known credential type is not valid")
			}
		})
	}

	if _, ok := ProfileFor("generic-jwt"); ok || Policy("generic-jwt").Valid() || CredentialType("generic-jwt").Valid() {
		t.Fatal("legacy generic JWT must not be a typed credential profile")
	}
}

func TestProfileForReturnsRequiredScopeCopy(t *testing.T) {
	first, _ := ProfileFor(PolicyAgentResource)
	first.RequiredScopes[0] = "attacker.scope"
	second, _ := ProfileFor(PolicyAgentResource)
	if got := second.RequiredScopes[0]; got != ScopeAgentRun {
		t.Fatalf("canonical profile mutated through returned slice: %q", got)
	}
}

func TestScopeWireHelpers(t *testing.T) {
	got, err := ParseScopes("agent.run workspace.primary artifact.read")
	if err != nil {
		t.Fatalf("ParseScopes: %v", err)
	}
	want := []Scope{ScopeAgentRun, "workspace.primary", "artifact.read"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes: got %v, want %v", got, want)
	}
	formatted, err := FormatScopes(want...)
	if err != nil {
		t.Fatalf("FormatScopes: %v", err)
	}
	if formatted != "agent.run workspace.primary artifact.read" {
		t.Fatalf("formatted scopes: %q", formatted)
	}

	for _, raw := range []string{
		" agent.run",
		"agent.run ",
		"agent.run  workspace.primary",
		"agent.run\tworkspace.primary",
		"agent.run\nworkspace.primary",
		"agent.run agent.run",
		"bad\"scope",
		"bad\\scope",
		strings.Repeat("x", 129),
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := ParseScopes(raw)
			if !HasErrorCode(err, CodeClaimMalformed) {
				t.Fatalf("ParseScopes(%q): got %v, want claim_malformed", raw, err)
			}
		})
	}
	if got, err := ParseScopes(""); err != nil || got != nil {
		t.Fatalf("empty scope: got %v, %v", got, err)
	}
	if _, err := FormatScopes(ScopeAgentRun, ScopeAgentRun); !HasErrorCode(err, CodeClaimMalformed) {
		t.Fatalf("duplicate FormatScopes: got %v", err)
	}
}

func TestClaimsUsePinnedJWTFieldNames(t *testing.T) {
	claims := validClaimsFor(t, PolicyDeviceSession)
	encoded, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	for claimName, want := range map[string]string{
		ClaimCredentialType:  string(CredentialTypeDeviceSession),
		ClaimScope:           string(ScopeDesktopSession),
		ClaimDeviceID:        claims.DeviceID,
		ClaimDeviceSessionID: claims.DeviceSessionID,
		"sub":                claims.Subject,
		"iss":                claims.Issuer,
	} {
		if got, ok := wire[claimName].(string); !ok || got != want {
			t.Errorf("claim %s: got %#v, want %q", claimName, wire[claimName], want)
		}
	}
}

func validClaimsFor(t *testing.T, policy Policy) Claims {
	t.Helper()
	profile, ok := ProfileFor(policy)
	if !ok {
		t.Fatalf("unknown test credential policy %q", policy)
	}
	scope, err := FormatScopes(profile.RequiredScopes...)
	if err != nil {
		t.Fatalf("format profile scopes: %v", err)
	}
	claims := Claims{
		CredentialType: profile.CredentialType,
		Scope:          scope,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.workmax.test",
			Subject:   "u_42",
			Audience:  jwt.ClaimStrings{string(profile.Audience)},
			ExpiresAt: jwt.NewNumericDate(contractTestNow.Add(15 * time.Minute)),
			NotBefore: jwt.NewNumericDate(contractTestNow.Add(-5 * time.Second)),
			IssuedAt:  jwt.NewNumericDate(contractTestNow),
			ID:        "jti_0123456789abcdef",
		},
	}
	if profile.DeviceID != PresenceForbidden {
		claims.DeviceID = "0123456789abcdef0123456789abcdef"
	}
	if profile.DeviceSession == PresenceRequired {
		claims.DeviceSessionID = "abcdef0123456789abcdef0123456789"
	}
	return claims
}

func contractValidator(t *testing.T, options ...ValidatorOption) *Validator {
	t.Helper()
	options = append([]ValidatorOption{WithClock(func() time.Time { return contractTestNow })}, options...)
	validator, err := NewValidator("https://issuer.workmax.test", options...)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return validator
}
