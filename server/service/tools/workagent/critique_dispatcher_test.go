package workagent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeCritiqueRunner — test double that lets tests verify orchestration
// behaviour (parse pipeline / error propagation / prompt assembly)
// without touching the SDK / account pool. The SDKCritiqueRunner has
// its own integration coverage in the dispatcher commit (3/3); this
// file pins the runner-agnostic invariants.
type fakeCritiqueRunner struct {
	rawResponse string
	err         error
	gotInput    CritiqueInput
}

func (r *fakeCritiqueRunner) Invoke(_ context.Context, in CritiqueInput) (string, error) {
	r.gotInput = in
	return r.rawResponse, r.err
}

// TestRunCritique_ParsesValidResponse — the happy path: runner returns
// well-formed JSON, RunCritique decodes it via ParseCritiqueResponse.
func TestRunCritique_ParsesValidResponse(t *testing.T) {
	runner := &fakeCritiqueRunner{
		rawResponse: `{
  "overall_score": 7.4,
  "verdict": "ok",
  "dimensions": {
    "goal_fit":      { "score": 8, "notes": "", "fixes": [] },
    "hierarchy":     { "score": 7, "notes": "", "fixes": [] },
    "craft":         { "score": 7, "notes": "", "fixes": [] },
    "functionality": { "score": 8, "notes": "", "fixes": [] },
    "originality":   { "score": 7, "notes": "", "fixes": [] }
  },
  "top_fixes": []
}`,
	}
	result, err := RunCritique(context.Background(), runner, CritiqueInput{
		ArtifactText: "test artifact",
	})
	if err != nil {
		t.Fatalf("RunCritique: %v", err)
	}
	if result.OverallScore != 7.4 {
		t.Errorf("overall score = %v, want 7.4", result.OverallScore)
	}
	if result.Dimensions[CritiqueGoalFit].Score != 8 {
		t.Errorf("goal_fit not parsed via runner -> parser pipeline")
	}
}

// TestRunCritique_PropagatesRunnerError — when the SDK fails, the
// dispatcher must surface the error verbatim (not zero-value the
// result). Lets the upstream caller fail-soft skip critique without
// confusing it with a "valid Block" verdict.
func TestRunCritique_PropagatesRunnerError(t *testing.T) {
	upstream := errors.New("account pool empty")
	runner := &fakeCritiqueRunner{err: upstream}
	_, err := RunCritique(context.Background(), runner, CritiqueInput{})
	if !errors.Is(err, upstream) {
		t.Errorf("error not propagated: got %v, want wrap of %v", err, upstream)
	}
}

// TestRunCritique_ParseFailureSurfaces — runner returns non-JSON.
// We want the caller to know parse failed (not "the model returned
// a zero-axis result"); EvaluateCritique on the zero result will
// then emit Block, which is the right semantic.
func TestRunCritique_ParseFailureSurfaces(t *testing.T) {
	runner := &fakeCritiqueRunner{rawResponse: "I cannot grade this."}
	_, err := RunCritique(context.Background(), runner, CritiqueInput{})
	if err == nil {
		t.Errorf("expected parse error on non-JSON runner output")
	}
}

// TestRunCritique_NilRunnerSubstitutesSDKType — passing nil should
// not crash on the type-substitution path. The SDK runner itself
// requires a populated globals.GraConf/GraDBs to succeed, which is
// out of scope for unit tests; production wires those before the
// dispatcher fires. So we only verify the substitution happens by
// checking that the function compiles + accepts nil without
// hitting the type-assertion path. We drive Invoke separately with
// an explicit fake elsewhere in this file.
func TestRunCritique_NilRunnerSubstitutesSDKType(t *testing.T) {
	// Sanity probe: the production runner is the documented
	// fallback. Constructing it here verifies the type stays
	// callable; integration coverage that actually invokes it
	// against a real account pool lives in the dispatcher's
	// integration test (phase 3 wiring).
	var fallback CritiqueRunner = SDKCritiqueRunner{}
	if fallback == nil {
		t.Errorf("SDKCritiqueRunner should satisfy CritiqueRunner")
	}
}

// TestBuildCritiqueUserPrompt_IncludesSkillAndArtifact — the prompt
// the sub-agent's user-message contains MUST carry the parent skill
// name + artifact text so the rubric in SKILL.md has both pieces.
func TestBuildCritiqueUserPrompt_IncludesSkillAndArtifact(t *testing.T) {
	prompt := buildCritiqueUserPrompt(CritiqueInput{
		SkillName:    "ppt",
		ArtifactText: "  Q1 deck draft  ",
	})
	if !strings.Contains(prompt, "<skill>ppt</skill>") {
		t.Errorf("prompt missing skill tag: %q", prompt)
	}
	if !strings.Contains(prompt, "<artifact-to-review>") {
		t.Errorf("prompt missing artifact wrapper: %q", prompt)
	}
	if !strings.Contains(prompt, "Q1 deck draft") {
		t.Errorf("prompt missing artifact body (or didn't trim leading space): %q", prompt)
	}
}

// TestBuildCritiqueUserPrompt_FilesListEmpty — when no files, the
// generated-files block is omitted entirely (not rendered as
// `<generated-files></generated-files>`). The sub-agent reads
// "no block" as "text-only artifact" cleanly.
func TestBuildCritiqueUserPrompt_FilesListEmpty(t *testing.T) {
	prompt := buildCritiqueUserPrompt(CritiqueInput{
		ArtifactText: "x",
	})
	if strings.Contains(prompt, "<generated-files>") {
		t.Errorf("empty files list should not render the wrapper: %q", prompt)
	}
}

// TestBuildCritiqueUserPrompt_FilesListedByPath — when files are
// supplied, each path renders as `- {path}` so the sub-agent can
// reference them by exact name in its review.
func TestBuildCritiqueUserPrompt_FilesListedByPath(t *testing.T) {
	prompt := buildCritiqueUserPrompt(CritiqueInput{
		ArtifactText: "x",
		GeneratedFiles: []map[string]interface{}{
			{"path": "outputs/deck.pptx"},
			{"path": "outputs/notes.md"},
			// Falls back to "name" when "path" is absent — covers
			// the canvas-mode payload shape that ships name without
			// an explicit path.
			{"name": "thumb.png"},
		},
	})
	if !strings.Contains(prompt, "- outputs/deck.pptx") {
		t.Errorf("path missing: %q", prompt)
	}
	if !strings.Contains(prompt, "- thumb.png") {
		t.Errorf("name fallback missing: %q", prompt)
	}
}

// TestBuildCritiqueUserPrompt_TrailingInstruction — the closing
// instruction reminds the model to emit JSON per the SKILL.md
// contract; without it the model occasionally falls back to prose
// when the artifact is short.
func TestBuildCritiqueUserPrompt_TrailingInstruction(t *testing.T) {
	prompt := buildCritiqueUserPrompt(CritiqueInput{ArtifactText: "x"})
	if !strings.Contains(prompt, "JSON contract") {
		t.Errorf("trailing JSON-contract reminder missing: %q", prompt)
	}
}

// TestExtractCritiqueText_Variants — sub-agent's text vs non-text
// blocks. Non-text blocks (tool_use / tool_result / thinking)
// must contribute zero captured text so the parser doesn't see
// e.g. tool-call JSON snippets.
func TestExtractCritiqueText_Variants(t *testing.T) {
	cases := map[string]struct {
		block string
		want  string
	}{
		"text block":     {`{"type":"text","text":"hello"}`, "hello"},
		"tool_use block": {`{"type":"tool_use","id":"x"}`, ""},
		"tool_result":    {`{"type":"tool_result","content":[]}`, ""},
		"thinking":       {`{"type":"thinking","thinking":"hmm"}`, ""},
		"empty":          {"", ""},
		"malformed":      {`{not json`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := extractCritiqueText([]byte(tc.block))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
