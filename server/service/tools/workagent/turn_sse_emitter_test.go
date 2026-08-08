package workagent

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	workagentModel "server/model/workagent"

	"github.com/gin-gonic/gin"
)

// Pre-registration fallback path: with no SSEConnection set, the
// emitter writes through the raw writer + flusher and the resulting
// bytes are a well-formed SSE record.

func TestSSEEmitter_PreRegistrationWritesFramedRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()

	emitter := NewSSEEmitter(recorder, recorder, "[Test]")
	ok := emitter.Send(workagentModel.AgentSSEEvent{
		Type:      workagentModel.AgentEventMessage,
		MessageID: "test-msg-1",
	})
	if !ok {
		t.Fatal("expected Send to succeed")
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, "data: ") {
		t.Errorf("missing SSE 'data: ' framing; got %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("missing SSE trailing newlines; got %q", body)
	}

	// Inner payload is valid JSON carrying the event fields.
	jsonStart := strings.TrimPrefix(body, "data: ")
	jsonStart = strings.TrimSuffix(jsonStart, "\n\n")
	var got workagentModel.AgentSSEEvent
	if err := json.Unmarshal([]byte(jsonStart), &got); err != nil {
		t.Fatalf("inner payload not valid JSON: %v", err)
	}
	if got.MessageID != "test-msg-1" {
		t.Errorf("MessageID round-trip: got %q, want test-msg-1", got.MessageID)
	}
}

func TestSSEEmitter_FailedJSONMarshalReturnsFalse(t *testing.T) {
	// AgentSSEEvent.Result is json.RawMessage; an invalid raw
	// triggers a marshal error inside Send.
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()

	emitter := NewSSEEmitter(recorder, recorder, "[Test]")
	ok := emitter.Send(workagentModel.AgentSSEEvent{
		Type:   workagentModel.AgentEventDone,
		Result: json.RawMessage([]byte("{not valid json")),
	})
	if ok {
		t.Error("Send must return false when the inner Result raw is invalid JSON")
	}
}

func TestSSEEmitter_WriteRawBypassesJSONEncoding(t *testing.T) {
	// Heartbeat path emits ": hb <unix>\n\n" — bare comment,
	// not an event. Confirm the bytes land verbatim without
	// being re-wrapped in a `data:` frame.
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()

	emitter := NewSSEEmitter(recorder, recorder, "[Test]")
	payload := []byte(": hb 12345\n\n")
	ok := emitter.WriteRaw(payload)
	if !ok {
		t.Fatal("WriteRaw failed")
	}
	if recorder.Body.String() != string(payload) {
		t.Errorf("raw payload not written verbatim; got %q", recorder.Body.String())
	}
}

func TestSSEEmitter_ConcurrentSendsDoNotInterleave(t *testing.T) {
	// Two goroutines firing Send concurrently must not produce
	// torn frames. We can't directly observe "torn" — we observe
	// that the total body splits cleanly along "\n\n" into N
	// frames, each parsing as valid JSON.
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	emitter := NewSSEEmitter(&buf, noopFlusher{}, "[Test]")

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			emitter.Send(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventMessage,
				MessageID: "concurrent",
			})
		}(i)
	}
	wg.Wait()

	body := buf.String()
	// Split on "\n\n" — N events → at least N+1 substrings (last
	// is empty because every frame ends with \n\n).
	parts := strings.Split(body, "\n\n")
	frameCount := 0
	for _, p := range parts {
		p = strings.TrimPrefix(p, "data: ")
		if p == "" {
			continue
		}
		var ev workagentModel.AgentSSEEvent
		if err := json.Unmarshal([]byte(p), &ev); err != nil {
			t.Errorf("torn frame in concurrent output: %q (err=%v)", p, err)
			continue
		}
		frameCount++
	}
	if frameCount != n {
		t.Errorf("expected %d clean frames, got %d", n, frameCount)
	}
}

// noopFlusher satisfies http.Flusher for tests that don't care
// about flushing — bytes.Buffer doesn't implement Flush.
type noopFlusher struct{}

func (noopFlusher) Flush() {}
