package workagent

import (
	"encoding/json"
	"strings"
	"testing"
)

// invokeSafe is the panic guard for downstream callbacks. The
// contract: a callback panic must be absorbed (returns normally)
// and a label must reach the logger so ops can identify which
// site misbehaved. We can't easily assert the log capture, so
// the test pins the no-panic-leak guarantee — that's what the
// agent message loop relies on to keep streaming after a buggy
// callback.
func TestInvokeSafe_AbsorbsPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("invokeSafe leaked a panic: %v", r)
		}
	}()
	invokeSafe("test-panic", func() {
		panic("downstream callback exploded")
	})
	// Returning normally here is the actual assertion — if
	// invokeSafe re-raises the panic, the deferred recover
	// above fires and t.Errorf records the leak.
}

// Happy path: invokeSafe returns the same way a direct call would
// for a callback that completes cleanly. Pin this so a future
// "harden everything" pass can't accidentally swallow legitimate
// completion.
func TestInvokeSafe_RunsCleanCallback(t *testing.T) {
	called := false
	invokeSafe("test-clean", func() {
		called = true
	})
	if !called {
		t.Error("invokeSafe did not run the callback")
	}
}

// scrubCredentialsFromLog must mask the four credential shapes that
// upstream gateways and the SDK CLI's transport-error path can echo
// in stderr. False positives (legitimate diagnostic info masked)
// matter as much as false negatives (real creds leak), so we pin
// both directions.
func TestScrubCredentialsFromLog(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		mustNotEmit  []string // substrings that MUST NOT appear after scrub
		mustContain  []string // substrings that MUST still appear (diagnostic info preserved)
	}{
		{
			name:        "bearer_token",
			input:       `request failed: 401 with header Bearer abc123_def-456.789`,
			mustNotEmit: []string{"abc123_def-456.789"},
			mustContain: []string{"401", "Bearer [REDACTED]"},
		},
		{
			name:        "sk_anthropic_key",
			input:       `error: invalid api key sk-test-anthropic-fixture-000000000000`,
			mustNotEmit: []string{"sk-test-anthropic-fixture-000000000000"},
			mustContain: []string{"invalid api key", "sk-[REDACTED]"},
		},
		{
			name:        "auth_header_full_line",
			input:       `> Authorization: Bearer sk-test-bearer-fixture-000000000`,
			mustNotEmit: []string{"sk-test-bearer-fixture-000000000"},
			mustContain: []string{"Authorization: [REDACTED]"},
		},
		{
			name:        "json_api_key",
			input:       `{"api_key":"sk-test-json-fixture-000000000","model":"claude-3"}`,
			mustNotEmit: []string{"sk-test-json-fixture-000000000"},
			// model=claude-3 is non-secret diagnostic info; must survive.
			mustContain: []string{`"model":"claude-3"`},
		},
		{
			name:        "env_var_shape",
			input:       `ANTHROPIC_API_KEY=sk-test-env-fixture-000000000`,
			mustNotEmit: []string{"sk-test-env-fixture-000000000"},
			mustContain: []string{"api_key=[REDACTED]"},
		},
		{
			name:        "preserves_trace_ids_and_status_codes",
			input:       `request_id=abc123 status=429 quota=monthly trace=xyz789`,
			// Nothing matches a credential shape — input should pass
			// through unchanged. Pin this so a future "tighter regex"
			// pass doesn't accidentally redact short identifiers.
			mustNotEmit: []string{"REDACTED"},
			mustContain: []string{"request_id=abc123", "status=429", "trace=xyz789"},
		},
		{
			name:        "short_strings_dont_match_sk_pattern",
			// sk- followed by <20 chars must NOT match — too short to
			// be a real key, and we don't want to redact filenames
			// like "sk-12.txt".
			input:       `wrote sk-short.txt to disk`,
			mustNotEmit: []string{"REDACTED"},
			mustContain: []string{"sk-short.txt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scrubCredentialsFromLog(tc.input)
			for _, banned := range tc.mustNotEmit {
				if strings.Contains(got, banned) {
					t.Errorf("scrubbed output still contains %q\ninput:  %q\noutput: %q", banned, tc.input, got)
				}
			}
			for _, want := range tc.mustContain {
				if !strings.Contains(got, want) {
					t.Errorf("scrubbed output missing %q\ninput:  %q\noutput: %q", want, tc.input, got)
				}
			}
		})
	}
}

// classifyResultForPool decides how a turn's result frame affects the
// agent-account breaker. The truth table here pins the contract so a
// future SDK schema change or refactor can't silently flip 429 from
// neutral back to "success" (which would mask upstream throttling).
func TestClassifyResultForPool(t *testing.T) {
	intPtr := func(i int) *int { return &i }

	build := func(isError bool, apiStatus *int, subtype string) json.RawMessage {
		payload := map[string]interface{}{
			"type":     "result",
			"is_error": isError,
		}
		if apiStatus != nil {
			payload["api_error_status"] = *apiStatus
		}
		if subtype != "" {
			payload["subtype"] = subtype
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("build payload: %v", err)
		}
		return raw
	}

	cases := []struct {
		name string
		raw  json.RawMessage
		want poolOutcome
	}{
		{"empty bytes → success", json.RawMessage{}, poolOutcomeSuccess},
		{"nil bytes → success", nil, poolOutcomeSuccess},
		{"malformed json → success (preserves prior behaviour)", json.RawMessage("not json"), poolOutcomeSuccess},
		{"is_error=false → success", build(false, nil, ""), poolOutcomeSuccess},
		{"is_error=false with status (shouldn't happen but tolerate) → success", build(false, intPtr(429), ""), poolOutcomeSuccess},
		{"is_error=true 429 → neutral (upstream throttle, account fine)", build(true, intPtr(429), ""), poolOutcomeNeutral},
		{"is_error=true 401 → failure (bad creds)", build(true, intPtr(401), ""), poolOutcomeFailure},
		{"is_error=true 403 → failure (forbidden)", build(true, intPtr(403), ""), poolOutcomeFailure},
		{"is_error=true 500 → failure (upstream broke; account counter trips circuit breaker)", build(true, intPtr(500), ""), poolOutcomeFailure},
		{"is_error=true no status → failure (default for unknown)", build(true, nil, ""), poolOutcomeFailure},

		// Project-side kill-switch subtypes — must be Neutral, NOT
		// Failure. Penalising the account for OUR cap firing would
		// silently rotate accounts based on operator-set budget policy.
		{"is_error=true subtype=error_max_budget_usd no status → neutral (project cap, not account fault)", build(true, nil, "error_max_budget_usd"), poolOutcomeNeutral},
		{"is_error=true subtype=error_max_budget_usd with 500 status → neutral (subtype check wins)", build(true, intPtr(500), "error_max_budget_usd"), poolOutcomeNeutral},

		// An unknown error_* subtype must still be Failure — only
		// known project-side kill-switches get the neutral pass.
		{"is_error=true unknown error_unknown_subtype → failure (only whitelisted kill-switches are neutral)", build(true, nil, "error_unknown_subtype"), poolOutcomeFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := classifyResultForPool(tc.raw)
			if got != tc.want {
				t.Fatalf("classifyResultForPool() = %v, want %v\ninput: %s", got, tc.want, string(tc.raw))
			}
		})
	}
}

// derefStr is used in log lines for SDK fields that come as *string.
// A nil pointer must render empty, not crash. Pin the contract.
func TestDerefStr(t *testing.T) {
	if got := derefStr(nil); got != "" {
		t.Errorf("derefStr(nil) = %q, want \"\"", got)
	}
	s := "hello"
	if got := derefStr(&s); got != "hello" {
		t.Errorf("derefStr(&%q) = %q, want %q", s, got, s)
	}
	empty := ""
	if got := derefStr(&empty); got != "" {
		t.Errorf("derefStr(&\"\") = %q, want \"\"", got)
	}
}

// classifyResultForPool also returns the parsed status struct. The
// log lines downstream read Subtype + APIErrorStatus to render the
// reason; pin the round-trip so a refactor that drops or reshuffles
// fields gets caught by the test.
func TestClassifyResultForPool_PassesThroughStatusFields(t *testing.T) {
	raw := json.RawMessage(`{"type":"result","is_error":true,"subtype":"error_max_budget_usd","api_error_status":429,"stop_reason":"refusal","total_cost_usd":0.0123}`)
	_, status := classifyResultForPool(raw)
	if status.Subtype != "error_max_budget_usd" {
		t.Errorf("Subtype = %q, want \"error_max_budget_usd\"", status.Subtype)
	}
	if status.APIErrorStatus == nil || *status.APIErrorStatus != 429 {
		t.Errorf("APIErrorStatus = %v, want 429", status.APIErrorStatus)
	}
	if status.StopReason == nil || *status.StopReason != "refusal" {
		t.Errorf("StopReason = %v, want \"refusal\"", status.StopReason)
	}
	if status.TotalCostUSD == nil || *status.TotalCostUSD != 0.0123 {
		t.Errorf("TotalCostUSD = %v, want 0.0123", status.TotalCostUSD)
	}
}
