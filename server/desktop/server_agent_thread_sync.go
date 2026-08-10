//go:build desktop

package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// PUT /agent/threads/:uuid/cloud-sync — "does this conversation leave my
// machine?", answered by the person whose conversation it is.
//
// The three states were already defined and already honored everywhere that
// reads them; what was missing was the write. Nothing in production code ever
// set 'paused', so the protection built into the sync writer (an upsert that
// preserves a local pause instead of stamping 'synced' back over it) guarded a
// state no user could reach.
//
//	local   — never left this machine. There is no cloud copy to pause, so
//	          this is not a setting; it is a fact about the thread's history.
//	paused  — the cloud knows about it, and the user has stopped sending
//	          further changes up. Existing cloud content is untouched: this is
//	          a sync switch, not a delete.
//	synced  — the normal state.
//
// A local thread is therefore rejected rather than "paused": pretending to
// pause something that was never syncing would tell the user their data had
// been protected by an action that did nothing.

const maxThreadCloudSyncBodyBytes = 512

const (
	cloudSyncStateLocal  = "local"
	cloudSyncStatePaused = "paused"
	cloudSyncStateSynced = "synced"
)

type threadCloudSyncRequest struct {
	State string `json:"state"`
}

type threadCloudSyncResponse struct {
	ThreadUUID     string `json:"thread_uuid"`
	CloudSyncState string `json:"cloud_sync_state"`
}

var errThreadCloudSyncBody = errors.New("invalid_cloud_sync_state")

func decodeThreadCloudSyncRequest(body io.Reader) (threadCloudSyncRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxThreadCloudSyncBodyBytes+1))
	if err != nil || len(raw) > maxThreadCloudSyncBodyBytes {
		return threadCloudSyncRequest{}, errThreadCloudSyncBody
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var in threadCloudSyncRequest
	if err := decoder.Decode(&in); err != nil || decoder.More() {
		return threadCloudSyncRequest{}, errThreadCloudSyncBody
	}
	if in.State != cloudSyncStatePaused && in.State != cloudSyncStateSynced {
		return threadCloudSyncRequest{}, errThreadCloudSyncBody
	}
	return in, nil
}

func (s *Server) handleSetThreadCloudSync(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "cloud_sync_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	in, err := decodeThreadCloudSyncRequest(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_cloud_sync_state"})
		return
	}
	// Ownership as everywhere else: you decide for your own threads, and a
	// foreign uuid is not found rather than forbidden.
	identity, ok := s.requestOwner(c)
	if !ok {
		return
	}
	var current string
	if err := s.cfg.DB.Raw(
		`SELECT COALESCE(cloud_sync_state, 'synced') FROM w_workagent_thread WHERE uuid = ? AND uid = ?`,
		threadUUID, identity.UID,
	).Row().Scan(&current); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "thread_not_found"})
		return
	}
	if current == cloudSyncStateLocal {
		c.JSON(http.StatusConflict, gin.H{
			"error":            "thread_not_synced",
			"cloud_sync_state": cloudSyncStateLocal,
		})
		return
	}
	if current != in.State {
		if err := s.cfg.DB.Exec(
			`UPDATE w_workagent_thread SET cloud_sync_state = ? WHERE uuid = ? AND uid = ?`,
			in.State, threadUUID, identity.UID,
		).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cloud_sync_update_failed"})
			return
		}
	}
	// updated_at is deliberately NOT touched: pausing sync is a preference
	// about a conversation, not activity in it, and bumping the timestamp
	// would jump the thread to the top of the sidebar for no reason a user
	// would recognize.
	c.JSON(http.StatusOK, threadCloudSyncResponse{
		ThreadUUID:     threadUUID,
		CloudSyncState: in.State,
	})
}
