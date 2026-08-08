// Package workagent — question_form_dispatcher.go.
//
// M1 turn-1 short-circuit. Detects the conditions under which the
// runtime should emit a question_form block instead of calling the
// SDK, builds the SSE block payload (resolving i18n keys against the
// user's locale), and exposes a tiny imperative API the API handler
// (agent_api.go) can call between setupTurnEnvironment and the SDK
// invocation.
//
// Dispatch contract:
//
//   - Triggers ONLY when ALL of:
//     1. config.IsQuestionFormEnabledFor(uid) — feature flag
//     2. thread.MessageCount == 0 — first turn
//     3. skill bundle declares QuestionForm.Enabled = true
//     4. Caller-supplied "already answered" check returns false
//     (i.e. no synthetic discovery_form_result yet from NLP
//     pre-scan or URL-based fork — those land in PR-6).
//   - Emits SSE block(question_form) + done(form_emitted), zero SDK
//     tokens, then returns Ok=false to signal "we already
//     responded; caller should release credits and bail".
//   - Tolerant of partial config (skill omits some fields) — we
//     drop malformed questions silently so a skill in mid-authoring
//     doesn't crash the gate. Empty-questions case = Skipped, no
//     emit, fall through to SDK.
package workagent

import (
	"encoding/json"
	"fmt"

	"server/config"
	"server/globals"
	"server/service/tools/workagent/i18n"
	"server/service/tools/workagent/skills"
)

// QuestionFormDispatcher decides whether a turn should be intercepted
// with a question_form short-circuit and, when it should, emits the
// SSE events. Constructed once at package init; receiver methods are
// stateless past construction so concurrent turns can share it.
type QuestionFormDispatcher struct {
	resolver *i18n.Resolver
}

// NewQuestionFormDispatcher returns a dispatcher with the supplied
// locale resolver. Production callers should pass i18n.Default(); tests
// can supply an isolated resolver via i18n.Load().
func NewQuestionFormDispatcher(resolver *i18n.Resolver) *QuestionFormDispatcher {
	return &QuestionFormDispatcher{resolver: resolver}
}

// DefaultQuestionFormDispatcher is the package-level singleton wired
// to i18n.Default(). API handlers take this; tests can swap by
// constructing their own with NewQuestionFormDispatcher.
var DefaultQuestionFormDispatcher = NewQuestionFormDispatcher(i18n.Default())

// DispatchInput bundles the per-turn state the dispatcher reads. Kept
// narrow so the API handler doesn't have to thread a fat context
// object — the four facts here are everything we need.
type DispatchInput struct {
	UID              uint                // current user
	ThreadMessageCnt int                 // chatThread.MessageCount BEFORE this turn
	SkillBundle      *skills.SkillBundle // parsed skill, may carry QuestionForm
	Locale           string              // user's preferred lang ("zh", "en-US", ...)
	// PrefillAnswers carries partial NLP inference matches into the
	// emitted form. These are presentation defaults only; they are
	// not persisted as discovery answers unless the user submits or
	// all required fields were inferred and the caller auto-skips.
	PrefillAnswers map[string]string
	// AlreadyAnswered = true when a prior discovery_form_result row
	// exists for the CURRENT skill (G10/G11, 2026-05-17) OR the
	// caller's NLP pre-scan inferred all required fields. Caller
	// is responsible for the per-skill lookup; the dispatcher does
	// not query the DB. Pre-G11 this gate was paired with
	// ThreadMessageCnt > 0; post-G11 AlreadyAnswered is the single
	// authoritative signal so a skill switch mid-thread can re-fire
	// the form when the new skill has unanswered questions.
	AlreadyAnswered bool
	TempMessageID   string        // SSE message_id minted by API handler
	SendEvent       SendEventFunc // SSE write closure (matches handler's local sendEvent)
}

// SendEventFunc matches the agent_api.go local closure shape
// (workagentModel.AgentSSEEvent → write to SSE writer). Lifted here
// as a typed alias so callers don't need to touch model types
// transitively.
//
// The agent_api.go handler builds its sendEvent inline as a closure
// over the SSE writer + tempMessageID + tracking state. Passing it
// directly avoids duplicating that wiring.
type SendEventFunc = func(eventType string, messageID string, blockIndex int, blockJSON, resultJSON json.RawMessage)

// DispatchResult tells the caller whether to continue or short-circuit.
type DispatchResult struct {
	// Emitted is true when the dispatcher emitted an SSE event
	// stream and the caller should release credits + return.
	Emitted bool

	// Reason is a human-readable trace for the structured log line.
	// When Emitted=false this is "why we passed through" (e.g. "not
	// first turn"); when Emitted=true this is "why we intercepted"
	// (e.g. "first turn, ppt skill, form enabled").
	Reason string
}

// Dispatch is the single entry point. Returns DispatchResult.Emitted=true
// when the dispatcher took over the turn. Caller reads:
//
//	res := dispatcher.Dispatch(...)
//	if res.Emitted {
//	    shouldFinalize = false
//	    return // SSE done already written
//	}
//	// fall through to SDK call
//
// The dispatcher is tolerant of partial state — nil bundle, no
// QuestionForm, malformed config all degrade to Emitted=false with a
// trace reason.
func (d *QuestionFormDispatcher) Dispatch(in DispatchInput) DispatchResult {
	if !config.GetWorkAgentFeatures().IsQuestionFormEnabledFor(in.UID) {
		return DispatchResult{Reason: "feature disabled or uid not in whitelist"}
	}
	// G11 (2026-05-17) — the ThreadMessageCnt > 0 gate was retired.
	// Pre-G11 it served as proxy for "are we at the start of skill
	// interaction"; post-G11 skill interactions can start mid-thread
	// (user switches skill on turn 5) and AlreadyAnswered (now
	// per-skill via FindLatestDiscoveryAnswersForSkill) is the
	// authoritative gate.
	if in.AlreadyAnswered {
		return DispatchResult{Reason: "discovery already answered for this skill"}
	}
	if in.SkillBundle == nil || in.SkillBundle.QuestionForm == nil {
		return DispatchResult{Reason: "skill has no question_form configured"}
	}
	if !in.SkillBundle.QuestionForm.Enabled {
		return DispatchResult{Reason: "skill question_form.enabled=false"}
	}

	resolvedQuestions := d.resolveQuestions(in.SkillBundle.QuestionForm.Questions, in.Locale, in.PrefillAnswers)
	if len(resolvedQuestions) == 0 {
		// Manifest declared question_form but no usable questions.
		// Skill-authoring bug (caught by manifest validation in PR-6).
		// Don't crash the turn; fall through to SDK.
		globals.Warn(fmt.Sprintf("[QuestionForm] skill %q declares form but no usable questions", in.SkillBundle.Name))
		return DispatchResult{Reason: "form declared but no questions resolved"}
	}

	skipLabel := ""
	if key := in.SkillBundle.QuestionForm.SkipKey; key != "" {
		skipLabel = d.resolver.Resolve(in.Locale, key)
	} else {
		skipLabel = d.resolver.Resolve(in.Locale, "form.skip")
	}

	// Build the Server-owned SSE block payload consumed by Desktop.
	// G10 (2026-05-17) — form_id is now the skill name, not the
	// legacy "discovery" constant. FE round-trips it back via
	// SubmitDiscoveryAnswers so the persisted row's form_id matches
	// the skill that emitted the form. loadDiscoveryContext +
	// FindLatestDiscoveryAnswersForSkill use this form_id to scope
	// reads to the current skill (no more cross-skill leakage).
	formID := in.SkillBundle.Name
	if formID == "" {
		formID = "discovery" // legacy fallback for bundles missing a name (unreachable in prod)
	}
	blockPayload := map[string]interface{}{
		"type":    "question_form",
		"form_id": formID,
		"schema": map[string]interface{}{
			"questions":   resolvedQuestions,
			"skip_button": map[string]string{"label": skipLabel},
		},
	}
	blockJSON, err := json.Marshal(blockPayload)
	if err != nil {
		globals.Error(fmt.Sprintf("[QuestionForm] marshal block failed: %v", err))
		return DispatchResult{Reason: "block marshal error"}
	}

	// Done event with stop_reason=form_emitted. Result mimics the
	// SDK's ResultMessage shape so the existing client done-handler
	// path doesn't need a new branch.
	//
	// Note on the FE wire contract (audited 2026-05-12):
	//   - The FE currently does NOT branch on stop_reason — it only
	//     reads is_error / result.checklist_gate / result.critique_gate
	//     from the done event. The form-vs-normal-turn distinction is
	//     conveyed implicitly via the preceding block payload's `type`
	//     (question_form / directions_picker / text), which the FE
	//     does branch on (agentEventHandler.ts case 'block').
	//   - stop_reason is kept on the wire for: (a) server-side log
	//     parsing / observability — operators can filter SSE archives
	//     for "form_emitted" turns; (b) forwards-compat: if the FE
	//     ever needs to know "this was a synthetic-emit turn, skip
	//     the 'agent typed' indicator", the signal is already on the
	//     wire and no BE deploy is needed.
	// Do not remove without confirming neither (a) nor (b) is in use.
	resultPayload := map[string]interface{}{
		"type":        "result",
		"subtype":     "success",
		"is_error":    false,
		"stop_reason": "form_emitted",
		"kind":        "question_form",
		"form_id":     formID, // G10 — matches the block payload's form_id
	}
	resultJSON, err := json.Marshal(resultPayload)
	if err != nil {
		globals.Error(fmt.Sprintf("[QuestionForm] marshal result failed: %v", err))
		return DispatchResult{Reason: "result marshal error"}
	}

	in.SendEvent("block", in.TempMessageID, 0, json.RawMessage(blockJSON), nil)
	in.SendEvent("done", in.TempMessageID, 0, nil, json.RawMessage(resultJSON))

	globals.Info(fmt.Sprintf("[QuestionForm] short-circuit: skill=%s uid=%d locale=%s questions=%d",
		in.SkillBundle.Name, in.UID, in.Locale, len(resolvedQuestions)))

	return DispatchResult{
		Emitted: true,
		Reason:  fmt.Sprintf("first turn, skill=%s, %d questions emitted", in.SkillBundle.Name, len(resolvedQuestions)),
	}
}

// resolveQuestions converts skill.yaml's QuestionFormField entries
// into the self-contained wire shape consumed by Desktop. Each label / option
// is resolved against the active locale.
//
// Drops malformed entries silently:
//   - missing id (drop entry)
//   - empty label_key (drop)
//   - empty options for single_select (drop, since the form is
//     unusable without choices)
//
// This mirrors the frontend normalizer's defensive parsing — same
// inputs produce same drops on both sides.
func (d *QuestionFormDispatcher) resolveQuestions(raw []skills.QuestionFormField, locale string, prefill map[string]string) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(raw))
	for _, q := range raw {
		if q.ID == "" || q.LabelKey == "" {
			continue
		}
		typ := q.Type
		if typ == "" {
			typ = "single_select"
		}

		entry := map[string]interface{}{
			"id":    q.ID,
			"label": d.resolver.Resolve(locale, q.LabelKey),
			"type":  typ,
		}
		if q.Required {
			entry["required"] = true
		}
		if q.Default != "" {
			entry["default"] = q.Default
		}

		if typ == "single_select" || typ == "multi_select" {
			opts := make([]map[string]string, 0, len(q.Options))
			for _, opt := range q.Options {
				if opt.Value == "" || opt.LabelKey == "" {
					continue
				}
				opts = append(opts, map[string]string{
					"value": opt.Value,
					"label": d.resolver.Resolve(locale, opt.LabelKey),
				})
			}
			if len(opts) == 0 {
				// select with no options is unusable — drop the
				// whole question.
				continue
			}
			entry["options"] = opts
		}
		if v := validQuestionPrefill(q, typ, prefill[q.ID]); v != "" {
			entry["default"] = v
		}

		out = append(out, entry)
	}
	return out
}

func validQuestionPrefill(q skills.QuestionFormField, typ, value string) string {
	if value == "" {
		return ""
	}
	switch typ {
	case "single_select", "multi_select", "button":
		for _, opt := range q.Options {
			if opt.Value == value {
				return value
			}
		}
	case "text":
		return value
	}
	return ""
}
