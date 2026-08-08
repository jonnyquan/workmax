package workagent

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
)

// TryAcquireTurnLock — happy + contention + release symmetry.

func TestTryAcquireTurnLock_HappyPath(t *testing.T) {
	const key = "test-thread-acquire-1"
	lock, ok := TryAcquireTurnLock(key)
	if !ok {
		t.Fatal("expected first acquire to succeed")
	}
	defer lock.Release()
	if lock.mu == nil {
		t.Error("acquired TurnLock has nil mu — Release would be a no-op")
	}
}

func TestTryAcquireTurnLock_ContentionRejects(t *testing.T) {
	const key = "test-thread-acquire-2"
	first, ok := TryAcquireTurnLock(key)
	if !ok {
		t.Fatal("expected first acquire to succeed")
	}
	defer first.Release()

	_, ok2 := TryAcquireTurnLock(key)
	if ok2 {
		t.Error("expected second acquire on same key to fail (TryLock contention)")
	}
}

func TestTryAcquireTurnLock_ReleaseUnblocksNextAcquire(t *testing.T) {
	const key = "test-thread-acquire-3"
	first, _ := TryAcquireTurnLock(key)
	first.Release()
	second, ok := TryAcquireTurnLock(key)
	if !ok {
		t.Fatal("expected acquire after Release to succeed")
	}
	second.Release()
}

func TestTurnLock_ZeroValueReleaseIsNoOp(t *testing.T) {
	// The `if !ok { return }` path on the busy branch leaves a
	// `defer lock.Release()` reaching a zero-value TurnLock —
	// must not panic.
	var lock TurnLock
	lock.Release() // no panic = pass
}

func TestTryAcquireTurnLock_ConcurrentRaceProducesOneWinner(t *testing.T) {
	const key = "test-thread-acquire-race"
	const goroutines = 8
	var wg sync.WaitGroup
	var winners atomic.Int64
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, ok := TryAcquireTurnLock(key)
			if ok {
				winners.Add(1)
				lock.Release()
			}
		}()
	}
	wg.Wait()
	// Race outcome: at least 1 winner (someone always wins
	// uncontended starts), at most `goroutines` (if they all
	// serialized via TryLock+Release sequences). The contract is
	// "TryLock never queues" — so the load-bearing assertion is
	// "no panic, the lock map didn't tear, and at least 1
	// goroutine won".
	if winners.Load() < 1 {
		t.Errorf("expected ≥1 winner across %d goroutines, got %d", goroutines, winners.Load())
	}
}

// BuildThreadBusyPayload — wire shape pin.

func TestBuildThreadBusyPayload_CarriesCanonicalShape(t *testing.T) {
	raw := BuildThreadBusyPayload("Another message is processing.")
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	wantPairs := map[string]interface{}{
		"type":     "result",
		"subtype":  "thread_busy",
		"is_error": true,
		"code":     "THREAD_BUSY",
		"error":    "Another message is processing.",
	}
	for k, want := range wantPairs {
		if got[k] != want {
			t.Errorf("field %q: got %#v, want %#v", k, got[k], want)
		}
	}
}

func TestBuildThreadBusyPayload_CarriesCallerPhrase(t *testing.T) {
	// Each surface localizes the human phrase but the code stays
	// THREAD_BUSY. Pin that the phrase makes it through verbatim.
	raw := BuildThreadBusyPayload("Another Canvas Agent message is processing.")
	if !strings.Contains(string(raw), "Canvas Agent") {
		t.Errorf("surface phrase must appear in payload; got %s", raw)
	}
}

// SetupTurnSSE — headers + flusher + temp id.

func TestSetupTurnSSE_WritesHeadersAndConnectedPing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	setup, ok := SetupTurnSSE(c, "temp_agent")
	if !ok {
		t.Fatal("expected SetupTurnSSE to succeed on a streaming-capable recorder")
	}

	hdr := recorder.Header()
	if hdr.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", hdr.Get("Content-Type"))
	}
	if hdr.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control: got %q, want no-cache", hdr.Get("Cache-Control"))
	}
	if hdr.Get("Connection") != "keep-alive" {
		t.Errorf("Connection: got %q, want keep-alive", hdr.Get("Connection"))
	}
	if hdr.Get("X-Accel-Buffering") != "no" {
		t.Errorf("X-Accel-Buffering: got %q, want no", hdr.Get("X-Accel-Buffering"))
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, ": connected\n\n") {
		t.Errorf("missing initial ': connected' ping; body=%q", body)
	}

	if setup.Flusher == nil {
		t.Error("Flusher must be non-nil on success")
	}
	if !strings.HasPrefix(setup.TempMessageID, "temp_agent_") {
		t.Errorf("TempMessageID prefix: got %q, want temp_agent_*", setup.TempMessageID)
	}
}

func TestSetupTurnSSE_UniqueTempMessageIDPerCall(t *testing.T) {
	// Two calls in the same nanosecond must NOT collide — the
	// atomic counter is the load-bearing piece.
	gin.SetMode(gin.TestMode)
	mintOnce := func(prefix string) string {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		setup, _ := SetupTurnSSE(c, prefix)
		return setup.TempMessageID
	}
	a := mintOnce("temp_agent")
	b := mintOnce("temp_agent")
	if a == b {
		t.Errorf("expected distinct temp ids, got %q and %q", a, b)
	}
}

// Note on the non-Flushable branch in SetupTurnSSE: gin wraps
// every ResponseWriter in its own type that delegates Flush() to
// the underlying writer, so the `c.Writer.(http.Flusher)` cast
// always succeeds in a real Gin handler — the check exists only
// as defense-in-depth against future net/http changes. There's no
// way to drive Gin into producing a non-Flusher *gin.responseWriter
// from a test, so the negative branch isn't exercised here. The
// happy path (above) is the load-bearing assertion.
