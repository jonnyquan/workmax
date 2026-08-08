//go:build desktop

package cloud_proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"runtime"
	"sync"
	"time"

	"server/desktop/buildinfo"
)

// OAuthFlowTimeout caps how long the sidecar waits for the user to
// complete the OAuth dance in the browser. 5 minutes is comfortable
// (sign in + 2FA + consent) and short enough that an abandoned
// flow doesn't tie up a port forever.
const OAuthFlowTimeout = 5 * time.Minute

// OAuthFlow orchestrates one OAuth Authorization Code + PKCE round
// trip. The lifecycle is:
//
//  1. Start()  — sidecar mints PKCE + state, spins up the loopback
//     callback server, and returns the authorize URL that the shelln Main
//     validates before handing it to the system browser.
//  2. Wait()   — blocks until the loopback server receives the
//     redirect, exchanges code → token, persists to
//     Keychain, returns success.
//
// One instance per concurrent flow. Multiple concurrent flows from
// the same user are not expected (one window, one user)
// but the design tolerates them via per-flow state.
type OAuthFlow struct {
	client     *Client
	tokenStore *TokenStore
	deviceID   string
	deviceInfo string
	random     io.Reader

	mu         sync.Mutex
	pending    *pendingFlow
	starting   bool
	generation uint64
}

type pendingFlow struct {
	generation   uint64
	waitClaimed  bool
	scope        string
	pkce         PKCEPair
	state        string
	loopback     *LoopbackCallbackServer
	authorizeURL string
	context      context.Context
	cancel       context.CancelFunc
}

// NewOAuthFlow constructs an orchestrator. `deviceID` comes from
// _local_meta (sidecar's persistent 32-char hex device id); passed
// in so the flow is decoupled from the SQLite layer.
func NewOAuthFlow(client *Client, store *TokenStore, deviceID string) *OAuthFlow {
	return &OAuthFlow{
		client:     client,
		tokenStore: store,
		deviceID:   deviceID,
		deviceInfo: defaultDeviceInfoJSON(),
		random:     DefaultRandom(),
	}
}

func defaultDeviceInfoJSON() string {
	raw, err := json.Marshal(map[string]string{
		"os":          runtime.GOOS,
		"app_version": buildinfo.Version,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

// StartResult is what the deferred-compatibility /auth/start route returns.
type StartResult struct {
	AuthorizeURL string // retained for a future reviewed system-browser handoff
	AuthPort     int    // loopback callback port (debug / display only)
	State        string // for logs; renderer doesn't need to use it
	generation   uint64 // opaque in-process handle binding Wait to this Start
}

// Start mints fresh PKCE+state, spins the loopback server, and builds the
// authorize URL. The bundled Renderer has no entry point for this legacy flow;
// a future caller must use a reviewed system-browser handoff.
//
// Concurrent Start() calls are rejected — the first one wins until
// the corresponding Wait() returns. Resetting requires Cancel().
func (f *OAuthFlow) Start(ctx context.Context, scope string) (StartResult, error) {
	f.mu.Lock()
	if f.pending != nil || f.starting {
		f.mu.Unlock()
		return StartResult{}, errors.New("oauth flow: another flow is already in progress (call Cancel first)")
	}
	if err := ctx.Err(); err != nil {
		f.mu.Unlock()
		return StartResult{}, err
	}
	f.starting = true
	f.generation++
	generation := f.generation
	f.mu.Unlock()

	committed := false
	defer func() {
		if committed {
			return
		}
		f.mu.Lock()
		if f.generation == generation {
			f.starting = false
		}
		f.mu.Unlock()
	}()

	pkce, err := GeneratePKCE(f.random)
	if err != nil {
		return StartResult{}, fmt.Errorf("oauth start: %w", err)
	}
	state, err := GenerateState(f.random)
	if err != nil {
		return StartResult{}, fmt.Errorf("oauth start: %w", err)
	}

	loopback, err := NewLoopbackCallbackServer()
	if err != nil {
		return StartResult{}, fmt.Errorf("oauth start: %w", err)
	}
	// Start immediately after binding so every later cleanup path can use the
	// server's idempotent Stop method; a listener that was never handed to
	// Serve would otherwise not be known to http.Server.Shutdown.
	loopback.Start()
	if err := ctx.Err(); err != nil {
		_ = loopback.Stop()
		return StartResult{}, err
	}

	if scope == "" {
		scope = "workagent"
	}
	authURL := f.buildAuthorizeURL(pkce, state, loopback.RedirectURI(), scope)

	f.mu.Lock()
	if f.generation != generation || !f.starting || ctx.Err() != nil {
		f.mu.Unlock()
		_ = loopback.Stop()
		if err := ctx.Err(); err != nil {
			return StartResult{}, err
		}
		return StartResult{}, context.Canceled
	}
	pendingContext, pendingCancel := context.WithCancel(context.Background())
	f.pending = &pendingFlow{
		generation:   generation,
		scope:        scope,
		pkce:         pkce,
		state:        state,
		loopback:     loopback,
		authorizeURL: authURL,
		context:      pendingContext,
		cancel:       pendingCancel,
	}
	f.starting = false
	committed = true
	f.mu.Unlock()

	return StartResult{
		AuthorizeURL: authURL,
		AuthPort:     loopback.Port(),
		State:        state,
		generation:   generation,
	}, nil
}

// Wait blocks until the OAuth callback fires (success or error) OR
// the flow times out via OAuthFlowTimeout. On success the TokenPair
// is persisted to the Keychain before Wait returns.
//
// Always finishes exactly the Start generation supplied by the caller on
// return (success or error), freeing that generation's loopback port.
func (f *OAuthFlow) Wait(ctx context.Context, start StartResult) (TokenPair, error) {
	f.mu.Lock()
	p := f.pending
	if p == nil || start.generation == 0 || p.generation != start.generation ||
		f.generation != start.generation {
		f.mu.Unlock()
		return TokenPair{}, errors.New("oauth wait: flow is no longer current")
	}
	if p.waitClaimed {
		f.mu.Unlock()
		return TokenPair{}, errors.New("oauth wait: flow already has a waiter")
	}
	p.waitClaimed = true
	f.mu.Unlock()

	flowCtx, cancel := context.WithTimeout(ctx, OAuthFlowTimeout)
	stopPendingCancellation := context.AfterFunc(p.context, cancel)
	defer stopPendingCancellation()
	defer cancel()
	defer f.finishPending(p)

	cb, err := p.loopback.Wait(flowCtx)
	if err != nil {
		return TokenPair{}, fmt.Errorf("oauth wait: %w", err)
	}

	// CSRF check: state in the callback must match what we sent.
	if cb.State != p.state {
		return TokenPair{}, fmt.Errorf("oauth wait: state mismatch (csrf protection triggered)")
	}

	if cb.ErrParam != "" {
		desc := cb.ErrDesc
		if desc == "" {
			desc = cb.ErrParam
		}
		return TokenPair{}, fmt.Errorf("oauth callback error: %s — %s", cb.ErrParam, desc)
	}

	if cb.Code == "" {
		return TokenPair{}, errors.New("oauth wait: callback missing code")
	}

	pair, err := f.client.ExchangeCodeForTokenForScope(
		flowCtx,
		cb.Code,
		p.loopback.RedirectURI(),
		p.pkce.Verifier,
		f.deviceID,
		f.deviceInfo,
		p.scope,
	)
	if err != nil {
		return TokenPair{}, fmt.Errorf("oauth wait: %w", err)
	}

	if err := f.commitSession(p, flowCtx, pair); err != nil {
		return TokenPair{}, fmt.Errorf("oauth wait: persist: %w", err)
	}
	return pair, nil
}

// commitSession makes the legacy OAuth save linearizable with Cancel. This is
// important while /auth/start remains as a deferred compatibility route:
// logout or the newer Login Transaction flow must either cancel before the
// Keychain write (zero late write) or observe a completed write and clear/
// replace it afterwards.
func (f *OAuthFlow) commitSession(
	pending *pendingFlow,
	flowContext context.Context,
	pair TokenPair,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if pending == nil || f.pending != pending || f.generation != pending.generation ||
		pending.context.Err() != nil || flowContext.Err() != nil {
		return context.Canceled
	}
	return f.tokenStore.Save(pair)
}

// finishPending releases exactly the generation observed by Wait. A canceled
// Wait may finish after another caller has already started a replacement flow;
// it must never use the broad Cancel operation and tear that newer flow down.
func (f *OAuthFlow) finishPending(pending *pendingFlow) {
	if pending == nil {
		return
	}
	f.mu.Lock()
	if f.pending == pending && f.generation == pending.generation {
		f.generation++
		f.pending = nil
	}
	f.mu.Unlock()
	if pending.cancel != nil {
		pending.cancel()
	}
	if pending.loopback != nil {
		_ = pending.loopback.Stop()
	}
}

// Cancel tears down any in-progress flow (stops the loopback server,
// clears pending state). Idempotent.
func (f *OAuthFlow) Cancel() {
	f.mu.Lock()
	f.generation++
	f.starting = false
	p := f.pending
	f.pending = nil
	f.mu.Unlock()
	if p != nil {
		if p.cancel != nil {
			p.cancel()
		}
		if p.loopback != nil {
			_ = p.loopback.Stop()
		}
	}
}

// buildAuthorizeURL composes the GET URL the renderer should load.
// Matches the wire shape backend P-1.4 parses (response_type=code,
// PKCE S256, etc).
func (f *OAuthFlow) buildAuthorizeURL(pkce PKCEPair, state, redirectURI, scope string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.client.ClientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {pkce.Method},
		"state":                 {state},
		"scope":                 {scope},
	}
	return f.client.BaseURL + CloudRouteOAuthAuthorize + "?" + q.Encode()
}
