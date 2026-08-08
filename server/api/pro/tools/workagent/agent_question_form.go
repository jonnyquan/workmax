package workagent

import (
	"encoding/json"
	"fmt"

	"server/globals"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/service/tools/workagent/skills"
)

// maybeEmitQuestionFormInput bundles the per-turn state needed to
// decide whether the M1 short-circuit should fire and, when it does,
// to emit the SSE block + done events. Mirrors the structure of
// setupTurnEnvironmentInput so HandleAgentChat reads consistently —
// a small struct rather than a 6-arg function call.
type maybeEmitQuestionFormInput struct {
	uid           uint
	chatThread    *workagentModel.ChatThread
	chatRequest   ChatStreamRequest
	userMessage   string // raw user message text — used for NLP pre-scan auto-skip
	agentMode     string
	tempMessageID string
	sendEvent     func(workagentModel.AgentSSEEvent)
	// bundle is the pre-built SkillBundle for agentMode. Optional —
	// when nil, the function loads it via the registry. G2 (2026-05-17)
	// — the caller's HandleAgentChat builds once and passes the same
	// pointer to both maybeEmitQuestionForm and maybeEmitDirectionsPicker
	// so we don't pay the renderFrameworkWithSlots cost twice. Even
	// with the loader-level caches added by G1, the framework render
	// is ctx-aware and can't be cached at the loader.
	bundle *skills.SkillBundle
}

// maybeEmitQuestionForm wraps QuestionFormDispatcher.Dispatch with
// the API-handler-side wiring: skill bundle resolution, locale
// extraction, and the SSE adapter that bridges DispatchInput's
// SendEventFunc shape to the handler's local sendEvent closure.
//
// Returns true when the dispatcher emitted SSE events and the
// handler should set shouldFinalize=false and return. Returns false
// in every "fall through to SDK" path (feature off, not first turn,
// skill has no form, etc.) — caller continues with normal SDK flow.
//
// Errors are intentionally absorbed: a failure to load the skill
// bundle or build the dispatch input shouldn't block the SDK call,
// since the SDK can still produce a valid response without the form.
// The structured log line carries the skip reason for ops triage.
func (api *AIChatApiNew) maybeEmitQuestionForm(in maybeEmitQuestionFormInput) bool {
	bundle := in.bundle
	if bundle == nil {
		// Fallback for legacy call sites that don't pre-build (tests,
		// future surfaces that re-enter this path without the
		// HandleAgentChat preamble).
		var err error
		bundle, err = loadSkillBundleForFormDispatch(in.agentMode)
		if err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] question_form bundle load skipped (mode=%s): %v", in.agentMode, err))
			return false
		}
	}

	// Adapter: DispatchInput.SendEvent has a typed signature with
	// raw JSON fields; the handler's sendEvent takes a typed
	// AgentSSEEvent struct. Map between them here so the dispatcher
	// stays free of model-package dependencies and the handler
	// stays free of dispatcher implementation details.
	sendAdapter := func(eventType, messageID string, blockIndex int, block, result json.RawMessage) {
		typ := workagentModel.AgentEventBlock
		switch eventType {
		case "block":
			typ = workagentModel.AgentEventBlock
		case "done":
			typ = workagentModel.AgentEventDone
		case "message":
			typ = workagentModel.AgentEventMessage
		}
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:       typ,
			MessageID:  messageID,
			BlockIndex: blockIndex,
			Block:      block,
			Result:     result,
		})
	}

	// G10/G11 (2026-05-17) — check whether THIS skill already has
	// a discovery_form_result row in the thread. If so, the form
	// has been answered (or NLP-inferred, or user-skipped with
	// defaults) for this skill and we don't re-fire it.
	//
	// Pre-G11 the dispatcher relied on "ThreadMessageCnt > 0" as
	// a single-shot gate, which silently suppressed the form when
	// a user switched skills mid-thread (e.g. ppt → socialAd on
	// turn 5) — socialAd's distinct questions never got asked.
	// Per-skill scoping makes the dispatch correct in both directions:
	// don't re-ask if answered, do ask if not.
	alreadyAnswered := false
	if existing, err := workagentService.DefaultMessageRepository().
		FindLatestDiscoveryAnswersForSkill(uint(in.chatThread.Id), in.agentMode); err == nil && existing != nil {
		alreadyAnswered = true
	}
	prefillAnswers := map[string]string(nil)

	// PR-6: NLP pre-scan. When the first user message already
	// implies all required fields (e.g. "10-page deck for series A
	// investors, modern minimal style"), skip the form entirely.
	// The dispatcher reads AlreadyAnswered and falls through to the
	// SDK with the inferred answers persisted in metadata.
	if !alreadyAnswered && bundle.QuestionForm != nil && bundle.QuestionForm.Enabled && in.userMessage != "" {
		inference := workagentService.InferFormAnswers(in.userMessage, bundle.QuestionForm)
		if inference.AllRequiredFilled {
			alreadyAnswered = true
			globals.Info(fmt.Sprintf("[Agent API] question_form auto-skipped: NLP inferred answers=%v", inference.Answers))
			// PR-6b: persist as synthetic discovery_form_result row
			// so subsequent turns' prompt assembler can read it back
			// via FindLatestDiscoveryAnswers and inject as
			// <discovery-context> in SystemAdditions. The persistence
			// is best-effort — failure logs but doesn't block the
			// turn.
			// G10 (2026-05-17) — pass agentMode so the persisted
			// form_id scopes the row to this skill. Without it, a
			// later skill-switch would inherit these inferred
			// answers across skill boundaries.
			workagentService.PersistInferredDiscoveryAnswers(
				in.uid,
				uint(in.chatThread.Id),
				in.agentMode,
				inference.Answers,
				"nlp_inferred",
			)
			workagentService.PersistPassMode(
				in.uid,
				uint(in.chatThread.Id),
				workagentService.WorkAgentPassModeDraft,
				"question_form_nlp_inferred",
			)
		} else if len(inference.Answers) > 0 {
			prefillAnswers = inference.Answers
			globals.Info(fmt.Sprintf("[Agent API] question_form partial NLP prefill: answers=%v", inference.Answers))
		}
	}

	res := workagentService.DefaultQuestionFormDispatcher.Dispatch(workagentService.DispatchInput{
		UID:              in.uid,
		ThreadMessageCnt: in.chatThread.MessageCount,
		SkillBundle:      bundle,
		Locale:           extractUserLocale(in.chatRequest),
		PrefillAnswers:   prefillAnswers,
		AlreadyAnswered:  alreadyAnswered,
		TempMessageID:    in.tempMessageID,
		SendEvent:        sendAdapter,
	})

	if res.Emitted {
		globals.Info(fmt.Sprintf("[Agent API] question_form short-circuit fired: %s", res.Reason))
	}
	return res.Emitted
}

// loadSkillBundleForFormDispatch fetches the skill bundle for the
// supplied agent mode. We pass an empty BuildContext because the
// dispatcher only reads the QuestionForm field — fileContext /
// hasFiles are irrelevant for form emission.
//
// Errors here are silent fallthroughs (the caller's signal is "no
// form, run SDK normally"). Real misconfiguration shows up via the
// skill registry's WarnFunc, which already routes through
// globals.Warn.
func loadSkillBundleForFormDispatch(agentMode string) (*skills.SkillBundle, error) {
	if agentMode == "" {
		return nil, fmt.Errorf("empty agent mode")
	}
	registry := workagentService.GetSkillRegistry()
	return registry.Build(agentMode, skills.BuildContext{})
}

// extractUserLocale pulls the user's preferred language off the
// incoming request. Desktop sends its active locale as the top-level `lang`
// field on ChatStreamRequest.
//
// Empty / unset → empty string, which the i18n resolver treats as
// "fall through to en". Region suffixes ("zh-CN") flow through
// untouched — the resolver's normalizeLocale strips them.
//
// Previously this returned "" unconditionally as a PR-4 stub
// waiting on PR-5 wiring; the wiring was already present on the
// frontend, the BE just never read it. Result: every non-EN user
// saw English form labels until 2026-05-12 even though the
// frontend transmitted their locale correctly. (Caught by the
// wire-shape audit; one of the rectangular "shipped but
// unconnected" bugs the audit pattern is designed to surface.)
func extractUserLocale(req ChatStreamRequest) string {
	return req.Lang
}

// maybeEmitDirectionsPickerInput bundles the per-turn state for the
// M2 picker dispatcher. Mirrors maybeEmitQuestionFormInput so
// HandleAgentChat reads consistently.
type maybeEmitDirectionsPickerInput struct {
	uid           uint
	chatThread    *workagentModel.ChatThread
	chatRequest   ChatStreamRequest
	agentMode     string
	tempMessageID string
	sendEvent     func(workagentModel.AgentSSEEvent)
	// bundle — same shape and G2 rationale as maybeEmitQuestionFormInput.
	// Pass the same pointer the M1 caller used; nil falls back to a
	// fresh load.
	bundle *skills.SkillBundle
}

// maybeEmitDirectionsPicker wraps DirectionsPickerDispatcher.Dispatch
// with the API-handler-side wiring: skill bundle resolution, locale
// extraction, brand-context detection, SSE adapter.
//
// Returns true when the dispatcher emitted SSE events and the
// handler should set shouldFinalize=false and return. False
// (every "fall through to SDK" path) → handler continues with
// normal SDK flow.
//
// Errors from skill bundle load are absorbed: a bundle that won't
// load means we can't tell if directions_fallback is on, so we
// just skip the picker rather than blocking the turn.
func (api *AIChatApiNew) maybeEmitDirectionsPicker(in maybeEmitDirectionsPickerInput) bool {
	bundle := in.bundle
	if bundle == nil {
		var err error
		bundle, err = loadSkillBundleForFormDispatch(in.agentMode)
		if err != nil {
			// Same silent skip as the form path — bundle errors
			// shouldn't block production turns.
			return false
		}
	}

	// Brand context detection — backlog #11 3/n switched the primary
	// signal from chat_message metadata scan to brand_assets table
	// (uid-scoped, cross-thread). HasBrandContext keeps the metadata
	// fallback for pre-#11 threads so suppression stays correct
	// during the migration window.
	hasBrandContext := workagentService.HasBrandContext(uint(in.chatThread.UID), uint(in.chatThread.Id))

	sendAdapter := func(eventType, messageID string, blockIndex int, block, result json.RawMessage) {
		typ := workagentModel.AgentEventBlock
		switch eventType {
		case "block":
			typ = workagentModel.AgentEventBlock
		case "done":
			typ = workagentModel.AgentEventDone
		case "message":
			typ = workagentModel.AgentEventMessage
		}
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:       typ,
			MessageID:  messageID,
			BlockIndex: blockIndex,
			Block:      block,
			Result:     result,
		})
	}

	res := workagentService.DefaultDirectionsPickerDispatcher.Dispatch(workagentService.DirectionsDispatchInput{
		UID:             in.uid,
		ThreadID:        uint(in.chatThread.Id),
		SkillBundle:     bundle,
		Locale:          extractUserLocale(in.chatRequest),
		HasBrandContext: hasBrandContext,
		TempMessageID:   in.tempMessageID,
		SendEvent:       sendAdapter,
	})

	if res.Emitted {
		globals.Info(fmt.Sprintf("[Agent API] directions_picker short-circuit fired: %s", res.Reason))
	}
	return res.Emitted
}
