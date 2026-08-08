package provider

import (
	"context"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"server/globals"
	"server/model"
	"server/utils"
	"strings"
	"sync"
	"time"
)

// Quota alert reason codes. These are the only values
// notifyProviderQuotaIssue accepts as a reason argument and the only ones
// that get rendered into the email subject / body.
const (
	QuotaReasonDailyExceeded     = "daily_quota_exceeded"
	QuotaReasonMonthlyExceeded   = "monthly_quota_exceeded"
	QuotaReasonUpstreamExhausted = "upstream_quota_exhausted"
)

// quotaAlertCooldown is how long to suppress repeat alerts for the same
// (providerID, reason). Set to 24h so an exhausted provider produces at
// most one email per day per reason — enough to surface the issue without
// flooding the operator inbox while the issue is being resolved.
var quotaAlertCooldown = 24 * time.Hour

// quotaAlertLastSentAt tracks the most recent alert send per
// (providerID, reason). Process-local; restarting the server resets the
// table, which intentionally allows a fresh alert from the new process —
// useful as confirmation that the new process also sees the issue.
var quotaAlertLastSentAt sync.Map // key: "<providerID>:<reason>", value: time.Time

// Test seams. Tests swap these to mock time and email send. Keep package-
// private — there is no production reason to override either.
var (
	nowFn  = time.Now
	sendFn = utils.SendEmail
)

// upstreamQuotaPatterns lists case-insensitive substrings whose presence
// in a provider error message means the upstream gateway has run out of
// credit / quota / billing budget. Matched against strings.ToLower(errMsg).
//
// Conservative on purpose: false positives spam the operator, false negatives
// just fall through to the existing failure-stat path. Specifically, we do
// NOT include "rate_limit_exceeded" / "model_overloaded" / "server_error_5xx"
// — those are transient and already retried by the wrappedProvider retry
// config; only quota-distinct phrases live here.
var upstreamQuotaPatterns = []string{
	"insufficient",
	"余额不足",
	"余额",
	"额度",
	"resource_exhausted",
	"quota exceeded",
	"quota_exceeded",
	"billing",
	"out of credit",
	"no credit",
	"no credits",
	"402",
	"payment required",
	"usage limit",
	"account suspended",
}

// shouldSendQuotaAlert returns true when a notification for (providerID,
// reason) is allowed at `now` under the cooldown window, and records
// `now` as the most recent send time. Returns false (and does not record)
// when a previous send is still inside the cooldown.
//
// Extracted so tests can exercise the throttle without going through
// notifyProviderQuotaIssue (which short-circuits on blank AdminEmail in
// the test environment).
func shouldSendQuotaAlert(providerID uint, reason string, now time.Time) bool {
	key := fmt.Sprintf("%d:%s", providerID, reason)
	if prev, loaded := quotaAlertLastSentAt.Load(key); loaded {
		if t, ok := prev.(time.Time); ok && now.Sub(t) < quotaAlertCooldown {
			return false
		}
	}
	quotaAlertLastSentAt.Store(key, now)
	return true
}

// isUpstreamQuotaError reports whether errMsg looks like an upstream
// credit / billing / quota exhaustion. See upstreamQuotaPatterns for the
// exhaustive list; matching is case-insensitive substring.
func isUpstreamQuotaError(errMsg string) bool {
	if errMsg == "" {
		return false
	}
	lower := strings.ToLower(errMsg)
	for _, pat := range upstreamQuotaPatterns {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// notifyProviderQuotaIssue sends a quota-exhaustion alert email to the
// configured Server operator. Fire-and-forget — callers should invoke it as
// `go notifyProviderQuotaIssue(...)` so the generation path never blocks
// on email I/O.
//
// Throttled to one send per (providerID, reason) per quotaAlertCooldown;
// if the cooldown has not elapsed, the call is a no-op.
//
// reason should be one of the QuotaReason* constants. upstreamErr is the
// raw error string from the upstream gateway; it is rendered into the
// email body for upstream_quota_exhausted alerts and ignored for the
// internal-quota reasons.
func notifyProviderQuotaIssue(_ context.Context, providerID uint, reason, upstreamErr string) {
	if providerID == 0 || reason == "" {
		return
	}

	now := nowFn()
	if !shouldSendQuotaAlert(providerID, reason, now) {
		return
	}

	operatorEmail := strings.TrimSpace(globals.GraConf.System.AdminEmail)
	if operatorEmail == "" {
		globals.Warn("[ProviderAlert] Operator email not configured, skipping quota alert")
		return
	}

	// Look the row up fresh so the email carries current usage stats. If
	// the lookup fails (unlikely — provider was just used), fall back to
	// a row with only the ID populated so the email still goes out.
	var row model.GeneratorProvider
	dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := globals.GraDBs["system"].WithContext(dbCtx).First(&row, "id = ?", providerID).Error; err != nil {
		globals.Warn(fmt.Sprintf("[ProviderAlert] Provider lookup failed (id=%d): %v", providerID, err))
		row = model.GeneratorProvider{}
		row.Id = providerID
	}

	subject := fmt.Sprintf("⚠️ Provider quota issue: %s (%s)", displayProviderName(&row), reason)
	body := buildQuotaAlertBody(&row, reason, upstreamErr, now)

	if err := sendFn(operatorEmail, subject, body); err != nil {
		globals.Error(fmt.Sprintf("[ProviderAlert] Failed to send quota alert: %v", err))
		return
	}
	globals.Info(fmt.Sprintf("[ProviderAlert] Quota alert sent to %s for provider=%s reason=%s", operatorEmail, displayProviderName(&row), reason))
}

func displayProviderName(row *model.GeneratorProvider) string {
	if row != nil && row.Name != "" {
		return row.Name
	}
	if row != nil && row.Id != 0 {
		return fmt.Sprintf("provider#%d", row.Id)
	}
	return "unknown"
}

func reasonDisplay(reason string) string {
	switch reason {
	case QuotaReasonDailyExceeded:
		return "Internal daily quota exceeded"
	case QuotaReasonMonthlyExceeded:
		return "Internal monthly quota exceeded"
	case QuotaReasonUpstreamExhausted:
		return "Upstream gateway out of credit / quota"
	default:
		return reason
	}
}

func formatUsage(used, quota int) string {
	if quota <= 0 {
		return "—"
	}
	return fmt.Sprintf("%d / %d", used, quota)
}

func formatLastUsed(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04:05 MST")
}

// truncateUpstreamError caps the upstream error string so a multi-KB
// gateway dump does not blow up the email body. 1000 chars is plenty to
// see the relevant message; longer payloads almost always repeat
// themselves.
func truncateUpstreamError(s string) string {
	const max = 1000
	if len(s) <= max {
		return s
	}
	return s[:max] + " …(truncated)"
}

func buildQuotaAlertBody(row *model.GeneratorProvider, reason, upstreamErr string, now time.Time) string {
	upstreamTrimmed := truncateUpstreamError(strings.TrimSpace(upstreamErr))

	// Try the resource template first; fall back to inline HTML if the
	// template is missing (local dev with no ServerRunPath set, or a
	// stripped image).
	basePath := globals.GraConf.System.ServerRunPath
	if basePath == "" {
		basePath = "."
	}
	templatePath := filepath.Join(basePath, "resource", "template", "mail_provider_quota_alert.html")
	templateContent, readErr := ioutil.ReadFile(templatePath)
	if readErr != nil {
		globals.Warn(fmt.Sprintf("[ProviderAlert] Template not found, using fallback: %v", readErr))
		return buildFallbackQuotaAlertBody(row, reason, upstreamTrimmed, now)
	}

	upstreamBlock := ""
	if upstreamTrimmed != "" {
		upstreamBlock = fmt.Sprintf(
			`<div class="upstream-error-box"><h3>Upstream Error</h3><div class="error-text">%s</div></div>`,
			htmlEscape(upstreamTrimmed),
		)
	}

	mediaType := row.MediaType
	if mediaType == "" {
		mediaType = model.MediaTypeImage
	}

	body := string(templateContent)
	body = strings.ReplaceAll(body, "{{providerName}}", htmlEscape(displayProviderName(row)))
	body = strings.ReplaceAll(body, "{{providerType}}", htmlEscape(row.Type))
	body = strings.ReplaceAll(body, "{{providerModel}}", htmlEscape(row.Model))
	body = strings.ReplaceAll(body, "{{mediaType}}", htmlEscape(mediaType))
	body = strings.ReplaceAll(body, "{{reasonCode}}", htmlEscape(reason))
	body = strings.ReplaceAll(body, "{{reasonDisplay}}", htmlEscape(reasonDisplay(reason)))
	body = strings.ReplaceAll(body, "{{dailyUsage}}", formatUsage(row.DailyUsed, row.DailyQuota))
	body = strings.ReplaceAll(body, "{{monthlyUsage}}", formatUsage(row.MonthlyUsed, row.MonthlyQuota))
	body = strings.ReplaceAll(body, "{{lastUsedAt}}", formatLastUsed(row.LastUsedAt))
	body = strings.ReplaceAll(body, "{{upstreamError}}", htmlEscape(upstreamTrimmed))
	body = strings.ReplaceAll(body, "{{upstreamErrorBlock}}", upstreamBlock)
	body = strings.ReplaceAll(body, "{{timestamp}}", now.Format("2006-01-02 15:04:05 MST"))
	return body
}

func buildFallbackQuotaAlertBody(row *model.GeneratorProvider, reason, upstreamTrimmed string, now time.Time) string {
	upstreamSection := ""
	if upstreamTrimmed != "" {
		upstreamSection = fmt.Sprintf(
			`<div style="background:#fff;border-left:4px solid #ef4444;padding:12px;margin:12px 0;font-family:monospace;font-size:12px;white-space:pre-wrap;">%s</div>`,
			htmlEscape(upstreamTrimmed),
		)
	}
	mediaType := row.MediaType
	if mediaType == "" {
		mediaType = model.MediaTypeImage
	}
	return fmt.Sprintf(`<!DOCTYPE html>
<html><body style="font-family:Arial,sans-serif;color:#333;">
<div style="max-width:600px;margin:0 auto;padding:20px;">
  <h2 style="color:#ef4444;">⚠️ Provider Quota Issue</h2>
  <p><strong>%s</strong></p>
  <ul>
    <li><strong>Provider:</strong> %s</li>
    <li><strong>Type / Model:</strong> %s · %s</li>
    <li><strong>Media Type:</strong> %s</li>
    <li><strong>Reason Code:</strong> <code>%s</code></li>
    <li><strong>Daily Usage:</strong> %s</li>
    <li><strong>Monthly Usage:</strong> %s</li>
    <li><strong>Last Used:</strong> %s</li>
    <li><strong>Detected At:</strong> %s</li>
  </ul>
  %s
  <p style="background:#fffbeb;border:1px solid #fcd34d;padding:10px;border-radius:6px;color:#92400e;">
    💡 Use protected Server operator tooling to top up the upstream account, raise its quota, or temporarily disable this provider.
  </p>
  <p style="color:#6b7280;font-size:12px;">Throttled to one email per provider + reason per 24 hours.</p>
</div>
</body></html>`,
		htmlEscape(reasonDisplay(reason)),
		htmlEscape(displayProviderName(row)),
		htmlEscape(row.Type),
		htmlEscape(row.Model),
		htmlEscape(mediaType),
		htmlEscape(reason),
		formatUsage(row.DailyUsed, row.DailyQuota),
		formatUsage(row.MonthlyUsed, row.MonthlyQuota),
		formatLastUsed(row.LastUsedAt),
		now.Format("2006-01-02 15:04:05 MST"),
		upstreamSection,
	)
}

// htmlEscape is the tiny escape-set we need to keep error strings, model
// names, and untrusted upstream messages from breaking the email layout.
// Email clients are notoriously bad at recovering from broken HTML, so
// even an internal operator-only message benefits from escaping.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
