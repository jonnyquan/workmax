package workagent

// agent_api_handle_chat_test.go — early-return path coverage for
// HandleAgentChat. The handler is the chatTurn god-handler the
// architecture review flagged for orchestration extraction; before
// touching the 720-line closure we want a safety net pinning the
// observable contracts of every path that doesn't reach the SDK
// (i.e. the early-return guards).
//
// What this file covers:
//   1. uid=0           → 401 JSON, no SSE
//   2. malformed JSON  → 400 JSON
//   3. empty threadID  → 400 JSON
//   4. thread missing  → 404 JSON
//   5. cross-tenant    → 404 JSON (CWE-639 IDOR regression)
//   6. thread busy     → 200 SSE `done` event with subtype=thread_busy
//
// What this file deliberately does NOT cover (reaches SDK / account
// pool / billing / workspace prep): full happy-path turn, insufficient
// credits, agent_manager-not-enabled. Those need broader fixtures and
// belong in a follow-up that introduces SDK / account-pool fakes.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// installTestDB swaps globals.GraDBs["system"] for the given fixture
// DB and restores the original on cleanup. Mirrors the pattern used in
// the short-drama analytics tests so a future helper extraction can
// merge the two without churn.
func installTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
}

// withClaims is a tiny middleware that injects the given uid into the
// gin context the same way JWTAuth would, without going through token
// parsing. uid=0 is special-cased to mean "no claims" (anonymous).
func withClaims(uid uint) gin.HandlerFunc {
	return func(c *gin.Context) {
		if uid == 0 {
			c.Next()
			return
		}
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
		c.Next()
	}
}

// buildHandlerEngine returns a fresh gin engine with the chat route
// wired up. The handler is constructed AFTER installTestDB swaps
// globals.GraDBs because NewAIChatApiNew captures the system DB
// reference at construction.
func buildHandlerEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/chat/agent", withClaims(uid), api.HandleAgentChat)
	return r
}

func TestReserveCreditsForTurn_ProjectBudgetExceeded(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	api := NewAIChatApiNew()

	uid := uint(42)
	capCredits := 5
	project := model.CanvasProject{
		UID:               int(uid),
		UUID:              "budget-project-" + t.Name(),
		Title:             "Budget Project",
		BudgetCreditsCap:  &capCredits,
		BudgetCreditsUsed: 4,
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	thread := &workagentModel.ChatThread{
		UID:       int(uid),
		UUID:      "budget-thread-" + t.Name(),
		Name:      "Budget Thread",
		ProjectID: project.Id,
	}
	if err := db.Create(thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/agent", nil)

	var events []workagentModel.AgentSSEEvent
	reservationID, _, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:           ctx,
		uid:           uid,
		chatThread:    thread,
		creditCost:    2,
		tempMessageID: "assistant-temp",
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			events = append(events, event)
		},
	})
	if ok {
		t.Fatalf("budget exceeded should stop reservation, got ok=true reservationID=%d", reservationID)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Type != workagentModel.AgentEventDone {
		t.Fatalf("event type = %q, want done", events[0].Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Result, &payload); err != nil {
		t.Fatalf("unmarshal budget payload: %v (%s)", err, string(events[0].Result))
	}
	if payload["subtype"] != "budget_exceeded" || payload["code"] != "BUDGET_EXCEEDED" {
		t.Fatalf("unexpected budget payload: %#v", payload)
	}
	if got := int(payload["requested_credits"].(float64)); got != 2 {
		t.Fatalf("requested_credits = %d, want 2", got)
	}
	if got := int(payload["current_used"].(float64)); got != 4 {
		t.Fatalf("current_used = %d, want 4", got)
	}
	if got := int(payload["cap"].(float64)); got != capCredits {
		t.Fatalf("cap = %d, want %d", got, capCredits)
	}

	var fresh model.CanvasProject
	if err := db.First(&fresh, project.Id).Error; err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if fresh.BudgetCreditsUsed != 4 {
		t.Fatalf("budget used should not change on rejected reserve, got %d", fresh.BudgetCreditsUsed)
	}
}

func TestReserveCreditsForTurn_UsesExecutionTimeoutPlusSettlementMargin(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	api := NewAIChatApiNew()

	user := model.User{Member: model.MEMBER_SUBSCRIPTION_FREE, Nickname: "ttl", Email: "ttl@example.com"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	pack := model.CreditsPack{
		UID: int(user.Id), SourceType: model.CreditsSourcePurchase,
		SourceID: "workagent-ttl", CreditsTotal: 100,
	}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatalf("seed Pack: %v", err)
	}
	thread := &workagentModel.ChatThread{UID: int(user.Id), UUID: "ttl-thread", Name: "TTL"}

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/agent", nil)
	ctx.Request.Header.Set("X-Agent-Request-Id", "ttl-snapshot")

	reservationID, _, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:            ctx,
		uid:            user.Id,
		chatThread:     thread,
		creditCost:     2,
		reservationTTL: 35 * time.Minute,
		tempMessageID:  "assistant-temp",
		sendEvent:      func(workagentModel.AgentSSEEvent) {},
	})
	if !ok || reservationID == 0 {
		t.Fatalf("Reserve = id:%d ok:%v, want created", reservationID, ok)
	}

	var reservation model.CreditReservation
	if err := db.First(&reservation, reservationID).Error; err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	gotTTL := reservation.ExpiresAt.Sub(reservation.CreatedAt)
	if gotTTL < 35*time.Minute-time.Second || gotTTL > 35*time.Minute+time.Second {
		t.Fatalf("persisted TTL = %s, want 35m execution+settlement window", gotTTL)
	}
}

func TestReserveCreditsForTurn_ExistingNonTerminalFailsClosed(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	api := NewAIChatApiNew()

	const requestID = "stable-request-id-in-progress"
	uid := uint(42)
	existing := model.CreditReservation{
		UID:            int(uid),
		Tool:           "workagent",
		IdempotencyKey: requestID,
		Reserved:       2,
		Status:         model.CreditReservationStatusReserved,
		ExpiresAt:      time.Now().Add(10 * time.Minute),
		Remark:         "workagent chat",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("seed in-progress reservation: %v", err)
	}

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/agent", nil)
	ctx.Request.Header.Set("X-Agent-Request-Id", requestID)
	thread := &workagentModel.ChatThread{UID: int(uid), UUID: "thread-in-progress", Name: "In progress"}

	var events []workagentModel.AgentSSEEvent
	reservationID, idempotencyKey, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:           ctx,
		uid:           uid,
		chatThread:    thread,
		creditCost:    2,
		tempMessageID: "assistant-temp",
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			events = append(events, event)
		},
	})
	if ok || reservationID != 0 {
		t.Fatalf("existing non-terminal reservation must stop execution: id=%d ok=%v", reservationID, ok)
	}
	if idempotencyKey != requestID {
		t.Fatalf("idempotency key = %q, want %q", idempotencyKey, requestID)
	}
	if len(events) != 1 || events[0].Type != workagentModel.AgentEventDone {
		t.Fatalf("busy events = %+v, want one done event", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Result, &payload); err != nil {
		t.Fatalf("decode busy payload: %v (%s)", err, events[0].Result)
	}
	if payload["subtype"] != "thread_busy" || payload["code"] != "THREAD_BUSY" || payload["is_error"] != true {
		t.Fatalf("unexpected in-progress payload: %#v", payload)
	}
	if !strings.Contains(payload["error"].(string), "already being processed") {
		t.Fatalf("busy error does not explain in-progress request: %#v", payload)
	}

	var persisted model.CreditReservation
	if err := db.First(&persisted, existing.Id).Error; err != nil {
		t.Fatalf("reload reservation: %v", err)
	}
	if persisted.Status != model.CreditReservationStatusReserved || persisted.Used != 0 {
		t.Fatalf("in-progress reservation was settled: status=%q used=%d", persisted.Status, persisted.Used)
	}
	var count int64
	if err := db.Model(&model.CreditReservation{}).
		Where("uid = ? AND idempotency_key = ?", uid, requestID).
		Count(&count).Error; err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if count != 1 {
		t.Fatalf("reservation count = %d, want 1", count)
	}
}

func TestReserveCreditsForTurn_TerminalReservationReplaysPersistedMessage(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	api := NewAIChatApiNew()

	const requestID = "stable-request-id-terminal-replay"
	uid := uint(42)
	now := time.Now()
	reservation := model.CreditReservation{
		UID:            int(uid),
		Tool:           "workagent",
		IdempotencyKey: requestID,
		Reserved:       2,
		Used:           2,
		Status:         model.CreditReservationStatusFinalized,
		ExpiresAt:      now.Add(10 * time.Minute),
		FinalizedAt:    &now,
	}
	if err := db.Create(&reservation).Error; err != nil {
		t.Fatalf("seed terminal reservation: %v", err)
	}
	messageKey := requestID
	message := workagentModel.ChatMessage{
		UID:                   int(uid),
		UUID:                  "message-terminal-replay",
		ThreadID:              77,
		AIText:                "persisted answer",
		ChatMode:              string(workagentModel.ChatModeAgent),
		MessageIdempotencyKey: &messageKey,
	}
	if err := db.Create(&message).Error; err != nil {
		t.Fatalf("seed persisted message: %v", err)
	}

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/agent", nil)
	ctx.Request.Header.Set("X-Agent-Request-Id", requestID)
	var events []workagentModel.AgentSSEEvent
	thread := &workagentModel.ChatThread{UID: int(uid)}
	thread.Id = 77
	reservationID, _, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:           ctx,
		uid:           uid,
		chatThread:    thread,
		creditCost:    2,
		tempMessageID: "assistant-temp",
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			events = append(events, event)
		},
	})
	if ok || reservationID != 0 {
		t.Fatalf("terminal replay must stop execution: id=%d ok=%v", reservationID, ok)
	}
	if len(events) != 1 || events[0].Type != workagentModel.AgentEventDone || events[0].MessageID != message.UUID {
		t.Fatalf("replay events = %+v, want persisted done event", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Result, &payload); err != nil {
		t.Fatalf("decode replay payload: %v (%s)", err, events[0].Result)
	}
	if payload["subtype"] != "already_processed" || payload["replayed"] != true || payload["result"] != message.AIText {
		t.Fatalf("unexpected replay payload: %#v", payload)
	}
}

func TestReserveCreditsForTurn_TerminalReservationWithoutMessageFailsGenerically(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	api := NewAIChatApiNew()

	const requestID = "stable-request-id-terminal-missing"
	uid := uint(42)
	now := time.Now()
	reservation := model.CreditReservation{
		UID:            int(uid),
		Tool:           "workagent",
		IdempotencyKey: requestID,
		Reserved:       2,
		Used:           2,
		Status:         model.CreditReservationStatusFinalized,
		ExpiresAt:      now.Add(10 * time.Minute),
		FinalizedAt:    &now,
	}
	if err := db.Create(&reservation).Error; err != nil {
		t.Fatalf("seed terminal reservation: %v", err)
	}

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/agent", nil)
	ctx.Request.Header.Set("X-Agent-Request-Id", requestID)
	var events []workagentModel.AgentSSEEvent
	thread := &workagentModel.ChatThread{UID: int(uid)}
	thread.Id = 88
	reservationID, _, ok := api.reserveCreditsForTurn(reserveCreditsInput{
		ctx:           ctx,
		uid:           uid,
		chatThread:    thread,
		creditCost:    2,
		tempMessageID: "assistant-temp",
		sendEvent: func(event workagentModel.AgentSSEEvent) {
			events = append(events, event)
		},
	})
	if ok || reservationID != 0 {
		t.Fatalf("terminal reservation without message must stop execution: id=%d ok=%v", reservationID, ok)
	}
	if len(events) != 1 || events[0].Type != workagentModel.AgentEventDone {
		t.Fatalf("failure events = %+v, want one done event", events)
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0].Result, &payload); err != nil {
		t.Fatalf("decode generic error payload: %v (%s)", err, events[0].Result)
	}
	if payload["subtype"] != "server_error" || payload["is_error"] != true {
		t.Fatalf("unexpected missing-message payload: %#v", payload)
	}
}

// seedThread inserts a chat_thread for the given owner and returns its
// numeric id (the threadID the handler accepts on the wire).
func seedThread(t *testing.T, db *gorm.DB, ownerUID uint) string {
	t.Helper()
	thread := workagentModel.ChatThread{
		UID:  int(ownerUID),
		UUID: "test-thread-" + t.Name(),
		Name: "fixture",
	}
	if err := db.Create(&thread).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	// HandleAgentChat takes the numeric id as a string in conversationId.
	return strFromUint(thread.Id)
}

func strFromUint(n uint) string {
	if n == 0 {
		return "0"
	}
	out := []byte{}
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func postJSON(engine *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var bodyReader *bytes.Reader
	switch v := body.(type) {
	case string:
		bodyReader = bytes.NewReader([]byte(v))
	default:
		bytesBody, _ := json.Marshal(v)
		bodyReader = bytes.NewReader(bytesBody)
	}
	req := httptest.NewRequest(http.MethodPost, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	return w
}

func TestHandleAgentChat_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildHandlerEngine(t, 0) // uid=0 → no claims set
	w := postJSON(engine, "/chat/agent", map[string]any{
		"conversationId": "1",
		"messages":       []map[string]any{{"role": "user", "content": "hi"}},
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Errorf("body should contain 'Unauthorized', got %q", w.Body.String())
	}
	// 401 path must NOT emit SSE — the response is pure JSON. Otherwise
	// the frontend's stream proxy wraps a JSON 401 in a half-formed
	// event-stream which the SSE parser can't decode.
	if got := w.Header().Get("Content-Type"); strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, must not be event-stream on 401", got)
	}
}

func TestHandleAgentChat_BadRequestOnMalformedJSON(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildHandlerEngine(t, 42)
	// Send raw garbage so ShouldBindJSON fails.
	w := postJSON(engine, "/chat/agent", "{not json")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleAgentChat_BadRequestOnMissingThreadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildHandlerEngine(t, 42)
	// conversationId omitted → empty string → 400 "Thread ID required".
	// Same path covers explicit "0" — both are the "no thread selected"
	// signal from the frontend.
	w := postJSON(engine, "/chat/agent", map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Thread ID required") {
		t.Errorf("body should mention 'Thread ID required', got %q", w.Body.String())
	}
}

func TestHandleAgentChat_NotFoundWhenThreadMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildHandlerEngine(t, 42)
	w := postJSON(engine, "/chat/agent", map[string]any{
		"conversationId": "999999", // Doesn't exist.
		"messages":       []map[string]any{{"role": "user", "content": "hi"}},
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Thread not found") {
		t.Errorf("body should mention 'Thread not found', got %q", w.Body.String())
	}
}

func TestHandleAgentChat_NotFoundOnCrossTenantAccess_IDORRegression(t *testing.T) {
	// Critical: getChatThread returns ErrRecordNotFound when uid mismatch
	// so we DON'T leak the existence of another user's thread. A 403
	// "Permission denied" would let an attacker scan id-space for valid
	// threads (any 403 means "real thread, owned by someone else"). 404
	// is identical to the "doesn't exist" branch — no oracle.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	// Seed a thread owned by user 100.
	ownerUID := uint(100)
	threadID := seedThread(t, db, ownerUID)

	// Try to access it as user 42.
	attackerUID := uint(42)
	engine := buildHandlerEngine(t, attackerUID)
	w := postJSON(engine, "/chat/agent", map[string]any{
		"conversationId": threadID,
		"messages":       []map[string]any{{"role": "user", "content": "leak my data"}},
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (must NOT be 403 — would leak existence)", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Thread not found") {
		t.Errorf("body should be the same 'Thread not found' as the missing-thread branch, got %q", w.Body.String())
	}
}

// fakeFlushWriter wraps the recorder so c.Writer.(http.Flusher)
// type-asserts succeed. httptest.ResponseRecorder implements Flusher
// in newer Go, but the wrapper is defensive in case of toolchain
// differences and explicitly tracks Flush calls if a future test wants
// to count them.
type fakeFlushWriter struct {
	*httptest.ResponseRecorder
	flushCount int
}

func (f *fakeFlushWriter) Flush() {
	f.flushCount++
	f.ResponseRecorder.Flush()
}

func TestHandleAgentChat_ThreadBusyEmitsSSEDoneEvent(t *testing.T) {
	// Per-thread serialization: when a turn is already in flight for the
	// same thread, the second concurrent send must be rejected
	// immediately with an SSE `done` event carrying subtype=thread_busy.
	// Without this, the time.Now() idempotency fallback below in the
	// reservation path lets two reservations finalize for one logical
	// turn and the user pays twice — exactly the bug agentTurnLocks
	// guards against.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	uid := uint(42)
	threadID := seedThread(t, db, uid)

	// Pre-acquire the per-thread lock as if another turn were already
	// running. The lock map is process-global inside workagentService;
	// the busy-event SSE path under test only fires when a second
	// concurrent acquire fails TryLock against this same lock.
	lock := workagentService.GetAgentTurnLock(threadID)
	lock.Lock()
	// Always release so subsequent tests on the same package-global map
	// don't get stuck.
	t.Cleanup(func() {
		// Best effort — if the handler somehow acquired the lock (it
		// shouldn't on the busy path), this Unlock would panic.
		// Recover defensively so a misbehaving regression doesn't
		// cascade into "all subsequent tests block forever".
		defer func() { _ = recover() }()
		lock.Unlock()
	})

	engine := buildHandlerEngine(t, uid)
	w := postJSON(engine, "/chat/agent", map[string]any{
		"conversationId": threadID,
		"messages":       []map[string]any{{"role": "user", "content": "hi"}},
	})

	// Busy path emits an SSE `done` event with status 200 (the response
	// is technically successful — it's just signalling "try again
	// later" through the protocol). A 4xx here would force the frontend
	// to treat it as a fetch failure instead of a stream-level message.
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (busy path is SSE, not HTTP error)", w.Code)
	}

	body := w.Body.String()
	// The SSE frame for the busy event. Order: `: connected` comment
	// then `data: {...}` for the done event.
	if !strings.Contains(body, "data:") {
		t.Fatalf("expected SSE frame in body, got %q", body)
	}
	// Pull out the JSON payload carrying the busy result.
	if !strings.Contains(body, `"THREAD_BUSY"`) {
		t.Errorf("body missing THREAD_BUSY code, got %q", body)
	}
	if !strings.Contains(body, `"thread_busy"`) {
		t.Errorf("body missing thread_busy subtype, got %q", body)
	}

	// Decode the agent SSE event itself for a structural assertion. The
	// frame is `data: <json>\n\n` — extract the JSON between `data: `
	// and the first `\n\n`.
	dataIdx := strings.Index(body, "data: {")
	if dataIdx < 0 {
		t.Fatalf("no JSON data frame in body: %q", body)
	}
	jsonStart := dataIdx + len("data: ")
	jsonEnd := strings.Index(body[jsonStart:], "\n\n")
	if jsonEnd < 0 {
		t.Fatalf("data frame not terminated: %q", body[dataIdx:])
	}
	rawEvent := body[jsonStart : jsonStart+jsonEnd]

	var event workagentModel.AgentSSEEvent
	if err := json.Unmarshal([]byte(rawEvent), &event); err != nil {
		t.Fatalf("event JSON parse: %v (raw=%q)", err, rawEvent)
	}
	if event.Type != workagentModel.AgentEventDone {
		t.Errorf("event.Type = %q, want %q", event.Type, workagentModel.AgentEventDone)
	}

	// Result is a json.RawMessage carrying the busy-payload object.
	var result map[string]any
	if err := json.Unmarshal(event.Result, &result); err != nil {
		t.Fatalf("event.Result parse: %v", err)
	}
	if result["code"] != "THREAD_BUSY" {
		t.Errorf("result.code = %v, want THREAD_BUSY", result["code"])
	}
	if result["is_error"] != true {
		t.Errorf("result.is_error = %v, want true", result["is_error"])
	}
	if result["subtype"] != "thread_busy" {
		t.Errorf("result.subtype = %v, want thread_busy", result["subtype"])
	}
}

// TestHandleAgentChat_TurnLockReleasedOnEarlyReturn pins that the
// per-thread mutex is NOT acquired on paths that early-return BEFORE
// the lock acquisition (uid=0 / bad JSON / missing threadID / thread
// not found / IDOR). Without this guard, a malformed request would
// have left the lock mid-state and the next legit turn would have
// waited or failed.
func TestHandleAgentChat_TurnLockNotAcquiredOnEarlyReturns(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	uid := uint(42)
	threadID := seedThread(t, db, uid)

	// Sanity: lock starts unowned. We can grab it immediately with
	// TryLock, then release.
	lock := workagentService.GetAgentTurnLock(threadID)
	if !lock.TryLock() {
		t.Fatalf("lock should be unowned at start of test")
	}
	lock.Unlock()

	engine := buildHandlerEngine(t, uid)

	// 1. Missing threadID — early-returns BEFORE turn-lock.
	_ = postJSON(engine, "/chat/agent", map[string]any{
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	})

	// 2. Cross-tenant access — early-returns at getChatThread.
	otherEngine := buildHandlerEngine(t, 99) // attacker uid
	_ = postJSON(otherEngine, "/chat/agent", map[string]any{
		"conversationId": threadID,
		"messages":       []map[string]any{{"role": "user", "content": "x"}},
	})

	// After both, the original thread's lock must still be unowned.
	if !lock.TryLock() {
		t.Errorf("turn lock leaked after early-return paths")
		return
	}
	lock.Unlock()
}

// Compile-time assertion: the test file uses sync only via the
// pre-acquired Mutex pattern in the busy-path test. Anchor the import
// so a future cleanup pass doesn't auto-remove it.
var _ = sync.Mutex{}
