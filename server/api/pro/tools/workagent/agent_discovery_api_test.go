package workagent

// agent_discovery_api_test.go — M1 question-form submission coverage.
// Same shape as agent_direction_api_test.go: verify the IDOR posture,
// the binding contract, and the synthetic row that downstream
// preflight reads.

import (
	"net/http"
	"strings"
	"testing"

	workagentService "server/service/tools/workagent"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildDiscoveryEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/threads/:id/discovery", withClaims(uid), api.SubmitDiscoveryAnswers)
	return r
}

func TestSubmitDiscoveryAnswers_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "exec",
			"tone":     "modern_minimal",
			"scale":    "medium",
		},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"persisted":true`) {
		t.Errorf("body should signal persisted=true, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"answers":3`) {
		t.Errorf("body should report answer count, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"pass_mode":"draft"`) {
		t.Errorf("body should report pass_mode=draft, got %q", w.Body.String())
	}

	// Synthetic row must carry the kind preflight scans for.
	repo := workagentService.DefaultMessageRepository()
	threadID := uintFromStr(t, threadIDStr)
	msg, err := repo.FindLatestDiscoveryAnswers(threadID)
	if err != nil || msg == nil {
		t.Fatalf("expected discovery_form_result row, got msg=%v err=%v", msg, err)
	}
	if !strings.Contains(msg.Metadata, `"form_id":"ppt"`) {
		t.Errorf("metadata should carry form_id, got %q", msg.Metadata)
	}
	if !strings.Contains(msg.Metadata, `"audience":"exec"`) {
		t.Errorf("metadata should carry user answers, got %q", msg.Metadata)
	}
	// Default skip_reason for user submission path
	if !strings.Contains(msg.Metadata, `"skip_reason":"user_submitted"`) {
		t.Errorf("metadata should default skip_reason to user_submitted, got %q", msg.Metadata)
	}
	passMode, err := repo.FindMostRecentByMetadataKind(threadID, "workagent_pass_mode")
	if err != nil || passMode == nil {
		t.Fatalf("expected workagent_pass_mode row, got msg=%v err=%v", passMode, err)
	}
	if !strings.Contains(passMode.Metadata, `"mode":"draft"`) {
		t.Errorf("pass mode metadata should switch to draft, got %q", passMode.Metadata)
	}
	if !strings.Contains(passMode.Metadata, `"source":"question_form"`) {
		t.Errorf("pass mode metadata should carry question_form source, got %q", passMode.Metadata)
	}
}

func TestSubmitDiscoveryAnswers_PersistsSkipReason(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "public",
			"tone":     "modern_minimal",
			"scale":    "short",
		},
		"skip_reason": "user_skipped",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	repo := workagentService.DefaultMessageRepository()
	threadID := uintFromStr(t, threadIDStr)
	msg, _ := repo.FindLatestDiscoveryAnswers(threadID)
	if msg == nil || !strings.Contains(msg.Metadata, `"skip_reason":"user_skipped"`) {
		t.Errorf("skip_reason override not persisted; metadata=%v", msg)
	}
}

func TestSubmitDiscoveryAnswers_CrossTenantReturns404(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 99)  // owned by 99
	engine := buildDiscoveryEngine(t, 42) // caller is 42

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "exec",
			"tone":     "modern_minimal",
			"scale":    "medium",
		},
	})

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 on cross-tenant; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitDiscoveryAnswers_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildDiscoveryEngine(t, 0)
	w := postJSON(engine, "/threads/1/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "exec",
			"tone":     "modern_minimal",
			"scale":    "medium",
		},
	})

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestSubmitDiscoveryAnswers_BadRequestOnEmptyAnswers(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	// answers omitted → binding:"required" fails
	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on missing answers", w.Code)
	}
}

func TestSubmitDiscoveryAnswers_BadRequestOnEmptyMap(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	// Empty map IS bound (Go zero-value is fine for the binding tag),
	// so the explicit len==0 guard catches it. Distinct from missing-
	// key 400 to give the client a clearer error.
	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on empty answers map", w.Code)
	}
}

func TestSubmitDiscoveryAnswers_BadRequestOnMissingFormID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"answers": map[string]string{"audience": "exec"},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on missing form_id", w.Code)
	}
	if !strings.Contains(w.Body.String(), "form_id is required") {
		t.Errorf("body should explain missing form_id, got %q", w.Body.String())
	}
}

func TestSubmitDiscoveryAnswers_BadRequestOnInvalidOption(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "not-a-real-option",
			"tone":     "modern_minimal",
			"scale":    "medium",
		},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on invalid option; body=%s", w.Code, w.Body.String())
	}
}

func TestSubmitDiscoveryAnswers_BadRequestOnMissingRequiredAnswer(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	threadIDStr := seedThread(t, db, 42)
	engine := buildDiscoveryEngine(t, 42)

	w := postJSON(engine, "/threads/"+threadIDStr+"/discovery", map[string]any{
		"form_id": "ppt",
		"answers": map[string]string{
			"audience": "exec",
			"tone":     "modern_minimal",
		},
	})

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 on missing required answer; body=%s", w.Code, w.Body.String())
	}
}
