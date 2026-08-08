//go:build desktop

package desktop

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	cloudproxy "server/desktop/cloud_proxy"
	desktopsync "server/desktop/sync"
)

type putAgentThreadRequest struct {
	Name      string
	AgentMode string
}

type putAgentThreadResponse struct {
	State   string         `json:"state"`
	Created bool           `json:"created"`
	Thread  LocalThreadRow `json:"thread"`
}

type pendingAgentThreadResponse struct {
	State      string `json:"state"`
	ThreadUUID string `json:"thread_uuid"`
}

// handlePutAgentThread creates-or-recovers one cloud thread and only exposes
// it after the canonical cloud resource has been atomically upserted into the
// active session's local SQLite cache. The same caller-provided v4 UUID flows
// through every layer, making both automatic 401 retry and user retry safe.
func (s *Server) handlePutAgentThread(c *gin.Context) {
	if s.cfg.Proxy == nil || s.cfg.TokenStore == nil || s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_create_unavailable"})
		return
	}
	threadUUID, err := canonicalDesktopThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	request, err := decodePutAgentThreadRequest(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	request.Name, err = normalizeDesktopThreadName(request.Name)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_name"})
		return
	}
	if !IsAgentModeAllowed(request.AgentMode) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":         "agent_mode_not_enabled",
			"allowed_modes": DesktopAllowedAgentModes,
		})
		return
	}
	request.AgentMode, _ = NormalizeDesktopAgentMode(request.AgentMode)

	// Local route: build the thread locally without requiring a cloud session
	// (L3d, D2). An unauthenticated local-only user gets localSingleUserUID; an
	// authenticated local-route user keeps their real uid. Cloud path below is
	// untouched.
	if s.shouldUseLocalRoute() {
		uid := localSingleUserUID
		if pair, _, acqErr := cloudproxy.AcquireAccessTokenWithLease(
			c.Request.Context(), s.cfg.TokenStore, s.cfg.Proxy.CloudClient(),
		); acqErr == nil {
			if u, uidErr := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken); uidErr == nil && u != 0 {
				uid = uint64(u)
			}
		}
		row, created, err := s.createLocalAgentThread(uid, threadUUID, request)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "local_thread_create_failed"})
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		c.JSON(status, putAgentThreadResponse{State: "ready", Created: created, Thread: row})
		return
	}

	cloud := s.cfg.Proxy.CloudClient()
	pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(
		c.Request.Context(),
		s.cfg.TokenStore,
		cloud,
	)
	if err != nil {
		s.writePutAgentThreadAuthError(c, err)
		return
	}
	expectedUID, err := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken)
	if err != nil || expectedUID == 0 {
		// A credential that cannot be bound to one positive local UID must not
		// remain eligible for later local-cache writes. Fence only the lease
		// observed by this request: a concurrent replacement login is a distinct
		// authority and must remain usable.
		if !s.cfg.TokenStore.FenceSessionLease(lease) {
			writeSessionChanged(c)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session_unavailable"})
		return
	}
	leaseContext, releaseLease := lease.BindContext(c.Request.Context())
	defer releaseLease()

	input := cloudproxy.PutThreadInput{
		UUID:      threadUUID,
		Name:      request.Name,
		AgentMode: request.AgentMode,
	}
	result, err := cloud.PutThread(leaseContext, pair.AccessToken, input)
	if errors.Is(err, cloudproxy.ErrPutThreadAuthExpired) {
		freshPair, refreshErr := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
			leaseContext,
			s.cfg.TokenStore,
			cloud,
			pair,
			lease,
		)
		if refreshErr != nil {
			s.writePutAgentThreadAuthError(c, refreshErr)
			return
		}
		freshUID, uidErr := cloudproxy.ExtractUIDFromAccessToken(freshPair.AccessToken)
		if uidErr != nil || freshUID == 0 || freshUID != expectedUID {
			// Refresh is allowed to rotate credentials, never subjects. Fence the
			// epoch that now contains an unbindable/wrong-subject credential so no
			// subsequent request can accidentally authorize it. A newer login may
			// have replaced it while this response was being checked, so fencing is
			// conditional on the request's original lease.
			if !s.cfg.TokenStore.FenceSessionLease(lease) {
				writeSessionChanged(c)
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
			return
		}
		result, err = cloud.PutThread(leaseContext, freshPair.AccessToken, input)
	}
	if err != nil {
		s.writePutAgentThreadCloudError(c, lease, err)
		return
	}
	if err := lease.Check(); err != nil {
		writeSessionChanged(c)
		return
	}
	if result.Thread.UUID != threadUUID ||
		result.Thread.AgentType != "general_agent" ||
		!IsAgentModeAllowed(result.Thread.AgentMode) {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":                "invalid_cloud_thread",
			"retry_with_same_uuid": true,
		})
		return
	}

	localThread, err := desktopsync.CommitCreatedThread(
		leaseContext,
		s.cfg.DB,
		lease,
		int(expectedUID),
		result.Thread,
	)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		if errors.Is(err, desktopsync.ErrUIDConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "local_identity_conflict"})
			return
		}
		// Cloud is already authoritative. Keep the stable UUID visible so the
		// typed client can retry the same PUT; also ask pull-sync to reconcile.
		if s.cfg.ThreadsSyncer != nil {
			s.cfg.ThreadsSyncer.Trigger("agent-thread-put-reconcile")
		}
		c.JSON(http.StatusAccepted, pendingAgentThreadResponse{
			State:      "pending_local_sync",
			ThreadUUID: threadUUID,
		})
		return
	}

	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, putAgentThreadResponse{
		State:   "ready",
		Created: result.Created,
		Thread: LocalThreadRow{
			UUID:         localThread.UUID,
			Name:         localThread.Name,
			AgentMode:    localThread.AgentMode,
			MessageCount: localThread.MessageCount,
			UpdatedAt:    localThread.UpdatedAt,
			CloudSync:    localThread.CloudSyncState,
		},
	})
}

// createLocalAgentThread 在本地 SQLite 创建（或幂等复用）一个不依赖云端的线程，
// 用于 preferred_route=local 的 PUT /agent/threads/:uuid。cloud_thread_id 留空
// （local route 的对话/缓存/列表/同步都不需要它——见 L1 streamLegacyAgentTurn
// 跳过 cloud_thread_id 解析、ListLocalThreads 不过滤、sync 单向拉取且过滤空值）。
// cloud_sync_state='local' 标识纯本地线程（区别于真同步过的 'synced'）。
// 返回 LocalThreadRow + created（true=本次新建，false=同 uuid+uid 已存在幂等复用）。
func (s *Server) createLocalAgentThread(uid uint64, threadUUID string, req putAgentThreadRequest) (LocalThreadRow, bool, error) {
	agentMode := req.AgentMode
	if agentMode == "" {
		agentMode = DefaultDesktopAgentMode
	}
	db := s.cfg.DB

	// 幂等：同 uid 下同 uuid 已存在则复用（重复 PUT 同 uuid 安全，不重复 INSERT）。
	var (
		existingID      int64
		existingName    string
		existingMode    string
		existingMsgCnt  int
		existingUpdated string
		existingSync    string
	)
	scanErr := db.Raw(
		`SELECT id, COALESCE(name,''), agent_mode, message_count, updated_at,
		        COALESCE(cloud_sync_state,'synced')
		   FROM w_workagent_thread
		  WHERE uuid = ? AND uid = ?`,
		threadUUID, uid,
	).Row().Scan(&existingID, &existingName, &existingMode, &existingMsgCnt, &existingUpdated, &existingSync)
	if scanErr == nil && existingID > 0 {
		return LocalThreadRow{
			UUID:         threadUUID,
			Name:         existingName,
			AgentMode:    existingMode,
			MessageCount: existingMsgCnt,
			UpdatedAt:    parseSQLiteTime(existingUpdated),
			CloudSync:    existingSync,
		}, false, nil
	}
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return LocalThreadRow{}, false, fmt.Errorf("local thread lookup: %w", scanErr)
	}

	// 新建。cloud_thread_id=NULL；cloud_sync_state='local'。
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread
		     (uid, uuid, name, agent_mode, agent_type, cloud_thread_id,
		      cloud_sync_state, message_count, created_at, updated_at)
		   VALUES (?, ?, ?, ?, 'general_agent', NULL, 'local', 0, ?, ?)`,
		uid, threadUUID, req.Name, agentMode, nowStr, nowStr,
	).Error; err != nil {
		return LocalThreadRow{}, false, fmt.Errorf("local thread insert: %w", err)
	}
	return LocalThreadRow{
		UUID:         threadUUID,
		Name:         req.Name,
		AgentMode:    agentMode,
		MessageCount: 0,
		UpdatedAt:    now,
		CloudSync:    "local",
	}, true, nil
}

func (s *Server) writePutAgentThreadAuthError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cloudproxy.ErrSessionChanged):
		writeSessionChanged(c)
	case errors.Is(err, cloudproxy.ErrNoSession), errors.Is(err, cloudproxy.ErrPutThreadAuthExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session_unavailable"})
	}
}

func (s *Server) writePutAgentThreadCloudError(c *gin.Context, lease cloudproxy.SessionLease, err error) {
	if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	switch {
	case errors.Is(err, cloudproxy.ErrPutThreadAuthExpired):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
	case errors.Is(err, cloudproxy.ErrPutThreadConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "thread_uuid_conflict"})
	default:
		c.JSON(http.StatusBadGateway, gin.H{
			"error":                "agent_create_unavailable",
			"retry_with_same_uuid": true,
		})
	}
}

func canonicalDesktopThreadUUID(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("thread uuid required")
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 || parsed.String() != value {
		return "", errors.New("thread uuid must be canonical v4")
	}
	return value, nil
}

func normalizeDesktopThreadName(value string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" || !utf8.ValidString(name) || len(name) > 200 {
		return "", errors.New("invalid thread name")
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("invalid thread name")
		}
	}
	return name, nil
}

func decodePutAgentThreadRequest(request *http.Request) (putAgentThreadRequest, error) {
	var result putAgentThreadRequest
	if request == nil || request.Body == nil {
		return result, errors.New("thread request body is missing")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxAgentThreadPutBodyBytes+1))
	defer clear(body)
	if err != nil || len(body) == 0 || len(body) > maxAgentThreadPutBodyBytes || !utf8.Valid(body) {
		return result, errors.New("thread request body is malformed")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return result, errors.New("thread request must be one JSON object")
	}
	seen := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return result, errors.New("thread request is malformed")
		}
		key, ok := token.(string)
		if !ok || (key != "name" && key != "agent_mode") {
			return result, errors.New("thread request has an unknown field")
		}
		if _, duplicate := seen[key]; duplicate {
			return result, errors.New("thread request repeats a field")
		}
		seen[key] = struct{}{}
		switch key {
		case "name":
			if err := decoder.Decode(&result.Name); err != nil {
				return putAgentThreadRequest{}, errors.New("thread name is malformed")
			}
		case "agent_mode":
			if err := decoder.Decode(&result.AgentMode); err != nil {
				return putAgentThreadRequest{}, errors.New("agent mode is malformed")
			}
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 2 {
		return putAgentThreadRequest{}, errors.New("thread request fields are incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return putAgentThreadRequest{}, errors.New("thread request has trailing data")
	}
	return result, nil
}
