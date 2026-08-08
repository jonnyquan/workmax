package workagent

import (
	"context"
	"fmt"
	htmltmpl "html/template"
	"os"
	"path/filepath"
	"server/globals"
	"server/utils"
	"strings"
	"sync"
	"time"
)

// AgentErrorNotifier handles email notifications for agent errors.
// Email sends go through a bounded worker pool — see emailJobs +
// emailWorkers. The previous implementation spawned an unbounded
// `go func()` per email, so a 429 storm or a stuck SMTP relay could
// stack thousands of goroutines blocked on net.Conn writes.
type AgentErrorNotifier struct {
	adminEmail  string
	rateLimiter map[string]time.Time // Prevent spam
	mutex       sync.RWMutex
	enabled     bool

	// emailJobs is a bounded queue of "send this email" callbacks.
	// Workers drain it serially per worker; overflow drops with a
	// warn log, matching the AgentAccountPool stat-update pattern.
	emailJobs    chan func()
	shutdownOnce sync.Once
}

const (
	// emailWorkers — email is I/O bound and low volume; two workers
	// are enough to keep flush latency low without spawning fan-out
	// for what's typically a single admin recipient.
	emailWorkers = 2
	// emailQueueSize — a 429 storm typically dumps 10-50 unique
	// errors before the 10-min rate limiter catches them, so 64 is
	// generous headroom; oldest go on the floor with a warn log if
	// SMTP is fully wedged.
	emailQueueSize = 64
)

var (
	errorNotifier    *AgentErrorNotifier
	notifierInitOnce sync.Once
)

// InitAgentErrorNotifier initializes the global error notifier.
// Spawns the email worker pool on first call.
func InitAgentErrorNotifier(adminEmail string) *AgentErrorNotifier {
	notifierInitOnce.Do(func() {
		errorNotifier = &AgentErrorNotifier{
			adminEmail:  adminEmail,
			rateLimiter: make(map[string]time.Time),
			enabled:     adminEmail != "",
			emailJobs:   make(chan func(), emailQueueSize),
		}

		for i := 0; i < emailWorkers; i++ {
			go errorNotifier.emailWorker()
		}

		if errorNotifier.enabled {
			globals.Info(fmt.Sprintf("[AgentError] Email notifications enabled for: %s (workers=%d, queue=%d)",
				adminEmail, emailWorkers, emailQueueSize))
		} else {
			globals.Warn("[AgentError] Email notifications disabled (no admin email configured)")
		}
	})

	return errorNotifier
}

// emailWorker drains the email queue. Runs until Shutdown closes the
// channel. Panics inside SendEmail are caught so one bad job doesn't
// take the worker down with it.
func (n *AgentErrorNotifier) emailWorker() {
	for fn := range n.emailJobs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					globals.Error(fmt.Sprintf("[AgentError] email worker recovered from panic: %v", r))
				}
			}()
			fn()
		}()
	}
}

// submitEmail enqueues a send-email closure. Drops + warns when the
// queue is full or after Shutdown — same drop-with-log semantics as
// the account pool's stat updates so a stuck SMTP can't unbound the
// process.
//
// Returns true when the job is on the queue, false on overflow or
// post-shutdown drop. Callers that pre-recorded rate-limit state
// based on the assumption the email would fly should use that signal
// to roll back; otherwise a dropped notification silently consumes
// the rate-limit window and the next 10 minutes of identical errors
// stay silent too.
func (n *AgentErrorNotifier) submitEmail(label string, fn func()) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			globals.Warn(fmt.Sprintf("[AgentError] email submit post-shutdown drop (%s)", label))
			ok = false
		}
	}()
	select {
	case n.emailJobs <- fn:
		return true
	default:
		globals.Warn(fmt.Sprintf("[AgentError] Email queue full, dropping %s", label))
		return false
	}
}

// clearRateLimitIfMatches removes a rate-limit record only when its
// timestamp matches the supplied recordedAt — the compare-and-delete
// guards against clobbering a more recent entry that might have
// landed between the original NotifyAgentError call and a deferred
// rollback fired from inside the email worker.
func (n *AgentErrorNotifier) clearRateLimitIfMatches(key string, recordedAt time.Time) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	if existing, exists := n.rateLimiter[key]; exists && existing.Equal(recordedAt) {
		delete(n.rateLimiter, key)
	}
}

// Shutdown signals the email workers to drain and exit. Idempotent
// via sync.Once — wired from main.go's setupGracefulShutdown so
// SIGTERM gets a chance to flush the queue before os.Exit.
func (n *AgentErrorNotifier) Shutdown() {
	n.shutdownOnce.Do(func() {
		close(n.emailJobs)
	})
}

// GetAgentErrorNotifier returns the global notifier instance
func GetAgentErrorNotifier() *AgentErrorNotifier {
	if errorNotifier == nil {
		return InitAgentErrorNotifier("")
	}
	return errorNotifier
}

// NotifyAgentError sends an email notification for agent errors
// Rate limited to prevent spam (max 1 email per error type per 10 minutes)
func NotifyAgentError(ctx context.Context, err error, threadID, userID, agentSessionID string) {
	if err == nil {
		return
	}

	// Always log critical GLM subscription errors, even if email is disabled
	if isGLMSubscriptionError(err) {
		RecordGLMSubscriptionError(err, threadID, userID, agentSessionID)
	}

	notifier := GetAgentErrorNotifier()
	if !notifier.enabled {
		return
	}

	// Rate limiting by error message prefix (first 50 chars)
	errorKey := err.Error()
	if len(errorKey) > 50 {
		errorKey = errorKey[:50]
	}

	const rateLimitWindow = 10 * time.Minute
	now := time.Now()

	notifier.mutex.Lock()
	lastSent, exists := notifier.rateLimiter[errorKey]
	if exists && now.Sub(lastSent) < rateLimitWindow {
		notifier.mutex.Unlock()
		return // Skip, too frequent
	}
	notifier.rateLimiter[errorKey] = now
	// Opportunistic cleanup: the rate-limiter is keyed by the first 50 chars
	// of the error message, which is effectively unbounded over a long run.
	// Every successful send, prune anything that has already aged out of the
	// window — the map can only grow to as many distinct errors fired in one
	// window, not all errors ever seen.
	for key, sent := range notifier.rateLimiter {
		if now.Sub(sent) > rateLimitWindow {
			delete(notifier.rateLimiter, key)
		}
	}
	notifier.mutex.Unlock()

	// Build the email body synchronously so we don't capture err
	// across the worker boundary and avoid races on rate-limited
	// retries. The actual SMTP write is what we want bounded.
	subject := "🚨 Agent SDK Error Alert"
	if isGLMSubscriptionError(err) {
		subject = "🔴 URGENT: GLM Coding Plan Expired"
	}
	body := notifier.buildErrorEmail(err, threadID, userID, agentSessionID)
	to := notifier.adminEmail

	// Capture the timestamp we just recorded so the rollback path can
	// CAS-style match before deleting — see clearRateLimitIfMatches.
	recordedAt := now
	enqueued := notifier.submitEmail("agent-error", func() {
		if sendErr := utils.SendEmail(to, subject, body); sendErr != nil {
			// SMTP failure: roll back so the next identical error
			// inside the 10-min window can attempt delivery again.
			// Without this the rate limiter happily reports "we
			// just sent one" for the full window even though the
			// admin never saw it.
			notifier.clearRateLimitIfMatches(errorKey, recordedAt)
			globals.Error(fmt.Sprintf("[AgentError] Failed to send email: %v", sendErr))
		} else {
			globals.Info(fmt.Sprintf("[AgentError] Email sent to %s", to))
		}
	})
	if !enqueued {
		// Queue full / post-shutdown: nothing will ever try to send.
		// Same rollback rationale as the SMTP-failure branch.
		notifier.clearRateLimitIfMatches(errorKey, recordedAt)
	}
}

// isGLMSubscriptionError checks if error is a GLM subscription expiration error
func isGLMSubscriptionError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := strings.ToLower(err.Error())
	return strings.Contains(errMsg, "429") &&
		(strings.Contains(errMsg, "glm coding plan") ||
			strings.Contains(errMsg, "package has expired") ||
			strings.Contains(errMsg, "subscription") ||
			strings.Contains(errMsg, "type\":\"1309\""))
}

// RecordGLMSubscriptionError records GLM subscription errors with detailed context
func RecordGLMSubscriptionError(err error, threadID, userID, agentSessionID string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	// Log detailed error information
	globals.Error(fmt.Sprintf(
		"[GLM_SUBSCRIPTION_ERROR] "+
			"Timestamp: %s | "+
			"ThreadID: %s | "+
			"UserID: %s | "+
			"AgentSessionID: %s | "+
			"Error: %v",
		timestamp, threadID, userID, agentSessionID, err,
	))

	// Also log to console for immediate visibility
	fmt.Printf("\n"+
		"╔════════════════════════════════════════════════════════════════╗\n"+
		"║  🔴 GLM CODING PLAN EXPIRED - URGENT ACTION REQUIRED 🔴      ║\n"+
		"╠════════════════════════════════════════════════════════════════╣\n"+
		"║  Time:         %s                                  ║\n"+
		"║  Thread ID:    %-47s║\n"+
		"║  User ID:      %-47s║\n"+
		"║  Agent Session:%-47s║\n"+
		"║                                                                ║\n"+
		"║  Action: Renew subscription at https://z.ai/subscribe         ║\n"+
		"╚════════════════════════════════════════════════════════════════╝\n\n",
		timestamp, threadID, userID, agentSessionID,
	)
}

// buildErrorEmail builds the HTML email content using templates
func (n *AgentErrorNotifier) buildErrorEmail(err error, threadID, userID, agentSessionID string) string {
	now := time.Now().Format("2006-01-02 15:04:05")

	// Classify error type
	errorType := "Unknown Error"
	errMsg := strings.ToLower(err.Error())

	// Check for specific GLM subscription errors first
	isGLMExpired := false
	if strings.Contains(errMsg, "429") &&
		(strings.Contains(errMsg, "glm coding plan") ||
			strings.Contains(errMsg, "package has expired") ||
			strings.Contains(errMsg, "subscription") ||
			strings.Contains(errMsg, "type\":\"1309\"")) {
		errorType = "GLM Subscription Expired"
		isGLMExpired = true
		// Log this critical error
		globals.Error(fmt.Sprintf("[AgentError] 🔴 GLM Coding Plan EXPIRED - ThreadID: %s, UserID: %s", threadID, userID))
	} else {
		switch {
		case strings.Contains(errMsg, "session") && strings.Contains(errMsg, "not found"):
			errorType = "Session Not Found"
		case strings.Contains(errMsg, "timeout") || strings.Contains(errMsg, "deadline"):
			errorType = "Network Timeout"
		case strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate limit"):
			errorType = "Rate Limit"
		case strings.Contains(errMsg, "connection"):
			errorType = "Connection Error"
		case strings.Contains(errMsg, "permission") || strings.Contains(errMsg, "denied"):
			errorType = "Permission Denied"
		default:
			errorType = "API Failure"
		}
	}

	// Load appropriate template using server_run_path from config
	basePath := globals.GraConf.System.ServerRunPath
	if basePath == "" {
		basePath = "." // current directory
	}

	var templatePath string
	if isGLMExpired {
		templatePath = filepath.Join(basePath, "resource", "template", "mail_agent_error_glm.html")
	} else {
		templatePath = filepath.Join(basePath, "resource", "template", "mail_agent_error.html")
	}

	templateContent, readErr := os.ReadFile(templatePath)
	if readErr != nil {
		globals.Error(fmt.Sprintf("[AgentError] Failed to read email template %s: %v", templatePath, readErr))
		// Fallback to simple text email
		return n.buildFallbackEmail(errorType, err.Error(), now, threadID, userID, agentSessionID)
	}

	// HTML-escape every user-and-SDK-influenced field before injecting
	// into the template. err.Error() flows from upstream provider
	// responses surfaced through SDK in-band errors — a crafted prompt
	// can induce content like `<img src=x onerror=fetch(...)>` to land
	// here, which without escaping renders as live HTML in the admin's
	// email client. errorType is from a controlled switch above so it's
	// safe, but we escape it too for defense-in-depth.
	htmlOut := string(templateContent)
	htmlOut = strings.ReplaceAll(htmlOut, "{{errorType}}", htmltmpl.HTMLEscapeString(errorType))
	htmlOut = strings.ReplaceAll(htmlOut, "{{errorMessage}}", htmltmpl.HTMLEscapeString(err.Error()))
	htmlOut = strings.ReplaceAll(htmlOut, "{{timestamp}}", htmltmpl.HTMLEscapeString(now))
	htmlOut = strings.ReplaceAll(htmlOut, "{{threadID}}", htmltmpl.HTMLEscapeString(threadID))
	htmlOut = strings.ReplaceAll(htmlOut, "{{userID}}", htmltmpl.HTMLEscapeString(userID))
	htmlOut = strings.ReplaceAll(htmlOut, "{{agentSessionID}}", htmltmpl.HTMLEscapeString(agentSessionID))

	return htmlOut
}

// buildFallbackEmail creates a simple email when template loading fails.
// Same HTML-escape contract as buildErrorEmail — every user-or-SDK-
// influenced field is escaped before splicing into the HTML body.
func (n *AgentErrorNotifier) buildFallbackEmail(errorType, errorMsg, timestamp, threadID, userID, agentSessionID string) string {
	return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif; padding: 20px;">
	<h2>🚨 Agent SDK Error Alert</h2>
	<p><strong>Error Type:</strong> %s</p>
	<p><strong>Message:</strong> %s</p>
	<hr>
	<p><strong>Time:</strong> %s</p>
	<p><strong>Thread ID:</strong> %s</p>
	<p><strong>User ID:</strong> %s</p>
	<p><strong>Agent Session ID:</strong> %s</p>
	<hr>
	<p><em>Note: Email template could not be loaded. This is a fallback message.</em></p>
</body>
</html>
`,
		htmltmpl.HTMLEscapeString(errorType),
		htmltmpl.HTMLEscapeString(errorMsg),
		htmltmpl.HTMLEscapeString(timestamp),
		htmltmpl.HTMLEscapeString(threadID),
		htmltmpl.HTMLEscapeString(userID),
		htmltmpl.HTMLEscapeString(agentSessionID),
	)
}

// NotifyAccountSwitch sends an email notification when agent account is switched
func NotifyAccountSwitch(oldAccountID uint64, oldAccountName string, newAccountID uint64, newAccountName string, reason string) {
	notifier := GetAgentErrorNotifier()
	if !notifier.enabled {
		return
	}

	subject := "⚠️ Agent Account Auto-Switched"
	body := buildAccountSwitchEmail(oldAccountID, oldAccountName, newAccountID, newAccountName, reason)
	to := notifier.adminEmail

	// Account-switch notifications aren't rate-limited, so submitEmail's
	// drop signal has nothing to roll back here — the drop is just a
	// missed notification for one switch event.
	notifier.submitEmail("account-switch", func() {
		if sendErr := utils.SendEmail(to, subject, body); sendErr != nil {
			globals.Error(fmt.Sprintf("[AgentAccountSwitch] Failed to send email: %v", sendErr))
		} else {
			globals.Info(fmt.Sprintf("[AgentAccountSwitch] Email sent to %s", to))
		}
	})
}

// buildAccountSwitchEmail builds the HTML email content for account switch notification
func buildAccountSwitchEmail(oldAccountID uint64, oldAccountName string, newAccountID uint64, newAccountName string, reason string) string {
	now := time.Now().Format("2006-01-02 15:04:05")

	// Try to load template using server_run_path from config
	basePath := globals.GraConf.System.ServerRunPath
	if basePath == "" {
		basePath = "." // current directory
	}
	templatePath := filepath.Join(basePath, "resource", "template", "mail_agent_account_switch.html")
	templateContent, readErr := os.ReadFile(templatePath)
	if readErr != nil {
		globals.Warn(fmt.Sprintf("[AgentAccountSwitch] Template not found, using fallback: %v", readErr))
		return fmt.Sprintf(`
<html>
<body style="font-family: Arial, sans-serif; padding: 20px;">
	<h2>⚠️ Agent Account Auto-Switched</h2>
	<p>The agent system has automatically switched to a different account due to failures.</p>
	<hr>
	<p><strong>Time:</strong> %s</p>
	<p><strong>Old Account:</strong> %s (ID: %d)</p>
	<p><strong>New Account:</strong> %s (ID: %d)</p>
	<p><strong>Reason:</strong> %s</p>
	<hr>
	<p><strong>Recommended Actions:</strong></p>
	<ul>
		<li>Check the old account's status and error logs</li>
		<li>Verify the old account's API credentials</li>
		<li>Monitor the new account's performance</li>
		<li>Consider fixing the old account if it's the primary one</li>
	</ul>
</body>
</html>
`,
			htmltmpl.HTMLEscapeString(now),
			htmltmpl.HTMLEscapeString(oldAccountName),
			oldAccountID,
			htmltmpl.HTMLEscapeString(newAccountName),
			newAccountID,
			htmltmpl.HTMLEscapeString(reason),
		)
	}

	// HTML-escape user-influenced fields. Account names are admin-set,
	// reason is server-internal, but escape both for defense in depth
	// since these flow through the same admin-mailbox surface.
	htmlOut := string(templateContent)
	htmlOut = strings.ReplaceAll(htmlOut, "{{timestamp}}", htmltmpl.HTMLEscapeString(now))
	htmlOut = strings.ReplaceAll(htmlOut, "{{oldAccountID}}", fmt.Sprintf("%d", oldAccountID))
	htmlOut = strings.ReplaceAll(htmlOut, "{{oldAccountName}}", htmltmpl.HTMLEscapeString(oldAccountName))
	htmlOut = strings.ReplaceAll(htmlOut, "{{newAccountID}}", fmt.Sprintf("%d", newAccountID))
	htmlOut = strings.ReplaceAll(htmlOut, "{{newAccountName}}", htmltmpl.HTMLEscapeString(newAccountName))
	htmlOut = strings.ReplaceAll(htmlOut, "{{reason}}", htmltmpl.HTMLEscapeString(reason))

	return htmlOut
}
