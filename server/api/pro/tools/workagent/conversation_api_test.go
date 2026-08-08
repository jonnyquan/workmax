package workagent

// conversation_api_test.go — REST coverage for the conversation CRUD
// surface. Six handlers, six contracts to pin:
//
//   1. GetConversationByUuid     → uid-scoped UUID lookup, 4xx-shaped guards
//   2. UpdateConversationSettings → field-by-field validation rejections
//   3. SetConversationVisibility  → owner-only is_public flip
//   4. DeleteConversation         → early-return guards before SSE teardown
//   5. ClearMessages              → id-shape guards (lifecycle service stub)
//   6. ClearConversation          → id-shape guards (lifecycle service stub)
//
// IDOR posture (CWE-639): cross-tenant probes collapse to the same
// "not found" body as missing-row probes — no enumeration oracle.
//
// What this file deliberately does NOT cover:
//   - DeleteConversation / Clear* happy paths (reach into
//     ThreadLifecycleService + SSE manager + workspace teardown,
//     which need broader fakes — follow-up scope).
//   - GetConversationFiles (touches the filesystem via fileService —
//     same follow-up).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workagentModel "server/model/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// buildConversationEngine wires the six handlers under test on a fresh
// gin engine. Each route mirrors the production router path shape so
// gin path param names (`:id`, `:uuid`) match what the handlers read.
func buildConversationEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/conversations/uuid/:uuid", withClaims(uid), api.GetConversationByUuid)
	r.PUT("/conversations/:id/settings", withClaims(uid), api.UpdateConversationSettings)
	r.POST("/conversations/:id/visibility", withClaims(uid), api.SetConversationVisibility)
	r.DELETE("/conversations/:id", withClaims(uid), api.DeleteConversation)
	r.POST("/conversations/:id/messages/clear", withClaims(uid), api.ClearMessages)
	r.POST("/conversations/:id/clear", withClaims(uid), api.ClearConversation)
	return r
}

// seedConversationThread inserts a chat_thread for the owner with a
// real v4 UUID + general_agent agent_type so LoadByUUIDForOwner's
// agent_type predicate matches. Returns the row pointer so callers can
// assert against id / uuid without a second SELECT.
func seedConversationThread(t *testing.T, db *gorm.DB, ownerUID uint, name string) *workagentModel.ChatThread {
	t.Helper()
	row := &workagentModel.ChatThread{
		UID:       int(ownerUID),
		UUID:      uuid.New().String(),
		Name:      name,
		AgentMode: "ppt",
		AgentType: onlySupportedAgentType,
		IsPublic:  true,
		ProjectID: 77,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return row
}

// ---------------------------------------------------------------------
// GetConversationByUuid
// ---------------------------------------------------------------------

func TestGetConversationByUuid_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 0) // uid=0 → no claims
	w := getRequest(engine, "/conversations/uuid/"+uuid.New().String())

	// Handler uses response.FailWithMessage which returns HTTP 200
	// with a body-encoded failure — matches the rest of the file's
	// FailWithMessage assertions. The contract pinned here is "uid=0
	// is rejected before any DB I/O", not the specific status code.
	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Errorf("body should say Unauthorized, got %q", w.Body.String())
	}
}

func TestGetConversationByUuid_RejectsMalformedUUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	// Anything that doesn't shape-match a v4 UUID — sending it through
	// the DB would turn into a full string-comparison scan, and the
	// parser quirk surface is a probe channel for the column.
	w := getRequest(engine, "/conversations/uuid/not-a-uuid")

	// Same generic "Conversation not found" body as the missing-row
	// branch — must not leak that this path failed at parse time vs.
	// at DB lookup.
	if w.Code != http.StatusOK {
		// Backend uses response.FailWithMessage which returns HTTP 200
		// with success=false in the body. Some endpoints (like delete)
		// use status codes; this one stays in the body convention.
		t.Errorf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should say 'Conversation not found', got %q", w.Body.String())
	}
}

func TestGetConversationByUuid_NotFoundWhenMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	// Valid-shape UUID that doesn't exist in the DB.
	w := getRequest(engine, "/conversations/uuid/"+uuid.New().String())

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should say 'Conversation not found', got %q", w.Body.String())
	}
}

func TestGetConversationByUuid_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	// Critical: LoadByUUIDForOwner returns ErrRecordNotFound when uid
	// mismatches so we DON'T leak that another user owns the UUID. A
	// distinct "permission denied" body would let an attacker harvest
	// real UUIDs by querying random ones.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(100)
	row := seedConversationThread(t, db, ownerUID, "owned-by-100")

	attackerUID := uint(42)
	engine := buildConversationEngine(t, attackerUID)
	w := getRequest(engine, "/conversations/uuid/"+row.UUID)

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should be the same 'Conversation not found' as the missing branch, got %q", w.Body.String())
	}
}

func TestGetConversationByUuid_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "happy-thread")

	engine := buildConversationEngine(t, ownerUID)
	w := getRequest(engine, "/conversations/uuid/"+row.UUID)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if got, ok := resp.Data["uuid"].(string); !ok || got != row.UUID {
		t.Errorf("uuid = %v, want %s", resp.Data["uuid"], row.UUID)
	}
	if got, ok := resp.Data["name"].(string); !ok || got != "happy-thread" {
		t.Errorf("name = %v, want 'happy-thread'", resp.Data["name"])
	}
	if got, ok := resp.Data["projectId"].(float64); !ok || got != 77 {
		t.Errorf("projectId = %v, want 77", resp.Data["projectId"])
	}
}

// ---------------------------------------------------------------------
// UpdateConversationSettings
// ---------------------------------------------------------------------

func TestUpdateConversationSettings_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := putJSON(engine, "/conversations/abc/settings", map[string]any{
		"name": strPtr("anything"),
	})

	if !strings.Contains(w.Body.String(), "Invalid conversation ID") {
		t.Errorf("body should reject non-numeric id, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(100)
	row := seedConversationThread(t, db, ownerUID, "owned-by-100")

	attackerUID := uint(42)
	engine := buildConversationEngine(t, attackerUID)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"name": strPtr("hijacked"),
	})

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should not leak existence on cross-tenant, got %q", w.Body.String())
	}

	// Belt-and-braces: the row must still hold its original name. If a
	// future regression dropped the uid predicate from
	// ApplyUpdatesForOwner, the body would still say "not found" if the
	// handler short-circuited on LoadByIDForOwner; this catches the
	// case where the LOAD passed but the UPDATE was unscoped.
	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load row: %v", err)
	}
	if after.Name != "owned-by-100" {
		t.Errorf("name = %q, attacker mutated row across tenants", after.Name)
	}
}

func TestUpdateConversationSettings_NotFoundWhenMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := putJSON(engine, "/conversations/999999/settings", map[string]any{
		"name": strPtr("anything"),
	})

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should say 'Conversation not found', got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsEmptyName(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"name": strPtr("   "), // Whitespace-only — TrimSpace makes it empty.
	})

	if !strings.Contains(w.Body.String(), "cannot be empty") {
		t.Errorf("body should reject blank name, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsOversizedName(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	// 201 bytes — past the 200-cap, no need to inflate to MB to trip
	// the boundary.
	bigName := strings.Repeat("a", 201)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"name": &bigName,
	})

	if !strings.Contains(w.Body.String(), "too long") {
		t.Errorf("body should reject oversized name, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsContextCountOutOfRange(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	out := 51
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"contextCount": &out,
	})

	if !strings.Contains(w.Body.String(), "Invalid context count") {
		t.Errorf("body should reject >50 context count, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsOversizedSystemPrompt(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	prompt := strings.Repeat("x", maxSystemPromptBytes+1)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"systemPrompt": &prompt,
	})

	if !strings.Contains(w.Body.String(), "exceeds") {
		t.Errorf("body should reject oversized system prompt, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsNaNTemperature(t *testing.T) {
	// Critical: math.IsNaN must run BEFORE the range check, because
	// NaN comparisons return false — a naive `< 0 || > 2` would let a
	// NaN payload through and corrupt every downstream LLM call.
	// Encode NaN via the raw JSON body — encoding/json refuses to
	// marshal float64 NaN, so go straight to the wire format.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	rawBody := `{"temperature": NaN}`
	// Send malformed JSON (NaN isn't valid JSON) — ShouldBindJSON's
	// stdlib decoder rejects this, returning the BadRequest body.
	// That's actually the right defence: the wire never carries NaN
	// in the first place. Keep the test pinned so a future "let's
	// accept loose JSON" change can't silently regress.
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", rawBody)

	if !strings.Contains(w.Body.String(), "Invalid request format") {
		t.Errorf("body should reject NaN at the JSON parse boundary, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsTemperatureOutOfRange(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	hot := 2.5
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"temperature": &hot,
	})

	if !strings.Contains(w.Body.String(), "temperature") {
		t.Errorf("body should reject >2 temperature, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsMaxTokensOutOfRange(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	tiny := 50
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"maxTokens": &tiny,
	})

	if !strings.Contains(w.Body.String(), "maxTokens") {
		t.Errorf("body should reject <100 maxTokens, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_RejectsUnknownAgentMode(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"agentMode": strPtr("wat-mode"),
	})

	if !strings.Contains(w.Body.String(), "Invalid agentMode") {
		t.Errorf("body should reject unknown agentMode, got %q", w.Body.String())
	}
}

func TestUpdateConversationSettings_HappyPath_NameUpdates(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")

	engine := buildConversationEngine(t, ownerUID)
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"name": strPtr("after"),
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	// Verify persisted state — the response body is a success ack,
	// the source of truth is the DB row.
	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load row: %v", err)
	}
	if after.Name != "after" {
		t.Errorf("name = %q, want 'after'", after.Name)
	}
}

func TestUpdateConversationSettings_AgentModeChangeResetsSDKSession(t *testing.T) {
	// Critical: a mode switch MUST clear agent_session_id so the next
	// turn bootstraps fresh under the new prompt. The chat path
	// (HandleAgentChat) does the same reset; if only one of the two
	// resets is in place, switching mode in the Settings modal lags
	// chat-driven switches by exactly one turn. Pin both halves.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "before")
	// Seed a non-empty session_id so we can prove it gets cleared.
	if err := db.Model(&workagentModel.ChatThread{}).
		Where("id = ?", row.Id).
		Update("agent_session_id", "stale-session-xxx").Error; err != nil {
		t.Fatalf("seed agent_session_id: %v", err)
	}

	engine := buildConversationEngine(t, ownerUID)
	// "ppt" is the seeded default — switch to "logo" to trigger reset.
	w := putJSON(engine, "/conversations/"+uintToStr(row.Id)+"/settings", map[string]any{
		"agentMode": strPtr("logo"),
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load row: %v", err)
	}
	if after.AgentMode != "logo" {
		t.Errorf("agent_mode = %q, want 'logo'", after.AgentMode)
	}
	if after.AgentSessionID != "" {
		t.Errorf("agent_session_id = %q, want cleared", after.AgentSessionID)
	}
}

// ---------------------------------------------------------------------
// SetConversationVisibility
// ---------------------------------------------------------------------

func TestSetConversationVisibility_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 0)
	w := postJSON(engine, "/conversations/1/visibility", map[string]any{"isPublic": false})

	if w.Code != http.StatusOK {
		// FailWithMessage path → 200 with success=false body. Same
		// shape as the other unauthorized branches in this file. Keep
		// the assertion on the body so a future "use 401 codes
		// everywhere" change is a single-line edit here.
	}
	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Errorf("body should say Unauthorized, got %q", w.Body.String())
	}
}

func TestSetConversationVisibility_RejectsMissingIsPublicField(t *testing.T) {
	// Pointer-typed field means a missing key is `nil` rather than
	// false. Without this guard, a payload that forgot the `isPublic`
	// key would silently unshare the thread — the test pins the
	// explicit-required contract.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "shared")

	engine := buildConversationEngine(t, ownerUID)
	w := postJSON(engine, "/conversations/"+uintToStr(row.Id)+"/visibility", map[string]any{})

	if !strings.Contains(w.Body.String(), "isPublic is required") {
		t.Errorf("body should require explicit isPublic, got %q", w.Body.String())
	}

	// Row must remain unchanged.
	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if !after.IsPublic {
		t.Errorf("is_public = false, want true (handler should not have touched the row)")
	}
}

func TestSetConversationVisibility_NotFoundOnCrossTenant(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(100)
	row := seedConversationThread(t, db, ownerUID, "owned-by-100")

	attackerUID := uint(42)
	engine := buildConversationEngine(t, attackerUID)
	w := postJSON(engine, "/conversations/"+uintToStr(row.Id)+"/visibility", map[string]any{
		"isPublic": false,
	})

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("cross-tenant must not leak existence, got %q", w.Body.String())
	}

	// Belt-and-braces: target row must still be public.
	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if !after.IsPublic {
		t.Errorf("is_public = false, attacker flipped across tenants")
	}
}

func TestSetConversationVisibility_HappyPath_FlipsToFalse(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(42)
	row := seedConversationThread(t, db, ownerUID, "shared")

	engine := buildConversationEngine(t, ownerUID)
	w := postJSON(engine, "/conversations/"+uintToStr(row.Id)+"/visibility", map[string]any{
		"isPublic": false,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if after.IsPublic {
		t.Errorf("is_public = true, want false after flip")
	}
}

// ---------------------------------------------------------------------
// DeleteConversation — early-return guards. Happy path needs SSE +
// ThreadLifecycleService fakes which belong in a follow-up.
// ---------------------------------------------------------------------

func TestDeleteConversation_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 0)
	w := deleteRequest(engine, "/conversations/1")

	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Errorf("body should say Unauthorized, got %q", w.Body.String())
	}
}

func TestDeleteConversation_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := deleteRequest(engine, "/conversations/abc")

	if !strings.Contains(w.Body.String(), "Invalid thread ID") {
		t.Errorf("body should reject non-numeric id, got %q", w.Body.String())
	}
}

func TestDeleteConversation_NotFoundWhenMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := deleteRequest(engine, "/conversations/999999")

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("body should say 'Conversation not found', got %q", w.Body.String())
	}
}

func TestDeleteConversation_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(100)
	row := seedConversationThread(t, db, ownerUID, "owned-by-100")

	attackerUID := uint(42)
	engine := buildConversationEngine(t, attackerUID)
	w := deleteRequest(engine, "/conversations/"+uintToStr(row.Id))

	if !strings.Contains(w.Body.String(), "Conversation not found") {
		t.Errorf("cross-tenant must not leak existence, got %q", w.Body.String())
	}

	// Row must still exist after the attacker's delete attempt.
	var after workagentModel.ChatThread
	if err := db.First(&after, row.Id).Error; err != nil {
		t.Errorf("re-load: %v — attacker deleted row across tenants", err)
	}
}

// ---------------------------------------------------------------------
// ClearMessages / ClearConversation — id-shape guards only. Happy
// paths go through ThreadLifecycleService.ClearMessages/ClearThread
// which need broader fakes (workspace cleanup, message + file delete).
// ---------------------------------------------------------------------

func TestClearMessages_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := postJSON(engine, "/conversations/abc/messages/clear", map[string]any{})

	if !strings.Contains(w.Body.String(), "Invalid conversation ID") {
		t.Errorf("body should reject non-numeric id, got %q", w.Body.String())
	}
}

func TestClearConversation_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildConversationEngine(t, 42)
	w := postJSON(engine, "/conversations/abc/clear", map[string]any{})

	if !strings.Contains(w.Body.String(), "Invalid conversation ID") {
		t.Errorf("body should reject non-numeric id, got %q", w.Body.String())
	}
}

// ---------------------------------------------------------------------
// Local helpers — `strPtr` mints a *string for the nullable PUT body
// fields; `uintToStr` formats uint without a fmt.Sprintf dependency.
// Both are local to this file to avoid name collisions with the
// package-wide helpers (postJSON / putJSON / strFromUint are already
// taken by sibling test files).
// ---------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func uintToStr(n uint) string {
	if n == 0 {
		return "0"
	}
	// Reuse the existing strFromUint helper from
	// agent_api_handle_chat_test.go — same package, no allocation
	// difference, keeps both call sites in lockstep if the format
	// ever needs to widen.
	return strFromUint(n)
}

// Ensure compile-time references to the http and httptest packages
// stay even if a future edit prunes a test — the imports protect the
// other tests from import-cycle drift.
var _ = httptest.NewRecorder
