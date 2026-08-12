//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// fakeNetErr satisfies net.Error so we can exercise the Timeout()
// branch without standing up a real socket.
type fakeNetErr struct {
	msg     string
	timeout bool
}

func (e fakeNetErr) Error() string   { return e.msg }
func (e fakeNetErr) Timeout() bool   { return e.timeout }
func (e fakeNetErr) Temporary() bool { return e.timeout }

func TestClassifyTokenStoreError_NoSession(t *testing.T) {
	got := ClassifyTokenStoreError(ErrNoSession)
	if got.Kind != KindAuthRequired {
		t.Errorf("kind: got %q, want %q", got.Kind, KindAuthRequired)
	}
	if got.Retryable {
		t.Error("auth_required must not be retryable")
	}
}

func TestClassifyTokenStoreError_OtherError(t *testing.T) {
	got := ClassifyTokenStoreError(errors.New("keychain locked"))
	if got.Kind != KindAuthRequired {
		t.Errorf("kind: got %q, want %q", got.Kind, KindAuthRequired)
	}
	if got.Details["reason"] != "keychain locked" {
		t.Errorf("details.reason missing or wrong: %v", got.Details)
	}
}

func TestClassifyNetworkError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantKind  ProxyErrorKind
		retryable bool
		msgSubstr string
	}{
		{"context cancelled", context.Canceled, KindNetworkUnavailable, true, "取消"},
		{"deadline exceeded", context.DeadlineExceeded, KindNetworkUnavailable, true, "取消"},
		{"timeout", fakeNetErr{msg: "i/o timeout", timeout: true}, KindNetworkUnavailable, true, "超时"},
		{"generic", errors.New("connection refused"), KindNetworkUnavailable, true, "网络连接失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNetworkError(tc.err)
			if got.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Retryable != tc.retryable {
				t.Errorf("retryable: got %v, want %v", got.Retryable, tc.retryable)
			}
			if !strings.Contains(got.Message, tc.msgSubstr) {
				t.Errorf("message %q missing substr %q", got.Message, tc.msgSubstr)
			}
		})
	}
}

func TestClassifyHTTPResponse(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   http.Header
		body      string
		wantKind  ProxyErrorKind
		retryable bool
		retryMS   int
	}{
		{
			name:      "401 invalid_token",
			status:    401,
			headers:   http.Header{"Www-Authenticate": []string{`Bearer error="invalid_token"`}},
			wantKind:  KindAuthExpired,
			retryable: false,
		},
		{
			name:      "401 no header still maps to auth_expired",
			status:    401,
			wantKind:  KindAuthExpired,
			retryable: false,
		},
		{
			name:      "402 quota",
			status:    402,
			wantKind:  KindQuotaExceeded,
			retryable: false,
		},
		{
			name:      "403 quota_exceeded body",
			status:    403,
			body:      `{"error":"quota_exceeded","message":"out of credits"}`,
			wantKind:  KindQuotaExceeded,
			retryable: false,
		},
		{
			name:      "403 other reason → bad_request",
			status:    403,
			body:      `{"error":"feature_disabled"}`,
			wantKind:  KindBadRequest,
			retryable: false,
		},
		{
			name:      "429 with Retry-After",
			status:    429,
			headers:   http.Header{"Retry-After": []string{"7"}},
			wantKind:  KindRateLimited,
			retryable: true,
			retryMS:   7000,
		},
		{
			name:      "429 without Retry-After",
			status:    429,
			wantKind:  KindRateLimited,
			retryable: true,
			retryMS:   0,
		},
		{
			name:      "413",
			status:    413,
			wantKind:  KindPayloadTooLarge,
			retryable: false,
		},
		{
			name:      "500",
			status:    500,
			wantKind:  KindServiceUnavailable,
			retryable: true,
		},
		{
			name:      "502 also service",
			status:    502,
			wantKind:  KindServiceUnavailable,
			retryable: true,
		},
		{
			name:      "400 with error.message",
			status:    400,
			body:      `{"error":{"message":"bad input"}}`,
			wantKind:  KindBadRequest,
			retryable: false,
		},
		{
			name:      "400 with bare error field",
			status:    400,
			body:      `{"error":"missing field"}`,
			wantKind:  KindBadRequest,
			retryable: false,
		},
		{
			name:      "300 unexpected",
			status:    300,
			wantKind:  KindUnknown,
			retryable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Header:     tc.headers,
			}
			if resp.Header == nil {
				resp.Header = http.Header{}
			}
			got := ClassifyHTTPResponse(resp, []byte(tc.body))
			if got.Kind != tc.wantKind {
				t.Errorf("kind: got %q, want %q", got.Kind, tc.wantKind)
			}
			if got.Retryable != tc.retryable {
				t.Errorf("retryable: got %v, want %v", got.Retryable, tc.retryable)
			}
			if tc.retryMS != 0 && got.RetryAfterMS != tc.retryMS {
				t.Errorf("retry_after_ms: got %d, want %d", got.RetryAfterMS, tc.retryMS)
			}
			if tc.status >= 400 {
				if got.Details["upstream_status"] != tc.status {
					t.Errorf("details.upstream_status: got %v, want %d", got.Details["upstream_status"], tc.status)
				}
			}
		})
	}
}

func TestClassifyHTTPResponse_400ExtractsUpstreamMessage(t *testing.T) {
	resp := &http.Response{StatusCode: 400, Header: http.Header{}}
	body := []byte(`{"error":{"message":"thread_id is required"}}`)
	got := ClassifyHTTPResponse(resp, body)
	if got.Message != "thread_id is required" {
		t.Errorf("want extracted upstream message, got %q", got.Message)
	}
}

func TestClassifyHTTPResponse_RedactsUpstreamErrorSecrets(t *testing.T) {
	resp := &http.Response{StatusCode: 400, Header: http.Header{}}
	body := []byte(`{"error":{"message":"Authorization: Bearer access-secret Basic bare-basic-secret https://user:pass@example.com/path?refresh_token=refresh-secret&access_token=access-secret&api-key=api-secret X-Local-Token=local-secret client_secret=client-secret password=password-secret secret=generic-secret"}}`)
	got := ClassifyHTTPResponse(resp, body)
	serialized := proxyErrorStringForTest(got)
	for _, secret := range []string{
		"access-secret",
		"bare-basic-secret",
		"refresh-secret",
		"user:pass",
		"api-secret",
		"local-secret",
		"client-secret",
		"password-secret",
		"generic-secret",
	} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("proxy error leaked %q: %s", secret, serialized)
		}
	}
	if !strings.Contains(serialized, "[REDACTED]") {
		t.Fatalf("expected redaction marker in proxy error: %s", serialized)
	}
}

func TestClassifyHTTPResponse_BodyPrefixTruncates(t *testing.T) {
	resp := &http.Response{StatusCode: 500, Header: http.Header{}}
	huge := strings.Repeat("a", 1024)
	got := ClassifyHTTPResponse(resp, []byte(huge))
	prefix, _ := got.Details["upstream_body_prefix"].(string)
	if len(prefix) <= 512 {
		// 512 chars + "…" = 515 runes (but each "…" is 3 bytes)
		// so we expect a string longer than 512 chars but shorter than 1024
	}
	if !strings.HasSuffix(prefix, "…") {
		t.Errorf("expected truncation marker, got suffix of length %d", len(prefix))
	}
}

func TestBodyPrefix_RedactsJSONSecrets(t *testing.T) {
	body := []byte(`{"access_token":"access-secret","refresh_token":"refresh-secret","nested":{"client_secret":"client-secret","api-key":"api-secret"},"message":"Authorization: Basic basic-secret X-Local-Token: local-secret"}`)
	got := bodyPrefix(body)
	for _, secret := range []string{"access-secret", "refresh-secret", "client-secret", "api-secret", "basic-secret", "local-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("bodyPrefix leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker in bodyPrefix: %s", got)
	}
}

func TestClassifyNetworkError_RedactsReason(t *testing.T) {
	got := ClassifyNetworkError(errors.New("dial https://user:pass@example.com?access_token=secret-token: Basic basic-secret"))
	serialized := proxyErrorStringForTest(got)
	for _, secret := range []string{"user:pass", "secret-token", "basic-secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("proxy error leaked %q: %s", secret, serialized)
		}
	}
}

func TestSanitizeProxyError_RedactsSensitiveDetailKeys(t *testing.T) {
	got := sanitizeProxyError(ProxyError{
		Kind:    KindUnknown,
		Message: "failed",
		Details: map[string]any{
			"client_secret": "client-secret",
			"password":      12345,
			"nested": map[string]any{
				"apikey": "api-secret",
			},
			"access_token=key-secret": "value",
		},
	})
	serialized := proxyErrorStringForTest(got)
	for _, secret := range []string{"client-secret", "12345", "api-secret", "key-secret"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("proxy error leaked %q: %s", secret, serialized)
		}
	}
	if !strings.Contains(serialized, "[REDACTED]") {
		t.Fatalf("expected redaction marker in proxy error: %s", serialized)
	}
}

func TestClassifyUpstreamStreamError(t *testing.T) {
	got := ClassifyUpstreamStreamError(errors.New("unexpected EOF"))
	if got.Kind != KindServiceUnavailable {
		t.Errorf("kind: got %q, want service_unavailable", got.Kind)
	}
	if !got.Retryable {
		t.Error("mid-stream upstream error should be retryable")
	}

	nilGot := ClassifyUpstreamStreamError(nil)
	if nilGot.Kind != KindUnknown {
		t.Errorf("nil err → unknown; got %q", nilGot.Kind)
	}
}

func TestParseRetryAfterMS(t *testing.T) {
	cases := map[string]int{
		"":        0,
		"0":       0,
		"3":       3000,
		"  12  ":  12000,
		"abc":     0,
		"-5":      0,
		"Wed, 21": 0, // HTTP-date form not supported
		"100":     100000,
	}
	for in, want := range cases {
		if got := parseRetryAfterMS(in); got != want {
			t.Errorf("parseRetryAfterMS(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestExtractErrorMessage(t *testing.T) {
	cases := map[string]string{
		`{"error":{"message":"bad"}}`:   "bad",
		`{"error_description":"oh no"}`: "oh no",
		`{"error":"plain"}`:             "plain",
		`{"unrelated":"hi"}`:            "",
		``:                              "",
		`not json at all`:               "",
		`{"error":{"message":"` + strings.Repeat("x", 250) + `"}}`: "", // too long, skipped
	}
	for in, want := range cases {
		if got := extractErrorMessage([]byte(in)); got != want {
			t.Errorf("extractErrorMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func proxyErrorStringForTest(pe ProxyError) string {
	parts := []string{pe.Message, pe.LogID}
	for key, value := range pe.Details {
		parts = append(parts, key, strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(valueToStringForTest(value), "\n", " "), "\t", " ")))
	}
	return strings.Join(parts, " ")
}

func valueToStringForTest(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		parts := make([]string, 0, len(v))
		for key, item := range v {
			parts = append(parts, key+"="+valueToStringForTest(item))
		}
		return strings.Join(parts, " ")
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			parts = append(parts, valueToStringForTest(item))
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

// One taxonomy, two voices. ClassifyLocalUpstreamError writes for a user
// looking at their OWN endpoint — their API key, their provider's bill, their
// Ollama that is not running — so the sentences differ on purpose. The KIND
// must not: a renderer, a log reader, and the cache classifier all switch on
// it, and a status that means "rate limited" on one path cannot mean something
// else on the other.
//
// The two exceptions are listed rather than tolerated, because each is a
// deliberate re-reading of the same status for a different owner.
func TestLocalUpstreamClassificationSharesTheCloudTaxonomy(t *testing.T) {
	deliberate := map[int]struct {
		kind ProxyErrorKind
		why  string
	}{
		// The cloud's 401 means the workmax session died and only re-login
		// fixes it. A local endpoint's 401 means the key in Settings is wrong,
		// which is a credential the user supplies, not one that expired.
		401: {KindAuthRequired, "a local 401 is the user's API key, not their workmax login"},
		// Likewise 403: the cloud reads a non-quota 403 as a plain refusal;
		// from a local endpoint it is the same rejected credential as a 401.
		403: {KindAuthRequired, "a local 403 is the same rejected credential"},
		// pi turns a refused TCP connection into a synthetic 502, so a local
		// 502 is overwhelmingly "nothing is listening" rather than "a gateway
		// is down" — measured against pi 0.84.1. 500/503/504 keep the cloud's
		// reading; only the ambiguous one diverges.
		502: {KindNetworkUnavailable, "pi reports a refused connection as a 502"},
		// The cloud's 404 falls into the 4xx catch-all; a local 404 is the
		// base_url, which is worth its own sentence but the same kind.
		// (Kind agrees there — not an exception, just worth knowing.)
	}
	for status := 400; status < 600; status++ {
		resp := &http.Response{StatusCode: status, Header: http.Header{}}
		cloud := ClassifyHTTPResponse(resp, nil)
		local := ClassifyLocalUpstreamError(status, "")
		if want, ok := deliberate[status]; ok {
			if local.Kind != want.kind {
				t.Errorf("status %d: local kind = %q, want %q (%s)", status, local.Kind, want.kind, want.why)
			}
			continue
		}
		if cloud.Kind != local.Kind {
			t.Errorf("status %d: cloud kind %q vs local kind %q — one status, one meaning",
				status, cloud.Kind, local.Kind)
		}
		if cloud.Retryable != local.Retryable {
			t.Errorf("status %d: cloud retryable %v vs local %v", status, cloud.Retryable, local.Retryable)
		}
	}
}

// Every kind this classifier can produce must be one the renderer accepts —
// the enum is closed, and a kind outside it is dropped as a malformed frame.
func TestLocalUpstreamClassificationStaysInsideTheEnum(t *testing.T) {
	known := map[ProxyErrorKind]bool{
		KindNetworkUnavailable: true, KindAuthRequired: true, KindAuthExpired: true,
		KindServiceUnavailable: true, KindQuotaExceeded: true, KindRateLimited: true,
		KindPayloadTooLarge: true, KindBadRequest: true, KindSessionChanged: true,
		KindUnknown: true,
	}
	for _, status := range []int{0, 99, 100, 204, 301, 400, 401, 402, 403, 404, 413, 429, 500, 502, 503, 504, 599, 600} {
		pe := ClassifyLocalUpstreamError(status, "")
		if !known[pe.Kind] {
			t.Fatalf("status %d produced kind %q, which is outside the closed enum", status, pe.Kind)
		}
		if pe.Message == "" {
			t.Fatalf("status %d produced no message for the user to read", status)
		}
	}
}
