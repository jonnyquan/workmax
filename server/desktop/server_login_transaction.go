//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

const (
	maxLoginTransactionPasswordBodyBytes = 4 << 10
	localLoginTransactionFlowHeader      = "X-WorkMax-Login-Flow"
)

// PasswordLoginCoordinator is the Sidecar-owned security boundary used by the
// local HTTP adapter. Its snapshot is intentionally non-sensitive; the
// concrete cloud_proxy implementation retains every transaction capability,
// callback binding, authorization code and OAuth token inside Go.
type PasswordLoginCoordinator interface {
	StartPassword(context.Context, string, string) (cloudproxy.LoginTransactionCoordinatorSnapshot, error)
	CompletePassword(context.Context, string, string, string) (cloudproxy.LoginTransactionCoordinatorSnapshot, error)
	Snapshot() cloudproxy.LoginTransactionCoordinatorSnapshot
	CancelFlow(string) (cloudproxy.LoginTransactionCoordinatorSnapshot, error)
	Cancel()
}

type localLoginTransactionPasswordRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type localLoginTransactionResponse struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

func (s *Server) handleLoginTransactionBegin(c *gin.Context) {
	setLocalLoginNoStoreHeaders(c)
	if s.cfg.LoginCoordinator == nil {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	flowID, err := localLoginTransactionFlowID(c.Request)
	if err != nil {
		writeLocalLoginError(c, http.StatusBadRequest, publicLocalLoginState(s.cfg.LoginCoordinator.Snapshot()), "invalid_request")
		return
	}
	s.authLifecycleMu.Lock()
	defer s.authLifecycleMu.Unlock()
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	// Keep the deferred legacy OAuth route mutually exclusive with the
	// Server-backed Login Transaction flow because both commit to one session.
	if s.cfg.OAuthFlow != nil {
		s.cfg.OAuthFlow.Cancel()
	}
	operationContext, operationCancel := s.authOperationContext(c.Request.Context())
	defer operationCancel()
	snapshot, err := s.cfg.LoginCoordinator.StartPassword(operationContext, "workagent", flowID)
	if err != nil {
		writeLocalLoginCoordinatorError(c, snapshot, err)
		return
	}
	// Close the tiny window between the coordinator's final Start fence and
	// writing 201. A disconnected request or Sidecar shutdown must not leave an
	// unreachable pending transaction installed after an ambiguous response.
	if operationContext.Err() != nil || c.Request.Context().Err() != nil || s.authClosing.Load() {
		_, _ = s.cfg.LoginCoordinator.CancelFlow(flowID)
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	writeLocalLoginSnapshot(c, http.StatusCreated, snapshot)
}

func (s *Server) handleLoginTransactionStatus(c *gin.Context) {
	setLocalLoginNoStoreHeaders(c)
	if s.cfg.LoginCoordinator == nil {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	writeLocalLoginSnapshot(c, http.StatusOK, s.cfg.LoginCoordinator.Snapshot())
}

func (s *Server) handleLoginTransactionPassword(c *gin.Context) {
	setLocalLoginNoStoreHeaders(c)
	if s.cfg.LoginCoordinator == nil {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	flowID, err := localLoginTransactionFlowID(c.Request)
	if err != nil {
		writeLocalLoginError(c, http.StatusBadRequest, publicLocalLoginState(s.cfg.LoginCoordinator.Snapshot()), "invalid_request")
		return
	}
	request, err := decodeLocalLoginPasswordRequest(c.Request)
	if err != nil {
		writeLocalLoginError(c, http.StatusBadRequest, publicLocalLoginState(s.cfg.LoginCoordinator.Snapshot()), "invalid_request")
		return
	}
	defer func() { request.Password = "" }()
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	operationContext, operationCancel := s.authOperationContext(c.Request.Context())
	defer operationCancel()
	if operationContext.Err() != nil || s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	snapshot, err := s.cfg.LoginCoordinator.CompletePassword(
		operationContext,
		flowID,
		request.Email,
		request.Password,
	)
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	if err != nil {
		writeLocalLoginCoordinatorError(c, snapshot, err)
		return
	}
	writeLocalLoginSnapshot(c, http.StatusOK, snapshot)
}

func (s *Server) handleLoginTransactionCancel(c *gin.Context) {
	setLocalLoginNoStoreHeaders(c)
	if s.cfg.LoginCoordinator == nil {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	flowID, err := localLoginTransactionFlowID(c.Request)
	if err != nil {
		writeLocalLoginError(c, http.StatusBadRequest, publicLocalLoginState(s.cfg.LoginCoordinator.Snapshot()), "invalid_request")
		return
	}
	if s.authClosing.Load() {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	snapshot, err := s.cfg.LoginCoordinator.CancelFlow(flowID)
	if err != nil {
		writeLocalLoginCoordinatorError(c, snapshot, err)
		return
	}
	writeLocalLoginSnapshot(c, http.StatusOK, snapshot)
}

func localLoginTransactionFlowID(request *http.Request) (string, error) {
	if request == nil {
		return "", errors.New("login flow header is missing")
	}
	values := request.Header.Values(localLoginTransactionFlowHeader)
	if len(values) != 1 || !cloudproxy.ValidLoginTransactionLocalFlowID(values[0]) {
		return "", errors.New("login flow header is malformed")
	}
	return values[0], nil
}

func decodeLocalLoginPasswordRequest(request *http.Request) (localLoginTransactionPasswordRequest, error) {
	var result localLoginTransactionPasswordRequest
	if request == nil || request.Body == nil {
		return result, errors.New("password request body is missing")
	}
	contentTypes := request.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		return result, errors.New("Content-Type must appear exactly once")
	}
	mediaType, params, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/json" ||
		(len(params) != 0 && (len(params) != 1 || !strings.EqualFold(params["charset"], "utf-8"))) {
		return result, errors.New("Content-Type must be canonical JSON")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxLoginTransactionPasswordBodyBytes+1))
	defer clear(body)
	if err != nil || len(body) == 0 || len(body) > maxLoginTransactionPasswordBodyBytes || !utf8.Valid(body) {
		return result, errors.New("password request body is malformed")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return result, errors.New("password request must be one JSON object")
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return result, errors.New("password request is malformed")
		}
		key, ok := token.(string)
		if !ok || (key != "email" && key != "password") {
			return result, errors.New("password request has an unknown field")
		}
		if _, duplicate := seen[key]; duplicate {
			return result, errors.New("password request repeats a field")
		}
		seen[key] = struct{}{}
		switch key {
		case "email":
			if err := decoder.Decode(&result.Email); err != nil {
				return localLoginTransactionPasswordRequest{}, errors.New("email is malformed")
			}
		case "password":
			if err := decoder.Decode(&result.Password); err != nil {
				return localLoginTransactionPasswordRequest{}, errors.New("password is malformed")
			}
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 2 {
		return localLoginTransactionPasswordRequest{}, errors.New("password request fields are incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return localLoginTransactionPasswordRequest{}, errors.New("password request has trailing data")
	}
	return result, nil
}

func writeLocalLoginSnapshot(
	c *gin.Context,
	status int,
	snapshot cloudproxy.LoginTransactionCoordinatorSnapshot,
) {
	state, ok := mapLocalLoginState(snapshot.State)
	if !ok {
		writeLocalLoginError(c, http.StatusServiceUnavailable, "idle", "unavailable")
		return
	}
	c.JSON(status, localLoginTransactionResponse{State: state})
}

func writeLocalLoginCoordinatorError(
	c *gin.Context,
	snapshot cloudproxy.LoginTransactionCoordinatorSnapshot,
	err error,
) {
	state := publicLocalLoginState(snapshot)
	switch {
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorInvalidInput):
		writeLocalLoginError(c, http.StatusBadRequest, state, "invalid_request")
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorInvalidCredentials):
		writeLocalLoginError(c, http.StatusUnauthorized, "awaiting_password", "invalid_credentials")
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorBusy):
		writeLocalLoginError(c, http.StatusConflict, state, "busy")
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorIdle):
		writeLocalLoginError(c, http.StatusConflict, "idle", "invalid_request")
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorTerminal):
		writeLocalLoginError(c, http.StatusGone, "idle", "expired")
	case errors.Is(err, cloudproxy.ErrLoginTransactionCoordinatorCanceled):
		writeLocalLoginError(c, http.StatusConflict, "idle", "canceled")
	default:
		writeLocalLoginError(c, http.StatusServiceUnavailable, state, "unavailable")
	}
}

func writeLocalLoginError(c *gin.Context, status int, state string, code string) {
	if !validPublicLocalLoginState(state) {
		state = "idle"
	}
	c.JSON(status, localLoginTransactionResponse{State: state, Error: code})
}

func publicLocalLoginState(snapshot cloudproxy.LoginTransactionCoordinatorSnapshot) string {
	state, ok := mapLocalLoginState(snapshot.State)
	if !ok {
		return "idle"
	}
	return state
}

func mapLocalLoginState(state cloudproxy.LoginTransactionCoordinatorState) (string, bool) {
	switch state {
	case cloudproxy.LoginTransactionCoordinatorStateIdle:
		return "idle", true
	case cloudproxy.LoginTransactionCoordinatorStateStarting,
		cloudproxy.LoginTransactionCoordinatorStateCompleting:
		return "submitting", true
	case cloudproxy.LoginTransactionCoordinatorStatePending:
		return "awaiting_password", true
	case cloudproxy.LoginTransactionCoordinatorStateComplete:
		return "authenticated", true
	default:
		return "", false
	}
}

func validPublicLocalLoginState(state string) bool {
	switch state {
	case "idle", "awaiting_password", "submitting", "authenticated":
		return true
	default:
		return false
	}
}

func setLocalLoginNoStoreHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
}
