package workagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"server/globals"
	workagentModel "server/model/workagent"
)

// testSessionSeq monotonically increments to disambiguate test-session
// IDs minted in the same nanosecond — same posture as sseConnSeq in
// the SSE connection manager (41a9f8bf). Two concurrent admin "Test
// connection" clicks for the same account within a second otherwise
// collided in the SDK's session-state tracking, leading to confusing
// cross-test interference (the SDK reused state from the prior run).
var testSessionSeq atomic.Uint64

// TestResult captures the outcome of a per-account smoke test.
// Returned both to the admin UI and used by performRealAPITest to
// decide whether to bump failure / success counters in DB.
type TestResult struct {
	Success      bool
	Message      string
	Error        string
	ResponseTime string
	Response     string // Agent返回的响应内容
}

// TestAccount sends a real "hi" prompt through the SDK with the
// account's credentials and reports back what happened. Used by the
// admin UI's "Test connection" button. Errors and responses both
// flow into the per-account stats counters via the same submitStatUpdate
// queue as production traffic so the UI reflects the test outcome
// next time it polls.
func (p *AgentAccountPool) TestAccount(accountID uint64) (map[string]interface{}, error) {
	var account workagentModel.AgentAccount
	err := globals.GraDBs["system"].Where("id = ? AND deleted_at IS NULL", accountID).First(&account).Error
	if err != nil {
		return nil, fmt.Errorf("account %d not found", accountID)
	}

	if account.BaseURL == "" {
		return nil, errors.New("base_url is empty")
	}
	if account.APIKey == "" {
		return nil, errors.New("api_key is empty")
	}

	p.mu.RLock()
	cbState := "unknown"
	if cb := p.circuitBreakers[accountID]; cb != nil {
		cbState = cb.GetState()
	}
	p.mu.RUnlock()

	globals.Info(fmt.Sprintf("[AgentAccountPool] Testing account: %s (Provider: %s, ID: %d)",
		account.Name, account.Provider, accountID))

	testResult := p.performRealAPITest(&account)

	return map[string]interface{}{
		"success":        testResult.Success,
		"account":        account.Name,
		"provider":       account.Provider,
		"baseUrl":        account.BaseURL,
		"circuitBreaker": cbState,
		"message":        testResult.Message,
		"error":          testResult.Error,
		"responseTime":   testResult.ResponseTime,
	}, nil
}

// performRealAPITest fires a real Agent SDK request against the
// account, classifies the result, and feeds the per-account stats
// counters in the same way production traffic does — so a failed
// test correctly increments consecutive_failures and can trip the
// circuit breaker, just as if a user had hit that account.
//
// Three outcome paths:
//
//	error from the SDK         → recordTestFailure with classified message
//	success but is_error result → recordTestFailure with extracted err
//	clean success              → recordTestSuccess
func (p *AgentAccountPool) performRealAPITest(account *workagentModel.AgentAccount) TestResult {
	startTime := time.Now()
	globals.Info(fmt.Sprintf("[AgentAccountPool] Sending test request to %s", account.Provider))

	testManager := &AgentClientManager{}
	if err := testManager.ReinitializeWithAccount(account); err != nil {
		globals.Error(fmt.Sprintf("[AgentAccountPool] Test failed - initialization error: %v", err))
		return TestResult{
			Success: false,
			Error:   fmt.Sprintf("Failed to initialize client: %v", err),
			Message: "Client initialization failed",
		}
	}

	// 5-minute timeout — Agent SDK initialization + first response can
	// take a while, especially on cold model gateways.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	testSessionID := fmt.Sprintf("test-session-%d-%d", time.Now().UnixNano(), testSessionSeq.Add(1))

	// Direct call to the SDK rather than through ProcessAgentConversationWithRetry
	// because retry would re-resolve the active account, but here we want
	// to test the *given* account, not whichever one's currently active.
	conversation, err := testManager.client.ProcessAgentConversation(
		ctx,
		"hi",
		testSessionID,
		"",
		"",
		AgentCallbacks{},
	)

	elapsed := time.Since(startTime)
	responseTime := fmt.Sprintf("%.2fs", elapsed.Seconds())

	if err != nil {
		return p.recordTestFailure(account, classifyTestError(err), responseTime)
	}

	// Even on a success-the-protocol return, the SDK can encode an
	// in-band error in the result body. Detect that and treat as a
	// failure for stats purposes.
	fullResponse := ""
	responseText := "[no content]"
	if conversation != nil && len(conversation.Result) > 0 {
		fullResponse = string(conversation.Result)
		responseText = fullResponse
		// Rune-safe truncation: a CJK gateway error response (智谱
		// GLM, Tongyi, etc.) commonly carries multi-byte UTF-8
		// characters and a naive byte slice at index 200 routinely
		// lands inside a 3-byte sequence, producing invalid UTF-8
		// in the log line and the admin response panel. Same fix
		// already applied in 1f73df4e (GetContentSummary) and
		// fa2141f4 (RecordFailure last_error).
		runes := []rune(responseText)
		if len(runes) > 200 {
			responseText = string(runes[:200]) + "..."
		}
	}
	globals.Info(fmt.Sprintf("[AgentAccountPool] Test response: %s", responseText))

	if isInBandTestError(fullResponse) {
		globals.Error(fmt.Sprintf("[AgentAccountPool] Test failed for account %s: API returned error", account.Name))
		errMsg := classifyInBandTestError(fullResponse)
		return p.recordTestFailure(account, errMsg, responseTime)
	}

	globals.Info(fmt.Sprintf("[AgentAccountPool] Test passed for account %s (Response time: %s)",
		account.Name, responseTime))
	p.recordTestSuccess(account)

	return TestResult{
		Success:      true,
		Message:      "API connection test passed successfully",
		ResponseTime: responseTime,
		Response:     responseText,
	}
}

// classifyTestError maps an SDK / transport error to a user-facing
// {Error, Message} pair. Falls back to the raw error message when
// none of the known patterns match.
type classifiedTestError struct {
	Err string
	Msg string
}

func classifyTestError(err error) classifiedTestError {
	errMsg := err.Error()
	switch {
	case strings.Contains(errMsg, "401"),
		strings.Contains(errMsg, "unauthorized"),
		strings.Contains(errMsg, "authentication_error"):
		return classifiedTestError{"API Key authentication failed", "Invalid API Key"}
	case strings.Contains(errMsg, "timeout"),
		strings.Contains(errMsg, "deadline exceeded"):
		return classifiedTestError{"Request timeout", "Connection timeout"}
	case strings.Contains(errMsg, "connection refused"),
		strings.Contains(errMsg, "no such host"):
		return classifiedTestError{"Connection failed", "Cannot reach the API endpoint"}
	default:
		return classifiedTestError{errMsg, "API test failed"}
	}
}

// isInBandTestError reports whether a successful-the-protocol response
// body actually contains an embedded error. Some providers return 200
// with `{"is_error": true, ...}` instead of an HTTP error.
func isInBandTestError(fullResponse string) bool {
	return strings.Contains(fullResponse, `"is_error":true`) ||
		strings.Contains(fullResponse, "API Error") ||
		strings.Contains(fullResponse, `"type":"error"`)
}

// classifyInBandTestError extracts a short user-facing message from the
// in-band error body. Pattern-matched against the known shapes — falls
// back to a generic "check logs" message when none match.
func classifyInBandTestError(fullResponse string) classifiedTestError {
	switch {
	case strings.Contains(fullResponse, "Insufficient balance"):
		return classifiedTestError{"Insufficient balance or no resource package", "API test failed"}
	case strings.Contains(fullResponse, "429"):
		return classifiedTestError{"Rate limit exceeded or quota exhausted", "API test failed"}
	case strings.Contains(fullResponse, "authentication"),
		strings.Contains(fullResponse, "401"):
		return classifiedTestError{"Authentication failed", "API test failed"}
	default:
		return classifiedTestError{"API returned error, check logs for details", "API test failed"}
	}
}

// recordTestFailure submits the same failure-stats UPDATE that
// production failure handling submits, so a failed test counts toward
// the breaker's failure threshold.
func (p *AgentAccountPool) recordTestFailure(account *workagentModel.AgentAccount, classified classifiedTestError, responseTime string) TestResult {
	accID := account.ID
	errMsgCaptured := classified.Err
	// Byte-cap with a rune-boundary walk-back, matching the production
	// RecordFailure fix (cb8f207c). The previous shape mixed metrics:
	// outer `len > recordFailureMaxErrLen` was byte length, inner
	// `runes > recordFailureMaxErrLen` was rune count. A 1500-byte
	// CJK error (~500 runes) passed the outer check and failed the
	// inner, so no truncation happened and the full error UPDATE'd
	// last_error on every failed test — defeating the byte cap that
	// was meant to bound write traffic during a model-gateway storm.
	if len(errMsgCaptured) > recordFailureMaxErrLen {
		cut := recordFailureMaxErrLen
		for cut > 0 && !utf8.RuneStart(errMsgCaptured[cut]) {
			cut--
		}
		errMsgCaptured = errMsgCaptured[:cut] + "…(truncated)"
	}
	p.submitStatUpdate("TestAccount-failure", func() {
		db := globals.GraDBs["system"]
		now := time.Now()
		if err := db.Exec(
			"UPDATE w_workagent_account SET total_requests = total_requests + 1, failed_requests = failed_requests + 1, consecutive_failures = consecutive_failures + 1, last_error = ?, last_error_at = ?, last_used_at = ? WHERE id = ?",
			errMsgCaptured, now, now, accID,
		).Error; err != nil {
			globals.Error(fmt.Sprintf("[AgentAccountPool] Failed to update test failure stats: %v", err))
		}
	})

	return TestResult{
		Success:      false,
		Error:        classified.Err,
		Message:      classified.Msg,
		ResponseTime: responseTime,
	}
}

// recordTestSuccess increments total_requests and resets the
// consecutive failure counter, mirroring production success handling.
func (p *AgentAccountPool) recordTestSuccess(account *workagentModel.AgentAccount) {
	accID := account.ID
	p.submitStatUpdate("TestAccount-success", func() {
		db := globals.GraDBs["system"]
		now := time.Now()
		if err := db.Exec(
			"UPDATE w_workagent_account SET total_requests = total_requests + 1, consecutive_failures = 0, last_used_at = ? WHERE id = ?",
			now, accID,
		).Error; err != nil {
			globals.Error(fmt.Sprintf("[AgentAccountPool] Failed to update test success stats: %v", err))
		}
	})
}
