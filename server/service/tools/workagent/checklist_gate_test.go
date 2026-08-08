package workagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"server/service/tools/workagent/detectors"
)

// stubDetector is a controllable Detector for gate tests. Each
// instance returns a fixed Result + optional error from Run.
type stubDetector struct {
	name   string
	result detectors.Result
	err    error
	calls  int
}

func (s *stubDetector) Name() string { return s.name }
func (s *stubDetector) Run(_ context.Context, _ detectors.Input) (detectors.Result, error) {
	s.calls++
	return s.result, s.err
}

func newTestRegistry(stubs ...*stubDetector) *detectors.Registry {
	r := detectors.NewRegistry()
	for _, s := range stubs {
		r.Register(s)
	}
	return r
}

func TestGate_EmptyChecklistPasses(t *testing.T) {
	res := RunGateWith(context.Background(), &Checklist{}, detectors.Input{}, newTestRegistry())
	if res.Decision != GatePass {
		t.Errorf("empty checklist should pass, got %v", res.Decision)
	}
}

func TestGate_AllPass(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "ok"}},
		P1: []ChecklistItem{{ID: "P1-1", Title: "y", Detector: "ok"}},
	}
	reg := newTestRegistry(&stubDetector{name: "ok", result: detectors.Pass()})
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GatePass {
		t.Errorf("all-pass should be GatePass, got %v", res.Decision)
	}
	if len(res.P0Fails) != 0 || len(res.P1Fails) != 0 {
		t.Errorf("no fails expected, got P0=%d P1=%d", len(res.P0Fails), len(res.P1Fails))
	}
}

func TestGate_P0FailBlocks(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "fail_p0"}},
		P1: []ChecklistItem{{ID: "P1-1", Title: "y", Detector: "ok"}},
	}
	reg := newTestRegistry(
		&stubDetector{name: "fail_p0", result: detectors.Fail("metric without source")},
		&stubDetector{name: "ok", result: detectors.Pass()},
	)
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GateBlock {
		t.Errorf("P0 fail should block, got %v", res.Decision)
	}
	if len(res.P0Fails) != 1 {
		t.Fatalf("expected 1 P0 fail, got %d", len(res.P0Fails))
	}
	if !strings.Contains(res.P0Fails[0].Result.Issues[0], "metric") {
		t.Errorf("issue should bubble up")
	}
}

func TestGate_P1FailWarnsWhenP0Pass(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "ok"}},
		P1: []ChecklistItem{{ID: "P1-1", Title: "y", Detector: "fail_p1"}},
	}
	reg := newTestRegistry(
		&stubDetector{name: "ok", result: detectors.Pass()},
		&stubDetector{name: "fail_p1", result: detectors.Fail("low contrast")},
	)
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GateWarn {
		t.Errorf("P1-only fail should warn, got %v", res.Decision)
	}
}

func TestGate_P0BlockBeatsP1Warn(t *testing.T) {
	// When BOTH P0 and P1 fail, decision is Block (not downgraded
	// to Warn).
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "fail"}},
		P1: []ChecklistItem{{ID: "P1-1", Title: "y", Detector: "fail"}},
	}
	reg := newTestRegistry(&stubDetector{name: "fail", result: detectors.Fail("issue")})
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GateBlock {
		t.Errorf("P0+P1 both failing should be Block, got %v", res.Decision)
	}
}

func TestGate_P2NeverAffectsDecision(t *testing.T) {
	c := &Checklist{
		P2: []ChecklistItem{{ID: "P2-1", Title: "x", Detector: "fail"}},
	}
	reg := newTestRegistry(&stubDetector{name: "fail", result: detectors.Fail("nice-to-have miss")})
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GatePass {
		t.Errorf("P2 fail must not change Decision, got %v", res.Decision)
	}
	if len(res.P2Notes) != 1 {
		t.Errorf("P2 fail should be recorded in P2Notes, got %d", len(res.P2Notes))
	}
}

func TestGate_SkippedDoesNotFail(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "skip"}},
	}
	reg := newTestRegistry(&stubDetector{name: "skip", result: detectors.Skipped("no spec")})
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GatePass {
		t.Errorf("Skipped P0 must not block, got %v", res.Decision)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped item should be recorded, got %d", len(res.Skipped))
	}
}

func TestGate_MissingDetectorSkips(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "ghost"}},
	}
	reg := newTestRegistry() // no detectors
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GatePass {
		t.Errorf("missing detector must degrade to skip+pass, got %v", res.Decision)
	}
	if len(res.Skipped) != 1 {
		t.Errorf("expected 1 skipped, got %d", len(res.Skipped))
	}
}

func TestGate_DetectorErrorSkips(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{{ID: "P0-1", Title: "x", Detector: "broken"}},
	}
	reg := newTestRegistry(&stubDetector{
		name:   "broken",
		result: detectors.Result{},
		err:    errors.New("network timeout"),
	})
	res := RunGateWith(context.Background(), c, detectors.Input{}, reg)
	if res.Decision != GatePass {
		t.Errorf("detector error should not block, got %v", res.Decision)
	}
}

func TestPreflightDigest_Format(t *testing.T) {
	c := &Checklist{
		P0: []ChecklistItem{
			{Title: "honest_data", Description: "no fabricated metrics"},
		},
		P1: []ChecklistItem{
			{Title: "color_contrast", Description: "≥ AA"},
		},
		P2: []ChecklistItem{
			{Title: "consistent_grid", Description: "8pt"},
		},
	}
	digest := PreflightDigest(c)
	if !strings.Contains(digest, "<skill-checklist>") {
		t.Errorf("missing wrapper tag")
	}
	if !strings.Contains(digest, "<p0>") || !strings.Contains(digest, "<p1>") {
		t.Errorf("missing priority subtags")
	}
	if strings.Contains(digest, "<p2>") {
		t.Errorf("P2 should not appear in digest")
	}
	if !strings.Contains(digest, "honest_data") {
		t.Errorf("P0 title missing")
	}
}

func TestPreflightDigest_EmptyChecklistEmptyOutput(t *testing.T) {
	if got := PreflightDigest(&Checklist{}); got != "" {
		t.Errorf("empty checklist should produce empty digest, got %q", got)
	}
	if got := PreflightDigest(nil); got != "" {
		t.Errorf("nil checklist should produce empty digest, got %q", got)
	}
}

func TestFormatBlockFindings_Empty(t *testing.T) {
	if got := FormatBlockFindings(nil); got != "" {
		t.Errorf("nil fails should produce empty findings, got %q", got)
	}
}

// PR-9b/2a tests for AttachChecklistGateResult — the read-only
// metadata enrichment that rides on the done event.

func TestAttachChecklistGateResult_PassDecisionAttaches(t *testing.T) {
	original := []byte(`{"type":"result","subtype":"success"}`)
	gate := GateResult{Decision: GatePass}
	got := AttachChecklistGateResult(original, gate)

	var payload map[string]interface{}
	if err := json.Unmarshal(got, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["type"] != "result" {
		t.Errorf("original fields lost: %v", payload)
	}
	gateField, ok := payload["checklist_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("checklist_gate field missing")
	}
	if gateField["decision"] != "pass" {
		t.Errorf("expected decision=pass, got %v", gateField["decision"])
	}
	if _, hasRedo := gateField["redo_prompt"]; hasRedo {
		t.Errorf("pass decision should not carry redo_prompt")
	}
}

func TestAttachChecklistGateResult_BlockCarriesRedoPrompt(t *testing.T) {
	original := []byte(`{"type":"result"}`)
	gate := GateResult{
		Decision: GateBlock,
		P0Fails: []GateItemResult{
			{
				Item:   ChecklistItem{ID: "P0-1", Title: "honest_data"},
				Result: detectors.Fail("metric without source"),
			},
		},
	}
	got := AttachChecklistGateResult(original, gate)

	var payload map[string]interface{}
	_ = json.Unmarshal(got, &payload)
	gateField := payload["checklist_gate"].(map[string]interface{})
	if gateField["decision"] != "block" {
		t.Errorf("expected block, got %v", gateField["decision"])
	}
	redoPrompt, ok := gateField["redo_prompt"].(string)
	if !ok || !strings.Contains(redoPrompt, "Please regenerate") {
		t.Errorf("block decision should carry redo_prompt with regenerate text, got %v", gateField["redo_prompt"])
	}
	p0 := gateField["p0_fails"].([]interface{})
	if len(p0) != 1 {
		t.Errorf("expected 1 p0 fail entry, got %d", len(p0))
	}
}

func TestAttachChecklistGateResult_WarnNoRedoPrompt(t *testing.T) {
	original := []byte(`{"type":"result"}`)
	gate := GateResult{
		Decision: GateWarn,
		P1Fails: []GateItemResult{
			{
				Item:   ChecklistItem{ID: "P1-1", Title: "color_contrast"},
				Result: detectors.Fail("contrast 3.2:1"),
			},
		},
	}
	got := AttachChecklistGateResult(original, gate)
	var payload map[string]interface{}
	_ = json.Unmarshal(got, &payload)
	gateField := payload["checklist_gate"].(map[string]interface{})
	if gateField["decision"] != "warn" {
		t.Errorf("expected warn, got %v", gateField["decision"])
	}
	if _, hasRedo := gateField["redo_prompt"]; hasRedo {
		t.Errorf("warn decision should not carry redo_prompt")
	}
	p1 := gateField["p1_fails"].([]interface{})
	if len(p1) != 1 {
		t.Errorf("expected 1 p1 fail, got %d", len(p1))
	}
}

func TestAttachChecklistGateResult_NonObjectResultPreserved(t *testing.T) {
	// SDK ResultMessage occasionally arrives as null / string /
	// array — attach must NOT clobber a non-object payload.
	for _, original := range [][]byte{[]byte(`null`), []byte(`"string error"`), []byte(`[1,2,3]`)} {
		got := AttachChecklistGateResult(original, GateResult{Decision: GatePass})
		if string(got) != string(original) {
			t.Errorf("non-object original mutated: was %q, got %q", string(original), string(got))
		}
	}
}

func TestFormatBlockFindings_FormatsForRedo(t *testing.T) {
	fails := []GateItemResult{
		{
			Item:   ChecklistItem{Title: "honest_data"},
			Result: detectors.Fail("提升 30% — no source"),
		},
		{
			Item:   ChecklistItem{Title: "brand_color_compliance"},
			Result: detectors.Fail("hex #FF00FF not in brand-spec.md"),
		},
	}
	out := FormatBlockFindings(fails)
	if !strings.Contains(out, "<lint-findings priority=\"P0\">") {
		t.Errorf("missing wrapper")
	}
	if !strings.Contains(out, "honest_data") || !strings.Contains(out, "提升 30%") {
		t.Errorf("first issue missing")
	}
	if !strings.Contains(out, "Please regenerate fixing these 2 issues") {
		t.Errorf("redo prompt missing")
	}
}
