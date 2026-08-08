//go:build desktop

package cloud_proxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var coordinatorTestNow = time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)

var (
	coordinatorTestFlowA = coordinatorLocalFlowID(0x11)
	coordinatorTestFlowB = coordinatorLocalFlowID(0x22)
)

func coordinatorLocalFlowID(fill byte) string {
	raw := make([]byte, loginTransactionCoordinatorFlowIDBytes)
	for index := range raw {
		raw[index] = fill
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

type coordinatorEntropyReader byte

func (r coordinatorEntropyReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(r)
	}
	return len(buffer), nil
}

type coordinatorTestCloud struct {
	t      *testing.T
	server *httptest.Server

	transactionID     string
	transactionSecret string
	exchangeToken     string
	authorizationCode string
	expiresAt         time.Time

	createHook   func(http.ResponseWriter, *http.Request)
	passwordHook func(http.ResponseWriter, *http.Request)
	exchangeHook func(http.ResponseWriter, *http.Request)
	tokenHook    func(http.ResponseWriter, *http.Request)

	mu               sync.Mutex
	sequence         []string
	headers          map[string][]http.Header
	createRequests   []loginTransactionCreateRequest
	passwordRequests []loginTransactionPasswordRequest
	tokenForms       []url.Values
}

func newCoordinatorTestCloud(t *testing.T) *coordinatorTestCloud {
	t.Helper()
	cloud := &coordinatorTestCloud{
		t:                 t,
		transactionID:     testLoginToken('i', loginTransactionIDBytes),
		transactionSecret: testLoginToken('t', loginTransactionCapabilityBytes),
		exchangeToken:     testLoginToken('x', loginTransactionCapabilityBytes),
		authorizationCode: testLoginToken('c', loginTransactionAuthCodeBytes),
		expiresAt:         coordinatorTestNow.Add(10 * time.Minute),
		headers:           make(map[string][]http.Header),
	}
	cloud.server = httptest.NewServer(http.HandlerFunc(cloud.handle))
	t.Cleanup(cloud.server.Close)
	return cloud
}

func (c *coordinatorTestCloud) handle(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.URL.Path == CloudRouteLoginTransactionCreate:
		c.record("create", request)
		var payload loginTransactionCreateRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			c.t.Errorf("decode create request: %v", err)
		}
		c.mu.Lock()
		c.createRequests = append(c.createRequests, payload)
		c.mu.Unlock()
		if c.createHook != nil {
			c.createHook(writer, request)
			return
		}
		c.writeCreate(writer)

	case request.URL.Path == expandLoginTransactionRoute(CloudRouteLoginTransactionPassword, c.transactionID):
		c.record("password", request)
		var payload loginTransactionPasswordRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			c.t.Errorf("decode password request: %v", err)
		}
		c.mu.Lock()
		c.passwordRequests = append(c.passwordRequests, payload)
		c.mu.Unlock()
		if c.passwordHook != nil {
			c.passwordHook(writer, request)
			return
		}
		c.writePassword(writer)

	case request.URL.Path == expandLoginTransactionRoute(CloudRouteLoginTransactionExchange, c.transactionID):
		c.record("exchange", request)
		if c.exchangeHook != nil {
			c.exchangeHook(writer, request)
			return
		}
		c.writeExchange(writer)

	case request.URL.Path == CloudRouteOAuthToken:
		c.record("token", request)
		if err := request.ParseForm(); err != nil {
			c.t.Errorf("parse token form: %v", err)
		}
		c.mu.Lock()
		c.tokenForms = append(c.tokenForms, cloneCoordinatorURLValues(request.Form))
		c.mu.Unlock()
		if c.tokenHook != nil {
			c.tokenHook(writer, request)
			return
		}
		c.writeToken(writer)

	default:
		c.t.Errorf("unexpected coordinator request: %s %s", request.Method, request.URL.RequestURI())
		http.Error(writer, "unexpected", http.StatusNotFound)
	}
}

func (c *coordinatorTestCloud) record(stage string, request *http.Request) {
	c.mu.Lock()
	c.sequence = append(c.sequence, stage)
	c.headers[stage] = append(c.headers[stage], request.Header.Clone())
	c.mu.Unlock()
}

func (c *coordinatorTestCloud) writeCreate(writer http.ResponseWriter) {
	coordinatorWriteJSON(c.t, writer, http.StatusCreated, map[string]any{
		"transaction_id":     c.transactionID,
		"transaction_secret": c.transactionSecret,
		"expires_at":         c.expiresAt,
		"methods":            []string{"password"},
		"future_field":       "accepted",
	})
}

func (c *coordinatorTestCloud) writePassword(writer http.ResponseWriter) {
	coordinatorWriteJSON(c.t, writer, http.StatusOK, map[string]any{
		"transaction_id": c.transactionID,
		"exchange_token": c.exchangeToken,
		"expires_at":     c.expiresAt,
	})
}

func (c *coordinatorTestCloud) writeExchange(writer http.ResponseWriter) {
	create := c.lastCreate()
	writer.Header().Set("Location", create.RedirectURI+"?code="+c.authorizationCode+"&state="+create.State)
	writer.WriteHeader(http.StatusSeeOther)
}

func (c *coordinatorTestCloud) writeToken(writer http.ResponseWriter) {
	coordinatorWriteJSON(c.t, writer, http.StatusOK, map[string]any{
		"access_token":       "coordinator-access-secret",
		"token_type":         "Bearer",
		"expires_in":         900,
		"refresh_token":      "coordinator-refresh-secret",
		"refresh_expires_in": 7_776_000,
		"scope":              "workagent",
	})
}

func (c *coordinatorTestCloud) lastCreate() loginTransactionCreateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.createRequests) == 0 {
		return loginTransactionCreateRequest{}
	}
	return c.createRequests[len(c.createRequests)-1]
}

func (c *coordinatorTestCloud) lastTokenForm() url.Values {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.tokenForms) == 0 {
		return nil
	}
	return cloneCoordinatorURLValues(c.tokenForms[len(c.tokenForms)-1])
}

func (c *coordinatorTestCloud) requestCount(stage string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.headers[stage])
}

func (c *coordinatorTestCloud) requestHeaders(stage string) []http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]http.Header, len(c.headers[stage]))
	for index, header := range c.headers[stage] {
		result[index] = header.Clone()
	}
	return result
}

func (c *coordinatorTestCloud) requestSequence() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sequence...)
}

func cloneCoordinatorURLValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}
	return result
}

func coordinatorWriteJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode coordinator response: %v", err)
	}
}

func newCoordinatorForTest(
	cloud *coordinatorTestCloud,
	store *TokenStore,
) (*LoginTransactionCoordinator, *Client) {
	client := NewClient(cloud.server.URL)
	client.HTTPClient = cloud.server.Client()
	coordinator := NewLoginTransactionCoordinator(client, store, testLoginDeviceID)
	coordinator.random = coordinatorEntropyReader(0x5a)
	coordinator.now = func() time.Time { return coordinatorTestNow }
	coordinator.deviceInfo = `{"os":"test","app_version":"test"}`
	return coordinator, client
}

func TestLoginTransactionCoordinator_HappyPathFreezesBindingsAndReturnsOnlySafeState(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	keychain := newFakeKeychain()
	store := NewTokenStore(keychain)
	coordinator, client := newCoordinatorForTest(cloud, store)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	serverURL, err := url.Parse(cloud.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(serverURL, []*http.Cookie{{Name: "shared-session", Value: "must-not-cross"}})
	sharedRedirectError := errors.New("shared redirect callback")
	var sharedRedirects atomic.Int32
	shared := cloud.server.Client()
	shared.Jar = jar
	shared.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		sharedRedirects.Add(1)
		return sharedRedirectError
	}
	client.HTTPClient = shared

	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("initial snapshot = %+v", snapshot)
	}
	started, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA)
	if err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	if started.State != LoginTransactionCoordinatorStatePending ||
		!started.ExpiresAt.Equal(cloud.expiresAt) || !reflect.DeepEqual(started.Methods, []string{"password"}) {
		t.Fatalf("start snapshot = %+v", started)
	}

	coordinator.mu.Lock()
	pending := coordinator.pending
	redirect := pending.redirect
	state := pending.state
	verifier := pending.pkce.Verifier
	challenge := pending.pkce.Challenge
	loopback := pending.loopback
	coordinator.mu.Unlock()
	assertCoordinatorLoopbackOpen(t, redirect)
	assertCoordinatorPublicValueRedacted(t, started,
		cloud.transactionID, cloud.transactionSecret, cloud.exchangeToken,
		cloud.authorizationCode, redirect, state, verifier, challenge,
		coordinatorTestFlowA,
		"coordinator-access-secret", "coordinator-refresh-secret")

	// Snapshot returns a defensive methods copy.
	started.Methods[0] = "mutated"
	if got := coordinator.Snapshot().Methods; !reflect.DeepEqual(got, []string{"password"}) {
		t.Fatalf("snapshot methods were mutable: %v", got)
	}

	create := cloud.lastCreate()
	if create.ClientID != "workmax-desktop" || create.DeviceID != testLoginDeviceID ||
		create.RedirectURI != redirect || create.State != state ||
		create.CodeChallenge != challenge || create.CodeChallengeMethod != "S256" || create.Scope != "workagent" {
		t.Fatalf("create binding = %+v", create)
	}

	// Mutating constructor inputs after Start cannot drift the frozen pending
	// transaction or its token exchange.
	client.ClientID = "mutated-client"
	client.BaseURL = "https://mutated.invalid"
	coordinator.deviceID = "ffffffffffffffffffffffffffffffff"

	completed, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "correct-password")
	if err != nil {
		t.Fatalf("CompletePassword: %v", err)
	}
	if completed.State != LoginTransactionCoordinatorStateComplete || !completed.ExpiresAt.IsZero() || len(completed.Methods) != 0 {
		t.Fatalf("complete result = %+v", completed)
	}
	assertCoordinatorPublicValueRedacted(t, completed,
		cloud.transactionID, cloud.transactionSecret, cloud.exchangeToken,
		cloud.authorizationCode, redirect, state, verifier, challenge,
		coordinatorTestFlowA,
		"coordinator-access-secret", "coordinator-refresh-secret")

	if got := cloud.requestSequence(); !reflect.DeepEqual(got, []string{"create", "password", "exchange", "token"}) {
		t.Fatalf("request sequence = %v", got)
	}
	form := cloud.lastTokenForm()
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != cloud.authorizationCode ||
		form.Get("redirect_uri") != redirect || form.Get("client_id") != "workmax-desktop" ||
		form.Get("code_verifier") != verifier || form.Get("device_id") != testLoginDeviceID ||
		form.Get("device_info") != `{"os":"test","app_version":"test"}` {
		t.Fatalf("token form = %#v", form)
	}
	digest := sha256.Sum256([]byte(form.Get("code_verifier")))
	if got := base64.RawURLEncoding.EncodeToString(digest[:]); got != create.CodeChallenge {
		t.Fatalf("token verifier challenge = %q, create challenge = %q", got, create.CodeChallenge)
	}

	stored, err := store.Get()
	if err != nil {
		t.Fatalf("TokenStore.Get: %v", err)
	}
	if stored.AccessToken != "coordinator-access-secret" || stored.RefreshToken != "coordinator-refresh-secret" ||
		stored.Scope != "workagent" {
		t.Fatalf("stored session = %+v", stored)
	}
	for _, stage := range []string{"create", "password", "exchange", "token"} {
		for _, header := range cloud.requestHeaders(stage) {
			if header.Get("Cookie") != "" {
				t.Fatalf("%s inherited shared Cookie Jar: %q", stage, header.Get("Cookie"))
			}
		}
	}
	if sharedRedirects.Load() != 0 {
		t.Fatalf("shared CheckRedirect called %d time(s)", sharedRedirects.Load())
	}
	if client.HTTPClient != shared || shared.Jar != jar {
		t.Fatal("coordinator mutated shared HTTP client")
	}
	loopback.resultMu.Lock()
	callbackResult := loopback.result
	loopback.resultMu.Unlock()
	if callbackResult != nil {
		t.Fatalf("exchange 303 was followed to loopback: %+v", callbackResult)
	}
	assertCoordinatorLoopbackClosed(t, redirect)
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("post-success snapshot = %+v", snapshot)
	}

	countsBeforeReplay := cloud.requestSequence()
	_, err = coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "correct-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorIdle) {
		t.Fatalf("replay error = %v, want idle", err)
	}
	if got := cloud.requestSequence(); !reflect.DeepEqual(got, countsBeforeReplay) {
		t.Fatalf("replay reached network: before=%v after=%v", countsBeforeReplay, got)
	}
}

func TestLoginTransactionCoordinator_InvalidCredentialsRequireExplicitRetry(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	cloud.passwordHook = func(writer http.ResponseWriter, request *http.Request) {
		if cloud.requestCount("password") == 1 {
			coordinatorWriteJSON(t, writer, http.StatusUnauthorized, map[string]any{
				"error":  "invalid_credentials",
				"detail": "RAW_INVALID_CREDENTIAL_DETAIL",
			})
			return
		}
		cloud.writePassword(writer)
	}
	store := NewTokenStore(newFakeKeychain())
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}

	pending, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "wrong-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorInvalidCredentials) {
		t.Fatalf("first CompletePassword error = %v", err)
	}
	if pending.State != LoginTransactionCoordinatorStatePending || cloud.requestCount("password") != 1 ||
		cloud.requestCount("exchange") != 0 || cloud.requestCount("token") != 0 {
		t.Fatalf("automatic retry or stage advance: snapshot=%+v sequence=%v", pending, cloud.requestSequence())
	}
	assertCoordinatorErrorRedacted(t, err,
		"person@example.com", "wrong-password", cloud.transactionSecret,
		cloud.exchangeToken, "RAW_INVALID_CREDENTIAL_DETAIL")

	completed, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "correct-password")
	if err != nil || completed.State != LoginTransactionCoordinatorStateComplete {
		t.Fatalf("explicit retry = %+v, err=%v", completed, err)
	}
	if cloud.requestCount("password") != 2 || cloud.requestCount("exchange") != 1 || cloud.requestCount("token") != 1 {
		t.Fatalf("explicit retry counts: %v", cloud.requestSequence())
	}
}

func TestLoginTransactionCoordinator_PasswordResponseLossFailsClosedAndNeverRetries(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	cloud.passwordHook = func(writer http.ResponseWriter, _ *http.Request) {
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("httptest writer does not support hijacking")
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack password response: %v", err)
			return
		}
		_ = connection.Close()
	}
	store := NewTokenStore(newFakeKeychain())
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	coordinator.mu.Lock()
	redirect := coordinator.pending.redirect
	coordinator.mu.Unlock()

	_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "loss-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorUnavailable) {
		t.Fatalf("response-loss error = %v, want unavailable", err)
	}
	if cloud.requestCount("password") != 1 || cloud.requestCount("exchange") != 0 || cloud.requestCount("token") != 0 {
		t.Fatalf("response loss was replayed/advanced: %v", cloud.requestSequence())
	}
	assertCoordinatorErrorRedacted(t, err, "person@example.com", "loss-password", cloud.transactionSecret)
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("response-loss snapshot = %+v", snapshot)
	}
	assertCoordinatorLoopbackClosed(t, redirect)
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("response loss persisted session: %v", err)
	}
	if _, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "loss-password"); !errors.Is(err, ErrLoginTransactionCoordinatorIdle) {
		t.Fatalf("response-loss replay error = %v", err)
	}
	if cloud.requestCount("password") != 1 {
		t.Fatalf("response-loss replay sent password %d times", cloud.requestCount("password"))
	}
}

func TestLoginTransactionCoordinator_CancelInterruptsCompletionAndClosesLoopback(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	passwordEntered := make(chan struct{}, 1)
	cloud.passwordHook = func(_ http.ResponseWriter, request *http.Request) {
		passwordEntered <- struct{}{}
		<-request.Context().Done()
	}
	store := NewTokenStore(newFakeKeychain())
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	coordinator.mu.Lock()
	redirect := coordinator.pending.redirect
	coordinator.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "cancel-password")
		result <- err
	}()
	select {
	case <-passwordEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("password request did not start")
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateCompleting {
		t.Fatalf("completing snapshot = %+v", snapshot)
	}
	coordinator.Cancel()
	select {
	case err := <-result:
		if !errors.Is(err, ErrLoginTransactionCoordinatorCanceled) {
			t.Fatalf("canceled completion error = %v", err)
		}
		assertCoordinatorErrorRedacted(t, err, "cancel-password", cloud.transactionSecret)
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not interrupt CompletePassword")
	}
	if cloud.requestCount("password") != 1 || cloud.requestCount("exchange") != 0 || cloud.requestCount("token") != 0 {
		t.Fatalf("canceled flow advanced: %v", cloud.requestSequence())
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("canceled snapshot = %+v", snapshot)
	}
	assertCoordinatorLoopbackClosed(t, redirect)
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("canceled completion persisted session: %v", err)
	}
	coordinator.Cancel() // idempotent
}

func TestLoginTransactionCoordinator_ConcurrentStartHasOneWinnerAndCancelPreventsLateInstall(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	createEntered := make(chan struct{}, 1)
	cloud.createHook = func(_ http.ResponseWriter, request *http.Request) {
		createEntered <- struct{}{}
		<-request.Context().Done()
	}
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))
	type startResult struct {
		snapshot LoginTransactionCoordinatorSnapshot
		err      error
	}
	first := make(chan startResult, 1)
	go func() {
		snapshot, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA)
		first <- startResult{snapshot: snapshot, err: err}
	}()
	select {
	case <-createEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("create request did not start")
	}
	redirect := cloud.lastCreate().RedirectURI
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateStarting {
		t.Fatalf("starting snapshot = %+v", snapshot)
	}
	second, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowB)
	if !errors.Is(err, ErrLoginTransactionCoordinatorBusy) || second.State != LoginTransactionCoordinatorStateStarting {
		t.Fatalf("second StartPassword = %+v, err=%v", second, err)
	}
	if cloud.requestCount("create") != 1 {
		t.Fatalf("concurrent Start sent %d creates", cloud.requestCount("create"))
	}
	staleComplete, err := coordinator.CompletePassword(
		context.Background(),
		coordinatorTestFlowB,
		"stale@example.com",
		"stale-password",
	)
	if !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) ||
		staleComplete.State != LoginTransactionCoordinatorStateStarting {
		t.Fatalf("cross-flow Complete during Start = %+v, err=%v", staleComplete, err)
	}
	staleCancel, err := coordinator.CancelFlow(coordinatorTestFlowB)
	if !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) ||
		staleCancel.State != LoginTransactionCoordinatorStateStarting {
		t.Fatalf("cross-flow Cancel during Start = %+v, err=%v", staleCancel, err)
	}
	if cloud.requestCount("password") != 0 || coordinator.Snapshot().State != LoginTransactionCoordinatorStateStarting {
		t.Fatalf("cross-flow mutation changed Start: sequence=%v snapshot=%+v", cloud.requestSequence(), coordinator.Snapshot())
	}

	coordinator.Cancel()
	select {
	case result := <-first:
		if !errors.Is(result.err, ErrLoginTransactionCoordinatorCanceled) ||
			result.snapshot.State != LoginTransactionCoordinatorStateIdle {
			t.Fatalf("canceled Start = %+v, err=%v", result.snapshot, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not interrupt StartPassword")
	}
	assertCoordinatorLoopbackClosed(t, redirect)
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("late Start installed pending state: %+v", snapshot)
	}
}

func TestLoginTransactionCoordinator_StaleFlowCannotCompleteOrCancelReplacement(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	store := NewTokenStore(newFakeKeychain())
	coordinator, _ := newCoordinatorForTest(cloud, store)

	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("Start flow A: %v", err)
	}
	if snapshot, err := coordinator.CancelFlow(coordinatorTestFlowA); err != nil ||
		snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("Cancel flow A = %+v, err=%v", snapshot, err)
	}
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowB); err != nil {
		t.Fatalf("Start flow B: %v", err)
	}

	before := cloud.requestSequence()
	staleComplete, err := coordinator.CompletePassword(
		context.Background(),
		coordinatorTestFlowA,
		"flow-a@example.com",
		"flow-a-password",
	)
	if !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) ||
		staleComplete.State != LoginTransactionCoordinatorStatePending {
		t.Fatalf("stale Complete = %+v, err=%v", staleComplete, err)
	}
	if got := cloud.requestSequence(); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale Complete reached network: before=%v after=%v", before, got)
	}
	staleCancel, err := coordinator.CancelFlow(coordinatorTestFlowA)
	if !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) ||
		staleCancel.State != LoginTransactionCoordinatorStatePending {
		t.Fatalf("stale Cancel = %+v, err=%v", staleCancel, err)
	}
	if coordinator.Snapshot().State != LoginTransactionCoordinatorStatePending {
		t.Fatalf("stale flow disturbed replacement: %+v", coordinator.Snapshot())
	}

	completed, err := coordinator.CompletePassword(
		context.Background(),
		coordinatorTestFlowB,
		"flow-b@example.com",
		"flow-b-password",
	)
	if err != nil || completed.State != LoginTransactionCoordinatorStateComplete {
		t.Fatalf("Complete flow B = %+v, err=%v", completed, err)
	}
	cloud.mu.Lock()
	passwordRequests := append([]loginTransactionPasswordRequest(nil), cloud.passwordRequests...)
	cloud.mu.Unlock()
	if len(passwordRequests) != 1 || passwordRequests[0].Email != "flow-b@example.com" ||
		passwordRequests[0].Password != "flow-b-password" {
		t.Fatalf("password requests crossed flow generations: %+v", passwordRequests)
	}
}

func TestLoginTransactionCoordinator_ConcurrentCompleteHasOneWinner(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	passwordEntered := make(chan struct{}, 1)
	releasePassword := make(chan struct{})
	cloud.passwordHook = func(writer http.ResponseWriter, _ *http.Request) {
		passwordEntered <- struct{}{}
		<-releasePassword
		cloud.writePassword(writer)
	}
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	first := make(chan error, 1)
	go func() {
		_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "first@example.com", "first-password")
		first <- err
	}()
	select {
	case <-passwordEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("first password request did not start")
	}
	second, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "second@example.com", "second-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorBusy) || second.State != LoginTransactionCoordinatorStateCompleting {
		t.Fatalf("concurrent CompletePassword = %+v, err=%v", second, err)
	}
	if cloud.requestCount("password") != 1 {
		t.Fatalf("concurrent Complete sent %d passwords", cloud.requestCount("password"))
	}
	close(releasePassword)
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first CompletePassword: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first CompletePassword did not finish")
	}
	if got := cloud.requestSequence(); !reflect.DeepEqual(got, []string{"create", "password", "exchange", "token"}) {
		t.Fatalf("concurrent sequence = %v", got)
	}
}

func TestLoginTransactionCoordinator_SecureTokenExchangeRejectsRedirectWithoutSharedJar(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	var redirectTargetRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetRequests.Add(1)
		coordinatorWriteJSON(t, w, http.StatusOK, map[string]any{
			"access_token": "must-not-be-reached",
		})
	}))
	defer redirectTarget.Close()
	rawMarker := "RAW_TOKEN_REDIRECT_SECRET"
	cloud.tokenHook = func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Location", redirectTarget.URL+"/?code="+rawMarker)
		writer.Header().Set("X-Internal-Secret", rawMarker)
		writer.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(writer, `{"access_token":"`+rawMarker+`","refresh_token":"`+rawMarker+`"}`)
	}
	store := NewTokenStore(newFakeKeychain())
	coordinator, client := newCoordinatorForTest(cloud, store)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	serverURL, _ := url.Parse(cloud.server.URL)
	jar.SetCookies(serverURL, []*http.Cookie{{Name: "shared-session", Value: rawMarker}})
	var sharedRedirects atomic.Int32
	shared := cloud.server.Client()
	shared.Jar = jar
	shared.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		sharedRedirects.Add(1)
		return nil
	}
	client.HTTPClient = shared

	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	_, err = coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "token-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorUnavailable) {
		t.Fatalf("token redirect error = %v, want unavailable", err)
	}
	assertCoordinatorErrorRedacted(t, err, rawMarker, "token-password", cloud.transactionSecret,
		cloud.exchangeToken, cloud.authorizationCode, redirectTarget.URL)
	if redirectTargetRequests.Load() != 0 || sharedRedirects.Load() != 0 {
		t.Fatalf("token 3xx followed: target=%d sharedCallback=%d",
			redirectTargetRequests.Load(), sharedRedirects.Load())
	}
	for _, header := range cloud.requestHeaders("token") {
		if header.Get("Cookie") != "" {
			t.Fatalf("secure token exchange inherited Cookie Jar: %q", header.Get("Cookie"))
		}
	}
	if cloud.requestCount("token") != 1 || coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("token redirect state/count: snapshot=%+v sequence=%v", coordinator.Snapshot(), cloud.requestSequence())
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("token redirect persisted session: %v", err)
	}
}

func TestLoginTransactionCoordinator_RejectsTokenScopeDriftBeforeKeychainSave(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	cloud.tokenHook = func(writer http.ResponseWriter, _ *http.Request) {
		coordinatorWriteJSON(t, writer, http.StatusOK, map[string]any{
			"access_token":       "scope-drift-access-secret",
			"token_type":         "Bearer",
			"expires_in":         900,
			"refresh_token":      "scope-drift-refresh-secret",
			"refresh_expires_in": 7_776_000,
			"scope":              "workagent billing.admin",
		})
	}
	store := NewTokenStore(newFakeKeychain())
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}

	_, err := coordinator.CompletePassword(
		context.Background(),
		coordinatorTestFlowA,
		"person@example.com",
		"correct-password",
	)
	if !errors.Is(err, ErrLoginTransactionCoordinatorUnavailable) {
		t.Fatalf("scope drift error = %v", err)
	}
	assertCoordinatorErrorRedacted(t, err,
		"billing.admin", "scope-drift-access-secret", "scope-drift-refresh-secret")
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("scope drift persisted session: %v", err)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("scope drift retained transaction: %+v", snapshot)
	}
}

type coordinatorWriteFailKeychain struct {
	base *fakeKeychain
	err  error
}

func (k coordinatorWriteFailKeychain) Write(_, _ string, _ []byte) error { return k.err }
func (k coordinatorWriteFailKeychain) Read(service, account string) ([]byte, error) {
	return k.base.Read(service, account)
}
func (k coordinatorWriteFailKeychain) Delete(service, account string) error {
	return k.base.Delete(service, account)
}

type coordinatorBlockingWriteKeychain struct {
	base    *fakeKeychain
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	writes  atomic.Int32
}

func newCoordinatorBlockingWriteKeychain() *coordinatorBlockingWriteKeychain {
	return &coordinatorBlockingWriteKeychain{
		base:    newFakeKeychain(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (k *coordinatorBlockingWriteKeychain) Write(service, account string, value []byte) error {
	k.writes.Add(1)
	k.once.Do(func() { close(k.entered) })
	<-k.release
	return k.base.Write(service, account, value)
}

func (k *coordinatorBlockingWriteKeychain) Read(service, account string) ([]byte, error) {
	return k.base.Read(service, account)
}

func (k *coordinatorBlockingWriteKeychain) Delete(service, account string) error {
	return k.base.Delete(service, account)
}

func TestLoginTransactionCoordinator_SaveFenceLinearizesBeforeCancel(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	keychain := newCoordinatorBlockingWriteKeychain()
	defer closeCoordinatorTestChannel(keychain.release)
	store := NewTokenStore(keychain)
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}

	completeResult := make(chan error, 1)
	go func() {
		_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "commit-password")
		completeResult <- err
	}()
	select {
	case <-keychain.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Keychain Save did not start")
	}

	cancelDone := make(chan struct{})
	go func() {
		coordinator.Cancel()
		close(cancelDone)
	}()
	select {
	case <-cancelDone:
		t.Fatal("Cancel crossed an in-progress Save linearization fence")
	case <-time.After(50 * time.Millisecond):
	}
	closeCoordinatorTestChannel(keychain.release)

	select {
	case err := <-completeResult:
		if err != nil {
			t.Fatalf("CompletePassword after Save won fence: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CompletePassword did not finish after Keychain release")
	}
	select {
	case <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not finish after committed Save")
	}
	if keychain.writes.Load() != 1 {
		t.Fatalf("Keychain writes = %d, want 1", keychain.writes.Load())
	}
	stored, err := store.Get()
	if err != nil || stored.AccessToken != "coordinator-access-secret" {
		t.Fatalf("committed session = %+v, err=%v", stored, err)
	}
	if coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("post-commit Cancel snapshot = %+v", coordinator.Snapshot())
	}
}

func TestLoginTransactionCoordinator_CancelFenceWinsBeforeSaveWithZeroLateWrites(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	tokenEntered := make(chan struct{}, 1)
	releaseToken := make(chan struct{})
	defer closeCoordinatorTestChannel(releaseToken)
	cloud.tokenHook = func(writer http.ResponseWriter, _ *http.Request) {
		tokenEntered <- struct{}{}
		<-releaseToken
		cloud.writeToken(writer)
	}
	keychain := newCoordinatorBlockingWriteKeychain()
	defer closeCoordinatorTestChannel(keychain.release)
	store := NewTokenStore(keychain)
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}

	completeResult := make(chan error, 1)
	go func() {
		_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "cancel-before-save")
		completeResult <- err
	}()
	select {
	case <-tokenEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("token exchange did not start")
	}
	coordinator.Cancel()
	select {
	case err := <-completeResult:
		if !errors.Is(err, ErrLoginTransactionCoordinatorCanceled) {
			t.Fatalf("Cancel-before-Save error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel did not interrupt token exchange")
	}
	closeCoordinatorTestChannel(releaseToken)
	select {
	case <-keychain.entered:
		t.Fatal("stale completion reached Keychain after Cancel won generation fence")
	case <-time.After(50 * time.Millisecond):
	}
	if keychain.writes.Load() != 0 {
		t.Fatalf("late Keychain writes = %d, want 0", keychain.writes.Load())
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Cancel-before-Save persisted session: %v", err)
	}
	if coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("Cancel-before-Save snapshot = %+v", coordinator.Snapshot())
	}
}

func TestLoginTransactionCoordinator_CommitFenceRejectsExpiredPendingWithoutSave(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	var currentUnixNano atomic.Int64
	currentUnixNano.Store(coordinatorTestNow.UnixNano())
	keychain := newCoordinatorBlockingWriteKeychain()
	close(keychain.release)
	store := NewTokenStore(keychain)
	coordinator, _ := newCoordinatorForTest(cloud, store)
	coordinator.now = func() time.Time {
		return time.Unix(0, currentUnixNano.Load()).UTC()
	}
	cloud.tokenHook = func(writer http.ResponseWriter, _ *http.Request) {
		// Advance only after password and capability exchange have succeeded,
		// reproducing the final pre-Keychain expiry window deterministically.
		currentUnixNano.Store(cloud.expiresAt.Add(time.Nanosecond).UnixNano())
		cloud.writeToken(writer)
	}

	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	_, err := coordinator.CompletePassword(
		context.Background(),
		coordinatorTestFlowA,
		"person@example.com",
		"expires-before-save",
	)
	if !errors.Is(err, ErrLoginTransactionCoordinatorTerminal) {
		t.Fatalf("commit expiry error = %v", err)
	}
	if keychain.writes.Load() != 0 {
		t.Fatalf("expired pending reached Keychain %d time(s)", keychain.writes.Load())
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired pending persisted session: %v", err)
	}
	if coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("expired commit retained transaction: %+v", coordinator.Snapshot())
	}
}

func TestLoginTransactionCoordinator_KeychainFailureIsClosedAndTerminalLocally(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	rawMarker := "RAW_KEYCHAIN_SECRET"
	keychain := coordinatorWriteFailKeychain{base: newFakeKeychain(), err: errors.New(rawMarker)}
	store := NewTokenStore(keychain)
	coordinator, _ := newCoordinatorForTest(cloud, store)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "persist-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorUnavailable) {
		t.Fatalf("Keychain failure error = %v", err)
	}
	assertCoordinatorErrorRedacted(t, err, rawMarker, "persist-password",
		"coordinator-access-secret", "coordinator-refresh-secret", cloud.transactionSecret)
	if coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("Keychain failure retained pending state: %+v", coordinator.Snapshot())
	}
	if _, err := store.Get(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Keychain failure stored session: %v", err)
	}
}

func TestLoginTransactionCoordinator_ExpiredPendingFailsBeforePassword(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	coordinator.mu.Lock()
	redirect := coordinator.pending.redirect
	coordinator.mu.Unlock()
	coordinator.now = func() time.Time { return cloud.expiresAt.Add(time.Second) }

	_, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, "person@example.com", "expired-password")
	if !errors.Is(err, ErrLoginTransactionCoordinatorTerminal) {
		t.Fatalf("expired pending error = %v", err)
	}
	if cloud.requestCount("password") != 0 || coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("expired pending reached network/state: sequence=%v snapshot=%+v",
			cloud.requestSequence(), coordinator.Snapshot())
	}
	assertCoordinatorLoopbackClosed(t, redirect)
}

func TestLoginTransactionCoordinator_ExpiryTimerReleasesLoopbackAndAllowsRestart(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	cloud.expiresAt = coordinatorTestNow.Add(250 * time.Millisecond)
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("StartPassword: %v", err)
	}
	coordinator.mu.Lock()
	redirect := coordinator.pending.redirect
	coordinator.mu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for coordinator.Snapshot().State != LoginTransactionCoordinatorStateIdle && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("expiry timer retained pending state: %+v", snapshot)
	}
	assertCoordinatorLoopbackClosed(t, redirect)

	cloud.expiresAt = coordinatorTestNow.Add(10 * time.Minute)
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowB); err != nil {
		t.Fatalf("restart after expiry: %v", err)
	}
	if cloud.requestCount("create") != 2 {
		t.Fatalf("restart create requests = %d, want 2", cloud.requestCount("create"))
	}
	coordinator.Cancel()
}

func TestLoginTransactionCoordinator_RejectsUnboundedServerTTL(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	cloud.expiresAt = coordinatorTestNow.Add(loginTransactionCoordinatorMaxTTL + time.Second)
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))

	_, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA)
	if !errors.Is(err, ErrLoginTransactionCoordinatorTerminal) {
		t.Fatalf("unbounded TTL error = %v", err)
	}
	if snapshot := coordinator.Snapshot(); snapshot.State != LoginTransactionCoordinatorStateIdle {
		t.Fatalf("unbounded TTL retained state: %+v", snapshot)
	}
}

func TestLoginTransactionCoordinator_InvalidInputDoesNotReachNetworkOrConsumePending(t *testing.T) {
	cloud := newCoordinatorTestCloud(t)
	coordinator, _ := newCoordinatorForTest(cloud, NewTokenStore(newFakeKeychain()))
	if _, err := coordinator.StartPassword(context.Background(), "workagent  profile", coordinatorTestFlowA); !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) {
		t.Fatalf("invalid scope error = %v", err)
	}
	if cloud.requestCount("create") != 0 {
		t.Fatalf("invalid scope reached network")
	}
	if _, err := coordinator.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); err != nil {
		t.Fatalf("valid StartPassword: %v", err)
	}
	before := cloud.requestSequence()
	for _, test := range []struct {
		email    string
		password string
	}{
		{email: "not-an-email", password: "secret"},
		{email: "person@example.com", password: ""},
		{email: "person@example.com", password: "secret\x00value"},
	} {
		if _, err := coordinator.CompletePassword(context.Background(), coordinatorTestFlowA, test.email, test.password); !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) {
			t.Fatalf("invalid credentials input error = %v", err)
		}
	}
	if got := cloud.requestSequence(); !reflect.DeepEqual(got, before) ||
		coordinator.Snapshot().State != LoginTransactionCoordinatorStatePending {
		t.Fatalf("invalid input consumed pending/network: before=%v after=%v snapshot=%+v",
			before, got, coordinator.Snapshot())
	}
	coordinator.Cancel()

	invalidDevice := NewLoginTransactionCoordinator(
		NewClient(cloud.server.URL),
		NewTokenStore(newFakeKeychain()),
		"NOT-A-CANONICAL-DEVICE-ID",
	)
	if _, err := invalidDevice.StartPassword(context.Background(), "workagent", coordinatorTestFlowA); !errors.Is(err, ErrLoginTransactionCoordinatorInvalidInput) {
		t.Fatalf("invalid device error = %v", err)
	}
	if cloud.requestCount("create") != 1 {
		t.Fatalf("invalid device reached network: create count=%d", cloud.requestCount("create"))
	}
}

func TestValidLoginTransactionLocalFlowIDRequiresCanonicalThirtyTwoBytes(t *testing.T) {
	if !ValidLoginTransactionLocalFlowID(coordinatorTestFlowA) ||
		!ValidLoginTransactionLocalFlowID(coordinatorTestFlowB) {
		t.Fatal("canonical 32-byte flow IDs were rejected")
	}
	for _, invalid := range []string{
		"",
		coordinatorTestFlowA + "=",
		coordinatorTestFlowA[:len(coordinatorTestFlowA)-1],
		coordinatorTestFlowA + "A",
		" " + coordinatorTestFlowA,
		strings.Repeat("_", 43),
		strings.Repeat("a", 43),
		strings.Repeat("A", 42) + "+",
	} {
		if ValidLoginTransactionLocalFlowID(invalid) {
			t.Fatalf("malformed flow ID accepted: %q", invalid)
		}
	}
}

func assertCoordinatorPublicValueRedacted(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal public coordinator value: %v", err)
	}
	for _, secret := range forbidden {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Errorf("public coordinator value leaked %q: %s", secret, raw)
		}
	}
}

func assertCoordinatorErrorRedacted(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected coordinator error")
	}
	for _, secret := range forbidden {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Errorf("coordinator error leaked %q: %q", secret, err.Error())
		}
	}
}

func assertCoordinatorLoopbackOpen(t *testing.T, redirectURI string) {
	t.Helper()
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse loopback: %v", err)
	}
	connection, err := net.DialTimeout("tcp", parsed.Host, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("loopback %s is not listening: %v", parsed.Host, err)
	}
	_ = connection.Close()
}

func assertCoordinatorLoopbackClosed(t *testing.T, redirectURI string) {
	t.Helper()
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		t.Fatalf("parse loopback: %v", err)
	}
	for attempt := 0; attempt < 20; attempt++ {
		connection, dialErr := net.DialTimeout("tcp", parsed.Host, 50*time.Millisecond)
		if dialErr != nil {
			return
		}
		_ = connection.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("loopback %s is still listening", parsed.Host)
}

func closeCoordinatorTestChannel(channel chan struct{}) {
	select {
	case <-channel:
	default:
		close(channel)
	}
}
