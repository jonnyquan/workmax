package modelgateway

import (
	"net/http"
	"testing"
)

// A relay's base URL is operator-typed data. Every plausible shape has to
// resolve to exactly one correct endpoint, because the failure mode of
// getting it wrong is a provider 404 that reads like an outage.
func TestUpstreamURL(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		protocol Protocol
		op       Operation
		want     string
		wantErr  bool
	}{
		{name: "bare host", base: "https://api.example.com", protocol: ProtocolAnthropic,
			want: "https://api.example.com/v1/messages"},
		{name: "trailing slash", base: "https://api.example.com/", protocol: ProtocolAnthropic,
			want: "https://api.example.com/v1/messages"},
		{name: "path prefix", base: "https://relay.example.com/anthropic", protocol: ProtocolAnthropic,
			want: "https://relay.example.com/anthropic/v1/messages"},
		{
			// An operator who pasted the full endpoint must not get
			// /v1/messages/v1/messages.
			name: "base already ends in the protocol path", base: "https://api.example.com/v1/messages",
			protocol: ProtocolAnthropic, want: "https://api.example.com/v1/messages",
		},
		{name: "openai", base: "https://api.example.com", protocol: ProtocolOpenAI,
			want: "https://api.example.com/v1/chat/completions"},
		{name: "empty", base: "", protocol: ProtocolAnthropic, wantErr: true},
		// A scheme we do not control is not a provider endpoint; refusing here
		// keeps a mis-typed row from becoming an SSRF primitive.
		{name: "non-http scheme", base: "file:///etc/passwd", protocol: ProtocolAnthropic, wantErr: true},
		{name: "no scheme", base: "api.example.com", protocol: ProtocolAnthropic, wantErr: true},

		// count_tokens: the endpoint the packaged claude CLI actually calls.
		{name: "count_tokens bare host", base: "https://api.example.com",
			protocol: ProtocolAnthropic, op: OpCountTokens,
			want: "https://api.example.com/v1/messages/count_tokens"},
		{name: "count_tokens path prefix", base: "https://relay.example.com/anthropic",
			protocol: ProtocolAnthropic, op: OpCountTokens,
			want: "https://relay.example.com/anthropic/v1/messages/count_tokens"},
		{
			// The operator pasted the completion endpoint; count_tokens must
			// extend it rather than double it.
			name:     "count_tokens on a base that ends in the protocol path",
			base:     "https://api.example.com/v1/messages",
			protocol: ProtocolAnthropic, op: OpCountTokens,
			want: "https://api.example.com/v1/messages/count_tokens",
		},
		{
			name:     "count_tokens on a base that already ends in count_tokens",
			base:     "https://api.example.com/v1/messages/count_tokens",
			protocol: ProtocolAnthropic, op: OpCountTokens,
			want: "https://api.example.com/v1/messages/count_tokens",
		},
		// OpenAI has no token counter. Refuse rather than invent a path.
		{name: "count_tokens is not an OpenAI endpoint", base: "https://api.example.com",
			protocol: ProtocolOpenAI, op: OpCountTokens, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := upstreamURL(tc.base, tc.protocol, tc.op)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("upstreamURL(%q) = %q, want an error", tc.base, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("upstreamURL(%q): %v", tc.base, err)
			}
			if got != tc.want {
				t.Errorf("upstreamURL(%q) = %q, want %q", tc.base, got, tc.want)
			}
		})
	}
}

// The pool is historically all Anthropic relays, many with an empty provider
// string. Defaulting unknown values the other way would strand every existing
// row; defaulting an OpenAI request to an Anthropic account would send a body
// the provider cannot parse.
func TestProviderSpeaks(t *testing.T) {
	cases := []struct {
		provider string
		protocol Protocol
		want     bool
	}{
		{"", ProtocolAnthropic, true},
		{"anthropic", ProtocolAnthropic, true},
		{"Claude-Relay", ProtocolAnthropic, true},
		{"openai", ProtocolAnthropic, false},
		{"azure-openai", ProtocolAnthropic, false},
		{"openai", ProtocolOpenAI, true},
		{"OpenAI-Compatible", ProtocolOpenAI, true},
		{"deepseek", ProtocolOpenAI, true},
		{"anthropic", ProtocolOpenAI, false},
		{"", ProtocolOpenAI, false},
	}
	for _, tc := range cases {
		if got := providerSpeaks(tc.provider, tc.protocol); got != tc.want {
			t.Errorf("providerSpeaks(%q, %q) = %v, want %v", tc.provider, tc.protocol, got, tc.want)
		}
	}
}

// The response allowlist exists because upstream headers routinely carry
// rate-limit state and request ids tied to OUR account.
func TestCopyResponseHeaders_OnlyForwardsTheAllowlist(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "text/event-stream")
	src.Set("Cache-Control", "no-cache")
	src.Set("Request-Id", "req_upstream_secret")
	src.Set("Anthropic-Ratelimit-Requests-Remaining", "3")
	src.Set("Set-Cookie", "session=upstream")
	src.Set("Transfer-Encoding", "chunked")

	dst := http.Header{}
	copyResponseHeaders(dst, src)

	if dst.Get("Content-Type") != "text/event-stream" {
		t.Error("Content-Type must be forwarded — the client needs to know it is reading SSE")
	}
	if dst.Get("Cache-Control") != "no-cache" {
		t.Error("Cache-Control must be forwarded")
	}
	for _, forbidden := range []string{"Request-Id", "Anthropic-Ratelimit-Requests-Remaining", "Set-Cookie", "Transfer-Encoding"} {
		if dst.Get(forbidden) != "" {
			t.Errorf("header %q leaked through the allowlist", forbidden)
		}
	}
}

func TestNumericRetryAfter(t *testing.T) {
	cases := map[string]int{
		"7":   7,
		" 12": 12,
		"0":   1,
		"-5":  1,
		"":    1,
		// The HTTP-date form is dropped rather than converted: it would echo
		// the provider's clock, which is one more fact about our upstream
		// than a client needs.
		"Wed, 21 Oct 2026 07:28:00 GMT": 1,
		"99999":                         300,
	}
	for input, want := range cases {
		if got := numericRetryAfter(input); got != want {
			t.Errorf("numericRetryAfter(%q) = %d, want %d", input, got, want)
		}
	}
}
