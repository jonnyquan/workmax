//go:build desktop

package local_agent

import (
	"context"
	"sync"
	"testing"
	"time"

	claudesdk "github.com/jonnyquan/claude-agent-sdk-go/pkg/claudesdk"

	agentruntime "server/desktop/agentruntime"
)

type recordedEvents struct {
	mu  sync.Mutex
	evs []agentruntime.Event
}

func (r *recordedEvents) emit(ev agentruntime.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, ev)
	return nil
}

func (r *recordedEvents) kinds() []agentruntime.EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []agentruntime.EventKind
	for _, e := range r.evs {
		out = append(out, e.Kind)
	}
	return out
}

func testApprovalConfig(broker *agentruntime.ApprovalBroker, persisted *[]string) *agentruntime.ApprovalConfig {
	return &agentruntime.ApprovalConfig{
		Broker:      broker,
		TurnUUID:    testTurnUUID,
		ThreadUUID:  "thr_l2",
		AutoAllowed: map[string]bool{"Read": true, "Glob": true, "Grep": true},
		AskAllowed:  map[string]bool{"Write": true, "Edit": true},
		Persist:     func(tool string) { *persisted = append(*persisted, tool) },
		Timeout:     2 * time.Second,
	}
}

func isAllow(t *testing.T, res claudesdk.PermissionResult) bool {
	t.Helper()
	switch res.(type) {
	case *claudesdk.PermissionResultAllow:
		return true
	case *claudesdk.PermissionResultDeny:
		return false
	}
	t.Fatalf("unexpected result type %T", res)
	return false
}

func pctx() claudesdk.ToolPermissionContext {
	return claudesdk.ToolPermissionContext{Context: context.Background()}
}

// The three tiers: reads pass silently, writes ask, everything else is
// denied without a card.
func TestApprovalCallback_SurfaceTiers(t *testing.T) {
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	rec := &recordedEvents{}
	cb := approvalCallback(testApprovalConfig(broker, &persisted), rec.emit)

	if res, _ := cb("Read", map[string]any{"file_path": "a.md"}, pctx()); !isAllow(t, res) {
		t.Error("Read must auto-allow")
	}
	if res, _ := cb("Bash", map[string]any{"command": "ls"}, pctx()); isAllow(t, res) {
		t.Error("Bash is outside the surface and must be denied")
	}
	kinds := rec.kinds()
	if len(kinds) != 1 || kinds[0] != agentruntime.EventToolDenied {
		t.Errorf("events = %v, want exactly one tool_denied for the out-of-surface call", kinds)
	}
}

// A write asks; the user's four answers land as allow/deny plus the right
// grant bookkeeping.
func TestApprovalCallback_AnswersAndGrants(t *testing.T) {
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	rec := &recordedEvents{}
	cfg := testApprovalConfig(broker, &persisted)
	cb := approvalCallback(cfg, rec.emit)

	answer := func(d agentruntime.ApprovalDecision) claudesdk.PermissionResult {
		done := make(chan claudesdk.PermissionResult, 1)
		go func() {
			res, _ := cb("Write", map[string]any{"file_path": "/ws/out.md"}, pctx())
			done <- res
		}()
		// Wait for the card, then answer it.
		var id string
		for i := 0; i < 200 && id == ""; i++ {
			time.Sleep(5 * time.Millisecond)
			rec.mu.Lock()
			for _, e := range rec.evs {
				if e.Kind == agentruntime.EventApprovalReq && e.ApprovalID != "" {
					id = e.ApprovalID
				}
			}
			rec.evs = nil
			rec.mu.Unlock()
		}
		if id == "" {
			t.Fatal("no approval_request event surfaced")
		}
		if !broker.Resolve(testTurnUUID, id, d) {
			t.Fatalf("Resolve(%s) found nothing pending", id)
		}
		return <-done
	}

	if !isAllow(t, answer(agentruntime.ApprovalAllowOnce)) {
		t.Error("allow_once must allow")
	}
	if isAllow(t, answer(agentruntime.ApprovalDeny)) {
		t.Error("deny must deny")
	}
	if !isAllow(t, answer(agentruntime.ApprovalAllowSession)) {
		t.Error("allow_session must allow")
	}
	// The session grant now short-circuits: no new card.
	if res, _ := cb("Write", map[string]any{"file_path": "/ws/two.md"}, pctx()); !isAllow(t, res) {
		t.Error("a session grant must skip the card")
	}
	if !broker.SessionGranted("thr_l2", "Write") {
		t.Error("session grant must be recorded")
	}

	if !isAllow(t, answer2(t, broker, rec, cb, "Edit", agentruntime.ApprovalAllowAlways)) {
		t.Error("allow_always must allow")
	}
	if len(persisted) != 1 || persisted[0] != "Edit" {
		t.Errorf("persisted = %v, want [Edit]", persisted)
	}
}

// answer2 mirrors answer for a second tool (Write is session-granted by the
// time Edit is exercised).
func answer2(t *testing.T, broker *agentruntime.ApprovalBroker, rec *recordedEvents, cb claudesdk.CanUseToolCallback, tool string, d agentruntime.ApprovalDecision) claudesdk.PermissionResult {
	t.Helper()
	done := make(chan claudesdk.PermissionResult, 1)
	go func() {
		res, _ := cb(tool, map[string]any{"file_path": "/ws/x.md"}, pctx())
		done <- res
	}()
	var id string
	for i := 0; i < 200 && id == ""; i++ {
		time.Sleep(5 * time.Millisecond)
		rec.mu.Lock()
		for _, e := range rec.evs {
			if e.Kind == agentruntime.EventApprovalReq && e.ApprovalID != "" {
				id = e.ApprovalID
			}
		}
		rec.evs = nil
		rec.mu.Unlock()
	}
	if id == "" {
		t.Fatal("no approval_request event surfaced")
	}
	if !broker.Resolve(testTurnUUID, id, d) {
		t.Fatalf("Resolve(%s) found nothing pending", id)
	}
	return <-done
}

// Nobody answering is a denial, not a hang; the wait respects the timeout.
func TestApprovalCallback_TimeoutDenies(t *testing.T) {
	broker := agentruntime.NewApprovalBroker()
	var persisted []string
	rec := &recordedEvents{}
	cfg := testApprovalConfig(broker, &persisted)
	cfg.Timeout = 50 * time.Millisecond
	cb := approvalCallback(cfg, rec.emit)

	start := time.Now()
	res, _ := cb("Write", map[string]any{"file_path": "/ws/slow.md"}, pctx())
	if isAllow(t, res) {
		t.Error("an unanswered card must deny")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("timeout did not bound the wait")
	}
	// The abandoned request no longer resolves.
	if broker.Resolve(testTurnUUID, "ap-1", agentruntime.ApprovalAllowOnce) {
		t.Error("a timed-out request must not accept late answers")
	}
}
