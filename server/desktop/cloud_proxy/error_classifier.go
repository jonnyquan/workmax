//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// ProxyErrorKind is the closed enum of error categories the sidecar
// surfaces to the renderer. Matches cloud-proxy.md §6.1 exactly.
//
// CodePilot's reference ClaudeErrorCategory had ~24 entries (local CLI
// errors, MCP errors, BYOK errors). We're none of those — desktop sidecar
// has no local CLI, no MCP, no BYOK, no model choice. The matrix
// collapses to ten.
type ProxyErrorKind string

const (
	KindNetworkUnavailable ProxyErrorKind = "network_unavailable"
	KindAuthRequired       ProxyErrorKind = "auth_required"
	KindAuthExpired        ProxyErrorKind = "auth_expired"
	KindServiceUnavailable ProxyErrorKind = "service_unavailable"
	KindQuotaExceeded      ProxyErrorKind = "quota_exceeded"
	KindRateLimited        ProxyErrorKind = "rate_limited"
	KindPayloadTooLarge    ProxyErrorKind = "payload_too_large"
	KindBadRequest         ProxyErrorKind = "bad_request"
	KindSessionChanged     ProxyErrorKind = "session_changed"
	KindUnknown            ProxyErrorKind = "unknown"
)

// ProxyError is the wire-shape of a proxy_error SSE event. The renderer
// receives this as the `data` payload; UI logic switches on Kind to
// decide whether to show a retry button / login prompt / upgrade CTA.
type ProxyError struct {
	Kind         ProxyErrorKind `json:"kind"`
	Message      string         `json:"message"`
	Retryable    bool           `json:"retryable"`
	RetryAfterMS int            `json:"retry_after_ms,omitempty"`
	LogID        string         `json:"log_id,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

// ClassifyTokenStoreError maps token-load failures to a ProxyError.
// Used by Chat() before it even gets to the upstream HTTP call.
func ClassifyTokenStoreError(err error) (pe ProxyError) {
	defer func() { pe = sanitizeProxyError(pe) }()
	if errors.Is(err, ErrNoSession) {
		return ProxyError{
			Kind:      KindAuthRequired,
			Message:   "需要登录 workmax 账号",
			Retryable: false,
		}
	}
	// Any other TokenStore error is a Keychain / disk problem the user
	// can't fix without re-login; treat it the same way.
	return ProxyError{
		Kind:      KindAuthRequired,
		Message:   "需要登录 workmax 账号",
		Retryable: false,
		Details:   map[string]any{"reason": err.Error()},
	}
}

// ClassifyNetworkError maps an HTTP-client-side error (DNS, TCP, TLS,
// I/O during the request) to a ProxyError. ctx.Err()-driven cancellation
// is filtered out by the caller before we get here.
func ClassifyNetworkError(err error) (pe ProxyError) {
	defer func() { pe = sanitizeProxyError(pe) }()
	// Wrapped context cancellation surfaces as net.OpError("operation
	// was canceled") or url.Error wrapping context.Canceled. The caller
	// should have checked ctx.Err() already; if we see it here it's
	// still safe to surface as network — the renderer hides the bubble
	// when it initiated the cancel.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ProxyError{
			Kind:      KindNetworkUnavailable,
			Message:   "请求已取消",
			Retryable: true,
			Details:   map[string]any{"reason": err.Error()},
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ProxyError{
			Kind:      KindNetworkUnavailable,
			Message:   "网络超时，请检查你的网络",
			Retryable: true,
			Details:   map[string]any{"reason": err.Error()},
		}
	}
	return ProxyError{
		Kind:      KindNetworkUnavailable,
		Message:   "网络连接失败，请检查你的网络",
		Retryable: true,
		Details:   map[string]any{"reason": err.Error()},
	}
}

// ClassifyHTTPResponse maps an upstream non-200 response to a ProxyError.
// Reads Retry-After (for 429) and WWW-Authenticate (for 401) per RFC.
//
// Callers MUST have read+closed the response body before discarding —
// this function only inspects the status line and headers; it does not
// drain the body itself so the caller has full control over rate-limit
// signals embedded in the body.
func ClassifyHTTPResponse(resp *http.Response, body []byte) (pe ProxyError) {
	defer func() { pe = sanitizeProxyError(pe) }()
	status := resp.StatusCode

	switch {
	case status == http.StatusUnauthorized: // 401
		// WWW-Authenticate: Bearer error="invalid_token" → access token
		// is dead. Refresh-token has been tried; the renderer must
		// re-login. If WWW-Authenticate is absent, treat as expired
		// anyway — the only way a 401 reaches the renderer is if the
		// access token failed AND the refresh-token chain couldn't
		// rotate it. Either way, re-login is the only fix.
		return ProxyError{
			Kind:      KindAuthExpired,
			Message:   "登录已过期，请重新登录",
			Retryable: false,
			Details: map[string]any{
				"upstream_status":      status,
				"www_authenticate":     resp.Header.Get("WWW-Authenticate"),
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	case status == http.StatusPaymentRequired || status == http.StatusForbidden: // 402, 403
		// 402 explicitly means quota; 403 with `error: quota_exceeded`
		// in the body also means quota. Other 403s (e.g. tier-gated
		// feature) fall through to bad_request semantics.
		if status == http.StatusPaymentRequired || strings.Contains(string(body), "quota_exceeded") {
			return ProxyError{
				Kind:      KindQuotaExceeded,
				Message:   "本月配额已用完，可升级到 Pro 解锁更多",
				Retryable: false,
				Details: map[string]any{
					"upstream_status":      status,
					"upstream_body_prefix": bodyPrefix(body),
				},
			}
		}
		return ProxyError{
			Kind:      KindBadRequest,
			Message:   "请求被拒绝",
			Retryable: false,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	case status == http.StatusTooManyRequests: // 429
		retryAfter := parseRetryAfterMS(resp.Header.Get("Retry-After"))
		return ProxyError{
			Kind:         KindRateLimited,
			Message:      "请求太频繁，请稍后再试",
			Retryable:    true,
			RetryAfterMS: retryAfter,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	case status == http.StatusRequestEntityTooLarge: // 413
		return ProxyError{
			Kind:      KindPayloadTooLarge,
			Message:   "请求过大，请减少文件或精简内容",
			Retryable: false,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	case status >= 500 && status < 600: // 5xx
		return ProxyError{
			Kind:      KindServiceUnavailable,
			Message:   "workmax 服务暂时异常，请稍后重试",
			Retryable: true,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	case status >= 400 && status < 500: // catch-all 4xx
		// Try to extract upstream error.message so the renderer can
		// show what workmax.app actually said (e.g. validation errors).
		msg := extractErrorMessage(body)
		if msg == "" {
			msg = "请求无效"
		}
		return ProxyError{
			Kind:      KindBadRequest,
			Message:   msg,
			Retryable: false,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}

	default: // 1xx/3xx that somehow reach us — treat as unknown
		return ProxyError{
			Kind:      KindUnknown,
			Message:   "未知响应",
			Retryable: true,
			Details: map[string]any{
				"upstream_status":      status,
				"upstream_body_prefix": bodyPrefix(body),
			},
		}
	}
}

// ClassifyLocalUpstreamError maps a failure the user's OWN model endpoint
// produced onto the same ProxyErrorKind enum every other path uses.
//
// It exists because the L2 pi runtime never sees an *http.Response: the HTTP
// call happens inside pi's subprocess and the outcome comes back as a sentence
// on the event stream ("400: {…}", "429 status code (no body)"). Everything
// arrived as kind:unknown, retryable:true — one undifferentiated shrug covering
// a model id that does not exist, an endpoint that is not listening, and a
// provider throttling the account, which are three different things for the
// person who has to fix one of them.
//
// The KINDS match ClassifyHTTPResponse status for status (pinned by a test) so
// there is one taxonomy. The MESSAGES deliberately do not: this endpoint
// belongs to the user, so a 401 is their API key rather than their workmax
// login, a 402 is their provider's bill rather than a Pro upgrade, and a 502 —
// which is also what pi reports for a refused TCP connection — is "is it
// running?" rather than "workmax is having a moment".
func ClassifyLocalUpstreamError(status int, body string) (pe ProxyError) {
	defer func() { pe = sanitizeProxyError(pe) }()
	details := map[string]any{
		"upstream_status":      status,
		"upstream_body_prefix": bodyPrefix([]byte(body)),
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden: // 401, 403
		return ProxyError{
			Kind:      KindAuthRequired,
			Message:   "本地模型 endpoint 拒绝了凭据：请检查设置中的 API key",
			Retryable: false,
			Details:   details,
		}

	case status == http.StatusPaymentRequired: // 402
		return ProxyError{
			Kind:      KindQuotaExceeded,
			Message:   "本地模型 endpoint 报告额度不足：请检查该服务商的余额或配额",
			Retryable: false,
			Details:   details,
		}

	case status == http.StatusNotFound: // 404
		// Same sentence L1 uses for the same status, because it is the same
		// mistake: a base_url that does not have the API where we asked.
		return ProxyError{
			Kind:      KindBadRequest,
			Message:   "本地模型 endpoint 返回 404：请检查 base_url 是否正确",
			Retryable: false,
			Details:   details,
		}

	case status == http.StatusRequestEntityTooLarge: // 413
		return ProxyError{
			Kind:      KindPayloadTooLarge,
			Message:   "请求过大，请减少文件或精简内容",
			Retryable: false,
			Details:   details,
		}

	case status == http.StatusTooManyRequests: // 429
		return ProxyError{
			Kind:      KindRateLimited,
			Message:   "本地模型 endpoint 限流，请稍后再试",
			Retryable: true,
			Details:   details,
		}

	case status == http.StatusBadGateway: // 502
		// The one status that means something different here than upstream.
		// Measured: pi turns a refused TCP connection into a synthetic 502, so
		// this branch covers both "nothing is listening on that port" and "a
		// real gateway is down" — indistinguishable from here, and the first is
		// overwhelmingly what a local-first user hits. "Is it running?" is the
		// question worth asking; "workmax is having a moment" is not.
		return ProxyError{
			Kind:      KindNetworkUnavailable,
			Message:   "无法连接本地模型 endpoint：请确认它正在运行，且 base_url 正确",
			Retryable: true,
			Details:   details,
		}

	case status >= 500 && status < 600: // other 5xx, 500/503/504 included
		return ProxyError{
			Kind:      KindServiceUnavailable,
			Message:   "本地模型 endpoint 返回了服务端错误，请稍后重试",
			Retryable: true,
			Details:   details,
		}

	case status >= 400 && status < 500: // catch-all 4xx, 400 included
		// The endpoint's own words when it left any: "model foo does not
		// exist" is the whole diagnosis, and nothing this side writes improves
		// on it.
		msg := extractErrorMessage([]byte(body))
		if msg == "" {
			msg = "本地模型 endpoint 拒绝了请求：请检查模型 ID 与端点配置"
		}
		return ProxyError{
			Kind:      KindBadRequest,
			Message:   msg,
			Retryable: false,
			Details:   details,
		}

	default:
		return ProxyError{
			Kind:      KindUnknown,
			Message:   "本地模型 endpoint 返回了无法识别的结果",
			Retryable: true,
			Details:   details,
		}
	}
}

// ClassifyUpstreamStreamError maps a mid-stream upstream failure
// (scanner.Err() != nil after we already started reading SSE) to a
// ProxyError. We tag retryable=true so the renderer can show a "retry"
// affordance, but cache_writer will already have flagged partial.
func ClassifyUpstreamStreamError(err error) (pe ProxyError) {
	defer func() { pe = sanitizeProxyError(pe) }()
	if err == nil {
		return ProxyError{Kind: KindUnknown, Message: "上游连接异常结束", Retryable: true}
	}
	return ProxyError{
		Kind:      KindServiceUnavailable,
		Message:   "workmax 服务连接中断，请稍后重试",
		Retryable: true,
		Details:   map[string]any{"reason": err.Error()},
	}
}

// parseRetryAfterMS converts the HTTP Retry-After header (RFC 9110 §10.2.3)
// to milliseconds. Supports the integer-seconds form only; HTTP-date form
// is uncommon in JSON APIs and we don't bother. 0 = absent or unparsable.
func parseRetryAfterMS(v string) int {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs < 0 {
		return 0
	}
	return secs * 1000
}

// bodyPrefix trims the upstream body to a reasonable size for logging
// in the Details map. We don't want to flood the renderer's console
// with a 1MB HTML error page from a misconfigured upstream.
func bodyPrefix(body []byte) string {
	const max = 512
	if len(body) <= max {
		return redactProxyErrorString(string(body))
	}
	return redactProxyErrorString(string(body[:max])) + "…"
}

// extractErrorMessage looks for {"error":{"message":"..."}} or
// {"error":"..."} in a JSON body without depending on a full
// json.Unmarshal of unknown-shape structs.
func extractErrorMessage(body []byte) string {
	// Cheap: scan for the substring then take everything inside the
	// quotes. Not bulletproof but good enough for the rare 4xx that
	// trickles through. Wrong parses just return ""; we don't crash.
	patterns := []string{`"message":"`, `"error_description":"`, `"error":"`}
	for _, p := range patterns {
		idx := strings.Index(string(body), p)
		if idx < 0 {
			continue
		}
		rest := string(body[idx+len(p):])
		end := strings.Index(rest, `"`)
		if end < 0 || end > 200 {
			continue
		}
		return rest[:end]
	}
	return ""
}
