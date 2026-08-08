package logintransaction

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

var testStartTime = time.Date(2026, time.August, 5, 8, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type incrementingReader struct {
	mu   sync.Mutex
	next byte
}

func (r *incrementingReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for index := range p {
		r.next++
		p[index] = r.next
	}
	return len(p), nil
}

type passwordResult struct {
	principal Principal
	err       error
}

type passwordCall struct {
	email    string
	password string
}

type passwordAuthenticatorStub struct {
	mu      sync.Mutex
	results []passwordResult
	calls   []passwordCall
}

func (s *passwordAuthenticatorStub) AuthenticatePassword(
	_ context.Context,
	email string,
	password string,
) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, passwordCall{email: email, password: password})
	if len(s.results) == 0 {
		return Principal{}, errors.New("unexpected password authentication")
	}
	result := s.results[0]
	if len(s.results) > 1 {
		s.results = s.results[1:]
	}
	return result.principal, result.err
}

func (s *passwordAuthenticatorStub) Calls() []passwordCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]passwordCall(nil), s.calls...)
}

type googleResult struct {
	principal Principal
	err       error
}

type googleAuthenticatorStub struct {
	mu      sync.Mutex
	results []googleResult
	calls   []GoogleExchange
}

func (s *googleAuthenticatorStub) AuthenticateGoogle(
	_ context.Context,
	exchange GoogleExchange,
) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, exchange)
	if len(s.results) == 0 {
		return Principal{}, errors.New("unexpected Google authentication")
	}
	result := s.results[0]
	if len(s.results) > 1 {
		s.results = s.results[1:]
	}
	return result.principal, result.err
}

func (s *googleAuthenticatorStub) Calls() []GoogleExchange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]GoogleExchange(nil), s.calls...)
}

type blockingPasswordAuthenticator struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls int
}

type cancelingPasswordAuthenticator struct {
	cancel context.CancelFunc
}

func (s *cancelingPasswordAuthenticator) AuthenticatePassword(
	_ context.Context,
	_ string,
	_ string,
) (Principal, error) {
	s.cancel()
	return Principal{UserID: 55}, nil
}

func (s *blockingPasswordAuthenticator) AuthenticatePassword(
	ctx context.Context,
	_ string,
	_ string,
) (Principal, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return Principal{UserID: 55}, nil
	case <-ctx.Done():
		return Principal{}, ctx.Err()
	}
}

func (s *blockingPasswordAuthenticator) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func validCreateInput() CreateInput {
	return CreateInput{
		ClientID:            "workmax-desktop",
		RedirectURI:         "http://127.0.0.1:49152/oauth/callback",
		OAuthState:          "MDEyMzQ1Njc4OWFiY2RlZg",
		CodeChallenge:       "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		CodeChallengeMethod: GooglePKCEMethod,
		Scope:               "workagent offline_access",
		DeviceID:            "0123456789abcdef0123456789abcdef",
	}
}

func newTestService(
	t *testing.T,
	password PasswordAuthenticator,
	google GoogleAuthenticator,
	ttl time.Duration,
) (*Service, *MemoryRepository, *fakeClock) {
	t.Helper()
	repository := NewMemoryRepository()
	clock := &fakeClock{now: testStartTime}
	service, err := NewService(repository, password, google, Options{
		TTL:    ttl,
		Clock:  clock,
		Random: &incrementingReader{},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, repository, clock
}

func mustCreate(t *testing.T, service *Service) TransactionHandle {
	t.Helper()
	handle, err := service.Create(context.Background(), validCreateInput())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return handle
}

func TestCreateFreezesRequestAndHashesSecret(t *testing.T) {
	service, repository, _ := newTestService(t, nil, nil, time.Minute)
	request := validCreateInput()

	handle, err := service.Create(context.Background(), request)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if handle.TransactionID == "" || handle.TransactionSecret == "" {
		t.Fatalf("Create() returned an empty capability: %+v", handle)
	}
	if got, want := handle.ExpiresAt, testStartTime.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", got, want)
	}

	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.Request != request {
		t.Fatalf("persisted request = %+v, want %+v", record.Request, request)
	}
	if record.State != StatePending || record.Version != 1 {
		t.Fatalf("record state/version = %s/%d, want %s/1", record.State, record.Version, StatePending)
	}
	if !secretMatches(record.SecretHash, handle.TransactionSecret) {
		t.Fatal("persisted transaction secret hash does not match returned secret")
	}
	if record.ID == handle.TransactionSecret || record.GoogleCodeVerifier == handle.TransactionSecret {
		t.Fatal("raw transaction secret was persisted in a string field")
	}

	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.TransactionID != handle.TransactionID || snapshot.State != StatePending {
		t.Fatalf("Inspect() = %+v", snapshot)
	}
}

func TestInspectAuthenticatedRequiresTransactionSecret(t *testing.T) {
	service, _, clock := newTestService(t, nil, nil, time.Minute)
	handle := mustCreate(t, service)

	snapshot, err := service.InspectAuthenticated(
		context.Background(),
		handle.TransactionID,
		handle.TransactionSecret,
	)
	if err != nil {
		t.Fatalf("InspectAuthenticated() error = %v", err)
	}
	if snapshot.TransactionID != handle.TransactionID || snapshot.State != StatePending {
		t.Fatalf("InspectAuthenticated() = %+v", snapshot)
	}

	_, wrongSecretErr := service.InspectAuthenticated(
		context.Background(),
		handle.TransactionID,
		strings.Repeat("x", 43),
	)
	_, unknownIDErr := service.InspectAuthenticated(
		context.Background(),
		strings.Repeat("y", 22),
		handle.TransactionSecret,
	)
	if wrongSecretErr != ErrInvalidTransaction || unknownIDErr != ErrInvalidTransaction {
		t.Fatalf(
			"capability errors differ: wrong secret=%v, unknown id=%v; want ErrInvalidTransaction",
			wrongSecretErr,
			unknownIDErr,
		)
	}

	clock.Advance(time.Minute)
	snapshot, err = service.InspectAuthenticated(
		context.Background(),
		handle.TransactionID,
		handle.TransactionSecret,
	)
	if err != nil {
		t.Fatalf("expired InspectAuthenticated() error = %v", err)
	}
	if snapshot.State != StateExpired {
		t.Fatalf("expired InspectAuthenticated().State = %s, want %s", snapshot.State, StateExpired)
	}
}

func TestCreateRejectsMalformedBindings(t *testing.T) {
	tests := map[string]func(*CreateInput){
		"non-loopback redirect": func(input *CreateInput) {
			input.RedirectURI = "https://example.com/oauth/callback"
		},
		"localhost redirect": func(input *CreateInput) {
			input.RedirectURI = "http://localhost:49152/oauth/callback"
		},
		"redirect query": func(input *CreateInput) {
			input.RedirectURI = "http://127.0.0.1:49152/oauth/callback?next=1"
		},
		"encoded redirect path": func(input *CreateInput) {
			input.RedirectURI = "http://127.0.0.1:49152/oauth%2Fcallback"
		},
		"short OAuth state": func(input *CreateInput) {
			input.OAuthState = "short"
		},
		"padded OAuth state": func(input *CreateInput) {
			input.OAuthState += "="
		},
		"plain PKCE": func(input *CreateInput) {
			input.CodeChallengeMethod = "plain"
		},
		"short PKCE challenge": func(input *CreateInput) {
			input.CodeChallenge = "short"
		},
		"non-canonical PKCE challenge": func(input *CreateInput) {
			input.CodeChallenge = strings.Repeat("C", 43)
		},
		"non-canonical scope": func(input *CreateInput) {
			input.Scope = "workagent  offline_access"
		},
		"non-hex device": func(input *CreateInput) {
			input.DeviceID = strings.Repeat("z", 32)
		},
		"uppercase device": func(input *CreateInput) {
			input.DeviceID = "ABCDEF0123456789abcdef0123456789"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			service, _, _ := newTestService(t, nil, nil, time.Minute)
			input := validCreateInput()
			mutate(&input)
			_, err := service.Create(context.Background(), input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestTransactionExpiresAtTTLBoundary(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{principal: Principal{UserID: 42}}},
	}
	service, repository, clock := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)

	clock.Advance(time.Minute)
	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != StateExpired {
		t.Fatalf("Inspect().State = %s, want %s", snapshot.State, StateExpired)
	}

	_, err = service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "correct horse battery staple",
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("CompletePassword() error = %v, want ErrExpired", err)
	}
	if calls := password.Calls(); len(calls) != 0 {
		t.Fatalf("authenticator calls = %d, want 0", len(calls))
	}

	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateExpired || record.ExchangeTokenHash != ([32]byte{}) || record.GoogleCodeVerifier != "" {
		t.Fatalf("expired record retained transient state: %+v", record)
	}
}

func TestPasswordCompletionAndOneTimeExchange(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{principal: Principal{UserID: 42}}},
	}
	service, repository, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)

	completion, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("CompletePassword() error = %v", err)
	}
	if completion.ExchangeToken == "" || completion.AuthMethod != AuthMethodPassword {
		t.Fatalf("CompletePassword() = %+v", completion)
	}
	if calls := password.Calls(); len(calls) != 1 || calls[0].email != "person@example.com" || calls[0].password != "correct horse battery staple" {
		t.Fatalf("authenticator calls = %+v", calls)
	}

	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != StateAuthenticated || snapshot.AuthMethod != AuthMethodPassword {
		t.Fatalf("authenticated snapshot = %+v", snapshot)
	}

	_, err = service.Exchange(context.Background(), ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: strings.Repeat("x", 43),
	})
	if !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("Exchange(wrong token) error = %v, want ErrInvalidTransaction", err)
	}

	grant, err := service.Exchange(context.Background(), ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	request := validCreateInput()
	if grant.UserID != 42 || grant.AuthMethod != AuthMethodPassword ||
		grant.ClientID != request.ClientID || grant.RedirectURI != request.RedirectURI ||
		grant.OAuthState != request.OAuthState || grant.CodeChallenge != request.CodeChallenge ||
		grant.CodeChallengeMethod != request.CodeChallengeMethod || grant.Scope != request.Scope ||
		grant.DeviceID != request.DeviceID {
		t.Fatalf("Exchange() grant = %+v", grant)
	}

	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateExchanged || record.ExchangeTokenHash != ([32]byte{}) {
		t.Fatalf("exchanged record = %+v", record)
	}

	_, err = service.Exchange(context.Background(), ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("second Exchange() error = %v, want ErrReplay", err)
	}
	_, err = service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "correct horse battery staple",
	})
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed CompletePassword() error = %v, want ErrReplay", err)
	}
}

func TestPasswordFailureReturnsToPendingAndCanRetry(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{
			{err: ErrAuthenticationFailed},
			{principal: Principal{UserID: 7}},
		},
	}
	service, _, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	input := PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "first attempt",
	}

	_, err := service.CompletePassword(context.Background(), input)
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("first CompletePassword() error = %v, want ErrAuthenticationFailed", err)
	}
	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != StatePending {
		t.Fatalf("state after rejected credentials = %s, want %s", snapshot.State, StatePending)
	}

	input.Password = "second attempt"
	completion, err := service.CompletePassword(context.Background(), input)
	if err != nil {
		t.Fatalf("second CompletePassword() error = %v", err)
	}
	if completion.AuthMethod != AuthMethodPassword {
		t.Fatalf("second CompletePassword() = %+v", completion)
	}
	if calls := password.Calls(); len(calls) != 2 {
		t.Fatalf("authenticator calls = %d, want 2", len(calls))
	}
}

func TestPasswordFailuresExhaustDurableTransactionBudget(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{err: ErrAuthenticationFailed}},
	}
	service, repository, clock := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	input := PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "wrong password",
	}

	for attempt := uint16(1); attempt <= MaxPasswordFailures; attempt++ {
		_, err := service.CompletePassword(context.Background(), input)
		if !errors.Is(err, ErrAuthenticationFailed) {
			t.Fatalf("attempt %d error = %v, want ErrAuthenticationFailed", attempt, err)
		}
		record, err := repository.Get(context.Background(), handle.TransactionID)
		if err != nil {
			t.Fatal(err)
		}
		wantState := StatePending
		if attempt == MaxPasswordFailures {
			wantState = StateFailed
		}
		if record.PasswordFailures != attempt || record.State != wantState || !record.LastPasswordFailure.Equal(clock.Now()) {
			t.Fatalf("attempt %d record = %+v, want failures=%d state=%s", attempt, record, attempt, wantState)
		}
	}

	_, err := service.CompletePassword(context.Background(), input)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("post-budget error = %v, want ErrInvalidState", err)
	}
	if calls := password.Calls(); len(calls) != int(MaxPasswordFailures) {
		t.Fatalf("authenticator calls = %d, want %d", len(calls), MaxPasswordFailures)
	}
}

func TestPasswordAuthenticatorInfrastructureErrorIsNotCredentialFailure(t *testing.T) {
	backendErr := errors.New("identity database unavailable")
	password := &passwordAuthenticatorStub{
		results: []passwordResult{
			{err: backendErr},
			{principal: Principal{UserID: 7}},
		},
	}
	service, repository, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	input := PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	}

	_, err := service.CompletePassword(context.Background(), input)
	if !errors.Is(err, backendErr) || errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("infrastructure error = %v, want wrapped backend error", err)
	}
	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != StatePending || record.PasswordFailures != 0 {
		t.Fatalf("infrastructure error consumed credential budget: %+v", record)
	}
	if _, err := service.CompletePassword(context.Background(), input); err != nil {
		t.Fatalf("retry after infrastructure error: %v", err)
	}
}

func TestWrongTransactionSecretDoesNotCallPasswordAuthenticator(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{principal: Principal{UserID: 42}}},
	}
	service, _, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)

	_, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: strings.Repeat("x", 43),
		Email:             "person@example.com",
		Password:          "password",
	})
	if !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("CompletePassword() error = %v, want ErrInvalidTransaction", err)
	}
	if calls := password.Calls(); len(calls) != 0 {
		t.Fatalf("authenticator calls = %d, want 0", len(calls))
	}
}

func TestConcurrentPasswordCompletionClaimsOnce(t *testing.T) {
	password := &blockingPasswordAuthenticator{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service, _, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	input := PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	}

	firstResult := make(chan error, 1)
	go func() {
		_, err := service.CompletePassword(context.Background(), input)
		firstResult <- err
	}()
	select {
	case <-password.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first password authenticator call did not start")
	}

	_, secondErr := service.CompletePassword(context.Background(), input)
	if !errors.Is(secondErr, ErrConflict) {
		t.Fatalf("concurrent CompletePassword() error = %v, want ErrConflict", secondErr)
	}
	close(password.release)
	if firstErr := <-firstResult; firstErr != nil {
		t.Fatalf("claimed CompletePassword() error = %v", firstErr)
	}
	if calls := password.CallCount(); calls != 1 {
		t.Fatalf("authenticator calls = %d, want 1", calls)
	}
}

func TestCancelledPasswordCompletionReleasesClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	password := &cancelingPasswordAuthenticator{cancel: cancel}
	service, _, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)

	_, err := service.CompletePassword(ctx, PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CompletePassword() error = %v, want context.Canceled", err)
	}
	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != StatePending {
		t.Fatalf("state after cancelled completion = %s, want %s", snapshot.State, StatePending)
	}
}

func TestGooglePKCECompletionAndExchange(t *testing.T) {
	google := &googleAuthenticatorStub{
		results: []googleResult{{principal: Principal{UserID: 88}}},
	}
	service, repository, _ := newTestService(t, nil, google, time.Minute)
	handle := mustCreate(t, service)

	start, err := service.StartGoogle(context.Background(), GoogleStartInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
	})
	if err != nil {
		t.Fatalf("StartGoogle() error = %v", err)
	}
	if start.ProviderState == "" || start.CodeChallengeMethod != GooglePKCEMethod {
		t.Fatalf("StartGoogle() = %+v", start)
	}
	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateGooglePending || record.GoogleCodeVerifier == "" {
		t.Fatalf("Google pending record = %+v", record)
	}
	if !secretMatches(record.GoogleStateHash, start.ProviderState) {
		t.Fatal("persisted Google state hash does not match provider state")
	}
	if got := pkceChallenge(record.GoogleCodeVerifier); got != start.CodeChallenge {
		t.Fatalf("provider PKCE challenge = %q, want %q", start.CodeChallenge, got)
	}
	verifier := record.GoogleCodeVerifier

	completion, err := service.CompleteGoogle(context.Background(), GoogleCompletionInput{
		ProviderState:     start.ProviderState,
		AuthorizationCode: "provider-code",
	})
	if err != nil {
		t.Fatalf("CompleteGoogle() error = %v", err)
	}
	if completion.AuthMethod != AuthMethodGoogle || completion.ExchangeToken == "" {
		t.Fatalf("CompleteGoogle() = %+v", completion)
	}
	calls := google.Calls()
	if len(calls) != 1 || calls[0].TransactionID != handle.TransactionID ||
		calls[0].AuthorizationCode != "provider-code" || calls[0].CodeVerifier != verifier {
		t.Fatalf("Google authenticator calls = %+v", calls)
	}

	record, err = repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateAuthenticated || record.GoogleStateHash != ([32]byte{}) || record.GoogleCodeVerifier != "" {
		t.Fatalf("authenticated Google record retained transient state: %+v", record)
	}

	grant, err := service.Exchange(context.Background(), ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if grant.UserID != 88 || grant.AuthMethod != AuthMethodGoogle {
		t.Fatalf("Exchange() grant = %+v", grant)
	}

	_, err = service.CompleteGoogle(context.Background(), GoogleCompletionInput{
		ProviderState:     start.ProviderState,
		AuthorizationCode: "provider-code",
	})
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed CompleteGoogle() error = %v, want ErrReplay", err)
	}
	if calls := google.Calls(); len(calls) != 1 {
		t.Fatalf("Google authenticator calls after replay = %d, want 1", len(calls))
	}
}

func TestGoogleStateMismatchDoesNotCallAuthenticator(t *testing.T) {
	google := &googleAuthenticatorStub{
		results: []googleResult{{principal: Principal{UserID: 88}}},
	}
	service, _, _ := newTestService(t, nil, google, time.Minute)
	handle := mustCreate(t, service)
	start, err := service.StartGoogle(context.Background(), GoogleStartInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
	})
	if err != nil {
		t.Fatalf("StartGoogle() error = %v", err)
	}
	wrongState := start.TransactionID + "." + strings.Repeat("w", 43)

	_, err = service.CompleteGoogle(context.Background(), GoogleCompletionInput{
		ProviderState:     wrongState,
		AuthorizationCode: "provider-code",
	})
	if !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("CompleteGoogle() error = %v, want ErrInvalidTransaction", err)
	}
	if calls := google.Calls(); len(calls) != 0 {
		t.Fatalf("Google authenticator calls = %d, want 0", len(calls))
	}
	snapshot, err := service.Inspect(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if snapshot.State != StateGooglePending {
		t.Fatalf("state after mismatched provider state = %s, want %s", snapshot.State, StateGooglePending)
	}
}

func TestGoogleProviderFailureIsTerminalAndReplayProtected(t *testing.T) {
	google := &googleAuthenticatorStub{
		results: []googleResult{{err: errors.New("provider rejected code")}},
	}
	service, repository, _ := newTestService(t, nil, google, time.Minute)
	handle := mustCreate(t, service)
	start, err := service.StartGoogle(context.Background(), GoogleStartInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
	})
	if err != nil {
		t.Fatalf("StartGoogle() error = %v", err)
	}
	input := GoogleCompletionInput{
		ProviderState:     start.ProviderState,
		AuthorizationCode: "provider-code",
	}

	_, err = service.CompleteGoogle(context.Background(), input)
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("CompleteGoogle() error = %v, want ErrAuthenticationFailed", err)
	}
	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateFailed || record.GoogleStateHash != ([32]byte{}) || record.GoogleCodeVerifier != "" {
		t.Fatalf("failed Google record = %+v", record)
	}

	_, err = service.CompleteGoogle(context.Background(), input)
	if !errors.Is(err, ErrReplay) {
		t.Fatalf("replayed CompleteGoogle() error = %v, want ErrReplay", err)
	}
	if calls := google.Calls(); len(calls) != 1 {
		t.Fatalf("Google authenticator calls = %d, want 1", len(calls))
	}
}

func TestGoogleCompletionExpiresBeforeAuthenticator(t *testing.T) {
	google := &googleAuthenticatorStub{
		results: []googleResult{{principal: Principal{UserID: 88}}},
	}
	service, repository, clock := newTestService(t, nil, google, time.Minute)
	handle := mustCreate(t, service)
	start, err := service.StartGoogle(context.Background(), GoogleStartInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
	})
	if err != nil {
		t.Fatalf("StartGoogle() error = %v", err)
	}
	clock.Advance(time.Minute)

	_, err = service.CompleteGoogle(context.Background(), GoogleCompletionInput{
		ProviderState:     start.ProviderState,
		AuthorizationCode: "provider-code",
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("CompleteGoogle() error = %v, want ErrExpired", err)
	}
	if calls := google.Calls(); len(calls) != 0 {
		t.Fatalf("Google authenticator calls = %d, want 0", len(calls))
	}
	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateExpired || record.GoogleStateHash != ([32]byte{}) || record.GoogleCodeVerifier != "" {
		t.Fatalf("expired Google record retained transient state: %+v", record)
	}
}

func TestConcurrentExchangeHasExactlyOneWinner(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{principal: Principal{UserID: 42}}},
	}
	service, _, _ := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	completion, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	})
	if err != nil {
		t.Fatalf("CompletePassword() error = %v", err)
	}
	input := ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			<-start
			_, exchangeErr := service.Exchange(context.Background(), input)
			results <- exchangeErr
		}()
	}
	close(start)

	successes := 0
	rejections := 0
	for index := 0; index < 2; index++ {
		exchangeErr := <-results
		switch {
		case exchangeErr == nil:
			successes++
		case errors.Is(exchangeErr, ErrReplay), errors.Is(exchangeErr, ErrConflict):
			rejections++
		default:
			t.Fatalf("concurrent Exchange() error = %v", exchangeErr)
		}
	}
	if successes != 1 || rejections != 1 {
		t.Fatalf("concurrent exchange outcomes: successes=%d rejections=%d", successes, rejections)
	}
}

func TestAuthenticatedTransactionExpiresBeforeExchange(t *testing.T) {
	password := &passwordAuthenticatorStub{
		results: []passwordResult{{principal: Principal{UserID: 42}}},
	}
	service, repository, clock := newTestService(t, password, nil, time.Minute)
	handle := mustCreate(t, service)
	completion, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	})
	if err != nil {
		t.Fatalf("CompletePassword() error = %v", err)
	}
	clock.Advance(time.Minute)

	_, err = service.Exchange(context.Background(), ExchangeInput{
		TransactionID: handle.TransactionID,
		ExchangeToken: completion.ExchangeToken,
	})
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("Exchange() error = %v, want ErrExpired", err)
	}
	record, err := repository.Get(context.Background(), handle.TransactionID)
	if err != nil {
		t.Fatalf("repository.Get() error = %v", err)
	}
	if record.State != StateExpired || record.ExchangeTokenHash != ([32]byte{}) {
		t.Fatalf("expired authenticated record retained exchange capability: %+v", record)
	}
}

func TestServiceDependencyAndEntropyFailures(t *testing.T) {
	if _, err := NewService(nil, nil, nil, Options{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewService(nil) error = %v, want ErrInvalidInput", err)
	}
	if _, err := NewService(NewMemoryRepository(), nil, nil, Options{TTL: -time.Second}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("NewService(negative TTL) error = %v, want ErrInvalidInput", err)
	}

	service, err := NewService(NewMemoryRepository(), nil, nil, Options{
		Clock:  &fakeClock{now: testStartTime},
		Random: failingReader{},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := service.Create(context.Background(), validCreateInput()); !errors.Is(err, ErrEntropyUnavailable) {
		t.Fatalf("Create() error = %v, want ErrEntropyUnavailable", err)
	}

	service, _, _ = newTestService(t, nil, nil, time.Minute)
	handle := mustCreate(t, service)
	if _, err := service.CompletePassword(context.Background(), PasswordCompletionInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
		Email:             "person@example.com",
		Password:          "password",
	}); !errors.Is(err, ErrAuthenticatorUnavailable) {
		t.Fatalf("CompletePassword() error = %v, want ErrAuthenticatorUnavailable", err)
	}
	if _, err := service.StartGoogle(context.Background(), GoogleStartInput{
		TransactionID:     handle.TransactionID,
		TransactionSecret: handle.TransactionSecret,
	}); !errors.Is(err, ErrAuthenticatorUnavailable) {
		t.Fatalf("StartGoogle() error = %v, want ErrAuthenticatorUnavailable", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}
