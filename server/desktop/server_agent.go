//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	cloudproxy "server/desktop/cloud_proxy"
)

// server_agent.go houses handlers under /agent/* — the renderer's
// workagent surface. Split out of server.go to keep each file
// scoped to one concern; same package, no behavior change.
//
// Endpoints:
//   - POST /agent/chat                   — typed SSE chat relay
//   - GET  /agent/skills/catalog         — filtered skills
//   - GET  /agent/skills/modes           — the local allowlist, no cloud
//   - GET  /agent/threads                — local thread list
//   - GET  /agent/threads/:uuid/messages — local messages

// agentChatRequest is the body the renderer POSTs to /agent/chat.
// The sidecar treats its local identifiers, user text, and allowlisted
// agent mode as authoritative. Before forwarding, it rewrites the cloud
// payload with the matching cloud conversation id and canonical turn.
//
// The caller is the bundled Desktop renderer's Agent chat flow.
type agentChatRequest struct {
	TurnUUID   string          `json:"turn_uuid"`
	ThreadUUID string          `json:"thread_uuid"`
	UserText   string          `json:"user_text"`
	ChatMode   string          `json:"chat_mode"`
	Payload    json.RawMessage `json:"payload"`
}

const (
	maxAgentChatBodyBytes     = 1 << 20   // 1 MiB
	maxAgentTurnUserTextBytes = 256 << 10 // typed Bridge parity
)

var errAgentChatBodyTooLarge = errors.New("chat request body too large")

// handleAgentChat is the renderer-facing chat endpoint. It:
//  1. Strictly decodes the exact typed turn-intent envelope.
//  2. Acquires the same-process turn lock.
//  3. Freezes/reuses the owner-scoped local idempotency intent.
//  4. Hands off to the legacy request-bound SSE relay.
//
// Returns when the relay returns (graceful done, error, or context
// cancellation). The renderer learns the outcome by consuming the
// stream — there's no separate JSON response.
func (s *Server) handleAgentChat(c *gin.Context) {
	if s.cfg.Proxy == nil || s.cfg.DB == nil || s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "agent_turn_recovery_unavailable",
		})
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAgentChatBodyBytes)
	body, err := decodeAgentChatRequest(c.Request)
	if err != nil {
		if errors.Is(err, errAgentChatBodyTooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "chat request body too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	if _, err := cloudproxy.DesktopTurnRequestID(body.TurnUUID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_turn_uuid"})
		return
	}
	validatedThreadUUID, err := validateAgentTurnThreadUUID(body.ThreadUUID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_thread_uuid"})
		return
	}
	body.ThreadUUID = validatedThreadUUID
	if strings.TrimSpace(body.UserText) == "" || len(body.UserText) > maxAgentTurnUserTextBytes || strings.ContainsRune(body.UserText, '\x00') {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_user_text"})
		return
	}
	if !IsAgentModeAllowed(body.ChatMode) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":         "chat_mode not enabled in desktop",
			"allowed_modes": DesktopAllowedAgentModes,
			"given_mode":    body.ChatMode,
		})
		return
	}
	normalizedMode, _ := NormalizeDesktopAgentMode(body.ChatMode)
	body.ChatMode = normalizedMode
	if err := validateLegacyAgentTurnPayload(body.Payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_agent_turn_payload"})
		return
	}

	uid, lease, err := s.currentAgentTurnSession()
	if err != nil {
		s.writeAgentTurnSessionError(c, err)
		return
	}
	digest, err := digestAgentTurnIntent(body.ThreadUUID, body.UserText, body.ChatMode)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_intent_invalid"})
		return
	}
	if _, err := checkAgentTurnIntentOwner(s.cfg.DB, lease, uid, body.TurnUUID); err != nil {
		s.writeAgentTurnOwnerCheckError(c, err)
		return
	}
	lock := s.agentTurnLock(body.TurnUUID)
	if !lock.TryLock() {
		sameOwner, err := checkAgentTurnIntentOwner(s.cfg.DB, lease, uid, body.TurnUUID)
		if err != nil {
			s.writeAgentTurnOwnerCheckError(c, err)
			return
		}
		if !sameOwner {
			// The lock holder has not committed its owner row yet. Use the
			// generic collision surface instead of exposing UUID liveness.
			c.JSON(http.StatusConflict, gin.H{"error": "turn_uuid_conflict"})
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "turn_in_progress"})
		return
	}
	defer lock.Unlock()

	intent, thread, _, err := ensureAgentTurnIntent(
		s.cfg.DB, lease, uid, body.TurnUUID, body.ThreadUUID,
		body.UserText, body.ChatMode, digest,
	)
	if err != nil {
		switch {
		case errors.Is(err, errAgentTurnIntentOwnerConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "turn_uuid_conflict"})
		case errors.Is(err, errAgentTurnIntentIdempotencyConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "idempotency_conflict"})
		case errors.Is(err, errAgentTurnIntentCanceled):
			c.JSON(http.StatusConflict, gin.H{"error": "turn_canceled"})
		case errors.Is(err, errAgentTurnIntentThreadNotFound):
			c.JSON(http.StatusNotFound, gin.H{
				"error":       "thread_not_found",
				"thread_uuid": body.ThreadUUID,
			})
		case errors.Is(err, cloudproxy.ErrSessionChanged):
			writeSessionChanged(c)
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
		}
		return
	}
	s.streamLegacyAgentTurn(c, legacyAgentTurnStreamInput{
		Intent: intent, Thread: thread, Lease: lease,
		Files: parseAgentChatPayloadFiles(body.Payload),
	})
}

func (s *Server) writeAgentTurnOwnerCheckError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errAgentTurnIntentOwnerConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "turn_uuid_conflict"})
	case errors.Is(err, cloudproxy.ErrSessionChanged):
		writeSessionChanged(c)
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_turn_recovery_unavailable"})
	}
}

// decodeAgentChatRequest deliberately accepts one exact JSON object. The
// renderer cannot smuggle legacy UID/thread_id authority through fields that
// encoding/json would otherwise ignore, and a duplicated key cannot change
// meaning depending on which decoder eventually consumes the body.
func decodeAgentChatRequest(request *http.Request) (agentChatRequest, error) {
	var result agentChatRequest
	if request == nil || request.Body == nil {
		return result, errors.New("chat request body is missing")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxAgentChatBodyBytes+1))
	defer clear(body)
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) || len(body) > maxAgentChatBodyBytes {
		return result, errAgentChatBodyTooLarge
	}
	if err != nil || len(body) == 0 || !utf8.Valid(body) {
		return result, errors.New("chat request body is malformed")
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return result, errors.New("chat request must be one JSON object")
	}
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return agentChatRequest{}, errors.New("chat request is malformed")
		}
		key, ok := token.(string)
		if !ok || (key != "turn_uuid" && key != "thread_uuid" && key != "user_text" && key != "chat_mode" && key != "payload") {
			return agentChatRequest{}, errors.New("chat request has an unknown field")
		}
		if _, duplicate := seen[key]; duplicate {
			return agentChatRequest{}, errors.New("chat request repeats a field")
		}
		seen[key] = struct{}{}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return agentChatRequest{}, errors.New("chat request field is malformed")
		}
		switch key {
		case "turn_uuid":
			if err := json.Unmarshal(raw, &result.TurnUUID); err != nil {
				return agentChatRequest{}, errors.New("turn_uuid is malformed")
			}
		case "thread_uuid":
			if err := json.Unmarshal(raw, &result.ThreadUUID); err != nil {
				return agentChatRequest{}, errors.New("thread_uuid is malformed")
			}
		case "user_text":
			if err := json.Unmarshal(raw, &result.UserText); err != nil {
				return agentChatRequest{}, errors.New("user_text is malformed")
			}
		case "chat_mode":
			if err := json.Unmarshal(raw, &result.ChatMode); err != nil {
				return agentChatRequest{}, errors.New("chat_mode is malformed")
			}
		case "payload":
			result.Payload = append(result.Payload[:0], raw...)
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 5 {
		return agentChatRequest{}, errors.New("chat request fields are incomplete")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return agentChatRequest{}, errors.New("chat request has trailing data")
	}
	return result, nil
}

// payloadWithDesktopChatContract keeps the Renderer envelope, local cache
// identity, and cloud WorkAgent contract consistent. Non-conflicting fields
// (for example files, language, model settings, and metadata extensions) are
// preserved, while the fields that select a conversation or turn are always
// overwritten from trusted sidecar state.
func payloadWithDesktopChatContract(
	raw json.RawMessage,
	cloudConversationID string,
	agentMode string,
	userText string,
) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("payload must be a JSON object: %w", err)
	}
	if obj == nil {
		obj = make(map[string]json.RawMessage)
	}

	metadata := make(map[string]json.RawMessage)
	if metadataRaw, ok := obj["metadata"]; ok && strings.TrimSpace(string(metadataRaw)) != "null" {
		if err := json.Unmarshal(metadataRaw, &metadata); err != nil {
			return nil, fmt.Errorf("payload.metadata must be a JSON object: %w", err)
		}
		if metadata == nil {
			metadata = make(map[string]json.RawMessage)
		}
	}

	conversationJSON, err := json.Marshal(cloudConversationID)
	if err != nil {
		return nil, fmt.Errorf("payload conversationId encode: %w", err)
	}
	chatModeJSON, err := json.Marshal("agent")
	if err != nil {
		return nil, fmt.Errorf("payload chatMode encode: %w", err)
	}
	agentModeJSON, err := json.Marshal(agentMode)
	if err != nil {
		return nil, fmt.Errorf("payload metadata.agentMode encode: %w", err)
	}
	messagesJSON, err := json.Marshal([]map[string]string{{
		"role":    "user",
		"content": userText,
	}})
	if err != nil {
		return nil, fmt.Errorf("payload messages encode: %w", err)
	}

	metadata["agentMode"] = agentModeJSON
	metadata["threadId"] = conversationJSON
	delete(metadata, "agent_mode")
	delete(metadata, "thread_id")
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("payload metadata encode: %w", err)
	}

	obj["conversationId"] = conversationJSON
	obj["chatMode"] = chatModeJSON
	obj["messages"] = messagesJSON
	obj["metadata"] = metadataJSON
	delete(obj, "conversation_id")
	delete(obj, "chat_mode")

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("payload encode: %w", err)
	}
	return out, nil
}

// sseKeepaliveInterval is the cadence of `: keepalive\n\n` comments
// emitted on /agent/chat while a turn is in flight. 30s comfortably
// beats any reasonable upstream-stall timeout (Cloudflare proxies
// disconnect idle SSE at 100s by default; we're well under that)
// while not flooding the channel.
const sseKeepaliveInterval = 30 * time.Second

// runSSEKeepalive ticks every sseKeepaliveInterval and emits a
// keepalive comment via dst. Stops on ctx cancellation OR when
// dst.WriteKeepalive returns an error (renderer disconnected; no
// point churning a dead writer).
//
// Concurrent-safe with proxy.Chat's event writes because GinSSEWriter
// serializes all writes through its mutex.
func runSSEKeepalive(ctx context.Context, dst cloudproxy.SSEWriter) {
	t := time.NewTicker(sseKeepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := dst.WriteKeepalive(); err != nil {
				return
			}
		}
	}
}

// skillsCatalogResponse is the wire shape of GET /agent/skills/catalog.
// Items keep the cloud's CloudSkillItem shape unchanged so the
// bundled Desktop renderer and sidecar share one stable contract.
type skillsCatalogResponse struct {
	Items []cloudproxy.CloudSkillItem `json:"items"`
	Count int                         `json:"count"`
	// AllowedModes is the desktop-only allowlist — surfaced so the
	// renderer can show an "only validated modes available" banner
	// without re-deriving the gate locally.
	AllowedModes []string `json:"allowed_modes"`
}

// handleSkillsCatalog returns the user-facing skill list filtered to
// the desktop allowlist. Always proxies the cloud (so we get the real
// version + capability flags) rather than serving a hardcoded list.
// On ordinary cloud failure we degrade gracefully: empty Items + the
// allowlist still surfaced so the renderer's UI doesn't break. Session-epoch
// replacement is not a cloud failure: it returns the closed session_changed
// conflict and must never masquerade as a valid empty catalog.
//
// Auth: uses the same token refresh path and closed classification as
// userinfo. No session or a second 401 after one refresh returns
// authentication_required; session replacement returns session_changed; local
// credential-state failures return session_unavailable. None of those auth
// states may degrade to a successful empty catalog.
func (s *Server) handleSkillsCatalog(c *gin.Context) {
	if s.cfg.Proxy == nil || s.cfg.TokenStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "cloud proxy or token store not configured",
		})
		return
	}
	cloud := s.cfg.Proxy.CloudClient()
	pair, lease, err := cloudproxy.AcquireAccessTokenWithLease(c.Request.Context(), s.cfg.TokenStore, cloud)
	if err != nil {
		if errors.Is(err, cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		if errors.Is(err, cloudproxy.ErrNoSession) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "session_unavailable"})
		return
	}
	leaseContext, releaseLease := lease.BindContext(c.Request.Context())
	defer releaseLease()

	// The cloud client is shared with the chat relay; ListSkills uses
	// the snug 10s timeout that's safe for a JSON lookup. We pull the
	// client off the Proxy because Proxy already owns its lifecycle.
	cloudItems, err := cloud.ListSkills(leaseContext, pair.AccessToken)
	if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	if errors.Is(err, cloudproxy.ErrListSkillsAuthExpired) {
		freshPair, refreshErr := cloudproxy.RefreshAccessTokenAfterUnauthorizedWithLease(
			leaseContext,
			s.cfg.TokenStore,
			cloud,
			pair,
			lease,
		)
		if refreshErr == nil {
			// Exactly one retry. A second 401 is never refreshed recursively;
			// it is classified below as authentication_required.
			cloudItems, err = cloud.ListSkills(leaseContext, freshPair.AccessToken)
			if errors.Is(err, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
				writeSessionChanged(c)
				return
			}
		} else if errors.Is(refreshErr, cloudproxy.ErrNoSession) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
			return
		} else if errors.Is(refreshErr, cloudproxy.ErrSessionChanged) || errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		} else {
			// A same-session token endpoint availability failure is still a
			// catalog availability failure, so retain the documented fail-soft
			// response below. Do not leave the original 401 sentinel in err or it
			// would be misreported as definitive authentication expiry.
			err = refreshErr
		}
	}
	if errors.Is(err, cloudproxy.ErrListSkillsAuthExpired) {
		if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "authentication_required"})
		return
	}
	if err != nil {
		if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
			writeSessionChanged(c)
			return
		}
		// Soft failure: surface the empty list + the allowlist so the
		// renderer can show a sensible UI ("can't reach catalog, here's
		// what's available offline").
		c.JSON(http.StatusOK, skillsCatalogResponse{
			Items:        []cloudproxy.CloudSkillItem{},
			Count:        0,
			AllowedModes: DesktopAllowedAgentModes,
		})
		return
	}
	if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}

	allowed := AllowedAgentModesSet()
	filtered := make([]cloudproxy.CloudSkillItem, 0, len(allowed))
	for _, item := range cloudItems {
		if _, ok := allowed[item.AgentMode]; ok && isDesktopSkillCatalogItemUsable(item) {
			filtered = append(filtered, item)
		}
	}
	if errors.Is(lease.Check(), cloudproxy.ErrSessionChanged) {
		writeSessionChanged(c)
		return
	}
	c.JSON(http.StatusOK, skillsCatalogResponse{
		Items:        filtered,
		Count:        len(filtered),
		AllowedModes: DesktopAllowedAgentModes,
	})
}

// agentModesResponse is the wire shape of GET /agent/skills/modes.
type agentModesResponse struct {
	AllowedModes []string `json:"allowed_modes"`
	// LocalRoute reports whether a turn sent right now would run on the local
	// model. The renderer needs this to answer "can I be used signed out?",
	// which is exactly the condition currentAgentTurnSession applies before it
	// hands out localSingleUserUID.
	LocalRoute bool `json:"local_route"`
	// ToolLoop reports whether that local turn would run the L2 tool loop.
	// The dispatch falls back to pure chat silently when no CLI is wired, and
	// a user who configured anthropic_compatible expecting tools deserves to
	// be told which one they are getting.
	ToolLoop bool `json:"tool_loop"`
}

// handleAgentModes answers "which agent modes may this Desktop use", without
// asking the cloud anything.
//
// The allowlist is desktop-local knowledge — a constant in this binary — but
// until now the only way to read it was GET /agent/skills/catalog, which needs
// a cloud session and returns 401 without one. That made the allowlist
// unreachable in precisely the configuration the local route exists to
// support: no account, local model, everything on this machine. The renderer
// therefore had no modes, so its composer stayed disabled and the local route
// could not be used at all.
//
// This route deliberately does NOT degrade the catalog's rule that an auth
// failure must never look like a valid empty catalog. It answers a different
// question and never contacts the cloud, so there is no auth state for it to
// misreport.
func (s *Server) handleAgentModes(c *gin.Context) {
	c.JSON(http.StatusOK, agentModesResponse{
		AllowedModes: DesktopAllowedAgentModes,
		LocalRoute:   s.shouldUseLocalRoute(),
		ToolLoop:     s.localToolLoopActive(),
	})
}

func isDesktopSkillCatalogItemUsable(item cloudproxy.CloudSkillItem) bool {
	if item.AgentMode == "" || item.Version == "" {
		return false
	}
	if item.LabelKey == "" || item.DescriptionKey == "" {
		return false
	}
	return true
}

// listThreadsResponse is the wire shape of GET /agent/threads.
type listThreadsResponse struct {
	Items []LocalThreadRow `json:"items"`
	Count int              `json:"count"`
}

// listMessagesResponse is the wire shape of GET /agent/threads/:uuid/messages.
type listMessagesResponse struct {
	Items []LocalMessageRow `json:"items"`
	Count int               `json:"count"`
}

// handleListThreads reads the most-recent N threads from local SQLite.
// Reads-only — never contacts the cloud, so this works in airplane
// mode. Limit validates via query param (?limit=N), default 50.
// include_paused=false hides locally-paused threads from the normal
// active list while keeping them recoverable from future settings UI.
//
// Used by the renderer's left-rail thread list. The cache_writer
// populates w_workagent_message rows during chat turns; the P1
// SyncWorker backfills thread rows from cloud and keeps this list
// current through local SQLite.
func (s *Server) handleListThreads(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "db not configured"})
		return
	}
	limit, err := localHistoryLimitQuery(c.Request, 50, 500)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	includePaused, err := localHistoryIncludePausedQuery(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	uid := s.activeLocalHistoryUID()
	rows, err := ListLocalThreads(s.cfg.DB, uid, limit, includePaused)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, listThreadsResponse{Items: rows, Count: len(rows)})
}

// handleListMessages reads all (up to limit) messages for a thread
// by uuid. Returns oldest-first so the renderer can render scrolling
// top-to-bottom without re-sorting. Missing thread → empty list +
// 200 (lets the renderer show a friendly empty state without
// branching on 404).
func (s *Server) handleListMessages(c *gin.Context) {
	if s.cfg.DB == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "db not configured"})
		return
	}
	uuid, err := validateLocalHistoryThreadUUID(c.Param("uuid"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := localHistoryLimitQuery(c.Request, 200, 1000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	uid := s.activeLocalHistoryUID()
	rows, err := ListLocalMessages(s.cfg.DB, uid, uuid, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// P1.B.3.x.2: fire-and-forget messages sync. Return local rows
	// immediately (so the user sees what's cached right away); the
	// background sync goroutine writes fresh rows + the renderer's
	// 5s poll picks them up on the next tick.
	//
	// Coalesce semantics live in MessagesSyncer — back-to-back
	// requests for the same thread are dropped while a sync is in
	// flight, so rapid renderer polls don't fan out to N concurrent
	// cloud calls.
	s.triggerMessagesSync(uuid, uid)

	c.JSON(http.StatusOK, listMessagesResponse{Items: rows, Count: len(rows)})
}

// triggerMessagesSync fires the per-thread messages syncer if the
// caller's thread has a known cloud_thread_id. No-op when:
//   - the syncer isn't wired (test configurations, or shutdown);
//   - there is no positive active local-history UID;
//   - the local thread row lacks cloud_thread_id (e.g. a thread
//     created by the cache writer before the first thread sync
//     landed — UpsertThreads (P1.B.3) populates the column);
//   - cloud_thread_id is not a positive integer (defensive).
//
// Extracted from handleListMessages so the handler body stays a
// readable sequence of "validate → read local → trigger sync →
// respond" rather than mixing in a six-line DB read + parse block.
func (s *Server) triggerMessagesSync(threadUUID string, uid uint64) {
	if s.cfg.MessagesSyncer == nil || uid == 0 || uid == noLocalHistoryUID {
		return
	}
	var cloudThreadIDStr string
	if err := s.cfg.DB.Raw(
		`SELECT COALESCE(cloud_thread_id, '') FROM w_workagent_thread WHERE uuid = ? AND uid = ? LIMIT 1`,
		threadUUID, uid,
	).Row().Scan(&cloudThreadIDStr); err != nil || cloudThreadIDStr == "" {
		return
	}
	cloudID, err := strconv.ParseUint(cloudThreadIDStr, 10, 64)
	if err != nil || cloudID == 0 {
		return
	}
	s.cfg.MessagesSyncer.Trigger(threadUUID, cloudID, uid)
}

// activeLocalHistoryUID returns the uid embedded in the current local
// access token. It intentionally does not refresh the token: local
// history reads should remain cheap and offline-friendly. A zero
// return means "no active uid filter" only for legacy tests /
// diagnostic boots without TokenStore. When TokenStore is configured
// but no parseable token exists or the refresh chain has expired,
// return a no-match sentinel so local endpoints do not expose stale
// rows from a prior account while the renderer is unauthenticated,
// expired, or the token entry is corrupt.
func (s *Server) activeLocalHistoryUID() uint64 {
	if s.cfg.TokenStore == nil {
		if s.shouldUseLocalRoute() {
			return localSingleUserUID
		}
		return 0
	}
	pair, err := s.cfg.TokenStore.Get()
	if err != nil || pair == nil || pair.AccessToken == "" {
		if s.shouldUseLocalRoute() {
			return localSingleUserUID
		}
		return noLocalHistoryUID
	}
	if pair.IsRefreshExpired(time.Now().UTC()) {
		if s.shouldUseLocalRoute() {
			return localSingleUserUID
		}
		return noLocalHistoryUID
	}
	uid, err := cloudproxy.ExtractUIDFromAccessToken(pair.AccessToken)
	if err != nil || uid == 0 {
		if s.shouldUseLocalRoute() {
			return localSingleUserUID
		}
		return noLocalHistoryUID
	}
	return uint64(uid)
}

const noLocalHistoryUID = uint64(1<<63 - 1)

func localHistoryLimitQuery(r *http.Request, defaultLimit, maxLimit int) (int, error) {
	values, ok := r.URL.Query()["limit"]
	if !ok {
		return defaultLimit, nil
	}
	if len(values) != 1 {
		return 0, fmt.Errorf("limit query must appear at most once")
	}
	value := values[0]
	if value == "" {
		return 0, fmt.Errorf("limit query must not be empty")
	}
	if strings.TrimSpace(value) != value || containsControl(value) {
		return 0, fmt.Errorf("limit query is malformed")
	}
	n, err := strconvAtoi(value)
	if err != nil || n <= 0 || n > maxLimit {
		return 0, fmt.Errorf("limit query must be between 1 and %d", maxLimit)
	}
	return n, nil
}

func validateLocalHistoryThreadUUID(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("thread uuid required")
	}
	if len(value) > 200 {
		return "", fmt.Errorf("thread uuid too long")
	}
	if strings.TrimSpace(value) != value || containsControl(value) {
		return "", fmt.Errorf("thread uuid is malformed")
	}
	return value, nil
}

func localHistoryIncludePausedQuery(r *http.Request) (bool, error) {
	values, ok := r.URL.Query()["include_paused"]
	if !ok {
		return true, nil
	}
	if len(values) != 1 {
		return false, fmt.Errorf("include_paused query must appear at most once")
	}
	value := values[0]
	if value == "" {
		return false, fmt.Errorf("include_paused query must not be empty")
	}
	if strings.TrimSpace(value) != value || containsControl(value) {
		return false, fmt.Errorf("include_paused query is malformed")
	}
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("include_paused query must be true or false")
	}
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// strconvAtoi keeps query limit parsing strict: decimal digits only,
// no signs, whitespace, or exponent forms.
func strconvAtoi(s string) (int, error) {
	var n int
	if len(s) == 0 {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			return 0, fmt.Errorf("overflow")
		}
	}
	return n, nil
}
