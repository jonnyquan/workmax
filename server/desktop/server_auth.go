//go:build desktop

package desktop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// server_auth.go houses handlers under /auth/* — session lifecycle
// for the desktop OAuth flow. Split out of server.go to keep each
// file scoped to one concern; same package, no behavior change.
//
// Endpoints:
//   - GET  /auth/status   — session state for the renderer's polling
//   - POST /auth/start    — kick off a fresh OAuth flow
//   - POST /auth/logout   — clear local session + best-effort revoke

// authStatusResponse is the shape of GET /auth/status. Keep this
// stable for the renderer's polling hook.
type authStatusResponse struct {
	State     string  `json:"state"`             // "unauthenticated" | "authenticated" | "expired"
	UserID    *string `json:"user_id,omitempty"` // reserved; account fields live on /auth/userinfo
	Tier      *string `json:"tier,omitempty"`    // reserved; account fields live on /auth/userinfo
	UpdatedAt string  `json:"updated_at"`        // ISO 8601 server timestamp; lets renderer detect stale polls
}

// handleAuthStatus reports the sidecar's view of session state.
// Reads the cached TokenPair from the Keychain (lazy-loaded on
// first call) and derives state from the expiries.
//
// We deliberately do NOT call workmax.app here — that would turn every
// renderer poll into a network round-trip. Renderer code that needs
// fresh user info calls the sidecar's /auth/userinfo endpoint, which
// does a user-initiated cloud round-trip.
func (s *Server) handleAuthStatus(c *gin.Context) {
	now := time.Now().UTC()
	resp := authStatusResponse{
		State:     "unauthenticated",
		UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if s.cfg.TokenStore == nil {
		c.JSON(http.StatusOK, resp)
		return
	}
	pair, err := s.cfg.TokenStore.Get()
	if err != nil {
		// ErrNoSession → keep state=unauthenticated; any other
		// error is unexpected but still surface as unauthenticated
		// to the renderer (rather than crashing the LoginPage).
		c.JSON(http.StatusOK, resp)
		return
	}
	if pair.AccessToken == "" {
		resp.State = "unauthenticated"
	} else if pair.IsRefreshExpired(now) {
		resp.State = "expired"
	} else {
		resp.State = "authenticated"
	}
	c.JSON(http.StatusOK, resp)
}

// handleAuthUserInfo returns the authenticated user's account snapshot
// by calling the cloud OAuth userinfo endpoint through the same token
// refresh path used by sync/chat. Unlike /auth/status, this endpoint is
// user-initiated from the Account panel, so a cloud round-trip is OK.
// One SessionLease covers token acquisition, HTTP, 401 recovery and the final
// response fence, preventing a prior login's account snapshot from crossing a
// same-UID re-login or logout boundary.
func (s *Server) handleAuthUserInfo(c *gin.Context) {
	if s.cfg.TokenStore == nil || s.cfg.Proxy == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
		return
	}
	cloud := s.cfg.Proxy.CloudClient()
	pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(c.Request.Context(), s.cfg.TokenStore, cloud)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		if errors.Is(err, cloudproxy.ErrNoSession) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session_unavailable"})
		return
	}
	leaseContext, releaseLease := lease.BindContext(c.Request.Context())
	defer releaseLease()

	info, err := cloud.UserInfo(leaseContext, pair.AccessToken)
	if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	if err != nil {
		if errors.Is(err, cloudproxy.ErrUserInfoAuthExpired) {
			info, err = s.refreshAndRetryUserInfo(leaseContext, cloud, pair, lease)
			if err == nil {
				if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
					writeSessionChanged(c)
					return
				}
				c.JSON(http.StatusOK, info)
				return
			}
			if errors.Is(err, cloudproxy.ErrNoSession) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
				return
			}
			if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
				writeSessionChanged(c)
				return
			}
			if errors.Is(err, cloudproxy.ErrUserInfoAuthExpired) {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
				return
			}
		}
		if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "userinfo_unavailable"})
		return
	}
	if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	c.JSON(http.StatusOK, info)
}

func (s *Server) refreshAndRetryUserInfo(
	ctx context.Context,
	cloud *cloudproxy.Client,
	stale *cloudproxy.TokenPair,
	lease cloudproxy.SessionLease,
) (cloudproxy.UserInfo, error) {
	newPair, err := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
		ctx,
		s.cfg.TokenStore,
		cloud,
		stale,
		lease,
	)
	if err != nil {
		return cloudproxy.UserInfo{}, err
	}
	if err := lease.Check(); err != nil {
		return cloudproxy.UserInfo{}, err
	}
	info, err := cloud.UserInfo(ctx, newPair.AccessToken)
	if err != nil {
		return cloudproxy.UserInfo{}, err
	}
	if err := lease.Check(); err != nil {
		return cloudproxy.UserInfo{}, err
	}
	return info, nil
}

func writeSessionChanged(c *gin.Context) {
	c.JSON(http.StatusConflict, gin.H{"error": "session_changed"})
}

// authStartResponse is the body of the deferred-compatibility POST
// /auth/start route. The bundled Renderer and current preload expose no caller
// for this legacy browser flow.
type authStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	AuthPort     int    `json:"auth_port"` // for logs / display only
}

// handleAuthStart begins the retained legacy OAuth flow for a compatible,
// privileged local caller. Current bundled sign-in uses the password Login
// Transaction routes instead and never calls this handler.
//
// The flow continues in handleAuthLogout's sibling code: once the
// user completes consent, the loopback callback fires, the sidecar
// exchanges the code for tokens, and saves them to Keychain. The
// renderer learns about completion by polling /auth/status.
//
// Concurrency: OAuthFlow rejects a second Start() while a first is
// pending — that maps to 409 Conflict here.
func (s *Server) handleAuthStart(c *gin.Context) {
	if s.cfg.OAuthFlow == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth flow not configured"})
		return
	}
	if s.authClosing.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sidecar is shutting down"})
		return
	}
	scope, err := authStartScopeFromRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.authLifecycleMu.Lock()
	defer s.authLifecycleMu.Unlock()
	if s.authClosing.Load() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sidecar is shutting down"})
		return
	}
	// The legacy and Login Transaction coordinators share one Keychain slot.
	// Starting either flow supersedes and fully fences the other.
	if s.cfg.LoginCoordinator != nil {
		s.cfg.LoginCoordinator.Cancel()
	}
	operationContext, operationCancel := s.authOperationContext(c.Request.Context())
	defer operationCancel()
	start, err := s.cfg.OAuthFlow.Start(operationContext, scope)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	// Request cancellation is propagated through an AfterFunc because the
	// Sidecar lifetime is the operation's direct parent. Re-check both sources
	// before detaching the long-lived callback waiter so a disconnect or
	// Shutdown in that narrow scheduling window cannot leave an abandoned flow.
	if operationContext.Err() != nil || c.Request.Context().Err() != nil || s.authClosing.Load() {
		s.cfg.OAuthFlow.Cancel()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "oauth start canceled"})
		return
	}
	if err := validateAuthStartResult(start); err != nil {
		s.cfg.OAuthFlow.Cancel()
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// Spawn a background goroutine to wait for the callback +
	// exchange. The renderer polls /auth/status to learn when it
	// finished; we don't make the renderer wait synchronously on
	// /auth/start because the OAuth flow can take minutes.
	go func() {
		_, _ = s.cfg.OAuthFlow.Wait(context.Background(), start)
		// Errors are surfaced via /auth/status reverting to
		// "unauthenticated"; we don't log here because the cloud_proxy
		// layer already logs through the stderr logger.
	}()

	c.JSON(http.StatusOK, authStartResponse{
		AuthorizeURL: start.AuthorizeURL,
		AuthPort:     start.AuthPort,
	})
}

func authStartScopeFromRequest(r *http.Request) (string, error) {
	if err := r.ParseForm(); err != nil {
		return "", fmt.Errorf("oauth start: parse scope: %w", err)
	}
	values, ok := r.PostForm["scope"]
	if !ok {
		return "workagent", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("oauth start: scope must appear at most once")
	}
	scope := values[0]
	if scope == "" {
		return "", fmt.Errorf("oauth start: scope must not be empty")
	}
	if strings.TrimSpace(scope) != scope || containsControl(scope) {
		return "", fmt.Errorf("oauth start: scope is malformed")
	}
	if scope != "workagent" {
		return "", fmt.Errorf("oauth start: unsupported scope")
	}
	return scope, nil
}

func validateAuthStartResult(start cloudproxy.StartResult) error {
	if start.AuthPort < 1 || start.AuthPort > 65535 {
		return fmt.Errorf("oauth start: invalid auth_port")
	}
	authorizeURL, err := url.Parse(start.AuthorizeURL)
	if err != nil {
		return fmt.Errorf("oauth start: invalid authorize_url")
	}
	if authorizeURL.Scheme != "http" && authorizeURL.Scheme != "https" {
		return fmt.Errorf("oauth start: authorize_url must use http or https")
	}
	if authorizeURL.Host == "" {
		return fmt.Errorf("oauth start: authorize_url must include host")
	}
	if authorizeURL.User != nil {
		return fmt.Errorf("oauth start: authorize_url must not include credentials")
	}
	if authorizeURL.Fragment != "" {
		return fmt.Errorf("oauth start: authorize_url must not include fragment")
	}
	if authorizeURL.Path != cloudproxy.CloudRouteOAuthAuthorize {
		return fmt.Errorf("oauth start: authorize_url must point to %s", cloudproxy.CloudRouteOAuthAuthorize)
	}

	redirectValues := authorizeURL.Query()["redirect_uri"]
	if len(redirectValues) != 1 || redirectValues[0] == "" {
		return fmt.Errorf("oauth start: authorize_url missing redirect_uri")
	}
	redirectRaw := redirectValues[0]
	redirectURI, err := url.Parse(redirectRaw)
	if err != nil {
		return fmt.Errorf("oauth start: invalid redirect_uri")
	}
	if redirectURI.Scheme != "http" {
		return fmt.Errorf("oauth start: redirect_uri must use http")
	}
	if redirectURI.Hostname() != "127.0.0.1" {
		return fmt.Errorf("oauth start: redirect_uri must use 127.0.0.1")
	}
	if redirectURI.Port() != strconv.Itoa(start.AuthPort) {
		return fmt.Errorf("oauth start: redirect_uri port must match auth_port")
	}
	if redirectURI.User != nil {
		return fmt.Errorf("oauth start: redirect_uri must not include credentials")
	}
	if redirectURI.Path != "/oauth/callback" {
		return fmt.Errorf("oauth start: redirect_uri must point to /oauth/callback")
	}
	if redirectURI.RawQuery != "" || redirectURI.Fragment != "" {
		return fmt.Errorf("oauth start: redirect_uri must not include query or fragment")
	}
	return nil
}

// handleAuthLogout clears the local session. Idempotent — calling
// when not logged in is fine.
//
// Order matters here:
//  1. Cancel and generation-fence pending legacy OAuth and Login Transaction
//     producers while holding authLifecycleMu.
//  2. Snapshot and fence the current TokenStore epoch before waiting for the
//     refresh gate, immediately canceling already-authorized cloud work.
//  3. Enter the refresh gate and POST cloud /api/desktop/oauth/revoke with the
//     retired snapshot — best-effort, see below.
//  4. Clear Keychain before leaving the refresh gate. If the gate wait exceeds
//     the logout budget, clear immediately and let the stale guarded refresh
//     fail its later commit.
//
// Why best-effort revoke: a network failure during logout shouldn't
// prevent the user from clearing their local session. If the cloud
// is unreachable, we still clear Keychain so the renderer routes
// back to LoginPage; the refresh chain will eventually time out
// (90 days) on its own. The cost of NOT clearing Keychain on
// network failure is worse — user clicks "Logout", nothing visible
// happens, they're stuck.
func (s *Server) handleAuthLogout(c *gin.Context) {
	if s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token store not configured"})
		return
	}
	s.authLifecycleMu.Lock()
	defer s.authLifecycleMu.Unlock()

	// 1. Cancel and generation-fence all login producers before retiring the
	// current session. authLifecycleMu prevents a new handler from starting one
	// until LogoutSession has completed.
	if s.cfg.OAuthFlow != nil {
		s.cfg.OAuthFlow.Cancel()
	}
	if s.cfg.LoginCoordinator != nil {
		s.cfg.LoginCoordinator.Cancel()
	}
	// Retire the model gateway credential in the same breath, and before the
	// revoke round trip: a local agent subprocess started while signed in
	// holds an "API key" for this sidecar, and logout must mean it stops
	// working now — including for the turn currently streaming — rather than
	// whenever that subprocess happens to exit.
	s.rotateModelGatewayToken("logout")

	// 2–4. Pre-fence, best-effort revoke, and local clear coordinate with the
	// same refresh gate as proactive and 401-driven rotation. The pre-fence
	// cancels a cooperative in-flight refresh immediately; a non-cooperative
	// owner cannot keep logout from performing a deadline-bounded local clear.
	var cloud *cloudproxy.Client
	if s.cfg.Proxy != nil {
		cloud = s.cfg.Proxy.CloudClient()
	}
	revokeCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	result, err := cloudproxy.LogoutSession(revokeCtx, s.cfg.TokenStore, cloud)
	if err != nil {
		// Keychain clear failed, but TokenStore has already installed an
		// authoritative in-process logout tombstone. Return only a stable public
		// code; a Keychain implementation's raw error must never cross to the
		// Renderer.
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":         "local_session_cleanup_failed",
			"revoke_status": result.RevokeStatus,
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":            true,
		"revoke_status": result.RevokeStatus,
	})
}
