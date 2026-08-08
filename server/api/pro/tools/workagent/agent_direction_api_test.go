package workagent

// agent_direction_api_test.go — DS-2 SubmitDirectionSelection coverage.
// Mirrors the agent_api_handle_chat_test.go shape: in-memory DB +
// inline route registration + httptest. Three contracts pinned:
//
//   1. Cross-tenant POST returns 404 (CWE-639 IDOR) — leaks no
//      existence info about the target thread.
//   2. Unknown direction id returns 400 — visual-directions.yaml is
//      a closed catalog; an unknown id is a deploy drift / client bug.
//   3. Happy path returns 200 + persisted=true and the synthetic
//      visual_direction_selected metadata row is queryable on the
//      thread for subsequent preflight reads.

import (
	"net/http"
	"strings"
	"testing"

	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildDirectionEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/threads/:id/direction", withClaims(uid), api.SubmitDirectionSelection)
	return r
}

func TestSubmitDirectionSelection_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	rec := &workagentService.RecordingSink{}
	prev := workagentService.SetMetricSink(rec)
	t.Cleanup(func() { workagentService.SetMetricSink(prev) })

	threadIDStr := seedThread(t, db, 42)
	engine := buildDirectionEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{
		"direction_id": "modern_minimal",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"persisted":true`) {
		t.Errorf("body should signal persisted=true, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"direction_id":"modern_minimal"`) {
		t.Errorf("body should echo direction_id, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"basename":"modern-minimal"`) {
		t.Errorf("body should echo locked design system, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"pass_mode":"finalize"`) {
		t.Errorf("body should report pass_mode=finalize, got %q", w.Body.String())
	}

	// Verify the synthetic row landed where preflight will look for it.
	repo := workagentService.DefaultMessageRepository()
	threadID := uintFromStr(t, threadIDStr)
	msg, err := repo.FindLatestByMetadataKind(threadID, "visual_direction_selected")
	if err != nil || msg == nil {
		t.Fatalf("expected visual_direction_selected row, got msg=%v err=%v", msg, err)
	}
	if !strings.Contains(msg.Metadata, `"direction_id":"modern_minimal"`) {
		t.Errorf("metadata should carry direction_id, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"source":"user_picker"`) {
		t.Errorf("metadata should default source to user_picker, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"design_system_basename":"modern-minimal"`) {
		t.Errorf("metadata should lock design system basename, got %q", msg.Metadata)
	}
	passMode, err := repo.FindMostRecentByMetadataKind(threadID, "workagent_pass_mode")
	if err != nil || passMode == nil {
		t.Fatalf("expected workagent_pass_mode row, got msg=%v err=%v", passMode, err)
	}
	if !strings.Contains(passMode.Metadata, `"mode":"finalize"`) {
		t.Errorf("pass mode metadata should switch to finalize, got %q", passMode.Metadata)
	}
	if !strings.Contains(passMode.Metadata, `"source":"direction_picker"`) {
		t.Errorf("pass mode metadata should carry direction_picker source, got %q", passMode.Metadata)
	}
	ev := rec.FindByEvent("wa_design_system_selected")
	if ev == nil {
		t.Fatal("expected wa_design_system_selected metric")
	}
	if ev.Fields["direction_id"] != "modern_minimal" || ev.Fields["design_system_basename"] != "modern-minimal" {
		t.Fatalf("metric fields = %#v", ev.Fields)
	}
}

func TestSubmitDirectionSelection_RejectsUnknownDirectionID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDirectionEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{
		"direction_id": "imaginary_direction_id",
	})

	// PersistSelectedDirection silently rejects unknown ids; the
	// handler's post-write FindLatest check turns that silence into a
	// 400 so the client knows something went wrong.
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitDirectionSelection_RejectsUnknownDirectionIDEvenWithOldSelection(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	threadID := uintFromStr(t, threadIDStr)
	workagentService.PersistSelectedDirection(42, threadID, "modern_minimal", "user_picker")
	engine := buildDirectionEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{
		"direction_id": "imaginary_direction_id",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 even when an older selection exists; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitDirectionSelection_CrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	// Thread belongs to uid=99; caller is uid=42. The IDOR defence
	// (LoadByIDForOwner with uid in WHERE) collapses missing-row and
	// cross-tenant into the same 404 so a malicious client can't
	// enumerate thread ids by probing.
	threadIDStr := seedThread(t, db, 99)
	engine := buildDirectionEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{
		"direction_id": "modern_minimal",
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 on cross-tenant; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitDirectionSelection_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildDirectionEngine(t, 0) // no claims
	w := postJSON(engine, "/threads/1/direction", map[string]any{
		"direction_id": "modern_minimal",
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestSubmitDirectionSelection_BadRequestOnMissingDirectionID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDirectionEngine(t, 42)

	// direction_id omitted → ShouldBindJSON binding:"required" fails.
	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on missing direction_id", w.Code)
	}
}

func TestSubmitDirectionSelection_PersistsCustomSource(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDirectionEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/direction", map[string]any{
		"direction_id": "vintage_film",
		"source":       "user_skipped",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	repo := workagentService.DefaultMessageRepository()
	threadID := uintFromStr(t, threadIDStr)
	msg, _ := repo.FindLatestByMetadataKind(threadID, "visual_direction_selected")
	if msg == nil || !strings.Contains(msg.Metadata, `"source":"user_skipped"`) {
		t.Errorf("source override not persisted; metadata=%v", msg)
	}
}

func uintFromStr(t *testing.T, s string) uint {
	t.Helper()
	var n uint
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("non-numeric thread id %q", s)
		}
		n = n*10 + uint(c-'0')
	}
	return n
}
