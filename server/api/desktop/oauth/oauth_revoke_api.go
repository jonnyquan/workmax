package oauth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	svc "server/service/desktop/oauth"
)

// Revoke handles POST /api/desktop/oauth/revoke. RFC 7009 token
// revocation. Sidecar's /auth/logout calls this so the refresh
// chain dies server-side instead of staying valid for its natural
// 90-day TTL.
//
// Wire shape (x-www-form-urlencoded per RFC 7009 §2.1):
//
//   POST /api/desktop/oauth/revoke
//   Content-Type: application/x-www-form-urlencoded
//
//   token=<refresh_token>&client_id=<workmax-desktop>&token_type_hint=refresh_token
//
// We only support token_type_hint=refresh_token (or absent — we treat
// every token as a refresh token because that's the only kind clients
// can present to us; access tokens are JWTs, not server-side state,
// so there's nothing to revoke for them — they expire in 15 min on
// their own).
//
// Response per RFC 7009 §2.2:
//
//   - 200 with empty body on success (including "token not found")
//   - 400 with {error, error_description} for client_id mismatch
//     or missing required params
//
// Always 200 for unrecognized tokens: per spec we don't want to leak
// "this token was valid" vs "this token never existed" via response
// shape. Telemetry can still distinguish via internal logs.
//
// Cache-Control headers same as /token responses (no-store) — the
// response carries no sensitive data but RFC 7009 §2.2 explicitly
// requires it ("MUST set the appropriate Cache-Control" / "MUST
// invalidate any cached responses").
func (a *OauthApi) Revoke(c *gin.Context) {
	writeTokenCacheHeaders(c)

	token := c.PostForm("token")
	clientID := c.PostForm("client_id")

	if token == "" || clientID == "" {
		writeTokenError(c, http.StatusBadRequest, "invalid_request",
			"token and client_id are required")
		return
	}

	// Optional hint per RFC 7009 §2.1. Only refresh_token is
	// meaningful in our model; access_token is a JWT with no
	// server-side state to revoke. If a client sends
	// token_type_hint=access_token we still accept the request (the
	// hint is advisory, not a requirement) but it'll fall through
	// to "token not found" because we don't store access tokens.
	hint := c.PostForm("token_type_hint")
	if hint != "" && hint != "refresh_token" && hint != "access_token" {
		writeTokenError(c, http.StatusBadRequest, "unsupported_token_type",
			"token_type_hint must be refresh_token or access_token")
		return
	}

	_, err := a.RefreshChainService.RevokeByToken(c.Request.Context(), token, clientID)
	if err != nil {
		switch {
		case errors.Is(err, svc.ErrRefreshTokenNotFound):
			// Per RFC 7009 §2.2 we return 200 on unrecognized tokens
			// to avoid leaking whether the token ever existed.
			c.Status(http.StatusOK)
			return
		case errors.Is(err, svc.ErrRefreshClientMismatch):
			// Distinct error so the calling client knows it presented
			// a token belonging to a different OAuth client. RFC 7009
			// §2.1 says we SHOULD validate; failing with invalid_grant
			// is the closest standard match.
			writeTokenError(c, http.StatusBadRequest, "invalid_grant",
				"client_id does not match the token chain")
			return
		default:
			// Unexpected DB / infra error. Log + 500. (The default
			// log path is via globals; we don't have a logger handle
			// here, so let writeTokenError's body carry the detail.)
			writeTokenError(c, http.StatusInternalServerError, "server_error",
				err.Error())
			return
		}
	}

	c.Status(http.StatusOK)
}
