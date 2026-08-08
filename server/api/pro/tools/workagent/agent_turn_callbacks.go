package workagent

// agent_turn_callbacks.go owns the SDK-callback adapter struct
// (agentTurnCallbacks) and its OnMessage / OnBlock / OnDone /
// runSkillValidators methods. Pulled out of agent_api.go (B1 chunk 2)
// so the giant HandleAgentChat handler doesn't share a file with the
// per-callback state machine. Same package, same names — every
// existing call site in agent_api.go works unchanged.
//
// The struct is a "callback wrapper": HandleAgentChat constructs one
// per turn from the prepare/setup phase outputs, hands it to the SDK
// via toAgentCallbacks(), and then reads the outbound signals after
// the SDK returns. The shape is documented at the struct itself
// rather than at the call site, so a future maintainer doesn't have
// to grep through the handler to see what state the callbacks need.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"server/config"
	"server/globals"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/service/tools/workagent/detectors"
	workagentSkills "server/service/tools/workagent/skills"

	"github.com/gin-gonic/gin"
)

// agentTurnCallbacks owns the SDK callbacks (OnMessage / OnBlock /
// OnDone) for a single chat turn. Pulled out of HandleAgentChat's
// 115-line inline closure because that closure captured 13 different
// pieces of state — too many to scan as a literal, too easy to
// reach for the wrong one when adding a new branch.
//
// Read-only fields are inputs from the prepare/setup phase; the
// outbound fields below are mutated by OnDone and read by the caller
// after the SDK call returns.
type agentTurnCallbacks struct {
	// Read-only inputs (set once at construction).
	api                  *AIChatApiNew
	ctx                  *gin.Context
	runtimeCtx           context.Context
	chatThread           *workagentModel.ChatThread
	chatRequest          ChatStreamRequest
	workspaceRootPath    string
	threadWorkspace      string
	agentMode            string
	modelTier            string
	userMessage          string
	revisionParentID     uint
	comparisonArtifactID uint
	comparisonSource     string
	assetCandidateID     uint
	tempMessageID        string
	uid                  uint
	executionStartTime   time.Time
	sendEvent            func(workagentModel.AgentSSEEvent)
	knowledgeMetadata    workagentService.KnowledgeRetrievalMetadata

	// Outbound signals — written by OnDone, read by the caller after
	// the SDK call returns.

	// generatedFilesPayload is the normalised list of files the SDK
	// turn produced (under threadWorkspace, after executionStartTime).
	// Caller attaches this to the saved conversation row and uses
	// len() for usage analytics.
	generatedFilesPayload []map[string]interface{}

	// generatedThreadFileIDs are the DB rows registered for this turn's
	// output files. Populated in OnDone before checklist/critique gates
	// so those gates can write lifecycle status to the artifact registry.
	generatedThreadFileIDs []uint

	// pptMissingOutput is true iff agentMode=="ppt" but no .pptx
	// shows up in the generated files (and the result isn't already
	// flagged as an error). Caller demotes shouldFinalize on this
	// signal so the user isn't billed for an under-specified turn.
	pptMissingOutput bool

	// accumulatedText is the concatenation of every assistant
	// text-block content streamed during the turn. Captured during
	// OnBlock so it's ready for the post-emit checklist gate
	// (PR-9b/2a runChecklistGate). Streaming text lands here in
	// emit order, matching what the user sees on screen.
	//
	// Tool-use / tool-result / thinking blocks are intentionally
	// skipped — the gate inspects user-facing artifact text only,
	// not the model's internal monologue.
	accumulatedText string

	// gateBlocked is true when runChecklistGate produced a Block
	// decision on this turn. PR-9b/2b reads this signal in the
	// redo loop to decide whether to re-invoke the SDK with the
	// redo prompt. Reset between redo iterations via resetForRedo.
	gateBlocked bool

	// gateRedoPrompt carries the FormatBlockFindings output that
	// the redo loop injects as the next turn's user message.
	// Populated by runChecklistGate alongside gateBlocked. Empty
	// when the gate didn't block.
	gateRedoPrompt string

	// gateAutoRedoAllowed records the specific gate's auto-redo
	// feature decision. Checklist and critique gates are controlled
	// by different flags, but the outer SDK loop only needs one
	// per-attempt boolean once a gate has blocked.
	gateAutoRedoAllowed bool
}

func (cb *agentTurnCallbacks) postProcessContext() context.Context {
	if cb.runtimeCtx != nil {
		return cb.runtimeCtx
	}
	return cb.ctx.Request.Context()
}

// resetForRedo wipes the per-turn accumulators that need to start
// fresh on each redo iteration. Identity (uid / agentMode / etc.)
// stays intact; only stream-driven state gets cleared.
func (cb *agentTurnCallbacks) resetForRedo() {
	cb.accumulatedText = ""
	cb.gateBlocked = false
	cb.gateRedoPrompt = ""
	cb.gateAutoRedoAllowed = false
	cb.generatedFilesPayload = nil
	cb.generatedThreadFileIDs = nil
	cb.pptMissingOutput = false
	cb.executionStartTime = time.Now()
}

// toAgentCallbacks adapts the struct to the value-based AgentCallbacks
// interface the SDK consumes. Returning method values rather than
// inline closures keeps the receiver pinned (so an OnDone retry from
// the SDK can't accidentally see a different cbs instance).
func (cb *agentTurnCallbacks) toAgentCallbacks() workagentService.AgentCallbacks {
	return workagentService.AgentCallbacks{
		OnMessage: cb.OnMessage,
		OnBlock:   cb.OnBlock,
		OnDone:    cb.OnDone,
	}
}

// OnMessage is the SDK's per-message callback. Sanitize workspace
// paths out of the JSON before forwarding to the SSE client (so
// absolute /agent_workspace/uid/N/... paths don't leak the on-disk
// layout) and emit a Message SSE event.
func (cb *agentTurnCallbacks) OnMessage(messageType string, message json.RawMessage, messageIndex int) {
	sanitized := workagentService.SanitizeAgentJSON(message, cb.workspaceRootPath)
	event := workagentModel.NewMessageEvent(cb.tempMessageID, messageType, messageIndex, sanitized)
	cb.sendEvent(event)

	// Demoted from Info: a 100-block PPT turn fires this every SDK
	// message. Operator dashboards only care about turn-level outcomes
	// (Done event) — per-message tracing belongs at Debug.
	globals.Debug(fmt.Sprintf("[Agent API] Message event: type=%s, index=%d",
		messageType, messageIndex))
}

// OnBlock is the SDK's per-content-block callback. Same
// sanitize-and-emit pattern as OnMessage but for the inner content
// blocks (text deltas, tool_use, tool_result, ...).
func (cb *agentTurnCallbacks) OnBlock(messageType string, block json.RawMessage, messageIndex int, blockIndex int) {
	sanitized := workagentService.SanitizeAgentJSON(block, cb.workspaceRootPath)
	event := workagentModel.NewBlockEvent(cb.tempMessageID, messageIndex, blockIndex, sanitized)
	cb.sendEvent(event)

	// PR-9b/2a: capture assistant text into accumulatedText so the
	// post-emit checklist gate has artifact text to scan. Best-
	// effort — failure to extract just leaves accumulatedText
	// empty (gate detectors that need text return Skipped).
	if extracted := extractTextFromBlock(block); extracted != "" {
		cb.accumulatedText += extracted
	}

	globals.Debug(fmt.Sprintf("[Agent API] Block event: type=%s, msg_idx=%d, block_idx=%d",
		describeBlockType(block), messageIndex, blockIndex))
}

// extractTextFromBlock pulls the user-facing text out of a streamed
// content block. Returns empty for non-text blocks (tool_use,
// tool_result, thinking, image) — the gate inspects what the user
// sees, not the model's tooling chatter.
func extractTextFromBlock(block json.RawMessage) string {
	if len(block) == 0 {
		return ""
	}
	var probe struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(block, &probe); err != nil {
		return ""
	}
	if probe.Type == "text" {
		return probe.Text
	}
	return ""
}

// OnDone is the SDK's terminal callback. It fires once after the
// agent turn finishes (success, error, or in-band error) and owns
// four enrichment passes BEFORE the final SSE done event:
//
//  1. wrapResultErrorIfNeeded — wrap 429s and other SDK errors
//     with user-friendly messages.
//  2. Scan output dir → attach generated files to result + capture
//     cb.generatedFilesPayload for the caller's persistence path.
//  3. PPT-mode contract: if no .pptx was produced AND the result
//     isn't already an error, mark the result as a "missing_output"
//     warning AND set cb.pptMissingOutput so the caller demotes
//     shouldFinalize (don't bill for an under-specified turn).
//  4. Run skill-declared post-generation validators (e.g. ppt's
//     validate-pptx.py). Wrapped in its own recover() so a validator
//     panic can't unwind through OnDone and prevent the done event
//     from firing — frontend would hang until SSE timeout. The
//     outer panic-refund defer would still catch the panic for
//     billing, but the user-facing UX would be a stuck spinner.
//     Validation is best-effort decoration.
//
// Then attachRemainingCreditsToResult enriches with the user's
// post-deduction credit balance so the UI can render "N turns left"
// without a round-trip, and the sanitized result ships as the SSE
// done event.
func (cb *agentTurnCallbacks) OnDone(result json.RawMessage) {
	result = wrapResultErrorIfNeeded(result)

	files := scanOutputsDirectorySince(cb.threadWorkspace, cb.executionStartTime)
	globals.Info(fmt.Sprintf("[Agent] Found %d newly generated files (after %s)",
		len(files), cb.executionStartTime.Format("15:04:05")))
	cb.registerGeneratedArtifacts(files)

	if len(files) > 0 {
		fileData := cb.api.buildFileOutputData(cb.chatThread, files)
		fileList := normalizeFileOutputList(fileData["files"])
		if len(fileList) > 0 {
			cb.generatedFilesPayload = fileList
			result = attachGeneratedFilesToResult(result, fileList)
			globals.Info(fmt.Sprintf("[Agent] Attached %d generated files to result", len(fileList)))
		}
	} else {
		globals.Info("[Agent] No new files generated during this execution")
	}

	if cb.chatThread != nil && !resultIndicatesError(result) {
		if issues := workagentService.VariationDraftManifestIssuesForOutputScan(cb.threadWorkspace, cb.chatThread.Id, extractOutputPaths(files)); len(issues) > 0 {
			result = markVariationManifestIssuesAsWarning(result, issues)
			globals.Warn(fmt.Sprintf("[Agent API] Pass 1 variation manifest issues: %s", strings.Join(issues, "; ")))
		}
	}

	// PPT mode contract: should generate at least one .ppt/.pptx
	// output. If not generated (often due to incomplete user
	// requirements), return a warning instead of a hard failure so
	// the user can refine input and continue.
	if cb.agentMode == "ppt" && !resultIndicatesError(result) && !hasGeneratedPPTFile(files) {
		cb.pptMissingOutput = true
		missingFields, suggestedPrompt := derivePPTMissingFields(cb.userMessage, cb.chatRequest.Metadata, cb.chatRequest.Files)
		result = markResultAsWarning(
			result,
			"missing_output",
			"PPT was not generated yet. Please provide clearer slide requirements (topic, audience, page count, style) and retry.",
			"PPT_OUTPUT_REQUIRED",
			missingFields,
			suggestedPrompt,
		)
		globals.Warn("[Agent API] PPT mode execution finished without ppt/pptx output")
	}

	cb.runSkillValidators(&result, files)
	cb.runChecklistGate(&result, files)
	cb.runCritiqueGate(&result)
	cb.captureArtifactComparison()
	cb.captureArtifactAssetCandidate()
	result = attachRAGMetadataToResult(result, cb.knowledgeMetadata)

	// Enrich with the user's post-deduction credit balance so the UI
	// can render "N turns left" instead of waiting for the next
	// failed-reservation round-trip. See attachRemainingCreditsToResult.
	result = attachRemainingCreditsToResult(result, cb.uid)

	sanitized := workagentService.SanitizeAgentJSON(result, cb.workspaceRootPath)
	event := workagentModel.NewDoneEvent(cb.tempMessageID, sanitized)
	cb.sendEvent(event)

	meta := extractResultMeta(result)
	globals.Info(fmt.Sprintf("[Agent API] Done event: session_id=%s, turns=%d",
		meta.SessionID, meta.NumTurns))
}

func markVariationManifestIssuesAsWarning(result json.RawMessage, issues []string) json.RawMessage {
	if len(issues) == 0 {
		return result
	}
	return markResultAsWarning(
		result,
		"variation_manifest_issue",
		"Pass 1 variation drafts were created, but the variation manifest is missing or invalid. The picker may not be able to bind draft artifacts.",
		"PASS1_VARIATION_MANIFEST_REQUIRED",
		nil,
		buildVariationManifestRepairPrompt(issues),
	)
}

func buildVariationManifestRepairPrompt(issues []string) string {
	var b strings.Builder
	b.WriteString("Repair the Pass 1 variation manifest for this draft pass.\n\n")
	if len(issues) > 0 {
		b.WriteString("Observed manifest issues:\n")
		for _, issue := range issues {
			issue = strings.TrimSpace(issue)
			if issue == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(issue)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Write outputs/.workagent/pass_1_variations.json with this shape:\n")
	b.WriteString(`{"variations":[{"id":"conservative|balanced|bold","label":"...","stance":"...","file_path":"relative/path.ext","design_system_basename":"...","asset_contract":"..."}]}`)
	b.WriteString("\n\nEnsure every file_path exists under outputs/, every variation id is unique, and each listed draft artifact can be previewed. Do not regenerate the final output yet.")
	return b.String()
}

func (cb *agentTurnCallbacks) registerGeneratedArtifacts(files []FileOutputInfo) {
	if len(files) == 0 || cb.chatThread == nil {
		return
	}
	ids := make([]uint, 0, len(files))
	for _, file := range files {
		if file.Path == "" {
			continue
		}
		threadFile, err := workagentService.GetFileService().RegisterOutputFile(
			int(cb.uid),
			cb.chatThread.Id,
			file.Path,
			file.Description,
		)
		if err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] artifact registration skipped for %s: %v", file.Path, err))
			continue
		}
		ids = append(ids, threadFile.Id)
	}
	cb.generatedThreadFileIDs = ids
	if len(ids) > 0 {
		globals.Info(fmt.Sprintf("[Agent API] Registered %d generated artifact file(s)", len(ids)))
	}
	if cb.revisionParentID != 0 {
		count, err := workagentService.NewArtifactRegistryRepository(nil).LinkThreadFilesToParent(
			int(cb.uid),
			cb.chatThread.Id,
			ids,
			cb.revisionParentID,
		)
		if err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] artifact parent link skipped parent=%d: %v", cb.revisionParentID, err))
		} else if count > 0 {
			globals.Info(fmt.Sprintf("[Agent API] linked %d artifact file(s) to parent artifact-%d", count, cb.revisionParentID))
		}
	}
	if cb.comparisonArtifactID != 0 && cb.comparisonSource == "agent_visual_diff" {
		count, err := workagentService.NewArtifactRegistryRepository(nil).LinkThreadFilesToParentWithRelation(
			int(cb.uid),
			cb.chatThread.Id,
			ids,
			cb.comparisonArtifactID,
			workagentModel.ArtifactRelationComparisonReport,
		)
		if err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] visual diff artifact link skipped parent=%d: %v", cb.comparisonArtifactID, err))
		} else if count > 0 {
			globals.Info(fmt.Sprintf("[Agent API] linked %d visual diff report artifact(s) to artifact-%d", count, cb.comparisonArtifactID))
		}
	}
}

func (cb *agentTurnCallbacks) updateGeneratedArtifactLifecycle(status string, reviewState string, source string, summary string) {
	cb.updateGeneratedArtifactLifecycleWithReviewDetails(status, reviewState, source, summary, "")
}

func (cb *agentTurnCallbacks) updateGeneratedArtifactLifecycleWithReviewDetails(status string, reviewState string, source string, summary string, reviewDetailsJSON string) {
	if len(cb.generatedThreadFileIDs) == 0 || cb.chatThread == nil {
		return
	}
	count, err := workagentService.NewArtifactRegistryRepository(nil).UpdateLifecycleForThreadFilesWithReviewDetails(
		int(cb.uid),
		cb.chatThread.Id,
		cb.generatedThreadFileIDs,
		status,
		reviewState,
		source,
		summary,
		reviewDetailsJSON,
	)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Agent API] artifact lifecycle update failed source=%s status=%s review=%s: %v", source, status, reviewState, err))
		return
	}
	if count > 0 {
		globals.Info(fmt.Sprintf("[Agent API] artifact lifecycle updated source=%s status=%s review=%s count=%d", source, status, reviewState, count))
	}
}

func (cb *agentTurnCallbacks) useGeneratedArtifactAsRedoParent(source string) {
	if len(cb.generatedThreadFileIDs) == 0 || cb.chatThread == nil {
		return
	}
	artifactID, err := workagentService.NewArtifactRegistryRepository(nil).LatestArtifactIDForThreadFiles(
		int(cb.uid),
		cb.chatThread.Id,
		cb.generatedThreadFileIDs,
	)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Agent API] redo parent capture failed source=%s: %v", source, err))
		return
	}
	if artifactID == 0 {
		return
	}
	cb.revisionParentID = artifactID
	globals.Info(fmt.Sprintf("[Agent API] redo parent captured source=%s artifact-%d", source, artifactID))
}

// runSkillValidators runs the post-generation skill validators (e.g.
// ppt's validate-pptx.py: structural + content check on the .pptx).
// Failures degrade gracefully — a missing python3 / timeout / crash
// surfaces as a "skipped" status, not a turn-failing error. The full
// result set rides along on the done event so the frontend can
// render an inline warning chip.
//
// Wrapped in its own recover() so a validator panic (e.g. disk-full
// during script extraction, malformed embed.FS content) doesn't
// unwind out of OnDone and prevent the done event from firing. The
// outer panic-refund defer would catch the panic correctly for
// billing purposes, but the SSE done event would never be sent and
// the frontend would hang until SSE timeout. Validation is
// best-effort decoration; treat its failure as "no validation field
// on done" rather than "turn dies".
//
// result is taken by pointer so the validator-results attach is
// observable to the caller (OnDone). A return-the-new-result design
// would force OnDone to thread the value through a temp.
func (cb *agentTurnCallbacks) runSkillValidators(result *json.RawMessage, files []FileOutputInfo) {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("[Agent API] Skill validator panicked: %v (skipping validation; turn continues)", r))
		}
	}()
	bundle, bundleErr := workagentService.GetSkillRegistry().Build(cb.agentMode, workagentSkills.BuildContext{})
	if bundleErr != nil {
		return
	}
	outputPaths := extractOutputPaths(files)
	validatorResults := workagentService.RunSkillPostGenerationScripts(cb.postProcessContext(), bundle, workagentService.ValidatorInput{
		AgentMode:   cb.agentMode,
		OutputFiles: outputPaths,
		ThreadDir:   cb.threadWorkspace,
	})
	if len(validatorResults) > 0 {
		*result = workagentService.AttachValidationResultsToResult(*result, validatorResults)
		globals.Info(fmt.Sprintf("[Agent API] Skill validators attached: %d result(s)", len(validatorResults)))
	}
}

// runChecklistGate is the M5 / DS-4 post-emit gate runner (PR-9b/2a
// — read-only attach; redo loop deferred to PR-9b/2b).
//
// Runs each P0/P1/P2 detector named in the active skill's
// references/checklist.md against the artifact text + generated
// files. Aggregates the verdicts via RunGate and attaches the
// summary to the done event's result payload as a `checklist_gate`
// field.
//
// Behavior contract for PR-9b/2a:
//   - Pass / Warn / Block all attach metadata; the turn always
//     emits regardless of decision (no redo, no SDK re-invoke)
//   - Block-decision metadata carries the redo-prompt formatter's
//     output as `redo_prompt` so a future PR-9b/2b can use it
//     directly without re-running the gate
//   - Frontend can surface the P1 fails as the override card
//     (today the field rides observability; UI wiring is PR-9b/3)
//
// Same fail-safe contract as runSkillValidators — panics are
// recovered, errors degrade to "no gate field on done", the turn
// still ships.
func (cb *agentTurnCallbacks) runChecklistGate(result *json.RawMessage, files []FileOutputInfo) {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("[Agent API] Checklist gate panicked: %v (skipping; turn continues)", r))
		}
	}()

	// Feature flag: only run when WA_FEAT_PRE_EMIT_GATE is on for
	// this (uid, skill). Same gate flag controls preflight + post-
	// emit so the operator only flips one knob.
	if !config.GetWorkAgentFeatures().IsPreEmitGateEnabledForSkill(cb.uid, cb.agentMode) {
		return
	}

	checklist := workagentService.LoadChecklistForSkill("", cb.agentMode)
	if checklist.IsEmpty() {
		return
	}

	// Use the text accumulated across OnBlock calls. The streaming
	// callbacks fire well before OnDone so accumulatedText is the
	// full assistant text by the time we run the gate.
	gateResult := workagentService.RunGate(
		cb.postProcessContext(),
		checklist,
		detectors.Input{
			SkillName: cb.agentMode,
			Artifact: detectors.Artifact{
				Text:  cb.accumulatedText,
				Files: extractOutputPaths(files),
			},
			ThreadDir: cb.threadWorkspace,
		},
	)

	*result = workagentService.AttachChecklistGateResult(*result, gateResult)
	globals.Info(fmt.Sprintf("[Agent API] Checklist gate attached: skill=%s decision=%s p0_fails=%d p1_fails=%d",
		cb.agentMode, gateResult.Decision, len(gateResult.P0Fails), len(gateResult.P1Fails)))
	switch gateResult.Decision {
	case workagentService.GateBlock:
		cb.updateGeneratedArtifactLifecycle(workagentModel.ArtifactStatusChangesRequested, workagentModel.ArtifactReviewChangesRequested, "checklist_gate", summarizeChecklistGate(gateResult))
	case workagentService.GateWarn:
		cb.updateGeneratedArtifactLifecycle(workagentModel.ArtifactStatusNeedsReview, workagentModel.ArtifactReviewNeedsReview, "checklist_gate", summarizeChecklistGate(gateResult))
	case workagentService.GatePass:
		cb.updateGeneratedArtifactLifecycle(workagentModel.ArtifactStatusDraft, workagentModel.ArtifactReviewNone, "checklist_gate", "")
	}

	// PR-9b/2b: signal the redo loop. Block decision → caller
	// re-invokes the SDK with FormatBlockFindings as the next
	// user message; warn / pass → caller emits + exits the loop.
	if gateResult.Decision == workagentService.GateBlock {
		cb.gateBlocked = true
		cb.gateRedoPrompt = workagentService.FormatBlockFindings(gateResult.P0Fails)
		cb.gateAutoRedoAllowed = config.GetWorkAgentFeatures().IsPreEmitGateAutoRedoEnabledFor(cb.uid, cb.agentMode)
		if cb.gateAutoRedoAllowed {
			cb.useGeneratedArtifactAsRedoParent("checklist_gate")
		}
	}
}

// runCritiqueGate is the M3 critique sub-agent runner (Sprint-B
// 3/3). Fires AFTER runChecklistGate so the deterministic pre-emit
// rules get first say — no point spending Haiku tokens scoring an
// artifact the rule gate already flagged for redo.
//
// Behaviour contract:
//   - Feature flag IsCritiqueGateEnabledForSkill must pass
//   - Skip when pre-emit gate already blocked (cb.gateBlocked
//     true) — that turn is regenerating, critique would be noise
//   - Skip on empty accumulatedText — no artifact to score
//   - Skip on SDK / parse error — log, surface no critique_gate
//     metadata, let the turn emit cleanly (fail-soft posture)
//   - On Block decision + auto-redo flag: set the same gateBlocked
//   - gateRedoPrompt signals the pre-emit Block uses, so the
//     existing redo loop in agent_api.go transparently picks up
//     the critique-driven redo without a second branch
//
// Same fail-safe contract as runSkillValidators / runChecklistGate
// — panics recover, errors degrade to "no critique field on done."
func (cb *agentTurnCallbacks) runCritiqueGate(result *json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("[Agent API] Critique gate panicked: %v (skipping; turn continues)", r))
		}
	}()

	if !config.GetWorkAgentFeatures().IsCritiqueGateEnabledForSkill(cb.uid, cb.agentMode) {
		return
	}
	// Pre-emit Block already commits the turn to redo. Critique on
	// an about-to-be-discarded artifact is wasted Haiku tokens.
	if cb.gateBlocked {
		return
	}
	if cb.accumulatedText == "" {
		return
	}

	in := workagentService.CritiqueInput{
		UID:            cb.uid,
		ModelTier:      cb.modelTier,
		SkillName:      cb.agentMode,
		ArtifactText:   cb.accumulatedText,
		GeneratedFiles: cb.generatedFilesPayload,
		WorkspaceRoot:  cb.threadWorkspace,
	}
	critique, err := workagentService.RunCritique(cb.postProcessContext(), nil, in)
	if err != nil {
		// Fail-soft: log + skip metadata. The turn already produced
		// an artifact; an unscored verdict is better than crashing
		// the post-emit pipeline.
		globals.Warn(fmt.Sprintf("[Agent API] Critique gate skipped (uid=%d skill=%s): %v",
			cb.uid, cb.agentMode, err))
		return
	}

	thresholds := workagentService.DefaultCritiqueThresholds()
	decision := workagentService.EvaluateCritique(critique, thresholds)
	*result = workagentService.AttachCritiqueGateResult(*result, critique, decision, cb.agentMode)

	globals.Info(fmt.Sprintf("[Agent API] Critique gate attached: skill=%s decision=%s overall=%.1f",
		cb.agentMode, decision, critique.OverallScore))
	switch decision {
	case workagentService.CritiqueBlock:
		cb.updateGeneratedArtifactLifecycleWithReviewDetails(workagentModel.ArtifactStatusChangesRequested, workagentModel.ArtifactReviewChangesRequested, "critique_gate", summarizeCritiqueGate(critique), marshalCritiqueReviewDetails(critique, decision))
	case workagentService.CritiqueWarn:
		cb.updateGeneratedArtifactLifecycleWithReviewDetails(workagentModel.ArtifactStatusNeedsReview, workagentModel.ArtifactReviewNeedsReview, "critique_gate", summarizeCritiqueGate(critique), marshalCritiqueReviewDetails(critique, decision))
	case workagentService.CritiquePass:
		cb.updateGeneratedArtifactLifecycleWithReviewDetails(workagentModel.ArtifactStatusApproved, workagentModel.ArtifactReviewApproved, "critique_gate", "", marshalCritiqueReviewDetails(critique, decision))
	}

	// On Block, share the redo signals with the pre-emit gate so
	// the unified redo loop fires regardless of which gate caught
	// the issue. Auto-redo is gated independently — ops can run
	// critique scoring (verdict surfaces in the done event) without
	// burning the SDK on a regeneration.
	if decision == workagentService.CritiqueBlock {
		if config.GetWorkAgentFeatures().IsCritiqueGateAutoRedoEnabledFor(cb.uid, cb.agentMode) {
			cb.gateBlocked = true
			cb.gateRedoPrompt = workagentService.FormatCritiqueRedoPrompt(critique, thresholds)
			cb.gateAutoRedoAllowed = true
			cb.useGeneratedArtifactAsRedoParent("critique_gate")
		}
	}
}

func summarizeChecklistGate(gateResult workagentService.GateResult) string {
	items := append([]workagentService.GateItemResult{}, gateResult.P0Fails...)
	items = append(items, gateResult.P1Fails...)
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, 5)
	for _, item := range items {
		title := strings.TrimSpace(item.Item.Title)
		if title == "" {
			title = strings.TrimSpace(item.Item.ID)
		}
		issues := strings.TrimSpace(strings.Join(item.Result.Issues, "; "))
		if title == "" && issues == "" {
			continue
		}
		if issues != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", title, issues))
		} else {
			lines = append(lines, "- "+title)
		}
		if len(lines) >= 5 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func summarizeCritiqueGate(critique workagentService.CritiqueResult) string {
	lines := make([]string, 0, 5)
	for _, fix := range critique.TopFixes {
		fix = strings.TrimSpace(fix)
		if fix == "" {
			continue
		}
		lines = append(lines, "- "+fix)
		if len(lines) >= 5 {
			break
		}
	}
	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}
	for _, dim := range workagentService.CritiqueDimensions() {
		score := critique.Dimensions[dim]
		for _, fix := range score.Fixes {
			fix = strings.TrimSpace(fix)
			if fix == "" {
				continue
			}
			lines = append(lines, fmt.Sprintf("- %s: %s", dim, fix))
			if len(lines) >= 5 {
				return strings.Join(lines, "\n")
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func marshalCritiqueReviewDetails(critique workagentService.CritiqueResult, decision workagentService.CritiqueDecision) string {
	dimensions := make(map[string]workagentService.ArtifactReviewDimensionDetails, len(critique.Dimensions))
	for _, dim := range workagentService.CritiqueDimensions() {
		score, ok := critique.Dimensions[dim]
		if !ok {
			continue
		}
		dimensions[string(dim)] = workagentService.ArtifactReviewDimensionDetails{
			Score: score.Score,
			Notes: strings.TrimSpace(score.Notes),
			Fixes: trimNonEmptyStrings(score.Fixes, 6),
		}
	}
	payload := workagentService.ArtifactReviewDetails{
		Source:       "critique_gate",
		Decision:     decision.String(),
		OverallScore: critique.OverallScore,
		Verdict:      strings.TrimSpace(critique.Verdict),
		TopFixes:     trimNonEmptyStrings(critique.TopFixes, 8),
		Dimensions:   dimensions,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

func trimNonEmptyStrings(values []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	capacity := len(values)
	if capacity > limit {
		capacity = limit
	}
	out := make([]string, 0, capacity)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) >= limit {
			break
		}
	}
	return out
}

var artifactRevisionIDPattern = regexp.MustCompile(`(?im)^\s*-?\s*artifact_id\s*:\s*artifact-(\d+)\s*$`)
var artifactComparisonLatestIDPattern = regexp.MustCompile(`(?im)^\s*-?\s*latest_artifact_id\s*:\s*artifact-(\d+)\s*$`)
var assetCandidateKindPattern = regexp.MustCompile(`(?im)^\s*-?\s*asset[_ ]kind\s*:\s*(brand|character|product|director_style|prompt_asset|design_system|reference)\s*$`)
var comparisonExplicitDecisionPattern = regexp.MustCompile(`(?i)\b(?:recommendation|decision|recommended)\s*:?\s*(keep|revise|rollback)\b`)
var comparisonRollbackPattern = regexp.MustCompile(`\brollback\b`)
var comparisonRevisePattern = regexp.MustCompile(`\brevise\b`)
var comparisonKeepPattern = regexp.MustCompile(`\bkeep\b`)

func parseRevisionParentArtifactID(prompt string) uint {
	matches := artifactRevisionIDPattern.FindStringSubmatch(prompt)
	return parseArtifactIDMatch(matches)
}

func parseComparisonArtifactID(prompt string) uint {
	matches := artifactComparisonLatestIDPattern.FindStringSubmatch(prompt)
	return parseArtifactIDMatch(matches)
}

func parseAssetCandidateArtifactID(prompt string) uint {
	normalized := strings.ToLower(prompt)
	if !strings.Contains(normalized, "asset-library candidate") && !strings.Contains(normalized, "asset_kind") {
		return 0
	}
	matches := artifactRevisionIDPattern.FindStringSubmatch(prompt)
	return parseArtifactIDMatch(matches)
}

func parseComparisonSource(prompt string) string {
	if parseComparisonArtifactID(prompt) == 0 {
		return ""
	}
	normalized := strings.ToLower(prompt)
	if strings.Contains(normalized, "visual diff") {
		return "agent_visual_diff"
	}
	return "agent_compare"
}

func extractAssetCandidateInput(text string) (workagentService.ArtifactAssetCandidateInput, bool) {
	kindMatches := assetCandidateKindPattern.FindStringSubmatch(text)
	if len(kindMatches) != 2 {
		return workagentService.ArtifactAssetCandidateInput{}, false
	}
	assetKind := strings.ToLower(strings.TrimSpace(kindMatches[1]))
	profile := map[string]interface{}{
		"source":  "agent_asset_candidate",
		"summary": trimForProfile(text, 8000),
	}
	addProfileLine(profile, "intendedUse", extractLabeledLine(text, "intended use"))
	addProfileLine(profile, "visualGuidance", extractLabeledLine(text, "visual guidance"))
	addProfileLine(profile, "reusableConstraints", extractLabeledLine(text, "reusable constraints"))
	addProfileLine(profile, "negativeReuseNotes", extractLabeledLine(text, "negative reuse notes"))
	if assetKind == "design_system" {
		addProfileLine(profile, "designSystemMarkdown", extractFirstLabeledBlock(text,
			"designSystemMarkdown",
			"design system markdown",
			"design_system_markdown",
		))
	}

	name := extractLabeledLine(text, "name")
	slug := extractLabeledLine(text, "slug")
	return workagentService.ArtifactAssetCandidateInput{
		AssetKind: assetKind,
		Name:      name,
		Slug:      slug,
		Profile:   profile,
	}, true
}

func extractLabeledLine(text string, label string) string {
	pattern := regexp.MustCompile(`(?im)^\s*-?\s*` + regexp.QuoteMeta(label) + `\s*:\s*(.+?)\s*$`)
	matches := pattern.FindStringSubmatch(text)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func extractFirstLabeledBlock(text string, labels ...string) string {
	for _, label := range labels {
		if block := extractLabeledFencedBlock(text, label); block != "" {
			return block
		}
	}
	return ""
}

func extractLabeledFencedBlock(text string, label string) string {
	pattern := regexp.MustCompile(`(?ims)^\s*-?\s*` + regexp.QuoteMeta(label) + `\s*:\s*` + "```" + `(?:markdown|md)?\s*\n?(.*?)\n?` + "```")
	matches := pattern.FindStringSubmatch(text)
	if len(matches) != 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func addProfileLine(profile map[string]interface{}, key string, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		profile[key] = value
	}
}

func trimForProfile(text string, max int) string {
	text = strings.TrimSpace(text)
	if len(text) <= max {
		return text
	}
	return text[:max]
}

func parseArtifactIDMatch(matches []string) uint {
	if len(matches) != 2 {
		return 0
	}
	id, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return 0
	}
	return uint(id)
}

func extractComparisonDecision(text string) string {
	normalized := strings.ToLower(text)
	if matches := comparisonExplicitDecisionPattern.FindStringSubmatch(text); len(matches) == 2 {
		return strings.ToLower(matches[1])
	}
	switch {
	case comparisonRollbackPattern.MatchString(normalized):
		return "rollback"
	case comparisonRevisePattern.MatchString(normalized):
		return "revise"
	case comparisonKeepPattern.MatchString(normalized):
		return "keep"
	default:
		return ""
	}
}

func (cb *agentTurnCallbacks) captureArtifactComparison() {
	if cb.comparisonArtifactID == 0 || cb.chatThread == nil {
		return
	}
	summary := strings.TrimSpace(cb.accumulatedText)
	if summary == "" {
		return
	}
	if len(summary) > 8000 {
		summary = summary[:8000]
	}
	source := strings.TrimSpace(cb.comparisonSource)
	if source == "" {
		source = "agent_compare"
	}
	_, err := workagentService.NewArtifactRegistryRepository(nil).UpdateComparison(
		int(cb.uid),
		cb.chatThread.Id,
		cb.comparisonArtifactID,
		source,
		summary,
		extractComparisonDecision(summary),
	)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Agent API] artifact comparison capture skipped artifact=%d: %v", cb.comparisonArtifactID, err))
	}
}

func (cb *agentTurnCallbacks) captureArtifactAssetCandidate() {
	if cb.assetCandidateID == 0 || cb.chatThread == nil {
		return
	}
	input, ok := extractAssetCandidateInput(cb.accumulatedText)
	if !ok {
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		input.Name = fmt.Sprintf("Artifact %d %s", cb.assetCandidateID, strings.ReplaceAll(input.AssetKind, "_", " "))
	}
	if strings.TrimSpace(input.Slug) == "" {
		input.Slug = fmt.Sprintf("artifact-%d-%s", cb.assetCandidateID, strings.ReplaceAll(input.AssetKind, "_", "-"))
	}
	_, err := workagentService.NewArtifactAssetCandidateRepository(nil).UpsertForArtifact(
		int(cb.uid),
		cb.chatThread.Id,
		cb.assetCandidateID,
		input,
	)
	if err != nil {
		globals.Warn(fmt.Sprintf("[Agent API] asset candidate capture skipped artifact=%d: %v", cb.assetCandidateID, err))
	}
}
