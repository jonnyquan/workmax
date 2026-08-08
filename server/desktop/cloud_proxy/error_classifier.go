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
