//go:build desktop

package pi_agent

import (
	"strconv"
	"strings"

	cloudproxy "server/desktop/cloud_proxy"
)

// pi runs the model call inside its own subprocess, so the only thing that
// reaches this side is the sentence it puts on `message_end.errorMessage`. Two
// shapes, measured against pi 0.84.1 driving an openai-completions provider:
//
//	"400: {\"message\":\"model foo does not exist\",…}"   — status, then body
//	"429 status code (no body)"                            — status, no body
//
// Both lead with the HTTP status, which is the whole classification. A refused
// TCP connection arrives as "502 status code (no body)" — pi's client turns a
// transport failure into a synthetic gateway status, so "cannot connect" and
// "the gateway is down" are one branch downstream, as they should be.
//
// Anything else — a message with no leading status, a shape a later pi
// invents — returns ok=false, and the caller keeps the tolerant fallback. A
// guess dressed as a diagnosis is worse than admitting we do not know.
const maxPiUpstreamBodyBytes = 4096

func classifyPiUpstreamMessage(msg string) (cloudproxy.ProxyError, bool) {
	status, body, ok := parsePiUpstreamMessage(msg)
	if !ok {
		return cloudproxy.ProxyError{}, false
	}
	return cloudproxy.ClassifyLocalUpstreamError(status, body), true
}

// parsePiUpstreamMessage splits pi's sentence into (status, body). The status
// must lead and must be followed by ":" or " status code" — a bare number
// anywhere in a free-text error is somebody's token count, not an HTTP status.
func parsePiUpstreamMessage(msg string) (int, string, bool) {
	msg = strings.TrimSpace(msg)
	if len(msg) < 3 {
		return 0, "", false
	}
	status, err := strconv.Atoi(msg[:3])
	if err != nil || status < 100 || status > 599 {
		return 0, "", false
	}
	rest := msg[3:]
	switch {
	case strings.HasPrefix(rest, ":"):
		body := strings.TrimSpace(strings.TrimPrefix(rest, ":"))
		if len(body) > maxPiUpstreamBodyBytes {
			body = body[:maxPiUpstreamBodyBytes]
		}
		return status, body, true
	case strings.HasPrefix(rest, " status code"):
		return status, "", true
	}
	return 0, "", false
}
