package oauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"

	"server/globals"
	request "server/model/system/request"
	"server/utils"

	model "server/model/desktop/oauth"
	svc "server/service/desktop/oauth"
)

// OAuthAccessTokenTTL is how long an OAuth-issued access token is
// valid. Deliberately short (15 min, per backend-oauth §4.2) so the
// rotation-protected refresh token is the real long-lived credential.
const OAuthAccessTokenTTL = 15 * time.Minute

const maxDeviceInfoBytes = 2048

// tokenResponse is the success payload of POST /token. Field names
// follow RFC 6749 §4.1.4 wire shape.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"` // always "Bearer"
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int    `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
}

// tokenErrorResponse is the error payload (RFC 6749 §5.2). HTTP
// status code 400 for almost everything; 401 only when the client
// itself can't be authenticated (which doesn't apply to our public
// workmax-desktop client — no client_secret to fail).
type tokenErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// Token handles POST /api/desktop/oauth/token. Dispatches by
// grant_type to one of two grant implementations:
//
//	authorization_code → tokenAuthorizationCodeGrant
//	refresh_token      → tokenRefreshTokenGrant
//
// Anything else returns `unsupported_grant_type` per spec.
//
// Request body is x-www-form-urlencoded (RFC 6749 §4.1.3 / §6).
func (a *OauthApi) Token(c *gin.Context) {
	switch c.PostForm("grant_type") {
	case "authorization_code":
		a.tokenAuthorizationCodeGrant(c)
	case "refresh_token":
		a.tokenRefreshTokenGrant(c)
	case "":
		writeTokenError(c, http.StatusBadRequest, "invalid_request", "grant_type is required")
	default:
		writeTokenError(c, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// tokenAuthorizationCodeGrant handles the first leg: code + PKCE
// verifier → access + refresh.
//
// Steps (in this order — earlier failures don't leak whether a code
// existed, by design):
//
//  1. Read + presence-check params
//  2. Validate device_id format before consuming the code
//  3. Consume the code (atomic single-use)
//  4. Cross-check code's stored client_id and redirect_uri
//  5. Verify PKCE
//  6. Mint access token (15 min)
//  7. Issue refresh token (first in new chain)
//  8. Return 200 + tokenResponse JSON
func (a *OauthApi) tokenAuthorizationCodeGrant(c *gin.Context) {
	code := c.PostForm("code")
	clientID := c.PostForm("client_id")
	redirectURI := c.PostForm("redirect_uri")
	codeVerifier := c.PostForm("code_verifier")
	deviceID := c.PostForm("device_id")
	deviceInfoRaw := c.PostForm("device_info")

	// Required params per backend-oauth §2.2.
	if code == "" || clientID == "" || redirectURI == "" || codeVerifier == "" || deviceID == "" {
		writeTokenError(c, http.StatusBadRequest, "invalid_request", "code, client_id, redirect_uri, code_verifier, device_id are all required")
		return
	}
	if !isValidDeviceID(deviceID) {
		writeTokenError(c, http.StatusBadRequest, "invalid_request", "device_id must be a 32-character hex string")
		return
	}
	if err := validateDeviceInfo(deviceInfoRaw); err != nil {
		writeTokenError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	consumed, err := a.CodeService.ConsumeValidated(c.Request.Context(), svc.ConsumeValidatedInput{
		Code:         code,
		ClientID:     clientID,
		RedirectURI:  redirectURI,
		CodeVerifier: codeVerifier,
		DeviceID:     deviceID,
	})
	if err != nil {
		// Don't distinguish not-found / used / expired on the wire —
		// they all map to invalid_grant per RFC 6749 §5.2.
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, already used, or expired")
		return
	}

	// Client/redirect/PKCE bindings were checked before the atomic code
	// consumption. Keep policy revalidation below: an administratively disabled
	// client or removed scope must not mint a new session.
	client, err := a.ClientReg.FindActiveClient(c.Request.Context(), consumed.ClientID)
	if err != nil {
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "OAuth client is unavailable")
		return
	}
	canonicalScope, err := a.ClientReg.ValidateScopes(client, consumed.Scope)
	if err != nil {
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", "authorization scope is no longer available")
		return
	}
	consumed.Scope = canonicalScope

	a.issueTokenPair(c, uint(consumed.UID), consumed.ClientID, consumed.Scope, deviceID, deviceInfoRaw, "" /* new chain */)
}

func isValidDeviceID(deviceID string) bool {
	if len(deviceID) != 32 {
		return false
	}
	_, err := hex.DecodeString(deviceID)
	return err == nil
}

func validateDeviceInfo(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > maxDeviceInfoBytes {
		return fmt.Errorf("device_info must be at most %d bytes", maxDeviceInfoBytes)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return fmt.Errorf("device_info must be valid JSON")
	}
	if obj == nil {
		return fmt.Errorf("device_info must be a JSON object")
	}
	return nil
}

// tokenRefreshTokenGrant handles the second leg: presented refresh
// token → new access + new refresh (in same chain).
//
// All rotation failure modes map to invalid_grant per RFC 6749 §5.2.
// The replay-detected path is handled inside RefreshChainService.Rotate
// (sweeps the whole chain) — we just see the error and respond.
func (a *OauthApi) tokenRefreshTokenGrant(c *gin.Context) {
	refreshTok := c.PostForm("refresh_token")
	clientID := c.PostForm("client_id")

	if refreshTok == "" || clientID == "" {
		writeTokenError(c, http.StatusBadRequest, "invalid_request", "refresh_token and client_id are required")
		return
	}

	rotated, err := a.RefreshChainService.Rotate(c.Request.Context(), svc.RotateInput{
		PresentedToken: refreshTok,
		ClientID:       clientID,
	})
	if err != nil {
		// Map every refresh-chain error to invalid_grant. We could
		// log differently per sentinel (errors.Is checks against
		// ErrRefresh*) but the wire shape is uniform.
		desc := "refresh token is invalid, expired, or already revoked"
		switch {
		case errors.Is(err, svc.ErrRefreshClientMismatch):
			desc = "client_id does not match the refresh token chain"
		}
		writeTokenError(c, http.StatusBadRequest, "invalid_grant", desc)
		return
	}

	// Mint a fresh access token bound to the same chain's claims.
	access, accessExp, err := a.issueAccessToken(
		uint(rotated.UID),
		rotated.ClientID,
		rotated.Scope,
		rotated.DeviceID,
		rotated.ChainID,
	)
	if err != nil {
		writeTokenError(c, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	writeTokenCacheHeaders(c)
	c.JSON(http.StatusOK, tokenResponse{
		AccessToken:      access,
		TokenType:        "Bearer",
		ExpiresIn:        int(time.Until(accessExp).Seconds()),
		RefreshToken:     rotated.Token,
		RefreshExpiresIn: int(time.Until(rotated.ExpiresAt).Seconds()),
		Scope:            rotated.Scope,
	})
}

// issueTokenPair is the shared tail of authorization_code grant:
// generate access token + open new refresh chain + return JSON.
//
// chainID is normally "" so we mint a fresh one; tests can pass a
// known value to assert chain semantics.
func (a *OauthApi) issueTokenPair(
	c *gin.Context,
	uid uint, clientID, scope, deviceID, deviceInfoRaw, chainIDOverride string,
) {
	chainID := chainIDOverride
	if chainID == "" {
		chainID = newChainID()
	}

	access, accessExp, err := a.issueAccessToken(uid, clientID, scope, deviceID, chainID)
	if err != nil {
		writeTokenError(c, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	var deviceInfoPtr *string
	if deviceInfoRaw != "" {
		deviceInfoPtr = &deviceInfoRaw
	}

	issued, err := a.RefreshChainService.Issue(c.Request.Context(), svc.IssueInput{
		ChainID:    chainID,
		DeviceID:   deviceID,
		ClientID:   clientID,
		UID:        int(uid),
		Scope:      scope,
		DeviceInfo: deviceInfoPtr,
	})
	if err != nil {
		writeTokenError(c, http.StatusInternalServerError, "server_error", err.Error())
		return
	}

	writeTokenCacheHeaders(c)
	c.JSON(http.StatusOK, tokenResponse{
		AccessToken:      access,
		TokenType:        "Bearer",
		ExpiresIn:        int(time.Until(accessExp).Seconds()),
		RefreshToken:     issued.Token,
		RefreshExpiresIn: int(time.Until(issued.ExpiresAt).Seconds()),
		Scope:            scope,
	})
}

// issueAccessToken builds and signs an OAuth-flavored JWT. Differs
// from utils.NewJWT().CreateClaims() in three ways:
//
//  1. TTL is OAuthAccessTokenTTL (15 min) instead of the much
//     longer ExpiresTime from config.
//  2. Includes OAuthClientID, Audience, Scope, credential type, Device ID and
//     Device Session claims so resource middleware can distinguish it from a
//     legacy /api/auth/sign-in token.
//  3. Uses the refresh-chain ID as the stateful Device Session binding.
//
// Email + Nickname are intentionally empty — the OAuth flow doesn't
// look them up (avoids a DB round-trip per /token call). Code that
// reads claims.Email today is the legacy login path, which goes
// through CreateClaims with full BaseClaims.
func (a *OauthApi) issueAccessToken(
	uid uint,
	clientID, scope, deviceID, deviceSessionID string,
) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(OAuthAccessTokenTTL)
	if uid == 0 || clientID == "" || scope == "" || deviceID == "" || deviceSessionID == "" {
		return "", time.Time{}, fmt.Errorf("desktop access token requires user, client, scope, device and device session")
	}

	claims := request.CustomClaims{
		BaseClaims: request.BaseClaims{
			Id: uid,
		},
		OAuthClientID:   clientID,
		OAuthScope:      scope,
		CredentialType:  model.DesktopCredentialDeviceSession,
		DeviceID:        deviceID,
		DeviceSessionID: deviceSessionID,
		BufferTime:      0, // OAuth flow refreshes via refresh_token; no buffer-rebake
		StandardClaims: jwt.StandardClaims{
			Audience:  model.DesktopResourceAudience,
			Subject:   fmt.Sprintf("u_%d", uid),
			Id:        newChainID(),                     // jti — unique per token (security + uniqueness even within same second)
			NotBefore: now.Add(-5 * time.Second).Unix(), // tiny clock-skew tolerance
			IssuedAt:  now.Unix(),
			ExpiresAt: expiresAt.Unix(),
			Issuer:    globals.GraConf.JWT.Issuer,
		},
	}

	signed, err := utils.NewJWT().CreateToken(claims)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return signed, expiresAt, nil
}

// newChainID returns a 32-char hex string from 16 random bytes.
// Used as the chain_id for a brand-new refresh chain (one chain per
// fresh OAuth authorization flow).
func newChainID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand can fail only on systems with no entropy source
		// — at which point the process can't securely do anything.
		// Surface a clearly-flagged fallback so logs scream if we
		// ever hit it.
		return "rng-failed-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return hex.EncodeToString(b[:])
}

// writeTokenError serializes the RFC 6749 error envelope and sets
// the OAuth-required no-cache headers. Caller picks the HTTP status.
func writeTokenError(c *gin.Context, status int, errCode, desc string) {
	writeTokenCacheHeaders(c)
	c.JSON(status, tokenErrorResponse{
		Error:            errCode,
		ErrorDescription: desc,
	})
}

// writeTokenCacheHeaders sets the cache-busting headers RFC 6749 §5.1
// mandates for ALL /token responses (success + error). Proxies must
// not cache token responses since each is single-use / time-bound.
func writeTokenCacheHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
}

// Unused but kept so the import graph picks up the model package
// — handy when we add the /userinfo handler in P-1.6 which DOES
// touch model types.
var _ = model.DesktopClientID
