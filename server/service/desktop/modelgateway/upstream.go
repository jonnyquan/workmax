package modelgateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	workagentService "server/service/tools/workagent"
)

// ProviderAccount is the subset of a platform credential the gateway needs.
// It is a value type on purpose: nothing downstream should be able to reach
// back into the pool row, and nothing here is ever marshalled.
type ProviderAccount struct {
	ID       uint64
	Name     string
	Provider string
	BaseURL  string
	APIKey   string
}

// ProviderAccountSource hands out platform credentials and takes health
// feedback. An interface so tests can point the gateway at an httptest server
// and so the selection policy can change without touching the proxy path.
type ProviderAccountSource interface {
	// AccountForModel picks a credential able to serve modelID over protocol.
	AccountForModel(modelID string, protocol Protocol) (*ProviderAccount, error)
	// RecordSuccess / RecordFailure feed the pool's circuit breakers, so a
	// gateway outage trips the same breaker a cloud-turn outage does instead
	// of silently hammering a dead account.
	RecordSuccess(accountID uint64)
	RecordFailure(accountID uint64, err error)
}

// PoolAccountSource is the production source: the SAME w_workagent_account
// pool that the cloud agent turn spends from.
//
// Why this pool and not w_generator_provider or a config block:
//
//   - w_workagent_account IS the conversation-model credential store. It
//     already carries base_url + api_key, an is_premium split that mirrors
//     the work-pro / work-plus tiers (agent_account_pool.go:373-388), circuit
//     breakers, and a monthly token-spend budget. The catalog's modelId is
//     literally the modelTier value that pool indexes on.
//   - w_generator_provider is image/video/audio generation routing
//     (model/generator_provider.go) — a different product surface with a
//     different cost model.
//   - A config block would create a second place credentials live, with its
//     own rotation story and no breaker.
//
// Sharing the pool also means one budget and one ops surface: a key rotated
// for the cloud turn is rotated for the Desktop gateway in the same action.
type PoolAccountSource struct {
	pool *workagentService.AgentAccountPool
}

func NewPoolAccountSource() *PoolAccountSource {
	return &PoolAccountSource{pool: workagentService.GetAgentAccountPool()}
}

func (s *PoolAccountSource) AccountForModel(modelID string, protocol Protocol) (*ProviderAccount, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("agent account pool is not initialized")
	}
	account, err := s.pool.GetAccountForModelTier(modelID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, errors.New("no account available")
	}
	if !providerSpeaks(account.Provider, protocol) {
		// Honest refusal rather than a silent protocol translation. Turning
		// an OpenAI request into an Anthropic one (and its stream back again)
		// is a large, lossy transform — tool calls, content blocks, delta
		// framing — and getting it subtly wrong would corrupt a tool loop in
		// ways that look like model misbehaviour.
		return nil, errNoAccountForProtocol
	}
	return &ProviderAccount{
		ID:       account.ID,
		Name:     account.Name,
		Provider: account.Provider,
		BaseURL:  account.BaseURL,
		APIKey:   account.APIKey,
	}, nil
}

func (s *PoolAccountSource) RecordSuccess(accountID uint64) {
	if s != nil && s.pool != nil {
		s.pool.RecordSuccess(accountID)
	}
}

func (s *PoolAccountSource) RecordFailure(accountID uint64, err error) {
	if s != nil && s.pool != nil {
		s.pool.RecordFailure(accountID, err)
	}
}

// errNoAccountForProtocol is the "we have credentials, but none that speak
// this wire format" case, kept distinct so the message can say so.
var errNoAccountForProtocol = errors.New("no provider account speaks the requested protocol")

// providerSpeaks decides whether a pool account's `provider` string can serve
// a wire protocol.
//
// Empty and unrecognized values read as Anthropic because that is what the
// pool has always held: accounts feed the Claude Agent SDK through
// ANTHROPIC_BASE_URL (service/tools/workagent/agent_client_manager.go:97-143).
// Defaulting the other way would strand every existing row.
func providerSpeaks(provider string, protocol Protocol) bool {
	normalized := strings.ToLower(strings.TrimSpace(provider))
	switch protocol {
	case ProtocolOpenAI:
		return strings.Contains(normalized, "openai") ||
			strings.Contains(normalized, "azure") ||
			strings.Contains(normalized, "deepseek") ||
			strings.Contains(normalized, "qwen") ||
			strings.Contains(normalized, "moonshot") ||
			strings.Contains(normalized, "openai_compatible")
	default:
		if normalized == "" {
			return true
		}
		if strings.Contains(normalized, "openai") || strings.Contains(normalized, "azure") {
			return false
		}
		return true
	}
}

// hopByHopHeaders are never copied in either direction.
var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
}

// forwardableRequestHeaders is an ALLOWLIST of client headers that reach the
// provider. An allowlist, not a denylist, because the inbound request carries
// the Desktop's own OAuth bearer in Authorization and its device metadata in
// x-workmax-* headers — none of which the provider should ever see, and a
// denylist would leak every header we forgot to think about.
//
// anthropic-version and anthropic-beta are here because they are protocol
// version negotiation, not identity: a client that needs a beta feature has
// no other way to ask for it.
var forwardableRequestHeaders = map[string]struct{}{
	"anthropic-version": {},
	"anthropic-beta":    {},
	"accept":            {},
	"accept-encoding":   {},
}

// forwardableResponseHeaders is likewise an allowlist. Upstream response
// headers routinely carry rate-limit state and request ids tied to OUR
// account; the client needs none of them.
var forwardableResponseHeaders = map[string]struct{}{
	"content-type":      {},
	"cache-control":     {},
	"content-encoding":  {},
	"x-accel-buffering": {},
}

// Upstream performs the outbound call. One instance per gateway, so the
// connection pool is shared and per-call cost is a request, not a dial.
type Upstream struct {
	client *http.Client
}

// NewUpstream builds the outbound client. responseHeaderTimeout bounds the
// wait for a status line without bounding the stream that follows it — a
// legitimate model turn can stream for minutes, but an upstream that has not
// even answered in 90s is gone.
func NewUpstream(responseHeaderTimeout time.Duration) *Upstream {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		// Never let the transport transparently gzip: we forward the body
		// byte-for-byte, so a decompression we did not ask for would leave
		// the client's Content-Encoding header lying about the payload.
		DisableCompression: true,
	}
	return &Upstream{client: &http.Client{Transport: transport}}
}

// errUnsupportedOperation is "this protocol has no such endpoint" — a refusal,
// never a guess at a path the provider does not serve.
var errUnsupportedOperation = errors.New("protocol does not support the requested operation")

// upstreamURL joins an account base URL with the protocol's path, tolerating
// bases that already carry a path prefix (relays commonly do) and bases that
// already end in the protocol path (so a mis-configured account produces one
// correct URL rather than /v1/messages/v1/messages).
func upstreamURL(baseURL string, protocol Protocol, op Operation) (string, error) {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return "", errors.New("provider base url is empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("provider base url must be http or https")
	}
	full, ok := protocol.UpstreamPathFor(op)
	if !ok {
		return "", errUnsupportedOperation
	}
	root := protocol.UpstreamPath()
	basePath := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(basePath, full):
		parsed.Path = basePath
	case strings.HasSuffix(basePath, root):
		// The account was configured down to the completion endpoint. Append
		// only what the operation adds, so ".../v1/messages" produces
		// ".../v1/messages/count_tokens" rather than a doubled path.
		parsed.Path = basePath + strings.TrimPrefix(full, root)
	default:
		parsed.Path = basePath + full
	}
	return parsed.String(), nil
}

// buildRequest assembles the outbound call: allowlisted client headers, the
// platform credential in the shape the protocol expects, and nothing else.
func (u *Upstream) buildRequest(
	ctx context.Context,
	protocol Protocol,
	op Operation,
	account *ProviderAccount,
	body []byte,
	clientHeader http.Header,
) (*http.Request, error) {
	endpoint, err := upstreamURL(account.BaseURL, protocol, op)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	for name, values := range clientHeader {
		key := strings.ToLower(name)
		if _, ok := forwardableRequestHeaders[key]; !ok {
			continue
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	switch protocol {
	case ProtocolOpenAI:
		req.Header.Set("Authorization", "Bearer "+account.APIKey)
	default:
		req.Header.Set("x-api-key", account.APIKey)
		// Some relays authenticate on Authorization instead; the SDK sets
		// both from ANTHROPIC_API_KEY / ANTHROPIC_AUTH_TOKEN for the same
		// reason (agent_client_manager.go:120-125).
		req.Header.Set("Authorization", "Bearer "+account.APIKey)
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	}
	return req, nil
}

// Do issues the request. The response body is the caller's to close.
func (u *Upstream) Do(
	ctx context.Context,
	protocol Protocol,
	op Operation,
	account *ProviderAccount,
	body []byte,
	clientHeader http.Header,
) (*http.Response, error) {
	req, err := u.buildRequest(ctx, protocol, op, account, body, clientHeader)
	if err != nil {
		return nil, err
	}
	return u.client.Do(req)
}

// copyResponseHeaders applies the response allowlist onto the client writer.
func copyResponseHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		key := strings.ToLower(name)
		if _, hop := hopByHopHeaders[key]; hop {
			continue
		}
		if _, ok := forwardableResponseHeaders[key]; !ok {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

// isStreamingResponse reports whether the upstream answered with SSE, which
// decides whether we stream through or buffer. We read the upstream's own
// content type rather than trusting the request's `stream: true`, because the
// upstream is the authority on what it actually sent.
func isStreamingResponse(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

// maxBufferedResponseBytes caps a non-streamed upstream body. Well past any
// legitimate completion, and it exists so a hostile or broken upstream cannot
// make us hold an unbounded body per in-flight request.
const maxBufferedResponseBytes = 32 << 20

func readBufferedBody(body io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(body, maxBufferedResponseBytes))
}

// classifyTransportError turns a failed round trip into our vocabulary. The
// error text is never surfaced — a dial error names the upstream host.
func classifyTransportError(ctx context.Context, err error) *GatewayError {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return newError(http.StatusGatewayTimeout, ErrClassUpstreamTimeout,
			"the upstream model provider did not respond in time")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return newError(http.StatusGatewayTimeout, ErrClassUpstreamTimeout,
			"the upstream model provider did not respond in time")
	}
	return newError(http.StatusBadGateway, ErrClassUpstreamError,
		"the model gateway could not reach the upstream provider")
}
