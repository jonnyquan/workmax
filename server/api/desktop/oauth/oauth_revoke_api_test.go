package oauth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	model "server/model/desktop/oauth"
)

// newTestApiWithRevoke spins up the OauthApi with POST /token AND
// POST /revoke. We reuse the token API setup because most revoke
// tests need to first acquire a real refresh token via /token before
// they can revoke it.
func newTestApiWithRevoke(t *testing.T) (*gin.Engine, *OauthApi) {
	t.Helper()
	r, api := newTestApiWithToken(t)
	r.POST("/api/desktop/oauth/revoke", api.Revoke)
	return r, api
}

// postRevokeForm sends a x-www-form-urlencoded POST to /revoke and
// returns the recorder. Mirrors the postTokenForm helper.
func postRevokeForm(r *gin.Engine, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/oauth/revoke", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

// acquireRefreshToken runs the auth-code grant on the given engine
// and returns the (refresh_token, access_token) pair for a fresh
// chain. Tests use this to set up a revocable token without
// duplicating the full grant flow.
func acquireRefreshToken(t *testing.T, r *gin.Engine, api *OauthApi, uid int, scope string) (refreshTok, accessTok string) {
	t.Helper()
	code := seedAuthCode(t, api, uid, scope)
	form := validTokenFormCodeGrant(code)
	w := postTokenForm(r, form)
	if w.Code != http.StatusOK {
		t.Fatalf("seed: token grant failed: status=%d body=%s", w.Code, w.Body.String())
	}
	body := decodeTokenJSON(t, w)
	return body.RefreshToken, body.AccessToken
}

func TestRevoke_HappyPath(t *testing.T) {
	r, api := newTestApiWithRevoke(t)
	refreshTok, _ := acquireRefreshToken(t, r, api, 42, "workagent")

	w := postRevokeForm(r, url.Values{
		"token":     {refreshTok},
		"client_id": {model.DesktopClientID},
	})
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	// Empty body on success.
	if w.Body.Len() != 0 {
		t.Errorf("body should be empty, got %q", w.Body.String())
	}

	// Reusing the same refresh token should now fail (chain revoked).
	rotateForm := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshTok},
		"client_id":     {model.DesktopClientID},
	}
	rotate := postTokenForm(r, rotateForm)
	if rotate.Code != http.StatusBadRequest {
		t.Errorf("post-revoke rotation: got status %d, want 400 (body: %s)",
			rotate.Code, rotate.Body.String())
	}
	if e := decodeTokenError(t, rotate); e.Error != "invalid_grant" {
		t.Errorf("post-revoke rotation error: got %q, want invalid_grant", e.Error)
	}
}

func TestRevoke_RejectsMissingParams(t *testing.T) {
	r, _ := newTestApiWithRevoke(t)
	cases := []struct {
		name string
		form url.Values
	}{
		{"missing token", url.Values{"client_id": {model.DesktopClientID}}},
		{"missing client_id", url.Values{"token": {"some-token"}}},
		{"both missing", url.Values{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postRevokeForm(r, tc.form)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
			if e := decodeTokenError(t, w); e.Error != "invalid_request" {
				t.Errorf("error: got %q, want invalid_request", e.Error)
			}
		})
	}
}

func TestRevoke_UnknownTokenReturns200(t *testing.T) {
	// RFC 7009 §2.2: MUST return 200 for unrecognized tokens so the
	// response shape doesn't leak whether a token ever existed.
	r, _ := newTestApiWithRevoke(t)
	w := postRevokeForm(r, url.Values{
		"token":     {"definitely-not-a-real-token"},
		"client_id": {model.DesktopClientID},
	})
	if w.Code != http.StatusOK {
		t.Errorf("unknown token: got %d, want 200 (RFC 7009 §2.2)", w.Code)
	}
}

func TestRevoke_ClientMismatch(t *testing.T) {
	// A token belonging to workmax-desktop client cannot be revoked by
	// presenting client_id="someone-else". RFC 7009 §2.1 SHOULD check.
	r, api := newTestApiWithRevoke(t)
	refreshTok, _ := acquireRefreshToken(t, r, api, 42, "workagent")
	w := postRevokeForm(r, url.Values{
		"token":     {refreshTok},
		"client_id": {"different-client"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400 (client mismatch)", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error: got %q, want invalid_grant", e.Error)
	}
}

func TestRevoke_TokenTypeHintAccepted(t *testing.T) {
	r, api := newTestApiWithRevoke(t)
	refreshTok, _ := acquireRefreshToken(t, r, api, 42, "workagent")

	w := postRevokeForm(r, url.Values{
		"token":           {refreshTok},
		"client_id":       {model.DesktopClientID},
		"token_type_hint": {"refresh_token"},
	})
	if w.Code != http.StatusOK {
		t.Errorf("with refresh_token hint: got %d, want 200", w.Code)
	}
}

func TestRevoke_TokenTypeHintRejected(t *testing.T) {
	r, _ := newTestApiWithRevoke(t)
	w := postRevokeForm(r, url.Values{
		"token":           {"any"},
		"client_id":       {model.DesktopClientID},
		"token_type_hint": {"some_unknown_type"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown hint: got %d, want 400", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "unsupported_token_type" {
		t.Errorf("error: got %q, want unsupported_token_type", e.Error)
	}
}

func TestRevoke_Idempotent(t *testing.T) {
	r, api := newTestApiWithRevoke(t)
	refreshTok, _ := acquireRefreshToken(t, r, api, 42, "workagent")

	// First revoke succeeds.
	w1 := postRevokeForm(r, url.Values{
		"token":     {refreshTok},
		"client_id": {model.DesktopClientID},
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("first revoke: %d", w1.Code)
	}
	// Second revoke (same token, already revoked) also returns 200.
	w2 := postRevokeForm(r, url.Values{
		"token":     {refreshTok},
		"client_id": {model.DesktopClientID},
	})
	if w2.Code != http.StatusOK {
		t.Errorf("second revoke (idempotent): got %d, want 200", w2.Code)
	}
}

func TestRevoke_SetsCacheHeaders(t *testing.T) {
	r, api := newTestApiWithRevoke(t)
	refreshTok, _ := acquireRefreshToken(t, r, api, 42, "workagent")

	w := postRevokeForm(r, url.Values{
		"token":     {refreshTok},
		"client_id": {model.DesktopClientID},
	})
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store (RFC 7009 §2.2)", got)
	}
	if got := w.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma: got %q, want no-cache", got)
	}
}
