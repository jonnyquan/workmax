package tools

// Per-Shot collaborative lock HTTP handlers.
//
// Three thin handlers — all parsing + auth + scope + service call +
// error → errorCode mapping. The actual contention semantics
// (atomic UPDATE, stale TTL, jobId match) live in
// server/service/tools/canvas/shot_lock.go and are unit-tested
// against in-memory sqlite there. This file only owns the HTTP
// envelope shape, which the front-end's lock hook treats as
// contract.
//
// Conflict envelope: respondCanvasErrorWithCodeAndData passes a
// `lockState` payload alongside the error code so the UI can render
// "held by user X since Y" inline with the toast — no second
// roundtrip required.

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"server/globals"
	projectService "server/service/project"
	canvasService "server/service/tools/canvas"
	"server/utils"
)

// Error codes for the three shot-lock conflict shapes. Stable strings
// the front-end branches on; mirror the service-layer sentinels.
const (
	shotLockErrorNotFound      = "SHOT_NOT_FOUND"
	shotLockErrorHeldByOther   = "SHOT_LOCK_HELD_BY_OTHER"
	shotLockErrorJobMismatch   = "SHOT_LOCK_JOB_MISMATCH"
	shotLockErrorInvalidInput  = "SHOT_LOCK_INVALID_INPUT"
	maxCanvasShotLockBodyBytes = 8 << 10 // 8 KiB
)

type shotLockRequest struct {
	// Caller-supplied operation id — see canvas-tool-design.md §8.3.
	// One per "edit session" so two tabs of the same user contend
	// instead of silently sharing a lock.
	JobID string `json:"jobId"`
}

// AcquireShotLock claims (or renews / steals stale) the lock for a
// single Shot. Idempotent for same uid+jobId; conflict otherwise.
func (a *CanvasApi) AcquireShotLock(c *gin.Context) {
	uid, projectID, shotID, jobID, ok := parseShotLockArgs(c)
	if !ok {
		return
	}
	state, err := canvasService.AcquireShotLock(
		c.Request.Context(),
		globals.GraDBs["system"],
		uid, projectID, shotID, jobID,
	)
	if err != nil {
		respondShotLockError(c, err, "Acquire shot lock failed")
		return
	}
	c.JSON(200, gin.H{
		"code":    1,
		"message": "ok",
		"data": gin.H{
			"success":   true,
			"lockState": state,
		},
	})
}

// HeartbeatShotLock bumps the lock's acquired_at so a long-running
// operation (Veo / Sora generation) doesn't time out into stealable
// state. Same conflict shape as Acquire on miss.
func (a *CanvasApi) HeartbeatShotLock(c *gin.Context) {
	uid, projectID, shotID, jobID, ok := parseShotLockArgs(c)
	if !ok {
		return
	}
	state, err := canvasService.HeartbeatShotLock(
		c.Request.Context(),
		globals.GraDBs["system"],
		uid, projectID, shotID, jobID,
	)
	if err != nil {
		respondShotLockError(c, err, "Heartbeat shot lock failed")
		return
	}
	c.JSON(200, gin.H{
		"code":    1,
		"message": "ok",
		"data": gin.H{
			"success":   true,
			"lockState": state,
		},
	})
}

// ReleaseShotLock clears the lock. Idempotent on already-unlocked;
// HeldByOther / JobMismatch stay loud so a buggy client can't wipe
// teammate's in-flight state silently.
func (a *CanvasApi) ReleaseShotLock(c *gin.Context) {
	uid, projectID, shotID, jobID, ok := parseShotLockArgs(c)
	if !ok {
		return
	}
	err := canvasService.ReleaseShotLock(
		c.Request.Context(),
		globals.GraDBs["system"],
		uid, projectID, shotID, jobID,
	)
	if err != nil {
		respondShotLockError(c, err, "Release shot lock failed")
		return
	}
	c.JSON(200, gin.H{
		"code":    1,
		"message": "ok",
		"data":    gin.H{"success": true},
	})
}

// parseShotLockArgs validates uid + URL params + body, surfacing the
// same envelope as the rest of the canvas API on bad input. Returns
// ok=false (with a response already written) when validation fails.
func parseShotLockArgs(c *gin.Context) (uid int, projectID uint, shotID uint, jobID string, ok bool) {
	rawUID := utils.GetUserID(c)
	if rawUID == 0 {
		respondCanvasUnauthorized(c)
		return 0, 0, 0, "", false
	}
	uid = int(rawUID)

	projectID64, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || projectID64 == 0 {
		respondCanvasError(c, "Invalid project id")
		return 0, 0, 0, "", false
	}
	projectID = uint(projectID64)
	canEditProject, err := projectService.NewRepository(globals.GraDBs["system"]).CanEditProject(projectID, uint(uid))
	if err != nil {
		respondCanvasError(c, "Validate project failed: "+err.Error())
		return 0, 0, 0, "", false
	}
	if !canEditProject {
		respondCanvasErrorWithCode(c, "Shot not found", shotLockErrorNotFound)
		return 0, 0, 0, "", false
	}

	shotID64, err := strconv.ParseUint(c.Param("shotId"), 10, 64)
	if err != nil || shotID64 == 0 {
		respondCanvasErrorWithCode(c, "Invalid shot id", shotLockErrorInvalidInput)
		return 0, 0, 0, "", false
	}
	shotID = uint(shotID64)

	var req shotLockRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxCanvasShotLockBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		respondCanvasErrorWithCode(
			c,
			"Invalid request: "+err.Error(),
			shotLockErrorInvalidInput,
		)
		return 0, 0, 0, "", false
	}
	jobID = req.JobID
	return uid, projectID, shotID, jobID, true
}

// respondShotLockError maps the four shot-lock sentinel error shapes
// into the canvas response envelope. NotFound and InvalidInput carry
// only an error code; the two contention errors (HeldByOther,
// JobMismatch) also ship the live ShotLockState so the UI can render
// "held by user X since Y" without a second query.
func respondShotLockError(c *gin.Context, err error, fallbackPrefix string) {
	switch {
	case errors.Is(err, canvasService.ErrShotLockInvalidInput):
		respondCanvasErrorWithCode(c, "Invalid shot lock input", shotLockErrorInvalidInput)
	case errors.Is(err, canvasService.ErrShotLockNotFound):
		respondCanvasErrorWithCode(c, "Shot not found", shotLockErrorNotFound)
	case errors.Is(err, canvasService.ErrShotLockHeldByOther):
		var conflict *canvasService.ShotLockConflict
		if errors.As(err, &conflict) {
			respondCanvasErrorWithCodeAndData(
				c,
				"Shot is locked by another user",
				shotLockErrorHeldByOther,
				gin.H{"lockState": conflict.State},
			)
			return
		}
		respondCanvasErrorWithCode(c, "Shot is locked by another user", shotLockErrorHeldByOther)
	case errors.Is(err, canvasService.ErrShotLockJobMismatch):
		var conflict *canvasService.ShotLockConflict
		if errors.As(err, &conflict) {
			respondCanvasErrorWithCodeAndData(
				c,
				"Shot lock job id mismatch",
				shotLockErrorJobMismatch,
				gin.H{"lockState": conflict.State},
			)
			return
		}
		respondCanvasErrorWithCode(c, "Shot lock job id mismatch", shotLockErrorJobMismatch)
	default:
		respondCanvasError(c, fallbackPrefix+": "+err.Error())
	}
}
