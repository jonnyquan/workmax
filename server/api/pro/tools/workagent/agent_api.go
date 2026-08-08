package workagent

import (
	"fmt"
	"sync/atomic"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	accountService "server/service/account"
	"server/service/pricing"
	workagentService "server/service/tools/workagent"

	"github.com/gin-gonic/gin"
)

// Per-thread turn serialization lives in workagentService.GetAgentTurnLock
// (service/tools/workagent/agent_turn_lock.go). The contract is
// reject-not-queue: TryLock returns false → handler emits a `done`
// event with a busy subtype so the client can show an inline notice.
// Two concurrent POSTs on the same thread used to slip past
// idempotency when the X-Agent-Request-Id header was absent (the
// time.Now().UnixNano() fallback was distinct per request → two
// reservations finalized for one logical turn → user paid twice).
// Pre-2026-05 there were two parallel maps (this one and Canvas
// Agent's); they were consolidated as part of R3.

// agentMessageIDSeq disambiguates tempMessageIDs minted in the same
// nanosecond. UnixNano alone collides under burst load (two POSTs
// arriving within the same nanosecond on the same goroutine scheduler
// tick) and the SSE stream uses tempMessageID as the per-turn key on
// the frontend — collision means two parallel turns share a key and
// the second turn's blocks land in the first turn's row. Same pattern
// as 41a9f8bf (SSE conn ID) and 48051bd3 (test session ID).
var agentMessageIDSeq atomic.Uint64

// workagentIdempotencySeq does the same for the idempotency-key fallback
// minted when X-Agent-Request-Id is absent. UnixNano-only collision
// here has a billing blast radius: two requests with the same fallback
// key collapse to one reservation, and the credit reservation service
// short-circuits the second turn as "already processed" while still
// streaming the agent's reply — under-billing.
var workagentIdempotencySeq atomic.Uint64

// HandleAgentChat handles Agent mode chat
// This follows SDK's native Message/ContentBlock design
func (api *AIChatApiNew) HandleAgentChat(c *gin.Context) {
	startTime := time.Now()

	// Validation + SSE setup + tempMessageID mint moved into prepareTurn
	// so HandleAgentChat's main body stays focused on billing → SDK
	// turn → persist. Early-return paths inside prepareTurn already
	// wrote the JSON response and we just propagate the bail.
	prep, ok := api.prepareTurn(c)
	if !ok {
		return
	}
	uid := prep.uid
	chatRequest := prep.chatRequest
	threadID := prep.threadID
	chatThread := prep.chatThread
	userMessage := prep.userMessage
	flusher := prep.flusher
	tempMessageID := prep.tempMessageID

	// B5 Slice 2a — shared SSE emitter. Two routing paths:
	//   - registered SSEConnection (post-registerSSEConnection):
	//     WriteChunk bumps LastActivity so the stale-connection
	//     reaper doesn't kill long-running PPT generations.
	//   - pre-registration fallback: raw writer + Flusher under the
	//     emitter's internal mutex.
	// The emitter's bool return is discarded here — work-agent's
	// callback path doesn't gate on individual write success;
	// canvas's heartbeat path consumes the bool to short-circuit.
	emitter := workagentService.NewSSEEmitter(c.Writer, flusher, "[Agent API]")
	sendEvent := func(event workagentModel.AgentSSEEvent) {
		emitter.Send(event)
	}

	// Per-thread serialization. TryLock semantics rejects a second
	// concurrent send immediately rather than queuing it behind a
	// 10-minute turn (with time.Now() fallback idempotency keys,
	// both reservations could finalize and double-charge). B5
	// Slice 1 extracted the TryLock + THREAD_BUSY envelope into
	// shared workagentService helpers so the canvas surface uses
	// the same primitives.
	turnLock, locked := workagentService.TryAcquireTurnLock(threadID)
	if !locked {
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result: workagentService.BuildThreadBusyPayload(
				"Another message is currently being processed for this conversation. Please wait for it to finish.",
			),
		})
		return
	}
	defer turnLock.Release()

	// Get Agent client
	agentManager := workagentService.GetAgentClientManager()
	if !agentManager.IsEnabled() {
		globals.Error("[Agent] AgentManager is not enabled")
		recordAgentError(chatThread)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    errorResultPayload(),
		})
		return
	}
	globals.Info("[Agent] AgentManager is enabled")
	configuredTimeout := 0
	if cfg := agentManager.GetConfig(); cfg != nil {
		configuredTimeout = cfg.Timeout
	}
	// Freeze one timeout snapshot before Reserve. The same duration drives the
	// SDK context below; the financial hold adds a five-minute persistence and
	// settlement margin so a valid long-running turn cannot be swept at 10m.
	agentTimeout := workagentService.ResolveAgentExecutionTimeout(configuredTimeout, workAgentDefaultTimeout)
	reservationTTL := workagentService.ReservationTTLForExecution(agentTimeout)

	// Workspace prep + file resolve + mode/tier persist + premium gate.
	// Pulled into setupTurnEnvironment so this 4-concern block stops
	// hiding in the middle of the handler. ok=false means an SSE error
	// has already been written. This deliberately runs before credit
	// reservation so attachment pricing is based on the actual uid-scoped
	// files the agent will see, not on untrusted request count.
	env, ok := api.setupTurnEnvironment(setupTurnEnvironmentInput{
		uid:           uid,
		chatThread:    chatThread,
		chatRequest:   chatRequest,
		agentManager:  agentManager,
		tempMessageID: tempMessageID,
		sendEvent:     sendEvent,
	})
	if !ok {
		return
	}
	threadWorkspace := env.threadWorkspace
	workspaceRootPath := env.workspaceRootPath
	agentFiles := env.agentFiles
	agentMode := env.agentMode
	modelTier := env.modelTier
	customPrompt := env.customPrompt

	// Reserve credits after file resolution. Missing/cross-tenant file IDs
	// are dropped before this point, so attachment pricing now matches the
	// real agentFiles payload instead of overcharging for skipped files.
	creditCost := pricing.AgentCost(len(agentFiles))
	reservationID, idempotencyKey, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:            c,
		uid:            uid,
		chatThread:     chatThread,
		creditCost:     creditCost,
		reservationTTL: reservationTTL,
		tempMessageID:  tempMessageID,
		sendEvent:      sendEvent,
	})
	if !ok {
		return
	}

	db := globals.GraDBs["system"]
	reservationSvc := accountService.NewCreditReservationService()

	// Finalize on success; release on any failure path. shouldFinalize drives it.
	// Both closures share an internal settled flag — see the
	// workagentService.ReservationClosures contract for why.
	releaseReservation, finalizeReservation := workagentService.ReservationClosures(
		db, reservationSvc, reservationID, creditCost, int(uid), "[Agent]",
	)

	// usedCredits starts at the reservation ceiling — that's what
	// the caller gets billed if the SDK doesn't surface a
	// total_cost_usd actual (some upstream gateways omit it).
	// After the SDK returns, the post-turn extractor below updates
	// this to the actual credit cost derived from total_cost_usd
	// (see pricing.AgentCostFromUSD); the deferred finalize then
	// charges the actual + refunds the delta via the reservation
	// service's partial-finalize path.
	shouldFinalize := true
	usedCredits := creditCost
	defer func() {
		if shouldFinalize {
			finalizeReservation(usedCredits)
		} else {
			releaseReservation("agent_failed")
		}
	}()

	// Panic refund. shouldFinalize defaults to true and the defer above
	// runs even after a panic (gin.Recovery() lets deferred functions
	// fire before responding). Without this guard, any crash between
	// reservation and the success path — JSON marshal in OnDone, a nil
	// deref in workagentService.SanitizeAgentConversation, an SDK panic — finalizes the
	// reservation: the user pays for a turn that never produced a
	// message. Flip the flag, release, then re-panic so gin.Recovery
	// still logs and 500s the request as before.
	defer func() {
		if r := recover(); r != nil {
			shouldFinalize = false
			releaseReservation("panic")
			panic(r)
		}
	}()

	globals.Info(fmt.Sprintf("[Agent API] Using mode: %s, modelTier: %s", agentMode, modelTier))

	// M1 turn-1 question_form short-circuit. When the feature flag is
	// on AND this is the first turn AND the skill declares a form,
	// we emit a question_form block + done(form_emitted) directly
	// without invoking the SDK. The user picks radios, the next turn
	// arrives carrying the answers, normal SDK flow resumes from
	// turn 2 onwards.
	//
	// Released credit reservation: form emission costs zero tokens,
	// so charging is wrong. shouldFinalize=false flips the defer to
	// release.
	//
	// Platform context: ProjectDocs/writer-work-agent-plugin-platform-design-2026-08.md.
	// G1+G2 (2026-05-17) — build the skill bundle once and share it
	// between M1 (question form) and M2 (directions picker)
	// dispatchers. With the loader's per-skill identity+body caches
	// (G1) the underlying Build is mostly cache hits, but the
	// framework renderFrameworkWithSlots call is ctx-aware and runs
	// per Build. Sharing one pointer here saves that render across
	// both dispatchers. Nil-bundle fallback inside each dispatcher
	// keeps test-only call paths working.
	prebuiltBundle, prebuiltErr := loadSkillBundleForFormDispatch(agentMode)
	if prebuiltErr != nil {
		globals.Warn(fmt.Sprintf("[Agent API] skill bundle pre-build skipped (mode=%s): %v", agentMode, prebuiltErr))
	}

	if dispatchedForm := api.maybeEmitQuestionForm(maybeEmitQuestionFormInput{
		uid:           uid,
		chatThread:    chatThread,
		chatRequest:   chatRequest,
		userMessage:   userMessage,
		agentMode:     agentMode,
		tempMessageID: tempMessageID,
		sendEvent:     sendEvent,
		bundle:        prebuiltBundle,
	}); dispatchedForm {
		shouldFinalize = false
		return
	}

	// M2 directions picker fallback. Triggers AFTER the question
	// form (the form is more authoritative when it fires) but BEFORE
	// the SDK call, on the same shouldFinalize=false / 0-token
	// short-circuit. Conditions: skill declares directions_fallback,
	// feature flag on, no brand context in thread, no prior
	// direction selection. See directions_picker_dispatcher.go.
	if dispatchedPicker := api.maybeEmitDirectionsPicker(maybeEmitDirectionsPickerInput{
		uid:           uid,
		chatThread:    chatThread,
		chatRequest:   chatRequest,
		agentMode:     agentMode,
		tempMessageID: tempMessageID,
		sendEvent:     sendEvent,
		bundle:        prebuiltBundle, // same pointer as M1 — G2 share
	}); dispatchedPicker {
		shouldFinalize = false
		return
	}

	// @-mention expansion (B1, 2026-05-16). User text may contain
	// `@brand/<slug>` / `@character/<slug>` / `@product/<slug>` /
	// `@director/<slug>` tokens; resolve each to the asset's Name
	// before the model sees the message. Unresolved slugs fall
	// through unchanged — the model treats them like any other
	// unfamiliar token. Runs once at ingest; the agent's own tool
	// outputs are not re-expanded (canvas owns mention expansion
	// at the tool-input boundary).
	effectiveUserMessage := workagentService.ExpandMentionsForChat(c.Request.Context(), uid, userMessage)
	if effectiveUserMessage != userMessage {
		globals.Info(fmt.Sprintf("[Agent API] @-mentions expanded: %d → %d chars",
			len(userMessage), len(effectiveUserMessage)))
	}
	if agentMode == "ppt" {
		effectiveUserMessage = buildPPTUserPrompt(effectiveUserMessage, chatRequest.Metadata, chatRequest.Files)
		if effectiveUserMessage != userMessage {
			globals.Info(fmt.Sprintf("[Agent API] PPT prompt enriched: original=%d chars, effective=%d chars",
				len(userMessage), len(effectiveUserMessage)))
		}
	}

	// SDK callbacks — OnMessage / OnBlock / OnDone bodies live on
	// agentTurnCallbacks. The struct holds the read-only inputs the
	// callbacks close over PLUS two outbound signals the caller reads
	// after the SDK returns: generatedFilesPayload (used by the
	// success path's attach-and-persist) and pptMissingOutput (the
	// "ppt mode produced no .pptx" flag that demotes shouldFinalize).
	cbs := &agentTurnCallbacks{
		api:                  api,
		ctx:                  c,
		chatThread:           chatThread,
		chatRequest:          chatRequest,
		workspaceRootPath:    workspaceRootPath,
		threadWorkspace:      threadWorkspace,
		agentMode:            agentMode,
		modelTier:            modelTier, // used by M3 critique sub-agent for account selection
		userMessage:          userMessage,
		revisionParentID:     parseRevisionParentArtifactID(userMessage),
		comparisonArtifactID: parseComparisonArtifactID(userMessage),
		comparisonSource:     parseComparisonSource(userMessage),
		assetCandidateID:     parseAssetCandidateArtifactID(userMessage),
		tempMessageID:        tempMessageID,
		uid:                  uid,
		executionStartTime:   time.Now(), // record before SDK to filter only newly generated files
		sendEvent:            sendEvent,
	}
	globals.Info(fmt.Sprintf("[Agent] Execution start time: %s", cbs.executionStartTime.Format("2006-01-02 15:04:05.000")))
	callbacks := cbs.toAgentCallbacks()

	// Detach the SDK's context from gin's request context so a long-
	// running agent turn isn't killed by Gin's WriteTimeout, but DO
	// propagate client-disconnect → cancel via a watcher goroutine.
	// Without that watcher, if the user closes the tab mid-turn the
	// SDK keeps running (and billing) on the now-disconnected stream.
	// setupAgentContext returns a single cleanup() that cancels and
	// waits for the watcher to exit; defer it once.
	agentCtx, agentTimeout, cleanupAgentContext := setupAgentContext(c, uid, threadID, chatThread.Id, chatThread.ProjectID, agentTimeout)
	cbs.runtimeCtx = agentCtx
	defer cleanupAgentContext()

	// Register with the global SSE connection manager so we enforce
	// concurrency caps (per-user + global) and can clean up stale
	// connections in the cleanup ticker. registerSSEConnection
	// surfaces SSE_LIMIT / SSE_LIMIT_PER_USER as discriminated SSE
	// done events; on success returns a cleanup func + the connection
	// itself, which we route every subsequent write through (so
	// LastActivity bumps with each chunk and the cleanup ticker
	// doesn't reap a long-running PPT turn mid-stream).
	sseConn, unregisterSSE, ok := registerSSEConnection(c, uid, threadID, agentTimeout, tempMessageID, sendEvent)
	if !ok {
		shouldFinalize = false
		return
	}
	defer unregisterSSE()
	emitter.SetConnection(sseConn)

	globals.Info(fmt.Sprintf("[Agent] Starting with timeout: %v", agentTimeout))

	// Account selection + per-request client construction. Two
	// failure paths (no account / build error) both surface as the
	// generic SSE error done event with shouldFinalize=false. Pulled
	// into selectAndBuildAgentClient so HandleAgentChat stops carrying
	// 30 lines of two-strikes boilerplate.
	selectedAccount, agentClient, ok := api.selectAndBuildAgentClient(selectAndBuildAgentClientInput{
		creditCost:    creditCost,
		modelTier:     modelTier,
		agentMode:     agentMode,
		customPrompt:  customPrompt,
		agentManager:  agentManager,
		chatThread:    chatThread,
		tempMessageID: tempMessageID,
		sendEvent:     sendEvent,
	})
	if !ok {
		shouldFinalize = false
		return
	}

	// PR-9b/1 + PR-6b: build the SystemAdditions tail before SDK
	// invocation. Includes M5 ChecklistDigest (when gate flag on)
	// and M1 DiscoveryContext (when discovery_form_result rows
	// exist for this thread — covers both NLP-inferred and form-
	// submitted answers). Empty string when neither layer fires.
	var knowledgeMetadata workagentService.KnowledgeRetrievalMetadata
	systemAdditions := workagentService.BuildPreflightAdditionsForThreadWithOptions(
		uid, agentMode, uint(chatThread.Id),
		workagentService.PreflightOptions{
			PlanMode:          chatRequest.Metadata.PlanMode,
			UserMessage:       effectiveUserMessage,
			ProjectID:         chatThread.ProjectID,
			KnowledgeMetadata: &knowledgeMetadata,
		},
	)
	cbs.knowledgeMetadata = knowledgeMetadata

	// PR-9b/2b: gate-driven SDK redo loop. The first iteration is
	// the user's original turn; subsequent iterations only fire when
	// the post-emit checklist gate (runChecklistGate, OnDone) returns
	// a Block decision AND the auto-redo flag is enabled for this
	// (uid, skill) pair. Each redo continues the SAME SDK session so
	// the model has full context, and feeds FormatBlockFindings as
	// the next "user" turn. Capped at maxGateRedos to bound token
	// cost; on third Block we keep the last attempt and let the
	// frontend ChecklistGateCard show the verdict.
	const maxGateRedos = 2
	currentPrompt := effectiveUserMessage
	currentSession := chatThread.AgentSessionID

	var conversation *workagentModel.AgentConversation
	var newSessionID string
	var err error
	for attempt := 0; attempt <= maxGateRedos; attempt++ {
		if attempt > 0 {
			cbs.resetForRedo()
			globals.Info(fmt.Sprintf("[Agent] Gate redo attempt=%d/%d uid=%d skill=%s thread=%s",
				attempt, maxGateRedos, uid, agentMode, threadID))
		}

		conversation, newSessionID, err = agentClient.ProcessAgentConversationWithRetry(
			agentCtx, // Use independent context with agent-specific timeout
			currentPrompt,
			threadID,
			currentSession,
			threadWorkspace,
			agentFiles, // Pass file information to SDK
			callbacks,
			agentMode,    // Pass agent mode to use mode-specific prompt
			customPrompt, // Pass custom prompt from thread settings
			selectedAccount,
			systemAdditions, // PR-9b/1: pre-flight checklist + future Sprint-B layers
		)
		if err != nil {
			break
		}

		// Exit conditions (any one ends the loop):
		//   - gate didn't block (pass/warn/disabled) → emit + done
		//   - blocking gate's auto-redo flag is disabled → emit + done
		//   - context cancelled → emit + done (don't burn another turn)
		//   - last attempt → keep the result; banner shows the verdict
		if !cbs.gateBlocked || !cbs.gateAutoRedoAllowed || agentCtx.Err() != nil {
			break
		}
		if attempt >= maxGateRedos {
			globals.Warn(fmt.Sprintf("[Agent] Gate redo exhausted after %d attempts uid=%d skill=%s thread=%s",
				maxGateRedos+1, uid, agentMode, threadID))
			break
		}

		// Carry the redo prompt + continue the same SDK session so
		// the regeneration sees the prior turn in conversation history.
		currentPrompt = cbs.gateRedoPrompt
		currentSession = newSessionID
	}

	// Cost-drift observability + token-based settle. The reservation
	// stamped the ceiling (creditCost = pricing.AgentCost based on
	// file count). The SDK reports the upstream gateway's actual USD
	// cost on the result frame; convert to credits via
	// pricing.AgentCostFromUSD and stash in usedCredits so the
	// deferred finalize charges actuals (the reservation service's
	// partial-finalize path refunds the delta).
	//
	// Fires regardless of finalize/release outcome (a cancelled turn
	// can still have a partial cost the gateway billed) and
	// regardless of whether actual was reported (some gateways don't
	// surface total_cost_usd; the absent-actual case logs at Debug
	// and we charge the ceiling).
	if conversation != nil {
		if actualUSD, ok := extractTotalCostUSDFromResult(conversation.Result); ok {
			actualCredits := pricing.AgentCostFromUSD(*actualUSD)
			// Clamp to the reservation ceiling — Finalize rejects
			// used > reserved with an error. An outlier turn that
			// burns more than the predictive ceiling gets capped at
			// what we reserved; the loss-at-margin is intentional
			// (the predictive ceiling is the user's contract).
			if actualCredits > creditCost {
				actualCredits = creditCost
			}
			usedCredits = actualCredits
			globals.Info(fmt.Sprintf("[Agent] Cost settle: ceiling=%d_credits actual=%d_credits (%.6f_USD) uid=%d session=%s",
				creditCost, usedCredits, *actualUSD, uid, newSessionID))
		} else {
			globals.Debug(fmt.Sprintf("[Agent] Cost settle: ceiling=%d_credits actual=<not_reported,charging_ceiling> uid=%d session=%s",
				creditCost, uid, newSessionID))
		}
	}

	// Two SDK-result error gates: transport error (err != nil) and
	// in-band error (conversation.Result has is_error=true, e.g. GLM
	// subscription expired). Both must bail BEFORE persist so we don't
	// charge or leave a STATUS_SUCCESS row for a turn the user
	// couldn't actually use. Pulled into handlePostSDKErrors so this
	// boilerplate stops repeating.
	if !api.handlePostSDKErrors(handlePostSDKErrorsInput{
		sdkErr:        err,
		conversation:  conversation,
		uid:           uid,
		threadID:      threadID,
		chatThread:    chatThread,
		tempMessageID: tempMessageID,
		sendEvent:     sendEvent,
	}) {
		shouldFinalize = false
		return
	}

	persistAgentSessionID(chatThread, newSessionID)

	// Hoist OnDone's outbound signals out of the callbacks struct.
	// generatedFilesPayload feeds the conversation attach below;
	// pptMissingOutput demotes shouldFinalize so the user isn't
	// billed for a ppt-mode turn that produced no .pptx (the SSE
	// done event already shipped a "missing_output" warning).
	generatedFilesPayload := cbs.generatedFilesPayload
	if cbs.pptMissingOutput {
		shouldFinalize = false
	}

	// Sanitize conversation paths before persisting
	if conversation != nil && len(generatedFilesPayload) > 0 {
		conversation.GeneratedFiles = generatedFilesPayload
		conversation.GeneratedFilesV2 = generatedFilesPayload
		conversation.Result = attachGeneratedFilesToResult(conversation.Result, generatedFilesPayload)
	}
	if conversation != nil {
		conversation.Result = attachRAGMetadataToResult(conversation.Result, knowledgeMetadata)
	}

	// Tail of the chatTurn: sanitize → save → thread name → usage.
	// shouldFinalize signals the outer billing defer (line ~305): true
	// on success → finalize the reservation; false on save failure →
	// release. Pulled out into persistAgentTurn so the 720-line
	// HandleAgentChat closure stops carrying the persistence flow as
	// inline state.
	//
	// AND-combine to PRESERVE shouldFinalize=false signals from
	// upstream — OnDone flips it to false on a ppt-mode turn that
	// produced no .pptx output (the user gets a "missing_output"
	// warning, must not be billed). A naive `shouldFinalize =
	// api.persistAgentTurn(...)` would let a successful save lift
	// the flag back to true and bill the user for a turn the
	// frontend already rendered as "needs more info." Save failures
	// (return false) still demote correctly because false && X = false.
	persistedOK := api.persistAgentTurn(persistAgentTurnInput{
		ctx:                 c,
		uid:                 uid,
		accountID:           selectedAccount.ID,
		creditCost:          creditCost,
		usedCredits:         usedCredits,
		threadID:            threadID,
		chatThread:          chatThread,
		conversation:        conversation,
		workspaceRootPath:   workspaceRootPath,
		userMessage:         userMessage,
		modelTier:           modelTier,
		agentMode:           agentMode,
		idempotencyKey:      idempotencyKey,
		generatedFilesCount: len(generatedFilesPayload),
		startTime:           startTime,
	})
	shouldFinalize = shouldFinalize && persistedOK
}

// File layout note (B1 split): HandleAgentChat above is the entry
// point for the agent-chat turn. The pieces it composes live in
// peer files in this package:
//
//   - agent_turn_phases.go     — prepareTurn, setupTurnEnvironment,
//                                reserveCreditsForTurn,
//                                selectAndBuildAgentClient,
//                                setupAgentContext,
//                                registerSSEConnection,
//                                handlePostSDKErrors,
//                                persistAgentTurn (+ Input structs).
//   - agent_turn_callbacks.go  — agentTurnCallbacks struct + the
//                                SDK callback methods (OnMessage,
//                                OnBlock, OnDone, runSkillValidators).
//   - agent_persistence.go     — recordAgentError, saveAgentConversation,
//                                updateThreadName, AIGenerateThreadName.
//   - agent_result_helpers.go  — pure utility helpers for
//                                extracting / sanitising / attaching
//                                fields on AgentConversation +
//                                Result JSON.
//   - agent_thread_lookup.go   — getChatThread + chatThreadSingleflight.
//   - agent_file_helpers.go    — getFileByID / getFilesByIDs / FromDB
//                                shims over FileService.
//   - agent_other_endpoints.go — GetSSEConnectionStats, DeleteMessage.
//
// Historical note: a prior resolveWorkspaceFilePath helper that
// lived in this file accepted absolute / escape-relative paths
// (CWE-22 path traversal) — deleted; callers must go through
// workagentService.ResolveInsideWorkspace so a malicious request
// can't pull /etc/passwd into a prompt or symlink it into the
// workspace.
