package oauth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestE2E_FullFlowWithUserInfo is the P-1.7 integration test:
// walks the entire 5-step OAuth dance against an in-memory SQLite
// + a real Gin engine, exercising every handler P-1.1 through P-1.6
// produced. Pin-tests the cross-PR invariants in one place so
// regressions during P-1.7+ refactors trip a single failure rather
// than scattering across per-PR test files.
//
// Steps:
//  1. GET  /api/desktop/oauth/authorize           (P-1.4)
//  2. POST /api/desktop/oauth/authorize/consent   (P-1.4)
//  3. POST /api/desktop/oauth/token (code grant)  (P-1.5)
//  4. GET  /api/desktop/oauth/userinfo            (P-1.6, OAuth Bearer-gated)
//  5. POST /api/desktop/oauth/token (refresh)     (P-1.5)
//
// What this catches that the per-PR tests don't:
//   - access token from /token actually decodes + survives OAuth
//     Bearer middleware on /userinfo (the wire-level integration that no
//     in-package test exercises end-to-end)
//   - tier/email/etc from the seeded user actually surface in
//     /userinfo when reached via the real OAuth flow (not just
//     when /userinfo is called directly with a hand-minted token)
//   - rotation produces an access token that ALSO works on /userinfo
//     (i.e. nothing about rotation breaks the JWT claims layout)
func TestE2E_FullFlowWithUserInfo(t *testing.T) {
	r, api, user := newTestApiWithUserInfo(t)

	// Step 1: GET /authorize as the seeded logged-in user
	authQ := url.Values{
		"response_type":         {"code"},
		"client_id":             {"workmax-desktop"},
		"redirect_uri":          {"http://127.0.0.1:54321/oauth/callback"},
		"code_challenge":        {testCodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"e2e-state-token"},
		"scope":                 {"workagent"},
	}
	// AuthVerifier is the test stub from newTestApi that accepts
	// "user-N" as the cookie value for uid=N. Repoint at user.Id so
	// /authorize maps the cookie to the seeded user.
	api.AuthVerifier = func(token string) (uint, bool) {
		if token == "seeded-user" {
			return user.Id, true
		}
		return 0, false
	}

	w1 := doGet(r, "/api/desktop/oauth/authorize", authQ, map[string]string{
		"access_token": "seeded-user",
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("step 1 /authorize: status %d, body %s", w1.Code, w1.Body.String())
	}
	// Extract pending id from the consent HTML.
	pendingID := mustExtractPendingID(t, w1.Body.String())

	// Step 2: POST /consent action=approve
	w2 := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {pendingID},
		"action":                {"approve"},
	})
	if w2.Code != http.StatusFound {
		t.Fatalf("step 2 /consent: status %d, body %s", w2.Code, w2.Body.String())
	}
	loc, err := url.Parse(w2.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if loc.Query().Get("state") != "e2e-state-token" {
		t.Errorf("state echo broken: got %q", loc.Query().Get("state"))
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("step 2 missing ?code= in redirect")
	}

	// Step 3: POST /token grant=authorization_code
	w3 := postTokenForm(r, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:54321/oauth/callback"},
		"client_id":     {"workmax-desktop"},
		"code_verifier": {testCodeVerifier},
		"device_id":     {testDeviceIDE2E},
		"device_info":   {`{"os":"darwin","app_version":"0.0.6-e2e"}`},
	})
	if w3.Code != http.StatusOK {
		t.Fatalf("step 3 /token: status %d, body %s", w3.Code, w3.Body.String())
	}
	tok := decodeTokenJSON(t, w3)
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("step 3 missing tokens")
	}

	// Step 4: GET /userinfo with the access token from step 3
	w4 := doGetAuthorized(r, "/api/desktop/oauth/userinfo", tok.AccessToken)
	if w4.Code != http.StatusOK {
		t.Fatalf("step 4 /userinfo: status %d, body %s", w4.Code, w4.Body.String())
	}
	var ui userinfoResponse
	if err := json.Unmarshal(w4.Body.Bytes(), &ui); err != nil {
		t.Fatalf("step 4 decode: %v, body: %s", err, w4.Body.String())
	}
	if ui.Email != user.Email {
		t.Errorf("/userinfo email: got %q, want %q", ui.Email, user.Email)
	}
	if ui.DisplayName != user.Nickname {
		t.Errorf("/userinfo display_name: got %q, want %q", ui.DisplayName, user.Nickname)
	}
	if ui.Tier != "pro" {
		t.Errorf("/userinfo tier: got %q, want pro (seed Member=1)", ui.Tier)
	}
	if ui.UserID == "" {
		t.Error("/userinfo user_id empty")
	}

	// Step 5: POST /token grant=refresh_token; verify the new access
	// token still works on /userinfo.
	time.Sleep(10 * time.Millisecond) // tiny gap so jti differs
	w5 := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {"workmax-desktop"},
	})
	if w5.Code != http.StatusOK {
		t.Fatalf("step 5 /token refresh: status %d, body %s", w5.Code, w5.Body.String())
	}
	tok2 := decodeTokenJSON(t, w5)
	if tok2.RefreshToken == tok.RefreshToken {
		t.Error("refresh token didn't rotate")
	}

	// New access token must also work on /userinfo.
	w6 := doGetAuthorized(r, "/api/desktop/oauth/userinfo", tok2.AccessToken)
	if w6.Code != http.StatusOK {
		t.Fatalf("step 6 /userinfo post-rotate: status %d, body %s", w6.Code, w6.Body.String())
	}
	var ui2 userinfoResponse
	_ = json.Unmarshal(w6.Body.Bytes(), &ui2)
	if ui2.UserID != ui.UserID {
		t.Errorf("/userinfo user_id drift across rotation: %q vs %q", ui2.UserID, ui.UserID)
	}
}

// doGetAuthorized is a convenience wrapper that sets the Authorization
// header from a bearer token.
func doGetAuthorized(r http.Handler, path, bearer string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	r.ServeHTTP(w, req)
	return w
}

// mustExtractPendingID pulls the hidden form field value out of the
// consent HTML. Cheaper than parsing the document.
func mustExtractPendingID(t *testing.T, body string) string {
	t.Helper()
	const tag = `name="code_grant_request_id" value="`
	idx := strings.Index(body, tag)
	if idx == -1 {
		t.Fatalf("could not find pending id in consent HTML: %s", body)
	}
	rest := body[idx+len(tag):]
	end := strings.Index(rest, `"`)
	if end == -1 {
		t.Fatalf("malformed hidden field in consent HTML: %s", body)
	}
	return rest[:end]
}
