package workagent

// metrics_emit_test.go — G15 (2026-05-17). Pins that the three
// in-package emit sites (preflight compose, side-files load,
// discovery persist) actually fire under realistic conditions.
//
// The fourth emit site (wa_skill_switch) lives in
// server/api/pro/tools/workagent/agent_turn_phases.go and is
// covered by the API-layer test suite — too much auth/SDK setup
// to exercise from this package.

import (
	"testing"

	"server/utils/testutil"
)

// TestEmit_PreflightCompose — every Build* call must fire one
// wa_preflight_compose event with skill + latency + output_len.
func TestEmit_PreflightCompose(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	_ = BuildPreflightAdditionsForThread(42, "ppt", 9001)

	ev := rec.FindByEvent("wa_preflight_compose")
	if ev == nil {
		t.Fatal("BuildPreflightAdditionsForThread should emit wa_preflight_compose")
	}
	if ev.Fields["skill"] != "ppt" {
		t.Errorf("skill = %v, want %q", ev.Fields["skill"], "ppt")
	}
	// thread_id is uint at the call site; the recorded value
	// preserves the type so downstream tooling reading the JSON
	// will see a numeric.
	if ev.Fields["thread_id"] != uint(9001) {
		t.Errorf("thread_id = %v (%T), want 9001", ev.Fields["thread_id"], ev.Fields["thread_id"])
	}
	if _, ok := ev.Fields["latency_ms"].(int64); !ok {
		t.Errorf("latency_ms = %v (%T), want int64", ev.Fields["latency_ms"], ev.Fields["latency_ms"])
	}
	if _, ok := ev.Fields["output_len"]; !ok {
		t.Error("missing output_len field")
	}
	if _, ok := ev.Fields["layer_count"]; !ok {
		t.Error("missing layer_count field")
	}
}

// TestEmit_PreflightCompose_FiresEvenForEmptyOutput — the no-op
// path (empty skillName) returns before the metric; but a real
// skill name that produces no composer output (no threadID, no
// brand, no library) must still emit. Pins the "always emit"
// invariant from the compose site.
func TestEmit_PreflightCompose_FiresForSkillWithNoContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	// uid=0 + threadID=0 + skillName=ppt → composer has nothing
	// to add beyond the universal layers (which themselves degrade
	// to empty under zero context). The metric still fires.
	_ = BuildPreflightAdditionsForThread(0, "ppt", 0)

	if ev := rec.FindByEvent("wa_preflight_compose"); ev == nil {
		t.Fatal("compose metric must fire even when context is empty (no-op path observability)")
	}
}

// TestEmit_PreflightCompose_EmptySkillSkipsEmit — the early
// return on empty skillName must NOT emit. The metric is keyed by
// skill; emitting with skill="" would pollute downstream queries.
func TestEmit_PreflightCompose_EmptySkillSkipsEmit(t *testing.T) {
	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	_ = BuildPreflightAdditionsForThread(42, "", 9001)

	if ev := rec.FindByEvent("wa_preflight_compose"); ev != nil {
		t.Errorf("empty-skill early return must skip the metric; got %v", ev.Fields)
	}
}

// TestEmit_SideFiles — ppt today ships side files. Loading ppt's
// preflight must fire wa_preflight_side_files with file_count > 0
// and a non-zero approx_tokens estimate.
func TestEmit_SideFiles(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	_ = BuildPreflightAdditionsForThread(42, "ppt", 9002)

	ev := rec.FindByEvent("wa_preflight_side_files")
	if ev == nil {
		t.Fatal("ppt preflight should emit wa_preflight_side_files (ppt ships side files)")
	}
	if ev.Fields["skill"] != "ppt" {
		t.Errorf("skill = %v, want %q", ev.Fields["skill"], "ppt")
	}
	if fc, ok := ev.Fields["file_count"].(int); !ok || fc < 1 {
		t.Errorf("file_count = %v (%T), want int >= 1", ev.Fields["file_count"], ev.Fields["file_count"])
	}
	if tb, ok := ev.Fields["total_bytes"].(int); !ok || tb < 1 {
		t.Errorf("total_bytes = %v (%T), want int >= 1", ev.Fields["total_bytes"], ev.Fields["total_bytes"])
	}
	if at, ok := ev.Fields["approx_tokens"].(int); !ok || at < 1 {
		t.Errorf("approx_tokens = %v (%T), want int >= 1", ev.Fields["approx_tokens"], ev.Fields["approx_tokens"])
	}
}

// TestEmit_SideFiles_SkippedWhenSkillShipsNone — logo has no
// side files today. The emit site is gated on len(sideFiles) > 0
// so the metric must NOT fire — otherwise downstream "avg
// approx_tokens per skill" would be skewed by zero-rows.
func TestEmit_SideFiles_SkippedWhenSkillShipsNone(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	_ = BuildPreflightAdditionsForThread(42, "logo", 9003)

	if ev := rec.FindByEvent("wa_preflight_side_files"); ev != nil {
		t.Errorf("logo ships no side files; the metric must not fire (would skew per-skill averages); got %v", ev.Fields)
	}
}

// TestEmit_DiscoveryPersist — every successful PersistDiscovery
// Answers must fire wa_discovery_persist with skill + source +
// answer_count.
func TestEmit_DiscoveryPersist(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	PersistDiscoveryAnswers(42, 9004, "ppt", map[string]string{
		"audience": "exec",
		"tone":     "formal",
	}, "user_submitted")

	ev := rec.FindByEvent("wa_discovery_persist")
	if ev == nil {
		t.Fatal("PersistDiscoveryAnswers must emit wa_discovery_persist")
	}
	if ev.Fields["skill"] != "ppt" {
		t.Errorf("skill = %v, want %q (form_id used as skill key)", ev.Fields["skill"], "ppt")
	}
	if ev.Fields["source"] != "user_submitted" {
		t.Errorf("source = %v, want %q", ev.Fields["source"], "user_submitted")
	}
	if ev.Fields["answer_count"] != 2 {
		t.Errorf("answer_count = %v, want 2", ev.Fields["answer_count"])
	}
}

// TestEmit_DiscoveryPersist_SurfacesSkipPath — pin that a skip
// submission emits source="user_skipped" so downstream "skip rate
// per skill" queries can rate against the user_submitted total.
func TestEmit_DiscoveryPersist_SurfacesSkipPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	PersistDiscoveryAnswers(42, 9005, "socialAd", map[string]string{
		"platform": "douyin",
	}, "user_skipped")

	ev := rec.FindByEvent("wa_discovery_persist")
	if ev == nil {
		t.Fatal("skip path must still emit (with source=user_skipped)")
	}
	if ev.Fields["source"] != "user_skipped" {
		t.Errorf("source = %v, want %q", ev.Fields["source"], "user_skipped")
	}
	if ev.Fields["skill"] != "socialAd" {
		t.Errorf("skill = %v, want %q", ev.Fields["skill"], "socialAd")
	}
}

// TestEmit_DiscoveryPersist_DefaultsSourceWhenEmpty — empty
// skipReason defaults to "user_submitted" (per the writer's
// behaviour). The metric must reflect the defaulted value, not
// the empty string, so downstream queries can group by source
// uniformly.
func TestEmit_DiscoveryPersist_DefaultsSourceWhenEmpty(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDBForPreflight(t, db)

	rec := &RecordingSink{}
	prev := SetMetricSink(rec)
	defer SetMetricSink(prev)

	PersistDiscoveryAnswers(42, 9006, "ppt", map[string]string{"k": "v"}, "")

	ev := rec.FindByEvent("wa_discovery_persist")
	if ev == nil {
		t.Fatal("default-source path must still emit")
	}
	if ev.Fields["source"] != "user_submitted" {
		t.Errorf("source = %v, want %q (writer defaults empty → user_submitted)", ev.Fields["source"], "user_submitted")
	}
}
