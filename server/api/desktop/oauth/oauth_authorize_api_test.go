package oauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	model "server/model/desktop/oauth"
	svc "server/service/desktop/oauth"
)

// newTestApi spins up an OauthApi backed by an in-memory SQLite,
// seeds the workmax-desktop client, and registers the routes on a
// fresh gin.Engine. Returns the engine, the api (so tests can swap
// AuthVerifier per case), and the DB (for direct row inspection).
func newTestApi(t *testing.T) (*gin.Engine, *OauthApi, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Client{}, &model.AuthorizationCode{}, &model.RefreshToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed workmax-desktop public client.
	if err := db.Create(&model.Client{
		ClientID:      model.DesktopClientID,
		ClientName:    "WorkMax Desktop",
		ClientType:    model.ClientTypePublic,
		RedirectURIs:  `["http://127.0.0.1:*/oauth/callback"]`,
		AllowedScopes: `["workagent"]`,
		IsActive:      true,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	api := NewOauthApi(db)
	t.Cleanup(api.PendingService.Stop)

	// Default verifier: every test that wants "logged in" overrides
	// this. Otherwise getAuthenticatedUID returns (0, false).
	api.AuthVerifier = func(token string) (uint, bool) {
		if token == "user-42" {
			return 42, true
		}
		return 0, false
	}

	r := gin.New()
	r.GET("/api/desktop/oauth/authorize", api.Authorize)
	r.POST("/api/desktop/oauth/authorize/consent", api.Consent)
	return r, api, db
}

// validAuthorizeQuery returns a query-string with every required
// param set to a reasonable default. Tests override per case.
func validAuthorizeQuery() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {model.DesktopClientID},
		"redirect_uri":          {"http://127.0.0.1:54321/oauth/callback"},
		"code_challenge":        {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
		"code_challenge_method": {"S256"},
		"state":                 {"opaque-state"},
		"scope":                 {"workagent"},
	}
}

func doGet(r *gin.Engine, path string, q url.Values, cookies map[string]string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path+"?"+q.Encode(), nil)
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	r.ServeHTTP(w, req)
	return w
}

func doPostForm(r *gin.Engine, path string, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

// === GET /authorize: structural validation ===

func TestAuthorize_RejectsMissingResponseType(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Del("response_type")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "response_type") {
		t.Errorf("body should mention response_type: %s", w.Body.String())
	}
}

func TestAuthorize_RejectsWrongResponseType(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Set("response_type", "token")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestAuthorize_RejectsMissingCodeChallenge(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Del("code_challenge")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestAuthorize_RejectsMalformedCodeChallenge(t *testing.T) {
	r, _, _ := newTestApi(t)
	for _, challenge := range []string{
		"too-short",
		strings.Repeat("a", svc.PKCEVerifierMaxLen+1),
		strings.Repeat("a", svc.PKCEVerifierMinLen-1) + "+",
	} {
		q := validAuthorizeQuery()
		q.Set("code_challenge", challenge)
		w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("challenge length %d status: got %d, want 400", len(challenge), w.Code)
		}
	}
}

func TestAuthorize_RequiresState(t *testing.T) {
	r, api, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Del("state")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "state") {
		t.Fatalf("body should identify missing state: %s", w.Body.String())
	}
	if api.PendingService.Size() != 0 {
		t.Fatal("missing state must not create a pending authorization")
	}
}

func TestAuthorize_RejectsPlainChallengeMethod(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Set("code_challenge_method", "plain")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestAuthorize_RejectsUnknownClient(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Set("client_id", "not-registered")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unknown client") {
		t.Errorf("body: %s", w.Body.String())
	}
}

func TestAuthorize_RejectsBadRedirectURI(t *testing.T) {
	r, _, _ := newTestApi(t)
	cases := []string{
		"http://0.0.0.0:54321/oauth/callback",
		"https://127.0.0.1:54321/oauth/callback",
		"http://127.0.0.1:54321/different/path",
	}
	for _, ru := range cases {
		t.Run(ru, func(t *testing.T) {
			q := validAuthorizeQuery()
			q.Set("redirect_uri", ru)
			w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
			if w.Code != http.StatusBadRequest {
				t.Errorf("status: got %d, want 400", w.Code)
			}
		})
	}
}

func TestAuthorize_RejectsScopeOutsideClientAllowlist(t *testing.T) {
	r, api, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Set("scope", "workagent billing.write")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Invalid scope") {
		t.Fatalf("body should identify invalid scope without echoing it: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "billing.write") {
		t.Fatal("response must not echo a rejected scope")
	}
	if api.PendingService.Size() != 0 {
		t.Fatal("invalid scope must not create a pending authorization")
	}
}

func TestAuthorize_DefaultScopeIsValidated(t *testing.T) {
	r, api, _ := newTestApi(t)
	q := validAuthorizeQuery()
	q.Del("scope")
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if api.PendingService.Size() != 1 {
		t.Fatalf("default workagent scope was not admitted")
	}
}

// === GET /authorize: auth gating ===

func TestAuthorize_NotLoggedIn_ReturnsLoginRequired(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	// No cookie.
	w := doGet(r, "/api/desktop/oauth/authorize", q, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d, want 401", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Desktop sign-in is not available") {
		t.Errorf("body should report the fail-closed Desktop login gap: %s", body)
	}
	if strings.Contains(body, "href=\"/\"") || strings.Contains(body, "workmax.app") {
		t.Errorf("login-required page must not depend on a retired browser client: %s", body)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy: got %q, want no-referrer", got)
	}
}

func TestAuthorize_LoggedIn_RendersConsentForm(t *testing.T) {
	r, api, _ := newTestApi(t)
	q := validAuthorizeQuery()
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "WorkMax Desktop") {
		t.Errorf("consent body should name the client: %s", body)
	}
	if !strings.Contains(body, "name=\"action\" value=\"approve\"") ||
		!strings.Contains(body, "name=\"action\" value=\"deny\"") {
		t.Errorf("consent should have approve+deny buttons: %s", body)
	}
	if strings.Contains(body, "P0 placeholder") {
		t.Errorf("consent page should not expose milestone copy: %s", body)
	}
	// Pending request should be stored.
	if api.PendingService.Size() != 1 {
		t.Errorf("PendingService.Size: got %d, want 1", api.PendingService.Size())
	}
}

func TestAuthorize_BearerHeader_AlsoWorks(t *testing.T) {
	r, _, _ := newTestApi(t)
	q := validAuthorizeQuery()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/desktop/oauth/authorize?"+q.Encode(), nil)
	req.Header.Set("Authorization", "Bearer user-42")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Bearer header should authenticate: got %d", w.Code)
	}
}

// === POST /authorize/consent ===

func TestConsent_Approve_RedirectsWithCode(t *testing.T) {
	r, api, db := newTestApi(t)

	// Set up a pending authorization by calling Store directly.
	id, err := api.PendingService.Store(context.Background(), svc.StoreInput{
		ClientID:            model.DesktopClientID,
		UID:                 42,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "challenge-abc",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
		State:               "state-xyz",
	})
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	w := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {id},
		"action":                {"approve"},
	})
	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location not parseable: %q (%v)", loc, err)
	}
	if u.Host != "127.0.0.1:54321" || u.Path != "/oauth/callback" {
		t.Errorf("redirect host/path drift: %s", loc)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Error("Location must include ?code=")
	}
	if u.Query().Get("state") != "state-xyz" {
		t.Errorf("state echo: got %q, want state-xyz", u.Query().Get("state"))
	}

	// Auth code row should exist with the right metadata.
	var stored model.AuthorizationCode
	if err := db.Where("code = ?", code).First(&stored).Error; err != nil {
		t.Fatalf("lookup stored code: %v", err)
	}
	if stored.UID != 42 || stored.CodeChallenge != "challenge-abc" || stored.ClientID != model.DesktopClientID {
		t.Errorf("stored code mismatch: %+v", stored)
	}
}

func TestConsent_Deny_RedirectsWithError(t *testing.T) {
	r, api, _ := newTestApi(t)
	id, _ := api.PendingService.Store(context.Background(), svc.StoreInput{
		ClientID:            model.DesktopClientID,
		UID:                 42,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
		State:               "state-deny",
	})

	w := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {id},
		"action":                {"deny"},
	})
	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d, want 302", w.Code)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("error") != "access_denied" {
		t.Errorf("expected ?error=access_denied, got %s", u.Query().Encode())
	}
	if u.Query().Get("code") != "" {
		t.Error("denied flow must not return ?code=")
	}
	if u.Query().Get("state") != "state-deny" {
		t.Errorf("state echo: got %q", u.Query().Get("state"))
	}
}

func TestConsent_ExpiredOrUnknownID(t *testing.T) {
	r, _, _ := newTestApi(t)
	w := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {"no-such-id"},
		"action":                {"approve"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "expired") {
		t.Errorf("body should mention expired: %s", w.Body.String())
	}
}

func TestConsent_InvalidAction(t *testing.T) {
	r, api, _ := newTestApi(t)
	id, _ := api.PendingService.Store(context.Background(), svc.StoreInput{
		ClientID:            model.DesktopClientID,
		UID:                 42,
		RedirectURI:         "http://127.0.0.1:54321/oauth/callback",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
	})

	w := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {id},
		"action":                {"banana"},
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	if got := api.PendingService.Size(); got != 1 {
		t.Fatalf("invalid action consumed pending authorization: size=%d", got)
	}

	w = doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {id},
		"action":                {"approve"},
	})
	if w.Code != http.StatusFound {
		t.Fatalf("valid retry status: got %d, want 302", w.Code)
	}
}

// === E2E: GET /authorize → user clicks Approve → 302 with code ===

func TestE2E_AuthorizeThenApprove(t *testing.T) {
	r, _, db := newTestApi(t)

	// 1. GET /authorize as logged-in user
	q := validAuthorizeQuery()
	w := doGet(r, "/api/desktop/oauth/authorize", q, map[string]string{"access_token": "user-42"})
	if w.Code != http.StatusOK {
		t.Fatalf("authorize status: got %d", w.Code)
	}
	body := w.Body.String()
	// Pluck the hidden form field id out of the consent HTML.
	const tag = `name="code_grant_request_id" value="`
	idx := strings.Index(body, tag)
	if idx == -1 {
		t.Fatalf("could not find request id in body")
	}
	rest := body[idx+len(tag):]
	end := strings.Index(rest, `"`)
	requestID := rest[:end]
	if requestID == "" {
		t.Fatal("extracted empty request id")
	}

	// 2. POST consent with approve
	w2 := doPostForm(r, "/api/desktop/oauth/authorize/consent", url.Values{
		"code_grant_request_id": {requestID},
		"action":                {"approve"},
	})
	if w2.Code != http.StatusFound {
		t.Fatalf("consent status: got %d", w2.Code)
	}
	u, _ := url.Parse(w2.Header().Get("Location"))
	code := u.Query().Get("code")
	if code == "" {
		t.Fatal("redirect missing ?code=")
	}

	// 3. Verify the auth_code row carries the original PKCE challenge.
	var stored model.AuthorizationCode
	if err := db.Where("code = ?", code).First(&stored).Error; err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if stored.CodeChallenge != q.Get("code_challenge") {
		t.Errorf("code_challenge drift: got %q, want %q", stored.CodeChallenge, q.Get("code_challenge"))
	}
	if stored.CodeChallengeMethod != "S256" {
		t.Errorf("code_challenge_method: got %q", stored.CodeChallengeMethod)
	}
	if stored.RedirectURI != q.Get("redirect_uri") {
		t.Errorf("redirect_uri drift: got %q", stored.RedirectURI)
	}
	if stored.UID != 42 {
		t.Errorf("UID: got %d, want 42", stored.UID)
	}
}
