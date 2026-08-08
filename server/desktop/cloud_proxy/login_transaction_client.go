//go:build desktop

package cloud_proxy

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	loginTransactionMaxRequestBodyBytes  = 8 << 10
	loginTransactionMaxResponseBodyBytes = 16 << 10
	loginTransactionMaxContentTypeBytes  = 256
	loginTransactionMaxLocationBytes     = 4 << 10

	loginTransactionIDBytes         = 16
	loginTransactionCapabilityBytes = 32
	loginTransactionOAuthStateBytes = 16
	loginTransactionPKCEBytes       = 32
	loginTransactionAuthCodeBytes   = 32

	loginTransactionScheme = "DesktopLogin"
	loginExchangeScheme    = "DesktopExchange"
	loginPKCEMethodS256    = "S256"
)

// LoginTransactionMethodPassword is the Server method implemented by the
// first Desktop Login Transaction slice. A later coordinator must still check
// the advertised Methods before presenting a credential UI.
const LoginTransactionMethodPassword = "password"

// LoginTransactionState is the persisted state exposed by the authenticated
// Server status endpoint. These are orchestration states, never credentials.
type LoginTransactionState string

const (
	LoginTransactionStatePending                LoginTransactionState = "pending"
	LoginTransactionStatePasswordAuthenticating LoginTransactionState = "password_authenticating"
	LoginTransactionStateGooglePending          LoginTransactionState = "google_pending"
	LoginTransactionStateGoogleExchanging       LoginTransactionState = "google_exchanging"
	LoginTransactionStateAuthenticated          LoginTransactionState = "authenticated"
	LoginTransactionStateExchanged              LoginTransactionState = "exchanged"
	LoginTransactionStateFailed                 LoginTransactionState = "failed"
	LoginTransactionStateExpired                LoginTransactionState = "expired"
)

var (
	// ErrLoginTransactionInvalidInput means the Sidecar attempted to build a
	// request that did not satisfy the frozen Desktop protocol. The request is
	// rejected before it reaches the network.
	ErrLoginTransactionInvalidInput = errors.New("desktop login transaction: invalid input")
	// ErrLoginTransactionTransport is deliberately opaque: transport errors can
	// contain URLs or other environment detail and must not become Renderer
	// errors or logs through this security-sensitive client.
	ErrLoginTransactionTransport = errors.New("desktop login transaction: transport failure")
	// ErrLoginTransactionProtocol means the Server response was not the bounded,
	// typed response this client expected. Raw bodies, headers and redirect
	// locations are intentionally omitted from the returned error.
	ErrLoginTransactionProtocol = errors.New("desktop login transaction: invalid server response")
)

// LoginTransactionServerError is a bounded rejection returned by the Go
// Server. Code is either one of the protocol's public error codes or a local
// closed fallback; it is never copied from an arbitrary response value.
type LoginTransactionServerError struct {
	Operation  string
	StatusCode int
	Code       string
}

func (e *LoginTransactionServerError) Error() string {
	if e == nil {
		return "desktop login transaction: server rejected request"
	}
	return fmt.Sprintf("desktop login transaction %s: server rejected request (HTTP %d, code %s)",
		e.Operation, e.StatusCode, e.Code)
}

// LoginTransactionCreateInput is the OAuth/Device envelope the Sidecar
// freezes when it creates a Server-owned transaction. ClientID comes from the
// Client itself so a caller cannot accidentally drift it from token exchange.
type LoginTransactionCreateInput struct {
	DeviceID            string
	RedirectURI         string
	OAuthState          string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
}

// LoginTransactionHandle contains two bearer capabilities. It must remain in
// the Go Sidecar and must never be JSON-encoded into a local Renderer response.
type LoginTransactionHandle struct {
	TransactionID     string
	TransactionSecret string
	ExpiresAt         time.Time
	Methods           []string
}

// LoginTransactionSnapshot is the capability-authenticated, non-secret
// status envelope returned by the Server.
type LoginTransactionSnapshot struct {
	TransactionID string
	Version       uint64
	State         LoginTransactionState
	ExpiresAt     time.Time
	UpdatedAt     time.Time
}

// LoginTransactionPasswordInput carries a user's transient credentials plus
// the Sidecar-owned transaction capability. Callers must not persist or log
// this value.
type LoginTransactionPasswordInput struct {
	TransactionID     string
	TransactionSecret string
	Email             string
	Password          string
}

// LoginTransactionCompletion contains the post-authentication one-time
// capability. It stays inside the Go coordinator and is consumed immediately.
type LoginTransactionCompletion struct {
	TransactionID string
	ExchangeToken string
	ExpiresAt     time.Time
}

// LoginTransactionExchangeInput supplies the one-time exchange capability and
// the exact callback binding retained by the Sidecar. ExpectedRedirectURI and
// ExpectedOAuthState are used to validate the 303 without following it.
type LoginTransactionExchangeInput struct {
	TransactionID       string
	ExchangeToken       string
	ExpectedRedirectURI string
	ExpectedOAuthState  string
}

// LoginTransactionAuthorization is the sensitive authorization result parsed
// from a validated 303. The code should be passed directly to
// ExchangeCodeForToken and never stored or sent across the Electron bridge.
type LoginTransactionAuthorization struct {
	Code string
}

type loginTransactionCreateRequest struct {
	ClientID            string `json:"client_id"`
	DeviceID            string `json:"device_id"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
}

type loginTransactionCreateResponse struct {
	TransactionID     string    `json:"transaction_id"`
	TransactionSecret string    `json:"transaction_secret"`
	ExpiresAt         time.Time `json:"expires_at"`
	Methods           []string  `json:"methods"`
}

type loginTransactionStatusResponse struct {
	TransactionID string                `json:"transaction_id"`
	Version       uint64                `json:"version"`
	State         LoginTransactionState `json:"state"`
	ExpiresAt     time.Time             `json:"expires_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

type loginTransactionPasswordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginTransactionPasswordResponse struct {
	TransactionID string    `json:"transaction_id"`
	ExchangeToken string    `json:"exchange_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type loginTransactionErrorResponse struct {
	Error string `json:"error"`
}

// CreateLoginTransaction creates one Server-owned transaction. It makes one
// http.Client.Do call, has no application-level retry, and never follows a
// redirect.
func (c *Client) CreateLoginTransaction(
	ctx context.Context,
	in LoginTransactionCreateInput,
) (LoginTransactionHandle, error) {
	const operation = "create"
	baseURL, err := c.validateLoginTransactionClient(operation)
	if err != nil {
		return LoginTransactionHandle{}, err
	}
	if err := validateLoginTransactionCreateInput(c.ClientID, in); err != nil {
		return LoginTransactionHandle{}, invalidLoginTransactionInput(operation, err.Error())
	}

	body, err := marshalLoginTransactionRequest(operation, loginTransactionCreateRequest{
		ClientID:            c.ClientID,
		DeviceID:            in.DeviceID,
		RedirectURI:         in.RedirectURI,
		State:               in.OAuthState,
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: in.CodeChallengeMethod,
		Scope:               in.Scope,
	})
	if err != nil {
		return LoginTransactionHandle{}, err
	}
	defer clear(body)

	var response loginTransactionCreateResponse
	if err := c.doLoginTransactionJSON(
		ctx,
		operation,
		http.MethodPost,
		baseURL+CloudRouteLoginTransactionCreate,
		"",
		body,
		http.StatusCreated,
		&response,
	); err != nil {
		return LoginTransactionHandle{}, err
	}
	if !validCanonicalLoginToken(response.TransactionID, loginTransactionIDBytes) {
		return LoginTransactionHandle{}, protocolLoginTransactionError(operation, "transaction_id is malformed")
	}
	if !validCanonicalLoginToken(response.TransactionSecret, loginTransactionCapabilityBytes) {
		return LoginTransactionHandle{}, protocolLoginTransactionError(operation, "transaction capability is malformed")
	}
	if response.ExpiresAt.IsZero() {
		return LoginTransactionHandle{}, protocolLoginTransactionError(operation, "expires_at is missing")
	}
	methods, ok := validateLoginTransactionMethods(response.Methods)
	if !ok {
		return LoginTransactionHandle{}, protocolLoginTransactionError(operation, "methods are malformed")
	}

	return LoginTransactionHandle{
		TransactionID:     response.TransactionID,
		TransactionSecret: response.TransactionSecret,
		ExpiresAt:         response.ExpiresAt.UTC(),
		Methods:           methods,
	}, nil
}

// InspectLoginTransaction fetches the authenticated, non-secret transaction
// state. It makes one http.Client.Do call, has no application-level retry, and
// sends no request body.
func (c *Client) InspectLoginTransaction(
	ctx context.Context,
	transactionID string,
	transactionSecret string,
) (LoginTransactionSnapshot, error) {
	const operation = "status"
	baseURL, err := c.validateLoginTransactionClient(operation)
	if err != nil {
		return LoginTransactionSnapshot{}, err
	}
	if !validCanonicalLoginToken(transactionID, loginTransactionIDBytes) {
		return LoginTransactionSnapshot{}, invalidLoginTransactionInput(operation, "transaction_id is malformed")
	}
	if !validCanonicalLoginToken(transactionSecret, loginTransactionCapabilityBytes) {
		return LoginTransactionSnapshot{}, invalidLoginTransactionInput(operation, "transaction capability is malformed")
	}

	var response loginTransactionStatusResponse
	if err := c.doLoginTransactionJSON(
		ctx,
		operation,
		http.MethodGet,
		baseURL+expandLoginTransactionRoute(CloudRouteLoginTransactionStatus, transactionID),
		loginTransactionScheme+" "+transactionSecret,
		nil,
		http.StatusOK,
		&response,
	); err != nil {
		return LoginTransactionSnapshot{}, err
	}
	if response.TransactionID != transactionID {
		return LoginTransactionSnapshot{}, protocolLoginTransactionError(operation, "transaction_id does not match request")
	}
	if response.Version == 0 || !validLoginTransactionState(response.State) {
		return LoginTransactionSnapshot{}, protocolLoginTransactionError(operation, "transaction state is malformed")
	}
	if response.ExpiresAt.IsZero() || response.UpdatedAt.IsZero() {
		return LoginTransactionSnapshot{}, protocolLoginTransactionError(operation, "transaction timestamps are missing")
	}

	return LoginTransactionSnapshot{
		TransactionID: response.TransactionID,
		Version:       response.Version,
		State:         response.State,
		ExpiresAt:     response.ExpiresAt.UTC(),
		UpdatedAt:     response.UpdatedAt.UTC(),
	}, nil
}

// CompleteLoginTransactionPassword verifies credentials through the Server and
// returns its one-time post-authentication capability. It makes one
// http.Client.Do call and has no application-level retry; callers decide how
// an ambiguous outcome is recovered and must never replay the password
// automatically.
func (c *Client) CompleteLoginTransactionPassword(
	ctx context.Context,
	in LoginTransactionPasswordInput,
) (LoginTransactionCompletion, error) {
	const operation = "password"
	baseURL, err := c.validateLoginTransactionClient(operation)
	if err != nil {
		return LoginTransactionCompletion{}, err
	}
	if err := validateLoginTransactionPasswordInput(in); err != nil {
		return LoginTransactionCompletion{}, invalidLoginTransactionInput(operation, err.Error())
	}

	body, err := marshalLoginTransactionRequest(operation, loginTransactionPasswordRequest{
		Email:    in.Email,
		Password: in.Password,
	})
	if err != nil {
		return LoginTransactionCompletion{}, err
	}
	// The JSON buffer contains the only user-supplied secret in this client.
	// Clear it as soon as the one-shot request/response call returns; callers
	// still own their input string and must never log or persist it.
	defer clear(body)

	var response loginTransactionPasswordResponse
	if err := c.doLoginTransactionJSON(
		ctx,
		operation,
		http.MethodPost,
		baseURL+expandLoginTransactionRoute(CloudRouteLoginTransactionPassword, in.TransactionID),
		loginTransactionScheme+" "+in.TransactionSecret,
		body,
		http.StatusOK,
		&response,
	); err != nil {
		return LoginTransactionCompletion{}, err
	}
	if response.TransactionID != in.TransactionID {
		return LoginTransactionCompletion{}, protocolLoginTransactionError(operation, "transaction_id does not match request")
	}
	if !validCanonicalLoginToken(response.ExchangeToken, loginTransactionCapabilityBytes) {
		return LoginTransactionCompletion{}, protocolLoginTransactionError(operation, "exchange capability is malformed")
	}
	if response.ExpiresAt.IsZero() {
		return LoginTransactionCompletion{}, protocolLoginTransactionError(operation, "expires_at is missing")
	}

	return LoginTransactionCompletion{
		TransactionID: response.TransactionID,
		ExchangeToken: response.ExchangeToken,
		ExpiresAt:     response.ExpiresAt.UTC(),
	}, nil
}

// ExchangeLoginTransaction consumes the one-time post-authentication
// capability. The Server's 303 is deliberately not followed: this method
// validates its exact loopback binding and returns only the authorization code
// to the Go coordinator.
func (c *Client) ExchangeLoginTransaction(
	ctx context.Context,
	in LoginTransactionExchangeInput,
) (LoginTransactionAuthorization, error) {
	const operation = "exchange"
	baseURL, err := c.validateLoginTransactionClient(operation)
	if err != nil {
		return LoginTransactionAuthorization{}, err
	}
	if !validCanonicalLoginToken(in.TransactionID, loginTransactionIDBytes) {
		return LoginTransactionAuthorization{}, invalidLoginTransactionInput(operation, "transaction_id is malformed")
	}
	if !validCanonicalLoginToken(in.ExchangeToken, loginTransactionCapabilityBytes) {
		return LoginTransactionAuthorization{}, invalidLoginTransactionInput(operation, "exchange capability is malformed")
	}
	expectedRedirect, ok := parseCanonicalLoginLoopback(in.ExpectedRedirectURI)
	if !ok {
		return LoginTransactionAuthorization{}, invalidLoginTransactionInput(operation, "expected redirect_uri is malformed")
	}
	if !validCanonicalLoginToken(in.ExpectedOAuthState, loginTransactionOAuthStateBytes) {
		return LoginTransactionAuthorization{}, invalidLoginTransactionInput(operation, "expected OAuth state is malformed")
	}

	response, err := c.executeLoginTransactionRequest(
		ctx,
		operation,
		http.MethodPost,
		baseURL+expandLoginTransactionRoute(CloudRouteLoginTransactionExchange, in.TransactionID),
		loginExchangeScheme+" "+in.ExchangeToken,
		nil,
	)
	if err != nil {
		return LoginTransactionAuthorization{}, err
	}
	defer response.Body.Close()

	body, err := readBoundedLoginTransactionBody(response)
	if err != nil {
		return LoginTransactionAuthorization{}, errForLoginTransactionResponse(operation, err)
	}
	defer clear(body)
	if response.StatusCode != http.StatusSeeOther {
		return LoginTransactionAuthorization{}, decodeLoginTransactionServerError(operation, response, body)
	}

	code, ok := validatedLoginTransactionLocation(
		response.Header,
		expectedRedirect,
		in.ExpectedOAuthState,
	)
	if !ok {
		return LoginTransactionAuthorization{}, protocolLoginTransactionError(operation, "redirect Location is malformed")
	}
	return LoginTransactionAuthorization{Code: code}, nil
}

func (c *Client) validateLoginTransactionClient(operation string) (string, error) {
	if c == nil {
		return "", invalidLoginTransactionInput(operation, "client is missing")
	}
	baseURL, err := NormalizeBaseURL(c.BaseURL)
	if err != nil {
		return "", invalidLoginTransactionInput(operation, "cloud base URL is malformed")
	}
	parsedBaseURL, err := url.Parse(baseURL)
	if err != nil || (parsedBaseURL.Scheme != "https" &&
		!(parsedBaseURL.Scheme == "http" &&
			(parsedBaseURL.Hostname() == "127.0.0.1" || parsedBaseURL.Hostname() == "::1"))) {
		return "", invalidLoginTransactionInput(operation, "cloud base URL is not secure")
	}
	if !validBoundedLoginText(c.ClientID, 1, 64) {
		return "", invalidLoginTransactionInput(operation, "client_id is malformed")
	}
	return baseURL, nil
}

func (c *Client) doLoginTransactionJSON(
	ctx context.Context,
	operation string,
	method string,
	requestURL string,
	authorization string,
	body []byte,
	wantStatus int,
	target any,
) error {
	response, err := c.executeLoginTransactionRequest(
		ctx,
		operation,
		method,
		requestURL,
		authorization,
		body,
	)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := readBoundedLoginTransactionBody(response)
	if err != nil {
		return errForLoginTransactionResponse(operation, err)
	}
	defer clear(raw)
	if !isLoginTransactionJSONContentType(response.Header) {
		return protocolLoginTransactionError(operation, "Content-Type is not canonical JSON")
	}
	if response.StatusCode != wantStatus {
		return decodeLoginTransactionServerError(operation, response, raw)
	}
	if !decodeLoginTransactionJSONObject(raw, target) {
		return protocolLoginTransactionError(operation, "JSON response is malformed")
	}
	return nil
}

func (c *Client) executeLoginTransactionRequest(
	ctx context.Context,
	operation string,
	method string,
	requestURL string,
	authorization string,
	body []byte,
) (*http.Response, error) {
	if ctx == nil {
		return nil, invalidLoginTransactionInput(operation, "context is missing")
	}
	if len(body) > loginTransactionMaxRequestBodyBytes {
		return nil, invalidLoginTransactionInput(operation, "request body is too large")
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, invalidLoginTransactionInput(operation, "request URL is malformed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	SetClientHeaders(request.Header)

	response, err := c.loginTransactionHTTPClient().Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("desktop login transaction %s: %w: %w",
				operation, ErrLoginTransactionTransport, ctxErr)
		}
		return nil, fmt.Errorf("desktop login transaction %s: %w", operation, ErrLoginTransactionTransport)
	}
	return response, nil
}

// loginTransactionHTTPClient is an intentionally separate client. It reuses
// only the concurrency-safe transport and timeout from Client.HTTPClient; it
// carries neither its redirect callback nor its cookie jar. Most importantly,
// it never mutates the shared Client while other cloud operations are active.
func (c *Client) loginTransactionHTTPClient() *http.Client {
	return c.credentialHTTPClient()
}

func marshalLoginTransactionRequest(operation string, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, invalidLoginTransactionInput(operation, "request JSON cannot be encoded")
	}
	if len(body) == 0 || len(body) > loginTransactionMaxRequestBodyBytes {
		return nil, invalidLoginTransactionInput(operation, "request body is too large")
	}
	return body, nil
}

func readBoundedLoginTransactionBody(response *http.Response) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, ErrLoginTransactionProtocol
	}
	if response.ContentLength < -1 || response.ContentLength > loginTransactionMaxResponseBodyBytes {
		return nil, ErrLoginTransactionProtocol
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, loginTransactionMaxResponseBodyBytes+1))
	if err != nil {
		clear(body)
		return nil, ErrLoginTransactionTransport
	}
	if len(body) > loginTransactionMaxResponseBodyBytes {
		clear(body)
		return nil, ErrLoginTransactionProtocol
	}
	if response.ContentLength >= 0 && int64(len(body)) != response.ContentLength {
		clear(body)
		return nil, ErrLoginTransactionProtocol
	}
	return body, nil
}

func errForLoginTransactionResponse(operation string, err error) error {
	if errors.Is(err, ErrLoginTransactionTransport) {
		return fmt.Errorf("desktop login transaction %s: %w", operation, ErrLoginTransactionTransport)
	}
	return protocolLoginTransactionError(operation, "response body is malformed")
}

func isLoginTransactionJSONContentType(header http.Header) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	value := values[0]
	if len(value) == 0 || len(value) > loginTransactionMaxContentTypeBytes ||
		!utf8.ValidString(value) || hasLoginControl(value) || strings.TrimSpace(value) != value {
		return false
	}
	mediaType, params, err := mime.ParseMediaType(value)
	if err != nil || mediaType != "application/json" {
		return false
	}
	if len(params) == 0 {
		return true
	}
	return len(params) == 1 && strings.EqualFold(params["charset"], "utf-8")
}

func decodeLoginTransactionJSONObject(body []byte, target any) bool {
	knownKeys, ok := loginTransactionKnownJSONKeys(target)
	if len(body) == 0 || !utf8.Valid(body) || target == nil || !ok ||
		!hasUniqueLoginTransactionTopLevelKeys(body, knownKeys) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func hasUniqueLoginTransactionTopLevelKeys(body []byte, knownKeys map[string]struct{}) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		if _, exact := knownKeys[key]; !exact {
			// encoding/json accepts case-insensitive aliases for tagged fields.
			// Reject those aliases before decoding so, for example,
			// Transaction_ID cannot silently overwrite transaction_id. Truly
			// additive, uniquely named response fields remain forward-compatible.
			for known := range knownKeys {
				if strings.EqualFold(key, known) {
					return false
				}
			}
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return false
	}
	_, err = decoder.Token()
	return errors.Is(err, io.EOF)
}

func loginTransactionKnownJSONKeys(target any) (map[string]struct{}, bool) {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	targetType = targetType.Elem()
	keys := make(map[string]struct{}, targetType.NumField())
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if name == "-" {
			continue
		}
		keys[name] = struct{}{}
	}
	return keys, len(keys) != 0
}

func decodeLoginTransactionServerError(
	operation string,
	response *http.Response,
	body []byte,
) error {
	if response == nil || !isLoginTransactionJSONContentType(response.Header) {
		return protocolLoginTransactionError(operation, "error response is not canonical JSON")
	}
	if response.StatusCode < http.StatusBadRequest || response.StatusCode > 599 {
		return protocolLoginTransactionError(operation, "error response status is malformed")
	}
	var envelope loginTransactionErrorResponse
	if !decodeLoginTransactionJSONObject(body, &envelope) {
		return protocolLoginTransactionError(operation, "error response is malformed")
	}
	code := canonicalLoginTransactionServerCode(envelope.Error)
	if response.StatusCode == http.StatusTooManyRequests {
		code = "rate_limited"
	}
	if code != "" && !validLoginTransactionServerCodeStatus(code, response.StatusCode) {
		return protocolLoginTransactionError(operation, "error response status and code do not match")
	}
	if code == "" {
		code = "upstream_rejected"
	}
	return &LoginTransactionServerError{
		Operation:  operation,
		StatusCode: response.StatusCode,
		Code:       code,
	}
}

func validLoginTransactionServerCodeStatus(code string, status int) bool {
	switch code {
	case "invalid_request":
		return status == http.StatusBadRequest
	case "identity_unavailable":
		return status == http.StatusInternalServerError || status == http.StatusServiceUnavailable
	case "invalid_transaction", "invalid_credentials":
		return status == http.StatusUnauthorized
	case "transaction_expired":
		return status == http.StatusGone
	case "transaction_conflict", "transaction_complete":
		return status == http.StatusConflict
	case "rate_limited":
		return status == http.StatusTooManyRequests
	default:
		return false
	}
}

func canonicalLoginTransactionServerCode(code string) string {
	switch code {
	case "invalid_request",
		"identity_unavailable",
		"invalid_transaction",
		"invalid_credentials",
		"transaction_expired",
		"transaction_conflict",
		"transaction_complete":
		return code
	default:
		return ""
	}
}

func validatedLoginTransactionLocation(
	header http.Header,
	expected *url.URL,
	expectedState string,
) (string, bool) {
	values := header.Values("Location")
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > loginTransactionMaxLocationBytes ||
		strings.TrimSpace(values[0]) != values[0] || !utf8.ValidString(values[0]) ||
		hasLoginControl(values[0]) || strings.Contains(values[0], "#") {
		return "", false
	}
	location, err := url.Parse(values[0])
	if err != nil || location.Scheme != expected.Scheme || location.Host != expected.Host ||
		location.User != nil || location.Path != expected.Path || location.RawPath != "" ||
		location.Fragment != "" || location.Opaque != "" || location.ForceQuery {
		return "", false
	}
	query, err := url.ParseQuery(location.RawQuery)
	if err != nil || len(query) != 2 || len(query["code"]) != 1 || len(query["state"]) != 1 {
		return "", false
	}
	code := query["code"][0]
	state := query["state"][0]
	if !validCanonicalLoginToken(code, loginTransactionAuthCodeBytes) ||
		!validCanonicalLoginToken(state, loginTransactionOAuthStateBytes) ||
		subtle.ConstantTimeCompare([]byte(state), []byte(expectedState)) != 1 {
		return "", false
	}
	// Code and state use canonical unpadded base64url, so their encoded values
	// need no escaping. Pinning the exact query string rejects duplicate,
	// reordered, empty and non-canonical encodings that url.Values would
	// otherwise normalize for us.
	if location.RawQuery != "code="+code+"&state="+state {
		return "", false
	}
	return code, true
}

func parseCanonicalLoginLoopback(raw string) (*url.URL, bool) {
	if len(raw) == 0 || len(raw) > 500 || strings.TrimSpace(raw) != raw || !utf8.ValidString(raw) ||
		hasLoginControl(raw) || strings.Contains(raw, "#") {
		return nil, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User != nil || parsed.Path != "/oauth/callback" || parsed.RawPath != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" || parsed.ForceQuery {
		return nil, false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || parsed.Host != "127.0.0.1:"+strconv.Itoa(port) {
		return nil, false
	}
	return parsed, true
}

func validateLoginTransactionCreateInput(clientID string, in LoginTransactionCreateInput) error {
	if !validBoundedLoginText(clientID, 1, 64) {
		return errors.New("client_id is malformed")
	}
	if len(in.DeviceID) != 32 || in.DeviceID != strings.ToLower(in.DeviceID) {
		return errors.New("device_id is malformed")
	}
	if _, err := hex.DecodeString(in.DeviceID); err != nil {
		return errors.New("device_id is malformed")
	}
	if _, ok := parseCanonicalLoginLoopback(in.RedirectURI); !ok {
		return errors.New("redirect_uri is malformed")
	}
	if !validCanonicalLoginToken(in.OAuthState, loginTransactionOAuthStateBytes) {
		return errors.New("OAuth state is malformed")
	}
	if in.CodeChallengeMethod != loginPKCEMethodS256 ||
		!validCanonicalLoginToken(in.CodeChallenge, loginTransactionPKCEBytes) {
		return errors.New("PKCE challenge is malformed")
	}
	if !validCanonicalLoginScope(in.Scope) {
		return errors.New("scope is malformed")
	}
	return nil
}

func validateLoginTransactionPasswordInput(in LoginTransactionPasswordInput) error {
	if !validCanonicalLoginToken(in.TransactionID, loginTransactionIDBytes) {
		return errors.New("transaction_id is malformed")
	}
	if !validCanonicalLoginToken(in.TransactionSecret, loginTransactionCapabilityBytes) {
		return errors.New("transaction capability is malformed")
	}
	if !validBoundedLoginText(in.Email, 3, 320) || !strings.Contains(in.Email, "@") {
		return errors.New("email is malformed")
	}
	if len(in.Password) == 0 || len(in.Password) > 1024 || !utf8.ValidString(in.Password) || hasLoginControlExceptWhitespace(in.Password) {
		return errors.New("password is malformed")
	}
	return nil
}

func validCanonicalLoginToken(value string, byteCount int) bool {
	if value == "" || len(value) != base64.RawURLEncoding.EncodedLen(byteCount) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == byteCount && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validBoundedLoginText(value string, minBytes int, maxBytes int) bool {
	return len(value) >= minBytes && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !hasLoginControl(value)
}

func validCanonicalLoginScope(scope string) bool {
	if len(scope) == 0 || len(scope) > 255 || strings.TrimSpace(scope) != scope || strings.Join(strings.Fields(scope), " ") != scope {
		return false
	}
	for _, token := range strings.Fields(scope) {
		for i := 0; i < len(token); i++ {
			b := token[i]
			if b < 0x21 || b > 0x7e || b == 0x22 || b == 0x5c {
				return false
			}
		}
	}
	return true
}

func validateLoginTransactionMethods(methods []string) ([]string, bool) {
	if len(methods) == 0 || len(methods) > 8 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(methods))
	hasPassword := false
	out := make([]string, len(methods))
	for i, method := range methods {
		if len(method) == 0 || len(method) > 64 {
			return nil, false
		}
		for j := 0; j < len(method); j++ {
			b := method[j]
			if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-') {
				return nil, false
			}
		}
		if _, duplicate := seen[method]; duplicate {
			return nil, false
		}
		seen[method] = struct{}{}
		out[i] = method
		hasPassword = hasPassword || method == LoginTransactionMethodPassword
	}
	return out, hasPassword
}

func validLoginTransactionState(state LoginTransactionState) bool {
	switch state {
	case LoginTransactionStatePending,
		LoginTransactionStatePasswordAuthenticating,
		LoginTransactionStateGooglePending,
		LoginTransactionStateGoogleExchanging,
		LoginTransactionStateAuthenticated,
		LoginTransactionStateExchanged,
		LoginTransactionStateFailed,
		LoginTransactionStateExpired:
		return true
	default:
		return false
	}
}

func hasLoginControl(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}

func hasLoginControlExceptWhitespace(value string) bool {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0x7f {
			return true
		}
	}
	return false
}

func expandLoginTransactionRoute(pattern string, transactionID string) string {
	return strings.Replace(pattern, ":id", transactionID, 1)
}

func invalidLoginTransactionInput(operation string, reason string) error {
	return fmt.Errorf("desktop login transaction %s: %w: %s", operation, ErrLoginTransactionInvalidInput, reason)
}

func protocolLoginTransactionError(operation string, reason string) error {
	return fmt.Errorf("desktop login transaction %s: %w: %s", operation, ErrLoginTransactionProtocol, reason)
}
