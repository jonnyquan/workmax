package modelgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	gateway "server/service/desktop/modelgateway"
)

// Route-catalog and offline composition tests register routes without a
// database, so the shell has to answer rather than panic — a nil-deref here
// would take down every request on the process, not just this one.
func TestGatewayApi_UnconfiguredFailsClosedInsteadOfPanicking(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name string
		api  *GatewayApi
	}{
		{name: "nil api", api: nil},
		{name: "nil gateway", api: &GatewayApi{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			engine.POST("/messages", tc.api.AnthropicMessages)

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages",
				strings.NewReader(`{"model":"work-pro"}`)))

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (raw %s)", err, w.Body.String())
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("no error object: %s", w.Body.String())
			}
			if errObj["gateway_code"] != "gateway_disabled" {
				t.Errorf("gateway_code = %v, want gateway_disabled", errObj["gateway_code"])
			}
		})
	}
}

// The gateway streams SSE by flushing each frame as it arrives. That only
// works because gin's ResponseWriter is an http.Flusher — if that ever
// changed, streaming would silently degrade to batching, which is the kind of
// regression nobody notices until a tool loop feels broken.
func TestGatewayApi_GinWriterCanFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.POST("/messages", func(c *gin.Context) {
		if _, ok := c.Writer.(http.Flusher); !ok {
			t.Error("gin.ResponseWriter is no longer an http.Flusher; the gateway can no longer stream")
		}
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages", nil))
}

// The two routes must not share a protocol: an Anthropic-shaped request
// answered with an OpenAI error body (or vice versa) breaks every client that
// switches on the provider's own error vocabulary.
func TestGatewayApi_RoutesCarryDistinctProtocols(t *testing.T) {
	if gateway.ProtocolAnthropic == gateway.ProtocolOpenAI {
		t.Fatal("the two protocol constants collapsed")
	}
	if gateway.ProtocolAnthropic.UpstreamPath() != "/v1/messages" {
		t.Errorf("anthropic upstream path = %q", gateway.ProtocolAnthropic.UpstreamPath())
	}
	if gateway.ProtocolOpenAI.UpstreamPath() != "/v1/chat/completions" {
		t.Errorf("openai upstream path = %q", gateway.ProtocolOpenAI.UpstreamPath())
	}
}
