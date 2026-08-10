// Package modelgateway is the cloud side of the Desktop model gateway: a bare
// model API the Desktop's LOCAL tool loop can call so it uses official models
// without ever holding a provider key.
//
// The one invariant everything here defends: a provider API key, a provider
// base URL, and a provider account identity NEVER reach a client. The Desktop
// is handed one thing — our own gateway URL — and authenticates with the same
// Desktop OAuth bearer it already uses for /api/desktop/models and
// /api/desktop/sync/*. The platform credential is attached on the way out and
// stripped from anything on the way back. That is why upstream errors are
// classified into a small closed vocabulary rather than proxied: a provider's
// 401 body routinely names the account it rejected.
//
// Metering, not charging: every call writes a w_desktop_model_gateway_usage
// row, and no credits move. See the migration for why reusing
// w_credit_reservation or service/agentbilling was not the right trade here.
package modelgateway

import (
	"net/http"
)

// Protocol is the wire shape a caller speaks. Each maps to one route and one
// error-body shape; the gateway does NOT translate between them, because the
// upstream account it picks must already speak the requested protocol.
type Protocol string

const (
	// ProtocolAnthropic is the Anthropic Messages API
	// (POST .../anthropic/v1/messages), spoken by the packaged claude engine.
	ProtocolAnthropic Protocol = "anthropic"
	// ProtocolOpenAI is OpenAI Chat Completions
	// (POST .../openai/v1/chat/completions), spoken by the pi engine.
	ProtocolOpenAI Protocol = "openai"
)

// UpstreamPath is where a protocol's request goes once the account's base URL
// is known. Kept beside the protocol so a new protocol cannot be added
// without deciding its path.
func (p Protocol) UpstreamPath() string {
	switch p {
	case ProtocolOpenAI:
		return "/v1/chat/completions"
	default:
		return "/v1/messages"
	}
}

// Operation names WHICH endpoint of a protocol a request is for. Protocol
// alone was enough while every caller wanted a completion; it stopped being
// enough when a real client turned out to call a second one.
//
// Measured, not guessed: claude CLI 2.1.226, driven through our own SDK with
// the production launch recipe against a path-recording stub, calls
// POST /v1/messages/count_tokens?beta=true whenever a tool result is large
// enough to need sizing (a Read of a ~40 KiB file did it). Without this the
// call met the sidecar's local-token perimeter and got a 403 with a body the
// CLI cannot read — the tool loop silently degraded to a chars/4 estimate on
// the very files where the estimate matters most. See
// server/desktop/local_agent/engine_cli_test.go for the resident probe.
//
// OpenAI has no counterpart endpoint, and pi (packages/ai
// openai-completions.ts) issues exactly one call — client.chat.completions
// .create against {baseUrl}/chat/completions — so OpCountTokens is Anthropic
// only and is refused on the OpenAI protocol rather than silently mapped.
type Operation string

const (
	// OpMessages is the completion endpoint: the protocol's own root path.
	OpMessages Operation = "messages"
	// OpCountTokens is Anthropic's token counter. Not billed by the provider,
	// so it meters as a call with zero tokens.
	OpCountTokens Operation = "count_tokens"
)

// UpstreamPathFor is the full provider path for one protocol/operation pair.
// An operation a protocol does not have returns ok=false: the gateway must
// refuse it, not invent a path the provider will 404 on.
func (p Protocol) UpstreamPathFor(op Operation) (string, bool) {
	root := p.UpstreamPath()
	switch op {
	case "", OpMessages:
		return root, true
	case OpCountTokens:
		if p == ProtocolOpenAI {
			return "", false
		}
		return root + "/count_tokens", true
	default:
		return "", false
	}
}

// Error classes. This is the complete vocabulary that can appear in a usage
// row's error_class and in a client-visible error body's machine-readable
// slot. It is deliberately small and closed: an operator greps it, and a
// client switches on it.
const (
	// ErrClassInvalidRequest — the body we were handed is not usable.
	ErrClassInvalidRequest = "invalid_request"
	// ErrClassRequestTooLarge — body exceeded the configured cap.
	ErrClassRequestTooLarge = "request_too_large"
	// ErrClassModelNotFound — `model` is not an enabled text row in the catalog.
	ErrClassModelNotFound = "model_not_found"
	// ErrClassInsufficientTier — the model exists but this membership cannot
	// use it. The body never enumerates what the caller COULD use; the
	// catalog endpoint is the place that answers that question, under its own
	// auth, and a probe against this endpoint must not become a second one.
	ErrClassInsufficientTier = "insufficient_tier"
	// ErrClassModelNotConfigured — catalog row has no upstreamModel mapping.
	ErrClassModelNotConfigured = "model_not_configured"
	// ErrClassOperationUnsupported — the route asked for an endpoint this
	// protocol does not have (count_tokens on OpenAI). An explicit refusal,
	// never a silent fall back to the completion endpoint.
	ErrClassOperationUnsupported = "operation_unsupported"
	// ErrClassProviderUnavailable — no healthy platform credential can serve
	// this protocol/tier right now.
	ErrClassProviderUnavailable = "provider_unavailable"
	// ErrClassRateLimited — OUR limiter said no (per-user rate, per-user
	// concurrency, or global concurrency).
	ErrClassRateLimited = "rate_limited"
	// ErrClassUpstreamInvalidRequest — upstream rejected the request shape.
	// The only upstream 4xx we forward as a 4xx, because it is the only one
	// the caller can act on.
	ErrClassUpstreamInvalidRequest = "upstream_invalid_request"
	// ErrClassUpstreamAuth — upstream rejected OUR credential. Never a 4xx to
	// the client: the caller did nothing wrong and must not learn that a
	// credential exists, let alone that it is broken.
	ErrClassUpstreamAuth = "upstream_auth"
	// ErrClassUpstreamRateLimited — upstream throttled us.
	ErrClassUpstreamRateLimited = "upstream_rate_limited"
	// ErrClassUpstreamError — any other upstream failure.
	ErrClassUpstreamError = "upstream_error"
	// ErrClassUpstreamTimeout — upstream did not answer inside our budget.
	ErrClassUpstreamTimeout = "upstream_timeout"
	// ErrClassGatewayDisabled — operator turned the tap off.
	ErrClassGatewayDisabled = "gateway_disabled"
	// ErrClassInternal — our bug.
	ErrClassInternal = "internal"
	// ErrClassUnauthorized — no usable identity on the request.
	ErrClassUnauthorized = "unauthorized"
)

// GatewayError is a refusal with everything needed to answer the client in
// its own protocol and to write an honest usage row. Message is ALWAYS text
// we wrote — no upstream body ever reaches this field.
type GatewayError struct {
	Status  int
	Class   string
	Message string
	// RetryAfterSeconds populates the Retry-After header when > 0.
	RetryAfterSeconds int
}

func (e *GatewayError) Error() string { return e.Class + ": " + e.Message }

func newError(status int, class, message string) *GatewayError {
	return &GatewayError{Status: status, Class: class, Message: message}
}

// anthropicErrorType maps our class onto the Anthropic error vocabulary, so a
// client built against the Messages API can switch on error.type as usual.
func anthropicErrorType(class string, status int) string {
	switch class {
	case ErrClassInvalidRequest, ErrClassUpstreamInvalidRequest:
		return "invalid_request_error"
	case ErrClassRequestTooLarge:
		return "request_too_large"
	case ErrClassUnauthorized:
		return "authentication_error"
	case ErrClassInsufficientTier:
		return "permission_error"
	case ErrClassModelNotFound:
		return "not_found_error"
	case ErrClassRateLimited, ErrClassUpstreamRateLimited:
		return "rate_limit_error"
	case ErrClassProviderUnavailable, ErrClassModelNotConfigured, ErrClassGatewayDisabled:
		return "overloaded_error"
	}
	if status >= http.StatusInternalServerError {
		return "api_error"
	}
	return "invalid_request_error"
}

// ErrorBody renders a GatewayError in the caller's protocol. Both shapes carry
// our class in a machine-readable slot so a Desktop can branch on
// "insufficient_tier" without string-matching prose.
func (e *GatewayError) ErrorBody(protocol Protocol) map[string]any {
	if protocol == ProtocolOpenAI {
		return map[string]any{
			"error": map[string]any{
				"message": e.Message,
				"type":    anthropicErrorType(e.Class, e.Status),
				"code":    e.Class,
				"param":   nil,
			},
		}
	}
	return map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    anthropicErrorType(e.Class, e.Status),
			"message": e.Message,
			// Non-standard but additive: the Anthropic shape has no slot for a
			// gateway-specific reason, and clients need one to tell "upgrade
			// your plan" apart from "we are out of capacity".
			"gateway_code": e.Class,
		},
	}
}

// classifyUpstreamStatus turns an upstream HTTP status into our class and the
// status WE return. The upstream body is never consulted and never forwarded.
//
// The load-bearing decisions:
//   - upstream 401/403 becomes 502, not 401/403. Those are OUR credential
//     failing; echoing them would tell an attacker that a key exists behind
//     the gateway and that probing changes its state.
//   - upstream 404 becomes 502. A catalog model we accepted but the provider
//     does not know is a mapping bug on our side, not a client error.
//   - upstream 400/422 stays a 400, because the caller's own request body is
//     the thing that has to change.
func classifyUpstreamStatus(status int) (int, string, string) {
	switch {
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return http.StatusBadRequest, ErrClassUpstreamInvalidRequest,
			"the upstream model provider rejected this request"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return http.StatusBadGateway, ErrClassUpstreamAuth,
			"the model gateway could not authenticate with the upstream provider"
	case status == http.StatusNotFound:
		return http.StatusBadGateway, ErrClassUpstreamError,
			"the upstream model provider does not serve the requested model"
	case status == http.StatusRequestEntityTooLarge:
		return http.StatusRequestEntityTooLarge, ErrClassUpstreamInvalidRequest,
			"the upstream model provider rejected this request as too large"
	case status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests, ErrClassUpstreamRateLimited,
			"the upstream model provider is rate limiting this gateway"
	case status == http.StatusGatewayTimeout || status == http.StatusRequestTimeout:
		return http.StatusGatewayTimeout, ErrClassUpstreamTimeout,
			"the upstream model provider timed out"
	case status >= http.StatusInternalServerError:
		return http.StatusBadGateway, ErrClassUpstreamError,
			"the upstream model provider is unavailable"
	default:
		return http.StatusBadGateway, ErrClassUpstreamError,
			"the upstream model provider returned an unexpected response"
	}
}
