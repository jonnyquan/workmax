package canvas

import (
	"context"
	"errors"
	"testing"
	"time"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

// shot_lock_test.go drives Acquire / Heartbeat / Release through
// every contention branch end-to-end against an in-memory sqlite.
// shot_sync_test.go is pure-DiffShots; this file pairs with it as
// the DB-backed half. Together they pin the full §13 M3-W7-02 +
// M4-W12-01 surface.
//
// Convention: every test seeds w_canvas_shot directly with a one-row helper
// so the assertion focus stays on lock state, not creation paths.

const (
	testUID        = 7
	otherUID       = 8
	testProjectID  = uint(42)
	otherProjectID = uint(99)
	testCardID     = "card-1"
	testJobID      = "regen-card-1"
	differentJobID = "regen-card-1-v2"
	otherUserJobID = "edit-by-other"
)

func seedShot(t *testing.T, db *gorm.DB, uid int, projectID uint, lockUID int, lockJobID string, lockAt *time.Time) model.Shot {
	t.Helper()
	row := model.Shot{
		UID:             uid,
		CanvasProjectID: projectID,
		LocalCardID:     testCardID,
		Status:          model.ShotStatusDraft,
		LockUserID:      lockUID,
		LockJobID:       lockJobID,
		LockAcquiredAt:  lockAt,
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed shot: %v", err)
	}
	return row
}

func reloadShot(t *testing.T, db *gorm.DB, id uint) model.Shot {
	t.Helper()
	var row model.Shot
	if err := db.First(&row, id).Error; err != nil {
		t.Fatalf("reload shot: %v", err)
	}
	return row
}

// ---------- AcquireShotLock ----------

func TestAcquireShotLock_OnUnlocked_Succeeds(t *testing.T) {
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	state, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if err != nil {
		t.Fatalf("acquire on unlocked: %v", err)
	}
	if state.UserID != testUID || state.JobID != testJobID || state.AcquiredAt == 0 {
		t.Fatalf("unexpected state: %+v", state)
	}

	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != testUID || persisted.LockJobID != testJobID || persisted.LockAcquiredAt == nil {
		t.Fatalf("lock not persisted: %+v", persisted)
	}
}

func TestAcquireShotLock_SelfSameJob_RenewsIdempotently(t *testing.T) {
	db := testutil.NewTestDB(t)
	old := time.Now().Add(-30 * time.Second)
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &old)

	// Re-acquire as the same uid + jobId. Must succeed and bump
	// AcquiredAt forward — that's how heartbeat fallbacks work
	// when the front-end forgets it already holds the lock.
	state, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if err != nil {
		t.Fatalf("re-acquire same job: %v", err)
	}
	if state.UserID != testUID || state.JobID != testJobID {
		t.Fatalf("renewed wrong holder: %+v", state)
	}
	if state.AcquiredAt <= old.UnixMilli() {
		t.Fatalf("acquired_at not bumped (was %d, now %d)", old.UnixMilli(), state.AcquiredAt)
	}
}

func TestAcquireShotLock_SelfDifferentJob_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &now)

	_, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, differentJobID)
	if !errors.Is(err, ErrShotLockJobMismatch) {
		t.Fatalf("expected ErrShotLockJobMismatch, got %v", err)
	}
	var conflict *ShotLockConflict
	if !errors.As(err, &conflict) || conflict.State.JobID != testJobID {
		t.Fatalf("conflict state wrong: %+v", err)
	}
}

func TestAcquireShotLock_OtherUserLive_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	// IMPORTANT: the row's `uid` is testUID (the project owner).
	// LockUserID = otherUID is a teammate scenario in the future
	// workspace model. For now, the lock_user_id != uid path is
	// the canonical cross-collaborator contention.
	row := seedShot(t, db, testUID, testProjectID, otherUID, otherUserJobID, &now)

	_, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockHeldByOther) {
		t.Fatalf("expected ErrShotLockHeldByOther, got %v", err)
	}
	var conflict *ShotLockConflict
	if !errors.As(err, &conflict) || conflict.State.UserID != otherUID {
		t.Fatalf("conflict didn't surface holder uid: %+v", err)
	}
}

func TestAcquireShotLock_StaleLock_GetsStolen(t *testing.T) {
	db := testutil.NewTestDB(t)
	stale := time.Now().Add(-2 * ShotLockTTL) // well past TTL
	row := seedShot(t, db, testUID, testProjectID, otherUID, otherUserJobID, &stale)

	state, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if err != nil {
		t.Fatalf("steal stale: %v", err)
	}
	if state.UserID != testUID || state.JobID != testJobID {
		t.Fatalf("steal yielded wrong holder: %+v", state)
	}
	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != testUID || persisted.LockJobID != testJobID {
		t.Fatalf("stale steal didn't persist: %+v", persisted)
	}
}

func TestAcquireShotLock_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	// No row seeded.
	_, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, 999, testJobID)
	if !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("expected ErrShotLockNotFound, got %v", err)
	}
}

func TestAcquireShotLock_WrongProject_NotFound(t *testing.T) {
	// Multi-tenant scope guard: a shot belonging to a different
	// project must not be reachable via Acquire even if the shotID
	// is correct. This is the four-tuple WHERE pattern from the
	// PanelShot tightening, applied to canvas Shot.
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	_, err := AcquireShotLock(context.Background(), db, testUID, otherProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("expected NotFound for cross-project, got %v", err)
	}
}

func TestAcquireShotLock_BlankInputs(t *testing.T) {
	db := testutil.NewTestDB(t)
	cases := []struct {
		name      string
		uid       int
		projectID uint
		shotID    uint
		jobID     string
	}{
		{"zero uid", 0, testProjectID, 1, testJobID},
		{"zero project", testUID, 0, 1, testJobID},
		{"zero shot", testUID, testProjectID, 0, testJobID},
		{"empty jobID", testUID, testProjectID, 1, ""},
		{"whitespace jobID", testUID, testProjectID, 1, "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AcquireShotLock(context.Background(), db, tc.uid, tc.projectID, tc.shotID, tc.jobID)
			if !errors.Is(err, ErrShotLockInvalidInput) {
				t.Fatalf("expected ErrShotLockInvalidInput, got %v", err)
			}
		})
	}
}

// ---------- HeartbeatShotLock ----------

func TestHeartbeatShotLock_HoldingMatchingJob_BumpsAcquiredAt(t *testing.T) {
	db := testutil.NewTestDB(t)
	old := time.Now().Add(-20 * time.Second)
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &old)

	state, err := HeartbeatShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if state.AcquiredAt <= old.UnixMilli() {
		t.Fatalf("heartbeat didn't bump (old=%d new=%d)", old.UnixMilli(), state.AcquiredAt)
	}
}

func TestHeartbeatShotLock_DifferentJob_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &now)

	_, err := HeartbeatShotLock(context.Background(), db, testUID, testProjectID, row.Id, differentJobID)
	if !errors.Is(err, ErrShotLockJobMismatch) {
		t.Fatalf("expected ErrShotLockJobMismatch, got %v", err)
	}
}

func TestHeartbeatShotLock_HeldByOther_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, otherUID, otherUserJobID, &now)

	_, err := HeartbeatShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockHeldByOther) {
		t.Fatalf("expected ErrShotLockHeldByOther, got %v", err)
	}
}

func TestHeartbeatShotLock_OnUnlocked_Conflicts(t *testing.T) {
	// Heartbeating an unlocked shot is meaningful — the caller
	// thinks it holds the lock but actually doesn't (released by
	// timeout, by another tab, by an admin). Surface as conflict
	// (default cause), not silent success, so the front-end can
	// re-acquire or abort.
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	_, err := HeartbeatShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockHeldByOther) {
		t.Fatalf("expected conflict on unlocked heartbeat, got %v", err)
	}
}

func TestHeartbeatShotLock_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	_, err := HeartbeatShotLock(context.Background(), db, testUID, testProjectID, 999, testJobID)
	if !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("expected ErrShotLockNotFound, got %v", err)
	}
}

// ---------- ReleaseShotLock ----------

func TestReleaseShotLock_HoldingMatchingJob_Clears(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &now)

	if err := ReleaseShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID); err != nil {
		t.Fatalf("release: %v", err)
	}
	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != 0 || persisted.LockJobID != "" || persisted.LockAcquiredAt != nil {
		t.Fatalf("release didn't clear: %+v", persisted)
	}
}

func TestReleaseShotLock_AlreadyUnlocked_Idempotent(t *testing.T) {
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	// Two consecutive releases on an unlocked row both succeed.
	for i := 0; i < 2; i++ {
		if err := ReleaseShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID); err != nil {
			t.Fatalf("idempotent release %d: %v", i, err)
		}
	}
}

func TestReleaseShotLock_DifferentJob_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, testUID, testJobID, &now)

	err := ReleaseShotLock(context.Background(), db, testUID, testProjectID, row.Id, differentJobID)
	if !errors.Is(err, ErrShotLockJobMismatch) {
		t.Fatalf("expected ErrShotLockJobMismatch, got %v", err)
	}
	// Lock must NOT be cleared — releasing the wrong jobId is a noop.
	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != testUID || persisted.LockJobID != testJobID {
		t.Fatalf("mismatched-job release silently cleared lock: %+v", persisted)
	}
}

func TestReleaseShotLock_HeldByOther_Conflicts(t *testing.T) {
	db := testutil.NewTestDB(t)
	now := time.Now()
	row := seedShot(t, db, testUID, testProjectID, otherUID, otherUserJobID, &now)

	err := ReleaseShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockHeldByOther) {
		t.Fatalf("expected ErrShotLockHeldByOther, got %v", err)
	}
	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != otherUID {
		t.Fatalf("attempted theft by release succeeded: %+v", persisted)
	}
}

func TestReleaseShotLock_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	err := ReleaseShotLock(context.Background(), db, testUID, testProjectID, 999, testJobID)
	if !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("expected ErrShotLockNotFound, got %v", err)
	}
}

// ---------- Cross-tenant scope ----------

func TestAllOps_RespectProjectScope(t *testing.T) {
	// Lock service scopes rows by shot id + project id. HTTP handlers
	// authorize project edit access before calling into the service, while
	// lock_user_id records the real collaborator uid. A wrong project id
	// must not see the row even when shotID happens to match.
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	if _, err := AcquireShotLock(context.Background(), db, otherUID, otherProjectID, row.Id, testJobID); !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("Acquire crossed scope: %v", err)
	}
	if _, err := HeartbeatShotLock(context.Background(), db, otherUID, otherProjectID, row.Id, testJobID); !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("Heartbeat crossed scope: %v", err)
	}
	if err := ReleaseShotLock(context.Background(), db, otherUID, otherProjectID, row.Id, testJobID); !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("Release crossed scope: %v", err)
	}
}

func TestAcquireShotLock_CollaboratorCanLockProjectShot(t *testing.T) {
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)

	state, err := AcquireShotLock(context.Background(), db, otherUID, testProjectID, row.Id, otherUserJobID)
	if err != nil {
		t.Fatalf("collaborator acquire: %v", err)
	}
	if state.UserID != otherUID || state.JobID != otherUserJobID {
		t.Fatalf("collaborator lock state = %+v, want uid %d job %q", state, otherUID, otherUserJobID)
	}
	persisted := reloadShot(t, db, row.Id)
	if persisted.LockUserID != otherUID || persisted.LockJobID != otherUserJobID {
		t.Fatalf("collaborator lock not persisted: %+v", persisted)
	}
}

func TestAcquireShotLock_RespectsSoftDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	row := seedShot(t, db, testUID, testProjectID, 0, "", nil)
	deletedAt := time.Now()
	if err := db.Model(&model.Shot{}).Where("id = ?", row.Id).
		Update("deleted_at", deletedAt).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	_, err := AcquireShotLock(context.Background(), db, testUID, testProjectID, row.Id, testJobID)
	if !errors.Is(err, ErrShotLockNotFound) {
		t.Fatalf("expected NotFound on soft-deleted, got %v", err)
	}
}
