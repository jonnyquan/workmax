//go:build desktop

package desktop

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// POST   /agent/threads/:uuid/pin — keep this conversation at the top.
// DELETE /agent/threads/:uuid/pin — let it fall back into the timeline.
//
// A pin is a view preference, not thread data: it lives in its own
// uid-scoped table (migration 0006) so cloud sync can never overwrite it,
// and each local account pins its own view of the same machine.
//
// Both operations are idempotent by construction — pinning twice is pinned,
// unpinning what was never pinned is unpinned. Retries need no special
// handling and the renderer needs no read-before-write.

func (s *Server) handlePinThread(c *gin.Context) {
	s.handleSetThreadPin(c, true)
}

func (s *Server) handleUnpinThread(c *gin.Context) {
	s.handleSetThreadPin(c, false)
}

func (s *Server) handleSetThreadPin(c *gin.Context, pinned bool) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "pin_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	// Ownership as everywhere else: you pin your own threads, and a foreign
	// uuid is not found rather than forbidden.
	uid, _, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	if pinned {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		err = s.cfg.DB.Exec(
			`INSERT INTO w_desktop_thread_pin (uid, thread_uuid, pinned_at) VALUES (?, ?, ?)
			 ON CONFLICT (uid, thread_uuid) DO NOTHING`,
			uid, threadUUID, now,
		).Error
	} else {
		err = s.cfg.DB.Exec(
			`DELETE FROM w_desktop_thread_pin WHERE uid = ? AND thread_uuid = ?`,
			uid, threadUUID,
		).Error
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "pin_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"pinned": pinned})
}
