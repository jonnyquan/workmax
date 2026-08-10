//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Renaming a conversation.
//
// The default name is minted before the conversation exists ("Untitled
// presentation"), so it is wrong more often than right — and the sidebar's
// grouping and search both key off the title, which makes a stale name a
// findability bug, not cosmetics. PUT cannot do this: its contract is
// idempotent create, and a repeat PUT with a different name deliberately
// returns the existing row untouched.
//
// Scope mirrors deletion, for the same reason: local-only threads. A synced
// thread's name belongs to the cloud copy, and the sync worker would overwrite
// a local rename on its next pull — a rename that silently undoes itself.

const maxAgentThreadRenameBodyBytes = 4 << 10

// renameAgentThreadResponse echoes the updated row so the renderer can paint
// from the server's version of the truth rather than its own optimistic copy.
type renameAgentThreadResponse struct {
	Renamed bool           `json:"renamed"`
	Thread  LocalThreadRow `json:"thread"`
}

func (s *Server) handleRenameAgentThread(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_rename_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	uid := identity.UID
	name, err := decodeRenameAgentThreadRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
		return
	}

	var (
		threadID  uint64
		syncState string
	)
	row := s.cfg.DB.Raw(
		`SELECT id, COALESCE(cloud_sync_state,'synced') FROM w_workagent_thread
		  WHERE uuid = ? AND uid = ? AND agent_type = 'general_agent'`,
		threadUUID, uid,
	).Row()
	if err := row.Scan(&threadID, &syncState); err != nil {
		if strings.Contains(err.Error(), "no rows") {
			c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_rename_failed"})
		return
	}
	if syncState != "local" {
		c.JSON(http.StatusConflict, gin.H{"error": "thread_synced"})
		return
	}

	// updated_at moves with the rename on purpose: the list orders by it, and
	// a conversation the user just retitled is one they are working with.
	now := time.Now().UTC()
	if err := s.cfg.DB.Exec(
		`UPDATE w_workagent_thread SET name = ?, updated_at = ? WHERE uid = ? AND id = ?`,
		name, now.Format(time.RFC3339Nano), uid, threadID,
	).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_rename_failed"})
		return
	}

	var messageCount int
	var agentMode string
	if err := s.cfg.DB.Raw(
		`SELECT agent_mode, message_count FROM w_workagent_thread WHERE id = ?`, threadID,
	).Row().Scan(&agentMode, &messageCount); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "thread_rename_failed"})
		return
	}
	c.JSON(http.StatusOK, renameAgentThreadResponse{
		Renamed: true,
		Thread: LocalThreadRow{
			UUID:         threadUUID,
			Name:         name,
			AgentMode:    agentMode,
			MessageCount: messageCount,
			UpdatedAt:    now,
			CloudSync:    "local",
		},
	})
}

// decodeRenameAgentThreadRequest accepts exactly {"name": "..."} and applies
// the same normalization the create path uses — one definition of a valid
// thread name, not two that drift.
func decodeRenameAgentThreadRequest(request *http.Request) (string, error) {
	if request == nil || request.Body == nil {
		return "", errors.New("rename body is missing")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxAgentThreadRenameBodyBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxAgentThreadRenameBodyBytes || !utf8.Valid(body) {
		return "", errors.New("rename body is malformed")
	}
	var payload struct {
		Name string `json:"name"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return "", errors.New("rename body is malformed")
	}
	if decoder.More() {
		return "", errors.New("rename body has trailing content")
	}
	return normalizeDesktopThreadName(payload.Name)
}
