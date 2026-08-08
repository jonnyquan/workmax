//go:build desktop

package cloud_proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gorm.io/gorm"
)

// ChatRequest is what the sidecar accepts from the renderer at
// POST /agent/chat and forwards (essentially as-is) to workmax.app.
//
// We don't enforce a fixed JSON shape here — the renderer's request
// body is opaque to the relay; we just forward it. The structured
// metadata (thread_id, mode, etc.) we DO care about lives in the
// X-WorkMax-* header set or in a small typed envelope the sidecar
// extracts before forwarding.
type ChatRequest struct {
	// ThreadID + ThreadUUID identify the local cache row to write into.
	// Required — without them the cache_writer can't persist.
	ThreadID   uint64
	ThreadUUID string
	// TurnUUID is the renderer-minted canonical v4 UUID for this immutable
	// user intent. It becomes the stable legacy Cloud idempotency key on the
	// first request, a 401 retry, and an explicit local recovery replay.
	TurnUUID string
	UID      uint64
	// ExpectedLease, when non-zero, prevents a request admitted by the local
	// handler from migrating onto a replacement login (including same-UID
	// re-login) while it queues behind token refresh.
	ExpectedLease SessionLease

	// UserText is the prompt the user typed. Stored verbatim in the
	// cache row's user_text column so offline browsing can show the
	// turn without re-running the model.
	UserText string

	// ChatMode is the workagent skill mode (e.g. "ppt"). Cached on the
	// row for filtering in offline browse.
	ChatMode string

	// Body is the raw JSON payload to forward to workmax.app. The relay
	// doesn't inspect it — the production backend understands its shape.
	Body []byte

	// FileIDs references uploaded local attachments (w_workagent_thread_file.id).
	// Local route only: the Engine resolves them via AttachmentLoader into model
	// context (image base64, document extracted text). Cloud route ignores it.
	FileIDs []int64
}

// ErrChatSessionChanged is the closed local result for a chat request whose
// frozen local-history owner no longer matches the access token that would be
// sent upstream. It deliberately carries no token, UID, or persistence detail.
var ErrChatSessionChanged = errors.New("chat: session changed")

// Proxy is the sidecar's chat-relay component. One instance per
// sidecar process. Safe for concurrent Chat() calls.
type Proxy struct {
	cloud      *Client
	tokenStore *TokenStore
	db         *gorm.DB

	// HTTPClient is the *long-lived* client for SSE-bearing calls.
	// Distinct from cloud.HTTPClient (which has a 10s overall timeout
	// suited to /token exchange) — SSE turns live for minutes.
	HTTPClient *http.Client
}

// CloudClient returns the underlying Client (the snug-timeout HTTP
// client suitable for short JSON calls like the skills catalog
// lookup). The chat relay uses Proxy.HTTPClient (no-timeout); this
// accessor is for non-streaming sidecar handlers that need the
// short-lived client.
func (p *Proxy) CloudClient() *Client { return p.cloud }

// NewProxy wires the proxy. Production callers pass a fresh Client
// (configured with BaseURL pointing at workmax.app) + the shared
// TokenStore. The internal SSE HTTP client uses no overall timeout
// (only per-read deadlines on the upstream body).
func NewProxy(cloud *Client, tokenStore *TokenStore, db *gorm.DB) *Proxy {
	return &Proxy{
		cloud:      cloud,
		tokenStore: tokenStore,
		db:         db,
		HTTPClient: &http.Client{
			// No overall timeout — SSE responses live for the duration
			// of a chat turn (potentially minutes). Idle-detection is
			// handled by SSE keepalive comments at the protocol level,
			// not by HTTP timeouts.
			Timeout: 0,
		},
	}
}

// Chat is the relay entry point. The flow:
//  1. Resolve access token (with one auto-refresh if needed).
//  2. POST the request body to the configured Go Server, capturing SSE.
//  3. Pipe upstream → downstream + cache, with cancellation.
//  4. Finalize the cache row (complete | partial).
//
// Returns nil on a clean turn (got `done` event from upstream).
// Returns a non-nil error on any failure path — caller logs it but
// the renderer already received a proxy_error SSE event detailing
// what happened.
func (p *Proxy) Chat(ctx context.Context, req ChatRequest, dst SSEWriter) error {
	if req.UID == 0 {
		return p.emitSessionChanged(dst)
	}
	requestID, err := DesktopTurnRequestID(req.TurnUUID)
	if err != nil {
		return p.emitErrorAndClose(dst, ProxyError{
			Kind:    KindBadRequest,
			Message: "invalid turn_uuid",
		}, nil)
	}
	if req.ExpectedLease.Epoch() != 0 {
		if err := req.ExpectedLease.Check(); err != nil {
			return p.emitSessionChanged(dst)
		}
	}
	// 1. Build the cache writer up front so even pre-network failures
	//    have a partial-row to point at. We don't actually INSERT
	//    until the first event arrives, so a failed-before-first-event
	//    turn leaves no row behind (cache_writer's lazy-INSERT).
	cache, err := NewCacheWriter(p.db, CacheWriterParams{
		UID:                   req.UID,
		ThreadID:              req.ThreadID,
		ThreadUUID:            req.ThreadUUID,
		MessageIdempotencyKey: requestID,
		UserText:              req.UserText,
		ChatMode:              req.ChatMode,
	})
	if err != nil {
		return p.emitErrorAndClose(dst, ProxyError{
			Kind:    KindBadRequest,
			Message: err.Error(),
		}, nil)
	}
	// Finalize runs no matter what — partial flag is set based on
	// whether the relay returned an error.
	var pipeErr error
	defer func() {
		_ = cache.Finalize(pipeErr)
	}()

	// 2. Resolve access token (refresh if needed).
	token, lease, refreshErr := p.acquireToken(ctx)
	if refreshErr != nil {
		if errors.Is(refreshErr, ErrSessionChanged) {
			pipeErr = ErrChatSessionChanged
			return p.emitSessionChanged(dst)
		}
		pipeErr = refreshErr
		return p.emitErrorAndClose(dst, ClassifyTokenStoreError(refreshErr), cache)
	}
	leaseCtx, releaseLease := lease.BindContext(ctx)
	defer releaseLease()
	if req.ExpectedLease.Epoch() != 0 && !lease.SameSession(req.ExpectedLease) {
		pipeErr = ErrChatSessionChanged
		return p.emitSessionChanged(dst)
	}
	if err := lease.Check(); err != nil {
		pipeErr = ErrChatSessionChanged
		return p.emitSessionChanged(dst)
	}
	if !chatTokenMatchesUID(token, req.UID) {
		pipeErr = ErrChatSessionChanged
		return p.emitSessionChanged(dst)
	}

	// 3. Upstream POST. Use the chat-specific HTTP client (no overall
	//    timeout); upstream returns 200 + text/event-stream OR an
	//    error status with a JSON body.
	upstreamReq, err := p.buildUpstreamRequest(leaseCtx, req, token.AccessToken)
	if err != nil {
		pipeErr = err
		return p.emitErrorAndClose(dst, ProxyError{
			Kind:    KindUnknown,
			Message: err.Error(),
		}, cache)
	}
	resp, err := cloneCredentialHTTPClient(p.HTTPClient).Do(upstreamReq)
	if err != nil {
		if chatLeaseSessionChanged(leaseCtx, lease, err) {
			pipeErr = ErrChatSessionChanged
			return p.emitSessionChanged(dst)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Caller cancelled before we even got a response. No need
			// to bother the renderer with a proxy_error — they triggered
			// the cancel themselves.
			pipeErr = err
			return err
		}
		pipeErr = err
		return p.emitErrorAndClose(dst, ClassifyNetworkError(err), cache)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Read upstream body for error classification (capped at 8 KiB
		// so a misbehaving upstream can't OOM us).
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if chatLeaseSessionChanged(leaseCtx, lease, nil) {
			pipeErr = ErrChatSessionChanged
			return p.emitSessionChanged(dst)
		}
		// 401 with refresh-token still valid → try one rotation +
		// re-issue. (Token might have been revoked mid-flight.)
		if resp.StatusCode == http.StatusUnauthorized {
			if pe, ok := p.tryAuthRecover(leaseCtx, req, token, lease, dst, cache); ok {
				pipeErr = pe // could still be non-nil if rotation also failed
				return pe
			}
		}
		pe := ClassifyHTTPResponse(resp, body)
		pipeErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
		return p.emitErrorAndClose(dst, pe, cache)
	}

	// 4. Pipe SSE → renderer + cache. PipeUpstream returns nil only
	//    after a terminal done event, ctx.Err() on cancellation, or a
	//    stream/truncation error otherwise.
	if err := PipeUpstream(leaseCtx, resp.Body, dst, cache); err != nil {
		pipeErr = err
		if chatLeaseSessionChanged(leaseCtx, lease, err) {
			pipeErr = ErrChatSessionChanged
			return p.emitSessionChanged(dst)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Renderer-initiated close. Don't emit a proxy_error;
			// they already know. Cache row is finalized partial.
			return err
		}
		return p.emitErrorAndClose(dst, ClassifyUpstreamStreamError(err), nil)
	}
	// A successfully forwarded done frame is the terminal linearization point.
	// Do not reinterpret that already-visible success as session_changed if a
	// replacement login races immediately after the downstream write.
	return nil
}

// acquireToken returns a non-expired access token, refreshing if
// needed. Thin delegating wrapper around the public
// AcquireAccessToken helper (access_token.go) — kept as a method
// because Chat() reads through it and the receiver bundles cloud +
// store cleanly. The helper version is for callers that don't
// already own a Proxy (e.g. the sync worker).
func (p *Proxy) acquireToken(ctx context.Context) (*TokenPair, SessionLease, error) {
	return AcquireAccessTokenWithLease(ctx, p.tokenStore, p.cloud)
}

// tryAuthRecover handles the rare "upstream 401 despite local token
// being unexpired" path. Forces a refresh + reissues the request.
// Returns (err, ok) — ok=false means caller should fall through to
// the normal HTTP error path.
func (p *Proxy) tryAuthRecover(
	ctx context.Context,
	req ChatRequest,
	stale *TokenPair,
	lease SessionLease,
	dst SSEWriter,
	cache *CacheWriter,
) (error, bool) {
	if err := lease.Check(); err != nil {
		return p.emitSessionChanged(dst), true
	}
	// Inspect the current revision before applying the anti-loop window. If a
	// newer login/refresh already replaced the rejected credentials, it is safe
	// (and preferable) to retry with that winner without rotating again.
	snapshot, snapshotErr := p.tokenStore.GetSnapshot()
	if snapshotErr != nil {
		if errors.Is(snapshotErr, ErrSessionChanged) {
			return p.emitSessionChanged(dst), true
		}
		return nil, false
	}
	if sameStoredCredentials(snapshot.Pair, *stale) {
		// Don't loop: if we just rotated within the last 5 seconds, give up.
		if time.Since(stale.SavedAt) < 5*time.Second {
			return nil, false
		}
		if snapshot.Pair.IsRefreshExpired(time.Now().UTC()) {
			return nil, false
		}
	}

	newPair, err := RefreshAccessTokenAfterUnauthorizedWithLease(
		ctx, p.tokenStore, p.cloud, stale, lease,
	)
	if err != nil {
		if chatLeaseSessionChanged(ctx, lease, err) {
			return p.emitSessionChanged(dst), true
		}
		if !errors.Is(err, ErrNoSession) {
			return p.emitErrorAndClose(dst, ProxyError{
				Kind:      KindUnknown,
				Message:   err.Error(),
				Retryable: true,
			}, nil), true
		}
		// Refresh chain dead → real auth_required.
		return p.emitErrorAndClose(dst, ProxyError{
			Kind:      KindAuthRequired,
			Message:   "登录已过期，请重新登录",
			Retryable: false,
		}, nil), true
	}
	if !chatTokenMatchesUID(newPair, req.UID) {
		return p.emitSessionChanged(dst), true
	}
	// Retry the upstream request once with the fresh token.
	upstreamReq, err := p.buildUpstreamRequest(ctx, req, newPair.AccessToken)
	if err != nil {
		return err, true
	}
	resp, err := cloneCredentialHTTPClient(p.HTTPClient).Do(upstreamReq)
	if err != nil {
		if chatLeaseSessionChanged(ctx, lease, err) {
			return p.emitSessionChanged(dst), true
		}
		return p.emitErrorAndClose(dst, ClassifyNetworkError(err), nil), true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		if chatLeaseSessionChanged(ctx, lease, nil) {
			return p.emitSessionChanged(dst), true
		}
		return p.emitErrorAndClose(dst, ClassifyHTTPResponse(resp, body), nil), true
	}
	if err := PipeUpstream(ctx, resp.Body, dst, cache); err != nil {
		if chatLeaseSessionChanged(ctx, lease, err) {
			return p.emitSessionChanged(dst), true
		}
		return p.emitErrorAndClose(dst, ClassifyUpstreamStreamError(err), nil), true
	}
	// As in the primary path, forwarded done is terminal even if the session
	// epoch changes immediately after that write.
	return nil, true
}

func chatLeaseSessionChanged(ctx context.Context, lease SessionLease, err error) bool {
	if errors.Is(err, ErrSessionChanged) ||
		(ctx != nil && errors.Is(context.Cause(ctx), ErrSessionChanged)) {
		return true
	}
	return errors.Is(lease.Check(), ErrSessionChanged)
}

func chatTokenMatchesUID(pair *TokenPair, expectedUID uint64) bool {
	if pair == nil || expectedUID == 0 || pair.AccessToken == "" {
		return false
	}
	uid, err := ExtractUIDFromAccessToken(pair.AccessToken)
	return err == nil && uid != 0 && uint64(uid) == expectedUID
}

// buildUpstreamRequest constructs the POST to workmax.app with Bearer
// auth + SSE accept header. Caller's ctx propagates so cancellation
// flows upstream.
//
// Cloud route: POST /api/work-agent/chat/agent — see
// server/router/pro/tools/workagent/aichat_router.go for the
// canonical registration.
func (p *Proxy) buildUpstreamRequest(ctx context.Context, req ChatRequest, accessToken string) (*http.Request, error) {
	if p == nil || p.cloud == nil {
		return nil, fmt.Errorf("chat: cloud client is missing")
	}
	baseURL, err := NormalizeBaseURL(p.cloud.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("chat: cloud base URL is invalid")
	}
	url := baseURL + CloudRouteChatAgent
	body := req.Body
	if body == nil {
		// Tests + malformed renderer requests — POST an empty JSON
		// object so the upstream doesn't error on empty body.
		body = []byte("{}")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "text/event-stream")
	r.Header.Set("Authorization", "Bearer "+accessToken)
	requestID, err := DesktopTurnRequestID(req.TurnUUID)
	if err != nil {
		return nil, fmt.Errorf("chat: invalid turn uuid")
	}
	r.Header.Set("X-Agent-Request-Id", requestID)
	// Mark every desktop request so the cloud handler can log + meter
	// distinctly from web traffic. SetClientHeaders also stamps the
	// X-WorkMax-Client-Version header so wire-shape drift between
	// sidecar + cloud is at least visible in access logs.
	SetClientHeaders(r.Header)
	return r, nil
}

// emitErrorAndClose writes a proxy_error SSE event downstream and
// returns it as a Go error. Used in every failure path so the
// renderer always learns *why* a turn failed. Returns an error so
// callers can `return p.emitErrorAndClose(...)` in one line.
//
// cache parameter is optional — pass nil when the cache row hasn't
// been created yet (we'd rather not introduce a row just to mark it
// partial).
func (p *Proxy) emitErrorAndClose(dst SSEWriter, pe ProxyError, cache *CacheWriter) error {
	pe = sanitizeProxyError(pe)
	if dst != nil {
		_ = dst.WriteProxyError(pe)
	}
	if cache != nil {
		// Don't finalize here — defer in Chat() owns the lifecycle.
		// emitErrorAndClose just emits the event.
	}
	// Tag the returned error with the kind so caller logs are useful.
	raw, _ := json.Marshal(pe)
	return fmt.Errorf("proxy_error: %s", string(raw))
}

func (p *Proxy) emitSessionChanged(dst SSEWriter) error {
	if dst != nil {
		_ = dst.WriteProxyError(ProxyError{
			Kind:      KindSessionChanged,
			Message:   "登录账号已变更，请重新打开当前会话",
			Retryable: false,
		})
	}
	return ErrChatSessionChanged
}
