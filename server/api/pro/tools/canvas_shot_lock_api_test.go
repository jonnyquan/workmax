package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	canvasService "server/service/tools/canvas"
)

// canvas_shot_lock_api_test.go pins the wire contract the front-end's
// useShotLock hook will branch on. The contention semantics
// themselves live in shot_lock_test.go (in-memory sqlite, 18 cases);
// this file pins the error → errorCode envelope shape that the
// service errors are translated into.
//
// Why pin separately: a refactor that quietly changed
// "SHOT_LOCK_HELD_BY_OTHER" to "shot_lock_held_by_other" or that
// dropped the lockState payload from the conflict envelope would
// surface as a silent UX regression — the lock toast would lose its
// "held by user X since Y" copy and fall back to a generic message.

func init() {
	gin.SetMode(gin.TestMode)
}

type shotLockEnvelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	ErrorCode string `json:"errorCode"`
	Data      struct {
		Success   bool   `json:"success"`
		ErrorCode string `json:"errorCode"`
		LockState *struct {
			UserID     int    `json:"userId"`
			JobID      string `json:"jobId"`
			AcquiredAt int64  `json:"acquiredAt"`
		} `json:"lockState,omitempty"`
	} `json:"data"`
}

func decodeShotLockEnvelope(t *testing.T, w *httptest.ResponseRecorder) shotLockEnvelope {
	t.Helper()
	var env shotLockEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody=%s", err, w.Body.String())
	}
	return env
}

func newShotLockTestContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	return c, w
}

func TestRespondShotLockError_NotFound(t *testing.T) {
	c, w := newShotLockTestContext()
	respondShotLockError(c, canvasService.ErrShotLockNotFound, "Acquire shot lock failed")

	env := decodeShotLockEnvelope(t, w)
	if env.ErrorCode != shotLockErrorNotFound {
		t.Fatalf("errorCode = %q, want %q", env.ErrorCode, shotLockErrorNotFound)
	}
	if env.Data.ErrorCode != shotLockErrorNotFound {
		t.Fatalf("data.errorCode = %q, want %q", env.Data.ErrorCode, shotLockErrorNotFound)
	}
	if env.Data.LockState != nil {
		t.Fatalf("not-found must NOT carry lockState; got %+v", env.Data.LockState)
	}
}

func TestRespondShotLockError_InvalidInput(t *testing.T) {
	c, w := newShotLockTestContext()
	respondShotLockError(c, canvasService.ErrShotLockInvalidInput, "Acquire shot lock failed")

	env := decodeShotLockEnvelope(t, w)
	if env.ErrorCode != shotLockErrorInvalidInput {
		t.Fatalf("errorCode = %q, want %q", env.ErrorCode, shotLockErrorInvalidInput)
	}
	if env.Data.LockState != nil {
		t.Fatalf("invalid-input must NOT carry lockState")
	}
}

func TestRespondShotLockError_HeldByOther_CarriesLockState(t *testing.T) {
	conflict := &canvasService.ShotLockConflict{
		Cause: canvasService.ErrShotLockHeldByOther,
		State: canvasService.ShotLockState{
			UserID:     42,
			JobID:      "regen-card-1",
			AcquiredAt: 1714073400000, // 2024-04-25T17:30:00Z in ms
		},
	}
	c, w := newShotLockTestContext()
	respondShotLockError(c, conflict, "Acquire shot lock failed")

	env := decodeShotLockEnvelope(t, w)
	if env.ErrorCode != shotLockErrorHeldByOther {
		t.Fatalf("errorCode = %q, want %q", env.ErrorCode, shotLockErrorHeldByOther)
	}
	if env.Data.LockState == nil {
		t.Fatalf("held-by-other must carry lockState")
	}
	if env.Data.LockState.UserID != 42 {
		t.Fatalf("lockState.userId = %d, want 42", env.Data.LockState.UserID)
	}
	if env.Data.LockState.JobID != "regen-card-1" {
		t.Fatalf("lockState.jobId = %q, want regen-card-1", env.Data.LockState.JobID)
	}
	if env.Data.LockState.AcquiredAt != 1714073400000 {
		t.Fatalf("lockState.acquiredAt = %d, want 1714073400000", env.Data.LockState.AcquiredAt)
	}
}

func TestRespondShotLockError_JobMismatch_CarriesLockState(t *testing.T) {
	conflict := &canvasService.ShotLockConflict{
		Cause: canvasService.ErrShotLockJobMismatch,
		State: canvasService.ShotLockState{
			UserID:     7,
			JobID:      "edit-session-original",
			AcquiredAt: 1714073400000,
		},
	}
	c, w := newShotLockTestContext()
	respondShotLockError(c, conflict, "Heartbeat shot lock failed")

	env := decodeShotLockEnvelope(t, w)
	if env.ErrorCode != shotLockErrorJobMismatch {
		t.Fatalf("errorCode = %q, want %q", env.ErrorCode, shotLockErrorJobMismatch)
	}
	if env.Data.LockState == nil || env.Data.LockState.JobID != "edit-session-original" {
		t.Fatalf("job-mismatch must surface holder's jobId; got %+v", env.Data.LockState)
	}
}

func TestRespondShotLockError_ConflictWithoutWrappedState_StillEmitsCode(t *testing.T) {
	// A bare ErrShotLockHeldByOther (not wrapped in ShotLockConflict)
	// must still surface the right errorCode — the lockState is
	// best-effort, not load-bearing for the error code itself. Pin
	// so a refactor that switched the conflict wrapping mechanism
	// would still correctly classify the error type.
	c, w := newShotLockTestContext()
	respondShotLockError(c, canvasService.ErrShotLockHeldByOther, "Acquire shot lock failed")

	env := decodeShotLockEnvelope(t, w)
	if env.ErrorCode != shotLockErrorHeldByOther {
		t.Fatalf("errorCode = %q, want %q", env.ErrorCode, shotLockErrorHeldByOther)
	}
	if env.Data.LockState != nil {
		t.Fatalf("bare error without ShotLockConflict must NOT fabricate a lockState")
	}
}
