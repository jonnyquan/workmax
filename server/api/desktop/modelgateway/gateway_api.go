// Package modelgateway serves the Desktop model gateway at
// POST /api/desktop/model-gateway/{anthropic,openai}/...
//
// It is a thin shell: identity comes from the same Desktop OAuth Bearer
// middleware that guards /api/desktop/models and /api/desktop/sync/*, and
// everything else — catalog admission, tier enforcement, credential
// selection, streaming, metering, rate limiting — lives in
// server/service/desktop/modelgateway so it can be tested without gin.
package modelgateway

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/config"
	"server/middleware"
	gateway "server/service/desktop/modelgateway"
)

// GatewayApi holds the single shared Gateway. Constructed once in
// initialize/router.go rather than per request, so the upstream connection
// pool and the rate-limit registry are process-wide — a per-request limiter
// would limit nothing.
type GatewayApi struct {
	Gateway *gateway.Gateway
}

// NewGatewayApi wires the production gateway: the shared system DB, the
// server's model_gateway config block (nil is the default-on shape), the
// w_workagent_account pool for credentials, and the DB usage recorder.
func NewGatewayApi(db *gorm.DB, cfg *config.ModelGateway) *GatewayApi {
	return &GatewayApi{Gateway: gateway.New(db, cfg, gateway.Options{})}
}

// AnthropicMessages handles POST /api/desktop/model-gateway/anthropic/v1/messages.
//
// The path is shaped so the Desktop can set ANTHROPIC_BASE_URL to
// ".../api/desktop/model-gateway/anthropic" and let the packaged engine
// append /v1/messages itself — no client-side URL assembly, no chance of the
// two drifting apart.
func (a *GatewayApi) AnthropicMessages(c *gin.Context) {
	a.serve(c, gateway.ProtocolAnthropic, gateway.OpMessages)
}

// AnthropicCountTokens handles
// POST /api/desktop/model-gateway/anthropic/v1/messages/count_tokens.
//
// It is here because the packaged engine actually calls it: a path-recording
// probe against the real claude CLI (2.1.226) showed the CLI issuing
// POST /v1/messages/count_tokens?beta=true whenever a tool result needs
// sizing. It is not optional politeness — without this route the call fell
// off the end of the sidecar's gateway paths and the tool loop degraded
// silently.
func (a *GatewayApi) AnthropicCountTokens(c *gin.Context) {
	a.serve(c, gateway.ProtocolAnthropic, gateway.OpCountTokens)
}

// OpenAIChatCompletions handles
// POST /api/desktop/model-gateway/openai/v1/chat/completions, the shape the
// pi engine speaks.
func (a *GatewayApi) OpenAIChatCompletions(c *gin.Context) {
	a.serve(c, gateway.ProtocolOpenAI, gateway.OpMessages)
}

func (a *GatewayApi) serve(c *gin.Context, protocol gateway.Protocol, op gateway.Operation) {
	if a == nil || a.Gateway == nil {
		// Route-catalog and offline composition tests register routes without
		// a database. Fail closed and loudly rather than panicking.
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"type": "error",
			"error": gin.H{
				"type":         "overloaded_error",
				"message":      "the model gateway is not configured on this server",
				"gateway_code": "gateway_disabled",
			},
		})
		return
	}

	var uid uint
	if claims, ok := middleware.OAuthClaims(c); ok && claims != nil {
		uid = claims.BaseClaims.Id
	}

	// Hand the RAW writer to the service: gin's ResponseWriter implements
	// http.Flusher, and streaming correctness depends on flushing each SSE
	// frame as it arrives. Anything that buffers here turns a streaming
	// gateway into a batching one.
	a.Gateway.HandleOperation(c.Writer, c.Request, protocol, op, uid)
	// The service owns the whole response, so stop gin's middleware chain
	// from appending anything after it.
	c.Abort()
}
