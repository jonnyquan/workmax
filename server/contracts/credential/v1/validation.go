package v1

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorCode is a stable, non-secret reason for credential rejection.
type ErrorCode string

const (
	CodeTokenMalformed       ErrorCode = "token_malformed"
	CodeTokenSignature       ErrorCode = "token_signature_invalid"
	CodeTokenInvalid         ErrorCode = "token_invalid"
	CodeCredentialType       ErrorCode = "credential_type_mismatch"
	CodeIssuer               ErrorCode = "issuer_mismatch"
	CodeAudience             ErrorCode = "audience_mismatch"
	CodeScopeRequired        ErrorCode = "scope_required"
	CodeScopeForbidden       ErrorCode = "scope_forbidden"
	CodeClaimRequired        ErrorCode = "claim_required"
	CodeClaimForbidden       ErrorCode = "claim_forbidden"
	CodeClaimMalformed       ErrorCode = "claim_malformed"
	CodeSubjectBinding       ErrorCode = "subject_binding_mismatch"
	CodeDeviceBinding        ErrorCode = "device_binding_mismatch"
	CodeDeviceSessionBinding ErrorCode = "device_session_binding_mismatch"
	CodeTokenExpired         ErrorCode = "token_expired"
	CodeTokenNotActive       ErrorCode = "token_not_active"
	CodeTokenIssuedInFuture  ErrorCode = "token_issued_in_future"
	CodeTokenTimeRange       ErrorCode = "token_time_range_invalid"
)

// ValidationError intentionally contains no received token or claim value.
// It is safe to map to metrics and sanitized authorization logs.
type ValidationError struct {
	Code  ErrorCode
	Claim string
}

func (err *ValidationError) Error() string {
	if err.Claim == "" {
		return fmt.Sprintf("credential rejected: %s", err.Code)
	}
	return fmt.Sprintf("credential rejected: %s (%s)", err.Code, err.Claim)
}

func validationError(code ErrorCode, claim string) error {
	return &ValidationError{Code: code, Claim: claim}
}

// HasErrorCode reports whether err contains a credential ValidationError with
// the requested stable code.
func HasErrorCode(err error, code ErrorCode) bool {
	var validationErr *ValidationError
	return errors.As(err, &validationErr) && validationErr.Code == code
}

// Validator performs typed claim validation after a token's signature has
// been verified. It is immutable and safe for concurrent use.
type Validator struct {
	issuer string
	leeway time.Duration
	now    func() time.Time
}

// ValidatorOption configures a Validator.
type ValidatorOption func(*Validator) error

// WithLeeway permits a bounded amount of clock skew for time claims.
func WithLeeway(leeway time.Duration) ValidatorOption {
	return func(validator *Validator) error {
		if leeway < 0 || leeway > 10*time.Minute {
			return fmt.Errorf("credential: leeway must be between zero and ten minutes")
		}
		validator.leeway = leeway
		return nil
	}
}

// WithClock installs a clock, primarily for deterministic contract tests.
func WithClock(now func() time.Time) ValidatorOption {
	return func(validator *Validator) error {
		if now == nil {
			return fmt.Errorf("credential: clock must not be nil")
		}
		validator.now = now
		return nil
	}
}

// NewValidator creates a validator for one exact issuer. Issuer matching is
// never disabled for typed credentials.
func NewValidator(issuer string, options ...ValidatorOption) (*Validator, error) {
	if issuer == "" || strings.TrimSpace(issuer) != issuer || len(issuer) > 512 {
		return nil, fmt.Errorf("credential: issuer must be a non-empty canonical value")
	}
	validator := &Validator{
		issuer: issuer,
		now:    time.Now,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("credential: validator option must not be nil")
		}
		if err := option(validator); err != nil {
			return nil, err
		}
	}
	return validator, nil
}

// Validate applies the canonical credential profile and any narrower route
// expectation. It does not verify a JWT signature; callers receiving raw JWTs
// should use Parser.ParseAndValidate instead.
func (validator *Validator) Validate(claims Claims, expectation Expectation) (Principal, error) {
	if validator == nil || validator.now == nil || validator.issuer == "" {
		return Principal{}, fmt.Errorf("credential: validator is not configured")
	}
	profile, ok := ProfileFor(expectation.Policy)
	if !ok {
		return Principal{}, fmt.Errorf("credential: expectation policy %q is not supported", expectation.Policy)
	}
	if claims.CredentialType != profile.CredentialType {
		return Principal{}, validationError(CodeCredentialType, ClaimCredentialType)
	}
	if claims.Issuer != validator.issuer {
		return Principal{}, validationError(CodeIssuer, "iss")
	}
	if len(claims.Audience) != 1 || Audience(claims.Audience[0]) != profile.Audience {
		return Principal{}, validationError(CodeAudience, "aud")
	}
	if err := validatePresence(claims.Subject, profile.Subject, "sub", validateSubject); err != nil {
		return Principal{}, err
	}
	if err := validatePresence(claims.DeviceID, profile.DeviceID, ClaimDeviceID, validateDeviceBinding); err != nil {
		return Principal{}, err
	}
	if err := validatePresence(claims.DeviceSessionID, profile.DeviceSession, ClaimDeviceSessionID, validateDeviceBinding); err != nil {
		return Principal{}, err
	}

	scopes, err := ParseScopes(claims.Scope)
	if err != nil {
		return Principal{}, err
	}
	requiredScopes := append(append([]Scope(nil), profile.RequiredScopes...), expectation.RequiredScopes...)
	if err := validateRequiredScopes(scopes, requiredScopes); err != nil {
		return Principal{}, err
	}
	if err := rejectForeignBaseScopes(scopes, profile); err != nil {
		return Principal{}, err
	}

	now := validator.now().UTC()
	if err := validator.validateTimes(claims, now); err != nil {
		return Principal{}, err
	}
	if expectation.BoundSubject != "" && claims.Subject != expectation.BoundSubject {
		return Principal{}, validationError(CodeSubjectBinding, "sub")
	}
	if expectation.BoundDeviceID != "" && claims.DeviceID != expectation.BoundDeviceID {
		return Principal{}, validationError(CodeDeviceBinding, ClaimDeviceID)
	}
	if expectation.BoundDeviceSessionID != "" && claims.DeviceSessionID != expectation.BoundDeviceSessionID {
		return Principal{}, validationError(CodeDeviceSessionBinding, ClaimDeviceSessionID)
	}

	return Principal{
		Policy:          profile.Policy,
		CredentialType:  claims.CredentialType,
		Subject:         claims.Subject,
		Audience:        profile.Audience,
		Scopes:          append([]Scope(nil), scopes...),
		DeviceID:        claims.DeviceID,
		DeviceSessionID: claims.DeviceSessionID,
		TokenID:         claims.ID,
		IssuedAt:        claims.IssuedAt.Time.UTC(),
		ExpiresAt:       claims.ExpiresAt.Time.UTC(),
	}, nil
}

func (validator *Validator) validateTimes(claims Claims, now time.Time) error {
	if claims.IssuedAt == nil {
		return validationError(CodeClaimRequired, "iat")
	}
	if claims.ExpiresAt == nil {
		return validationError(CodeClaimRequired, "exp")
	}
	issuedAt := claims.IssuedAt.Time.UTC()
	expiresAt := claims.ExpiresAt.Time.UTC()
	if issuedAt.After(now.Add(validator.leeway)) {
		return validationError(CodeTokenIssuedInFuture, "iat")
	}
	if !expiresAt.After(now.Add(-validator.leeway)) {
		return validationError(CodeTokenExpired, "exp")
	}
	if !expiresAt.After(issuedAt) {
		return validationError(CodeTokenTimeRange, "exp")
	}
	if claims.NotBefore != nil {
		notBefore := claims.NotBefore.Time.UTC()
		if notBefore.After(now.Add(validator.leeway)) {
			return validationError(CodeTokenNotActive, "nbf")
		}
		if !expiresAt.After(notBefore) {
			return validationError(CodeTokenTimeRange, "nbf")
		}
	}
	return nil
}

func validateRequiredScopes(actual, required []Scope) error {
	actualSet := make(map[Scope]struct{}, len(actual))
	for _, scope := range actual {
		actualSet[scope] = struct{}{}
	}
	requiredSet := make(map[Scope]struct{}, len(required))
	for _, scope := range required {
		if !validScopeToken(string(scope)) {
			return fmt.Errorf("credential: required scope %q is malformed", scope)
		}
		if _, duplicate := requiredSet[scope]; duplicate {
			continue
		}
		requiredSet[scope] = struct{}{}
		if _, present := actualSet[scope]; !present {
			return validationError(CodeScopeRequired, ClaimScope)
		}
	}
	return nil
}

func rejectForeignBaseScopes(scopes []Scope, currentProfile Profile) error {
	for _, scope := range scopes {
		for _, profile := range credentialProfiles {
			// AgentResource and DeviceSession intentionally share the same
			// Desktop OAuth token, so either of their base scopes may coexist.
			if profile.CredentialType == currentProfile.CredentialType && profile.Audience == currentProfile.Audience {
				continue
			}
			for _, reserved := range profile.RequiredScopes {
				if scope == reserved {
					return validationError(CodeScopeForbidden, ClaimScope)
				}
			}
		}
	}
	return nil
}
