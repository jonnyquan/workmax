//go:build desktop

package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// PUT    /agent/threads/:uuid/project — assign this conversation to a project.
// DELETE /agent/threads/:uuid/project — remove it from its project.
//
// A project assignment is a view preference, not thread data — same position
// pin takes (0006). It lives in its own uid-scoped table so cloud sync never
// overwrites it, and each local account groups its own view.
//
// Projects are implicit: typing a name creates it, and the project list is
// derived from distinct keys in use. No separate "create project" action.

const maxProjectKeyRunes = 64

type threadProjectPut struct {
	ProjectKey string `json:"project_key"`
}

func (s *Server) handleSetThreadProject(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "project_unavailable"})
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
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	// Strict decode: exactly one field, no trailing data, no unknown keys.
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	var in threadProjectPut
	if err := dec.Decode(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_project_body"})
		return
	}
	if dec.More() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "trailing_json"})
		return
	}
	key := strings.TrimSpace(in.ProjectKey)
	if !isValidProjectKey(key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_project_key"})
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.cfg.DB.Exec(
		`INSERT INTO w_desktop_thread_project (uid, thread_uuid, project_key, assigned_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT (uid, thread_uuid) DO UPDATE SET project_key = excluded.project_key, assigned_at = excluded.assigned_at`,
		uid, threadUUID, key, now,
	).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project_set_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"project_key": key})
}

func (s *Server) handleClearThreadProject(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "project_unavailable"})
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
	var threadID uint64
	if err := s.cfg.DB.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&threadID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	if err := s.cfg.DB.Exec(
		`DELETE FROM w_desktop_thread_project WHERE uid = ? AND thread_uuid = ?`,
		uid, threadUUID,
	).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "project_clear_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"cleared": true})
}

func isValidProjectKey(key string) bool {
	if key == "" || utf8.RuneCountInString(key) > maxProjectKeyRunes {
		return false
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// validateThreadProjectPut decodes and validates the PUT body. Exported for
// test reuse.
func DecodeThreadProjectPut(raw []byte) (threadProjectPut, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var in threadProjectPut
	if err := dec.Decode(&in); err != nil {
		return threadProjectPut{}, err
	}
	if dec.More() {
		return threadProjectPut{}, errors.New("trailing json content")
	}
	if !isValidProjectKey(strings.TrimSpace(in.ProjectKey)) {
		return threadProjectPut{}, fmt.Errorf("project_key must be 1–%d characters, no control characters", maxProjectKeyRunes)
	}
	return in, nil
}
