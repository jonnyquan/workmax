// Package v1 defines the target credential contract shared by Portal,
// Desktop, Agent and Admin admission code.
//
// The package is deliberately independent of Gin, GORM, global
// configuration and product services. Importing it does not install a
// middleware or change any route. Callers must explicitly select a resource
// policy and validate it before treating the returned Principal as trusted.
// A validated device_session_id still has to be resolved against the
// server-owned session/revocation store; claim presence alone is not proof
// that a device session remains active.
package v1

import (
	"fmt"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"
)

// CredentialContractVersion identifies the target-only credential wire
// shape. Legacy generic JWTs do not implement this contract.
const CredentialContractVersion = "1.0.0-draft"

// MaxSignedTokenBytes bounds work performed by the JWT parser. Typed bearer
// tokens are compact credentials, not a transport for arbitrary payloads.
const MaxSignedTokenBytes = 16 << 10

// JWT claim names are exported so token issuers and independently implemented
// verifiers do not need to duplicate wire literals.
const (
	ClaimCredentialType  = "credential_type"
	ClaimScope           = "scope"
	ClaimDeviceID        = "device_id"
	ClaimDeviceSessionID = "device_session_id"
)

// Policy identifies one route/resource admission policy. AgentResource and
// DeviceSession deliberately share one Desktop OAuth credential type; scopes
// select the narrower resource policy without requiring two Sidecar tokens.
type Policy string

const (
	PolicyPortalSession Policy = "portal-session"
	PolicyAdminSession  Policy = "admin-session"
	PolicyAgentResource Policy = "agent-resource"
	PolicyDeviceSession Policy = "device-session"
)

// CredentialType identifies signed or server-resolved credential data. It is
// not inferred from a route or client header. AgentResource is a Policy, not a
// distinct credential type, until a future token-exchange design exists.
type CredentialType string

const (
	CredentialTypePortalSession CredentialType = "portal-session"
	CredentialTypeAdminSession  CredentialType = "admin-session"
	CredentialTypeDeviceSession CredentialType = "device-session"
)

// Audience is the exact resource-server audience for a credential profile.
// Validators require exactly one audience to prevent a token from becoming a
// cross-surface bearer merely by adding another audience value.
type Audience string

const (
	AudiencePortal  Audience = "workmax.portal"
	AudienceAdmin   Audience = "workmax.admin"
	AudienceDesktop Audience = "workmax.desktop"

	// AudienceAgentTokenExchange is reserved for a future RFC 8707/token
	// exchange design. No current Profile accepts it.
	AudienceAgentTokenExchange Audience = "workmax.agent"
)

// Scope is one OAuth-style permission token. A space-delimited scope claim is
// parsed into these values before policy checks.
type Scope string

const (
	ScopePortalSession  Scope = "portal.session"
	ScopeAdminSession   Scope = "admin.session"
	ScopeAgentRun       Scope = "agent.run"
	ScopeDesktopSession Scope = "desktop.session"
)

// Presence declares whether a claim is mandatory, optional or prohibited for
// a credential type.
type Presence uint8

const (
	PresenceRequired Presence = iota + 1
	PresenceOptional
	PresenceForbidden
)

// Transport documents where the credential is resolved. Portal and Admin are
// server-side Cookie Session targets with Origin/CSRF checks; declaring their
// Principal shape here does not turn them into browser-visible bearer JWTs.
type Transport string

const (
	TransportServerSession Transport = "server-session"
	TransportOAuthBearer   Transport = "oauth-bearer"
)

// Profile is the immutable public description of one resource policy.
// RequiredScopes is copied by ProfileFor, so a caller cannot mutate the
// package's canonical policy.
type Profile struct {
	Policy         Policy
	CredentialType CredentialType
	Audience       Audience
	RequiredScopes []Scope
	Subject        Presence
	DeviceID       Presence
	DeviceSession  Presence
	Transport      Transport
}

var credentialProfiles = map[Policy]Profile{
	PolicyPortalSession: {
		Policy:         PolicyPortalSession,
		CredentialType: CredentialTypePortalSession,
		Audience:       AudiencePortal,
		RequiredScopes: []Scope{ScopePortalSession},
		Subject:        PresenceRequired,
		DeviceID:       PresenceForbidden,
		DeviceSession:  PresenceForbidden,
		Transport:      TransportServerSession,
	},
	PolicyAdminSession: {
		Policy:         PolicyAdminSession,
		CredentialType: CredentialTypeAdminSession,
		Audience:       AudienceAdmin,
		RequiredScopes: []Scope{ScopeAdminSession},
		Subject:        PresenceRequired,
		DeviceID:       PresenceForbidden,
		DeviceSession:  PresenceForbidden,
		Transport:      TransportServerSession,
	},
	PolicyAgentResource: {
		Policy:         PolicyAgentResource,
		CredentialType: CredentialTypeDeviceSession,
		Audience:       AudienceDesktop,
		RequiredScopes: []Scope{ScopeAgentRun},
		Subject:        PresenceRequired,
		DeviceID:       PresenceOptional,
		DeviceSession:  PresenceRequired,
		Transport:      TransportOAuthBearer,
	},
	PolicyDeviceSession: {
		Policy:         PolicyDeviceSession,
		CredentialType: CredentialTypeDeviceSession,
		Audience:       AudienceDesktop,
		RequiredScopes: []Scope{ScopeDesktopSession},
		Subject:        PresenceRequired,
		DeviceID:       PresenceOptional,
		DeviceSession:  PresenceRequired,
		Transport:      TransportOAuthBearer,
	},
}

// ProfileFor returns the canonical profile for a route/resource policy.
func ProfileFor(policy Policy) (Profile, bool) {
	profile, ok := credentialProfiles[policy]
	if !ok {
		return Profile{}, false
	}
	profile.RequiredScopes = append([]Scope(nil), profile.RequiredScopes...)
	return profile, true
}

// Valid reports whether a route/resource policy has a canonical profile.
func (policy Policy) Valid() bool {
	_, ok := credentialProfiles[policy]
	return ok
}

// Valid reports whether the value is a current credential_type claim.
func (credentialType CredentialType) Valid() bool {
	switch credentialType {
	case CredentialTypePortalSession, CredentialTypeAdminSession, CredentialTypeDeviceSession:
		return true
	default:
		return false
	}
}

// Claims is the signed JWT wire shape for typed credentials. Scope follows
// RFC 6749's space-delimited string representation. RegisteredClaims accepts
// either the standard single-string or array form of aud while validation
// below requires exactly one canonical audience.
type Claims struct {
	CredentialType  CredentialType `json:"credential_type"`
	Scope           string         `json:"scope"`
	DeviceID        string         `json:"device_id,omitempty"`
	DeviceSessionID string         `json:"device_session_id,omitempty"`
	jwt.RegisteredClaims
}

// Valid implements jwt.Claims. It intentionally performs only the JWT
// library's registered time checks. Full typed validation requires Validator
// or Parser.ParseAndValidate, which also checks issuer, audience, scope,
// credential type and device binding.
func (claims Claims) Valid() error {
	return claims.RegisteredClaims.Valid()
}

// Expectation is the route/resource-specific part of validation. Policy is
// mandatory. RequiredScopes add to, and never replace, the profile's base
// scope. Bound values are optional exact resource-policy bindings.
type Expectation struct {
	Policy               Policy
	RequiredScopes       []Scope
	BoundSubject         string
	BoundDeviceID        string
	BoundDeviceSessionID string
}

// Principal is returned only after signature and typed semantic validation.
// It contains normalized scopes and exact identity bindings suitable for
// later resource-policy checks. Device/session revocation, current Admin role,
// entitlement and resource ownership are deliberately later checks.
type Principal struct {
	Policy          Policy
	CredentialType  CredentialType
	Subject         string
	Audience        Audience
	Scopes          []Scope
	DeviceID        string
	DeviceSessionID string
	TokenID         string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// HasScope reports whether the validated principal contains scope.
func (principal Principal) HasScope(scope Scope) bool {
	for _, candidate := range principal.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

// ParseScopes parses a canonical RFC 6749 scope string. It rejects duplicate
// scope tokens and non-canonical whitespace instead of silently normalizing
// attacker-controlled input.
func ParseScopes(raw string) ([]Scope, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, " ")
	if len(parts) == 0 || strings.Join(parts, " ") != raw {
		return nil, validationError(CodeClaimMalformed, ClaimScope)
	}

	seen := make(map[Scope]struct{}, len(parts))
	scopes := make([]Scope, 0, len(parts))
	for _, part := range parts {
		if !validScopeToken(part) {
			return nil, validationError(CodeClaimMalformed, ClaimScope)
		}
		scope := Scope(part)
		if _, duplicate := seen[scope]; duplicate {
			return nil, validationError(CodeClaimMalformed, ClaimScope)
		}
		seen[scope] = struct{}{}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// FormatScopes validates and joins scope values into their canonical JWT wire
// representation.
func FormatScopes(scopes ...Scope) (string, error) {
	parts := make([]string, 0, len(scopes))
	seen := make(map[Scope]struct{}, len(scopes))
	for _, scope := range scopes {
		if !validScopeToken(string(scope)) {
			return "", validationError(CodeClaimMalformed, ClaimScope)
		}
		if _, duplicate := seen[scope]; duplicate {
			return "", validationError(CodeClaimMalformed, ClaimScope)
		}
		seen[scope] = struct{}{}
		parts = append(parts, string(scope))
	}
	return strings.Join(parts, " "), nil
}

func validScopeToken(scope string) bool {
	if scope == "" || len(scope) > 128 {
		return false
	}
	for i := 0; i < len(scope); i++ {
		character := scope[i]
		// RFC 6749 scope-token = %x21 / %x23-5B / %x5D-7E.
		if character != 0x21 && !(character >= 0x23 && character <= 0x5b) && !(character >= 0x5d && character <= 0x7e) {
			return false
		}
	}
	return true
}

func validateSubject(subject string) bool {
	return validOpaqueValue(subject, 1, 256)
}

func validateDeviceBinding(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		character := value[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}

func validOpaqueValue(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validatePresence(value string, presence Presence, claimName string, validator func(string) bool) error {
	switch presence {
	case PresenceRequired:
		if value == "" {
			return validationError(CodeClaimRequired, claimName)
		}
	case PresenceForbidden:
		if value != "" {
			return validationError(CodeClaimForbidden, claimName)
		}
	case PresenceOptional:
	default:
		return fmt.Errorf("credential: invalid presence policy for %s", claimName)
	}
	if value != "" && !validator(value) {
		return validationError(CodeClaimMalformed, claimName)
	}
	return nil
}
