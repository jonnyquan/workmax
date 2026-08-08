package provider

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsUpstreamQuotaError(t *testing.T) {
	hits := []string{
		"Insufficient credits to complete request",
		"insufficient_balance",
		"账户余额不足，请充值",
		"额度不足",
		"RESOURCE_EXHAUSTED: Quota exceeded for project",
		"Quota exceeded for resource X",
		"quota_exceeded",
		"billing hard limit reached",
		"Out of credit. Please top up your account.",
		"no credits remaining on this key",
		"HTTP 402 Payment Required",
		"payment required: please add billing details",
		"Account suspended due to overdue invoice",
		"Daily usage limit reached",
		"INSUFFICIENT FUNDS",
	}
	for _, msg := range hits {
		t.Run("hit/"+truncate(msg, 40), func(t *testing.T) {
			if !isUpstreamQuotaError(msg) {
				t.Fatalf("expected hit, got miss for %q", msg)
			}
		})
	}

	misses := []string{
		"",
		"context deadline exceeded",
		"timeout waiting for response",
		"rate limit exceeded, please retry",
		"model overloaded, try again later",
		"internal server error",
		"500 Internal Server Error",
		"network unreachable",
		"invalid prompt",
		"Provider failed without message",
	}
	for _, msg := range misses {
		t.Run("miss/"+truncate(msg, 40), func(t *testing.T) {
			if isUpstreamQuotaError(msg) {
				t.Fatalf("expected miss, got hit for %q", msg)
			}
		})
	}
}

// TestShouldSendQuotaAlert_Cooldown exercises the real throttle helper.
// Same (providerID, reason) within the cooldown must suppress; different
// reasons or providers each get their own counter; advancing past the
// window allows another send.
func TestShouldSendQuotaAlert_Cooldown(t *testing.T) {
	resetQuotaAlertState(t)

	t0 := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	if !shouldSendQuotaAlert(7, QuotaReasonDailyExceeded, t0) {
		t.Fatalf("first call: want allowed")
	}
	if shouldSendQuotaAlert(7, QuotaReasonDailyExceeded, t0.Add(1*time.Hour)) {
		t.Fatalf("within cooldown: want suppressed")
	}
	if !shouldSendQuotaAlert(7, QuotaReasonUpstreamExhausted, t0.Add(1*time.Hour)) {
		t.Fatalf("different reason same provider: want allowed")
	}
	if !shouldSendQuotaAlert(8, QuotaReasonDailyExceeded, t0.Add(1*time.Hour)) {
		t.Fatalf("different provider same reason: want allowed")
	}
	if !shouldSendQuotaAlert(7, QuotaReasonDailyExceeded, t0.Add(quotaAlertCooldown+time.Minute)) {
		t.Fatalf("after cooldown: want allowed")
	}
}

// TestNotifyProviderQuotaIssue_EarlyExits guards the no-op paths that
// must never panic or call into SMTP/DB: zero providerID, empty reason,
// and unconfigured admin email.
func TestNotifyProviderQuotaIssue_EarlyExits(t *testing.T) {
	resetQuotaAlertState(t)

	var sends atomic.Int32
	sendFn = func(to, subject, body string) error {
		sends.Add(1)
		return nil
	}

	notifyProviderQuotaIssue(context.Background(), 0, QuotaReasonDailyExceeded, "")
	notifyProviderQuotaIssue(context.Background(), 99, "", "")
	if got := sends.Load(); got != 0 {
		t.Fatalf("early-exit paths must not send, got %d sends", got)
	}
	// With a real (providerID, reason) but no AdminEmail configured (the
	// test environment leaves it blank), the function must short-circuit
	// before touching sendFn.
	notifyProviderQuotaIssue(context.Background(), 99, QuotaReasonDailyExceeded, "")
	if got := sends.Load(); got != 0 {
		t.Fatalf("blank admin email must not send, got %d sends", got)
	}
}

func TestReasonDisplayAndUsageFormatting(t *testing.T) {
	if got := reasonDisplay(QuotaReasonDailyExceeded); got != "Internal daily quota exceeded" {
		t.Fatalf("daily reason display: %q", got)
	}
	if got := reasonDisplay("unknown_code"); got != "unknown_code" {
		t.Fatalf("fallback reason display should echo: %q", got)
	}
	if got := formatUsage(0, 0); got != "—" {
		t.Fatalf("zero quota: %q", got)
	}
	if got := formatUsage(42, 100); got != "42 / 100" {
		t.Fatalf("usage format: %q", got)
	}
}

func TestTruncateUpstreamError(t *testing.T) {
	short := "short message"
	if truncateUpstreamError(short) != short {
		t.Fatalf("short string must pass through")
	}
	long := strings.Repeat("x", 1500)
	got := truncateUpstreamError(long)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Fatalf("long string must be marked truncated")
	}
	if len(got) > 1100 {
		t.Fatalf("truncated length out of bounds: %d", len(got))
	}
}

// resetQuotaAlertState clears the cooldown table and restores the test
// seams when the test ends. Tests that touch nowFn / sendFn / the
// cooldown map should call this in their setup.
func resetQuotaAlertState(t *testing.T) {
	t.Helper()
	quotaAlertLastSentAt.Range(func(k, _ any) bool {
		quotaAlertLastSentAt.Delete(k)
		return true
	})
	origNow := nowFn
	origSend := sendFn
	t.Cleanup(func() {
		nowFn = origNow
		sendFn = origSend
		quotaAlertLastSentAt.Range(func(k, _ any) bool {
			quotaAlertLastSentAt.Delete(k)
			return true
		})
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
