//go:build desktop

package pi_agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "server/desktop/agentruntime"
	cloudproxy "server/desktop/cloud_proxy"
)

// Every string below was captured from pi 0.84.1 against a scripted
// openai-completions endpoint, not invented: the two shapes are what its
// provider client actually produces, and the 502 is what a refused TCP
// connection becomes on the way through it.
func TestClassifyPiUpstreamMessage_RealShapes(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		kind      cloudproxy.ProxyErrorKind
		retryable bool
		wantIn    string // substring the user must be able to read
	}{
		{
			name:      "a 400 with the endpoint's own words",
			msg:       `400: {"message":"model foo does not exist","type":"invalid_request_error"}`,
			kind:      cloudproxy.KindBadRequest,
			retryable: false,
			wantIn:    "model foo does not exist",
		},
		{
			name:      "a 400 with nothing to say",
			msg:       "400 status code (no body)",
			kind:      cloudproxy.KindBadRequest,
			retryable: false,
			wantIn:    "模型 ID",
		},
		{
			name:      "a rejected key",
			msg:       `401: {"message":"invalid api key"}`,
			kind:      cloudproxy.KindAuthRequired,
			retryable: false,
			wantIn:    "API key",
		},
		{
			name:      "throttling",
			msg:       "429 status code (no body)",
			kind:      cloudproxy.KindRateLimited,
			retryable: true,
			wantIn:    "限流",
		},
		{
			// The one the task named: a refused connection, which pi reports as
			// a gateway status rather than as a transport error.
			name:      "nothing listening on the port",
			msg:       "502 status code (no body)",
			kind:      cloudproxy.KindNetworkUnavailable,
			retryable: true,
			wantIn:    "正在运行",
		},
		{
			name:      "the endpoint blew up",
			msg:       "500 status code (no body)",
			kind:      cloudproxy.KindServiceUnavailable,
			retryable: true,
			wantIn:    "服务端错误",
		},
		{
			name:      "the provider's meter",
			msg:       "402 status code (no body)",
			kind:      cloudproxy.KindQuotaExceeded,
			retryable: false,
			wantIn:    "额度",
		},
		{
			name:      "a base_url with no API behind it",
			msg:       "404 status code (no body)",
			kind:      cloudproxy.KindBadRequest,
			retryable: false,
			wantIn:    "base_url",
		},
		{
			name:      "too much to send",
			msg:       "413 status code (no body)",
			kind:      cloudproxy.KindPayloadTooLarge,
			retryable: false,
			wantIn:    "请求过大",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe, ok := classifyPiUpstreamMessage(tc.msg)
			if !ok {
				t.Fatalf("%q was not recognized as an upstream failure", tc.msg)
			}
			if pe.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", pe.Kind, tc.kind)
			}
			if pe.Retryable != tc.retryable {
				t.Errorf("retryable = %v, want %v", pe.Retryable, tc.retryable)
			}
			if !strings.Contains(pe.Message, tc.wantIn) {
				t.Errorf("message = %q, want it to carry %q", pe.Message, tc.wantIn)
			}
			if pe.Details["upstream_status"] == nil {
				t.Error("the status must survive into details for diagnosis")
			}
		})
	}
}

// Everything that is NOT one of pi's status-led shapes must fall through to the
// tolerant path. A parser that reads a leading number out of any sentence turns
// a token count into an HTTP status, and a guess dressed as a diagnosis is
// worse than admitting we do not know.
func TestClassifyPiUpstreamMessage_LeavesUnknownShapesAlone(t *testing.T) {
	for _, msg := range []string{
		"",
		"pi 模型调用失败",
		"429",                           // a status with nothing after it
		"400 tokens exceeded the limit", // a number that is not a status
		"the model returned 500 rows",
		"999 status code (no body)", // not an HTTP status at all
		"99: nope",
		"context length exceeded",
	} {
		if pe, ok := classifyPiUpstreamMessage(msg); ok {
			t.Errorf("%q was classified as %s/%q; it is not one of pi's shapes", msg, pe.Kind, pe.Message)
		}
	}
}

// End to end through the pump: a 400 on the wire must reach the caller as a
// typed RuntimeError, not as the old undifferentiated unknown.
func TestRunTurn_UpstreamFailureIsTyped(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"400: {\"message\":\"model foo does not exist\",\"type\":\"invalid_request_error\"}"}}`,
		`{"type":"agent_settled"}`,
	), nil)

	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("RunTurn err = %v, want a RuntimeError", err)
	}
	if re.Kind != cloudproxy.KindBadRequest {
		t.Errorf("kind = %q, want bad_request — a model id that does not exist is not retryable", re.Kind)
	}
	if re.Retryable {
		t.Error("retrying a request the endpoint will refuse again is not a recovery")
	}
	if !strings.Contains(re.Message, "model foo does not exist") {
		t.Errorf("message = %q, want the endpoint's own words", re.Message)
	}
}

// And an unreachable endpoint is a different sentence from a malformed request,
// which is the whole point of the exercise.
func TestRunTurn_UnreachableEndpointIsANetworkFailure(t *testing.T) {
	rt, _, _ := newTestRuntime(t, frames(
		promptOK,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"502 status code (no body)"}}`,
		`{"type":"agent_settled"}`,
	), nil)

	err := rt.RunTurn(context.Background(), turnInput(), (&captured{}).emit)
	var re *agentruntime.RuntimeError
	if !errors.As(err, &re) {
		t.Fatalf("RunTurn err = %v, want a RuntimeError", err)
	}
	if re.Kind != cloudproxy.KindNetworkUnavailable {
		t.Errorf("kind = %q, want network_unavailable", re.Kind)
	}
	if !re.Retryable {
		t.Error("an endpoint that is not up yet is worth trying again")
	}
}
