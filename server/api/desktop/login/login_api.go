// Package login exposes the Server-owned Desktop Login Transaction protocol.
// It is a JSON/redirect protocol for the trusted Sidecar, not a Web login UI.
package login

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	logintransaction "server/service/desktop/logintransaction"
	oauthservice "server/service/desktop/oauth"
	"server/service/identity"
	"server/service/secrets"
)

const (
	maxCreateBodyBytes   = 8 << 10
	maxPasswordBodyBytes = 8 << 10
	transactionIDBytes   = 16
	capabilityBytes      = 32
	transactionScheme    = "DesktopLogin"
	exchangeScheme       = "DesktopExchange"
)

type codeIssuer interface {
	ExchangeAndIssue(context.Context, logintransaction.ExchangeInput) (logintransaction.IssuedAuthorization, error)
}

// LoginApi carries the long-lived Desktop identity services. Tests may inject
// fakes; production uses NewLoginApi with the system database.
type LoginApi struct {
	ClientRegistry *oauthservice.ClientRegistry
	Transactions   *logintransaction.Service
	CodeIssuer     codeIssuer
}

func NewLoginApi(db *gorm.DB) (*LoginApi, error) {
	repo, err := logintransaction.NewGORMRepository(db)
	if err != nil {
		return nil, err
	}
	if err := secrets.ValidateConfiguration(); err != nil {
		return nil, fmt.Errorf("desktop login transaction API: secrets configuration: %w", err)
	}
	password, err := identity.NewPasswordAuthenticator(db)
	if err != nil {
		return nil, err
	}
	transactions, err := logintransaction.NewService(repo, password, nil, logintransaction.Options{})
	if err != nil {
		return nil, err
	}
	issuer, err := logintransaction.NewOAuthCodeIssuer(db)
	if err != nil {
		return nil, err
	}
	return &LoginApi{
		ClientRegistry: oauthservice.NewClientRegistry(db),
		Transactions:   transactions,
		CodeIssuer:     issuer,
	}, nil
}

type createRequest struct {
	ClientID            string `json:"client_id"`
	DeviceID            string `json:"device_id"`
	RedirectURI         string `json:"redirect_uri"`
	State               string `json:"state"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
	Scope               string `json:"scope"`
}

type createResponse struct {
	TransactionID     string    `json:"transaction_id"`
	TransactionSecret string    `json:"transaction_secret"`
	ExpiresAt         time.Time `json:"expires_at"`
	Methods           []string  `json:"methods"`
}

func (a *LoginApi) Create(c *gin.Context) {
	setNoStoreHeaders(c)
	if a == nil || a.ClientRegistry == nil || a.Transactions == nil {
		writeError(c, http.StatusServiceUnavailable, "identity_unavailable")
		return
	}
	var request createRequest
	if err := decodeStrictJSON(c, maxCreateBodyBytes, &request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	client, err := a.ClientRegistry.FindActiveClient(c.Request.Context(), request.ClientID)
	if err != nil {
		writeClientRegistryError(c, err)
		return
	}
	if err := a.ClientRegistry.ValidateRedirectURI(client, request.RedirectURI); err != nil {
		writeClientRegistryError(c, err)
		return
	}
	canonicalScope, err := a.ClientRegistry.ValidateScopes(client, request.Scope)
	if err != nil {
		writeClientRegistryError(c, err)
		return
	}

	handle, err := a.Transactions.Create(c.Request.Context(), logintransaction.CreateInput{
		ClientID:            request.ClientID,
		DeviceID:            request.DeviceID,
		RedirectURI:         request.RedirectURI,
		OAuthState:          request.State,
		CodeChallenge:       request.CodeChallenge,
		CodeChallengeMethod: request.CodeChallengeMethod,
		Scope:               canonicalScope,
	})
	if err != nil {
		if errors.Is(err, logintransaction.ErrInvalidInput) {
			writeError(c, http.StatusBadRequest, "invalid_request")
			return
		}
		writeError(c, http.StatusInternalServerError, "identity_unavailable")
		return
	}
	c.JSON(http.StatusCreated, createResponse{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		ExpiresAt:         handle.ExpiresAt,
		Methods:           []string{"password"},
	})
}

func (a *LoginApi) Status(c *gin.Context) {
	setNoStoreHeaders(c)
	if requestHasBody(c.Request) {
		writeError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	secret, ok := readCapability(c.Request, transactionScheme)
	if !ok || !validEncodedBytes(c.Param("id"), transactionIDBytes) || a == nil || a.Transactions == nil {
		writeError(c, http.StatusUnauthorized, "invalid_transaction")
		return
	}
	snapshot, err := a.Transactions.InspectAuthenticated(c.Request.Context(), c.Param("id"), secret)
	if err != nil {
		writeTransactionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"transaction_id": snapshot.TransactionID,
		"version":        snapshot.Version,
		"state":          snapshot.State,
		"expires_at":     snapshot.ExpiresAt,
		"updated_at":     snapshot.UpdatedAt,
	})
}

type passwordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *LoginApi) CompletePassword(c *gin.Context) {
	setNoStoreHeaders(c)
	secret, ok := readCapability(c.Request, transactionScheme)
	if !ok || !validEncodedBytes(c.Param("id"), transactionIDBytes) || a == nil || a.Transactions == nil {
		writeError(c, http.StatusUnauthorized, "invalid_transaction")
		return
	}
	var request passwordRequest
	if err := decodeStrictJSON(c, maxPasswordBodyBytes, &request); err != nil {
		writeError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	completion, err := a.Transactions.CompletePassword(c.Request.Context(), logintransaction.PasswordCompletionInput{
		TransactionID:     c.Param("id"),
		TransactionSecret: secret,
		Email:             request.Email,
		Password:          request.Password,
	})
	if err != nil {
		if errors.Is(err, logintransaction.ErrAuthenticationFailed) {
			writeError(c, http.StatusUnauthorized, "invalid_credentials")
			return
		}
		writeTransactionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"transaction_id": completion.TransactionID,
		"exchange_token": completion.ExchangeToken,
		"expires_at":     completion.ExpiresAt,
	})
}

// Exchange consumes the post-authentication capability, creates a device-bound
// OAuth authorization code atomically, and redirects only to the frozen exact
// loopback callback. A trusted Sidecar must handle the redirect; the generic
// Renderer is not allowed to call this route or observe its Location value.
func (a *LoginApi) Exchange(c *gin.Context) {
	setNoStoreHeaders(c)
	if requestHasBody(c.Request) {
		writeError(c, http.StatusBadRequest, "invalid_request")
		return
	}
	token, ok := readCapability(c.Request, exchangeScheme)
	if !ok || !validEncodedBytes(c.Param("id"), transactionIDBytes) || a == nil || a.CodeIssuer == nil {
		writeError(c, http.StatusUnauthorized, "invalid_transaction")
		return
	}
	issued, err := a.CodeIssuer.ExchangeAndIssue(c.Request.Context(), logintransaction.ExchangeInput{
		TransactionID: c.Param("id"),
		ExchangeToken: token,
	})
	if err != nil {
		writeTransactionError(c, err)
		return
	}
	location, err := loopbackRedirect(issued.RedirectURI, issued.Code, issued.OAuthState)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "identity_unavailable")
		return
	}
	c.Redirect(http.StatusSeeOther, location)
}

func decodeStrictJSON(c *gin.Context, maxBytes int64, target any) error {
	contentTypes := c.Request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return errors.New("Content-Type must appear exactly once")
	}
	mediaType, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" ||
		(len(params) != 0 && (len(params) != 1 || !strings.EqualFold(params["charset"], "utf-8"))) {
		return errors.New("Content-Type must be application/json")
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes))
	if err != nil {
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8 JSON")
	}
	allowedFields, err := exactJSONFieldNames(target)
	if err != nil {
		return err
	}
	if err := rejectDuplicateTopLevelKeys(body, allowedFields); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func exactJSONFieldNames(target any) (map[string]struct{}, error) {
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() != reflect.Struct {
		return nil, errors.New("JSON target must be a pointer to a struct")
	}
	targetType = targetType.Elem()
	allowed := make(map[string]struct{}, targetType.NumField())
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		allowed[name] = struct{}{}
	}
	return allowed, nil
}

func rejectDuplicateTopLevelKeys(body []byte, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("request must contain one JSON object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("request object key is malformed")
		}
		if _, exact := allowed[key]; !exact {
			return fmt.Errorf("unknown or non-canonical JSON key %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate JSON key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("request must contain one JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func readCapability(request *http.Request, expectedScheme string) (string, bool) {
	values := request.Header.Values("Authorization")
	if len(values) != 1 {
		return "", false
	}
	parts := strings.Split(values[0], " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], expectedScheme) ||
		parts[1] == "" || len(parts[1]) > 512 || strings.TrimSpace(parts[1]) != parts[1] {
		return "", false
	}
	if !validEncodedBytes(parts[1], capabilityBytes) {
		return "", false
	}
	return parts[1], true
}

func validEncodedBytes(value string, byteCount int) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(byteCount) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == byteCount && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func requestHasBody(request *http.Request) bool {
	// A negative content length means an unknown-length stream (for example an
	// HTTP/2 request body), so it must be treated as present rather than empty.
	return request.ContentLength != 0 || len(request.TransferEncoding) != 0
}

func loopbackRedirect(raw, code, state string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User != nil || parsed.Path != "/oauth/callback" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid frozen loopback redirect")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid frozen loopback redirect port")
	}
	query := parsed.Query()
	query.Set("code", code)
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func setNoStoreHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
}

func writeTransactionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, logintransaction.ErrExpired):
		writeError(c, http.StatusGone, "transaction_expired")
	case errors.Is(err, logintransaction.ErrConflict):
		writeError(c, http.StatusConflict, "transaction_conflict")
	case errors.Is(err, logintransaction.ErrReplay), errors.Is(err, logintransaction.ErrInvalidState):
		writeError(c, http.StatusConflict, "transaction_complete")
	case errors.Is(err, logintransaction.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, logintransaction.ErrInvalidTransaction):
		writeError(c, http.StatusUnauthorized, "invalid_transaction")
	default:
		writeError(c, http.StatusInternalServerError, "identity_unavailable")
	}
}

func writeClientRegistryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, oauthservice.ErrClientNotFound),
		errors.Is(err, oauthservice.ErrClientInactive),
		errors.Is(err, oauthservice.ErrRedirectURIInvalid),
		errors.Is(err, oauthservice.ErrRedirectURIMismatch),
		errors.Is(err, oauthservice.ErrScopeInvalid),
		errors.Is(err, oauthservice.ErrScopeNotAllowed):
		writeError(c, http.StatusBadRequest, "invalid_request")
	default:
		writeError(c, http.StatusServiceUnavailable, "identity_unavailable")
	}
}

func writeError(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"error": code})
}
