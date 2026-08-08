//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	loginTransactionCoordinatorMaxTTL      = 15 * time.Minute
	loginTransactionCoordinatorFlowIDBytes = 32
)

// LoginTransactionCoordinatorState is deliberately non-sensitive. It is safe
// for a future Sidecar HTTP adapter to expose, unlike transaction IDs,
// capabilities, OAuth state, PKCE material, callback ports, codes or tokens.
type LoginTransactionCoordinatorState string

const (
	LoginTransactionCoordinatorStateIdle       LoginTransactionCoordinatorState = "idle"
	LoginTransactionCoordinatorStateStarting   LoginTransactionCoordinatorState = "starting"
	LoginTransactionCoordinatorStatePending    LoginTransactionCoordinatorState = "pending"
	LoginTransactionCoordinatorStateCompleting LoginTransactionCoordinatorState = "completing"
	LoginTransactionCoordinatorStateComplete   LoginTransactionCoordinatorState = "complete"
)

// LoginTransactionCoordinatorSnapshot is the entire public state surface of
// the coordinator. It intentionally excludes transaction_id, loopback port,
// redirect URI, OAuth state, PKCE verifier/challenge and every credential.
type LoginTransactionCoordinatorSnapshot struct {
	State     LoginTransactionCoordinatorState `json:"state"`
	ExpiresAt time.Time                        `json:"expires_at,omitempty"`
	Methods   []string                         `json:"methods,omitempty"`
}

var (
	ErrLoginTransactionCoordinatorInvalidInput       = errors.New("desktop login coordinator: invalid input")
	ErrLoginTransactionCoordinatorBusy               = errors.New("desktop login coordinator: another operation is active")
	ErrLoginTransactionCoordinatorIdle               = errors.New("desktop login coordinator: no pending transaction")
	ErrLoginTransactionCoordinatorInvalidCredentials = errors.New(
		"desktop login coordinator: credentials were rejected",
	)
	ErrLoginTransactionCoordinatorTerminal = errors.New(
		"desktop login coordinator: transaction is expired or terminal",
	)
	ErrLoginTransactionCoordinatorUnavailable = errors.New(
		"desktop login coordinator: service unavailable",
	)
	ErrLoginTransactionCoordinatorCanceled = errors.New("desktop login coordinator: operation canceled")
)

// LoginTransactionCoordinator owns the sensitive Desktop password-login
// lifecycle. It is a core object only: Sidecar HTTP and Electron adapters are
// intentionally outside this slice.
//
// One instance admits exactly one Start/Complete flow at a time. All bearer
// capabilities, OAuth state, PKCE verifier, authorization code and token pair
// remain private to this object and are discarded when the flow terminates.
type LoginTransactionCoordinator struct {
	client     *Client
	tokenStore *TokenStore
	deviceID   string
	deviceInfo string
	random     io.Reader
	now        func() time.Time

	mu               sync.Mutex
	generation       uint64
	state            LoginTransactionCoordinatorState
	startCancel      context.CancelFunc
	startingFlowID   string
	startingLoopback *LoopbackCallbackServer
	pending          *loginTransactionCoordinatorPending
}

type loginTransactionCoordinatorPending struct {
	generation uint64
	flowID     string
	client     Client
	tokenStore *TokenStore
	deviceID   string
	deviceInfo string
	scope      string
	handle     LoginTransactionHandle
	pkce       PKCEPair
	state      string
	redirect   string
	loopback   *LoopbackCallbackServer
	context    context.Context
	cancel     context.CancelFunc
}

// NewLoginTransactionCoordinator constructs the password-login core. The
// device ID is the persistent, canonical 32-character lowercase hex ID owned
// by the Sidecar. Configuration is validated before Start reaches the network.
func NewLoginTransactionCoordinator(
	client *Client,
	tokenStore *TokenStore,
	deviceID string,
) *LoginTransactionCoordinator {
	return &LoginTransactionCoordinator{
		client:     client,
		tokenStore: tokenStore,
		deviceID:   deviceID,
		deviceInfo: defaultDeviceInfoJSON(),
		random:     DefaultRandom(),
		now:        time.Now,
		state:      LoginTransactionCoordinatorStateIdle,
	}
}

// Snapshot returns a defensive, non-sensitive view of the current lifecycle.
func (c *LoginTransactionCoordinator) Snapshot() LoginTransactionCoordinatorSnapshot {
	if c == nil {
		return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateIdle}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

// StartPassword creates a Server-owned Login Transaction bound to a freshly
// allocated, actually listening 127.0.0.1 callback, fresh OAuth state/PKCE,
// and the coordinator's frozen device and scope. The returned snapshot is safe
// to expose; the transaction handle and callback details never leave the core.
func (c *LoginTransactionCoordinator) StartPassword(
	ctx context.Context,
	scope string,
	flowID string,
) (result LoginTransactionCoordinatorSnapshot, resultErr error) {
	if c == nil || ctx == nil {
		return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateIdle},
			ErrLoginTransactionCoordinatorInvalidInput
	}
	if ctx.Err() != nil {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if !ValidLoginTransactionLocalFlowID(flowID) {
		return c.Snapshot(), ErrLoginTransactionCoordinatorInvalidInput
	}
	if scope == "" {
		scope = "workagent"
	}
	client, tokenStore, deviceID, deviceInfo, random, now, ok := c.startConfiguration(scope)
	if !ok {
		return c.Snapshot(), ErrLoginTransactionCoordinatorInvalidInput
	}

	c.mu.Lock()
	if c.state != LoginTransactionCoordinatorStateIdle || c.pending != nil || c.startCancel != nil {
		result = c.snapshotLocked()
		c.mu.Unlock()
		return result, ErrLoginTransactionCoordinatorBusy
	}
	c.generation++
	generation := c.generation
	startContext, startCancel := context.WithCancel(ctx)
	c.startCancel = startCancel
	c.startingFlowID = flowID
	c.state = LoginTransactionCoordinatorStateStarting
	c.mu.Unlock()

	committed := false
	var loopback *LoopbackCallbackServer
	defer func() {
		startCancel()
		if committed {
			return
		}
		if loopback != nil {
			stopLoginTransactionCoordinatorLoopback(loopback)
		}
		c.abandonStart(generation)
		if resultErr != nil {
			result = c.Snapshot()
		}
	}()

	pkce, err := GeneratePKCE(random)
	if err != nil {
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorUnavailable
	}
	oauthState, err := GenerateState(random)
	if err != nil {
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorUnavailable
	}
	loopback, err = NewLoopbackCallbackServer()
	if err != nil {
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorUnavailable
	}
	loopback.Start()

	c.mu.Lock()
	if c.generation != generation || c.state != LoginTransactionCoordinatorStateStarting || startContext.Err() != nil {
		c.mu.Unlock()
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorCanceled
	}
	c.startingLoopback = loopback
	c.mu.Unlock()

	handle, err := client.CreateLoginTransaction(startContext, LoginTransactionCreateInput{
		DeviceID:            deviceID,
		RedirectURI:         loopback.RedirectURI(),
		OAuthState:          oauthState,
		CodeChallenge:       pkce.Challenge,
		CodeChallengeMethod: pkce.Method,
		Scope:               scope,
	})
	if err != nil {
		if !c.startIsCurrent(generation) || startContext.Err() != nil {
			return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorCanceled
		}
		return LoginTransactionCoordinatorSnapshot{}, mapLoginTransactionCoordinatorStartError(err)
	}
	currentTime := now().UTC()
	pendingTTL := handle.ExpiresAt.Sub(currentTime)
	if pendingTTL <= 0 || pendingTTL > loginTransactionCoordinatorMaxTTL ||
		!loginTransactionMethodAvailable(handle.Methods, LoginTransactionMethodPassword) {
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorTerminal
	}

	// The pending transaction owns a real local expiry timer. This releases the
	// loopback listener and returns the coordinator to idle even when the
	// Renderer disappears and never submits or explicitly cancels.
	pendingContext, pendingCancel := context.WithTimeout(context.Background(), pendingTTL)
	pending := &loginTransactionCoordinatorPending{
		generation: generation,
		flowID:     flowID,
		client:     client,
		tokenStore: tokenStore,
		deviceID:   deviceID,
		deviceInfo: deviceInfo,
		scope:      scope,
		handle: LoginTransactionHandle{
			TransactionID:     handle.TransactionID,
			TransactionSecret: handle.TransactionSecret,
			ExpiresAt:         handle.ExpiresAt.UTC(),
			// Google is intentionally outside this coordinator slice. Do not
			// advertise a Server method the Desktop core cannot complete yet.
			Methods: []string{LoginTransactionMethodPassword},
		},
		pkce:     pkce,
		state:    oauthState,
		redirect: loopback.RedirectURI(),
		loopback: loopback,
		context:  pendingContext,
		cancel:   pendingCancel,
	}

	c.mu.Lock()
	if c.generation != generation || c.state != LoginTransactionCoordinatorStateStarting ||
		c.startingFlowID != flowID ||
		startContext.Err() != nil || pendingContext.Err() != nil {
		c.mu.Unlock()
		pendingCancel()
		if errors.Is(pendingContext.Err(), context.DeadlineExceeded) {
			return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorTerminal
		}
		return LoginTransactionCoordinatorSnapshot{}, ErrLoginTransactionCoordinatorCanceled
	}
	c.pending = pending
	c.startCancel = nil
	c.startingFlowID = ""
	c.startingLoopback = nil
	c.state = LoginTransactionCoordinatorStatePending
	result = c.snapshotLocked()
	committed = true
	c.mu.Unlock()
	go c.expirePendingWhenDone(pending)
	return result, nil
}

// CompletePassword consumes only the current pending transaction. It performs
// one password request, one capability exchange, one authorization-code token
// exchange and one Keychain save, in that order. It never retries a password
// or an ambiguous operation and returns no credential-bearing value.
func (c *LoginTransactionCoordinator) CompletePassword(
	ctx context.Context,
	flowID string,
	email string,
	password string,
) (LoginTransactionCoordinatorSnapshot, error) {
	if c == nil || ctx == nil {
		return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateIdle},
			ErrLoginTransactionCoordinatorInvalidInput
	}
	if !ValidLoginTransactionLocalFlowID(flowID) ||
		!validLoginTransactionCoordinatorCredentials(email, password) {
		return c.Snapshot(), ErrLoginTransactionCoordinatorInvalidInput
	}
	if ctx.Err() != nil {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}

	c.mu.Lock()
	if c.state == LoginTransactionCoordinatorStateStarting {
		result := c.snapshotLocked()
		matches := c.startingFlowID == flowID
		c.mu.Unlock()
		if !matches {
			return result, ErrLoginTransactionCoordinatorInvalidInput
		}
		return result, ErrLoginTransactionCoordinatorBusy
	}
	pending := c.pending
	if pending == nil || c.state == LoginTransactionCoordinatorStateIdle {
		result := c.snapshotLocked()
		c.mu.Unlock()
		return result, ErrLoginTransactionCoordinatorIdle
	}
	if pending.flowID != flowID {
		result := c.snapshotLocked()
		c.mu.Unlock()
		return result, ErrLoginTransactionCoordinatorInvalidInput
	}
	if c.state == LoginTransactionCoordinatorStateCompleting {
		result := c.snapshotLocked()
		c.mu.Unlock()
		return result, ErrLoginTransactionCoordinatorBusy
	}
	if !pending.handle.ExpiresAt.After(c.now().UTC()) {
		generation := pending.generation
		c.mu.Unlock()
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorTerminal)
	}
	generation := pending.generation
	c.state = LoginTransactionCoordinatorStateCompleting
	c.mu.Unlock()

	operationContext, operationCancel := context.WithCancel(ctx)
	stopPendingCancellation := context.AfterFunc(pending.context, operationCancel)
	defer stopPendingCancellation()
	defer operationCancel()

	completion, err := pending.client.CompleteLoginTransactionPassword(
		operationContext,
		LoginTransactionPasswordInput{
			TransactionID:     pending.handle.TransactionID,
			TransactionSecret: pending.handle.TransactionSecret,
			Email:             email,
			Password:          password,
		},
	)
	if err != nil {
		return c.handlePasswordError(pending, generation, operationContext, err)
	}
	if !c.pendingIsCurrent(pending, generation) {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if operationContext.Err() != nil {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorCanceled)
	}
	if !completion.ExpiresAt.After(c.now().UTC()) {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorTerminal)
	}

	authorization, err := pending.client.ExchangeLoginTransaction(
		operationContext,
		LoginTransactionExchangeInput{
			TransactionID:       pending.handle.TransactionID,
			ExchangeToken:       completion.ExchangeToken,
			ExpectedRedirectURI: pending.redirect,
			ExpectedOAuthState:  pending.state,
		},
	)
	completion.ExchangeToken = ""
	if err != nil {
		return c.handleTerminalStageError(pending, generation, operationContext, err)
	}
	if !c.pendingIsCurrent(pending, generation) {
		authorization.Code = ""
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if operationContext.Err() != nil {
		authorization.Code = ""
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorCanceled)
	}

	pair, err := pending.client.exchangeLoginTransactionCodeForToken(
		operationContext,
		authorization.Code,
		pending.redirect,
		pending.pkce.Verifier,
		pending.deviceID,
		pending.deviceInfo,
		c.now().UTC(),
	)
	authorization.Code = ""
	if err != nil {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorUnavailable)
	}
	// The Server froze scope when the transaction was created. A token response
	// that changes it is a protocol failure, even if the new scope is otherwise
	// syntactically valid; never persist a broader or different session than the
	// one the coordinator requested.
	if pair.Scope != pending.scope {
		pair = TokenPair{}
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorUnavailable)
	}
	current, canceled, expired, saveFailed := c.commitSession(pending, generation, operationContext, pair)
	pair = TokenPair{}
	if !current {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if canceled {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if expired {
		return c.Snapshot(), ErrLoginTransactionCoordinatorTerminal
	}
	if saveFailed {
		return c.Snapshot(), ErrLoginTransactionCoordinatorUnavailable
	}
	// Completion is returned as a one-shot safe result. With no active
	// transaction left, subsequent Snapshot calls intentionally report idle.
	return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateComplete}, nil
}

// Cancel cancels Start/Complete, clears all pending capabilities and stops the
// loopback listener. It is idempotent. A response that raced cancellation is
// never replayed or reinstalled as pending state.
func (c *LoginTransactionCoordinator) Cancel() {
	if c == nil {
		return
	}
	c.cancelFlow("", false)
}

// CancelFlow cancels only the active flow identified by flowID. It is
// idempotent while idle, but a stale or unrelated flow can never cancel a
// newer starting, pending, or completing transaction.
func (c *LoginTransactionCoordinator) CancelFlow(
	flowID string,
) (LoginTransactionCoordinatorSnapshot, error) {
	if c == nil || !ValidLoginTransactionLocalFlowID(flowID) {
		return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateIdle},
			ErrLoginTransactionCoordinatorInvalidInput
	}
	return c.cancelFlow(flowID, true)
}

func (c *LoginTransactionCoordinator) cancelFlow(
	flowID string,
	exact bool,
) (LoginTransactionCoordinatorSnapshot, error) {
	c.mu.Lock()
	if exact {
		activeFlowID := ""
		switch {
		case c.state == LoginTransactionCoordinatorStateStarting:
			activeFlowID = c.startingFlowID
		case c.pending != nil:
			activeFlowID = c.pending.flowID
		case c.state == LoginTransactionCoordinatorStateIdle:
			result := c.snapshotLocked()
			c.mu.Unlock()
			return result, nil
		}
		if activeFlowID == "" || activeFlowID != flowID {
			result := c.snapshotLocked()
			c.mu.Unlock()
			return result, ErrLoginTransactionCoordinatorInvalidInput
		}
	}
	c.generation++
	startCancel := c.startCancel
	startingLoopback := c.startingLoopback
	pending := c.pending
	c.startCancel = nil
	c.startingFlowID = ""
	c.startingLoopback = nil
	c.pending = nil
	c.state = LoginTransactionCoordinatorStateIdle
	c.mu.Unlock()

	if startCancel != nil {
		startCancel()
	}
	if pending != nil && pending.cancel != nil {
		pending.cancel()
	}
	if startingLoopback != nil {
		stopLoginTransactionCoordinatorLoopback(startingLoopback)
	}
	if pending != nil && pending.loopback != nil && pending.loopback != startingLoopback {
		stopLoginTransactionCoordinatorLoopback(pending.loopback)
	}
	return LoginTransactionCoordinatorSnapshot{State: LoginTransactionCoordinatorStateIdle}, nil
}

// ValidLoginTransactionLocalFlowID accepts only the canonical unpadded
// base64url encoding of exactly 32 bytes. The value is a Main/Sidecar-local
// generation binding and is never part of a public coordinator snapshot.
func ValidLoginTransactionLocalFlowID(flowID string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(flowID)
	if err != nil || len(decoded) != loginTransactionCoordinatorFlowIDBytes {
		return false
	}
	return base64.RawURLEncoding.EncodeToString(decoded) == flowID
}

func (c *LoginTransactionCoordinator) startConfiguration(
	scope string,
) (Client, *TokenStore, string, string, io.Reader, func() time.Time, bool) {
	if c.client == nil || c.tokenStore == nil || c.random == nil || c.now == nil ||
		!validLoginTransactionDeviceID(c.deviceID) || !validCanonicalLoginScope(scope) ||
		!validLoginTransactionDeviceInfo(c.deviceInfo) {
		return Client{}, nil, "", "", nil, nil, false
	}
	client := *c.client
	if _, err := client.validateLoginTransactionClient("coordinator"); err != nil {
		return Client{}, nil, "", "", nil, nil, false
	}
	return client, c.tokenStore, c.deviceID, c.deviceInfo, c.random, c.now, true
}

func (c *LoginTransactionCoordinator) snapshotLocked() LoginTransactionCoordinatorSnapshot {
	state := c.state
	if state == "" {
		state = LoginTransactionCoordinatorStateIdle
	}
	result := LoginTransactionCoordinatorSnapshot{State: state}
	if c.pending != nil {
		result.ExpiresAt = c.pending.handle.ExpiresAt.UTC()
		result.Methods = append([]string(nil), c.pending.handle.Methods...)
	}
	return result
}

func (c *LoginTransactionCoordinator) startIsCurrent(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation == generation && c.state == LoginTransactionCoordinatorStateStarting
}

func (c *LoginTransactionCoordinator) abandonStart(generation uint64) {
	c.mu.Lock()
	if c.generation == generation && c.state == LoginTransactionCoordinatorStateStarting {
		c.startCancel = nil
		c.startingFlowID = ""
		c.startingLoopback = nil
		c.state = LoginTransactionCoordinatorStateIdle
	}
	c.mu.Unlock()
}

func (c *LoginTransactionCoordinator) pendingIsCurrent(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation == generation && c.pending == pending &&
		c.state == LoginTransactionCoordinatorStateCompleting
}

func (c *LoginTransactionCoordinator) restorePending(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
) (LoginTransactionCoordinatorSnapshot, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.pending != pending ||
		c.state != LoginTransactionCoordinatorStateCompleting {
		return c.snapshotLocked(), false
	}
	c.state = LoginTransactionCoordinatorStatePending
	return c.snapshotLocked(), true
}

func (c *LoginTransactionCoordinator) detachPending(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation || c.pending != pending {
		return false
	}
	c.generation++
	c.pending = nil
	c.state = LoginTransactionCoordinatorStateIdle
	return true
}

func (c *LoginTransactionCoordinator) terminatePending(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
	err error,
) (LoginTransactionCoordinatorSnapshot, error) {
	current := c.detachPending(pending, generation)
	cleanupLoginTransactionCoordinatorPending(pending)
	if !current {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	return c.Snapshot(), err
}

// commitSession is the success linearization fence. The generation check,
// non-context-aware Keychain write and pending detach deliberately happen
// under the coordinator mutex: either Cancel clears the generation before this
// lock is acquired (zero late writes), or this commit wins and Cancel observes
// an already-completed idle coordinator after the write.
func (c *LoginTransactionCoordinator) commitSession(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
	operationContext context.Context,
	pair TokenPair,
) (current bool, canceled bool, expired bool, saveFailed bool) {
	c.mu.Lock()
	if c.generation != generation || c.pending != pending ||
		c.state != LoginTransactionCoordinatorStateCompleting {
		c.mu.Unlock()
		return false, false, false, false
	}
	pendingContextErr := error(nil)
	if pending.context != nil {
		pendingContextErr = pending.context.Err()
	}
	deadlineExpired := !pending.handle.ExpiresAt.After(c.now().UTC())
	if operationContext.Err() != nil || pendingContextErr != nil || deadlineExpired {
		c.generation++
		c.pending = nil
		c.state = LoginTransactionCoordinatorStateIdle
		c.mu.Unlock()
		cleanupLoginTransactionCoordinatorPending(pending)
		if errors.Is(pendingContextErr, context.DeadlineExceeded) || deadlineExpired {
			return true, false, true, false
		}
		return true, true, false, false
	}
	err := pending.tokenStore.Save(pair)
	c.generation++
	c.pending = nil
	c.state = LoginTransactionCoordinatorStateIdle
	c.mu.Unlock()
	cleanupLoginTransactionCoordinatorPending(pending)
	return true, false, false, err != nil
}

func cleanupLoginTransactionCoordinatorPending(pending *loginTransactionCoordinatorPending) {
	if pending == nil {
		return
	}
	if pending.cancel != nil {
		pending.cancel()
	}
	if pending.loopback != nil {
		stopLoginTransactionCoordinatorLoopback(pending.loopback)
	}
}

func (c *LoginTransactionCoordinator) expirePendingWhenDone(
	pending *loginTransactionCoordinatorPending,
) {
	if c == nil || pending == nil || pending.context == nil {
		return
	}
	<-pending.context.Done()
	if !errors.Is(pending.context.Err(), context.DeadlineExceeded) {
		return
	}
	if c.detachPending(pending, pending.generation) {
		cleanupLoginTransactionCoordinatorPending(pending)
	}
}

func stopLoginTransactionCoordinatorLoopback(loopback *LoopbackCallbackServer) {
	if loopback == nil {
		return
	}
	_ = loopback.Stop()
	// Start launches Serve asynchronously. Closing the listener as a final,
	// idempotent fence also covers cancellation in the tiny interval before
	// Serve has registered it with http.Server.Shutdown.
	if loopback.listener != nil {
		_ = loopback.listener.Close()
	}
}

func (c *LoginTransactionCoordinator) handlePasswordError(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
	operationContext context.Context,
	err error,
) (LoginTransactionCoordinatorSnapshot, error) {
	if !c.pendingIsCurrent(pending, generation) {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if operationContext.Err() != nil {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorCanceled)
	}
	var serverError *LoginTransactionServerError
	if errors.As(err, &serverError) {
		switch serverError.Code {
		case "invalid_credentials":
			if snapshot, restored := c.restorePending(pending, generation); restored {
				return snapshot, ErrLoginTransactionCoordinatorInvalidCredentials
			}
			return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
		case "rate_limited":
			if snapshot, restored := c.restorePending(pending, generation); restored {
				return snapshot, ErrLoginTransactionCoordinatorUnavailable
			}
			return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
		case "identity_unavailable":
			return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorUnavailable)
		default:
			return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorTerminal)
		}
	}
	// A transport/protocol failure can arrive after the Server consumed the
	// password and minted an exchange capability. The outcome is unavailable,
	// and replaying it is forbidden; it is not misreported as an expiry.
	return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorUnavailable)
}

func (c *LoginTransactionCoordinator) handleTerminalStageError(
	pending *loginTransactionCoordinatorPending,
	generation uint64,
	operationContext context.Context,
	err error,
) (LoginTransactionCoordinatorSnapshot, error) {
	if !c.pendingIsCurrent(pending, generation) {
		return c.Snapshot(), ErrLoginTransactionCoordinatorCanceled
	}
	if operationContext.Err() != nil {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorCanceled)
	}
	var serverError *LoginTransactionServerError
	if errors.As(err, &serverError) &&
		(serverError.Code == "rate_limited" || serverError.Code == "identity_unavailable") {
		return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorUnavailable)
	}
	return c.terminatePending(pending, generation, ErrLoginTransactionCoordinatorTerminal)
}

func mapLoginTransactionCoordinatorStartError(err error) error {
	if errors.Is(err, ErrLoginTransactionInvalidInput) {
		return ErrLoginTransactionCoordinatorInvalidInput
	}
	var serverError *LoginTransactionServerError
	if errors.As(err, &serverError) {
		if serverError.Code == "transaction_expired" || serverError.Code == "transaction_complete" {
			return ErrLoginTransactionCoordinatorTerminal
		}
		return ErrLoginTransactionCoordinatorUnavailable
	}
	return ErrLoginTransactionCoordinatorUnavailable
}

func loginTransactionMethodAvailable(methods []string, wanted string) bool {
	for _, method := range methods {
		if method == wanted {
			return true
		}
	}
	return false
}

func validLoginTransactionCoordinatorCredentials(email string, password string) bool {
	return validBoundedLoginText(email, 3, 320) && strings.Contains(email, "@") &&
		len(password) > 0 && len(password) <= 1024 && utf8.ValidString(password) &&
		!hasLoginControlExceptWhitespace(password)
}

func validLoginTransactionDeviceID(deviceID string) bool {
	if len(deviceID) != 32 || deviceID != strings.ToLower(deviceID) {
		return false
	}
	_, err := hex.DecodeString(deviceID)
	return err == nil
}
