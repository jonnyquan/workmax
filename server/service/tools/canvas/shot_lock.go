// Per-Shot collaborative lock service.
//
// Three operations: Acquire, Heartbeat, Release. Each one runs as a
// single conditional UPDATE keyed on (id, canvas_project_id). The HTTP
// layer performs project edit authorization before calling into this service;
// lock_user_id stores the real actor uid so collaborators can coordinate on
// the same project-owned shot rows. The atomic claim semantics:
//
//   Acquire(uid, projectID, shotID, jobID)
//     UPDATE w_canvas_shot SET lock_user_id=uid, lock_job_id=jobID,
//                       lock_acquired_at=NOW()
//     WHERE  id=shotID AND canvas_project_id=projectID
//        AND deleted_at IS NULL
//        AND ( lock_user_id = 0
//              OR (lock_user_id = uid AND lock_job_id = jobID)
//              OR (lock_acquired_at < NOW() - TTL) )
//     If RowsAffected == 0 → look up the current row to distinguish:
//       not found            → ErrShotLockNotFound
//       held by other (live) → ErrShotLockHeldByOther + state
//       held by self diff job → ErrShotLockJobMismatch + state
//
//   Heartbeat(...)  — UPDATE that bumps lock_acquired_at when
//                     (lock_user_id == uid AND lock_job_id == jobID).
//                     Returns the same three error shapes on miss.
//
//   Release(...)    — UPDATE that clears the lock fields when
//                     (lock_user_id == uid AND lock_job_id == jobID).
//                     Idempotent: if the row is already unlocked,
//                     returns nil (no error). Releasing someone else's
//                     lock is ErrShotLockHeldByOther, not silent.
//
// jobId is intentionally caller-supplied: front-end uses one jobId per
// "edit session" so a Heartbeat from a stale tab can't keep alive a
// lock the same user has since taken on a fresh tab. Two same-user
// Acquires with different jobIds are a contention case — second loses.
//
// TTL is server-side and not configurable per-call. Veo / Sora
// generations take 5–10 min; we lean on Heartbeat (every ~30 s) to
// keep the lock alive, and use 90 s as the stale window — long enough
// to survive a temporarily wedged tab, short enough that a real
// crashed client clears the lock within a minute.

package canvas

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"server/model"
)

// ShotLockTTL is the stale-lock window. A lock is stealable by the
// next Acquire once `now - lock_acquired_at > ShotLockTTL`. Tuned
// against the real workload: AI video generations heartbeat every
// 30 s, so a 90 s TTL gives 3× headroom.
const ShotLockTTL = 90 * time.Second

// ShotLockState lives in document_v2.go — same shape used by the
// canvas document's §8.3 ShotLink.lock field. Reuse keeps the lock
// service and the document schema in lockstep so a client GET on a
// ShotLink reads exactly the shape the service writes.
//   UserID     int   — uid of holder; 0 only when read from a stale
//                      pre-acquire row (Acquire/Heartbeat success
//                      always echoes the caller's uid)
//   JobID      string — caller-supplied op id
//   AcquiredAt int64  — ms since unix epoch, mirrors the JS Date.now()
//                       the front-end will compare against

// Sentinel errors. Callers (HTTP layer) translate these to status
// codes — 404 for NotFound, 409 for the two contention errors,
// 400 for InvalidInput.
var (
	ErrShotLockNotFound     = errors.New("canvas: shot not found")
	ErrShotLockHeldByOther  = errors.New("canvas: shot lock held by another user")
	ErrShotLockJobMismatch  = errors.New("canvas: shot lock job id mismatch")
	ErrShotLockInvalidInput = errors.New("canvas: shot lock invalid input")
)

// ShotLockConflict wraps one of ErrShotLockHeldByOther /
// ErrShotLockJobMismatch with the live state so the HTTP layer can
// hand the client a structured payload (heldByUid, since, jobId).
type ShotLockConflict struct {
	Cause error
	State ShotLockState
}

func (e *ShotLockConflict) Error() string {
	return e.Cause.Error()
}

func (e *ShotLockConflict) Unwrap() error {
	return e.Cause
}

// AcquireShotLock atomically claims the lock or, if it's already held
// by the same uid+jobId (idempotent re-acquire) or the existing lock
// is stale (older than ShotLockTTL), takes it over and refreshes
// AcquiredAt. Returns the post-acquire state so callers can echo
// AcquiredAt into the client's lock token.
func AcquireShotLock(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	canvasProjectID uint,
	shotID uint,
	jobID string,
) (ShotLockState, error) {
	if uid <= 0 || canvasProjectID == 0 || shotID == 0 {
		return ShotLockState{}, ErrShotLockInvalidInput
	}
	normalizedJobID, err := NormalizeCanvasStorageKey(jobID, "jobId", true)
	if err != nil {
		return ShotLockState{}, ErrShotLockInvalidInput
	}
	jobID = normalizedJobID
	if db == nil {
		return ShotLockState{}, errors.New("canvas: AcquireShotLock requires a non-nil db")
	}

	now := time.Now()
	staleCutoff := now.Add(-ShotLockTTL)

	// Atomic claim. The OR chain is ordered cheapest-first:
	//   - currently unlocked (sentinel 0)
	//   - same uid + same jobId  → renew
	//   - stale lock (regardless of holder) → steal
	res := db.WithContext(ctx).Model(&model.Shot{}).
		Where("id = ? AND canvas_project_id = ? AND deleted_at IS NULL",
			shotID, canvasProjectID).
		Where("(lock_user_id = 0 OR (lock_user_id = ? AND lock_job_id = ?) OR lock_acquired_at < ?)",
			uid, jobID, staleCutoff).
		Updates(map[string]any{
			"lock_user_id":     uid,
			"lock_job_id":      jobID,
			"lock_acquired_at": now,
		})
	if err := res.Error; err != nil {
		return ShotLockState{}, err
	}
	if res.RowsAffected == 1 {
		return ShotLockState{UserID: uid, JobID: jobID, AcquiredAt: now.UnixMilli()}, nil
	}

	// Zero rows: either no such shot for this uid+project, or the
	// claim predicate failed (held by other, live). Read once to
	// distinguish — caller wants to know which.
	return readLockState(ctx, db, uid, canvasProjectID, shotID, jobID, ErrShotLockHeldByOther)
}

// HeartbeatShotLock bumps lock_acquired_at to now, but only if the
// caller still holds the lock (lock_user_id == uid AND lock_job_id ==
// jobID). A heartbeat against an unlocked / stolen / mismatched-job
// shot returns the same conflict shape as Acquire so the front-end
// can branch uniformly.
func HeartbeatShotLock(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	canvasProjectID uint,
	shotID uint,
	jobID string,
) (ShotLockState, error) {
	if uid <= 0 || canvasProjectID == 0 || shotID == 0 {
		return ShotLockState{}, ErrShotLockInvalidInput
	}
	normalizedJobID, err := NormalizeCanvasStorageKey(jobID, "jobId", true)
	if err != nil {
		return ShotLockState{}, ErrShotLockInvalidInput
	}
	jobID = normalizedJobID
	if db == nil {
		return ShotLockState{}, errors.New("canvas: HeartbeatShotLock requires a non-nil db")
	}

	now := time.Now()
	res := db.WithContext(ctx).Model(&model.Shot{}).
		Where("id = ? AND canvas_project_id = ? AND deleted_at IS NULL",
			shotID, canvasProjectID).
		Where("lock_user_id = ? AND lock_job_id = ?", uid, jobID).
		Update("lock_acquired_at", now)
	if err := res.Error; err != nil {
		return ShotLockState{}, err
	}
	if res.RowsAffected == 1 {
		return ShotLockState{UserID: uid, JobID: jobID, AcquiredAt: now.UnixMilli()}, nil
	}
	return readLockState(ctx, db, uid, canvasProjectID, shotID, jobID, ErrShotLockHeldByOther)
}

// ReleaseShotLock clears the lock fields when the caller still holds
// them. Idempotent on already-unlocked rows: returns nil (no error)
// so a duplicate release doesn't 409 the UI. Releasing someone
// else's live lock is still a conflict — silent stealing would let
// a buggy client wipe a teammate's in-flight work.
func ReleaseShotLock(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	canvasProjectID uint,
	shotID uint,
	jobID string,
) error {
	if uid <= 0 || canvasProjectID == 0 || shotID == 0 {
		return ErrShotLockInvalidInput
	}
	normalizedJobID, err := NormalizeCanvasStorageKey(jobID, "jobId", true)
	if err != nil {
		return ErrShotLockInvalidInput
	}
	jobID = normalizedJobID
	if db == nil {
		return errors.New("canvas: ReleaseShotLock requires a non-nil db")
	}

	res := db.WithContext(ctx).Model(&model.Shot{}).
		Where("id = ? AND canvas_project_id = ? AND deleted_at IS NULL",
			shotID, canvasProjectID).
		Where("lock_user_id = ? AND lock_job_id = ?", uid, jobID).
		Updates(map[string]any{
			"lock_user_id":     0,
			"lock_job_id":      "",
			"lock_acquired_at": nil,
		})
	if err := res.Error; err != nil {
		return err
	}
	if res.RowsAffected == 1 {
		return nil
	}

	// Did the row exist at all? If unlocked already, idempotent ok.
	// Otherwise classify the conflict.
	var row model.Shot
	if err := db.WithContext(ctx).
		Where("id = ? AND canvas_project_id = ? AND deleted_at IS NULL",
			shotID, canvasProjectID).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrShotLockNotFound
		}
		return err
	}
	if row.LockUserID == 0 {
		return nil // already unlocked — idempotent
	}
	state := stateFromRow(row)
	if row.LockUserID == uid {
		return &ShotLockConflict{Cause: ErrShotLockJobMismatch, State: state}
	}
	return &ShotLockConflict{Cause: ErrShotLockHeldByOther, State: state}
}

// readLockState is the post-conditional-update lookup that turns a
// "claim/heartbeat returned 0 rows" outcome into a precise error.
// Either the shot doesn't exist for this scope (NotFound) or the
// predicate failed because someone else holds it / the same user
// holds it under a different jobId. defaultCause is the error to
// return when the lock is held by someone else and live.
func readLockState(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	canvasProjectID uint,
	shotID uint,
	jobID string,
	defaultCause error,
) (ShotLockState, error) {
	var row model.Shot
	if err := db.WithContext(ctx).
		Where("id = ? AND canvas_project_id = ? AND deleted_at IS NULL",
			shotID, canvasProjectID).
		First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ShotLockState{}, ErrShotLockNotFound
		}
		return ShotLockState{}, err
	}
	state := stateFromRow(row)
	if row.LockUserID == 0 {
		// Race: between the failed UPDATE and this SELECT, the lock
		// got released. Surface as "held by other" so the caller
		// retries — second Acquire will succeed.
		return state, &ShotLockConflict{Cause: defaultCause, State: state}
	}
	if row.LockUserID == uid {
		return state, &ShotLockConflict{Cause: ErrShotLockJobMismatch, State: state}
	}
	return state, &ShotLockConflict{Cause: ErrShotLockHeldByOther, State: state}
}

func stateFromRow(row model.Shot) ShotLockState {
	state := ShotLockState{
		UserID: row.LockUserID,
		JobID:  row.LockJobID,
	}
	if row.LockAcquiredAt != nil {
		state.AcquiredAt = row.LockAcquiredAt.UnixMilli()
	}
	return state
}
