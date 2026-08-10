//go:build desktop

package cloud_proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Phase 0.3a regression: the SSE spec allows zero or one space after the field
// colon, so `data:{...}` is as valid as `data: {...}`. The parser used to
// require the spaced form and silently dropped every frame from an upstream
// that omits the space.
func TestPipeUpstream_AcceptsNoSpaceDataFrames(t *testing.T) {
	dst := &memSSEWriter{}
	err := PipeUpstream(
		context.Background(),
		strings.NewReader("event:text\ndata:{\"text\":\"Hello\"}\n\nevent:done\ndata:{}\n\n"),
		dst, nil,
	)
	if err != nil {
		t.Fatalf("PipeUpstream: %v", err)
	}
	frames, _ := dst.snapshot()
	if len(frames) != 2 {
		t.Fatalf("frame count = %d, want 2 (no-space frames must not be dropped): %+v", len(frames), frames)
	}
	if frames[0].Type != "text" || frames[0].Data != `{"text":"Hello"}` {
		t.Errorf("frame[0] = %+v, want type=text data intact", frames[0])
	}
	if frames[1].Type != "done" {
		t.Errorf("frame[1].Type = %q, want done", frames[1].Type)
	}
}

// A stalled upstream must not park PipeUpstream forever: the watchdog closes
// the body, the scanner unblocks with a read error, and TimedOut identifies
// the failure as the idle interruption (retryable) rather than a stream bug.
func TestIdleWatchdog_UnblocksStalledPipeUpstream(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		// One healthy frame, then silence forever.
		_, _ = pw.Write([]byte("event: text\ndata: {\"text\":\"hi\"}\n\n"))
	}()

	watchdog := NewIdleWatchdogReader(pr, 80*time.Millisecond, func() {
		_ = pr.CloseWithError(errors.New("connection abandoned by watchdog"))
	})
	defer watchdog.Stop()

	dst := &memSSEWriter{}
	done := make(chan error, 1)
	go func() { done <- PipeUpstream(context.Background(), watchdog, dst, nil) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("PipeUpstream returned nil on a stream with no done event")
		}
		if !watchdog.TimedOut() {
			t.Fatalf("watchdog did not report the timeout; err=%v", err)
		}
		if pe := ClassifyUpstreamStreamError(err); !pe.Retryable {
			t.Errorf("idle interruption classified non-retryable: %+v", pe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PipeUpstream still blocked 5s after the idle timeout — watchdog failed to unblock it")
	}

	frames, _ := dst.snapshot()
	if len(frames) != 1 || frames[0].Type != "text" {
		t.Errorf("frames before the stall must still be delivered, got %+v", frames)
	}
}

// The activity channel must actually reset the timer: a slow-but-alive
// upstream that keeps sending inside the window is never interrupted.
func TestIdleWatchdog_ActivityResetsTimer(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		for _, chunk := range []string{"event: text\n", "data: {\"text\":\"a\"}\n", "\n", "event: done\n", "data: {}\n", "\n"} {
			time.Sleep(60 * time.Millisecond) // each gap is under the 150ms window; the total is far over it
			_, _ = pw.Write([]byte(chunk))
		}
		_ = pw.Close()
	}()

	watchdog := NewIdleWatchdogReader(pr, 150*time.Millisecond, func() {
		_ = pr.CloseWithError(errors.New("watchdog fired"))
	})
	defer watchdog.Stop()

	if err := PipeUpstream(context.Background(), watchdog, &memSSEWriter{}, nil); err != nil {
		t.Fatalf("alive-but-slow upstream was interrupted: %v (timedOut=%v)", err, watchdog.TimedOut())
	}
	if watchdog.TimedOut() {
		t.Error("watchdog fired even though every read landed inside the window")
	}
}

// Chat-level: a cloud upstream that goes silent mid-turn is force-closed by
// the watchdog and surfaced to the renderer as a retryable proxy_error —
// previously this turn hung forever (client Timeout 0, no read deadline).
func TestProxy_Chat_IdleUpstreamIsCutRetryable(t *testing.T) {
	proxy, dst, _, _ := newProxyTestFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		sseLine(w, "event: text")
		sseLine(w, `data: {"text":"partial"}`)
		sseLine(w, "")
		flusher.Flush()
		// Never send another byte; hold the connection until the relay
		// abandons it (the body close cancels our request context).
		<-r.Context().Done()
	})
	proxy.idleTimeout = 100 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		done <- proxy.Chat(context.Background(), ChatRequest{
			ThreadID:   1,
			ThreadUUID: "thr_idle",
			TurnUUID:   proxyTestTurnUUID,
			UID:        proxyTestUID,
			UserText:   "hello?",
			ChatMode:   "ppt",
			Body:       []byte(`{}`),
		}, dst)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Chat returned nil for a turn with no done event")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Chat still blocked long after the idle timeout — half-open upstream hangs the turn again")
	}

	frames, proxyErrors := dst.snapshot()
	if len(frames) == 0 || frames[0].Type != "text" {
		t.Errorf("the frame delivered before the stall must reach the renderer, got %+v", frames)
	}
	if len(proxyErrors) != 1 {
		t.Fatalf("proxy error count = %d, want exactly 1: %+v", len(proxyErrors), proxyErrors)
	}
	if !proxyErrors[0].Retryable {
		t.Errorf("idle cut must be retryable, got %+v", proxyErrors[0])
	}
	if proxyErrors[0].Kind != KindServiceUnavailable {
		t.Errorf("kind = %q, want %q", proxyErrors[0].Kind, KindServiceUnavailable)
	}
}
