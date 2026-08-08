package workagent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseCritiqueResponse_HappyPath — the SDK-perfect case: the
// sub-agent returns exactly the SKILL.md-mandated JSON shape with
// no prose framing. Verifies every field round-trips through the
// loose intermediate.
func TestParseCritiqueResponse_HappyPath(t *testing.T) {
	raw := `{
  "overall_score": 7.4,
  "verdict": "目标对齐但视觉粗糙",
  "dimensions": {
    "goal_fit":      { "score": 8, "notes": "对齐用户陈述的需求", "fixes": ["..."] },
    "hierarchy":     { "score": 7, "notes": "三个层级建立但第3页跳变", "fixes": ["统一标题字号"] },
    "craft":         { "score": 6, "notes": "对齐有微瑕", "fixes": ["P3 标题左对齐", "P5 间距修复"] },
    "functionality": { "score": 8, "notes": "无装饰浪费", "fixes": [] },
    "originality":   { "score": 7, "notes": "避开了 slop", "fixes": [] }
  },
  "top_fixes": [
    "把 P3 标题字号从 24 加到 36",
    "P5 卡片间距统一到 16px",
    "替换 P7 的 stock 插画"
  ]
}`
	result, err := ParseCritiqueResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.OverallScore != 7.4 {
		t.Errorf("overall_score = %v, want 7.4", result.OverallScore)
	}
	if !strings.Contains(result.Verdict, "目标对齐") {
		t.Errorf("verdict not preserved: %q", result.Verdict)
	}
	if got := result.Dimensions[CritiqueCraft].Score; got != 6 {
		t.Errorf("craft score = %d, want 6", got)
	}
	if len(result.Dimensions[CritiqueCraft].Fixes) != 2 {
		t.Errorf("craft fixes count = %d, want 2", len(result.Dimensions[CritiqueCraft].Fixes))
	}
	if len(result.TopFixes) != 3 {
		t.Errorf("top_fixes count = %d, want 3", len(result.TopFixes))
	}
}

// TestParseCritiqueResponse_StripsProseFraming — the realistic case:
// model wraps the JSON in "Here's the review:\n```json\n{...}\n```\n
// Hope this helps!". The parser must extract the embedded object
// rather than fail.
func TestParseCritiqueResponse_StripsProseFraming(t *testing.T) {
	raw := "Here is the structured review.\n\n```json\n{\n  \"overall_score\": 7,\n  \"verdict\": \"ok\",\n  \"dimensions\": {\n    \"goal_fit\": { \"score\": 7, \"notes\": \"\", \"fixes\": [] },\n    \"hierarchy\": { \"score\": 8, \"notes\": \"\", \"fixes\": [] },\n    \"craft\": { \"score\": 7, \"notes\": \"\", \"fixes\": [] },\n    \"functionality\": { \"score\": 7, \"notes\": \"\", \"fixes\": [] },\n    \"originality\": { \"score\": 7, \"notes\": \"\", \"fixes\": [] }\n  },\n  \"top_fixes\": []\n}\n```\n\nHope this helps!"
	result, err := ParseCritiqueResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Verdict != "ok" {
		t.Errorf("verdict = %q, want 'ok'", result.Verdict)
	}
	if result.Dimensions[CritiqueGoalFit].Score != 7 {
		t.Errorf("goal_fit not parsed despite prose framing")
	}
}

// TestParseCritiqueResponse_TolerratesStringScores — Haiku
// occasionally emits scores as strings. The two-pass decoder must
// handle the type drift without failing the whole parse.
func TestParseCritiqueResponse_TolerratesStringScores(t *testing.T) {
	raw := `{
  "overall_score": "7.5",
  "verdict": "ok",
  "dimensions": {
    "goal_fit":      { "score": "8", "notes": "", "fixes": [] },
    "hierarchy":     { "score": 7,   "notes": "", "fixes": [] },
    "craft":         { "score": "7", "notes": "", "fixes": [] },
    "functionality": { "score": 8,   "notes": "", "fixes": [] },
    "originality":   { "score": "7", "notes": "", "fixes": [] }
  },
  "top_fixes": []
}`
	result, err := ParseCritiqueResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.OverallScore != 7.5 {
		t.Errorf("overall_score = %v, want 7.5", result.OverallScore)
	}
	if result.Dimensions[CritiqueGoalFit].Score != 8 {
		t.Errorf("string score 8 not parsed")
	}
	if result.Dimensions[CritiqueCraft].Score != 7 {
		t.Errorf("string score 7 not parsed")
	}
}

// TestParseCritiqueResponse_ClampsOutOfRangeScores — the model
// sometimes hallucinates 12 / -1 / 0.85. Clamp into [0,10] integer
// rather than failing the parse.
func TestParseCritiqueResponse_ClampsOutOfRangeScores(t *testing.T) {
	raw := `{
  "overall_score": 12,
  "verdict": "weird",
  "dimensions": {
    "goal_fit":      { "score": 12,   "notes": "", "fixes": [] },
    "hierarchy":     { "score": -3,   "notes": "", "fixes": [] },
    "craft":         { "score": 0.85, "notes": "", "fixes": [] },
    "functionality": { "score": 8,    "notes": "", "fixes": [] },
    "originality":   { "score": 7,    "notes": "", "fixes": [] }
  },
  "top_fixes": []
}`
	result, err := ParseCritiqueResponse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := result.Dimensions[CritiqueGoalFit].Score; got != 10 {
		t.Errorf("12 should clamp to 10, got %d", got)
	}
	if got := result.Dimensions[CritiqueHierarchy].Score; got != 0 {
		t.Errorf("-3 should clamp to 0, got %d", got)
	}
	if got := result.Dimensions[CritiqueCraft].Score; got != 1 {
		t.Errorf("0.85 should round to 1, got %d", got)
	}
}

// TestParseCritiqueResponse_RejectsEmpty — empty / whitespace input
// is unambiguously a failure (no graceful degradation).
func TestParseCritiqueResponse_RejectsEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t\n"} {
		_, err := ParseCritiqueResponse(raw)
		if err == nil {
			t.Errorf("expected error on empty input %q", raw)
		}
	}
}

// TestParseCritiqueResponse_RejectsNoJSONObject — pure prose with
// no { ... } block must error so the dispatcher can surface "model
// didn't return JSON" rather than silently passing a zero-value.
func TestParseCritiqueResponse_RejectsNoJSONObject(t *testing.T) {
	_, err := ParseCritiqueResponse("Sorry, I can't critique this without seeing the artifact.")
	if err == nil {
		t.Errorf("expected error when no JSON object present")
	}
}

// TestEvaluateCritique_AllPass — every axis ≥ block, overall ≥ warn
// → Pass.
func TestEvaluateCritique_AllPass(t *testing.T) {
	result := buildCritiqueResult(8, 8, 8, 8, 8, 8.0)
	got := EvaluateCritique(result, DefaultCritiqueThresholds())
	if got != CritiquePass {
		t.Errorf("decision = %s, want pass", got)
	}
}

// TestEvaluateCritique_LowAxisBlocks — one axis below the block
// threshold (regardless of overall) demotes to Block.
func TestEvaluateCritique_LowAxisBlocks(t *testing.T) {
	// craft = 5, every other = 9, overall = 8.2. Average looks
	// fine but craft alone trips the gate.
	result := buildCritiqueResult(9, 9, 5, 9, 9, 8.2)
	got := EvaluateCritique(result, DefaultCritiqueThresholds())
	if got != CritiqueBlock {
		t.Errorf("decision = %s, want block (one axis below 7)", got)
	}
}

// TestEvaluateCritique_AllAxesPassButLowOverallWarns — all axes
// equal to BlockBelow exactly, overall < WarnBelow → Warn.
func TestEvaluateCritique_AllAxesPassButLowOverallWarns(t *testing.T) {
	result := buildCritiqueResult(7, 7, 7, 7, 7, 7.0)
	got := EvaluateCritique(result, DefaultCritiqueThresholds())
	if got != CritiqueWarn {
		t.Errorf("decision = %s, want warn (all 7s = avg 7 < warn 8)", got)
	}
}

// TestEvaluateCritique_MissingAxisBlocks — a partial result (4 of 5
// dimensions) treats the missing axis as 0 → Block. Defends
// against silent pass-through when the sub-agent's JSON is truncated.
func TestEvaluateCritique_MissingAxisBlocks(t *testing.T) {
	result := CritiqueResult{
		OverallScore: 9,
		Dimensions: map[CritiqueDimension]CritiqueDimensionScore{
			CritiqueGoalFit:       {Score: 9},
			CritiqueHierarchy:     {Score: 9},
			CritiqueCraft:         {Score: 9},
			CritiqueFunctionality: {Score: 9},
			// originality intentionally absent
		},
	}
	got := EvaluateCritique(result, DefaultCritiqueThresholds())
	if got != CritiqueBlock {
		t.Errorf("decision = %s, want block (missing axis)", got)
	}
}

// TestEvaluateCritique_ZeroResultBlocks — the parser-failed degraded
// case: everything zero. Must Block, not silently Pass.
func TestEvaluateCritique_ZeroResultBlocks(t *testing.T) {
	got := EvaluateCritique(CritiqueResult{}, DefaultCritiqueThresholds())
	if got != CritiqueBlock {
		t.Errorf("decision = %s, want block on zero result", got)
	}
}

// TestFormatCritiqueRedoPrompt_IncludesTopFixes — top_fixes lead the
// prompt body, capped at 5 entries.
func TestFormatCritiqueRedoPrompt_IncludesTopFixes(t *testing.T) {
	result := buildCritiqueResult(9, 9, 5, 9, 9, 8.2)
	result.TopFixes = []string{"fix1", "fix2", "fix3", "fix4", "fix5", "fix6", "fix7"}
	out := FormatCritiqueRedoPrompt(result, DefaultCritiqueThresholds())
	if !strings.Contains(out, "fix1") || !strings.Contains(out, "fix5") {
		t.Errorf("top_fixes 1+5 missing from output: %q", out)
	}
	if strings.Contains(out, "fix6") {
		t.Errorf("top_fixes capped at 5; fix6 leaked: %q", out)
	}
}

// TestFormatCritiqueRedoPrompt_OnlySurfacesFailingAxes — axes ≥
// BlockBelow stay out of the redo prompt; the agent already knows
// those are fine.
func TestFormatCritiqueRedoPrompt_OnlySurfacesFailingAxes(t *testing.T) {
	result := buildCritiqueResult(9, 9, 5, 9, 9, 8.2)
	result.Dimensions[CritiqueCraft] = CritiqueDimensionScore{
		Score: 5,
		Notes: "对齐有微瑕",
		Fixes: []string{"P3 标题左对齐"},
	}
	out := FormatCritiqueRedoPrompt(result, DefaultCritiqueThresholds())
	if !strings.Contains(out, "[craft = 5]") {
		t.Errorf("failing axis 'craft' missing from output: %q", out)
	}
	if strings.Contains(out, "[goal_fit") || strings.Contains(out, "[hierarchy") {
		t.Errorf("passing axes should not surface in redo prompt: %q", out)
	}
}

// TestFormatCritiqueRedoPrompt_DeterministicAxisOrder — undercut
// axes appear in CritiqueDimensions() canonical order, not
// map-iteration order. Critical for prompt-cache hits on the
// redo turn.
func TestFormatCritiqueRedoPrompt_DeterministicAxisOrder(t *testing.T) {
	// Two failing axes; we want goal_fit (canonical first) before
	// craft (canonical third) regardless of insertion order.
	result := CritiqueResult{
		Dimensions: map[CritiqueDimension]CritiqueDimensionScore{
			CritiqueCraft:         {Score: 4, Notes: "对齐差", Fixes: []string{"fix-craft"}},
			CritiqueGoalFit:       {Score: 5, Notes: "目标偏移", Fixes: []string{"fix-goal"}},
			CritiqueHierarchy:     {Score: 9},
			CritiqueFunctionality: {Score: 9},
			CritiqueOriginality:   {Score: 9},
		},
	}
	out := FormatCritiqueRedoPrompt(result, DefaultCritiqueThresholds())
	goalIdx := strings.Index(out, "goal_fit")
	craftIdx := strings.Index(out, "craft = 4")
	if goalIdx < 0 || craftIdx < 0 {
		t.Fatalf("missing axis labels in output: %q", out)
	}
	if goalIdx > craftIdx {
		t.Errorf("axis order non-canonical: goal_fit at %d, craft at %d", goalIdx, craftIdx)
	}
}

// TestExtractFirstJSONObject_HandlesBracesInStrings — `{"k":"}"}` is
// a valid object with a brace inside the string literal. The
// brace-counter must not close on the inner '}'.
func TestExtractFirstJSONObject_HandlesBracesInStrings(t *testing.T) {
	in := `prelude {"k":"}"}` + ` postlude`
	got := extractFirstJSONObject(in)
	want := `{"k":"}"}`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestExtractFirstJSONObject_HandlesEscapedQuotes — `{"k":"\""}` has
// an escaped quote inside the string. The escape handler must not
// mistakenly toggle inString state.
func TestExtractFirstJSONObject_HandlesEscapedQuotes(t *testing.T) {
	in := `{"k":"a\"b"}`
	got := extractFirstJSONObject(in)
	if got != in {
		t.Errorf("got %q, want %q", got, in)
	}
}

// TestAttachCritiqueGateResult_HappyPath — happy path: payload
// gets the critique_gate field with verdict, dimensions, top_fixes.
// Decision string round-trips for the frontend renderer.
func TestAttachCritiqueGateResult_HappyPath(t *testing.T) {
	original := []byte(`{"session_id":"abc","num_turns":3}`)
	result := buildCritiqueResult(8, 8, 8, 8, 8, 8.2)
	result.Verdict = "目标对齐 + 视觉清晰"
	result.TopFixes = []string{"fix-a", "fix-b"}

	out := AttachCritiqueGateResult(original, result, CritiquePass, "ppt")

	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("decode attached payload: %v", err)
	}
	gate, ok := decoded["critique_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("critique_gate field missing or wrong shape: %v", decoded)
	}
	if gate["decision"] != "pass" {
		t.Errorf("decision = %v, want pass", gate["decision"])
	}
	if gate["skill"] != "ppt" {
		t.Errorf("skill = %v, want ppt", gate["skill"])
	}
	if gate["verdict"] != "目标对齐 + 视觉清晰" {
		t.Errorf("verdict not preserved: %v", gate["verdict"])
	}
	// Pre-existing fields must survive the attach.
	if decoded["session_id"] != "abc" {
		t.Errorf("original session_id lost: %v", decoded["session_id"])
	}
}

// TestAttachCritiqueGateResult_BlockIncludesRedoPrompt — only the
// Block decision attaches a redo_prompt. Pass / Warn don't (saves
// the frontend from having to inspect an empty string).
func TestAttachCritiqueGateResult_BlockIncludesRedoPrompt(t *testing.T) {
	result := buildCritiqueResult(9, 9, 4, 9, 9, 8.0)
	result.Dimensions[CritiqueCraft] = CritiqueDimensionScore{
		Score: 4,
		Notes: "对齐差",
		Fixes: []string{"P3 标题左对齐"},
	}
	result.TopFixes = []string{"P3 对齐"}

	for _, tc := range []struct {
		decision        CritiqueDecision
		expectRedoField bool
	}{
		{CritiquePass, false},
		{CritiqueWarn, false},
		{CritiqueBlock, true},
	} {
		out := AttachCritiqueGateResult([]byte(`{}`), result, tc.decision, "ppt")
		var decoded map[string]interface{}
		_ = json.Unmarshal(out, &decoded)
		gate := decoded["critique_gate"].(map[string]interface{})
		_, has := gate["redo_prompt"]
		if has != tc.expectRedoField {
			t.Errorf("decision=%s redo_prompt present=%v, want %v",
				tc.decision, has, tc.expectRedoField)
		}
	}
}

// TestAttachCritiqueGateResult_NullPayloadSurvives — defensive
// guard: a "null" original payload (Unmarshal returns nil map)
// must NOT panic on assignment. Mirrors AttachChecklistGateResult's
// posture.
func TestAttachCritiqueGateResult_NullPayloadSurvives(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("null payload should not panic: %v", r)
		}
	}()
	out := AttachCritiqueGateResult(
		[]byte(`null`),
		buildCritiqueResult(8, 8, 8, 8, 8, 8.0),
		CritiquePass,
		"ppt",
	)
	// Confirm we got a valid object back, not the literal null.
	var decoded map[string]interface{}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Errorf("output should be a JSON object, got %s", string(out))
	}
	if decoded["critique_gate"] == nil {
		t.Errorf("critique_gate should be attached even on null input")
	}
}

// TestAttachCritiqueGateResult_DimensionOrderingDeterministic — the
// dimensions slice in the wire payload follows CritiqueDimensions()
// canonical order regardless of map insertion order. Frontend can
// rely on goal_fit being [0], hierarchy [1], etc.
func TestAttachCritiqueGateResult_DimensionOrderingDeterministic(t *testing.T) {
	// Insert in reverse order — the formatter must still emit
	// goal_fit first, originality last.
	result := CritiqueResult{
		Dimensions: map[CritiqueDimension]CritiqueDimensionScore{
			CritiqueOriginality:   {Score: 5},
			CritiqueFunctionality: {Score: 6},
			CritiqueCraft:         {Score: 7},
			CritiqueHierarchy:     {Score: 8},
			CritiqueGoalFit:       {Score: 9},
		},
	}
	out := AttachCritiqueGateResult([]byte(`{}`), result, CritiquePass, "")
	var decoded map[string]interface{}
	_ = json.Unmarshal(out, &decoded)
	dims := decoded["critique_gate"].(map[string]interface{})["dimensions"].([]interface{})
	if len(dims) != 5 {
		t.Fatalf("dimensions length = %d, want 5", len(dims))
	}
	if dims[0].(map[string]interface{})["id"] != "goal_fit" {
		t.Errorf("dim[0] = %v, want goal_fit", dims[0])
	}
	if dims[4].(map[string]interface{})["id"] != "originality" {
		t.Errorf("dim[4] = %v, want originality", dims[4])
	}
}

// TestAttachCritiqueGateResult_OmitSkillWhenEmpty — empty skill
// param should NOT emit `"skill": ""` (the JSON would be noisy).
func TestAttachCritiqueGateResult_OmitSkillWhenEmpty(t *testing.T) {
	out := AttachCritiqueGateResult(
		[]byte(`{}`),
		buildCritiqueResult(8, 8, 8, 8, 8, 8.0),
		CritiquePass,
		"",
	)
	var decoded map[string]interface{}
	_ = json.Unmarshal(out, &decoded)
	gate := decoded["critique_gate"].(map[string]interface{})
	if _, has := gate["skill"]; has {
		t.Errorf("skill field should be omitted when empty, got %v", gate["skill"])
	}
}

// buildCritiqueResult is a test helper — fills the 5-axis map
// uniformly so per-test changes (one low score) read as a single
// surgical override.
func buildCritiqueResult(goal, hier, craft, fn, orig int, overall float64) CritiqueResult {
	return CritiqueResult{
		OverallScore: overall,
		Dimensions: map[CritiqueDimension]CritiqueDimensionScore{
			CritiqueGoalFit:       {Score: goal},
			CritiqueHierarchy:     {Score: hier},
			CritiqueCraft:         {Score: craft},
			CritiqueFunctionality: {Score: fn},
			CritiqueOriginality:   {Score: orig},
		},
	}
}
