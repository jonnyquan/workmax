//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// server_model_gateway.go is the loopback half of the official-model path:
// routes shaped like the providers the local engines already know how to talk
// to, authenticated by the in-memory gateway token, forwarded to the cloud on
// the OAuth session the sidecar owns.
//
// Which routes is an EMPIRICAL question, not a design one — the clients are
// somebody else's binaries. The set here is what the real ones were observed
// to call: claude CLI 2.1.226 calls /v1/messages and /v1/messages/count_tokens
// (see modelGatewayAnthropicCountTokens); pi's openai-completions transport
// calls exactly one endpoint, {baseUrl}/chat/completions, and OpenAI has no
// token-counting endpoint at all.
//
// Everything user-visible about failure lives here, and it is all explicit.
// OSS-4 forbids a silent fallback across routes; the same rule applies one
// level down. A turn that cannot run on the official model must say so — never
// quietly run on the user's own endpoint (a different bill and a different
// model), and never quietly run through the cloud agent route (a different
// execution environment, without the local workspace the tools were about to
// write into).

// maxModelGatewayBodyBytes bounds one forwarded request. A tool loop's request
// carries the whole conversation plus tool results, so the chat cap (1 MiB)
// is too tight; 8 MiB is roomy for a long agentic thread while still being a
// bound a runaway subprocess cannot walk past.
const maxModelGatewayBodyBytes = 8 << 20

// modelGatewayTarget is one endpoint's routing and error vocabulary.
type modelGatewayTarget struct {
	// Protocol is the gateway path segment ("anthropic" / "openai"), which
	// also picks the error body shape the caller can read.
	Protocol string
	// CloudPath is the upstream endpoint this shape forwards to.
	CloudPath string
	// Accept is what we ask the cloud for. Completions stream; count_tokens
	// answers one small JSON object, and asking it for SSE would be a lie we
	// tell every request.
	Accept string
}

var (
	modelGatewayAnthropic = modelGatewayTarget{
		Protocol:  modelGatewayProtocolAnthropic,
		CloudPath: cloudproxy.CloudRouteModelGatewayAnthropic,
		Accept:    "text/event-stream",
	}
	// modelGatewayAnthropicCountTokens exists because the packaged claude CLI
	// calls a second endpoint, and we found that out by watching it rather
	// than by reading about it: CLI 2.1.226, launched with the production
	// recipe against a path-recording stub, issues
	// POST /v1/messages/count_tokens?beta=true whenever a tool result is big
	// enough to need sizing (a Read of a ~40 KiB workspace file did it).
	//
	// Before this route the call landed on the sidecar's whole-port local
	// token perimeter and got a 403 the CLI could not parse. That is not a
	// dead turn — the CLI catches the failure and falls back to a chars/4
	// estimate — but it is a silent 403 on the official-model path, which is
	// exactly the shape of bug the perimeter exists to make visible.
	modelGatewayAnthropicCountTokens = modelGatewayTarget{
		Protocol:  modelGatewayProtocolAnthropic,
		CloudPath: cloudproxy.CloudRouteModelGatewayAnthropicCountTokens,
		Accept:    "application/json",
	}
	modelGatewayOpenAI = modelGatewayTarget{
		Protocol:  modelGatewayProtocolOpenAI,
		CloudPath: cloudproxy.CloudRouteModelGatewayOpenAI,
		Accept:    "text/event-stream",
	}
)

// The four things that can go wrong, in the words the user reads.
//
// They are written for somebody staring at a failed turn, so each one names
// the cause AND the two ways out — sign in, or go back to your own endpoint.
// "Model gateway error" would be true and useless.
const (
	modelGatewayUnboundMessage = "官方模型需要连接 WorkMax 账号：请先登录，" +
		"或在设置中改回你自己的本地模型 endpoint。"
	modelGatewayNoModelMessage = "尚未选择官方模型：请在设置中选择一个官方模型，" +
		"或改回你自己的本地模型 endpoint。"
	modelGatewayForbiddenMessage = "当前会员套餐不能使用所选的官方模型：" +
		"请在设置中换一个你的套餐支持的模型，或升级会员后重试。" +
		"也可以在设置中改回你自己的本地模型 endpoint。"
	modelGatewayRetiredModelMessage = "所选的官方模型已不可用（可能已下架）：" +
		"请在设置中重新选择一个模型，或改回你自己的本地模型 endpoint。"
	modelGatewayUnreachableMessage = "无法连接 WorkMax 云端，官方模型本轮不可用：" +
		"请检查网络后重试，或在设置中改回你自己的本地模型 endpoint。"
	modelGatewayExpiredMessage = "WorkMax 登录已过期：请重新登录后重试，" +
		"或在设置中改回你自己的本地模型 endpoint。"
	modelGatewayUnavailableMessage = "官方模型网关未就绪：请重启 WorkMax 后重试，" +
		"或在设置中改回你自己的本地模型 endpoint。"
	modelGatewayCredentialMessage = "无效的本地网关凭据。"
)

func (s *Server) handleModelGatewayAnthropicMessages(c *gin.Context) {
	s.forwardModelGateway(c, modelGatewayAnthropic)
}

func (s *Server) handleModelGatewayAnthropicCountTokens(c *gin.Context) {
	s.forwardModelGateway(c, modelGatewayAnthropicCountTokens)
}

func (s *Server) handleModelGatewayOpenAIChatCompletions(c *gin.Context) {
	s.forwardModelGateway(c, modelGatewayOpenAI)
}

// forwardModelGateway performs one loopback→cloud forward.
//
// The credential was already checked by the route's gateway-token middleware.
// What remains is the part that must be re-decided per request rather than
// cached into the token: who is signed in, which model they chose, and whether
// the cloud still says yes.
func (s *Server) forwardModelGateway(c *gin.Context, target modelGatewayTarget) {
	gateway := s.cfg.ModelGateway
	if gateway == nil || !gateway.Ready() {
		writeModelGatewayError(c, target, http.StatusServiceUnavailable,
			modelGatewayErrorAPIError, modelGatewayUnavailableMessage)
		return
	}
	if s.cfg.Proxy == nil || s.cfg.TokenStore == nil {
		writeModelGatewayError(c, target, http.StatusServiceUnavailable,
			modelGatewayErrorAPIError, modelGatewayUnavailableMessage)
		return
	}

	// An account binding is not something the token can vouch for: the token
	// outlives nothing, but the session it stands in for can end at any
	// moment, and this is the moment that matters.
	identity := s.resolveIdentity()
	if !identity.IsCloud() {
		writeModelGatewayError(c, target, http.StatusUnauthorized,
			modelGatewayErrorAuthentication, modelGatewayUnboundMessage)
		return
	}
	modelID := s.officialModelIDFor(identity.UID)
	if modelID == "" {
		writeModelGatewayError(c, target, http.StatusBadRequest,
			modelGatewayErrorInvalidRequest, modelGatewayNoModelMessage)
		return
	}

	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeModelGatewayError(c, target, http.StatusBadRequest,
			modelGatewayErrorInvalidRequest, "无法读取请求体。")
		return
	}
	// The model is the sidecar's answer, not the subprocess's. A local process
	// holding the loopback token could otherwise name any model it liked, and
	// the identity's own choice — the one the settings form checked against
	// the catalog — would be advisory. Rewriting it here makes the cloud
	// contract ("model must be a catalog modelId") true by construction.
	body, err := withModelGatewayModel(raw, modelID)
	if err != nil {
		writeModelGatewayError(c, target, http.StatusBadRequest,
			modelGatewayErrorInvalidRequest, "请求体不是合法的 JSON 对象。")
		return
	}

	ctx, releaseSession := gateway.BindContext(c.Request.Context())
	defer releaseSession()

	cloud := s.cfg.Proxy.CloudClient()
	pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(ctx, s.cfg.TokenStore, cloud)
	if err != nil {
		status, kind, message := modelGatewayTokenFailure(err)
		writeModelGatewayError(c, target, status, kind, message)
		return
	}
	leaseCtx, releaseLease := lease.BindContext(ctx)
	defer releaseLease()

	resp, err := s.sendModelGatewayRequest(leaseCtx, cloud, target, body, pair.AccessToken)
	if err != nil {
		s.writeModelGatewayTransportError(c, target, ctx, err)
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Exactly one refresh, mirroring every other cloud call in the
		// sidecar. A second 401 is a real "sign in again".
		drainModelGatewayBody(resp)
		fresh, refreshErr := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
			leaseCtx, s.cfg.TokenStore, cloud, pair, lease,
		)
		if refreshErr != nil {
			status, kind, message := modelGatewayTokenFailure(refreshErr)
			writeModelGatewayError(c, target, status, kind, message)
			return
		}
		resp, err = s.sendModelGatewayRequest(leaseCtx, cloud, target, body, fresh.AccessToken)
		if err != nil {
			s.writeModelGatewayTransportError(c, target, ctx, err)
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.writeModelGatewayUpstreamStatus(c, target, resp)
		return
	}
	streamModelGatewayResponse(c, resp)
}

// sendModelGatewayRequest builds and performs the upstream POST. The Bearer
// token stays inside this function's call: it is never echoed downstream, and
// the loopback caller has no way to observe it.
//
// The caller's own headers are NOT forwarded, and anthropic-beta is the
// deliberate case. The recording probe showed the CLI asking for seven betas
// at once (claude-code-20250219, interleaved-thinking-2025-05-14,
// thinking-token-count-2026-05-13, context-management-2025-06-27,
// prompt-caching-scope-2026-01-05, mid-conversation-system-2026-04-07,
// effort-2025-11-24) plus a ?beta=true query. Those are negotiated against
// ANTHROPIC's API, and the official path lands on whichever pooled relay ops
// configured — a relay that has never heard of one of them answers 400, which
// would turn a working turn into a dead one for a feature the CLI degrades
// gracefully without. So the betas stop here, and the turn runs on the
// baseline protocol. The query string is dropped for the same reason.
func (s *Server) sendModelGatewayRequest(
	ctx context.Context,
	cloud *cloudproxy.Client,
	target modelGatewayTarget,
	body []byte,
	accessToken string,
) (*http.Response, error) {
	baseURL, err := cloudproxy.NormalizeBaseURL(cloud.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("model gateway: cloud base URL is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+target.CloudPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	accept := target.Accept
	if accept == "" {
		accept = "text/event-stream"
	}
	request.Header.Set("Accept", accept)
	request.Header.Set("Authorization", "Bearer "+accessToken)
	cloudproxy.SetClientHeaders(request.Header)
	return s.cfg.ModelGateway.client.Do(request)
}

// writeModelGatewayTransportError distinguishes "the session ended under this
// request" from "the network failed", because the user's next action differs.
func (s *Server) writeModelGatewayTransportError(
	c *gin.Context,
	target modelGatewayTarget,
	sessionCtx context.Context,
	err error,
) {
	if errors.Is(err, cloudproxy.ErrSessionChanged) ||
		errors.Is(context.Cause(sessionCtx), cloudproxy.ErrSessionChanged) {
		writeModelGatewayError(c, target, http.StatusUnauthorized,
			modelGatewayErrorAuthentication, modelGatewayUnboundMessage)
		return
	}
	if sessionCtx.Err() != nil && c.Request.Context().Err() == nil {
		// The gateway generation was retired mid-forward (logout, or a new
		// login). Not a network problem, and not something a retry fixes
		// without signing in again.
		writeModelGatewayError(c, target, http.StatusUnauthorized,
			modelGatewayErrorAuthentication, modelGatewayUnboundMessage)
		return
	}
	writeModelGatewayError(c, target, http.StatusBadGateway,
		modelGatewayErrorAPIError, modelGatewayUnreachableMessage)
}

// writeModelGatewayUpstreamStatus turns a non-200 into the one thing the
// subprocess will show its user. The upstream body is read (bounded) and
// dropped rather than forwarded: it is written for a different audience, and
// the actionable sentence is ours.
func (s *Server) writeModelGatewayUpstreamStatus(c *gin.Context, target modelGatewayTarget, resp *http.Response) {
	drainModelGatewayBody(resp)
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusPaymentRequired:
		writeModelGatewayError(c, target, http.StatusForbidden,
			modelGatewayErrorPermission, modelGatewayForbiddenMessage)
	case http.StatusUnauthorized:
		writeModelGatewayError(c, target, http.StatusUnauthorized,
			modelGatewayErrorAuthentication, modelGatewayExpiredMessage)
	case http.StatusNotFound:
		// The cloud answers 404 for a model id that is no longer in the
		// catalog — retired, or switched off by ops. The stored choice is the
		// thing to change, so say that rather than reporting an outage.
		writeModelGatewayError(c, target, http.StatusNotFound,
			modelGatewayErrorInvalidRequest, modelGatewayRetiredModelMessage)
	case http.StatusTooManyRequests:
		writeModelGatewayError(c, target, http.StatusTooManyRequests,
			modelGatewayErrorRateLimit, "官方模型请求太频繁，请稍后再试。")
	case http.StatusRequestEntityTooLarge:
		writeModelGatewayError(c, target, http.StatusRequestEntityTooLarge,
			modelGatewayErrorInvalidRequest, "请求过大：请减少上下文或附件后重试。")
	default:
		writeModelGatewayError(c, target, http.StatusBadGateway, modelGatewayErrorAPIError,
			fmt.Sprintf("官方模型网关返回了错误（上游 %d）：请稍后重试，"+
				"或在设置中改回你自己的本地模型 endpoint。", resp.StatusCode))
	}
}

// drainModelGatewayBody consumes a bounded prefix so the connection can be
// reused, and closes the body. The bytes are discarded on purpose: an upstream
// error body is written for a different audience, and forwarding it verbatim
// into a subprocess's transcript is how internal detail reaches a user who
// cannot act on it.
func drainModelGatewayBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	_ = resp.Body.Close()
}

// streamModelGatewayResponse copies the upstream stream through unbuffered.
//
// Flushing per chunk is the whole point: the subprocess on the other end is
// rendering tokens as they arrive, and a buffering relay turns a live tool
// loop into one long silence followed by a wall of text.
func streamModelGatewayResponse(c *gin.Context, resp *http.Response) {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream"
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Set("Cache-Control", "no-store")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, _ := c.Writer.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// withModelGatewayModel replaces the request body's model with the identity's
// chosen official model, preserving every other field byte-for-byte.
func withModelGatewayModel(raw []byte, modelID string) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(modelID)
	if err != nil {
		return nil, err
	}
	fields["model"] = encoded
	return json.Marshal(fields)
}

// modelGatewayTokenFailure maps a token-acquisition failure onto the response
// the subprocess should see. "No session" and "session changed" are both
// "connect an account"; anything else is a local credential problem the user
// fixes by signing in again.
func modelGatewayTokenFailure(err error) (int, string, string) {
	switch {
	case errors.Is(err, cloudproxy.ErrNoSession), errors.Is(err, cloudproxy.ErrSessionChanged):
		return http.StatusUnauthorized, modelGatewayErrorAuthentication, modelGatewayUnboundMessage
	default:
		return http.StatusUnauthorized, modelGatewayErrorAuthentication, modelGatewayExpiredMessage
	}
}

// Provider-shaped error type names. The subprocess reads its own provider's
// error shape; giving it ours would surface as "unexpected response" instead
// of the sentence we wrote.
const (
	modelGatewayErrorAuthentication = "authentication_error"
	modelGatewayErrorPermission     = "permission_error"
	modelGatewayErrorInvalidRequest = "invalid_request_error"
	modelGatewayErrorRateLimit      = "rate_limit_error"
	modelGatewayErrorAPIError       = "api_error"
)

// writeModelGatewayError answers in the protocol's own error shape.
func writeModelGatewayError(c *gin.Context, target modelGatewayTarget, status int, kind, message string) {
	c.Header("Cache-Control", "no-store")
	if target.Protocol == modelGatewayProtocolOpenAI {
		c.AbortWithStatusJSON(status, gin.H{
			"error": gin.H{"message": message, "type": kind, "code": kind},
		})
		return
	}
	c.AbortWithStatusJSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": kind, "message": message},
	})
}
