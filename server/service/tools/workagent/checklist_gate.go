// Package workagent — checklist_gate.go.
//
// Pre-emit / post-emit checklist gate (M5 + DS-4). Two roles:
//
//  1. Pre-flight digest — before the SDK call, build a small XML
//     block summarizing the skill's P0/P1 rules. Injected into
//     the system prompt's SystemAdditions layer (PR-2). The model
//     reads the constraints BEFORE generating, not after.
//
//  2. Post-emit gate — after the SDK returns end_turn, dispatch
//     every checklist item's detector, aggregate results, return
//     a Decision the API handler reads:
//     - Pass:    proceed to emit
//     - Warn:    emit + frontend shows P1 override card
//     - Block:   inject hidden user message with findings,
//     redo (caller decides max retries)
package workagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"server/globals"
	"server/service/tools/workagent/detectors"
)

// GateDecision is the gate's verdict on an emitted artifact.
type GateDecision int

const (
	// GatePass — every detector returned pass / skipped. Caller
	// proceeds to emit.
	GatePass GateDecision = iota

	// GateWarn — at least one P1 detector failed; no P0 fail. Caller
	// emits but surfaces the P1 issues to the user (override card
	// in the frontend). P2 fails are absorbed into trace logs without
	// affecting this decision.
	GateWarn

	// GateBlock — at least one P0 detector failed. Caller MUST NOT
	// emit; if redo budget remains, inject the findings as a hidden
	// user message and re-run the SDK turn. Otherwise emit with
	// warning ("不达标但已重试 N 次，仍输出").
	GateBlock
)

// String — human-readable for log lines.
func (d GateDecision) String() string {
	switch d {
	case GatePass:
		return "pass"
	case GateWarn:
		return "warn"
	case GateBlock:
		return "block"
	default:
		return "unknown"
	}
}

// GateResult is the full report from RunGate. Carries the decision
// + per-item evidence the caller can surface to the user (P1 override
// card / P0 redo prompt) and to telemetry (which detectors fired).
type GateResult struct {
	Decision GateDecision
	P0Fails  []GateItemResult // empty if no P0 fired
	P1Fails  []GateItemResult // empty if no P1 fired
	P2Notes  []GateItemResult // tracing-only, never affects Decision
	Skipped  []GateItemResult // for trace
}

// GateItemResult pairs the checklist item with the detector verdict.
type GateItemResult struct {
	Item   ChecklistItem
	Result detectors.Result
}

// PreflightDigest returns the XML block injected into the system
// prompt's SystemAdditions layer (PR-2 SystemAdditionsComposer.
// ChecklistDigest). Format:
//
//	<skill-checklist>
//	<p0>
//	- <title>: <description>
//	- ...
//	</p0>
//	<p1>...</p1>
//	</skill-checklist>
//
// We surface titles + descriptions, NOT the detector machinery —
// the model doesn't need to know what regex runs against its output;
// it needs to know what the constraints are. P2 items are intentionally
// omitted from the digest (nice-to-have, not a binding rule the model
// should burn context on).
//
// Empty checklist → empty string. SystemAdditionsComposer skips
// empty fields so this just vanishes from the prompt.
func PreflightDigest(c *Checklist) string {
	if c.IsEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("<skill-checklist>\n")
	if len(c.P0) > 0 {
		b.WriteString("<p0>\n")
		for _, item := range c.P0 {
			fmt.Fprintf(&b, "- %s: %s\n", item.Title, item.Description)
		}
		b.WriteString("</p0>\n")
	}
	if len(c.P1) > 0 {
		b.WriteString("<p1>\n")
		for _, item := range c.P1 {
			fmt.Fprintf(&b, "- %s: %s\n", item.Title, item.Description)
		}
		b.WriteString("</p1>\n")
	}
	b.WriteString("</skill-checklist>")
	return b.String()
}

// RunGate runs every detector named in the checklist against the
// supplied artifact + context. Returns a GateResult with the
// aggregated decision.
//
// Detector failures (registry miss, panic) degrade to "skipped" with
// a reason — the gate must NOT crash the turn. Same fail-safe contract
// as detectors themselves.
//
// The detector registry is the package-level
// detectors.Default() — production callers don't need to wire one
// in. Tests can override by passing a custom registry via RunGateWith.
func RunGate(ctx context.Context, c *Checklist, in detectors.Input) GateResult {
	return RunGateWith(ctx, c, in, detectors.Default())
}

// RunGateWith is the same as RunGate but accepts an explicit detector
// registry. Used by tests to swap a stub registry; production code
// uses RunGate.
func RunGateWith(ctx context.Context, c *Checklist, in detectors.Input, reg *detectors.Registry) GateResult {
	if c.IsEmpty() {
		return GateResult{Decision: GatePass}
	}
	if reg == nil {
		reg = detectors.Default()
	}

	res := GateResult{Decision: GatePass}

	// Run P0 first — a single P0 fail will dominate the decision,
	// no need to be cute about parallelism for ~5 cheap regex
	// detectors per checklist.
	for _, item := range c.P0 {
		r := dispatchOne(ctx, reg, item, in)
		giResult := GateItemResult{Item: item, Result: r}
		switch r.Status {
		case detectors.StatusFail:
			res.P0Fails = append(res.P0Fails, giResult)
			res.Decision = GateBlock
		case detectors.StatusSkipped:
			res.Skipped = append(res.Skipped, giResult)
		}
	}

	for _, item := range c.P1 {
		r := dispatchOne(ctx, reg, item, in)
		giResult := GateItemResult{Item: item, Result: r}
		switch r.Status {
		case detectors.StatusFail:
			res.P1Fails = append(res.P1Fails, giResult)
			// P1 only escalates to Warn if P0 didn't already fire.
			if res.Decision == GatePass {
				res.Decision = GateWarn
			}
		case detectors.StatusSkipped:
			res.Skipped = append(res.Skipped, giResult)
		}
	}

	for _, item := range c.P2 {
		r := dispatchOne(ctx, reg, item, in)
		giResult := GateItemResult{Item: item, Result: r}
		// P2 never affects Decision; just record for trace.
		res.P2Notes = append(res.P2Notes, giResult)
		_ = giResult.Result.Status // silence linter on unused branch
	}

	return res
}

// dispatchOne resolves the detector by name and runs it. Missing
// detectors and panics degrade to Skipped — the alternative
// (raise / error out) would crash the gate path, blocking emit
// for a misconfigured checklist; that's worse than letting a single
// rule lapse.
func dispatchOne(ctx context.Context, reg *detectors.Registry, item ChecklistItem, in detectors.Input) (result detectors.Result) {
	defer func() {
		if r := recover(); r != nil {
			globals.Warn(fmt.Sprintf("[Checklist gate] detector %q panicked: %v", item.Detector, r))
			result = detectors.Skipped(fmt.Sprintf("detector panicked: %v", r))
		}
	}()

	d, ok := reg.Get(item.Detector)
	if !ok {
		// Authoring error — checklist references a detector name
		// that's not in the registry. Log and skip rather than
		// blocking; the operator gets a clear signal in the warn
		// log and the turn proceeds.
		globals.Warn(fmt.Sprintf("[Checklist gate] detector %q not registered (referenced by %s/%s)", item.Detector, in.SkillName, item.ID))
		return detectors.Skipped(fmt.Sprintf("detector %q not registered", item.Detector))
	}

	r, err := d.Run(ctx, in)
	if err != nil {
		// Detector error — also skipped (not failed). Detectors
		// that need to assert "this is broken" should return Fail
		// with issues; an error means "I couldn't determine".
		globals.Warn(fmt.Sprintf("[Checklist gate] detector %q errored: %v", item.Detector, err))
		return detectors.Skipped(fmt.Sprintf("detector errored: %v", err))
	}
	return r
}

// AttachChecklistGateResult enriches the done event's result payload
// with a `checklist_gate` field carrying the M5 / DS-4 gate verdict.
// Same shape pattern as AttachValidationResultsToResult — a JSON
// merge that survives result-frame variation (object vs string
// vs null) by leaving the original untouched on parse failure.
//
// Field schema (consumed by frontend P1 override card / dashboards):
//
//	"checklist_gate": {
//	  "skill": "ppt",
//	  "decision": "pass" | "warn" | "block",
//	  "p0_fails": [ { "id":"P0-1", "title":"honest_data", "issues":[...] }, ... ],
//	  "p1_fails": [ ... ],
//	  "redo_prompt": "<lint-findings ...>" | ""    // only when decision=block
//	}
//
// PR-9b/2a ships read-only — frontend can surface the data, but
// the SDK turn doesn't auto-redo. PR-9b/2b adds the redo loop and
// will reuse this same result-shape contract.
func AttachChecklistGateResult(result json.RawMessage, gateResult GateResult) json.RawMessage {
	payload := map[string]interface{}{}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &payload); err != nil {
			// Result isn't a JSON object (string error / array).
			// Wrapping it would clobber the original SDK payload
			// — leave it alone, log, and move on.
			globals.Warn(fmt.Sprintf("[Checklist gate] result not a JSON object, skipping enrichment: %v", err))
			return result
		}
		// json.Unmarshal of `null` SUCCEEDS but sets the target
		// map to nil. Detect and bail out — wrapping null as
		// {"checklist_gate": ...} would lose the upstream "null"
		// signal the SDK might be conveying.
		if payload == nil {
			return result
		}
	}

	gate := map[string]interface{}{
		"skill":    gateResult.skillNameForResult(),
		"decision": gateResult.Decision.String(),
	}
	if len(gateResult.P0Fails) > 0 {
		gate["p0_fails"] = formatGateItemResults(gateResult.P0Fails)
	}
	if len(gateResult.P1Fails) > 0 {
		gate["p1_fails"] = formatGateItemResults(gateResult.P1Fails)
	}
	if gateResult.Decision == GateBlock {
		gate["redo_prompt"] = FormatBlockFindings(gateResult.P0Fails)
	}

	payload["checklist_gate"] = gate

	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return data
}

// skillNameForResult returns the GateResult's first available skill
// hint. The current GateResult shape doesn't carry SkillName at the
// top level (it's per-item via ChecklistItem.ID); we surface the
// first item's parent or fall back to empty so the dashboard groups
// gate fires consistently. Sprint-B addition.
func (g *GateResult) skillNameForResult() string {
	for _, item := range g.P0Fails {
		if item.Item.ID != "" {
			return strings.SplitN(item.Item.ID, "-", 2)[0]
		}
	}
	return ""
}

// formatGateItemResults reduces the runtime GateItemResult shape to
// the JSON-emit subset — id + title + issues. Trace fields stay
// server-side (debugging context, not user-facing) to keep the
// done event payload trim.
func formatGateItemResults(items []GateItemResult) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]interface{}{
			"id":     it.Item.ID,
			"title":  it.Item.Title,
			"issues": it.Result.Issues,
		})
	}
	return out
}

// FormatBlockFindings turns the P0 fail results into the hidden user
// message text the redo loop injects. Format mirrors the design
// document's example:
//
//	<lint-findings priority="P0">
//	- <issue>
//	- <issue>
//	...
//	</lint-findings>
//
//	Please regenerate fixing these N issues.
//
// The message is plain text injected as a synthesized user turn —
// the SDK's RetryWithSession path picks it up like any normal user
// follow-up, and the model regenerates with the constraints in mind.
func FormatBlockFindings(p0Fails []GateItemResult) string {
	if len(p0Fails) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<lint-findings priority=\"P0\">\n")
	count := 0
	for _, f := range p0Fails {
		for _, issue := range f.Result.Issues {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Item.Title, issue)
			count++
			if count >= 20 {
				b.WriteString("- ... (truncated; resolve the listed items first)\n")
				break
			}
		}
		if count >= 20 {
			break
		}
	}
	b.WriteString("</lint-findings>\n\n")
	fmt.Fprintf(&b, "Please regenerate fixing these %d issues. Reference the active design-system / brand-spec / question-form context for valid values.", count)
	return b.String()
}
