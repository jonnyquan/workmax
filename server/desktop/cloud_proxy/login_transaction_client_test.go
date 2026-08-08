//go:build desktop

package cloud_proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testLoginDeviceID = "2825400e4ecb442f7b842f022cd40d4e"
	testLoginExpires  = "2026-08-05T12:30:00Z"
	testLoginUpdated  = "2026-08-05T12:20:00Z"
)

func testLoginToken(fill byte, size int) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, size))
}

func TestLoginTransactionClientRejectsRemotePlainHTTPBeforeNetwork(t *testing.T) {
	client := NewClient("http://example.com")
	requests := 0
	client.HTTPClient = &http.Client{Transport: loginTransactionRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("must not reach transport")
	})}

	_, err := client.CreateLoginTransaction(
		context.Background(),
		validLoginTransactionCreateInputForTest("http://127.0.0.1:49152/oauth/callback"),
	)
	if !errors.Is(err, ErrLoginTransactionInvalidInput) {
		t.Fatalf("insecure remote base error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("insecure remote base reached transport %d time(s)", requests)
	}
}

func validLoginTransactionCreateInputForTest(redirectURI string) LoginTransactionCreateInput {
	return LoginTransactionCreateInput{
		DeviceID:            testLoginDeviceID,
		RedirectURI:         redirectURI,
		OAuthState:          testLoginToken('s', loginTransactionOAuthStateBytes),
		CodeChallenge:       testLoginToken('p', loginTransactionPKCEBytes),
		CodeChallengeMethod: loginPKCEMethodS256,
		Scope:               "workagent",
	}
}

func validLoginTransactionPasswordInputForTest() LoginTransactionPasswordInput {
	return LoginTransactionPasswordInput{
		TransactionID:     testLoginToken('i', loginTransactionIDBytes),
		TransactionSecret: testLoginToken('t', loginTransactionCapabilityBytes),
		Email:             "person@example.com",
		Password:          "correct-password",
	}
}

func validLoginTransactionExchangeInputForTest(redirectURI string) LoginTransactionExchangeInput {
	return LoginTransactionExchangeInput{
		TransactionID:       testLoginToken('i', loginTransactionIDBytes),
		ExchangeToken:       testLoginToken('x', loginTransactionCapabilityBytes),
		ExpectedRedirectURI: redirectURI,
		ExpectedOAuthState:  testLoginToken('s', loginTransactionOAuthStateBytes),
	}
}

func TestLoginTransactionClient_HappyWireContractAndIndependentRedirectClient(t *testing.T) {
	var callbackRequests atomic.Int32
	callback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()
	redirectURI := callback.URL + "/oauth/callback"

	transactionID := testLoginToken('i', loginTransactionIDBytes)
	transactionSecret := testLoginToken('t', loginTransactionCapabilityBytes)
	exchangeToken := testLoginToken('x', loginTransactionCapabilityBytes)
	state := testLoginToken('s', loginTransactionOAuthStateBytes)
	challenge := testLoginToken('p', loginTransactionPKCEBytes)
	code := testLoginToken('c', loginTransactionAuthCodeBytes)

	var createRequests atomic.Int32
	var statusRequests atomic.Int32
	var passwordRequests atomic.Int32
	var exchangeRequests atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertLoginTransactionStandardRequestHeaders(t, r)
		if got := r.Header.Get("Cookie"); got != "" {
			t.Errorf("typed client inherited shared cookie jar: %q", got)
		}
		if got := r.Header.Get("Origin"); got != "" {
			t.Errorf("unexpected browser Origin header: %q", got)
		}

		switch r.URL.Path {
		case CloudRouteLoginTransactionCreate:
			createRequests.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("create method = %q", r.Method)
			}
			assertLoginTransactionAuthorization(t, r, "")
			assertLoginTransactionJSONRequest(t, r, map[string]any{
				"client_id":             "workmax-desktop",
				"device_id":             testLoginDeviceID,
				"redirect_uri":          redirectURI,
				"state":                 state,
				"code_challenge":        challenge,
				"code_challenge_method": loginPKCEMethodS256,
				"scope":                 "workagent",
			})
			writeLoginTransactionTestResponse(t, w, http.StatusCreated, "application/json; charset=UTF-8",
				`{"transaction_id":"`+transactionID+`","transaction_secret":"`+transactionSecret+`","expires_at":"`+testLoginExpires+`","methods":["password","google"],"future_field":{"ignored":true}}`)

		case expandLoginTransactionRoute(CloudRouteLoginTransactionStatus, transactionID):
			statusRequests.Add(1)
			if r.Method != http.MethodGet {
				t.Errorf("status method = %q", r.Method)
			}
			assertLoginTransactionAuthorization(t, r, loginTransactionScheme+" "+transactionSecret)
			assertLoginTransactionEmptyRequest(t, r)
			writeLoginTransactionTestResponse(t, w, http.StatusOK, "application/json",
				`{"transaction_id":"`+transactionID+`","version":2,"state":"authenticated","expires_at":"`+testLoginExpires+`","updated_at":"`+testLoginUpdated+`","future_field":"ignored"}`)

		case expandLoginTransactionRoute(CloudRouteLoginTransactionPassword, transactionID):
			passwordRequests.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("password method = %q", r.Method)
			}
			assertLoginTransactionAuthorization(t, r, loginTransactionScheme+" "+transactionSecret)
			assertLoginTransactionJSONRequest(t, r, map[string]any{
				"email":    "person@example.com",
				"password": "correct-password",
			})
			writeLoginTransactionTestResponse(t, w, http.StatusOK, "application/json",
				`{"transaction_id":"`+transactionID+`","exchange_token":"`+exchangeToken+`","expires_at":"`+testLoginExpires+`","future_field":7}`)

		case expandLoginTransactionRoute(CloudRouteLoginTransactionExchange, transactionID):
			exchangeRequests.Add(1)
			if r.Method != http.MethodPost {
				t.Errorf("exchange method = %q", r.Method)
			}
			assertLoginTransactionAuthorization(t, r, loginExchangeScheme+" "+exchangeToken)
			assertLoginTransactionEmptyRequest(t, r)
			w.Header().Set("Location", redirectURI+"?code="+code+"&state="+state)
			w.WriteHeader(http.StatusSeeOther)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(upstreamURL, []*http.Cookie{{Name: "shared-client-only", Value: "must-not-be-sent"}})
	redirectSentinel := errors.New("shared redirect callback")
	var sharedRedirectCalls atomic.Int32
	shared := &http.Client{
		Transport: upstream.Client().Transport,
		Timeout:   3 * time.Second,
		Jar:       jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			sharedRedirectCalls.Add(1)
			return redirectSentinel
		},
	}
	client := NewClient(upstream.URL)
	client.HTTPClient = shared

	created, err := client.CreateLoginTransaction(context.Background(), validLoginTransactionCreateInputForTest(redirectURI))
	if err != nil {
		t.Fatalf("CreateLoginTransaction: %v", err)
	}
	if created.TransactionID != transactionID || created.TransactionSecret != transactionSecret ||
		!created.ExpiresAt.Equal(mustLoginTransactionTestTime(t, testLoginExpires)) ||
		!reflect.DeepEqual(created.Methods, []string{"password", "google"}) {
		t.Fatalf("create result = %+v", created)
	}

	snapshot, err := client.InspectLoginTransaction(context.Background(), transactionID, transactionSecret)
	if err != nil {
		t.Fatalf("InspectLoginTransaction: %v", err)
	}
	if snapshot.TransactionID != transactionID || snapshot.Version != 2 ||
		snapshot.State != LoginTransactionStateAuthenticated ||
		!snapshot.ExpiresAt.Equal(mustLoginTransactionTestTime(t, testLoginExpires)) ||
		!snapshot.UpdatedAt.Equal(mustLoginTransactionTestTime(t, testLoginUpdated)) {
		t.Fatalf("status result = %+v", snapshot)
	}

	completion, err := client.CompleteLoginTransactionPassword(context.Background(), LoginTransactionPasswordInput{
		TransactionID:     transactionID,
		TransactionSecret: transactionSecret,
		Email:             "person@example.com",
		Password:          "correct-password",
	})
	if err != nil {
		t.Fatalf("CompleteLoginTransactionPassword: %v", err)
	}
	if completion.TransactionID != transactionID || completion.ExchangeToken != exchangeToken ||
		!completion.ExpiresAt.Equal(mustLoginTransactionTestTime(t, testLoginExpires)) {
		t.Fatalf("password result = %+v", completion)
	}

	authorization, err := client.ExchangeLoginTransaction(context.Background(), LoginTransactionExchangeInput{
		TransactionID:       transactionID,
		ExchangeToken:       exchangeToken,
		ExpectedRedirectURI: redirectURI,
		ExpectedOAuthState:  state,
	})
	if err != nil {
		t.Fatalf("ExchangeLoginTransaction: %v", err)
	}
	if authorization.Code != code {
		t.Fatalf("authorization code = %q, want %q", authorization.Code, code)
	}

	if createRequests.Load() != 1 || statusRequests.Load() != 1 || passwordRequests.Load() != 1 || exchangeRequests.Load() != 1 {
		t.Fatalf("request counts create=%d status=%d password=%d exchange=%d, want one each",
			createRequests.Load(), statusRequests.Load(), passwordRequests.Load(), exchangeRequests.Load())
	}
	if callbackRequests.Load() != 0 {
		t.Fatalf("303 was followed to loopback %d time(s)", callbackRequests.Load())
	}
	if sharedRedirectCalls.Load() != 0 {
		t.Fatalf("typed calls used shared CheckRedirect %d time(s)", sharedRedirectCalls.Load())
	}
	if client.HTTPClient != shared || shared.Transport != upstream.Client().Transport || shared.Jar != jar || shared.Timeout != 3*time.Second {
		t.Fatal("typed calls mutated the shared HTTP client")
	}
	if err := shared.CheckRedirect(nil, nil); !errors.Is(err, redirectSentinel) || sharedRedirectCalls.Load() != 1 {
		t.Fatal("shared CheckRedirect was replaced")
	}
}

func TestLoginTransactionClient_InvalidInputNeverReachesNetwork(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be reached", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	redirectURI := "http://127.0.0.1:49152/oauth/callback"
	validCreate := validLoginTransactionCreateInputForTest(redirectURI)
	validPassword := validLoginTransactionPasswordInputForTest()
	validExchange := validLoginTransactionExchangeInputForTest(redirectURI)

	tests := []struct {
		name   string
		invoke func(*Client) error
	}{
		{
			name: "nil client",
			invoke: func(_ *Client) error {
				var client *Client
				_, err := client.CreateLoginTransaction(context.Background(), validCreate)
				return err
			},
		},
		{
			name: "nil context",
			invoke: func(client *Client) error {
				_, err := client.CreateLoginTransaction(nil, validCreate)
				return err
			},
		},
		{
			name: "base URL has path",
			invoke: func(client *Client) error {
				client.BaseURL += "/not-an-origin"
				_, err := client.CreateLoginTransaction(context.Background(), validCreate)
				return err
			},
		},
		{
			name: "client ID has whitespace",
			invoke: func(client *Client) error {
				client.ClientID = " workmax-desktop"
				_, err := client.CreateLoginTransaction(context.Background(), validCreate)
				return err
			},
		},
		{
			name: "uppercase device ID",
			invoke: func(client *Client) error {
				input := validCreate
				input.DeviceID = strings.ToUpper(input.DeviceID)
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "localhost callback",
			invoke: func(client *Client) error {
				input := validCreate
				input.RedirectURI = "http://localhost:49152/oauth/callback"
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "empty callback fragment",
			invoke: func(client *Client) error {
				input := validCreate
				input.RedirectURI += "#"
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "non-canonical callback port",
			invoke: func(client *Client) error {
				input := validCreate
				input.RedirectURI = "http://127.0.0.1:049152/oauth/callback"
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "padded OAuth state",
			invoke: func(client *Client) error {
				input := validCreate
				input.OAuthState += "="
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "wrong PKCE method",
			invoke: func(client *Client) error {
				input := validCreate
				input.CodeChallengeMethod = "plain"
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "non-canonical scope",
			invoke: func(client *Client) error {
				input := validCreate
				input.Scope = "workagent  profile"
				_, err := client.CreateLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "status transaction ID",
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), "not-canonical", validPassword.TransactionSecret)
				return err
			},
		},
		{
			name: "status transaction secret",
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), validPassword.TransactionID, validPassword.TransactionSecret+"=")
				return err
			},
		},
		{
			name: "password email",
			invoke: func(client *Client) error {
				input := validPassword
				input.Email = "not-an-email"
				_, err := client.CompleteLoginTransactionPassword(context.Background(), input)
				return err
			},
		},
		{
			name: "password control",
			invoke: func(client *Client) error {
				input := validPassword
				input.Password = "secret\x00value"
				_, err := client.CompleteLoginTransactionPassword(context.Background(), input)
				return err
			},
		},
		{
			name: "exchange token",
			invoke: func(client *Client) error {
				input := validExchange
				input.ExchangeToken = "not-canonical"
				_, err := client.ExchangeLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "exchange callback path",
			invoke: func(client *Client) error {
				input := validExchange
				input.ExpectedRedirectURI = "http://127.0.0.1:49152/other"
				_, err := client.ExchangeLoginTransaction(context.Background(), input)
				return err
			},
		},
		{
			name: "exchange state",
			invoke: func(client *Client) error {
				input := validExchange
				input.ExpectedOAuthState += "="
				_, err := client.ExchangeLoginTransaction(context.Background(), input)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requests.Load()
			client := NewClient(upstream.URL)
			client.HTTPClient = upstream.Client()
			err := test.invoke(client)
			if !errors.Is(err, ErrLoginTransactionInvalidInput) {
				t.Fatalf("error = %v, want ErrLoginTransactionInvalidInput", err)
			}
			if requests.Load() != before {
				t.Fatalf("invalid input reached network: before=%d after=%d", before, requests.Load())
			}
			assertLoginTransactionErrorRedacted(t, err,
				validPassword.Password, validPassword.TransactionSecret, validExchange.ExchangeToken)
		})
	}
}

func TestMarshalLoginTransactionRequest_EnforcesBodyCap(t *testing.T) {
	_, err := marshalLoginTransactionRequest("test", map[string]string{
		"oversized": strings.Repeat("x", loginTransactionMaxRequestBodyBytes),
	})
	if !errors.Is(err, ErrLoginTransactionInvalidInput) {
		t.Fatalf("error = %v, want ErrLoginTransactionInvalidInput", err)
	}
}

type loginTransactionRoundTripFunc func(*http.Request) (*http.Response, error)

func (f loginTransactionRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestLoginTransactionClient_DoesNotRetryTransportFailures(t *testing.T) {
	redirectURI := "http://127.0.0.1:49152/oauth/callback"
	createInput := validLoginTransactionCreateInputForTest(redirectURI)
	passwordInput := validLoginTransactionPasswordInputForTest()
	exchangeInput := validLoginTransactionExchangeInputForTest(redirectURI)
	rawTransportError := "RAW_TRANSPORT_SECRET"

	tests := []struct {
		name   string
		invoke func(*Client) error
	}{
		{
			name: "create mutation",
			invoke: func(client *Client) error {
				_, err := client.CreateLoginTransaction(context.Background(), createInput)
				return err
			},
		},
		{
			name: "status",
			invoke: func(client *Client) error {
				_, err := client.InspectLoginTransaction(context.Background(), passwordInput.TransactionID, passwordInput.TransactionSecret)
				return err
			},
		},
		{
			name: "password mutation",
			invoke: func(client *Client) error {
				_, err := client.CompleteLoginTransactionPassword(context.Background(), passwordInput)
				return err
			},
		},
		{
			name: "exchange mutation",
			invoke: func(client *Client) error {
				_, err := client.ExchangeLoginTransaction(context.Background(), exchangeInput)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			transport := loginTransactionRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New(rawTransportError)
			})
			client := NewClient("https://go-server.invalid")
			client.HTTPClient = &http.Client{Transport: transport, Timeout: time.Second}

			err := test.invoke(client)
			if !errors.Is(err, ErrLoginTransactionTransport) {
				t.Fatalf("error = %v, want ErrLoginTransactionTransport", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("RoundTrip calls = %d, want exactly 1", calls.Load())
			}
			assertLoginTransactionErrorRedacted(t, err, rawTransportError,
				passwordInput.Password, passwordInput.TransactionSecret, exchangeInput.ExchangeToken)
		})
	}
}

func assertLoginTransactionStandardRequestHeaders(t *testing.T, request *http.Request) {
	t.Helper()
	for name, want := range map[string]string{
		"Accept":         "application/json",
		"Cache-Control":  "no-store",
		HeaderClientName: clientNameDesktop,
	} {
		values := request.Header.Values(name)
		if len(values) != 1 || values[0] != want {
			t.Errorf("%s = %q, want exactly %q", name, values, want)
		}
	}
	versions := request.Header.Values(HeaderClientVersion)
	if len(versions) != 1 || versions[0] == "" {
		t.Errorf("%s = %q, want exactly one non-empty value", HeaderClientVersion, versions)
	}
}

func assertLoginTransactionAuthorization(t *testing.T, request *http.Request, want string) {
	t.Helper()
	values := request.Header.Values("Authorization")
	if want == "" {
		if len(values) != 0 {
			t.Errorf("Authorization = %q, want absent", values)
		}
		return
	}
	if len(values) != 1 || values[0] != want {
		t.Errorf("Authorization = %q, want exactly %q", values, want)
	}
}

func assertLoginTransactionJSONRequest(t *testing.T, request *http.Request, want map[string]any) {
	t.Helper()
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 || contentTypes[0] != "application/json" {
		t.Errorf("Content-Type = %q, want exactly application/json", contentTypes)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
		return
	}
	if len(body) == 0 || len(body) > loginTransactionMaxRequestBodyBytes {
		t.Errorf("request body size = %d", len(body))
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Errorf("decode request body: %v", err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request body = %#v, want %#v", got, want)
	}
}

func assertLoginTransactionEmptyRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if values := request.Header.Values("Content-Type"); len(values) != 0 {
		t.Errorf("Content-Type = %q, want absent", values)
	}
	if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
		t.Errorf("empty request framing: ContentLength=%d TransferEncoding=%q", request.ContentLength, request.TransferEncoding)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read empty request: %v", err)
		return
	}
	if len(body) != 0 {
		t.Errorf("empty request body = %q", body)
	}
}

func writeLoginTransactionTestResponse(t *testing.T, writer http.ResponseWriter, status int, contentType string, body string) {
	t.Helper()
	if contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	writer.WriteHeader(status)
	if _, err := io.WriteString(writer, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func mustLoginTransactionTestTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func assertLoginTransactionErrorRedacted(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	for _, value := range forbidden {
		if value != "" && strings.Contains(message, value) {
			t.Errorf("error leaked forbidden value %q: %q", value, message)
		}
	}
}
