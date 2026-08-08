package workagent

// critique_gate.go — M3 critique sub-agent result handling
// (Sprint-B). Pure-logic phase 1: schema, parser, threshold
// evaluator, redo-prompt formatter. The actual sub-agent SDK
// invocation lands in a follow-up commit; this file owns the
// data layer the dispatcher will consume.
//
// Why split: the SDK call path requires account-pool wiring +
// Haiku model selection + token budgeting, all of which need
// integration tests. Schema/parser/threshold logic is pure CPU
// and rides into a unit-test-only commit so the dispatcher
// commit's review surface focuses on the SDK orchestration.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// CritiqueDimension is one axis of the 5-dimensional rubric the
// critique skill emits. Names match SKILL.md's JSON keys
// (goal_fit / hierarchy / craft / functionality / originality)
// so a future schema audit can grep both files for the same
// identifiers.
type CritiqueDimension string

const (
	CritiqueGoalFit       CritiqueDimension = "goal_fit"
	CritiqueHierarchy     CritiqueDimension = "hierarchy"
	CritiqueCraft         CritiqueDimension = "craft"
	CritiqueFunctionality CritiqueDimension = "functionality"
	CritiqueOriginality   CritiqueDimension = "originality"
)

// CritiqueDimensions returns the canonical ordering — used in the
// redo prompt formatter so the worst-scoring dimension surfaces
// first regardless of how the sub-agent ordered its JSON keys.
// Order matches SKILL.md §"5 维度评审".
func CritiqueDimensions() []CritiqueDimension {
	return []CritiqueDimension{
		CritiqueGoalFit,
		CritiqueHierarchy,
		CritiqueCraft,
		CritiqueFunctionality,
		CritiqueOriginality,
	}
}

// CritiqueDimensionScore wraps one axis's score + the model's
// reasoning + concrete fixes. Empty Notes / Fixes are tolerated
// (the parser doesn't reject the whole result on a partially-
// filled axis) but the threshold evaluator only trusts Score.
type CritiqueDimensionScore struct {
	Score int      `json:"score"`
	Notes string   `json:"notes"`
	Fixes []string `json:"fixes"`
}

// CritiqueResult is the parsed shape of the critique sub-agent's
// JSON response. Mirrors SKILL.md §"输出格式" exactly so the
// sub-agent's prompt and our parser stay in lockstep.
type CritiqueResult struct {
	OverallScore float64                                      `json:"overall_score"`
	Verdict      string                                       `json:"verdict"`
	Dimensions   map[CritiqueDimension]CritiqueDimensionScore `json:"dimensions"`
	TopFixes     []string                                     `json:"top_fixes"`
}

// CritiqueDecision mirrors the GateDecision tri-state on the
// pre-emit checklist gate. Same wire shape on the done event so
// the frontend can render both verdicts with shared logic.
type CritiqueDecision int

const (
	// CritiquePass — every dimension ≥ blockThreshold AND overall
	// score ≥ warnThreshold. Caller emits + ends the turn.
	CritiquePass CritiqueDecision = iota

	// CritiqueWarn — every dimension ≥ blockThreshold but overall
	// score < warnThreshold (i.e. all axes are technically OK
	// but the average is mediocre). Caller emits with a warning
	// banner; the frontend lets the user decide whether to redo.
	CritiqueWarn

	// CritiqueBlock — at least one dimension < blockThreshold.
	// Caller MUST redo (when redo flag enabled) or surface the
	// hard fixes (when redo disabled). This is the M3 critical
	// signal.
	CritiqueBlock
)

func (d CritiqueDecision) String() string {
	switch d {
	case CritiquePass:
		return "pass"
	case CritiqueWarn:
		return "warn"
	case CritiqueBlock:
		return "block"
	default:
		return fmt.Sprintf("unknown(%d)", int(d))
	}
}

// CritiqueThresholds hold the cut-off scores that map a parsed
// result to a decision. Defaults follow the design doc (§M3 5.7):
//
//   - any axis < BlockBelow → Block (hard redo)
//   - every axis ≥ BlockBelow but overall < WarnBelow → Warn
//   - else → Pass
//
// Per-skill overrides are a future iteration (e.g. brand-poster
// caring more about craft than originality); the W1 ramp ships a
// single global setting per the design's "start with fewer knobs"
// posture.
type CritiqueThresholds struct {
	BlockBelow int // dimension score < this → Block decision
	WarnBelow  int // overall score < this → Warn (when no Block axis)
}

// DefaultCritiqueThresholds are the W1 defaults. Block at 7 means
// "any 6 or below triggers a redo" — aggressive enough to catch
// cliché output, lenient enough that a 7-7-7-7-7 spread (avg 7)
// passes. Warn at 8 reflects "average should be at least 8/10 to
// declare done."
func DefaultCritiqueThresholds() CritiqueThresholds {
	return CritiqueThresholds{BlockBelow: 7, WarnBelow: 8}
}

// EvaluateCritique applies the threshold rules to a parsed
// CritiqueResult. Empty/zero results (e.g. parser failure
// degraded to a default-zero struct) decay to Block — the caller
// wants "we couldn't grade this" to behave the same as "we graded
// it badly," not silently to Pass.
//
// We tolerate missing dimensions (a sub-agent that returns 4 of
// 5 axes) by treating the missing one as a 0 — also Block. The
// alternative (skip missing axes) would let a partially-rendered
// JSON response score Pass on a single axis.
func EvaluateCritique(result CritiqueResult, thresholds CritiqueThresholds) CritiqueDecision {
	// All five canonical dimensions must clear BlockBelow. A missing
	// dimension reads as Score=0 via the zero-value struct, so the
	// loop catches both undercut scores and absent axes.
	for _, dim := range CritiqueDimensions() {
		score := result.Dimensions[dim].Score
		if score < thresholds.BlockBelow {
			return CritiqueBlock
		}
	}
	// Overall is the model's averaged score (or zero if missing).
	// Treat unset overall the same as a low overall: Warn rather
	// than Pass, since we can't claim quality without a rating.
	if result.OverallScore < float64(thresholds.WarnBelow) {
		return CritiqueWarn
	}
	return CritiquePass
}

// ParseCritiqueResponse extracts the structured critique payload
// from a raw model response. Robust to three real-world failure
// modes from the sub-agent's text generation:
//
//  1. Prose framing: "Here's the review:\n```json\n{...}\n```"
//  2. Bare JSON: just the object, no fences
//  3. Score range drift: model emits 0.85 instead of 8.5, or
//     "score": "8" (string instead of int)
//
// Returns the zero-value CritiqueResult + error on unrecoverable
// input. Caller treats the zero value as Block (per
// EvaluateCritique's "missing axes are 0" contract) so a parse
// error on the redo path means "we couldn't grade → must redo,"
// matching the user's intuition.
func ParseCritiqueResponse(raw string) (CritiqueResult, error) {
	var empty CritiqueResult
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return empty, fmt.Errorf("empty critique response")
	}

	// Extract the first balanced { ... } object. Fence-stripping
	// + brace-balance is more permissive than a strict regex
	// because the model occasionally wraps with ```json or just
	// ``` (no language tag). We don't try to handle multiple
	// JSON objects — the contract is one critique per turn.
	jsonText := extractFirstJSONObject(trimmed)
	if jsonText == "" {
		return empty, fmt.Errorf("no JSON object found in critique response")
	}

	// Two-pass decode: first into a permissive intermediate that
	// accepts string|number for scores (some Haiku responses ship
	// "score": "8"), then normalise to typed fields.
	var loose looseCritiqueResponse
	if err := json.Unmarshal([]byte(jsonText), &loose); err != nil {
		return empty, fmt.Errorf("decode critique JSON: %w", err)
	}

	out := CritiqueResult{
		OverallScore: loose.OverallScore.toFloat(),
		Verdict:      strings.TrimSpace(loose.Verdict),
		TopFixes:     stripEmptyStrings(loose.TopFixes),
		Dimensions:   make(map[CritiqueDimension]CritiqueDimensionScore, 5),
	}

	for k, v := range loose.Dimensions {
		dim := CritiqueDimension(k)
		out.Dimensions[dim] = CritiqueDimensionScore{
			Score: clampScore(v.Score.toFloat()),
			Notes: strings.TrimSpace(v.Notes),
			Fixes: stripEmptyStrings(v.Fixes),
		}
	}
	return out, nil
}

// FormatCritiqueRedoPrompt builds the hidden-user-message body
// the redo loop injects when EvaluateCritique returns Block.
// Mirrors the pre-emit gate's FormatBlockFindings shape so the
// frontend / downstream observability has one prompt format to
// recognise. Top fixes lead, then per-dimension fixes for axes
// that scored below threshold.
func FormatCritiqueRedoPrompt(result CritiqueResult, thresholds CritiqueThresholds) string {
	var b strings.Builder
	b.WriteString("<critique-findings priority=\"P0\">\n")

	// Highest-leverage fixes first, capped at 5 to keep token
	// cost predictable on redo. The sub-agent already de-dupes
	// + ranks these in top_fixes per SKILL.md §"输出格式".
	if len(result.TopFixes) > 0 {
		b.WriteString("Top fixes:\n")
		count := 0
		for _, f := range result.TopFixes {
			if f == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", f)
			count++
			if count >= 5 {
				break
			}
		}
	}

	// Per-axis surface for axes that triggered the Block. Iterating
	// CritiqueDimensions() (canonical order) keeps the output
	// deterministic across runs — important for prompt-cache hits
	// on the redo turn.
	wroteAxis := false
	for _, dim := range CritiqueDimensions() {
		d := result.Dimensions[dim]
		if d.Score >= thresholds.BlockBelow {
			continue
		}
		if !wroteAxis {
			b.WriteString("\nUndercut axes:\n")
			wroteAxis = true
		}
		fmt.Fprintf(&b, "- [%s = %d] %s\n", dim, d.Score, strings.TrimSpace(d.Notes))
		for i, fix := range d.Fixes {
			if i >= 3 {
				b.WriteString("  ...\n")
				break
			}
			fmt.Fprintf(&b, "  · %s\n", fix)
		}
	}

	b.WriteString("</critique-findings>\n\n")
	b.WriteString("Please regenerate addressing the above. Reference the active design-system / brand-spec / question-form context for valid values.")
	return b.String()
}

// extractFirstJSONObject returns the substring spanning the first
// balanced `{ ... }` block in s, or "" when no balanced object
// exists. We strip surrounding ``` fences first so the brace-
// counter doesn't have to know about Markdown.
//
// String-aware enough to survive `{"k": "}"}` (a brace inside a
// string literal won't fool the counter). Doesn't try to handle
// every JSON edge case — corrupt input falls through to the
// json.Unmarshal step which produces a clear error.
func extractFirstJSONObject(s string) string {
	// Strip ```json / ``` fences. `strings.ReplaceAll` handles the
	// closing fence too; if the model emits a fence-only-on-open
	// pattern (rare) the brace counter still finds the object.
	s = strings.ReplaceAll(s, "```json", "")
	s = strings.ReplaceAll(s, "```JSON", "")
	s = strings.ReplaceAll(s, "```", "")

	depth := 0
	start := -1
	inString := false
	escape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if c == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 && start >= 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// looseCritiqueResponse is the permissive intermediate the
// parser unmarshals into before normalising. The trick is
// looseScore: json.RawMessage so we can attempt int → float →
// string-numeric in order, instead of failing the whole decode
// when the model returns a string score.
type looseCritiqueResponse struct {
	OverallScore looseScore                     `json:"overall_score"`
	Verdict      string                         `json:"verdict"`
	Dimensions   map[string]looseDimensionScore `json:"dimensions"`
	TopFixes     []string                       `json:"top_fixes"`
}

type looseDimensionScore struct {
	Score looseScore `json:"score"`
	Notes string     `json:"notes"`
	Fixes []string   `json:"fixes"`
}

// looseScore tolerates int / float / numeric-string inputs from
// the model. Non-numeric values resolve to 0 (caught by
// EvaluateCritique as a Block-triggering missing axis).
type looseScore struct {
	raw json.RawMessage
}

func (l *looseScore) UnmarshalJSON(data []byte) error {
	l.raw = append(l.raw[:0], data...)
	return nil
}

func (l looseScore) toFloat() float64 {
	if len(l.raw) == 0 {
		return 0
	}
	// Strip surrounding quotes if the model sent "8" rather
	// than 8. The trimmed buffer is then numeric (or garbage).
	body := strings.TrimSpace(string(l.raw))
	body = strings.TrimPrefix(body, `"`)
	body = strings.TrimSuffix(body, `"`)
	body = strings.TrimSpace(body)

	var f float64
	if _, err := fmt.Sscanf(body, "%f", &f); err != nil {
		return 0
	}
	return f
}

// clampScore takes the float-normalised score and rounds it to
// the SKILL.md-mandated 1-10 integer range. Out-of-band values
// (12, -3, 0.85) are clamped rather than zeroed so a "score":
// 0.85 (model decimal-shifted by accident) reads as 1, not 0.
func clampScore(f float64) int {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	rounded := int(math.Round(f))
	if rounded < 0 {
		return 0
	}
	if rounded > 10 {
		return 10
	}
	// Decimal-shift recovery: 0.85 → 1, 0.4 → 0. We don't try
	// to "rescue" 0.5..1 to 5..10 because that would silently
	// mis-grade real low scores.
	return rounded
}

// AttachCritiqueGateResult wraps the parent agent turn's result
// payload with the critique verdict so the frontend can render
// it alongside the pre-emit gate's checklist_gate field. Mirrors
// AttachChecklistGateResult's shape so the frontend's done-event
// handler can pick both up via the same defensive parser.
//
// Wire shape (added to result.data):
//
//	"critique_gate": {
//	  "decision": "pass" | "warn" | "block",
//	  "skill":    string,           // parent skill, optional
//	  "overall_score": float,
//	  "verdict":  string,
//	  "dimensions": [
//	     { "id": "goal_fit",      "score": int, "notes": string, "fixes": [string] },
//	     { "id": "hierarchy",     "score": int, "notes": string, "fixes": [string] },
//	     { "id": "craft",         "score": int, "notes": string, "fixes": [string] },
//	     { "id": "functionality", "score": int, "notes": string, "fixes": [string] },
//	     { "id": "originality",   "score": int, "notes": string, "fixes": [string] }
//	  ],
//	  "top_fixes": [string],
//	  "redo_prompt": string         // populated only on Block
//	}
//
// NOTE: dimensions is an ARRAY, not a map — iterated in canonical
// CritiqueDimensions() order (see formatCritiqueDimensions). The
// array shape is the canonical Desktop wire contract and gives deterministic
// per-dimension render order regardless of Go map iteration. The previous docstring
// incorrectly showed a map shape; corrected 2026-05-12.
//
// Defensive against null payload (Unmarshal of "null" returns nil
// map; subsequent assignment panics) — same null-guard as
// AttachChecklistGateResult.
func AttachCritiqueGateResult(result json.RawMessage, critique CritiqueResult, decision CritiqueDecision, skill string) json.RawMessage {
	var payload map[string]interface{}
	if len(result) == 0 {
		payload = map[string]interface{}{}
	} else if err := json.Unmarshal(result, &payload); err != nil || payload == nil {
		payload = map[string]interface{}{}
	}

	gate := map[string]interface{}{
		"decision":      decision.String(),
		"overall_score": critique.OverallScore,
		"verdict":       critique.Verdict,
		"dimensions":    formatCritiqueDimensions(critique.Dimensions),
		"top_fixes":     critique.TopFixes,
	}
	if skill != "" {
		gate["skill"] = skill
	}
	if decision == CritiqueBlock {
		gate["redo_prompt"] = FormatCritiqueRedoPrompt(critique, DefaultCritiqueThresholds())
	}
	payload["critique_gate"] = gate

	out, err := json.Marshal(payload)
	if err != nil {
		// Re-marshal failure is essentially impossible (we built the
		// map ourselves); fall back to the original payload so the
		// turn still emits cleanly.
		return result
	}
	return out
}

// formatCritiqueDimensions reduces the typed map to the wire shape
// the frontend consumes. Preserves the canonical ordering by walking
// CritiqueDimensions() rather than ranging the map directly — the
// caller wants a deterministic JSON for prompt-cache hits + diff-
// friendly logs.
func formatCritiqueDimensions(dims map[CritiqueDimension]CritiqueDimensionScore) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(CritiqueDimensions()))
	for _, dim := range CritiqueDimensions() {
		d := dims[dim]
		out = append(out, map[string]interface{}{
			"id":    string(dim),
			"score": d.Score,
			"notes": d.Notes,
			"fixes": d.Fixes,
		})
	}
	return out
}

// stripEmptyStrings filters whitespace-only entries from a slice.
// The model occasionally emits `"fixes": ["", "fix-1", "  "]`
// when it's reasoning about which axis to populate.
func stripEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
