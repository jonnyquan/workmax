//go:build desktop

package desktop

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

func countAgentTurnLocks(s *Server) int {
	count := 0
	s.agentTurnLocks.Range(func(any, any) bool {
		count++
		return true
	})
	return count
}

// Phase 0.5: the per-turn lock registry used to keep one mutex per turn_uuid
// for the process lifetime. Entries must now be retired when the turn's lock
// is released, and a released turn must be re-acquirable.
func TestAgentTurnLockLifecycle(t *testing.T) {
	s := &Server{}

	lock, ok := s.acquireAgentTurnLock(serverTestTurnUUID)
	if !ok {
		t.Fatal("first acquire failed")
	}
	if got := countAgentTurnLocks(s); got != 1 {
		t.Fatalf("registry size while held = %d, want 1", got)
	}
	if _, ok := s.acquireAgentTurnLock(serverTestTurnUUID); ok {
		t.Fatal("second acquire succeeded while the turn was streaming")
	}

	s.releaseAgentTurnLock(serverTestTurnUUID, lock)
	if got := countAgentTurnLocks(s); got != 0 {
		t.Fatalf("registry size after release = %d, want 0 (unbounded growth regression)", got)
	}

	lock2, ok := s.acquireAgentTurnLock(serverTestTurnUUID)
	if !ok {
		t.Fatal("re-acquire after release failed")
	}
	s.releaseAgentTurnLock(serverTestTurnUUID, lock2)
	if got := countAgentTurnLocks(s); got != 0 {
		t.Fatalf("registry size after second release = %d, want 0", got)
	}
}

// End-to-end: one completed /agent/chat turn leaves no residue in the lock
// registry.
func TestHandleAgentChat_ReleasesTurnLockEntry(t *testing.T) {
	db := openServerTestDB(t)
	seedServerTestThread(t, db, 42, "thr_1")
	srv, base, tok := newServerFixtureExposingServer(t, db, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		io.WriteString(w, "event: text\ndata: {\"text\":\"hi\"}\n\n")
		flusher.Flush()
		io.WriteString(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	})

	req, _ := http.NewRequest(http.MethodPost, base+"/agent/chat", bytes.NewReader([]byte(`{
		"turn_uuid": "de305d54-75b4-431b-adb2-eb6b9e546014",
		"thread_uuid": "thr_1",
		"user_text": "hi",
		"chat_mode": "ppt",
		"payload": {"stream":true}
	}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("drain: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The handler's deferred release may run a beat after the client sees EOF.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countAgentTurnLocks(srv) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("turn lock registry still holds %d entries after the turn finished", countAgentTurnLocks(srv))
}
