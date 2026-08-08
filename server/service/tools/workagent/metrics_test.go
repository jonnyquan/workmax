package workagent

// metrics_test.go — G15 (2026-05-17). Pins the metric emitter
// contract:
//
//   1. EmitMetric routes through the active sink (default =
//      logger, swappable for tests).
//   2. RecordingSink captures event name + fields + adds the
//      canonical "event" key.
//   3. SetMetricSink returns the previous sink so defer-restore
//      works.
//   4. EmitMetric with empty event is a no-op (defensive against
//      forgotten event names — emitting a metric with no name
//      pollutes downstream queries).
//   5. JSON wire shape is parseable + carries the canonical fields
//      a downstream log shipper would key on.

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestEmitMetric_RoutesThroughActiveSink(t *testing.T) {
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	EmitMetric("test_event", map[string]any{
		"skill":      "ppt",
		"latency_ms": int64(42),
	})

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	got := events[0]
	if got.Event != "test_event" {
		t.Errorf("Event = %q, want %q", got.Event, "test_event")
	}
	if got.Fields["skill"] != "ppt" {
		t.Errorf("Fields[skill] = %v, want %q", got.Fields["skill"], "ppt")
	}
	if got.Fields["latency_ms"] != int64(42) {
		t.Errorf("Fields[latency_ms] = %v, want 42", got.Fields["latency_ms"])
	}
	// Canonical `event` field auto-injected by the sink so it
	// survives JSON round-trip even if call sites forget it.
	if got.Fields["event"] != "test_event" {
		t.Errorf("Fields[event] = %v, want %q (auto-injected)", got.Fields["event"], "test_event")
	}
}

func TestEmitMetric_EmptyEventIsNoOp(t *testing.T) {
	// Empty event name = bug. The emitter must drop silently
	// rather than ship `{"event":""}` lines that pollute
	// downstream queries.
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	EmitMetric("", map[string]any{"skill": "ppt"})

	if events := rec.Events(); len(events) != 0 {
		t.Errorf("expected 0 events for empty event name, got %d", len(events))
	}
}

func TestEmitMetric_NilFieldsAllowed(t *testing.T) {
	// Some call sites have no contextual fields (e.g. a pure
	// counter increment). Nil map must work and emit just the
	// canonical event field.
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	EmitMetric("counter_event", nil)

	events := rec.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Fields["event"] != "counter_event" {
		t.Errorf("expected auto-injected event field, got %v", events[0].Fields["event"])
	}
}

func TestRecordingSink_CopiesFieldsToProtectAgainstMutation(t *testing.T) {
	// Captured Fields map must be an independent copy — call sites
	// commonly reuse the map across loop iterations, and a
	// reference capture would silently corrupt earlier recordings.
	rec := &RecordingSink{}
	live := map[string]any{"skill": "ppt"}
	rec.Emit("event_a", live)
	live["skill"] = "logo" // would corrupt the capture if held by reference
	rec.Emit("event_b", live)

	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Fields["skill"] != "ppt" {
		t.Errorf("event[0].skill = %v, want %q (post-mutation corruption)", events[0].Fields["skill"], "ppt")
	}
	if events[1].Fields["skill"] != "logo" {
		t.Errorf("event[1].skill = %v, want %q", events[1].Fields["skill"], "logo")
	}
}

func TestRecordingSink_ConcurrentEmit(t *testing.T) {
	// Future M1/M2 dispatchers may emit from concurrent goroutines.
	// The recording sink's mutex must hold up under a parallel
	// write storm (Go's race detector will surface a missing lock).
	rec := &RecordingSink{}
	const goroutines = 50
	const perGoroutine = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				rec.Emit("concurrent_event", map[string]any{"i": j})
			}
		}()
	}
	wg.Wait()
	if got := len(rec.Events()); got != goroutines*perGoroutine {
		t.Errorf("expected %d events, got %d (lost under contention?)", goroutines*perGoroutine, got)
	}
}

func TestRecordingSink_FindByEvent(t *testing.T) {
	rec := &RecordingSink{}
	rec.Emit("alpha", map[string]any{"k": 1})
	rec.Emit("beta", map[string]any{"k": 2})
	rec.Emit("alpha", map[string]any{"k": 3})

	first := rec.FindByEvent("alpha")
	if first == nil {
		t.Fatal("FindByEvent should return first alpha")
	}
	if first.Fields["k"] != 1 {
		t.Errorf("FindByEvent returned k=%v, want 1 (first match)", first.Fields["k"])
	}
	if rec.FindByEvent("gamma") != nil {
		t.Error("FindByEvent should return nil for missing event")
	}
}

func TestSetMetricSink_ReturnsPreviousForRestore(t *testing.T) {
	sentinel := &RecordingSink{}
	originalPrev := SetMetricSink(sentinel)
	defer SetMetricSink(originalPrev)

	got := SetMetricSink(&RecordingSink{})
	// `got` should be the sentinel we installed, not the
	// process-default logger sink.
	if got != sentinel {
		t.Errorf("SetMetricSink should return the previously-active sink for defer-restore; got %v", got)
	}
}

func TestSetMetricSink_NilFallsBackToLoggerSink(t *testing.T) {
	// Defensive: passing nil should not panic and should not
	// strand the active sink as nil (every subsequent EmitMetric
	// would panic on nil-dispatch).
	prev := SetMetricSink(nil)
	defer SetMetricSink(prev)

	// Should not panic.
	EmitMetric("noop_event", nil)
}

func TestLoggerSink_JSONWireShape(t *testing.T) {
	// The downstream log shipper greps `[WA-METRIC] ` lines and
	// re-parses the trailing JSON. Test the shape end-to-end: a
	// real loggerSink emit, then re-parse the JSON we'd ship.
	//
	// We can't easily capture globals.Info output without
	// rewiring the logger, so reproduce the marshal step the
	// sink performs and assert the resulting line is parseable +
	// carries the canonical fields.
	fields := map[string]any{
		"event":      "wa_preflight_compose",
		"skill":      "ppt",
		"latency_ms": int64(42),
	}
	blob, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	line := MetricMarker + string(blob)

	if !strings.HasPrefix(line, "[WA-METRIC] ") {
		t.Errorf("line missing marker prefix: %q", line)
	}
	jsonPart := strings.TrimPrefix(line, MetricMarker)
	var roundTrip map[string]any
	if err := json.Unmarshal([]byte(jsonPart), &roundTrip); err != nil {
		t.Fatalf("downstream shipper would fail to parse: %v (input=%q)", err, jsonPart)
	}
	if roundTrip["event"] != "wa_preflight_compose" {
		t.Errorf("round-trip event = %v, want %q", roundTrip["event"], "wa_preflight_compose")
	}
	if roundTrip["skill"] != "ppt" {
		t.Errorf("round-trip skill = %v, want %q", roundTrip["skill"], "ppt")
	}
}
