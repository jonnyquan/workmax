package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwt "github.com/golang-jwt/jwt/v4"

	"server/config"
	"server/globals"
	request "server/model/system/request"

	model "server/model/desktop/oauth"
	svc "server/service/desktop/oauth"
)

// PKCE pair used across all /token tests. Verifier is RFC 7636 §B.1
// example; challenge is base64url(SHA256(verifier)).
const (
	testCodeVerifier  = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	testCodeChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	testDeviceID      = "2825400e4ecb442f7b842f022cd40d4e"
	testDeviceIDE2E   = "11111111111111111111111111111111"
)

func init() {
	// Sanity: the test verifier really does base64url-SHA256 to the
	// stored challenge. Catches silent breakage of the RFC vector.
	h := sha256.Sum256([]byte(testCodeVerifier))
	if base64.RawURLEncoding.EncodeToString(h[:]) != testCodeChallenge {
		panic("test verifier/challenge pair is broken")
	}
}

// newTestApiWithRoutes spins up the OauthApi like newTestApi (P-1.4
// helper) but ALSO registers POST /token. The /token handler needs
// globals.GraConf.JWT.SigningKey populated to actually sign tokens.
func newTestApiWithToken(t *testing.T) (*gin.Engine, *OauthApi) {
	t.Helper()

	// Token signing needs a key. The legacy /api/auth tests probably
	// bootstrap this elsewhere; in our package tests we set it
	// directly. Restored to empty after the test.
	prev := globals.GraConf
	t.Cleanup(func() { globals.GraConf = prev })
	globals.GraConf = config.Server{}
	globals.GraConf.JWT.SigningKey = "test-jwt-secret-key"
	globals.GraConf.JWT.Issuer = "workmax-test"

	r, api, _ := newTestApi(t)
	r.POST("/api/desktop/oauth/token", api.Token)
	return r, api
}

func postTokenForm(r *gin.Engine, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

func decodeTokenJSON(t *testing.T, w *httptest.ResponseRecorder) tokenResponse {
	t.Helper()
	var out tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v (raw: %s)", err, w.Body.String())
	}
	return out
}

func decodeTokenError(t *testing.T, w *httptest.ResponseRecorder) tokenErrorResponse {
	t.Helper()
	var out tokenErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode error body: %v (raw: %s)", err, w.Body.String())
	}
	return out
}

// seedAuthCode inserts an oauth_authorization_code row directly via
// the CodeService so /token tests can run without first traversing
// /authorize.
func seedAuthCode(t *testing.T, api *OauthApi, uid int, scope string) string {
	t.Helper()
	g, err := api.CodeService.Generate(context.Background(), svc.GenerateInput{
		ClientID:            model.DesktopClientID,
		UID:                 uid,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       testCodeChallenge,
		CodeChallengeMethod: "S256",
		Scope:               scope,
	})
	if err != nil {
		t.Fatalf("seed auth code: %v", err)
	}
	return g.Code
}

func validTokenFormCodeGrant(code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:54321/oauth/callback"},
		"client_id":     {model.DesktopClientID},
		"code_verifier": {testCodeVerifier},
		"device_id":     {testDeviceID},
	}
}

// === Grant-type routing ===

func TestToken_MissingGrantType(t *testing.T) {
	r, _ := newTestApiWithToken(t)
	w := postTokenForm(r, url.Values{})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "invalid_request" {
		t.Errorf("error code: got %q, want invalid_request", e.Error)
	}
}

func TestToken_UnknownGrantType(t *testing.T) {
	r, _ := newTestApiWithToken(t)
	w := postTokenForm(r, url.Values{"grant_type": {"client_credentials"}})
	e := decodeTokenError(t, w)
	if e.Error != "unsupported_grant_type" {
		t.Errorf("error code: got %q", e.Error)
	}
}

// === authorization_code grant ===

func TestToken_CodeGrant_Happy(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	form := validTokenFormCodeGrant(code)
	form.Set("device_info", `{"os":"darwin","app_version":"0.0.5"}`)
	w := postTokenForm(r, form)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body: %s", w.Code, w.Body.String())
	}
	tok := decodeTokenJSON(t, w)
	if tok.TokenType != "Bearer" {
		t.Errorf("token_type: got %q, want Bearer", tok.TokenType)
	}
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("expected both tokens populated")
	}
	// ExpiresIn should be ~15min (with a tiny window for test runtime).
	if tok.ExpiresIn < 14*60 || tok.ExpiresIn > 15*60 {
		t.Errorf("expires_in: got %d, want ~900 (15min)", tok.ExpiresIn)
	}
	if tok.Scope != "workagent" {
		t.Errorf("scope: got %q", tok.Scope)
	}

	// Cache-Control: no-store header set.
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}

	// Decode access_token, verify OAuthClientID + uid claims.
	parsed, err := jwt.ParseWithClaims(tok.AccessToken, &request.CustomClaims{}, func(_ *jwt.Token) (interface{}, error) {
		return []byte(globals.GraConf.JWT.SigningKey), nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("decode access_token: %v", err)
	}
	claims := parsed.Claims.(*request.CustomClaims)
	if claims.BaseClaims.Id != 42 {
		t.Errorf("claims.Id: got %d, want 42", claims.BaseClaims.Id)
	}
	if claims.OAuthClientID != model.DesktopClientID {
		t.Errorf("claims.OAuthClientID: got %q, want %q", claims.OAuthClientID, model.DesktopClientID)
	}
	if claims.Audience != model.DesktopResourceAudience {
		t.Errorf("claims.Audience: got %q, want %q", claims.Audience, model.DesktopResourceAudience)
	}
	if claims.Subject != "u_42" {
		t.Errorf("claims.Subject: got %q, want u_42", claims.Subject)
	}
	if claims.OAuthScope != model.DesktopOAuthScopeWorkAgent {
		t.Errorf("claims.OAuthScope: got %q", claims.OAuthScope)
	}
	if claims.CredentialType != model.DesktopCredentialDeviceSession {
		t.Errorf("claims.CredentialType: got %q", claims.CredentialType)
	}
	if claims.DeviceID != testDeviceID {
		t.Errorf("claims.DeviceID: got %q", claims.DeviceID)
	}
	if claims.DeviceSessionID == "" {
		t.Error("claims.DeviceSessionID must bind the refresh chain")
	}

	// Verify refresh_token row landed with device_id + device_info.
	var refreshRow model.RefreshToken
	if err := api.DB.Where("token = ?", tok.RefreshToken).First(&refreshRow).Error; err != nil {
		t.Fatalf("lookup refresh row: %v", err)
	}
	if refreshRow.DeviceID != testDeviceID {
		t.Errorf("refresh row DeviceID: got %q", refreshRow.DeviceID)
	}
	if refreshRow.DeviceInfo == nil || !strings.Contains(*refreshRow.DeviceInfo, "darwin") {
		t.Errorf("refresh row DeviceInfo: got %v", refreshRow.DeviceInfo)
	}
	if refreshRow.ParentID != nil {
		t.Errorf("expected ParentID nil for chain root, got %v", refreshRow.ParentID)
	}
	if claims.DeviceSessionID != refreshRow.ChainID {
		t.Errorf("access token session %q does not match refresh chain %q", claims.DeviceSessionID, refreshRow.ChainID)
	}

	// Verify code row was marked used.
	var codeRow model.AuthorizationCode
	if err := api.DB.Where("code = ?", code).First(&codeRow).Error; err != nil {
		t.Fatalf("lookup code row: %v", err)
	}
	if !codeRow.Used {
		t.Error("code row should be used=true after token exchange")
	}
}

func TestToken_CodeGrant_RejectsInvalidDeviceIDWithoutConsumingCode(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	for _, deviceID := range []string{
		"device-uuid-test",
		"2825400e4ecb442f7b842f022cd40d4",  // too long
		"2825400e4ecb442f7b842f022cd40d",   // too short
		"2825400e4ecb442f7b842f022cd40d4z", // not hex
	} {
		t.Run(deviceID, func(t *testing.T) {
			form := validTokenFormCodeGrant(code)
			form.Set("device_id", deviceID)
			w := postTokenForm(r, form)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
			e := decodeTokenError(t, w)
			if e.Error != "invalid_request" {
				t.Errorf("error code: got %q, want invalid_request", e.Error)
			}
		})
	}

	var codeRow model.AuthorizationCode
	if err := api.DB.Where("code = ?", code).First(&codeRow).Error; err != nil {
		t.Fatalf("lookup code row: %v", err)
	}
	if codeRow.Used {
		t.Error("invalid device_id should be rejected before consuming authorization code")
	}
}

func TestToken_CodeGrant_RevalidatesStoredScopeAgainstClient(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent billing.write")
	w := postTokenForm(r, validTokenFormCodeGrant(code))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"error":"invalid_grant"`) {
		t.Fatalf("unexpected error body: %s", w.Body.String())
	}
	var count int64
	if err := api.DB.Model(&model.RefreshToken{}).Count(&count).Error; err != nil {
		t.Fatalf("count refresh tokens: %v", err)
	}
	if count != 0 {
		t.Fatalf("unapproved scope created %d refresh token rows", count)
	}
}

func TestToken_CodeGrant_RejectsInvalidDeviceInfoWithoutConsumingCode(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	for _, tc := range []struct {
		name       string
		deviceInfo string
	}{
		{name: "malformed json", deviceInfo: `{"os":`},
		{name: "array instead of object", deviceInfo: `["darwin"]`},
		{name: "oversized object", deviceInfo: `{"blob":"` + strings.Repeat("x", maxDeviceInfoBytes) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := validTokenFormCodeGrant(code)
			form.Set("device_info", tc.deviceInfo)
			w := postTokenForm(r, form)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
			e := decodeTokenError(t, w)
			if e.Error != "invalid_request" {
				t.Errorf("error code: got %q, want invalid_request", e.Error)
			}
		})
	}

	var codeRow model.AuthorizationCode
	if err := api.DB.Where("code = ?", code).First(&codeRow).Error; err != nil {
		t.Fatalf("lookup code row: %v", err)
	}
	if codeRow.Used {
		t.Error("invalid device_info should be rejected before consuming authorization code")
	}
}

func TestToken_CodeGrant_RejectsMissingParams(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	for _, omit := range []string{"code", "redirect_uri", "client_id", "code_verifier", "device_id"} {
		t.Run("omit_"+omit, func(t *testing.T) {
			form := validTokenFormCodeGrant(code)
			form.Del(omit)
			w := postTokenForm(r, form)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
			e := decodeTokenError(t, w)
			if e.Error != "invalid_request" {
				t.Errorf("error code: got %q, want invalid_request", e.Error)
			}
		})
	}
}

func TestToken_CodeGrant_RejectsReusedCode(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	if w := postTokenForm(r, validTokenFormCodeGrant(code)); w.Code != http.StatusOK {
		t.Fatalf("first exchange should succeed, got %d: %s", w.Code, w.Body.String())
	}
	w := postTokenForm(r, validTokenFormCodeGrant(code))
	if w.Code != http.StatusBadRequest {
		t.Errorf("second exchange status: got %d, want 400", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error code: got %q, want invalid_grant", e.Error)
	}
}

func TestToken_CodeGrant_RejectsBadVerifier(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	form := validTokenFormCodeGrant(code)
	form.Set("code_verifier", "wrong-verifier-still-43-chars-padded-xxxxxxx")
	w := postTokenForm(r, form)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error code: got %q", e.Error)
	}
	assertAuthorizationCodeUnused(t, api, code)
}

func TestToken_CodeGrant_RejectsClientIDMismatch(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	form := validTokenFormCodeGrant(code)
	form.Set("client_id", "different-client")
	w := postTokenForm(r, form)
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error: got %q", e.Error)
	}
	assertAuthorizationCodeUnused(t, api, code)
}

func TestToken_CodeGrant_RejectsRedirectURIMismatch(t *testing.T) {
	r, api := newTestApiWithToken(t)
	code := seedAuthCode(t, api, 42, "workagent")

	form := validTokenFormCodeGrant(code)
	form.Set("redirect_uri", "http://127.0.0.1:99999/oauth/callback")
	w := postTokenForm(r, form)
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error: got %q", e.Error)
	}
	assertAuthorizationCodeUnused(t, api, code)
}

func TestToken_CodeGrant_NewTransactionCodeRequiresFrozenDevice(t *testing.T) {
	r, api := newTestApiWithToken(t)
	expectedDeviceID := testDeviceID
	generated, err := api.CodeService.Generate(context.Background(), svc.GenerateInput{
		ClientID:            model.DesktopClientID,
		UID:                 42,
		DeviceID:            &expectedDeviceID,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       testCodeChallenge,
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
	})
	if err != nil {
		t.Fatal(err)
	}

	wrong := validTokenFormCodeGrant(generated.Code)
	wrong.Set("device_id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	w := postTokenForm(r, wrong)
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Fatalf("wrong-device error = %q, want invalid_grant", e.Error)
	}
	assertAuthorizationCodeUnused(t, api, generated.Code)

	if w := postTokenForm(r, validTokenFormCodeGrant(generated.Code)); w.Code != http.StatusOK {
		t.Fatalf("frozen device exchange failed: %d %s", w.Code, w.Body.String())
	}
}

func assertAuthorizationCodeUnused(t *testing.T, api *OauthApi, code string) {
	t.Helper()
	var row model.AuthorizationCode
	if err := api.DB.Where("code = ?", code).First(&row).Error; err != nil {
		t.Fatalf("load authorization code: %v", err)
	}
	if row.Used {
		t.Fatal("invalid binding must not consume authorization code")
	}
}

// === refresh_token grant ===

// helper: full code→token exchange returning the refresh token.
func establishRefreshToken(t *testing.T, r *gin.Engine, api *OauthApi) string {
	t.Helper()
	code := seedAuthCode(t, api, 42, "workagent")
	w := postTokenForm(r, validTokenFormCodeGrant(code))
	if w.Code != http.StatusOK {
		t.Fatalf("establish: %d %s", w.Code, w.Body.String())
	}
	return decodeTokenJSON(t, w).RefreshToken
}

func TestToken_RefreshGrant_Happy(t *testing.T) {
	r, api := newTestApiWithToken(t)
	rt := establishRefreshToken(t, r, api)

	w := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {model.DesktopClientID},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, body: %s", w.Code, w.Body.String())
	}
	tok := decodeTokenJSON(t, w)
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("missing tokens")
	}
	if tok.RefreshToken == rt {
		t.Error("rotated refresh_token must differ from input")
	}
	if tok.ExpiresIn < 14*60 {
		t.Errorf("expires_in: got %d", tok.ExpiresIn)
	}

	// Old refresh row should be revoked='rotated'.
	var oldRow model.RefreshToken
	if err := api.DB.Where("token = ?", rt).First(&oldRow).Error; err != nil {
		t.Fatalf("lookup old row: %v", err)
	}
	if !oldRow.Revoked || oldRow.RevokedReason == nil || *oldRow.RevokedReason != model.RevokedReasonRotated {
		t.Errorf("old row: revoked=%v reason=%v", oldRow.Revoked, oldRow.RevokedReason)
	}
}

func TestToken_RefreshGrant_Replay_RevokesChain(t *testing.T) {
	r, api := newTestApiWithToken(t)
	rt := establishRefreshToken(t, r, api)

	// First refresh: succeeds.
	w1 := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {model.DesktopClientID},
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("first refresh: %d %s", w1.Code, w1.Body.String())
	}
	newRT := decodeTokenJSON(t, w1).RefreshToken

	// Replay the original (already rotated): must fail AND kill chain.
	w2 := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {model.DesktopClientID},
	})
	if w2.Code != http.StatusBadRequest {
		t.Errorf("replay status: got %d, want 400", w2.Code)
	}

	// The new refresh (which was active) should now be killed too.
	var activeRow model.RefreshToken
	if err := api.DB.Where("token = ?", newRT).First(&activeRow).Error; err != nil {
		t.Fatalf("lookup active row: %v", err)
	}
	if !activeRow.Revoked || activeRow.RevokedReason == nil || *activeRow.RevokedReason != model.RevokedReasonReplayDetected {
		t.Errorf("active row should be revoked='replay_detected', got revoked=%v reason=%v", activeRow.Revoked, activeRow.RevokedReason)
	}

	// Subsequent rotate of the now-killed active token also fails.
	w3 := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {newRT},
		"client_id":     {model.DesktopClientID},
	})
	if w3.Code != http.StatusBadRequest {
		t.Errorf("post-killchain refresh status: got %d, want 400", w3.Code)
	}
}

func TestToken_RefreshGrant_RejectsMissingParams(t *testing.T) {
	r, _ := newTestApiWithToken(t)
	cases := []url.Values{
		{"grant_type": {"refresh_token"}},
		{"grant_type": {"refresh_token"}, "client_id": {model.DesktopClientID}},
		{"grant_type": {"refresh_token"}, "refresh_token": {"some"}},
	}
	for _, form := range cases {
		w := postTokenForm(r, form)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status: got %d, form=%v", w.Code, form)
		}
		if e := decodeTokenError(t, w); e.Error != "invalid_request" {
			t.Errorf("error: got %q, form=%v", e.Error, form)
		}
	}
}

func TestToken_RefreshGrant_RejectsUnknownToken(t *testing.T) {
	r, _ := newTestApiWithToken(t)
	w := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"no-such-token"},
		"client_id":     {model.DesktopClientID},
	})
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error: got %q", e.Error)
	}
}

func TestToken_RefreshGrant_RejectsClientMismatch(t *testing.T) {
	r, api := newTestApiWithToken(t)
	rt := establishRefreshToken(t, r, api)

	w := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {rt},
		"client_id":     {"different-client"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d", w.Code)
	}
	if e := decodeTokenError(t, w); e.Error != "invalid_grant" {
		t.Errorf("error: got %q", e.Error)
	}
	// And critically: the rotation did NOT escalate to replay.
	var row model.RefreshToken
	_ = api.DB.Where("token = ?", rt).First(&row).Error
	if row.Revoked {
		t.Error("client mismatch should NOT revoke the token")
	}
}

// === E2E: authorize→consent→token (full flow) ===

func TestE2E_FullOAuthFlow(t *testing.T) {
	r, api := newTestApiWithToken(t)

	// Step 1: GET /authorize
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {model.DesktopClientID},
		"redirect_uri":          {"http://127.0.0.1:54321/oauth/callback"},
		"code_challenge":        {testCodeChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"opaque-state-e2e"},
		"scope":                 {"workagent"},
	}
	w1 := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w1.Code != http.StatusOK {
		t.Fatalf("authorize: %d", w1.Code)
	}
	// Extract pending id from consent form.
	body := w1.Body.String()
	const tag = `name="code_grant_request_id" value="`
	idx := strings.Index(body, tag)
	rest := body[idx+len(tag):]
	end := strings.Index(rest, `"`)
	pendingID := rest[:end]

	// Step 2: POST /authorize/consent action=approve → grab code
	w2 := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {pendingID},
		"action":                {"approve"},
	})
	if w2.Code != http.StatusFound {
		t.Fatalf("consent: %d", w2.Code)
	}
	redirURL, _ := url.Parse(w2.Header().Get("Location"))
	code := redirURL.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}
	if redirURL.Query().Get("state") != "opaque-state-e2e" {
		t.Errorf("state echo broken: %q", redirURL.Query().Get("state"))
	}

	// Step 3: POST /token grant_type=authorization_code
	w3 := postTokenForm(r, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:54321/oauth/callback"},
		"client_id":     {model.DesktopClientID},
		"code_verifier": {testCodeVerifier},
		"device_id":     {testDeviceIDE2E},
	})
	if w3.Code != http.StatusOK {
		t.Fatalf("token exchange: %d %s", w3.Code, w3.Body.String())
	}
	tok := decodeTokenJSON(t, w3)
	if tok.AccessToken == "" || tok.RefreshToken == "" {
		t.Fatal("missing tokens")
	}

	// Step 4: POST /token grant_type=refresh_token rotates
	time.Sleep(10 * time.Millisecond) // ensure timestamps differ slightly
	w4 := postTokenForm(r, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"client_id":     {model.DesktopClientID},
	})
	if w4.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", w4.Code, w4.Body.String())
	}
	tok2 := decodeTokenJSON(t, w4)
	if tok2.RefreshToken == tok.RefreshToken {
		t.Error("refresh token didn't rotate")
	}
	if tok2.AccessToken == tok.AccessToken {
		t.Error("access token didn't rotate")
	}

	// Verify both access tokens decode as same user / same client.
	for label, at := range map[string]string{"initial": tok.AccessToken, "rotated": tok2.AccessToken} {
		parsed, err := jwt.ParseWithClaims(at, &request.CustomClaims{}, func(_ *jwt.Token) (interface{}, error) {
			return []byte(globals.GraConf.JWT.SigningKey), nil
		})
		if err != nil {
			t.Errorf("%s access token decode: %v", label, err)
			continue
		}
		claims := parsed.Claims.(*request.CustomClaims)
		if claims.BaseClaims.Id != 42 {
			t.Errorf("%s claims.Id: got %d", label, claims.BaseClaims.Id)
		}
		if claims.OAuthClientID != model.DesktopClientID {
			t.Errorf("%s claims.OAuthClientID: got %q", label, claims.OAuthClientID)
		}
		if claims.Audience != model.DesktopResourceAudience || claims.OAuthScope != model.DesktopOAuthScopeWorkAgent {
			t.Errorf("%s resource claims drifted: aud=%q scope=%q", label, claims.Audience, claims.OAuthScope)
		}
		if claims.DeviceID != testDeviceIDE2E || claims.DeviceSessionID == "" {
			t.Errorf("%s device binding drifted: device=%q session=%q", label, claims.DeviceID, claims.DeviceSessionID)
		}
	}

	// Chain integrity: 2 refresh rows, both in same chain.
	var rows []model.RefreshToken
	if err := api.DB.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: got %d, want 2", len(rows))
	}
	if rows[0].ChainID != rows[1].ChainID {
		t.Errorf("chain split: %q vs %q", rows[0].ChainID, rows[1].ChainID)
	}
	if rows[1].ParentID == nil || *rows[1].ParentID != rows[0].ID {
		t.Errorf("parent linkage broken: %v", rows[1].ParentID)
	}
}
