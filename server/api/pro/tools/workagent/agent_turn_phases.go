package workagent

// agent_turn_phases.go owns the per-phase functions that
// HandleAgentChat orchestrates in sequence: prepareTurn (parse +
// auth + SSE bootstrap), setupTurnEnvironment (workspace + files +
// mode/tier + premium gate), reserveCreditsForTurn (billing
// reservation), selectAndBuildAgentClient (account pool pick +
// per-request client), setupAgentContext (timeout + client-cancel
// watcher), registerSSEConnection (per-user/global concurrency cap),
// handlePostSDKErrors (error gating after the SDK returns), and
// persistAgentTurn (final save + usage recording).
//
// Pulled out of agent_api.go (B1 chunk 3) so the giant HandleAgentChat
// handler doesn't share a file with its phase machinery. Each
// function takes a small input-struct (the *Input types below) and
// returns either (*output, true) on success or (nil, false) when an
// SSE/HTTP error has already been emitted — the caller bails on
// false. Same package as agent_api.go; all call sites work
// unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"server/globals"
	workagentModel "server/model/workagent"
	accountService "server/service/account"
	projectService "server/service/project"
	workagentService "server/service/tools/workagent"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	workAgentChatMaxBodyBytes     = 2 * 1024 * 1024
	workAgentSmallJSONMaxBytes    = 256 * 1024
	workAgentChatMaxMessages      = 20
	workAgentChatMaxMessageBytes  = 64 * 1024
	workAgentChatMaxFiles         = 30
	workAgentChatMaxMetadataBytes = 64 * 1024
	workAgentDefaultTimeout       = 30 * time.Minute
)

var (
	errAgentTurnTerminal   = errors.New("agent turn is already terminal")
	errAgentTurnInProgress = errors.New("agent turn is already in progress")
)

// registerSSEConnection allocates a tracked SSE connection for this
// turn and registers it with the global manager. The manager enforces
// per-user and global concurrency caps and reaps stale connections.
//
// On success returns:
//   - sseConn: pass it to trackedSSEConn so subsequent sendEvent
//     writes route through SSEConnection.WriteChunk (bumps
//     LastActivity, mutex-serializes vs. SDK callback writes).
//   - unregister: caller defers this once. Removes the registration
//     so a subsequent same-thread turn can register a fresh one
//     without hitting the per-user limit.
//   - ok: true when the manager accepted the connection.
//
// Two failure paths surface as distinct error codes so the frontend
// can render "you have too many tabs open" vs. "server is busy"
// accurately:
//
//   - ErrPerUserSSELimit → SSE_LIMIT_PER_USER (user has hit the
//     per-uid concurrency cap; closing other tabs helps).
//   - any other error → SSE_LIMIT (global cap or transient manager
//     error; closing tabs probably doesn't help).
//
// On failure the helper closes the orphaned sseConn (Register failed
// → nothing in the manager to Unregister, but the conn struct holds
// a writer mutex + state we want freed). Caller bails after seeing
// ok=false.
func registerSSEConnection(c *gin.Context, uid uint, threadID string, agentTimeout time.Duration, tempMessageID string, sendEvent func(workagentModel.AgentSSEEvent)) (*workagentService.SSEConnection, func(), bool) {
	sseConn := workagentService.NewSSEConnection(c, uid, threadID, agentTimeout)
	manager := workagentService.GetGlobalSSEManager()
	if err := manager.Register(sseConn); err != nil {
		var userMessage, errorCode string
		if errors.Is(err, workagentService.ErrPerUserSSELimit) {
			globals.Warn(fmt.Sprintf("[Agent] Per-user SSE limit hit for uid %d", uid))
			userMessage = "You have too many active sessions. Please close some and try again."
			errorCode = "SSE_LIMIT_PER_USER"
		} else {
			globals.Warn(fmt.Sprintf("[Agent] SSE connection limit reached: %v", err))
			userMessage = "Server is busy, please retry shortly."
			errorCode = "SSE_LIMIT"
		}
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    markResultAsError(errorResultPayload(), "error", userMessage, errorCode),
		})
		sseConn.Close()
		return nil, nil, false
	}

	unregister := func() {
		manager.Unregister(sseConn.ID)
	}
	return sseConn, unregister, true
}

// handlePostSDKErrorsInput bundles what handlePostSDKErrors needs to
// inspect the SDK call's outcome and respond. Read-only.
type handlePostSDKErrorsInput struct {
	sdkErr        error
	conversation  *workagentModel.AgentConversation
	uid           uint
	threadID      string
	chatThread    *workagentModel.ChatThread
	tempMessageID string
	sendEvent     func(workagentModel.AgentSSEEvent)
}

// handlePostSDKErrors gates whether HandleAgentChat continues to the
// persist-and-finalize step after the SDK call returns. There are two
// failure modes the SDK can produce:
//
//   - Transport-level error (sdkErr != nil): network blew up, the
//     CLI subprocess died, etc. Generic SSE error to the client.
//   - In-band error: the SDK call returns success but
//     conversation.Result has is_error=true (e.g. GLM subscription
//     expired, model rejected the request, validation tripped at
//     the upstream account level). The helper returns the inner
//     error message so we can route an admin email about it via
//     NotifyAgentError.
//
// Both surface as "this turn must not be billed AND must not be
// persisted as a successful conversation." Returns true when the
// turn looks healthy and the caller should continue to persistence;
// false when caller must flip shouldFinalize and bail.
//
// The transport-error path also writes the SSE done event itself
// (so the client unblocks); the in-band path doesn't, because the
// SDK already streamed a done event with is_error=true via OnDone
// — sending another one would duplicate.
func (api *AIChatApiNew) handlePostSDKErrors(in handlePostSDKErrorsInput) bool {
	if in.sdkErr != nil {
		globals.Error(fmt.Sprintf("[Agent API] Error: %v", in.sdkErr))
		recordAgentError(in.chatThread)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    errorResultPayload(),
		})
		return false
	}

	// In-band error result (e.g. GLM subscription expired): the SDK
	// returns success but conversation.Result has is_error=true. Bail
	// before save/usage so we don't charge or leave a STATUS_SUCCESS
	// row for a turn the user couldn't actually use. The helper
	// returns the inner result string so we can route an admin email
	// about it. We do NOT emit another SSE done — OnDone already
	// shipped one with the inner error payload.
	if errMsg, isErr := conversationInBandError(in.conversation); isErr {
		recordAgentError(in.chatThread)
		workagentService.NotifyAgentError(
			context.Background(),
			fmt.Errorf("%s", errMsg),
			in.threadID,
			fmt.Sprintf("%d", in.uid),
			in.chatThread.AgentSessionID,
		)
		return false
	}

	return true
}

// setupAgentContext builds the context the SDK call runs under. The
// context is intentionally detached from gin's request context so a
// long-running turn (PPT generations can run 5-30 minutes) doesn't
// trip Gin's WriteTimeout — but client-disconnect → cancel is still
// propagated via a watcher goroutine, so closing the browser tab
// stops the SDK and the billing without waiting for the timeout.
//
// Returns:
//   - agentCtx: the context to pass to the SDK call. Carries the
//     uid via WithUserID for downstream error reporting + the
//     numeric thread row id via WithThreadID for the production-
//     tools SDK bridge (Phase 2c) to stamp chat_message.metadata
//     against the right row.
//   - agentTimeout: the resolved timeout, also passed to the SSE
//     connection registration so its stale-connection reaper
//     matches the SDK's deadline.
//   - cleanup: defer this once. Cancels the context AND blocks until
//     the watcher goroutine has exited, so the defer chain can't
//     leak the goroutine past the handler return.
//
// The watcher's two-channel select is a best-effort cancellation:
// either the client disconnects first (cancel + log) or the agent
// finishes first (no-op). Cleanup's cancel() is idempotent, so
// calling it from the success path (after the SDK returned) is
// harmless — the watcher just sees agentCtx.Done() and exits.
func setupAgentContext(c *gin.Context, uid uint, threadID string, threadDBID uint, projectID uint, agentTimeout time.Duration) (context.Context, time.Duration, func()) {
	if agentTimeout <= 0 {
		agentTimeout = workAgentDefaultTimeout
	}

	baseCtx := workagentService.WithUserID(context.Background(), uid)
	baseCtx = workagentService.WithThreadID(baseCtx, threadDBID)
	// Plan-A Phase A3: stamp the thread's bound project on ctx so
	// project-aware production tools (lookup_asset and future
	// helpers) auto-scope without the agent having to repeat the
	// id on every call. projectID=0 is a valid stamp — downstream
	// treats it as "no project scope" same as a missing key.
	baseCtx = workagentService.WithProjectID(baseCtx, projectID)
	agentCtx, agentCancel := context.WithTimeout(baseCtx, agentTimeout)

	clientGone := c.Request.Context().Done()
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-clientGone:
			globals.Info(fmt.Sprintf("[Agent] Client disconnected, cancelling agent context (thread=%s)", threadID))
			agentCancel()
		case <-agentCtx.Done():
			// Agent finished (success, error, or timeout) — nothing to do.
		}
	}()

	cleanup := func() {
		agentCancel()
		<-watcherDone
	}
	return agentCtx, agentTimeout, cleanup
}

// selectAndBuildAgentClientInput bundles the inputs of the per-turn
// account-pick + client-build step. Read-only — the method doesn't
// mutate any of these.
type selectAndBuildAgentClientInput struct {
	creditCost    int
	modelTier     string
	agentMode     string
	customPrompt  string
	agentManager  *workagentService.AgentClientManager
	chatThread    *workagentModel.ChatThread
	tempMessageID string
	sendEvent     func(workagentModel.AgentSSEEvent)
}

// selectAndBuildAgentClient picks an account from the pool for the
// given model tier and builds a per-request ClaudeAgentGoClient with
// the agent mode + custom prompt baked in. Per-request (not shared)
// so two concurrent turns can't stomp each other's account/mode via
// the singleton client. Returns (account, client, true) on success.
//
// Two failure paths:
//
//   - GetAccountForModelTier returns an error (no eligible account
//     for the tier — typically all accounts in the pool tripped the
//     circuit breaker).
//   - BuildClientForAccount returns an error (CLI not installed,
//     env construction failed, etc.).
//
// Both surface as the generic errorResultPayload() SSE done event
// with ok=false. The caller flips shouldFinalize and bails. We
// deliberately don't differentiate "pool empty" vs "build failed" on
// the wire — the user-facing recovery is the same (retry shortly)
// and the diagnostic detail goes to the server log via globals.Error.
func (api *AIChatApiNew) selectAndBuildAgentClient(in selectAndBuildAgentClientInput) (*workagentModel.AgentAccount, *workagentService.ClaudeAgentGoClient, bool) {
	bailWithError := func() (*workagentModel.AgentAccount, *workagentService.ClaudeAgentGoClient, bool) {
		recordAgentError(in.chatThread)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    errorResultPayload(),
		})
		return nil, nil, false
	}

	accountPool := workagentService.GetAgentAccountPool()
	selectedAccount, err := accountPool.GetAccountForModelTier(in.modelTier, in.creditCost)
	if err != nil {
		globals.Error(fmt.Sprintf("[Agent API] No account available for tier '%s': %v", in.modelTier, err))
		return bailWithError()
	}

	agentClient, _, err := in.agentManager.BuildClientForAccount(selectedAccount)
	if err != nil {
		globals.Error(fmt.Sprintf("[Agent API] Failed to build agent client for account %d (%s): %v",
			selectedAccount.ID, selectedAccount.Name, err))
		return bailWithError()
	}

	return selectedAccount, agentClient, true
}

// turnEnvironment carries the per-turn environment HandleAgentChat
// needs once workspace/file/mode/tier resolution is done. All fields
// are inputs to the SDK call and the persistence path; none of them
// flow back out of HandleAgentChat.
type turnEnvironment struct {
	threadWorkspace   string
	workspaceRootPath string
	agentFiles        []workagentService.AgentFileInfo
	agentMode         string
	modelTier         string
	customPrompt      string
}

// setupTurnEnvironmentInput bundles what setupTurnEnvironment reads.
// chatThread is the only mutated input — when an agent-mode or
// model-tier change is persisted, the in-memory chatThread fields
// are updated in place so downstream code sees the new values
// without a re-fetch. That's deliberate; the alternative (return
// the updated thread back) duplicates the data.
type setupTurnEnvironmentInput struct {
	uid           uint
	chatThread    *workagentModel.ChatThread
	chatRequest   ChatStreamRequest
	agentManager  *workagentService.AgentClientManager
	tempMessageID string
	sendEvent     func(workagentModel.AgentSSEEvent)
}

// setupTurnEnvironment runs the four pre-SDK concerns in order:
//
//  1. EnsureThreadWorkspace — create the per-thread workspace dir.
//  2. resolveAgentInputFiles — three-tier path resolve with uid scope.
//  3. agentMode + modelTier determination + persistence (with cache
//     invalidation via ThreadRepository) when they differ from the
//     thread's stored values.
//  4. Premium-tier gate — work-plus requires a paid membership.
//
// Each of the three failure paths (workspace create, file resolve,
// premium denied) writes an SSE done event and returns ok=false.
// The mode/tier persistence path is best-effort (a Warn-and-continue
// log on DB failure) — those columns are advisory; the worst case is
// the next turn re-prompts the user to switch.
//
// chatThread is mutated in place when mode or tier changes successfully
// persist — downstream code reads those fields directly so the
// in-memory copy must reflect what's on disk.
//
// Returns (env, true) on full success. (nil, false) means an SSE
// error has already been emitted; caller flips shouldFinalize and
// returns.
func (api *AIChatApiNew) setupTurnEnvironment(in setupTurnEnvironmentInput) (*turnEnvironment, bool) {
	bailWithError := func(payload json.RawMessage) (*turnEnvironment, bool) {
		recordAgentError(in.chatThread)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    payload,
		})
		return nil, false
	}

	threadWorkspace, err := in.agentManager.EnsureThreadWorkspace(int(in.uid), in.chatThread.UUID)
	if err != nil {
		globals.Error(fmt.Sprintf("[Agent] Failed to prepare workspace: %v", err))
		return bailWithError(errorResultPayload())
	}

	workspaceRootPath := in.agentManager.WorkspaceRoot()

	// resolveAgentInputFiles: three-tier source-of-truth fallback
	// (Path → DB row scoped by uid → FilePath) with CWE-22 / CWE-639
	// guards inside. A non-nil err means the model would have seen
	// zero attachments — abort the turn rather than charging for a
	// useless run.
	agentFiles, fileErr := resolveAgentInputFiles(in.chatRequest.Files, in.uid, workspaceRootPath, threadWorkspace)
	if fileErr != nil {
		return bailWithError(fileReferenceErrorPayload(fileErr))
	}

	// Determine agent mode/model tier with priority: message metadata
	// > thread setting > defaults.
	agentMode := determineAgentMode(in.chatRequest.Metadata.AgentMode, in.chatThread.AgentMode)
	modelTier := determineModelTier(in.chatRequest.Metadata.ModelTier, in.chatThread.Model)
	customPrompt := in.chatThread.Prompt

	globals.Info(fmt.Sprintf("[Agent API] Agent mode determination: metadata='%s', thread='%s', final='%s'",
		in.chatRequest.Metadata.AgentMode, in.chatThread.AgentMode, agentMode))
	globals.Info(fmt.Sprintf("[Agent API] Model tier determination: metadata='%s', thread='%s', final='%s'",
		in.chatRequest.Metadata.ModelTier, in.chatThread.Model, modelTier))

	skillAccess, skillAccessErr := ensureSkillRuntimeAccessForProject(int(in.uid), agentMode, in.chatThread.ProjectID)
	if skillAccessErr != nil {
		globals.Error(fmt.Sprintf("[Agent API] Failed to verify skill access for uid %d mode %s: %v", in.uid, agentMode, skillAccessErr))
		return bailWithError(buildSkillAccessDeniedPayload("Failed to verify skill access", "unknown", ""))
	}
	if !skillAccess.Allowed {
		globals.Warn(fmt.Sprintf("[Agent API] User %d attempted gated skill %s without approved access (source=%s requiredTier=%s)", in.uid, agentMode, skillAccess.Source, skillAccess.RequiredTier))
		if skillAccess.Code == "SKILL_UNAVAILABLE" {
			return bailWithError(buildSkillUnavailablePayload(skillAccess.Message))
		}
		return bailWithError(buildSkillAccessDeniedPayload(skillAccess.Message, skillAccess.Source, skillAccess.RequiredTier))
	}

	// Backend guard: the Pro tier requires paid membership. Two deny
	// reasons (verification error vs. non-member) so the user-facing
	// copy stays accurate. quotaService is constructed lazily here
	// (rather than up at the credits block) because the Pro tier is
	// the only premium-gated one — the Standard tier doesn't need it.
	//
	// Internal tier ID stays "work-plus" (persisted to chat_thread.model
	// and consumed by GetAccountForModelTier) for backward compatibility
	// with existing rows; only the user-visible name moved from
	// "Work Plus" → "Pro" in the 2026-05-14 rename.
	if modelTier == "work-plus" {
		quotaService := accountService.NewQuotaService()
		isPremiumMember, premErr := quotaService.IsPremiumMember(int(in.uid))
		var denyMessage string
		switch {
		case premErr != nil:
			globals.Error(fmt.Sprintf("[Agent API] Failed to verify premium membership for uid %d: %v", in.uid, premErr))
			denyMessage = "Failed to verify membership"
		case !isPremiumMember:
			globals.Warn(fmt.Sprintf("[Agent API] User %d attempted Pro tier without premium membership", in.uid))
			denyMessage = "Pro is available to paid members only."
		}
		if denyMessage != "" {
			return bailWithError(buildTierAccessDeniedPayload(denyMessage))
		}
	}

	// Mode change → persist + reset SDK session (rationale on
	// requiresSessionReset). Tier change → persist only after the tier
	// access gate above has passed; otherwise a non-member click on Pro
	// would poison the thread model and make future sends fail before the
	// frontend has a chance to switch it back.
	if requiresSessionReset(in.chatThread.AgentMode, agentMode) {
		previousMode := in.chatThread.AgentMode
		if err := workagentService.DefaultThreadRepository().PersistAgentMode(in.chatThread.Id, agentMode); err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] Failed to persist mode switch to '%s': %v", agentMode, err))
		} else {
			in.chatThread.AgentMode = agentMode
			in.chatThread.AgentSessionID = ""
			in.chatThread.AgentSessionCreatedAt = nil
			globals.Info(fmt.Sprintf("[Agent API] Agent mode changed → fresh SDK session (thread=%d, new_mode=%s)", in.chatThread.Id, agentMode))
			// G15 — per-skill churn. `from` empty means the thread
			// had no prior mode (effectively first selection on a
			// brand-new thread that defaulted to "ppt" and is being
			// switched before its first turn). Downstream queries
			// can pair (from, to) to build a Sankey of skill
			// migration patterns.
			workagentService.EmitMetric("wa_skill_switch", map[string]any{
				"thread_id": in.chatThread.Id,
				"uid":       in.uid,
				"from":      previousMode,
				"to":        agentMode,
			})
		}
	}

	if modelTier != "" && modelTier != in.chatThread.Model {
		if err := workagentService.DefaultThreadRepository().UpdateModelTier(in.chatThread.Id, modelTier); err != nil {
			globals.Warn(fmt.Sprintf("[Agent API] Failed to update thread model to '%s': %v", modelTier, err))
		} else {
			in.chatThread.Model = modelTier
		}
	}

	if customPrompt != "" {
		globals.Info(fmt.Sprintf("[Agent API] Using custom prompt from thread (%d chars)", len(customPrompt)))
	}

	return &turnEnvironment{
		threadWorkspace:   threadWorkspace,
		workspaceRootPath: workspaceRootPath,
		agentFiles:        agentFiles,
		agentMode:         agentMode,
		modelTier:         modelTier,
		customPrompt:      customPrompt,
	}, true
}

// reserveCreditsInput bundles what reserveCreditsForTurn needs to
// reserve credits for a turn AND emit an SSE error event on failure.
// All fields are read-only (the method doesn't mutate any of them).
type reserveCreditsInput struct {
	ctx            *gin.Context
	uid            uint
	chatThread     *workagentModel.ChatThread
	creditCost     int
	reservationTTL time.Duration
	tempMessageID  string
	sendEvent      func(workagentModel.AgentSSEEvent)
}

// reserveCreditsForTurn front-loads the billing for an agent turn:
// generate (or accept) an idempotency key, run the Reserve
// transaction, and on failure emit one of two SSE done events
// (insufficient_credits or server_error) to the client. Returns the
// new reservation id and ok=true on success; ok=false signals the
// caller to bail out — an SSE event has already been written.
//
// An existing reservation is never permission to execute again. A terminal
// hit replays its persisted message when available (or fails generically when
// persistence is missing); a non-terminal hit means another Server instance
// may still own the request, so it emits a retryable busy result and stops
// before the Provider or settlement closures are reached.
func (api *AIChatApiNew) reserveCreditsForTurn(in reserveCreditsInput) (uint, string, bool) {
	db := globals.GraDBs["system"]
	reservationSvc := accountService.NewCreditReservationService()
	reservationTTL := in.reservationTTL
	if reservationTTL <= 0 {
		// Fail safe for internal/test callers that have not supplied the timeout
		// snapshot yet: Work Agent's documented fallback is 30 minutes, not the
		// generic ten-minute Reservation default.
		reservationTTL = workagentService.ReservationTTLForExecution(workAgentDefaultTimeout)
	}

	idempotencyKey := strings.TrimSpace(in.ctx.GetHeader("X-Agent-Request-Id"))
	if idempotencyKey == "" {
		// UnixNano + atomic counter. Same-nanosecond collision here
		// collapses two requests onto one reservation and the credit
		// service short-circuits the second turn as "already processed"
		// while the agent still streams a reply — under-billing.
		idempotencyKey = fmt.Sprintf("workagent_%d_%d_%d", in.uid, time.Now().UnixNano(), workagentIdempotencySeq.Add(1))
	}

	var reservationID uint
	err := db.Transaction(func(tx *gorm.DB) error {
		res, resErr := reservationSvc.Reserve(tx, accountService.ReservationRequest{
			UID:            int(in.uid),
			Tool:           "workagent",
			IdempotencyKey: idempotencyKey,
			Reserved:       in.creditCost,
			TTL:            reservationTTL,
			Remark:         "workagent chat",
			ProjectID:      in.chatThread.ProjectID,
		})
		if resErr != nil {
			return resErr
		}
		if !res.Created {
			if !res.Reservation.IsTerminal() {
				return errAgentTurnInProgress
			}
			return errAgentTurnTerminal
		}
		reservationID = res.Reservation.Id
		return nil
	})
	if err == nil {
		return reservationID, idempotencyKey, true
	}
	if errors.Is(err, errAgentTurnTerminal) {
		// Replay only after the reservation transaction releases its row lock.
		// Besides shortening the critical section, this avoids requiring a
		// second pooled DB connection while a one-connection SQLite harness is
		// still inside the transaction.
		if api.replayProcessedAgentTurn(in, idempotencyKey) {
			return 0, idempotencyKey, false
		}
		err = fmt.Errorf("request already processed")
	}
	if errors.Is(err, errAgentTurnInProgress) {
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result: workagentService.BuildThreadBusyPayload(
				"This request is already being processed. Please wait for it to finish.",
			),
		})
		return 0, idempotencyKey, false
	}
	if errors.Is(err, accountService.ErrReservationReplayConflict) {
		conflictPayload := map[string]interface{}{
			"type":      "result",
			"subtype":   "idempotency_conflict",
			"is_error":  true,
			"code":      "RESERVATION_REPLAY_CONFLICT",
			"retryable": false,
			"error":     "This request id was already used for different credit parameters.",
		}
		conflictData, _ := json.Marshal(conflictPayload)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    json.RawMessage(conflictData),
		})
		return 0, idempotencyKey, false
	}

	// Reserve failed — record the error timestamp and surface the
	// right SSE error code to the client.
	recordAgentError(in.chatThread)

	if errors.Is(err, accountService.ErrInsufficientCredits) {
		insufficientPayload := map[string]interface{}{
			"type":            "result",
			"subtype":         "insufficient_credits",
			"is_error":        true,
			"error":           "Insufficient credits to run Agent.",
			"code":            "INSUFFICIENT_CREDITS",
			"creditsRequired": in.creditCost,
		}
		insufficientData, _ := json.Marshal(insufficientPayload)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    json.RawMessage(insufficientData),
		})
		return 0, idempotencyKey, false
	}

	if errors.Is(err, projectService.ErrBudgetExceeded) {
		budgetExceededPayload := map[string]interface{}{
			"type":              "result",
			"subtype":           "budget_exceeded",
			"is_error":          true,
			"error":             "Work Agent project budget cap reached.",
			"code":              "BUDGET_EXCEEDED",
			"project_id":        in.chatThread.ProjectID,
			"requested_credits": in.creditCost,
		}
		if status, statusErr := projectService.Default().GetBudgetStatusForOwner(in.chatThread.ProjectID, in.uid); statusErr == nil && status != nil {
			budgetExceededPayload["current_used"] = status.Used
			if status.Cap != nil {
				budgetExceededPayload["cap"] = *status.Cap
			}
		}
		budgetExceededData, _ := json.Marshal(budgetExceededPayload)
		in.sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: in.tempMessageID,
			Result:    json.RawMessage(budgetExceededData),
		})
		return 0, idempotencyKey, false
	}

	globals.Error(fmt.Sprintf("[Agent] Reserve failed for user %d (cost %d): %v", in.uid, in.creditCost, err))
	serverErrPayload := map[string]interface{}{
		"type":     "result",
		"subtype":  "server_error",
		"is_error": true,
		"error":    "Server error",
	}
	serverErrData, _ := json.Marshal(serverErrPayload)
	in.sendEvent(workagentModel.AgentSSEEvent{
		Type:      workagentModel.AgentEventDone,
		MessageID: in.tempMessageID,
		Result:    json.RawMessage(serverErrData),
	})
	return 0, idempotencyKey, false
}

func (api *AIChatApiNew) replayProcessedAgentTurn(in reserveCreditsInput, idempotencyKey string) bool {
	if strings.TrimSpace(idempotencyKey) == "" || in.chatThread == nil {
		return false
	}
	var msg workagentModel.ChatMessage
	err := globals.GraDBs["system"].
		Where("uid = ? AND thread_id = ? AND message_idempotency_key = ?", in.uid, in.chatThread.Id, idempotencyKey).
		Order("id DESC").
		First(&msg).Error
	if err != nil {
		return false
	}

	var conversation workagentModel.AgentConversation
	result := json.RawMessage{}
	if strings.TrimSpace(msg.StructuredContent) != "" {
		if err := json.Unmarshal([]byte(msg.StructuredContent), &conversation); err == nil {
			replayStoredAgentMessages(msg.UUID, &conversation, in.sendEvent)
			result = conversation.Result
		}
	}
	if len(result) == 0 {
		payload := map[string]interface{}{
			"type":      "result",
			"subtype":   "already_processed",
			"is_error":  false,
			"result":    msg.AIText,
			"replayed":  true,
			"messageId": msg.UUID,
		}
		data, _ := json.Marshal(payload)
		result = json.RawMessage(data)
	} else {
		result = attachRemainingCreditsToResult(result, in.uid)
		result = markResultAsReplayed(result, msg.UUID)
	}

	in.sendEvent(workagentModel.AgentSSEEvent{
		Type:      workagentModel.AgentEventDone,
		MessageID: msg.UUID,
		Result:    result,
	})
	return true
}

func replayStoredAgentMessages(messageID string, conversation *workagentModel.AgentConversation, sendEvent func(workagentModel.AgentSSEEvent)) {
	if conversation == nil || len(conversation.Messages) == 0 {
		return
	}
	for messageIndex, raw := range conversation.Messages {
		if len(raw) == 0 {
			continue
		}
		var msg map[string]interface{}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		messageType, _ := msg["type"].(string)
		if messageType == "" {
			messageType = "assistant"
		}
		sendEvent(workagentModel.NewMessageEvent(messageID, messageType, messageIndex, raw))

		switch content := msg["content"].(type) {
		case []interface{}:
			for blockIndex, block := range content {
				blockRaw, err := json.Marshal(block)
				if err != nil {
					continue
				}
				sendEvent(workagentModel.NewBlockEvent(messageID, messageIndex, blockIndex, blockRaw))
			}
		case string:
			if strings.TrimSpace(content) == "" {
				continue
			}
			blockRaw, err := json.Marshal(map[string]string{
				"type": "text",
				"text": content,
			})
			if err == nil {
				sendEvent(workagentModel.NewBlockEvent(messageID, messageIndex, 0, blockRaw))
			}
		}
	}
}

func markResultAsReplayed(result json.RawMessage, messageID string) json.RawMessage {
	if len(result) == 0 {
		return result
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(result, &payload); err != nil {
		return result
	}
	payload["replayed"] = true
	payload["messageId"] = messageID
	data, err := json.Marshal(payload)
	if err != nil {
		return result
	}
	return data
}

// preparedTurn carries the validated inputs HandleAgentChat needs
// once the front-loaded validation + SSE setup phase has succeeded.
// Bag-of-fields rather than a builder because the consumer
// (HandleAgentChat) reads each one exactly once during unpacking;
// hiding them behind methods would just add noise.
type preparedTurn struct {
	uid           uint
	chatRequest   ChatStreamRequest
	threadID      string
	chatThread    *workagentModel.ChatThread
	userMessage   string
	flusher       http.Flusher
	tempMessageID string
}

// prepareTurn handles the front-loaded validation + SSE setup of an
// agent chat turn:
//
//  1. Resolve the JWT-claim uid; bail 401 on missing claim.
//  2. Parse the request body; bail 400 on malformed JSON.
//  3. Validate the conversation id is present and non-zero;
//     bail 400 if missing.
//  4. Load the chat thread with embedded ownership check; bail 404
//     on miss OR cross-tenant attempt (same response so the response
//     isn't an existence oracle — see CWE-639).
//  5. Extract the latest user message text (used by error recording
//     and downstream persistence).
//  6. Switch the response into SSE mode and grab the http.Flusher;
//     bail 500 if the underlying writer doesn't support flushing
//     (a misconfigured reverse proxy or test harness).
//  7. Mint a unique tempMessageID for the turn so frontend SSE rows
//     don't collide on burst traffic.
//  8. Flush an immediate `: connected\n\n` SSE comment so the
//     Next.js stream proxy gets response headers before the first
//     SDK chunk lands (long-running PPT generations otherwise look
//     like a timeout to the proxy).
//
// Steps 1-5 are JSON-error early returns (so the front-end stream
// proxy doesn't get a half-formed event-stream when the request is
// invalid); steps 6-8 commit the response to SSE mode. The order
// matters and is pinned by the early-return path tests in
// agent_api_handle_chat_test.go.
//
// Returns (preparedTurn, true) on success, (nil, false) when an
// early-return path already wrote its response. Caller treats false
// as "bail out, response already sent."
func (api *AIChatApiNew) prepareTurn(c *gin.Context) (*preparedTurn, bool) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return nil, false
	}

	var chatRequest ChatStreamRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, workAgentChatMaxBodyBytes)
	if err := c.ShouldBindJSON(&chatRequest); err != nil {
		globals.Error(fmt.Sprintf("[Agent] Failed to parse request: %v", err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return nil, false
	}
	if err := validateChatStreamRequestBounds(chatRequest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}

	threadID := chatRequest.ConversationID
	if threadID == "" || threadID == "0" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Thread ID required"})
		return nil, false
	}

	// getChatThread embeds the uid check; mismatch → ErrRecordNotFound
	// (we deliberately don't differentiate "wrong owner" from "doesn't
	// exist" so we don't leak the existence of other users' threads).
	// Real DB errors (connection failure, etc.) are wrapped — they
	// must surface as 5xx, not 404, so a transient DB blip doesn't
	// look like the user's thread silently disappeared.
	chatThread, err := api.getChatThread(threadID, uid)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			globals.Error(fmt.Sprintf("[Agent] Thread not found or access denied: %v", err))
			c.JSON(http.StatusNotFound, gin.H{"error": "Thread not found"})
			return nil, false
		}
		globals.Error(fmt.Sprintf("[Agent] Thread lookup failed (uid=%d, thread=%s): %v", uid, threadID, err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load thread"})
		return nil, false
	}

	// Extract user message for error recording (and downstream
	// persistence) — last message in the array is what triggered this
	// turn.
	var userMessage string
	if len(chatRequest.Messages) > 0 {
		userMessage = chatRequest.Messages[len(chatRequest.Messages)-1].Content
	}

	// Switch to SSE response. Headers must be set BEFORE the first
	// Write — once any data flushes, headers are frozen. Ordering vs
	// the validation block above is load-bearing: a 4xx response from
	// here on would have already-flushed `Content-Type: text/event-stream`
	// and the frontend's stream proxy would treat the JSON body as
	// half-formed events.
	//
	// B5 Slice 1 — SetupTurnSSE handles the headers + Flusher cast +
	// temp-message-id mint + ': connected' bootstrap. The previous
	// inline path stamped tempMessageID as "temp_agent_<unix>-<seq>";
	// SetupTurnSSE uses an identical layout (prefix + UnixNano +
	// atomic seq) so persisted log strings stay greppable across the
	// transition.
	sseSetup, sseOK := workagentService.SetupTurnSSE(c, "temp_agent")
	if !sseOK {
		return nil, false
	}

	return &preparedTurn{
		uid:           uid,
		chatRequest:   chatRequest,
		threadID:      threadID,
		chatThread:    chatThread,
		userMessage:   userMessage,
		flusher:       sseSetup.Flusher,
		tempMessageID: sseSetup.TempMessageID,
	}, true
}

func validateChatStreamRequestBounds(req ChatStreamRequest) error {
	if len(req.Messages) == 0 {
		return fmt.Errorf("message is required")
	}
	if len(req.Messages) > workAgentChatMaxMessages {
		return fmt.Errorf("too many messages")
	}
	for _, msg := range req.Messages {
		if len(msg.Content) > workAgentChatMaxMessageBytes {
			return fmt.Errorf("message is too long")
		}
	}
	if len(req.Files) > workAgentChatMaxFiles {
		return fmt.Errorf("too many files")
	}
	if raw, err := json.Marshal(req.Metadata); err == nil && len(raw) > workAgentChatMaxMetadataBytes {
		return fmt.Errorf("metadata is too large")
	}
	return nil
}

// persistAgentTurnInput bundles the dozen-or-so pieces of state the
// tail of HandleAgentChat reads. Bag-of-args because the inputs are
// genuinely heterogeneous (uid+thread for ownership, conversation for
// payload, modelTier+agentMode for usage, startTime for duration) —
// not because there's a meaningful struct hiding here.
type persistAgentTurnInput struct {
	ctx                 *gin.Context
	uid                 uint
	accountID           uint64
	creditCost          int
	usedCredits         int
	threadID            string
	chatThread          *workagentModel.ChatThread
	conversation        *workagentModel.AgentConversation
	workspaceRootPath   string
	userMessage         string
	modelTier           string
	agentMode           string
	idempotencyKey      string
	generatedFilesCount int
	startTime           time.Time
}

// persistAgentTurn finalizes a successful agent turn: sanitize the
// streamed conversation, save it, on save failure surface to ops AND
// release the reservation (return false), on success update the
// thread's display name + record usage (return true).
//
// Save-failure trade-off worth flagging: we currently REFUND credits
// on save failure even though the user already received the streamed
// answer. That's a deliberate "don't charge for what we lost" stance,
// but it also creates a free-turn window if the DB is flapping (every
// flap → free answer + refund). The proper fix (queue persistence +
// idempotent retry, or a "best-effort save" fallback table) is in
// work-agent-backlog.md as a Phase 2 item; for now we at least surface
// every save failure to ops via NotifyAgentError so the volume can be
// tracked instead of silently swallowed.
//
// Return value is the new shouldFinalize for the caller's billing
// defer — false means "release the reservation, don't bill", true
// means "finalize the reservation, bill the user".
func (api *AIChatApiNew) persistAgentTurn(in persistAgentTurnInput) bool {
	workagentService.SanitizeAgentConversation(in.conversation, in.workspaceRootPath)

	summary, messageID, err := saveAgentConversation(
		in.conversation, in.uid, in.chatThread.Id, in.userMessage, in.modelTier, in.idempotencyKey,
	)
	if err != nil {
		globals.Error(fmt.Sprintf("[Agent API] Failed to save conversation (uid=%d, thread=%s, sessionID=%s): %v",
			in.uid, in.threadID, in.chatThread.AgentSessionID, err))
		// Page ops: silent save failures hide both reliability issues
		// and the free-turn risk above. The notifier is rate-limited
		// internally so a flapping DB won't spam.
		workagentService.NotifyAgentError(
			context.Background(),
			fmt.Errorf("save conversation failed (uid=%d, thread=%s): %w", in.uid, in.threadID, err),
			in.threadID,
			fmt.Sprintf("%d", in.uid),
			in.chatThread.AgentSessionID,
		)
		recordAgentError(in.chatThread)
		return false
	}

	// Update thread name and statistics (same as normal mode)
	if summary != "" {
		api.updateThreadName(in.chatThread, summary)
	}

	duration := int(time.Since(in.startTime).Seconds())
	workagentService.GetAgentAccountPool().RecordTokenCreditUsage(in.accountID, in.usedCredits, time.Now())
	recordAgentUsage(in.ctx, recordAgentUsageInput{
		uid:                 in.uid,
		creditCost:          in.usedCredits,
		threadID:            in.threadID,
		chatThreadID:        in.chatThread.Id,
		messageID:           messageID,
		agentMode:           in.agentMode,
		modelTier:           in.modelTier,
		duration:            duration,
		generatedFilesCount: in.generatedFilesCount,
	})

	globals.Info(fmt.Sprintf("[Agent API] Conversation complete: %d messages, duration=%ds",
		len(in.conversation.Messages), duration))
	return true
}
