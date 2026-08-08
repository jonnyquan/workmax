// Package oauth holds the HTTP handlers for the /api/desktop/oauth/*
// endpoints. It is a thin Desktop wire layer that delegates to
// server/service/desktop/oauth.
//
// Design refs:
//
//	ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md
package oauth

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/utils"

	svc "server/service/desktop/oauth"
)

// OauthApi is the HTTP handler bundle. Holds the long-lived services
// (constructed once at app startup, not per request). Tests construct
// this directly with a test DB and call the handler methods.
//
// The struct deliberately exposes DB and services so test harnesses
// can wire them without going through globals.GraDBs. Production
// uses NewOauthApi which wires from globals.
//
// AuthVerifier is the injection seam for JWT verification: production
// uses utils.NewJWT().ParseToken; tests can swap in a stub that
// returns a fixed uid without setting up globals.GraConf.
type OauthApi struct {
	DB                  *gorm.DB
	ClientReg           *svc.ClientRegistry
	CodeService         *svc.CodeService
	PendingService      *svc.PendingAuthorizationService
	RefreshChainService *svc.RefreshChainService
	AuthVerifier        func(token string) (uid uint, ok bool)
}

// NewOauthApi constructs an OauthApi with services backed by the
// given *gorm.DB. Wire at app startup with globals.GraDBs["system"].
func NewOauthApi(db *gorm.DB) *OauthApi {
	return &OauthApi{
		DB:                  db,
		ClientReg:           svc.NewClientRegistry(db),
		CodeService:         svc.NewCodeService(db),
		PendingService:      svc.NewPendingAuthorizationService(),
		RefreshChainService: svc.NewRefreshChainService(db),
		AuthVerifier:        defaultJWTVerifier,
	}
}

// defaultJWTVerifier parses a JWT using workmax's utils.NewJWT() (which
// reads SigningKey from globals.GraConf). Returns ok=false on any
// failure path.
func defaultJWTVerifier(token string) (uint, bool) {
	if token == "" {
		return 0, false
	}
	claims, err := utils.NewJWT().ParseToken(token)
	if err != nil || claims == nil || claims.BaseClaims.Id == 0 {
		return 0, false
	}
	// claims.BaseClaims.Id rather than claims.Id — jwt.StandardClaims
	// also defines Id (the jti) so the bare selector is ambiguous.
	return claims.BaseClaims.Id, true
}

// authorizeRequest is the validated form of the GET /authorize query
// string. State is required by the first-party Desktop contract even though
// OAuth describes it as recommended; it binds the loopback callback to the
// initiating Sidecar flow.
type authorizeRequest struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Scope               string
}

func parseAuthorizeRequest(c *gin.Context) authorizeRequest {
	return authorizeRequest{
		ResponseType:        c.Query("response_type"),
		ClientID:            c.Query("client_id"),
		RedirectURI:         c.Query("redirect_uri"),
		CodeChallenge:       c.Query("code_challenge"),
		CodeChallengeMethod: c.Query("code_challenge_method"),
		State:               c.Query("state"),
		Scope:               strings.TrimSpace(c.Query("scope")),
	}
}

// validateAuthorizeRequest checks the structural OAuth-spec fields.
// Returns nil if the request looks well-formed enough to proceed to
// client lookup; otherwise an HTML-renderable error message.
//
// Specifically does NOT validate client_id or redirect_uri — those
// require DB lookup and the validation rules differ (per OAuth spec,
// missing/invalid client_id MUST NOT redirect, while missing
// response_type CAN redirect with error).
func validateAuthorizeRequest(r authorizeRequest) error {
	if r.ResponseType != "code" {
		return fmt.Errorf("response_type must be 'code' (got %q)", r.ResponseType)
	}
	if r.ClientID == "" {
		return errors.New("client_id is required")
	}
	if r.RedirectURI == "" {
		return errors.New("redirect_uri is required")
	}
	if r.CodeChallenge == "" {
		return errors.New("code_challenge is required (PKCE)")
	}
	if !validS256Challenge(r.CodeChallenge) {
		return errors.New("code_challenge must be a 43-128 character base64url value")
	}
	if r.CodeChallengeMethod != "S256" {
		return fmt.Errorf("code_challenge_method must be 'S256' (got %q)", r.CodeChallengeMethod)
	}
	if r.State == "" {
		return errors.New("state is required")
	}
	return nil
}

func validS256Challenge(value string) bool {
	if len(value) < svc.PKCEVerifierMinLen || len(value) > svc.PKCEVerifierMaxLen {
		return false
	}
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '_' {
			continue
		}
		return false
	}
	return true
}

// Authorize handles GET /api/desktop/oauth/authorize.
//
// Flow:
//  1. Parse + validate structural request params
//  2. Look up client (must exist + active)
//  3. Validate redirect_uri against client's loopback patterns
//  4. Check authentication via JWT (cookie or Authorization header)
//  5. If not authenticated → fail closed until Desktop login exists
//  6. Store pending request + render consent HTML form
//
// On all error paths we render plain HTML rather than redirecting to
// the redirect_uri, because at the points where errors happen we
// either don't know the redirect_uri yet OR we don't trust it (it's
// what failed validation). RFC 6749 §4.1.2.1 mandates this.
func (a *OauthApi) Authorize(c *gin.Context) {
	setAuthorizationResponseHeaders(c)

	req := parseAuthorizeRequest(c)
	if err := validateAuthorizeRequest(req); err != nil {
		renderErrorHTML(c, http.StatusBadRequest, "Invalid request", err.Error())
		return
	}

	client, err := a.ClientReg.FindActiveClient(c.Request.Context(), req.ClientID)
	if err != nil {
		// Don't leak "client not found" vs "client inactive" — same
		// surface message for both, per backend-oauth §7.
		renderErrorHTML(c, http.StatusBadRequest, "Unknown client", "The OAuth client is not registered or has been disabled.")
		return
	}
	if err := a.ClientReg.ValidateRedirectURI(client, req.RedirectURI); err != nil {
		renderErrorHTML(c, http.StatusBadRequest, "Invalid redirect_uri", err.Error())
		return
	}

	// Default scope if omitted.
	if req.Scope == "" {
		req.Scope = "workagent"
	}
	canonicalScope, err := a.ClientReg.ValidateScopes(client, req.Scope)
	if err != nil {
		renderErrorHTML(c, http.StatusBadRequest, "Invalid scope", "The requested permissions are not available to this client.")
		return
	}
	req.Scope = canonicalScope

	uid, ok := a.getAuthenticatedUID(c)
	if !ok {
		// Do not redirect to a generic browser login. The repository has no
		// separate Web client, and the supported Desktop login transaction
		// has not been implemented yet.
		renderLoginRequiredHTML(c)
		return
	}

	id, err := a.PendingService.Store(c.Request.Context(), svc.StoreInput{
		ClientID:            req.ClientID,
		UID:                 int(uid),
		RedirectURI:         req.RedirectURI,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		Scope:               req.Scope,
		State:               req.State,
	})
	if err != nil {
		renderErrorHTML(c, http.StatusInternalServerError, "Server error", err.Error())
		return
	}

	renderConsentHTML(c, consentViewModel{
		RequestID:  id,
		ClientName: client.ClientName,
		Scope:      req.Scope,
	})
}

// Consent handles POST /api/desktop/oauth/authorize/consent.
//
// Reads the hidden form ID + action (approve | deny). On approve,
// generates an auth code and 302-redirects to the registered
// redirect_uri with `?code=...&state=...`. On deny, redirects with
// `?error=access_denied&state=...` per RFC 6749 §4.1.2.1.
//
// This remains a compatibility-only early-access path: it does not yet
// re-bind the POST to the authenticated UID/device session or a CSRF token.
// It must not be treated as public-release Desktop login.
func (a *OauthApi) Consent(c *gin.Context) {
	setAuthorizationResponseHeaders(c)

	id := c.PostForm("code_grant_request_id")
	action := c.PostForm("action")
	if action != "approve" && action != "deny" {
		renderErrorHTML(c, http.StatusBadRequest, "Invalid action", fmt.Sprintf("Expected approve or deny, got %q", action))
		return
	}

	pa, err := a.PendingService.Consume(c.Request.Context(), id)
	if err != nil {
		renderErrorHTML(c, http.StatusBadRequest, "Session expired", "Your authorization request expired or has already been consumed. Restart the flow from the desktop app.")
		return
	}

	switch action {
	case "deny":
		c.Redirect(http.StatusFound, buildErrorRedirect(pa.RedirectURI, "access_denied", pa.State))
		return
	case "approve":
		// fall through
	}

	g, err := a.CodeService.Generate(c.Request.Context(), svc.GenerateInput{
		ClientID:            pa.ClientID,
		UID:                 pa.UID,
		RedirectURI:         pa.RedirectURI,
		CodeChallenge:       pa.CodeChallenge,
		CodeChallengeMethod: pa.CodeChallengeMethod,
		Scope:               pa.Scope,
	})
	if err != nil {
		renderErrorHTML(c, http.StatusInternalServerError, "Server error", err.Error())
		return
	}

	c.Redirect(http.StatusFound, buildCodeRedirect(pa.RedirectURI, g.Code, pa.State))
}

// getAuthenticatedUID extracts the workmax user id from the request's
// JWT. Reads either the Authorization Bearer header (programmatic
// clients) or the access_token cookie (browser navigation, the
// common path for /authorize).
//
// Returns ok=false on any failure path — handlers should treat that
// uniformly as "not logged in" rather than peeking at the parse
// error.
//
// Verification goes through a.AuthVerifier so tests can stub it
// without bootstrapping globals.GraConf.
func (a *OauthApi) getAuthenticatedUID(c *gin.Context) (uint, bool) {
	token := ""
	if auth := c.GetHeader("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token = strings.TrimPrefix(auth, "Bearer ")
	}
	if token == "" {
		if cookieToken, err := c.Cookie("access_token"); err == nil {
			token = cookieToken
		}
	}
	if a.AuthVerifier == nil {
		// Defensive default — should never hit in practice (constructor
		// installs defaultJWTVerifier).
		return defaultJWTVerifier(token)
	}
	return a.AuthVerifier(token)
}

// buildCodeRedirect constructs `redirect_uri?code=X&state=Y` (state
// only included when non-empty per RFC 6749).
func buildCodeRedirect(redirectURI, code, state string) string {
	return appendQuery(redirectURI, map[string]string{"code": code, "state": state})
}

// buildErrorRedirect constructs `redirect_uri?error=E&state=Y`.
func buildErrorRedirect(redirectURI, errCode, state string) string {
	return appendQuery(redirectURI, map[string]string{"error": errCode, "state": state})
}

// appendQuery merges params into the URL's query string, skipping
// empty values.
func appendQuery(rawURI string, params map[string]string) string {
	u, err := url.Parse(rawURI)
	if err != nil {
		// Fallback: caller validated redirect_uri earlier, but if
		// parsing somehow fails now, return the raw URI so the
		// browser at least sees something rather than a server crash.
		return rawURI
	}
	q := u.Query()
	for k, v := range params {
		if v == "" {
			continue
		}
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// === HTML rendering ===

type consentViewModel struct {
	RequestID  string
	ClientName string
	Scope      string
}

func setAuthorizationResponseHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

// consentTemplate is the current inline OAuth consent page. P1
// early-access keeps it server-rendered so the Desktop OAuth flow
// has no dependency on a separate client route. A public-release pass
// must replace it with a hardened Server-owned identity/consent flow.
//
// The form posts to a sibling endpoint (relative URL) so this
// template doesn't need to know its own mount path.
const consentTemplate = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Authorize {{.ClientName}}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#1d1d1f">
  <h2 style="font-size:22px;font-weight:600;margin-bottom:8px">{{.ClientName}} wants access</h2>
  <p style="color:#86868b;font-size:14px;margin-bottom:20px">It will be able to:</p>
  <ul style="font-size:14px;line-height:1.6;padding-left:20px">
    <li>Use your workmax account to power the WorkAgent assistant ({{.Scope}})</li>
    <li>Read &amp; write your WorkAgent threads, messages, and workspace files</li>
  </ul>
  <p style="color:#86868b;font-size:12px;margin:20px 0">If you didn't initiate this from WorkMax Desktop, click Cancel.</p>
  <form method="POST" action="./consent" style="display:flex;gap:12px;margin-top:24px">
    <input type="hidden" name="code_grant_request_id" value="{{.RequestID}}">
    <button type="submit" name="action" value="approve"
      style="flex:1;padding:10px 16px;border:none;border-radius:8px;background:#0071e3;color:white;font-size:15px;cursor:pointer">Authorize</button>
    <button type="submit" name="action" value="deny"
      style="flex:1;padding:10px 16px;border:1px solid #d2d2d7;border-radius:8px;background:white;color:#1d1d1f;font-size:15px;cursor:pointer">Cancel</button>
  </form>
</body>
</html>`

var consentTmpl = template.Must(template.New("consent").Parse(consentTemplate))

func renderConsentHTML(c *gin.Context, vm consentViewModel) {
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = consentTmpl.Execute(c.Writer, vm)
}

const errorTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#1d1d1f">
  <h2 style="font-size:20px;color:#d70015">{{.Title}}</h2>
  <p style="font-size:14px;line-height:1.5">{{.Detail}}</p>
</body></html>`

var errorTmpl = template.Must(template.New("error").Parse(errorTemplate))

type errorViewModel struct {
	Title  string
	Detail string
}

func renderErrorHTML(c *gin.Context, status int, title, detail string) {
	c.Status(status)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = errorTmpl.Execute(c.Writer, errorViewModel{Title: title, Detail: detail})
}

const loginRequiredTemplate = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Login required</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:480px;margin:80px auto;padding:0 24px;color:#1d1d1f">
  <h2 style="font-size:20px">Desktop sign-in is not available</h2>
  <p style="font-size:14px;line-height:1.5;margin-bottom:20px">This Server does not yet provide the complete Desktop first-sign-in flow. Close this window and ask the Server operator to enable a supported identity flow.</p>
</body></html>`

var loginRequiredTmpl = template.Must(template.New("login_required").Parse(loginRequiredTemplate))

func renderLoginRequiredHTML(c *gin.Context) {
	c.Status(http.StatusUnauthorized)
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = loginRequiredTmpl.Execute(c.Writer, nil)
}
