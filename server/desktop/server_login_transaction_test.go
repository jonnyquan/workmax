//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	cloudproxy "server/desktop/cloud_proxy"
)

var (
	localLoginTestFlowA = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	localLoginTestFlowB = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
)

type fakePasswordLoginCoordinator struct {
	mu                 sync.Mutex
	snapshot           cloudproxy.LoginTransactionCoordinatorSnapshot
	startErr           error
	completeErr        error
	startHook          func()
	startCalls         int
	completeCalls      int
	cancelCalls        int
	cancelFlowCalls    int
	startFlowID        string
	completeFlowID     string
	cancelFlowID       string
	activeFlowID       string
	enforceFlowBinding bool
	email              string
	password           string
}

func newFakePasswordLoginCoordinator() *fakePasswordLoginCoordinator {
	return &fakePasswordLoginCoordinator{
		snapshot: cloudproxy.LoginTransactionCoordinatorSnapshot{
			State: cloudproxy.LoginTransactionCoordinatorStateIdle,
		},
	}
}

func (f *fakePasswordLoginCoordinator) StartPassword(
	_ context.Context,
	_ string,
	flowID string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCalls++
	f.startFlowID = flowID
	if f.startErr == nil {
		f.snapshot.State = cloudproxy.LoginTransactionCoordinatorStatePending
		f.activeFlowID = flowID
	}
	if f.startHook != nil {
		f.startHook()
	}
	return f.snapshot, f.startErr
}

func (f *fakePasswordLoginCoordinator) CompletePassword(
	_ context.Context,
	flowID string,
	email string,
	password string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeCalls++
	f.completeFlowID = flowID
	f.email = email
	f.password = password
	if f.enforceFlowBinding && f.activeFlowID != flowID {
		return f.snapshot, cloudproxy.ErrLoginTransactionCoordinatorInvalidInput
	}
	if f.completeErr == nil {
		f.snapshot.State = cloudproxy.LoginTransactionCoordinatorStateComplete
		f.activeFlowID = ""
	}
	return f.snapshot, f.completeErr
}

func (f *fakePasswordLoginCoordinator) CancelFlow(
	flowID string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelFlowCalls++
	f.cancelFlowID = flowID
	if f.enforceFlowBinding && f.activeFlowID != "" && f.activeFlowID != flowID {
		return f.snapshot, cloudproxy.ErrLoginTransactionCoordinatorInvalidInput
	}
	f.activeFlowID = ""
	f.snapshot = cloudproxy.LoginTransactionCoordinatorSnapshot{
		State: cloudproxy.LoginTransactionCoordinatorStateIdle,
	}
	return f.snapshot, nil
}

func (f *fakePasswordLoginCoordinator) Snapshot() cloudproxy.LoginTransactionCoordinatorSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakePasswordLoginCoordinator) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	f.activeFlowID = ""
	f.snapshot = cloudproxy.LoginTransactionCoordinatorSnapshot{
		State: cloudproxy.LoginTransactionCoordinatorStateIdle,
	}
}

type blockingPasswordLoginCoordinator struct {
	mu               sync.Mutex
	state            cloudproxy.LoginTransactionCoordinatorState
	activeFlowID     string
	completeCancel   context.CancelFunc
	completeEntered  chan struct{}
	completeCanceled chan struct{}
	enterOnce        sync.Once
	cancelOnce       sync.Once
}

func newBlockingPasswordLoginCoordinator(flowID string) *blockingPasswordLoginCoordinator {
	return &blockingPasswordLoginCoordinator{
		state:            cloudproxy.LoginTransactionCoordinatorStatePending,
		activeFlowID:     flowID,
		completeEntered:  make(chan struct{}),
		completeCanceled: make(chan struct{}),
	}
}

func (c *blockingPasswordLoginCoordinator) StartPassword(
	context.Context,
	string,
	string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	return c.Snapshot(), cloudproxy.ErrLoginTransactionCoordinatorBusy
}

func (c *blockingPasswordLoginCoordinator) CompletePassword(
	ctx context.Context,
	flowID string,
	_ string,
	_ string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	c.mu.Lock()
	if c.activeFlowID != flowID || c.state != cloudproxy.LoginTransactionCoordinatorStatePending {
		snapshot := c.snapshotLocked()
		c.mu.Unlock()
		return snapshot, cloudproxy.ErrLoginTransactionCoordinatorInvalidInput
	}
	operationContext, operationCancel := context.WithCancel(ctx)
	c.completeCancel = operationCancel
	c.state = cloudproxy.LoginTransactionCoordinatorStateCompleting
	c.mu.Unlock()
	c.enterOnce.Do(func() { close(c.completeEntered) })

	<-operationContext.Done()
	c.cancelOnce.Do(func() { close(c.completeCanceled) })
	c.mu.Lock()
	if c.activeFlowID == flowID {
		c.activeFlowID = ""
		c.state = cloudproxy.LoginTransactionCoordinatorStateIdle
	}
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	return snapshot, cloudproxy.ErrLoginTransactionCoordinatorCanceled
}

func (c *blockingPasswordLoginCoordinator) Snapshot() cloudproxy.LoginTransactionCoordinatorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *blockingPasswordLoginCoordinator) snapshotLocked() cloudproxy.LoginTransactionCoordinatorSnapshot {
	return cloudproxy.LoginTransactionCoordinatorSnapshot{State: c.state}
}

func (c *blockingPasswordLoginCoordinator) CancelFlow(
	flowID string,
) (cloudproxy.LoginTransactionCoordinatorSnapshot, error) {
	c.mu.Lock()
	if c.activeFlowID != "" && c.activeFlowID != flowID {
		snapshot := c.snapshotLocked()
		c.mu.Unlock()
		return snapshot, cloudproxy.ErrLoginTransactionCoordinatorInvalidInput
	}
	cancel := c.completeCancel
	c.completeCancel = nil
	c.activeFlowID = ""
	c.state = cloudproxy.LoginTransactionCoordinatorStateIdle
	snapshot := c.snapshotLocked()
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return snapshot, nil
}

func (c *blockingPasswordLoginCoordinator) Cancel() {
	c.mu.Lock()
	cancel := c.completeCancel
	c.completeCancel = nil
	c.activeFlowID = ""
	c.state = cloudproxy.LoginTransactionCoordinatorStateIdle
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func TestLocalLoginTransactionRoutesExposeOnlyPublicState(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()

	assertLocalLoginResponse(t, requestLocalLogin(t, baseURL, token, http.MethodGet, "/auth/login-transaction", "", ""), http.StatusOK, "idle", "")
	assertLocalLoginResponse(t, requestLocalLogin(t, baseURL, token, http.MethodPost, "/auth/login-transaction", "", ""), http.StatusCreated, "awaiting_password", "")
	assertLocalLoginResponse(t, requestLocalLogin(
		t,
		baseURL,
		token,
		http.MethodPost,
		"/auth/login-transaction/password",
		`{"email":"person@example.com","password":"correct-password"}`,
		"application/json",
	), http.StatusOK, "authenticated", "")
	assertLocalLoginResponse(t, requestLocalLogin(t, baseURL, token, http.MethodDelete, "/auth/login-transaction", "", ""), http.StatusOK, "idle", "")

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.startCalls != 1 || coordinator.completeCalls != 1 || coordinator.cancelFlowCalls != 1 ||
		coordinator.startFlowID != localLoginTestFlowA || coordinator.completeFlowID != localLoginTestFlowA ||
		coordinator.cancelFlowID != localLoginTestFlowA ||
		coordinator.email != "person@example.com" || coordinator.password != "correct-password" {
		t.Fatalf("coordinator calls=%d/%d/%d flows=%q/%q/%q credentials=%q/%q", coordinator.startCalls, coordinator.completeCalls, coordinator.cancelFlowCalls, coordinator.startFlowID, coordinator.completeFlowID, coordinator.cancelFlowID, coordinator.email, coordinator.password)
	}
}

func TestLocalLoginTransactionPasswordRejectsNonCanonicalJSONBeforeCoordinator(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	coordinator.snapshot.State = cloudproxy.LoginTransactionCoordinatorStatePending
	baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()

	tests := []struct {
		name        string
		body        []byte
		contentType string
		wantStatus  int
	}{
		{name: "missing body", contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "missing Content-Type", body: []byte(`{"email":"person@example.com","password":"x"}`), wantStatus: http.StatusUnsupportedMediaType},
		{name: "wrong Content-Type", body: []byte(`{"email":"person@example.com","password":"x"}`), contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "unknown parameter", body: []byte(`{"email":"person@example.com","password":"x"}`), contentType: "application/json; profile=internal", wantStatus: http.StatusBadRequest},
		{name: "array", body: []byte(`[]`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "missing password", body: []byte(`{"email":"person@example.com"}`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: []byte(`{"email":"person@example.com","password":"x","id":"not-allowed"}`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "case alias", body: []byte(`{"Email":"person@example.com","password":"x"}`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "duplicate", body: []byte(`{"email":"person@example.com","email":"other@example.com","password":"x"}`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "trailing", body: []byte(`{"email":"person@example.com","password":"x"}{}`), contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "invalid UTF-8", body: []byte{'{', '"', 'e', 'm', 'a', 'i', 'l', '"', ':', '"', 0xff, '"', '}'}, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "over cap", body: bytes.Repeat([]byte{'x'}, maxLoginTransactionPasswordBodyBytes+1), contentType: "application/json", wantStatus: http.StatusRequestEntityTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestLocalLoginBytes(
				t,
				baseURL,
				token,
				http.MethodPost,
				"/auth/login-transaction/password",
				test.body,
				test.contentType,
			)
			defer response.Body.Close()
			if response.StatusCode != test.wantStatus {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("status=%d body=%q, want %d", response.StatusCode, raw, test.wantStatus)
			}
		})
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.completeCalls != 0 {
		t.Fatalf("invalid requests reached coordinator %d times", coordinator.completeCalls)
	}
}

func TestLocalLoginTransactionMutationsRequireOneCanonicalFlowHeader(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	coordinator.snapshot.State = cloudproxy.LoginTransactionCoordinatorStatePending
	baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()

	mutations := []struct {
		name        string
		method      string
		path        string
		body        []byte
		contentType string
	}{
		{name: "begin", method: http.MethodPost, path: "/auth/login-transaction"},
		{
			name:        "password",
			method:      http.MethodPost,
			path:        "/auth/login-transaction/password",
			body:        []byte(`{"email":"person@example.com","password":"must-not-run"}`),
			contentType: "application/json",
		},
		{name: "cancel", method: http.MethodDelete, path: "/auth/login-transaction"},
	}
	headerCases := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "duplicate", values: []string{localLoginTestFlowA, localLoginTestFlowB}},
		{name: "malformed", values: []string{"not-a-canonical-local-flow"}},
	}
	for _, mutation := range mutations {
		for _, headerCase := range headerCases {
			t.Run(mutation.name+"/"+headerCase.name, func(t *testing.T) {
				response := requestLocalLoginBytesWithFlowIDs(
					t,
					baseURL,
					token,
					mutation.method,
					mutation.path,
					mutation.body,
					mutation.contentType,
					headerCase.values,
				)
				assertLocalLoginResponse(
					t,
					response,
					http.StatusBadRequest,
					"awaiting_password",
					"invalid_request",
				)
			})
		}
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.startCalls != 0 || coordinator.completeCalls != 0 || coordinator.cancelFlowCalls != 0 {
		t.Fatalf("invalid flow headers reached coordinator: start=%d complete=%d cancel=%d", coordinator.startCalls, coordinator.completeCalls, coordinator.cancelFlowCalls)
	}
}

func TestLocalLoginTransactionStaleFlowCannotMutateReplacement(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	coordinator.enforceFlowBinding = true
	baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()

	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t, baseURL, token, http.MethodPost, "/auth/login-transaction", "", "", localLoginTestFlowA,
	), http.StatusCreated, "awaiting_password", "")
	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t, baseURL, token, http.MethodDelete, "/auth/login-transaction", "", "", localLoginTestFlowA,
	), http.StatusOK, "idle", "")
	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t, baseURL, token, http.MethodPost, "/auth/login-transaction", "", "", localLoginTestFlowB,
	), http.StatusCreated, "awaiting_password", "")

	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t,
		baseURL,
		token,
		http.MethodPost,
		"/auth/login-transaction/password",
		`{"email":"flow-a@example.com","password":"flow-a-password"}`,
		"application/json",
		localLoginTestFlowA,
	), http.StatusBadRequest, "awaiting_password", "invalid_request")
	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t, baseURL, token, http.MethodDelete, "/auth/login-transaction", "", "", localLoginTestFlowA,
	), http.StatusBadRequest, "awaiting_password", "invalid_request")
	assertLocalLoginResponse(t, requestLocalLoginWithFlowID(
		t,
		baseURL,
		token,
		http.MethodPost,
		"/auth/login-transaction/password",
		`{"email":"flow-b@example.com","password":"flow-b-password"}`,
		"application/json",
		localLoginTestFlowB,
	), http.StatusOK, "authenticated", "")

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.activeFlowID != "" || coordinator.completeFlowID != localLoginTestFlowB ||
		coordinator.cancelFlowID != localLoginTestFlowA {
		t.Fatalf("replacement binding drifted: active=%q complete=%q cancel=%q", coordinator.activeFlowID, coordinator.completeFlowID, coordinator.cancelFlowID)
	}
}

func TestLocalLoginTransactionPreciseCancelInterruptsBlockedComplete(t *testing.T) {
	coordinator := newBlockingPasswordLoginCoordinator(localLoginTestFlowA)
	baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()

	passwordResponse := make(chan *http.Response, 1)
	go func() {
		passwordResponse <- requestLocalLoginWithFlowID(
			t,
			baseURL,
			token,
			http.MethodPost,
			"/auth/login-transaction/password",
			`{"email":"person@example.com","password":"blocked-password"}`,
			"application/json",
			localLoginTestFlowA,
		)
	}()
	select {
	case <-coordinator.completeEntered:
	case <-time.After(time.Second):
		t.Fatal("password completion did not block")
	}

	cancelResponse := make(chan *http.Response, 1)
	go func() {
		cancelResponse <- requestLocalLoginWithFlowID(
			t,
			baseURL,
			token,
			http.MethodDelete,
			"/auth/login-transaction",
			"",
			"",
			localLoginTestFlowA,
		)
	}()
	select {
	case response := <-cancelResponse:
		assertLocalLoginResponse(t, response, http.StatusOK, "idle", "")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("precise cancel waited behind blocked password completion")
	}
	select {
	case <-coordinator.completeCanceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("precise cancel did not cancel the completion context")
	}
	select {
	case response := <-passwordResponse:
		assertLocalLoginResponse(t, response, http.StatusConflict, "idle", "canceled")
	case <-time.After(time.Second):
		t.Fatal("canceled password request did not return")
	}
}

func TestLocalLoginTransactionShutdownRejectsMutationsBeforeCoordinator(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	baseURL, token, server, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()
	server.authClosing.Store(true)

	for _, request := range []struct {
		method      string
		path        string
		body        string
		contentType string
	}{
		{method: http.MethodPost, path: "/auth/login-transaction"},
		{
			method:      http.MethodPost,
			path:        "/auth/login-transaction/password",
			body:        `{"email":"person@example.com","password":"must-not-process"}`,
			contentType: "application/json",
		},
		{method: http.MethodDelete, path: "/auth/login-transaction"},
	} {
		assertLocalLoginResponse(t, requestLocalLogin(
			t, baseURL, token, request.method, request.path, request.body, request.contentType,
		), http.StatusServiceUnavailable, "idle", "unavailable")
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.startCalls != 0 || coordinator.completeCalls != 0 || coordinator.cancelFlowCalls != 0 {
		t.Fatalf("shutdown admitted mutation: start=%d complete=%d cancel=%d", coordinator.startCalls, coordinator.completeCalls, coordinator.cancelFlowCalls)
	}
}

func TestLocalLoginTransactionBeginPostFenceCancelsInstalledFlow(t *testing.T) {
	coordinator := newFakePasswordLoginCoordinator()
	baseURL, token, server, shutdown := bootLocalLoginTransactionServer(t, coordinator)
	defer shutdown()
	coordinator.startHook = func() { server.authClosing.Store(true) }

	assertLocalLoginResponse(t, requestLocalLogin(
		t, baseURL, token, http.MethodPost, "/auth/login-transaction", "", "",
	), http.StatusServiceUnavailable, "idle", "unavailable")

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.startCalls != 1 || coordinator.cancelFlowCalls != 1 ||
		coordinator.cancelFlowID != localLoginTestFlowA || coordinator.activeFlowID != "" {
		t.Fatalf("post-Start fence failed: start=%d cancel=%d flow=%q active=%q", coordinator.startCalls, coordinator.cancelFlowCalls, coordinator.cancelFlowID, coordinator.activeFlowID)
	}
}

func TestLocalLoginTransactionMapsClosedCoordinatorErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		state      cloudproxy.LoginTransactionCoordinatorState
		wantStatus int
		wantState  string
		wantError  string
	}{
		{name: "credentials", err: cloudproxy.ErrLoginTransactionCoordinatorInvalidCredentials, state: cloudproxy.LoginTransactionCoordinatorStatePending, wantStatus: http.StatusUnauthorized, wantState: "awaiting_password", wantError: "invalid_credentials"},
		{name: "busy", err: cloudproxy.ErrLoginTransactionCoordinatorBusy, state: cloudproxy.LoginTransactionCoordinatorStateCompleting, wantStatus: http.StatusConflict, wantState: "submitting", wantError: "busy"},
		{name: "expired", err: cloudproxy.ErrLoginTransactionCoordinatorTerminal, state: cloudproxy.LoginTransactionCoordinatorStateIdle, wantStatus: http.StatusGone, wantState: "idle", wantError: "expired"},
		{name: "unavailable", err: cloudproxy.ErrLoginTransactionCoordinatorUnavailable, state: cloudproxy.LoginTransactionCoordinatorStateIdle, wantStatus: http.StatusServiceUnavailable, wantState: "idle", wantError: "unavailable"},
		{name: "raw internal", err: errors.New("database password and upstream body"), state: cloudproxy.LoginTransactionCoordinatorStateIdle, wantStatus: http.StatusServiceUnavailable, wantState: "idle", wantError: "unavailable"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			coordinator := newFakePasswordLoginCoordinator()
			coordinator.snapshot.State = test.state
			coordinator.completeErr = test.err
			baseURL, token, _, shutdown := bootLocalLoginTransactionServer(t, coordinator)
			defer shutdown()
			response := requestLocalLogin(
				t,
				baseURL,
				token,
				http.MethodPost,
				"/auth/login-transaction/password",
				`{"email":"person@example.com","password":"correct-password"}`,
				"application/json",
			)
			assertLocalLoginResponse(t, response, test.wantStatus, test.wantState, test.wantError)
		})
	}
}

func bootLocalLoginTransactionServer(
	t *testing.T,
	coordinator PasswordLoginCoordinator,
) (string, string, *Server, func()) {
	t.Helper()
	const token = "local-login-transaction-token"
	server, err := NewServer(ServerConfig{
		SidecarVersion:   "local-login-test",
		LocalToken:       token,
		DB:               openServerTestDB(t),
		LoginCoordinator: coordinator,
	})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		})
	}
	t.Cleanup(shutdown)
	return "http://" + server.listener.Addr().String(), token, server, shutdown
}

func requestLocalLogin(
	t *testing.T,
	baseURL string,
	token string,
	method string,
	path string,
	body string,
	contentType string,
) *http.Response {
	t.Helper()
	return requestLocalLoginBytes(t, baseURL, token, method, path, []byte(body), contentType)
}

func requestLocalLoginWithFlowID(
	t *testing.T,
	baseURL string,
	token string,
	method string,
	path string,
	body string,
	contentType string,
	flowID string,
) *http.Response {
	t.Helper()
	return requestLocalLoginBytesWithFlowIDs(
		t,
		baseURL,
		token,
		method,
		path,
		[]byte(body),
		contentType,
		[]string{flowID},
	)
}

func requestLocalLoginBytes(
	t *testing.T,
	baseURL string,
	token string,
	method string,
	path string,
	body []byte,
	contentType string,
) *http.Response {
	t.Helper()
	return requestLocalLoginBytesWithFlowIDs(
		t,
		baseURL,
		token,
		method,
		path,
		body,
		contentType,
		[]string{localLoginTestFlowA},
	)
}

func requestLocalLoginBytesWithFlowIDs(
	t *testing.T,
	baseURL string,
	token string,
	method string,
	path string,
	body []byte,
	contentType string,
	flowIDs []string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Local-Token", token)
	for _, flowID := range flowIDs {
		request.Header.Add(localLoginTransactionFlowHeader, flowID)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertLocalLoginResponse(
	t *testing.T,
	response *http.Response,
	wantStatus int,
	wantState string,
	wantError string,
) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if response.StatusCode != wantStatus || payload["state"] != wantState || payload["error"] != wantError {
		t.Fatalf("response=%d %v, want status=%d state=%q error=%q", response.StatusCode, payload, wantStatus, wantState, wantError)
	}
	wantKeys := 1
	if wantError != "" {
		wantKeys = 2
	}
	if len(payload) != wantKeys || strings.Contains(string(body), "correct-password") ||
		strings.Contains(string(body), "person@example.com") ||
		strings.Contains(string(body), localLoginTestFlowA) ||
		strings.Contains(string(body), localLoginTestFlowB) {
		t.Fatalf("response widened or leaked credentials: %q", body)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing login security headers: %v", response.Header)
	}
}
