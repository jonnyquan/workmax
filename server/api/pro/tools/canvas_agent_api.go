package tools

// Canvas Agent SSE handler — the HTTP entry point for canvas-mode
// agent chat. Owns input parse + SSE setup + per-turn write loop;
// delegates canvas-specific concerns (cost, attachment prep, system
// prompt, result enrichment) to the canvas surface registered under
// AgentMode="canvas" in service/tools/workagent/surfaces/canvas/.
//
// What stays here:
//   • HTTP handler body (input parse + SSE setup + writes)
//   • SSE serialization helper (sendCanvasAgentSSE)
//   • PromptGuard check (canvas-specific §12.3 content policy)
//   • Canvas agent context setup (setupCanvasAgentContext)
//
// What lives in service/tools/workagent (shared with the general work agent):
//   • Per-thread turn lock (GetAgentTurnLock)
//   • AgentSurface contract + global SurfaceRegistry
//   • SanitizeAgentJSON / SanitizeAgentConversation
//   • Account pool, SDK client manager, SSE connection manager
//
// What lives in service/tools/workagent/surfaces/canvas:
//   • Surface implementation (workagent.AgentSurface contract)
//   • Per-turn behaviors (Cost / PrepareAttachments / BuildSystemPrompt
//     / EnrichResult — see canvasTurn in surface.go)
//   • Request DTOs, validators, attachment prep, cost lookup
//
// R3 phase 4 will fold this handler's lifecycle into workagent's
// shared dispatcher; phases 1–3 + 4-prep set up the seam (shared
// turn lock, AgentSurface interface, canvasagent → surfaces/canvas
// relocation, surface registry, shared sanitizer).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"
	accountService "server/service/account"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	workagentService "server/service/tools/workagent"
	canvas "server/service/tools/workagent/surfaces/canvas"
	"server/utils"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Per-thread turn serialization is shared with the general work agent
// via workagentService.GetAgentTurnLock. Pre-2026-05 there were two
// parallel sync.Maps doing the same thing; consolidating them was the
// first step of R3 (Option A: Canvas Agent as WorkAgent surface).
// Threads share the w_workagent_thread table so UUIDs are globally
// unique — no per-surface key prefix needed.
var canvasAgentIdempotencySeq atomic.Uint64
var errCanvasAgentRequestAlreadyProcessed = errors.New("canvas agent request already processed")

// canvasAgentHeartbeatInterval is how often the SSE stream emits a
// comment-line keepalive between real events. C-08 Stage 1: absorbs
// LB / reverse-proxy idle timeouts (nginx default 60s; most cloud
// LBs 60-300s) so a slow agent run doesn't get silently severed.
//
// Wire format is the SSE comment ": hb <unix>\n\n". Standards-compliant
// clients, including the Desktop renderer, ignore any line starting with
// ":", so this is invisible at the application layer. No
// protocol change; just transport keepalive.
//
// 25s sits comfortably under the tightest common idle cap (nginx 60s)
// while not flooding the connection. Lifetime is bound to the request
// context: heartbeat goroutine starts after the SSE headers flush and
// exits when the handler returns (defer-channel pattern, see usage).
const (
	canvasAgentHeartbeatInterval = 25 * time.Second
	canvasAgentDefaultTimeout    = 10 * time.Minute
)

// formatCanvasAgentHeartbeat produces the wire bytes for one SSE
// heartbeat. Factored out so the format is pinned by a unit test —
// a regression that emitted (say) "data: hb" instead of ": hb" would
// pierce the Desktop parser's comment-drop path and surface a junk
// "non-JSON data line" log on every keepalive.
func formatCanvasAgentHeartbeat(now time.Time) []byte {
	return []byte(fmt.Sprintf(": hb %d\n\n", now.Unix()))
}

// sendCanvasAgentSSE marshals an SSE event and flushes it to the client.
// Returns false on any write error so the caller can abort the agent run
// (typically by calling agentCancel() to short-circuit further callbacks).
func sendCanvasAgentSSE(c *gin.Context, flusher http.Flusher, event workagentModel.AgentSSEEvent) bool {
	data, err := json.Marshal(event)
	if err != nil {
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to marshal SSE event: %v", err))
		return false
	}

	globals.Info(fmt.Sprintf("[CanvasAgent] 📤 SSE: type=%s", event.Type))
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", string(data)); err != nil {
		globals.Warn(fmt.Sprintf("[CanvasAgent] SSE write failed: %v", err))
		return false
	}
	flusher.Flush()
	return true
}

// (sanitize helpers moved to service/tools/workagent/agent_sanitize.go
// as workagentService.SanitizeAgentJSON / SanitizeAgentConversation —
// shared with the general work agent's handler so any future SSE
// merge picks the same sanitizer. R3 phase 4-prep.)

func setupCanvasAgentContext(c *gin.Context, uid uint, threadID string, threadDBID uint, timeout time.Duration) (context.Context, context.CancelFunc, func()) {
	baseCtx := workagentService.WithUserID(context.Background(), uid)
	baseCtx = workagentService.WithThreadID(baseCtx, threadDBID)
	baseCtx = context.WithValue(baseCtx, canvas.AgentUserIDKey, uid)
	agentCtx, agentCancel := context.WithTimeout(baseCtx, timeout)

	clientGone := c.Request.Context().Done()
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-clientGone:
			globals.Info(fmt.Sprintf("[CanvasAgent] Client disconnected, cancelling agent context (thread=%s)", threadID))
			agentCancel()
		case <-agentCtx.Done():
		}
	}()

	cleanup := func() {
		agentCancel()
		<-watcherDone
	}
	return agentCtx, agentCancel, cleanup
}

// (canvas.Surface.EnrichResult attaches the user's remaining credits
// to the Done payload — see service/tools/workagent/surfaces/canvas/
// surface.go. canvas.ErrorResultPayload provides the canonical agent-
// failure SSE payload — see service/tools/workagent/surfaces/canvas/
// canvas_agent_processor.go.)

// recordCanvasAgentError mirrors the workagent recordAgentError pattern
// (see api/pro/tools/workagent/agent_persistence.go) plus the rate-limited
// admin email notifier. Two effects:
//
//  1. Bump the thread's updated_at so the FE conversation list keeps the
//     failed thread visible at the top — without this, a thread that
//     errored mid-stream silently sinks below newer threads and the user
//     can't find where to retry.
//
//  2. Forward the error to NotifyAgentError so ops gets a rate-limited
//     email (max 1 per error prefix per 10 min). GLM subscription errors
//     get a dedicated record path even when email is off.
//
// Safe to call with a nil chatThread (e.g. errors before thread creation):
// the timestamp bump is skipped, the email still fires with empty thread
// metadata so ops sees the error class.
func recordCanvasAgentError(
	ctx context.Context,
	chatThread *workagentModel.ChatThread,
	err error,
	uid uint,
) {
	if err == nil {
		return
	}
	threadIDStr := ""
	threadUUID := ""
	if chatThread != nil {
		if tsErr := workagentService.DefaultThreadRepository().TouchTimestamp(chatThread.Id, time.Now()); tsErr != nil {
			globals.Error(fmt.Sprintf("[CanvasAgent] TouchTimestamp on error path failed: %v", tsErr))
		}
		threadIDStr = fmt.Sprintf("%d", chatThread.Id)
		threadUUID = chatThread.UUID
	}
	workagentService.NotifyAgentError(
		ctx,
		err,
		threadIDStr,
		fmt.Sprintf("%d", uid),
		threadUUID,
	)
}

// AgentChat handles Canvas Agent streaming chat requests.
// This endpoint runs the Claude Agent SDK with canvas-specific system prompts
// and agent_type=canvas. Reuses WorkAgent's infrastructure (AgentClientManager,
// AccountPool, Session Retry).
// @Summary Canvas Agent Chat (SSE streaming)
// @Tags Canvas
// @Accept json
// @Produce text/event-stream
// @Param request body canvas.CanvasAgentChatRequest true "Canvas Agent Chat Request"
// @Router /api/tools/canvas/agent/chat [post]
func (a *CanvasApi) AgentChat(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondCanvasChatHTTPError(c, http.StatusUnauthorized, "Unauthorized", canvasAIErrorUnauthorized, true)
		return
	}

	startTime := time.Now()

	// Parse request
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, canvas.MaxRequestBodyBytes)
	var req canvas.CanvasAgentChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to parse request: %v", err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request format"})
		return
	}

	normalizedConversationID, err := canvas.NormalizeConversationID(req.ConversationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid conversationId"})
		return
	}
	req.ConversationID = normalizedConversationID

	if err := canvas.ValidateRequest(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	projectID, err := parseAndAuthorizeCanvasThreadProjectIDForEdit(req.Context.ProjectID, uid)
	if err != nil {
		respondCanvasChatHTTPError(c, http.StatusBadRequest, "Invalid projectId", "PROJECT_NOT_FOUND", false)
		return
	}

	// Wrap parsed + validated request in the surface turn so the
	// canvas-specific concerns (cost, attachment prep, system prompt,
	// result enrichment) flow through workagent.SurfaceTurn instead
	// of being dispatched to package-level functions inline. R3
	// phase 4 will fold this handler's lifecycle into workagent's
	// shared dispatcher; phase 2 set up the seam, phase 3 moved the
	// implementation into workagent/surfaces/canvas, this lookup
	// ensures the registry path is exercised in production today.
	surface := workagentService.LookupSurface(canvas.CanvasAgentMode)
	if surface == nil {
		// Init order would have to be sabotaged for this to fire
		// (canvas package's init.go registers Surface{} at process
		// startup). Defensive — fall back to direct construction so a
		// botched init doesn't break the canvas chat experience.
		globals.Error("[CanvasAgent] surface registry missing canvas entry; falling back to direct Surface{}")
		surface = canvas.NewSurface()
	}
	turn := canvas.NewTurn(req)

	// Extract user message
	var userMessage string
	if len(req.Messages) > 0 {
		userMessage = req.Messages[len(req.Messages)-1].Content
	}

	// PromptGuard (§12.3): reject sensitive content before any credit
	// reservation or LLM work. Runs on the latest user message only —
	// historical turns are already in the thread and re-checking them
	// would create spurious rejections on legitimate follow-ups.
	guardRes := canvasService.Default().Check(canvasService.GuardInput{Prompt: userMessage})
	if guardRes.Action == canvasService.GuardActionReject {
		c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
			"code":    http.StatusUnprocessableEntity,
			"message": "Prompt blocked by content guard",
			"error": gin.H{
				"code":   guardRes.RejectReason,
				"reason": guardRes.RejectReason,
			},
		})
		return
	}
	// Forward the sanitized text downstream. On truncation this shortens
	// the prompt; on allow it's a no-op copy.
	userMessage = guardRes.CleanedPrompt
	if len(req.Messages) > 0 {
		req.Messages[len(req.Messages)-1].Content = userMessage
	}

	// ── Setup SSE ─────────────────────────────────────────────────────────
	// B5 Slice 1 — shared headers + Flusher cast + temp message ID
	// + ': connected' bootstrap. Surface-specific only in the temp
	// id prefix ("temp_canvas_agent" → identifies surface in logs).
	sseSetup, sseOK := workagentService.SetupTurnSSE(c, "temp_canvas_agent")
	if !sseOK {
		// SetupTurnSSE already wrote the 500 JSON body.
		return
	}
	flusher := sseSetup.Flusher
	tempMessageID := sseSetup.TempMessageID

	// B5 Slice 2a — shared SSE emitter. Replaces the surface-local
	// sendEvent closure + sseWriteMu + atomic.Pointer trackedSSEConn
	// block. Canvas's call sites consume the bool return (heartbeat
	// goroutine short-circuits on broken streams) so the shim
	// preserves that semantic.
	emitter := workagentService.NewSSEEmitter(c.Writer, flusher, "[CanvasAgent]")
	sendEvent := func(event workagentModel.AgentSSEEvent) bool {
		return emitter.Send(event)
	}

	// C-08 Stage 1: SSE heartbeat. Writes ": hb <unix>\n\n" every
	// canvasAgentHeartbeatInterval to keep the connection alive
	// through idle LB / proxy timeouts. Routes through the shared
	// SSEEmitter so heartbeat bytes never interleave a real event
	// mid-frame (the emitter's mutex / WriteChunk path is the same
	// one Send uses). Heartbeat write errors are swallowed — the
	// next real Send will surface them via the existing warn path.
	//
	// P1 #15: scope this heartbeat to the *pre-SSE-registration window*
	// ONLY. Once emitter.SetConnection(sseConn) runs below, the
	// shared workagentService.SSEConnection.runWriter takes over
	// heartbeats on the bounded write queue (its own :heartbeat
	// comment every 30s). Emitting from BOTH sources is a double-pay
	// and makes the C-08 25s contract meaningless.
	//
	// Why keep ANY in-handler heartbeat at all? The SSE conn isn't
	// registered until after attachment prep + system-prompt build,
	// which can take seconds on slow attachments. During that window
	// the shared writer isn't ticking yet; nginx default idle is 60s.
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(canvasAgentHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatStop:
				return
			case <-ticker.C:
				// Once the shared SSEConnection is registered, defer
				// to its runWriter — our tick is redundant.
				if emitter.HasConnection() {
					continue
				}
				emitter.WriteRaw(formatCanvasAgentHeartbeat(time.Now()))
			}
		}
	}()
	defer func() {
		close(heartbeatStop)
		<-heartbeatDone
	}()

	// ── Check Agent enabled ───────────────────────────────────────────────
	agentManager := workagentService.GetAgentClientManager()
	if !agentManager.IsEnabled() {
		globals.Error("[CanvasAgent] AgentManager is not enabled")
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}
	configuredTimeout := 0
	if cfg := agentManager.GetConfig(); cfg != nil {
		configuredTimeout = cfg.Timeout
	}
	// Freeze the timeout before Reserve and reuse it for the execution
	// context. The reservation remains valid through a five-minute terminal
	// persistence/settlement margin even when Canvas is configured above 10m.
	agentTimeout := workagentService.ResolveAgentExecutionTimeout(configuredTimeout, canvasAgentDefaultTimeout)
	reservationTTL := workagentService.ReservationTTLForExecution(agentTimeout)

	db := globals.GraDBs["system"]
	reservationSvc := accountService.NewCreditReservationService()

	// ── Find or create canvas thread ──────────────────────────────────────
	var chatThread *workagentModel.ChatThread
	threadCreated := false

	if req.ConversationID != "" {
		// Try to find existing thread
		threadQuery := globals.GraDBs["system"].WithContext(c.Request.Context()).
			Where("uuid = ? AND agent_type = ?", req.ConversationID, surface.AgentType())
		if projectID > 0 {
			threadQuery = threadQuery.Where("project_id = ? OR (project_id = 0 AND uid = ?)", projectID, int(uid))
		} else {
			threadQuery = threadQuery.Where("uid = ?", int(uid))
		}
		if err := threadQuery.First(&chatThread).Error; err != nil {
			globals.Warn(fmt.Sprintf("[CanvasAgent] Thread %s not found, will create new", req.ConversationID))
		}
	}

	if chatThread == nil {
		// Create a new canvas thread
		threadUUID := uuid.New().String()
		if req.ConversationID != "" {
			threadUUID = req.ConversationID
		}
		chatThread = &workagentModel.ChatThread{
			UID:       int(uid),
			UUID:      threadUUID,
			ProjectID: projectID,
			Name:      canvas.TruncateString(userMessage, 50),
			AgentMode: surface.AgentMode(),
			AgentType: surface.AgentType(),
			Model:     "canvas-agent",
		}
		if err := globals.GraDBs["system"].WithContext(c.Request.Context()).Create(chatThread).Error; err != nil {
			globals.Error(fmt.Sprintf("[CanvasAgent] Failed to create thread: %v", err))
			sendEvent(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result:    canvas.ErrorResultPayload(),
			})
			return
		}
		threadCreated = true
		globals.Info(fmt.Sprintf("[CanvasAgent] Created new thread: uuid=%s, id=%d", chatThread.UUID, chatThread.Id))
	}
	scopedProjectID, err := ensureCanvasThreadProjectScope(chatThread.ProjectID, projectID)
	if err != nil {
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}
	if scopedProjectID > 0 && chatThread.ProjectID == 0 {
		if err := globals.GraDBs["system"].WithContext(c.Request.Context()).
			Model(&workagentModel.ChatThread{}).
			Where("id = ?", chatThread.Id).
			Update("project_id", scopedProjectID).Error; err != nil {
			globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to scope thread to project %d: %v", scopedProjectID, err))
		}
		chatThread.ProjectID = scopedProjectID
	}

	// B5 Slice 1 — shared TryLock + THREAD_BUSY envelope helpers.
	// Surface-specific only in the human phrase (Canvas Agent vs
	// general message); the wire-shape stays identical so the FE's
	// THREAD_BUSY dispatcher works the same for every surface.
	turnLock, locked := workagentService.TryAcquireTurnLock(chatThread.UUID)
	if !locked {
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result: workagentService.BuildThreadBusyPayload(
				"Another Canvas Agent message is currently being processed for this conversation. Please wait for it to finish.",
			),
		})
		return
	}
	defer turnLock.Release()

	// ── Reserve credits ──────────────────────────────────────────────────
	creditCost := turn.Cost()
	// MintIdempotencyKey reads X-Canvas-Request-Id (per Surface) and
	// prefixes with canvas_agent: to scope across the three canvas
	// tools (canvas_chat / canvas_optimize_prompt / canvas_agent)
	// that share the same client header. Falls back to a UnixNano +
	// atomic-counter shape on missing header. See helper docs.
	idempotencyKey := workagentService.MintIdempotencyKey(c, surface, uid, &canvasAgentIdempotencySeq)
	var reservationID uint
	if err := db.Transaction(func(tx *gorm.DB) error {
		res, err := reservationSvc.Reserve(tx, accountService.ReservationRequest{
			UID:            int(uid),
			Tool:           surface.Tool(),
			IdempotencyKey: idempotencyKey,
			Reserved:       creditCost,
			TTL:            reservationTTL,
			Remark:         "canvas agent chat",
			// P1 #6 slice 2 — gate the canvas project budget cap (if set
			// on the project). scopedProjectID is 0 for non-canvas-scoped
			// agent threads and the gate becomes a no-op in that case.
			ProjectID: uint(scopedProjectID),
		})
		if err != nil {
			return err
		}
		if !res.Created {
			if res.Reservation.IsTerminal() {
				return errCanvasAgentRequestAlreadyProcessed
			}
			return accountService.ErrReservationInProgress
		}
		reservationID = res.Reservation.Id
		return nil
	}); err != nil {
		if errors.Is(err, accountService.ErrReservationInProgress) {
			sendEvent(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result: workagentService.BuildThreadBusyPayload(
					"This Canvas Agent request is already being processed. Please wait for it to finish.",
				),
			})
			return
		}
		if errors.Is(err, errCanvasAgentRequestAlreadyProcessed) {
			duplicate := map[string]interface{}{
				"type":     "result",
				"subtype":  "duplicate_completed",
				"is_error": false,
				"code":     "REQUEST_ALREADY_PROCESSED",
				"message":  "This Canvas Agent request was already processed. Reloading the thread will show the saved result if it was persisted.",
			}
			duplicateData, _ := json.Marshal(duplicate)
			event := workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result:    json.RawMessage(duplicateData),
			}
			event.ConversationID = chatThread.UUID
			sendEvent(event)
			return
		}
		if errors.Is(err, accountService.ErrReservationReplayConflict) {
			conflict := map[string]interface{}{
				"type":      "result",
				"subtype":   "idempotency_conflict",
				"is_error":  true,
				"code":      "RESERVATION_REPLAY_CONFLICT",
				"retryable": false,
				"error":     "This request id was already used for different credit parameters.",
			}
			conflictData, _ := json.Marshal(conflict)
			sendEvent(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result:    json.RawMessage(conflictData),
			})
			return
		}
		if errors.Is(err, accountService.ErrInsufficientCredits) {
			insufficient := map[string]interface{}{
				"type":            "result",
				"subtype":         "insufficient_credits",
				"is_error":        true,
				"error":           "Insufficient credits to run Canvas Agent.",
				"code":            "INSUFFICIENT_CREDITS",
				"creditsRequired": creditCost,
			}
			insufficientData, _ := json.Marshal(insufficient)
			sendEvent(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result:    json.RawMessage(insufficientData),
			})
			return
		}
		if errors.Is(err, projectService.ErrBudgetExceeded) {
			budgetExceeded := map[string]interface{}{
				"type":            "result",
				"subtype":         "budget_exceeded",
				"is_error":        true,
				"error":           "Canvas project budget cap reached. Increase the cap in project settings or run this prompt outside the project.",
				"code":            "BUDGET_EXCEEDED",
				"creditsRequired": creditCost,
			}
			budgetExceededData, _ := json.Marshal(budgetExceeded)
			sendEvent(workagentModel.AgentSSEEvent{
				Type:      workagentModel.AgentEventDone,
				MessageID: tempMessageID,
				Result:    json.RawMessage(budgetExceededData),
			})
			return
		}
		globals.Error(fmt.Sprintf("[CanvasAgent] Reserve failed for user %d (cost %d): %v", uid, creditCost, err))
		serverErr := map[string]interface{}{
			"type":     "result",
			"subtype":  "server_error",
			"is_error": true,
			"error":    "Server error",
		}
		serverErrData, _ := json.Marshal(serverErr)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    json.RawMessage(serverErrData),
		})
		return
	}

	// Both closures share an internal settled flag so explicit refunds
	// + the deferred release/finalize fight over who wins; only the
	// FIRST call commits to the DB. See workagentService.ReservationClosures
	// for the contract.
	releaseReservation, finalizeReservation := workagentService.ReservationClosures(
		db, reservationSvc, reservationID, creditCost, int(uid), "[CanvasAgent]",
	)

	// Finalize reservation on success; release on any failure path.
	// Canvas hasn't yet migrated to token-based settle (Phase: future),
	// so we pass the reservation ceiling to finalize — that matches
	// the pre-refactor behaviour. The workagent path computes actuals
	// from total_cost_usd; canvas can follow when the cost extraction
	// + UI estimator are aligned for canvas-specific flows.
	shouldFinalize := true
	defer func() {
		if shouldFinalize {
			finalizeReservation(creditCost)
		} else {
			releaseReservation("agent_failed")
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			shouldFinalize = false
			releaseReservation("panic")
			panic(r)
		}
	}()

	// ── Select account & initialize client ────────────────────────────────
	modelTier := ""
	if m, ok := req.Metadata["modelTier"]; ok {
		if mt, ok := m.(string); ok {
			modelTier = mt
		}
	}

	accountPool := workagentService.GetAgentAccountPool()
	selectedAccount, err := accountPool.GetAccountForModelTier(modelTier, creditCost)
	if err != nil {
		shouldFinalize = false
		globals.Error(fmt.Sprintf("[CanvasAgent] No account for tier '%s': %v", modelTier, err))
		recordCanvasAgentError(c.Request.Context(), chatThread, fmt.Errorf("no account for tier %q: %w", modelTier, err), uid)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}

	// Build canvas-specific system prompt via the surface turn so the
	// canvas-context injection is owned by canvas.Surface, not
	// inlined here. Future surface registry will dispatch this same
	// call regardless of agent_mode.
	canvasSystemPrompt := turn.BuildSystemPrompt()

	agentClient, _, err := agentManager.BuildClientForAccount(selectedAccount)
	if err != nil {
		shouldFinalize = false
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to build client with account %d (%s): %v",
			selectedAccount.ID, selectedAccount.Name, err))
		recordCanvasAgentError(c.Request.Context(), chatThread,
			fmt.Errorf("build client (account %d %s): %w", selectedAccount.ID, selectedAccount.Name, err), uid)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}

	// ── Prepare workspace + attachments ───────────────────────────────────
	threadWorkspace, err := agentManager.EnsureThreadWorkspace(int(uid), chatThread.UUID)
	if err != nil {
		shouldFinalize = false
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to prepare workspace: %v", err))
		recordCanvasAgentError(c.Request.Context(), chatThread,
			fmt.Errorf("ensure thread workspace: %w", err), uid)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}

	agentFiles, err := turn.PrepareAttachments(threadWorkspace)
	if err != nil {
		shouldFinalize = false
		globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to prepare attachments: %v", err))
		attachmentErr := map[string]interface{}{
			"type":     "result",
			"subtype":  "attachment_failed",
			"is_error": true,
			"error":    "Failed to prepare one or more attachments for Canvas Agent.",
			"code":     "ATTACHMENT_PREPARE_FAILED",
		}
		attachmentErrData, _ := json.Marshal(attachmentErr)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    json.RawMessage(attachmentErrData),
		})
		return
	}

	globals.Info(fmt.Sprintf("[CanvasAgent] Starting: uid=%d, thread=%s, elements=%d, skill=%s, attachments=%d",
		uid, chatThread.UUID, len(req.Context.Elements), req.Context.Skill, len(agentFiles)))

	// ── Create context with the timeout snapshot used by Reserve ─────────
	agentCtx, agentCancel, cleanupAgentContext := setupCanvasAgentContext(c, uid, fmt.Sprintf("%d", chatThread.Id), uint(chatThread.Id), agentTimeout)
	defer cleanupAgentContext()

	sseConn := workagentService.NewSSEConnection(c, uid, fmt.Sprintf("%d", chatThread.Id), agentTimeout)
	sseManager := workagentService.GetGlobalSSEManager()
	if err := sseManager.Register(sseConn); err != nil {
		shouldFinalize = false
		code := "SSE_LIMIT"
		message := "Too many active Canvas Agent streams. Please close another stream and try again."
		if errors.Is(err, workagentService.ErrPerUserSSELimit) {
			code = "SSE_LIMIT_PER_USER"
		}
		limitPayload := map[string]interface{}{
			"type":     "result",
			"subtype":  "sse_limit",
			"is_error": true,
			"error":    message,
			"code":     code,
		}
		limitData, _ := json.Marshal(limitPayload)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    json.RawMessage(limitData),
		})
		sseConn.Close()
		return
	}
	emitter.SetConnection(sseConn)
	defer sseManager.Unregister(sseConn.ID)

	workspaceRoot := agentManager.WorkspaceRoot()

	// ── Build AgentCallbacks (same pattern as WorkAgent) ──────────────────
	callbacks := workagentService.AgentCallbacks{
		OnMessage: func(messageType string, message json.RawMessage, messageIndex int) {
			event := workagentModel.NewMessageEvent(tempMessageID, messageType, messageIndex, workagentService.SanitizeAgentJSON(message, workspaceRoot))
			if !sendEvent(event) {
				agentCancel()
			}
		},

		OnBlock: func(messageType string, block json.RawMessage, messageIndex int, blockIndex int) {
			event := workagentModel.NewBlockEvent(tempMessageID, messageIndex, blockIndex, workagentService.SanitizeAgentJSON(block, workspaceRoot))
			if !sendEvent(event) {
				agentCancel()
			}
		},

		OnDone: func(result json.RawMessage) {
			result = turn.EnrichResult(result, uid)
			event := workagentModel.NewDoneEvent(tempMessageID, workagentService.SanitizeAgentJSON(result, workspaceRoot))
			event.ConversationID = chatThread.UUID
			if !sendEvent(event) {
				agentCancel()
			}
		},
	}

	// ── Execute conversation with retry ──────────────────────────────────
	// canvas surface doesn't yet route through the workagent
	// preflight gate (canvas mode has its own prompt assembly via
	// canvasSystemPrompt as customPrompt). Empty systemAdditions
	// preserves the canvas-specific prompt verbatim.
	conversation, newSessionID, err := agentClient.ProcessAgentConversationWithRetry(
		agentCtx,
		userMessage,
		fmt.Sprintf("%d", chatThread.Id),
		chatThread.AgentSessionID,
		threadWorkspace,
		agentFiles,
		callbacks,
		canvas.CanvasAgentMode,
		canvasSystemPrompt,
		selectedAccount,
		"", // systemAdditions — canvas surface skips the M5 gate for now
	)

	if err != nil {
		shouldFinalize = false
		globals.Error(fmt.Sprintf("[CanvasAgent] Conversation error: %v", err))
		recordCanvasAgentError(c.Request.Context(), chatThread,
			fmt.Errorf("agent conversation: %w", err), uid)
		sendEvent(workagentModel.AgentSSEEvent{
			Type:      workagentModel.AgentEventDone,
			MessageID: tempMessageID,
			Result:    canvas.ErrorResultPayload(),
		})
		return
	}

	// Client disconnected (or request-level timeout fired) while the
	// agent was still running — by the time ProcessAgentConversationWithRetry
	// returns, the user can't see the result. Don't charge for work the
	// user never received. The original mirror target
	// (canvas_chat_agent_api.go) was retired in #15 — this is now the
	// canonical implementation of the cancel-without-charge contract.
	if ctxErr := c.Request.Context().Err(); ctxErr != nil {
		shouldFinalize = false
		globals.Info(fmt.Sprintf("[CanvasAgent] Client disconnected before agent completed (uid=%d, err=%v); refunding", uid, ctxErr))
		return
	}

	// ── Check result for errors ──────────────────────────────────────────
	if conversation != nil && len(conversation.Result) > 0 {
		var resultCheck struct {
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal(conversation.Result, &resultCheck) == nil && resultCheck.IsError {
			shouldFinalize = false
			globals.Error(fmt.Sprintf("[CanvasAgent] Agent returned error: %s", resultCheck.Result))
			recordCanvasAgentError(c.Request.Context(), chatThread,
				fmt.Errorf("agent reported error: %s", resultCheck.Result), uid)
			return
		}
	}

	// ── Update session ID ────────────────────────────────────────────────
	if newSessionID != "" && newSessionID != chatThread.AgentSessionID {
		now := time.Now()
		if err := globals.GraDBs["system"].WithContext(c.Request.Context()).
			Model(&workagentModel.ChatThread{}).
			Where("id = ?", chatThread.Id).
			Updates(map[string]interface{}{
				"agent_session_id":         newSessionID,
				"agent_session_created_at": &now,
			}).Error; err != nil {
			globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to update session ID: %v", err))
		} else {
			globals.Info(fmt.Sprintf("[CanvasAgent] Session updated: %s -> %s", chatThread.AgentSessionID, newSessionID))
		}
	}

	// ── Save conversation to database ────────────────────────────────────
	workagentService.SanitizeAgentConversation(conversation, workspaceRoot)
	conversationJSON, err := json.Marshal(conversation)
	if err != nil {
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to marshal conversation: %v", err))
		shouldFinalize = false
		return
	}
	sanitizedConversationJSON := workagentService.ReplaceWorkspacePaths(string(conversationJSON), workspaceRoot)

	// Extract summary
	summary := "[Canvas Agent conversation]"
	if len(conversation.Messages) > 0 {
		summary = workagentService.GetContentSummary(conversation.Messages[len(conversation.Messages)-1])
	}
	if strings.TrimSpace(summary) == "" {
		summary = "[Canvas Agent conversation]"
	}

	message := workagentModel.ChatMessage{
		UUID:              conversation.ID,
		ThreadID:          int(chatThread.Id),
		UID:               int(uid),
		UserText:          strings.TrimSpace(userMessage),
		AIText:            summary,
		ContentType:       "agent_conversation",
		StructuredContent: sanitizedConversationJSON,
		ChatMode:          string(workagentModel.ChatModeAgent),
		Model:             modelTier,
		// P0-billing #13: persist the same idempotency key we used to
		// reserve credits, so a duplicate POST that hits a terminal
		// reservation can be replayed (see workagent's saveAgentConversation
		// + replayProcessedAgentTurn for the symmetric path). Without this,
		// the canvas duplicate_completed return at canvas_agent_api.go:454-470
		// has no saved conversation to replay — the user paid for media
		// they cannot retrieve until they manually reload the thread, and
		// only IF the message already happened to write before the dup
		// fired. This stores the key alongside the conversation that
		// proves the reservation cleared.
		MessageIdempotencyKey: &idempotencyKey,
	}

	if err := globals.GraDBs["system"].WithContext(c.Request.Context()).Create(&message).Error; err != nil {
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to save message: %v", err))
		recordCanvasAgentError(c.Request.Context(), chatThread,
			fmt.Errorf("save canvas agent message: %w", err), uid)
		shouldFinalize = false
		return
	}
	globals.Info(fmt.Sprintf("[CanvasAgent] Saved: conversationID=%s, messageID=%d", conversation.ID, message.Id))

	// ── Update thread name (first message) ───────────────────────────────
	if threadCreated && summary != "[Canvas Agent conversation]" {
		threadName := canvas.TruncateString(summary, 100)
		if err := globals.GraDBs["system"].WithContext(c.Request.Context()).
			Model(&workagentModel.ChatThread{}).
			Where("id = ?", chatThread.Id).
			Update("name", threadName).Error; err != nil {
			globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to update thread name: %v", err))
		}
	}

	// Update thread timestamp and message count. Use gorm.Expr to
	// do an atomic increment — read-modify-write against the
	// closure-captured `chatThread.MessageCount` was racey: two
	// concurrent agent turns on the same thread (multi-tab, retry
	// after disconnect) both read the same stale value and wrote
	// back N+1, losing one increment. Surface the error so a
	// failed update gets logged instead of disappearing.
	if err := globals.GraDBs["system"].WithContext(c.Request.Context()).
		Model(&workagentModel.ChatThread{}).
		Where("id = ?", chatThread.Id).
		Updates(map[string]interface{}{
			"updated_at":    time.Now(),
			"message_count": gorm.Expr("message_count + ?", 1),
			"msg_preview":   canvas.TruncateString(userMessage, 200),
		}).Error; err != nil {
		globals.Warn(fmt.Sprintf("[CanvasAgent] Failed to update thread timestamp/count: %v", err))
	}

	duration := int(time.Since(startTime).Seconds())
	record := &model.UsageRecord{
		UID:             int(uid),
		FeatureType:     model.FEATURE_TYPE_AI_AGENT,
		RecordID:        0,
		CreditsUsed:     creditCost,
		DurationSeconds: duration,
		IP:              c.ClientIP(),
		DeviceInfo:      c.GetHeader("User-Agent"),
		Status:          model.STATUS_SUCCESS,
	}
	if err := globals.GraDBs["system"].WithContext(c.Request.Context()).Create(record).Error; err != nil {
		globals.Error(fmt.Sprintf("[CanvasAgent] Failed to create usage record: %v", err))
	}
	accountPool.RecordTokenCreditUsage(selectedAccount.ID, creditCost, time.Now())
	globals.Info(fmt.Sprintf("[CanvasAgent] Complete: uid=%d, duration=%ds, messages=%d",
		uid, duration, len(conversation.Messages)))
}
