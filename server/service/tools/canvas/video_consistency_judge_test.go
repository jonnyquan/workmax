// video_consistency_judge_test.go — pure-function tests for the
// pre-render consistency judge (Task #9).
//
// The DB-coupled path + the actual LLM call aren't exercised here
// (they need a live llm client which production tests don't run);
// instead we pin:
//
//   1. parseConsistencyVerdict — every shape the LLM might emit,
//      including markdown fences, string "true", and missing
//      fields. The parser is the security perimeter between the
//      LLM's loose output and the rest of the system.
//   2. composeConsistencyUserMessage — formatting contract so a
//      regression doesn't accidentally swap "prior" and "new"
//      sections or drop the negative prompt.
//   3. dedupAndTrim — defensive cleanup on the mentions list.
//   4. BuildVideoConsistencyJudgment guard branches — empty
//      prompt, nil db, zero uid, zero projectID all short-circuit
//      to a green-light verdict without any LLM call.

package canvas

import (
	"context"
	"strings"
	"testing"
)

func TestParseConsistencyVerdict_HappyPath(t *testing.T) {
	// The canonical LLM response: clean JSON, all fields present.
	got, err := parseConsistencyVerdict(`{
		"ok": false,
		"summary": "character mismatch",
		"warnings": [
			{"kind": "character", "detail": "prior shots show a young woman, new prompt says 'young man'"}
		]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.OK {
		t.Error("OK should be false")
	}
	if got.Summary != "character mismatch" {
		t.Errorf("Summary = %q", got.Summary)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Kind != "character" {
		t.Errorf("Warnings = %+v", got.Warnings)
	}
}

func TestParseConsistencyVerdict_StripsMarkdownFence(t *testing.T) {
	// LLMs sometimes wrap JSON in ```json …  ``` even when asked
	// for raw output. The parser strips one outer fence so the
	// inner JSON parses cleanly.
	got, err := parseConsistencyVerdict("```json\n{\"ok\": true}\n```")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.OK {
		t.Error("OK should be true")
	}
}

func TestParseConsistencyVerdict_TolerantOfStringOK(t *testing.T) {
	// Some models return "ok": "true" (stringified bool). The
	// loose-decode fallback coerces it to a proper bool so the
	// verdict is still usable.
	got, err := parseConsistencyVerdict(`{"ok": "false", "warnings": [{"kind":"x","detail":"y"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.OK {
		t.Error("string \"false\" should coerce to OK=false")
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Detail != "y" {
		t.Errorf("Warnings = %+v", got.Warnings)
	}
}

func TestParseConsistencyVerdict_DefendsAgainstUselessOKFalse(t *testing.T) {
	// "ok=false, warnings=[]" is a meaningless verdict — the LLM
	// flagged a concern but didn't articulate it. Defensively
	// upgrade to ok=true rather than surfacing an empty warning
	// modal to the user.
	got, err := parseConsistencyVerdict(`{"ok": false, "warnings": []}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.OK {
		t.Error("OK=false with empty warnings must collapse to OK=true")
	}
}

func TestParseConsistencyVerdict_DefendsAgainstUselessOKFalse_Loose(t *testing.T) {
	// Same defense, but through the loose-decode branch. Triggered
	// by a string-bool ok.
	got, err := parseConsistencyVerdict(`{"ok": "false"}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.OK {
		t.Error("loose-decode: OK=false with empty warnings must collapse to OK=true")
	}
}

func TestParseConsistencyVerdict_DropsWarningWithEmptyDetail(t *testing.T) {
	// A warning row with no `detail` field is noise — drop it.
	// Surfacing "kind: character, detail: ''" in a modal is worse
	// than nothing.
	got, err := parseConsistencyVerdict(`{"ok": "false", "warnings": [{"kind":"setting"}, {"kind":"x","detail":"real"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Detail != "real" {
		t.Errorf("empty-detail warning should be dropped; got %+v", got.Warnings)
	}
}

func TestParseConsistencyVerdict_RejectsEmpty(t *testing.T) {
	if _, err := parseConsistencyVerdict(""); err == nil {
		t.Error("empty content should error")
	}
	if _, err := parseConsistencyVerdict("   \n  "); err == nil {
		t.Error("whitespace-only should error")
	}
}

func TestParseConsistencyVerdict_RejectsMalformedJSON(t *testing.T) {
	if _, err := parseConsistencyVerdict("not even close to json"); err == nil {
		t.Error("non-JSON content should error")
	}
}

func TestComposeConsistencyUserMessage_StructureIsPredictable(t *testing.T) {
	// Pin the literal sections so a regression that swapped prior
	// vs new (the LLM would judge backwards) surfaces immediately.
	got := composeConsistencyUserMessage(
		"a young woman walks at dawn",
		"blurry",
		[]string{"@character/lin-xia"},
		[]string{"a young woman buys coffee", "a young woman crosses street"},
	)
	if !strings.HasPrefix(got, "NEW SHOT PROMPT:\n") {
		t.Errorf("must start with NEW SHOT PROMPT section, got:\n%s", got)
	}
	if !strings.Contains(got, "NEGATIVE PROMPT (avoid):\nblurry") {
		t.Errorf("must include negative prompt section, got:\n%s", got)
	}
	if !strings.Contains(got, "REFERENCED ENTITIES:\n@character/lin-xia") {
		t.Errorf("must include mentions section, got:\n%s", got)
	}
	if !strings.Contains(got, "PRIOR SHOTS (most recent first):") {
		t.Errorf("must label prior shots ordering, got:\n%s", got)
	}
	// Prior shots are 1-indexed, most-recent-first.
	if !strings.Contains(got, "1. a young woman buys coffee") {
		t.Errorf("first prior shot must be numbered 1, got:\n%s", got)
	}
}

func TestComposeConsistencyUserMessage_OmitsEmptyOptionalSections(t *testing.T) {
	// When there's no negative prompt and no mentions, those
	// sections should disappear entirely — not appear as empty
	// headers (would waste tokens).
	got := composeConsistencyUserMessage(
		"a sunset",
		"",
		nil,
		[]string{"a noon scene"},
	)
	if strings.Contains(got, "NEGATIVE PROMPT") {
		t.Error("empty negative prompt should not render a header")
	}
	if strings.Contains(got, "REFERENCED ENTITIES") {
		t.Error("nil mentions should not render a header")
	}
}

func TestDedupAndTrim(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"a", "b", "a"}, []string{"a", "b"}},
		{[]string{"  a  ", "a"}, []string{"a"}},
		{[]string{"", "  ", "x"}, []string{"x"}},
		{nil, []string{}},
	}
	for _, c := range cases {
		got := dedupAndTrim(c.in)
		if len(got) != len(c.want) {
			t.Errorf("dedupAndTrim(%v) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("dedupAndTrim(%v)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestBuildVideoConsistencyJudgment_GuardsSkipWithoutLLMCall(t *testing.T) {
	// Every guard branch must return OK=true and a SkippedReason
	// without trying to call the LLM (which the test harness has
	// no live client for anyway). Passing nil DB is the proof
	// that the guards held — a leak would panic on WithContext.
	cases := []struct {
		name       string
		uid        uint
		projectID  uint64
		prompt     string
		wantReason string
	}{
		{"empty prompt", 42, 1, "  ", "empty prompt"},
		{"zero uid", 0, 1, "real", "no project context"},
		{"zero project", 42, 0, "real", "no project context"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BuildVideoConsistencyJudgment(
				context.Background(),
				nil, // db — must NOT be reached
				c.uid,
				c.projectID,
				ConsistencyJudgeInput{Prompt: c.prompt},
			)
			if err != nil {
				t.Errorf("guard branch should not error, got %v", err)
			}
			if !got.OK {
				t.Errorf("guard branch should green-light, got OK=false")
			}
			if got.SkippedReason != c.wantReason {
				t.Errorf("SkippedReason = %q, want %q", got.SkippedReason, c.wantReason)
			}
		})
	}
}
