package login

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	ormlogger "gorm.io/gorm/logger"

	oauthmodel "server/model/desktop/oauth"
	logintransaction "server/service/desktop/logintransaction"
	oauthservice "server/service/desktop/oauth"
	"server/service/secrets"
)

var (
	testTransactionID = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))
	testCapability    = base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdefghijklmnopqrstuv"))
)

type testPasswordAuthenticator struct{}

func (testPasswordAuthenticator) AuthenticatePassword(
	_ context.Context, email, password string,
) (logintransaction.Principal, error) {
	if email != "person@example.com" || password != "correct-password" {
		return logintransaction.Principal{}, logintransaction.ErrAuthenticationFailed
	}
	return logintransaction.Principal{UserID: 42}, nil
}

type testCodeIssuer struct {
	input logintransaction.ExchangeInput
	err   error
}

func (i *testCodeIssuer) ExchangeAndIssue(
	_ context.Context,
	in logintransaction.ExchangeInput,
) (logintransaction.IssuedAuthorization, error) {
	i.input = in
	if i.err != nil {
		return logintransaction.IssuedAuthorization{}, i.err
	}
	return logintransaction.IssuedAuthorization{
		Code:        "authorization-code",
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
		RedirectURI: "http://127.0.0.1:49152/oauth/callback",
		OAuthState:  "0123456789abcdef",
	}, nil
}

func newLoginAPITestHarness(t *testing.T) (*gin.Engine, *LoginApi, *testCodeIssuer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: ormlogger.Default.LogMode(ormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&oauthmodel.Client{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&oauthmodel.Client{
		ClientID:      oauthmodel.DesktopClientID,
		ClientName:    "WorkMax Desktop",
		ClientType:    oauthmodel.ClientTypePublic,
		RedirectURIs:  `["http://127.0.0.1:*/oauth/callback"]`,
		AllowedScopes: `["workagent"]`,
		IsActive:      true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	transactions, err := logintransaction.NewService(
		logintransaction.NewMemoryRepository(),
		testPasswordAuthenticator{},
		nil,
		logintransaction.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &testCodeIssuer{}
	api := &LoginApi{
		ClientRegistry: oauthservice.NewClientRegistry(db),
		Transactions:   transactions,
		CodeIssuer:     issuer,
	}
	router := gin.New()
	group := router.Group("/api/v1/desktop/identity")
	group.POST("/login-transactions", api.Create)
	group.GET("/login-transactions/:id", api.Status)
	group.POST("/login-transactions/:id/password", api.CompletePassword)
	group.POST("/login-transactions/:id/exchange", api.Exchange)
	return router, api, issuer
}

func TestNewLoginApiRequiresDatabaseAndSecretsReadiness(t *testing.T) {
	if _, err := NewLoginApi(nil); err == nil || !strings.Contains(err.Error(), "database is required") {
		t.Fatalf("NewLoginApi(nil) error = %v", err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared"), &gorm.Config{
		Logger: ormlogger.Default.LogMode(ormlogger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets.ClearKeyForTesting()
	t.Setenv("WORKMAX_SECRETS_KEY", "")
	if _, err := NewLoginApi(db); err == nil || !strings.Contains(err.Error(), "WORKMAX_SECRETS_KEY") {
		t.Fatalf("NewLoginApi without key error = %v", err)
	}
	key := make([]byte, 32)
	for index := range key {
		key[index] = 0x5a
	}
	secrets.SetKeyForTesting(key)
	t.Cleanup(secrets.ClearKeyForTesting)
	api, err := NewLoginApi(db)
	if err != nil || api == nil {
		t.Fatalf("NewLoginApi with key = %+v, err = %v", api, err)
	}
}

func validCreateBody() []byte {
	body, _ := json.Marshal(createRequest{
		ClientID:            oauthmodel.DesktopClientID,
		DeviceID:            "2825400e4ecb442f7b842f022cd40d4e",
		RedirectURI:         "http://127.0.0.1:49152/oauth/callback",
		State:               "MDEyMzQ1Njc4OWFiY2RlZg",
		CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallengeMethod: "S256",
		Scope:               "workagent",
	})
	return body
}

func performJSON(router http.Handler, method, path string, body []byte, authorization string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func createTransaction(t *testing.T, router http.Handler) createResponse {
	t.Helper()
	response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", validCreateBody(), "")
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	var out createResponse
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TransactionID == "" || out.TransactionSecret == "" || len(out.Methods) != 1 || out.Methods[0] != "password" {
		t.Fatalf("create response = %+v", out)
	}
	return out
}

func TestCreateAndAuthenticatedStatusExposeOnlyBoundedTransactionData(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	created := createTransaction(t, router)

	status := performJSON(
		router,
		http.MethodGet,
		"/api/v1/desktop/identity/login-transactions/"+created.TransactionID,
		nil,
		transactionScheme+" "+created.TransactionSecret,
	)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status.Code, status.Body.String())
	}
	body := status.Body.String()
	for _, secret := range []string{created.TransactionSecret, "MDEyMzQ1Njc4OWFiY2RlZg", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", "49152"} {
		if strings.Contains(body, secret) {
			t.Fatalf("status response leaked frozen or secret data %q: %s", secret, body)
		}
	}
	if status.Header().Get("Cache-Control") != "no-store" || status.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("missing no-store headers: %+v", status.Header())
	}
}

func TestStatusCollapsesUnknownAndWrongSecret(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	created := createTransaction(t, router)
	paths := []string{
		"/api/v1/desktop/identity/login-transactions/" + created.TransactionID,
		"/api/v1/desktop/identity/login-transactions/unknown-transaction",
	}
	var bodies []string
	for _, path := range paths {
		response := performJSON(router, http.MethodGet, path, nil, transactionScheme+" wrong-secret")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", response.Code)
		}
		bodies = append(bodies, response.Body.String())
	}
	if bodies[0] != bodies[1] || !strings.Contains(bodies[0], "invalid_transaction") {
		t.Fatalf("credential failures diverged: %q vs %q", bodies[0], bodies[1])
	}
}

func TestPasswordCompletionIsGenericAndRetryable(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	created := createTransaction(t, router)
	path := "/api/v1/desktop/identity/login-transactions/" + created.TransactionID + "/password"
	authorization := transactionScheme + " " + created.TransactionSecret

	wrong := performJSON(router, http.MethodPost, path, []byte(`{"email":"person@example.com","password":"wrong"}`), authorization)
	if wrong.Code != http.StatusUnauthorized || wrong.Body.String() != "{\"error\":\"invalid_credentials\"}" {
		t.Fatalf("wrong credential response = %d %s", wrong.Code, wrong.Body.String())
	}
	valid := performJSON(router, http.MethodPost, path, []byte(`{"email":"person@example.com","password":"correct-password"}`), authorization)
	if valid.Code != http.StatusOK {
		t.Fatalf("valid credential response = %d %s", valid.Code, valid.Body.String())
	}
	if strings.Contains(valid.Body.String(), "correct-password") || !strings.Contains(valid.Body.String(), "exchange_token") {
		t.Fatalf("password completion response = %s", valid.Body.String())
	}
}

func TestCreateRejectsUnknownFieldsAndUnsafeLoopback(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	unknown := append(bytes.TrimSuffix(validCreateBody(), []byte("}")), []byte(`,"return_url":"https://evil.example"}`)...)
	if response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", unknown, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", response.Code)
	}
	unsafe := strings.Replace(string(validCreateBody()), "127.0.0.1", "localhost", 1)
	if response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", []byte(unsafe), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("localhost redirect status = %d", response.Code)
	}
}

func TestExchangeRedirectsOnlyToFrozenLoopback(t *testing.T) {
	router, _, issuer := newLoginAPITestHarness(t)
	response := performJSON(
		router,
		http.MethodPost,
		"/api/v1/desktop/identity/login-transactions/"+testTransactionID+"/exchange",
		nil,
		exchangeScheme+" "+testCapability,
	)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("exchange status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Location"); got != "http://127.0.0.1:49152/oauth/callback?code=authorization-code&state=0123456789abcdef" {
		t.Fatalf("Location = %q", got)
	}
	if issuer.input.TransactionID != testTransactionID || issuer.input.ExchangeToken != testCapability {
		t.Fatalf("issuer input = %+v", issuer.input)
	}

	issuer.err = logintransaction.ErrReplay
	replayed := performJSON(
		router,
		http.MethodPost,
		"/api/v1/desktop/identity/login-transactions/"+testTransactionID+"/exchange",
		nil,
		exchangeScheme+" "+testCapability,
	)
	if replayed.Code != http.StatusConflict || !strings.Contains(replayed.Body.String(), "transaction_complete") {
		t.Fatalf("replay response = %d %s", replayed.Code, replayed.Body.String())
	}
}

func TestStatusAndExchangeRejectRequestBodies(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	created := createTransaction(t, router)

	status := performJSON(
		router,
		http.MethodGet,
		"/api/v1/desktop/identity/login-transactions/"+created.TransactionID,
		[]byte(`{}`),
		transactionScheme+" "+created.TransactionSecret,
	)
	if status.Code != http.StatusBadRequest || status.Body.String() != "{\"error\":\"invalid_request\"}" {
		t.Fatalf("status body response = %d %s", status.Code, status.Body.String())
	}

	exchange := performJSON(
		router,
		http.MethodPost,
		"/api/v1/desktop/identity/login-transactions/"+testTransactionID+"/exchange",
		[]byte(`{}`),
		exchangeScheme+" "+testCapability,
	)
	if exchange.Code != http.StatusBadRequest || exchange.Body.String() != "{\"error\":\"invalid_request\"}" {
		t.Fatalf("exchange body response = %d %s", exchange.Code, exchange.Body.String())
	}
}

func TestLoopbackRedirectRequiresExplicitValidPort(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/oauth/callback",
		"http://127.0.0.1:0/oauth/callback",
		"http://127.0.0.1:65536/oauth/callback",
		"http://127.0.0.1:49152/oauth%2Fcallback",
	} {
		if _, err := loopbackRedirect(raw, "code", "state"); err == nil {
			t.Fatalf("loopbackRedirect accepted %q", raw)
		}
	}
}

func TestPasswordCompletionRejectsDuplicateAuthorizationAndExtraJSON(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	created := createTransaction(t, router)
	path := "/api/v1/desktop/identity/login-transactions/" + created.TransactionID + "/password"

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"email":"person@example.com","password":"correct-password"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Add("Authorization", transactionScheme+" "+created.TransactionSecret)
	request.Header.Add("Authorization", transactionScheme+" duplicate")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status = %d", recorder.Code)
	}

	extra := performJSON(router, http.MethodPost, path, []byte(`{"email":"person@example.com","password":"correct-password"} {}`), transactionScheme+" "+created.TransactionSecret)
	if extra.Code != http.StatusBadRequest {
		t.Fatalf("extra JSON status = %d", extra.Code)
	}
}

func TestCreateRejectsDuplicateJSONKeysAndAcceptsUTF8Charset(t *testing.T) {
	router, _, _ := newLoginAPITestHarness(t)
	duplicate := strings.Replace(
		string(validCreateBody()),
		`"client_id":"workmax-desktop"`,
		`"client_id":"workmax-desktop","client_id":"workmax-desktop"`,
		1,
	)
	if response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", []byte(duplicate), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON key status = %d, body = %s", response.Code, response.Body.String())
	}
	caseVariant := strings.Replace(
		string(validCreateBody()),
		`"client_id":"workmax-desktop"`,
		`"Client_ID":"workmax-desktop"`,
		1,
	)
	if response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", []byte(caseVariant), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("case-variant JSON key status = %d, body = %s", response.Code, response.Body.String())
	}
	invalidUTF8 := append([]byte(nil), validCreateBody()...)
	invalidUTF8 = bytes.Replace(invalidUTF8, []byte("workagent"), []byte{'w', 'o', 'r', 'k', 0xff}, 1)
	if response := performJSON(router, http.MethodPost, "/api/v1/desktop/identity/login-transactions", invalidUTF8, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid UTF-8 JSON status = %d, body = %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/desktop/identity/login-transactions", bytes.NewReader(validCreateBody()))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("UTF-8 JSON status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestTransactionErrorMappingDoesNotLeakInternalErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	setNoStoreHeaders(context)
	writeTransactionError(context, errors.New("database host and secret details"))
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "database") {
		t.Fatalf("internal error leaked: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestClientRegistryErrorMappingSeparatesCallerAndInfrastructureFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "unknown client", err: oauthservice.ErrClientNotFound, wantStatus: http.StatusBadRequest},
		{name: "redirect mismatch", err: oauthservice.ErrRedirectURIMismatch, wantStatus: http.StatusBadRequest},
		{name: "scope not allowed", err: oauthservice.ErrScopeNotAllowed, wantStatus: http.StatusBadRequest},
		{name: "stored client corruption", err: oauthservice.ErrClientPatternsMalformed, wantStatus: http.StatusServiceUnavailable},
		{name: "database", err: errors.New("database unavailable"), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			writeClientRegistryError(context, test.err)
			if recorder.Code != test.wantStatus || strings.Contains(recorder.Body.String(), test.err.Error()) {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
