//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

type recoverableAgentTurnsResponse struct {
	Items []recoverableAgentTurnIntent `json:"items"`
	Count int                          `json:"count"`
}

type cancelAgentTurnResponse struct {
	TurnUUID string `json:"turn_uuid"`
	Canceled bool   `json:"canceled"`
}

type legacyAgentTurnStreamInput struct {
	Intent agentTurnIntent
	Thread agentTurnThread
	Lease  cloudproxy.SessionLease
	Files  []int64 // 本地附件 file_ids（L3b），仅 local 路径用
}

// localSingleUserUID is the reserved uid for unauthenticated local-only users
// (L3d, D2): a high-bit value far above any real cloud uid, so SQLite uid=?
// filtering keeps local-only threads/messages naturally isolated from real
// accounts. Compare noLocalHistoryUID = 1<<63-1.
const localSingleUserUID = uint64(1) << 62

func (s *Server) currentAgentTurnSession() (uint64, cloudproxy.SessionLease, error) {
	if s.cfg.TokenStore == nil {
		if s.shouldUseLocalRoute() {
			return s.localRouteUID(), cloudproxy.SessionLease{}, nil
		}
		return 0, cloudproxy.SessionLease{}, cloudproxy.ErrNoSession
	}
	snapshot, err := s.cfg.TokenStore.GetSnapshot()
	if err != nil {
		if s.shouldUseLocalRoute() {
			return s.localRouteUID(), cloudproxy.SessionLease{}, nil
		}
		return 0, cloudproxy.SessionLease{}, err
	}
	if snapshot.Pair.AccessToken == "" || snapshot.Pair.IsRefreshExpired(time.Now().UTC()) {
		if s.shouldUseLocalRoute() {
			return s.localRouteUID(), cloudproxy.SessionLease{}, nil
		}
		return 0, cloudproxy.SessionLease{}, cloudproxy.ErrNoSession
	}
	uid, err := cloudproxy.ExtractUIDFromAccessToken(snapshot.Pair.AccessToken)
	if err != nil || uid == 0 {
		return 0, cloudproxy.SessionLease{}, errors.New("agent turn session has no subject")
	}
	if err := snapshot.Lease.Check(); err != nil {
		return 0, cloudproxy.SessionLease{}, err
	}
	return uint64(uid), snapshot.Lease, nil
}

func (s *Server) agentTurnLock(turnUUID string) *sync.Mutex {
	value, _ := s.agentTurnLocks.LoadOrStore(turnUUID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (s *Server) handleListRecoverableAgentTurns(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_turn_recovery_unavailable"})
		return
	}
	limit, err := localHistoryLimitQuery(c.Request, 50, 500)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_limit"})
		return
	}
	uid, lease, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
	items, err := listRecoverableAgentTurnIntents(s.cfg.DB, lease, uid, limit)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
		return
	}
	c.JSON(http.StatusOK, recoverableAgentTurnsResponse{Items: items, Count: len(items)})
}

func (s *Server) handleReplayAgentTurn(c *gin.Context) {
	if s.cfg.Proxy == nil || s.cfg.DB == nil || s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_turn_recovery_unavailable"})
		return
	}
	turnUUID := c.Param("uuid")
	if _, err := cloudproxy.DesktopTurnRequestID(turnUUID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_turn_uuid"})
		return
	}
	uid, lease, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
	intent, thread, err := loadOwnedAgentTurnIntentAndThread(s.cfg.DB, lease, uid, turnUUID)
	if err != nil {
		s.writeAgentTurnIntentLoadError(c, err)
		return
	}
	if intent.State != agentTurnIntentInterrupted {
		c.JSON(http.StatusConflict, gin.H{"error": "turn_not_recoverable"})
		return
	}
	if _, err := validateFrozenAgentTurnIntent(intent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_intent_invalid"})
		return
	}
	lock := s.agentTurnLock(turnUUID)
	if !lock.TryLock() {
		c.JSON(http.StatusConflict, gin.H{"error": "turn_in_progress"})
		return
	}
	defer lock.Unlock()
	s.streamLegacyAgentTurn(c, legacyAgentTurnStreamInput{Intent: intent, Thread: thread, Lease: lease})
}

func (s *Server) handleCancelAgentTurn(c *gin.Context) {
	if s.cfg.DB == nil || s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_turn_recovery_unavailable"})
		return
	}
	turnUUID := c.Param("uuid")
	if _, err := cloudproxy.DesktopTurnRequestID(turnUUID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_turn_uuid"})
		return
	}
	uid, lease, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
	canceled, err := cancelAgentTurnIntent(s.cfg.DB, lease, uid, turnUUID)
	if err != nil {
		if errors.Is(err, errAgentTurnIntentNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "turn_not_found"})
			return
		}
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
		return
	}
	c.JSON(http.StatusOK, cancelAgentTurnResponse{TurnUUID: turnUUID, Canceled: canceled})
}

// streamLegacyAgentTurn replays the immutable legacy request with the same
// idempotency key. It is intentionally still one request-bound SSE relay, not
// Durable Attach: observer disconnect cancels this HTTP attempt.
func (s *Server) streamLegacyAgentTurn(c *gin.Context, input legacyAgentTurnStreamInput) {
	useLocal := s.shouldUseLocalRoute()

	// 云端专属预处理：解析 cloud_thread_id + 构造 workmax envelope。本地路由
	// 不需要 cloud_thread_id（本地线程可能从未同步云端），也不发 workmax 私有
	// 协议；本地请求体由 local_inference 引擎按 OpenAI/Anthropic 协议自构。
	var payload json.RawMessage
	if !useLocal {
		cloudThreadID, err := strconv.ParseUint(strings.TrimSpace(input.Thread.CloudThreadID), 10, 64)
		if err != nil || cloudThreadID == 0 {
			if err := s.interruptLegacyAgentTurnBeforeSSE(input, "thread_not_ready"); err != nil {
				s.writeAgentTurnPreStreamStateError(c, err)
				return
			}
			c.JSON(http.StatusConflict, gin.H{
				"error":       "thread_not_ready",
				"thread_uuid": input.Intent.ThreadUUID,
			})
			return
		}
		payload, err = payloadWithDesktopChatContract(
			json.RawMessage(`{"stream":true}`),
			strconv.FormatUint(cloudThreadID, 10),
			input.Intent.ChatMode,
			input.Intent.UserText,
		)
		if err != nil {
			if stateErr := s.interruptLegacyAgentTurnBeforeSSE(input, "intent_invalid"); stateErr != nil {
				s.writeAgentTurnPreStreamStateError(c, stateErr)
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_intent_invalid"})
			return
		}
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		if err := s.interruptLegacyAgentTurnBeforeSSE(input, "intent_invalid"); err != nil {
			s.writeAgentTurnPreStreamStateError(c, err)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_stream_unavailable"})
		return
	}
	if err := enterAgentTurnIntentState(
		s.cfg.DB, input.Lease, input.Intent.UID, input.Intent.TurnUUID,
		agentTurnIntentStarting,
	); err != nil {
		_ = s.interruptLegacyAgentTurnBeforeSSE(input, "intent_invalid")
		s.writeAgentTurnPreStreamStateError(c, err)
		return
	}
	if err := enterAgentTurnIntentState(
		s.cfg.DB, input.Lease, input.Intent.UID, input.Intent.TurnUUID,
		agentTurnIntentStreaming,
	); err != nil {
		_ = s.interruptLegacyAgentTurnBeforeSSE(input, "intent_invalid")
		s.writeAgentTurnPreStreamStateError(c, err)
		return
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	destination := &legacyAgentTurnOutcomeWriter{
		delegate: cloudproxy.NewGinSSEWriter(c.Writer, flusher),
	}
	keepaliveContext, cancelKeepalive := context.WithCancel(c.Request.Context())
	defer cancelKeepalive()
	go runSSEKeepalive(keepaliveContext, destination)

	// 路由分发：local → 本地推理引擎；否则 → 云端 Proxy。两者都把同一个
	// destination SSEWriter 传下去，turn intent / CacheWriter / renderer / 恢复
	// 全部协议无关地复用。cloud 路径额外带 workmax envelope Body 与 lease。
	chatReq := cloudproxy.ChatRequest{
		ThreadID:   input.Thread.ID,
		ThreadUUID: input.Thread.UUID,
		TurnUUID:   input.Intent.TurnUUID,
		UID:        input.Intent.UID,
		UserText:   input.Intent.UserText,
		ChatMode:   input.Intent.ChatMode,
		FileIDs:    input.Files,
	}
	var chatErr error
	if useLocal {
		chatErr = s.localTurnRunner().Chat(c.Request.Context(), chatReq, destination)
	} else {
		chatReq.ExpectedLease = input.Lease
		chatReq.Body = payload
		chatErr = s.cfg.Proxy.Chat(c.Request.Context(), chatReq, destination)
	}

	state := agentTurnIntentInterrupted
	errorKind := legacyAgentTurnErrorKind(chatErr)
	if destination.ThreadBusy() {
		errorKind = "turn_in_progress"
	} else if chatErr == nil {
		state = agentTurnIntentCompleted
		errorKind = ""
	}
	// A concurrent cancel is authoritative. Terminal bookkeeping is pinned to
	// the frozen uid/turn rather than the now-current login so an epoch change
	// after a forwarded done cannot leave this row stuck in streaming.
	if err := finalizeAgentTurnIntentOutcome(
		s.cfg.DB, input.Intent.UID, input.Intent.TurnUUID, state, errorKind,
	); err != nil {
		log.Printf("desktop agent turn outcome %s: %v", input.Intent.TurnUUID, err)
	}
}

func (s *Server) interruptLegacyAgentTurnBeforeSSE(input legacyAgentTurnStreamInput, errorKind string) error {
	return updateAgentTurnIntentState(
		s.cfg.DB, input.Lease, input.Intent.UID, input.Intent.TurnUUID,
		agentTurnIntentInterrupted, errorKind,
	)
}

func (s *Server) writeAgentTurnPreStreamStateError(c *gin.Context, err error) {
	if errors.Is(err, errAgentTurnIntentCanceled) {
		c.JSON(http.StatusConflict, gin.H{"error": "turn_canceled"})
		return
	}
	if errors.Is(err, cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
}

func (s *Server) writeAgentTurnSessionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cloudproxy.ErrNoSession):
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
	case errors.Is(err, cloudproxy.ErrSessionChanged):
		writeSessionChanged(c)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session_unavailable"})
	}
}

func (s *Server) writeAgentTurnIntentLoadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errAgentTurnIntentNotFound), errors.Is(err, errAgentTurnIntentThreadNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "turn_not_found"})
	case errors.Is(err, cloudproxy.ErrSessionChanged):
		writeSessionChanged(c)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
	}
}

func legacyAgentTurnErrorKind(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, cloudproxy.ErrChatSessionChanged), errors.Is(err, cloudproxy.ErrSessionChanged):
		return "session_changed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "request_canceled"
	default:
		return "stream_interrupted"
	}
}

type legacyAgentTurnOutcomeWriter struct {
	delegate cloudproxy.SSEWriter
	mu       sync.Mutex
	busy     bool
}

func (writer *legacyAgentTurnOutcomeWriter) WriteEvent(event cloudproxy.SSEEvent) error {
	if legacyAgentTurnDoneIsBusy(event) {
		writer.mu.Lock()
		writer.busy = true
		writer.mu.Unlock()
	}
	return writer.delegate.WriteEvent(event)
}

func (writer *legacyAgentTurnOutcomeWriter) WriteProxyError(proxyError cloudproxy.ProxyError) error {
	return writer.delegate.WriteProxyError(proxyError)
}

func (writer *legacyAgentTurnOutcomeWriter) WriteKeepalive() error {
	return writer.delegate.WriteKeepalive()
}

func (writer *legacyAgentTurnOutcomeWriter) ThreadBusy() bool {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.busy
}

func legacyAgentTurnDoneIsBusy(event cloudproxy.SSEEvent) bool {
	var envelope struct {
		Type    string          `json:"type"`
		Code    string          `json:"code"`
		Subtype string          `json:"subtype"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(event.Data), &envelope); err != nil {
		return false
	}
	if event.Type != "done" && envelope.Type != "done" {
		return false
	}
	if envelope.Code == "THREAD_BUSY" || envelope.Subtype == "thread_busy" {
		return true
	}
	if len(envelope.Result) == 0 {
		return false
	}
	var result struct {
		Code    string `json:"code"`
		Subtype string `json:"subtype"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return false
	}
	return result.Code == "THREAD_BUSY" || result.Subtype == "thread_busy"
}

const (
	// maxAgentChatPayloadBytes bounds the /agent/chat payload object (which may
	// now carry a small files[] array, L3b) — still tiny vs the 1 MiB body cap.
	maxAgentChatPayloadBytes = 1024
	// maxAgentChatAttachments caps how many file ids one turn may reference.
	maxAgentChatAttachments = 10
)

func validateLegacyAgentTurnPayload(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > maxAgentChatPayloadBytes || !utf8.Valid(raw) {
		return errors.New("agent turn payload is invalid")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p struct {
		Stream *bool   `json:"stream"`
		Files  []int64 `json:"files,omitempty"`
	}
	if err := dec.Decode(&p); err != nil {
		return errors.New("agent turn payload is invalid")
	}
	if p.Stream == nil || !*p.Stream {
		return errors.New("agent turn payload stream must be true")
	}
	if len(p.Files) > maxAgentChatAttachments {
		return errors.New("agent turn payload has too many files")
	}
	for _, id := range p.Files {
		if id <= 0 {
			return errors.New("agent turn payload file_id must be positive")
		}
	}
	if dec.More() {
		return errors.New("agent turn payload has trailing data")
	}
	return nil
}

// parseAgentChatPayloadFiles extracts the files array from a payload that
// validateLegacyAgentTurnPayload has already accepted. Errors are ignored
// (validation already ran); a malformed payload simply yields no attachments.
func parseAgentChatPayloadFiles(raw json.RawMessage) []int64 {
	var p struct {
		Files []int64 `json:"files,omitempty"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Files
}
