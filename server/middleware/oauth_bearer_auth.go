package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"server/globals"
	desktopoauth "server/model/desktop/oauth"
	request "server/model/system/request"
	"server/utils"

	"github.com/gin-gonic/gin"
)

// OAuthBearerPolicy is the resource-server side of an OAuth access token
// contract. Every field is independently checked after signature and standard
// time validation so a generic login JWT cannot cross into a Desktop resource
// route merely because both token families currently share a signing key.
type OAuthBearerPolicy struct {
	ClientID                   string
	Audience                   string
	CredentialType             string
	RequiredScopes             []string
	RequireDeviceSession       bool
	RequireActiveDeviceSession bool
	DeviceSessions             OAuthDeviceSessionChecker
}

// OAuthDeviceSessionChecker is implemented by the stateful Desktop OAuth
// service without coupling middleware to GORM.
type OAuthDeviceSessionChecker interface {
	ValidateAccessSession(ctx context.Context, uid uint, clientID, deviceID, deviceSessionID, grantedScope string) error
}

const oauthResourceCredentialContextKey = "oauth_resource_credential_evaluation"

// OAuthResourceCredentialEvaluation records whether a current request carries
// the strengthened Desktop Audience/Scope/Device envelope. It is still a
// compatibility policy using the umbrella `workagent` scope, not proof that a
// route has migrated to its final minimum target scopes.
type OAuthResourceCredentialEvaluation struct {
	Compliant   bool
	FailureCode OAuthCredentialFailureCode
}

// OAuthCredentialFailureCode is a bounded, non-PII shadow/admission reason.
// It is safe for metrics labels; received claims and token data are never
// included in the value.
type OAuthCredentialFailureCode string

const (
	OAuthCredentialMissingSubject      OAuthCredentialFailureCode = "missing_subject"
	OAuthCredentialClientMismatch      OAuthCredentialFailureCode = "client_mismatch"
	OAuthCredentialAudienceMismatch    OAuthCredentialFailureCode = "audience_mismatch"
	OAuthCredentialTypeMismatch        OAuthCredentialFailureCode = "credential_type_mismatch"
	OAuthCredentialIssuerMismatch      OAuthCredentialFailureCode = "issuer_mismatch"
	OAuthCredentialSubjectMismatch     OAuthCredentialFailureCode = "subject_mismatch"
	OAuthCredentialScopeMismatch       OAuthCredentialFailureCode = "scope_mismatch"
	OAuthCredentialDeviceSessionAbsent OAuthCredentialFailureCode = "device_session_missing"
)

type oauthCredentialError struct {
	code OAuthCredentialFailureCode
}

func (err *oauthCredentialError) Error() string {
	return "OAuth resource credential rejected: " + string(err.code)
}

func credentialFailure(code OAuthCredentialFailureCode) error {
	return &oauthCredentialError{code: code}
}

func credentialFailureCode(err error) OAuthCredentialFailureCode {
	var credentialErr *oauthCredentialError
	if errors.As(err, &credentialErr) {
		return credentialErr.code
	}
	return "policy_error"
}

// StrictDesktopResourceBearerPolicy adds active refresh-chain validation to
// the signed claim policy. Current routes do not mount it until rollover
// telemetry and minimum-scope migration gates pass.
func StrictDesktopResourceBearerPolicy(requiredClientID string, sessions OAuthDeviceSessionChecker) OAuthBearerPolicy {
	policy := DesktopResourceBearerPolicy(requiredClientID)
	policy.RequireActiveDeviceSession = true
	policy.DeviceSessions = sessions
	return policy
}

// DesktopResourceBearerPolicy is the current Desktop resource policy. The
// legacy `workagent` scope remains the externally visible grant during the
// compatibility phase; target typed scopes are introduced separately.
func DesktopResourceBearerPolicy(requiredClientID string) OAuthBearerPolicy {
	return OAuthBearerPolicy{
		ClientID:             requiredClientID,
		Audience:             desktopoauth.DesktopResourceAudience,
		CredentialType:       desktopoauth.DesktopCredentialDeviceSession,
		RequiredScopes:       []string{desktopoauth.DesktopOAuthScopeWorkAgent},
		RequireDeviceSession: true,
	}
}

// OAuthBearerAuth authenticates current OAuth-protected Desktop endpoints.
// Unlike JWTAuth it only accepts Authorization: Bearer, never cookies,
// never refreshes tokens, and returns RFC 6750-style 401 responses. During
// the credential rollover it preserves the existing client-ID admission
// contract, while shadow-evaluating Audience/Scope/Device Session claims.
func OAuthBearerAuth(requiredClientID string) gin.HandlerFunc {
	policy := DesktopResourceBearerPolicy(requiredClientID)
	return func(c *gin.Context) {
		claims, ok := authenticateOAuthBearer(c)
		if !ok {
			return
		}
		if err := validateCurrentOAuthClaims(claims, requiredClientID); err != nil {
			writeOAuthBearerError(c, "invalid_token", "token was not issued for this OAuth client")
			return
		}

		evaluation := OAuthResourceCredentialEvaluation{Compliant: true}
		if err := validateOAuthClaims(claims, policy); err != nil {
			evaluation.Compliant = false
			evaluation.FailureCode = credentialFailureCode(err)
		}
		c.Set(oauthResourceCredentialContextKey, evaluation)
		c.Set("claims", claims)
		c.Next()
	}
}

// OAuthBearerAuthWithPolicy authenticates a bearer token and enforces its
// intended resource boundary. It never accepts cookies and never refreshes a
// token in a response header. It is available for candidate routes but is not
// mounted on current Desktop resources during the compatibility rollover.
func OAuthBearerAuthWithPolicy(policy OAuthBearerPolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		claims, ok := authenticateOAuthBearer(c)
		if !ok {
			return
		}
		if err := validateOAuthClaims(claims, policy); err != nil {
			writeOAuthBearerError(c, "invalid_token", "token was not issued for this OAuth client")
			return
		}
		if err := validateActiveDeviceSession(c.Request.Context(), claims, policy); err != nil {
			writeOAuthBearerError(c, "invalid_token", "device session is inactive")
			return
		}

		c.Set("claims", claims)
		c.Next()
	}
}

func validateActiveDeviceSession(ctx context.Context, claims *request.CustomClaims, policy OAuthBearerPolicy) error {
	if !policy.RequireActiveDeviceSession {
		return nil
	}
	if policy.DeviceSessions == nil {
		return fmt.Errorf("active device session checker is required")
	}
	return policy.DeviceSessions.ValidateAccessSession(
		ctx,
		claims.BaseClaims.Id,
		claims.OAuthClientID,
		claims.DeviceID,
		claims.DeviceSessionID,
		claims.OAuthScope,
	)
}

func authenticateOAuthBearer(c *gin.Context) (*request.CustomClaims, bool) {
	token := bearerToken(c.GetHeader("Authorization"))
	if token == "" {
		writeOAuthBearerError(c, "invalid_token", "missing bearer token")
		return nil, false
	}

	claims, err := utils.NewJWT().ParseToken(token)
	if err != nil {
		if err == utils.TokenExpired {
			writeOAuthBearerError(c, "invalid_token", "token expired")
			return nil, false
		}
		writeOAuthBearerError(c, "invalid_token", "token is invalid")
		return nil, false
	}
	return claims, true
}

func validateCurrentOAuthClaims(claims *request.CustomClaims, requiredClientID string) error {
	if claims == nil {
		return fmt.Errorf("claims are missing")
	}
	if requiredClientID != "" && claims.OAuthClientID != requiredClientID {
		return fmt.Errorf("authorized party mismatch")
	}
	return nil
}

func validateOAuthClaims(claims *request.CustomClaims, policy OAuthBearerPolicy) error {
	if claims == nil || claims.BaseClaims.Id == 0 {
		return credentialFailure(OAuthCredentialMissingSubject)
	}
	if policy.ClientID != "" && claims.OAuthClientID != policy.ClientID {
		return credentialFailure(OAuthCredentialClientMismatch)
	}
	if policy.Audience != "" && !claims.VerifyAudience(policy.Audience, true) {
		return credentialFailure(OAuthCredentialAudienceMismatch)
	}
	if policy.CredentialType != "" && claims.CredentialType != policy.CredentialType {
		return credentialFailure(OAuthCredentialTypeMismatch)
	}
	if issuer := strings.TrimSpace(globals.GraConf.JWT.Issuer); issuer != "" && !claims.VerifyIssuer(issuer, true) {
		return credentialFailure(OAuthCredentialIssuerMismatch)
	}
	expectedSubject := "u_" + strconv.FormatUint(uint64(claims.BaseClaims.Id), 10)
	if claims.Subject != expectedSubject {
		return credentialFailure(OAuthCredentialSubjectMismatch)
	}
	if !hasAllScopes(claims.OAuthScope, policy.RequiredScopes) {
		return credentialFailure(OAuthCredentialScopeMismatch)
	}
	if policy.RequireDeviceSession && (strings.TrimSpace(claims.DeviceID) == "" || strings.TrimSpace(claims.DeviceSessionID) == "") {
		return credentialFailure(OAuthCredentialDeviceSessionAbsent)
	}
	return nil
}

func hasAllScopes(granted string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	grants := make(map[string]struct{}, len(strings.Fields(granted)))
	for _, scope := range strings.Fields(granted) {
		grants[scope] = struct{}{}
	}
	for _, scope := range required {
		if _, ok := grants[scope]; !ok {
			return false
		}
	}
	return true
}

func bearerToken(authorization string) string {
	if authorization == "" {
		return ""
	}
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	if parts[1] == "null" || parts[1] == "undefined" {
		return ""
	}
	return parts[1]
}

func writeOAuthBearerError(c *gin.Context, code, description string) {
	c.Header("WWW-Authenticate", fmt.Sprintf(`Bearer error="%s", error_description="%s"`, code, description))
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error":             code,
		"error_description": description,
	})
}

func OAuthClaims(c *gin.Context) (*request.CustomClaims, bool) {
	raw, ok := c.Get("claims")
	if !ok {
		return nil, false
	}
	claims, ok := raw.(*request.CustomClaims)
	return claims, ok
}

// OAuthResourceCredentialResult returns the strengthened-claim shadow
// evaluation for a compatibility-authenticated request.
func OAuthResourceCredentialResult(c *gin.Context) (OAuthResourceCredentialEvaluation, bool) {
	raw, ok := c.Get(oauthResourceCredentialContextKey)
	if !ok {
		return OAuthResourceCredentialEvaluation{}, false
	}
	evaluation, ok := raw.(OAuthResourceCredentialEvaluation)
	return evaluation, ok
}
