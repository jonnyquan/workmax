package workagent

// metrics.go — G15 (2026-05-17). Per-skill business metric event
// emitter for the work-agent module.
//
// The repo doesn't ship a Prometheus / OpenTelemetry pipeline today
// — the only sink for runtime observability is the lumberjack-backed
// text log written by globals.Info/Warn/Error. Standing up a
// metrics pipeline is a separate, larger conversation; this slice
// keeps the surface tiny by emitting metric events as structured
// JSON lines on the existing log stream with a unique
// "[WA-METRIC] " marker prefix.
//
// Wire shape:
//
//   [WA-METRIC] {"event":"wa_preflight_compose","skill":"ppt","latency_ms":42,"turn_id":...}
//
// Why a marker prefix instead of a separate logger / file: log
// shipping tooling (Vector / Fluent Bit / Promtail / Datadog
// Agent) can grep `[WA-METRIC] ` lines off the existing
// workmax.log file and re-parse the trailing JSON without any
// config change to the platform's logging stack. When the
// pipeline matures we can swap the production sink for an
// OpenTelemetry exporter without touching call sites.
//
// Why a Sink interface rather than direct globals.Info calls:
// tests need to assert what was emitted. The package-level
// defaultSink writes to the logger; tests substitute a
// RecordingSink and read events back.

import (
	"encoding/json"
	"fmt"
	"sync"

	"server/globals"
)

// MetricMarker is the line-prefix every emitted metric event
// carries. A downstream log shipper greps this exact string to
// carve metric events off the unified log stream.
const MetricMarker = "[WA-METRIC] "

// MetricSink is the destination for emitted metric events.
// Implementations: defaultSink (production — writes to globals.Info)
// and RecordingSink (tests — buffers events in memory).
type MetricSink interface {
	Emit(event string, fields map[string]any)
}

// loggerSink writes events as `[WA-METRIC] {json}` lines via the
// existing log pipeline. The marshal-failure path falls back to a
// plain Warn — a metric we can't serialize is a bug, but it must
// never block the agent flow it observes.
type loggerSink struct{}

func (loggerSink) Emit(event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	blob, err := json.Marshal(fields)
	if err != nil {
		globals.Warn(fmt.Sprintf("[WA-METRIC] marshal failed event=%s err=%v", event, err))
		return
	}
	globals.Info(MetricMarker + string(blob))
}

// activeSink is swappable so tests can capture emissions. Default
// is the logger sink. Production code never reassigns it.
var (
	activeSink   MetricSink = loggerSink{}
	activeSinkMu sync.RWMutex
)

// SetMetricSink swaps the active sink. Returns the previous sink
// so tests can defer-restore. The mutex protects against parallel
// test runs in the same process — though tests should avoid
// crossing the package boundary anyway.
func SetMetricSink(s MetricSink) MetricSink {
	activeSinkMu.Lock()
	defer activeSinkMu.Unlock()
	if s == nil {
		s = loggerSink{}
	}
	prev := activeSink
	activeSink = s
	return prev
}

// EmitMetric is the call site's entry point. Always non-blocking
// (sinks must absorb failures internally) and always safe — fields
// may be nil. Adds the canonical `event` field on the way through
// so call sites can't forget it.
//
// Field naming convention: snake_case keys, scalar values
// preferred (string, int, float, bool). Nested maps work but make
// downstream parsing harder.
func EmitMetric(event string, fields map[string]any) {
	if event == "" {
		return
	}
	activeSinkMu.RLock()
	sink := activeSink
	activeSinkMu.RUnlock()
	sink.Emit(event, fields)
}

// RecordingSink is the test helper. Stores every emitted event in
// order so tests can assert what was sent. Safe for concurrent
// use — preflight composition runs sub-millisecond but the M1/M2
// dispatchers can fire concurrently across goroutines in future.
type RecordingSink struct {
	mu     sync.Mutex
	events []RecordedMetric
}

// RecordedMetric is one captured emission. Fields is a copy so
// later mutation of the call site's map doesn't corrupt the
// recording.
type RecordedMetric struct {
	Event  string
	Fields map[string]any
}

// Emit implements MetricSink.
func (r *RecordingSink) Emit(event string, fields map[string]any) {
	cp := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		cp[k] = v
	}
	cp["event"] = event
	r.mu.Lock()
	r.events = append(r.events, RecordedMetric{Event: event, Fields: cp})
	r.mu.Unlock()
}

// Events returns a snapshot of captured events. Returned slice is
// a copy — safe to inspect without holding the lock.
func (r *RecordingSink) Events() []RecordedMetric {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecordedMetric, len(r.events))
	copy(out, r.events)
	return out
}

// FindByEvent returns the first recorded event with the given
// name, or nil if none. Convenience for tests that only care about
// one event type per scenario.
func (r *RecordingSink) FindByEvent(event string) *RecordedMetric {
	for _, e := range r.Events() {
		if e.Event == event {
			return &e
		}
	}
	return nil
}

// Reset clears captured events without re-creating the sink.
// Useful when tests run multiple scenarios against the same
// RecordingSink instance.
func (r *RecordingSink) Reset() {
	r.mu.Lock()
	r.events = nil
	r.mu.Unlock()
}
