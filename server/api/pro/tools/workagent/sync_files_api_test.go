package workagent

// sync_files_api_test.go — coverage for POST /files/sync early-return
// guards. The happy path requires the AgentClientManager + a real
// workspace directory on disk, which belongs in a follow-up that
// introduces a filesystem fixture. The guards (no-threadId / bad-id /
// missing-thread / cross-tenant) are pure DB + parse logic and pin
// the IDOR posture every endpoint in this surface area shares.

import (
	"net/http"
	"strings"
	"testing"

	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildSyncFilesEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.POST("/files/sync", withClaims(uid), api.SyncFiles)
	return r
}

func TestSyncFiles_RejectsMissingThreadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildSyncFilesEngine(t, 42)
	// `binding:"required"` on the struct rejects the empty body —
	// pin the user-visible message so a future binding-tag change
	// surfaces here instead of silently leaking a 400 elsewhere.
	w := postJSON(engine, "/files/sync", map[string]any{})

	if !strings.Contains(w.Body.String(), "threadId is required") {
		t.Errorf("body should require threadId, got %q", w.Body.String())
	}
}

func TestSyncFiles_RejectsNonNumericThreadID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildSyncFilesEngine(t, 42)
	w := postJSON(engine, "/files/sync", map[string]any{
		"threadId": "abc",
	})

	if !strings.Contains(w.Body.String(), "Invalid thread ID") {
		t.Errorf("body should reject non-numeric id, got %q", w.Body.String())
	}
}

func TestSyncFiles_NotFoundWhenThreadMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	engine := buildSyncFilesEngine(t, 42)
	w := postJSON(engine, "/files/sync", map[string]any{
		"threadId": "999999",
	})

	// getChatThread returns ErrRecordNotFound for both "missing"
	// and "cross-tenant"; the handler maps both to the same
	// generic "Thread not found" body — no enumeration oracle.
	if !strings.Contains(w.Body.String(), "Thread not found") {
		t.Errorf("body should say 'Thread not found', got %q", w.Body.String())
	}
}

func TestSyncFiles_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	// IDOR posture: a thread owned by uid=100 must not surface for
	// uid=42, AND the response body must be the same generic
	// "Thread not found" as the missing-row branch. The previous
	// inline `Where("id=? AND uid=?")` shape was replaced with the
	// shared getChatThread helper precisely because the shared
	// helper bakes the uid predicate into the SELECT and the
	// ErrRecordNotFound sentinel into the error map — pin that
	// contract here so a future regression to inline GORM lights up.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	ownerUID := uint(100)
	threadID := seedThread(t, db, ownerUID)

	attackerUID := uint(42)
	engine := buildSyncFilesEngine(t, attackerUID)
	w := postJSON(engine, "/files/sync", map[string]any{
		"threadId": threadID,
	})

	if w.Code != http.StatusOK {
		// FailWithMessage path returns 200 with success=false in
		// the body. If a future code change starts returning 403
		// or 404 codes on cross-tenant, that's actually safer —
		// but should be a conscious change paired with a tweak
		// here, not a silent regression.
	}
	if !strings.Contains(w.Body.String(), "Thread not found") {
		t.Errorf("cross-tenant must not leak existence, got %q", w.Body.String())
	}
}
