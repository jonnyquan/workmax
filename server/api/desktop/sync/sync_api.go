package sync

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"server/middleware"
	svc "server/service/desktop/sync"
)

// SyncApi bundles every /api/desktop/sync/* handler. Constructed once
// at app startup with the system DB; handlers read uid from the
// OAuth Bearer middleware claims and pass through to repo functions
// in server/service/desktop/sync.
//
// Mirrors the OauthApi pattern in sibling api/desktop/oauth — a
// configuration-holding struct (rather than bare functions) so tests
// can construct an isolated instance with a test DB without going
// through globals.
type SyncApi struct {
	DB *gorm.DB
}

// NewSyncApi wires a SyncApi backed by the given DB. Production
// wires this at app startup using globals.GraDBs["system"];
// tests construct directly with a *gorm.DB pointing at a temp DB.
func NewSyncApi(db *gorm.DB) *SyncApi {
	return &SyncApi{DB: db}
}

// listThreadsResponse is the typed envelope for GET /api/desktop/sync/threads.
// Items is the concrete repo type so the JSON shape is pinned by Go
// types (not the [{}, {}] of any-typed Envelope.Items).
type listThreadsResponse struct {
	Items      []listThreadsItem `json:"items"`
	NextCursor string            `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
	ServerTime string            `json:"server_time"`
}

// listThreadsItem wraps a ThreadDeltaRow with the wire-shape `action`
// field (every row from this endpoint is an upsert; deletes will
// land via tombstone in P1.A.5). Inlining ThreadDeltaRow with
// `json:",inline"` doesn't work for Go structs the same way it does
// for embedded; we keep the explicit JSON tags to make the wire
// shape obvious to anyone reading the code.
type listThreadsItem struct {
	Action Action `json:"action"`
	svc.ThreadDeltaRow
}

// ListThreads handles GET /api/desktop/sync/threads.
//
// Query params:
//   - since (string, optional): base64url cursor from a prior response.
//     Empty = full sync from beginning.
//   - limit (int, optional): max items per page; default 100, max 500.
//
// Auth: uid is extracted from the desktop OAuth Bearer JWT (the route
// middleware guarantees the token is present, valid, and issued to
// workmax-desktop; uid==0 here would be a middleware bug — we still 401
// defensively).
//
// Errors:
//   - 401 unauthorized when token uid missing
//   - 400 invalid_cursor when `since` is malformed
//   - 500 internal_error on DB failure (rare)
func (a *SyncApi) ListThreads(c *gin.Context) {
	uid := desktopOAuthUID(c)
	if uid == 0 {
		// OAuthBearerAuth middleware should have rejected first; defense-in-depth.
		c.JSON(401, gin.H{"error": "unauthorized"})
		return
	}

	cursor, err := svc.DecodeCursor(c.Query("since"))
	if err != nil {
		// Distinguish each kind so the renderer can decide whether
		// to retry (no — cursor is client-state-bug) or surface to
		// the user (mostly no — the renderer should treat it as a
		// "restart from beginning" hint).
		switch {
		case errors.Is(err, svc.ErrCursorBadEncoding):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "bad_encoding"})
		case errors.Is(err, svc.ErrCursorBadJSON):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "bad_json"})
		case errors.Is(err, svc.ErrCursorMissingField):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "missing_field"})
		case errors.Is(err, svc.ErrCursorFutureTime):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "future_timestamp"})
		default:
			c.JSON(400, gin.H{"error": "invalid_cursor"})
		}
		return
	}

	limit := DefaultLimit
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = ClampLimit(n)
		}
		// Silent fallback to default on garbage limit — same policy as
		// /agent/threads (P0.9b) so renderer behavior is consistent.
	}

	rows, nextUpsertCursor, hasMoreUpserts, err := svc.ListThreadsDelta(c.Request.Context(), a.DB, int(uid), cursor, limit)
	if err != nil {
		c.JSON(500, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}

	// P1.A.5b: merge tombstones into the items list as action="delete".
	// Each stream paginates independently with its own sub-cursor; the
	// composite next_cursor carries both forward to the next request.
	tombstoneCursor := svc.Cursor{}
	if cursor.Tombstone != nil {
		tombstoneCursor = *cursor.Tombstone
	}
	tombstones, nextTombstoneCursor, hasMoreTombstones, err := svc.ListTombstonesDelta(
		c.Request.Context(), a.DB, int(uid),
		"thread", tombstoneCursor, limit,
	)
	if err != nil {
		c.JSON(500, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}

	items := make([]listThreadsItem, 0, len(rows)+len(tombstones))
	for _, r := range rows {
		items = append(items, listThreadsItem{
			Action:         ActionUpsert,
			ThreadDeltaRow: r,
		})
	}
	for _, t := range tombstones {
		// Delete items carry only the identifying fields. uuid is
		// what the sidecar's UpsertThreads delete path reads.
		items = append(items, listThreadsItem{
			Action: ActionDelete,
			ThreadDeltaRow: svc.ThreadDeltaRow{
				CloudThreadID: fmt.Sprintf("%d", t.EntityID),
				UUID:          t.EntityUUID,
				UpdatedAt:     t.DeletedAt,
			},
		})
	}

	// Composite next cursor: upsert stream is the outer (UpdatedAt+ID),
	// tombstone stream is the nested Tombstone field.
	compositeNext := nextUpsertCursor
	if !nextTombstoneCursor.IsZero() {
		nt := nextTombstoneCursor // copy so address-of survives loop
		compositeNext.Tombstone = &nt
	} else if cursor.Tombstone != nil {
		// Preserve caller's tombstone cursor on a poll-with-no-deletes
		// so the next poll doesn't restart from the tombstone beginning.
		compositeNext.Tombstone = cursor.Tombstone
	}

	env := NewEnvelope(nil, "", false)
	c.JSON(200, listThreadsResponse{
		Items:      items,
		NextCursor: EncodeNextCursor(compositeNext),
		HasMore:    hasMoreUpserts || hasMoreTombstones,
		ServerTime: env.ServerTime,
	})
}

// getThreadResponse is the wire shape of GET /api/desktop/sync/threads/:id.
// Wraps the ThreadFullRow with no envelope (single-thread fetch
// doesn't need pagination metadata).
type getThreadResponse struct {
	Thread svc.ThreadFullRow `json:"thread"`
	// server_time so the renderer can detect stale responses
	// the same way it does with the delta endpoints.
	ServerTime string `json:"server_time"`
}

// GetThread handles GET /api/desktop/sync/threads/:id.
//
// :id is the cloud thread PK (numeric, matches CloudThreadID
// from the delta endpoint's items). Returned thread carries the
// heavy fields (prompt, latest_plan, plan_history) the delta
// endpoint excludes.
//
// Auth: uid from desktop OAuth Bearer JWT.
// IDOR: ErrThreadNotFound (not-owned OR doesn't exist) → 404 with
// no detail (don't leak existence).
//
// Errors:
//   - 401 unauthorized when token uid missing
//   - 400 invalid_request when :id missing/non-numeric
//   - 404 not_found when thread doesn't exist OR isn't owned
//   - 500 on DB failure
func (a *SyncApi) GetThread(c *gin.Context) {
	uid := desktopOAuthUID(c)
	if uid == 0 {
		c.JSON(401, gin.H{"error": "unauthorized"})
		return
	}

	idRaw := c.Param("id")
	if idRaw == "" {
		c.JSON(400, gin.H{"error": "invalid_request", "reason": "id is required"})
		return
	}
	cloudThreadID, parseErr := strconv.ParseUint(idRaw, 10, 64)
	if parseErr != nil {
		c.JSON(400, gin.H{"error": "invalid_request", "reason": "id must be numeric"})
		return
	}

	row, err := svc.GetThreadByCloudID(c.Request.Context(), a.DB, int(uid), cloudThreadID)
	if err != nil {
		if errors.Is(err, svc.ErrThreadNotFound) {
			c.JSON(404, gin.H{"error": "not_found"})
			return
		}
		c.JSON(500, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}

	env := NewEnvelope(nil, "", false)
	c.JSON(200, getThreadResponse{
		Thread:     row,
		ServerTime: env.ServerTime,
	})
}

// listMessagesResponse is the typed envelope for
// GET /api/desktop/sync/messages. Same Action-per-item wrapper
// pattern as listThreadsResponse.
type listMessagesResponse struct {
	Items      []listMessagesItem `json:"items"`
	NextCursor string             `json:"next_cursor"`
	HasMore    bool               `json:"has_more"`
	ServerTime string             `json:"server_time"`
}

type listMessagesItem struct {
	Action Action `json:"action"`
	svc.MessageDeltaRow
}

// ListMessages handles GET /api/desktop/sync/messages.
//
// Query params:
//   - thread_id (string, required): the cloud_thread_id from
//     /api/desktop/sync/threads. Parsed as uint64.
//   - since (string, optional): base64url cursor from a prior response.
//   - limit (int, optional): max items per page; default 100, max 500.
//
// IDOR posture: ListMessagesDelta runs an inner
// (id=? AND uid=?) lookup and returns ErrThreadNotOwned when the
// thread isn't owned by the requesting user. Maps to 404 here so
// we don't leak existence — same posture as the production
// work-agent's LoadByIDForOwner.
//
// Errors:
//   - 401 unauthorized when token uid missing
//   - 400 invalid_cursor on cursor parse failure (with reason)
//   - 400 invalid_request when thread_id missing/non-numeric
//   - 404 not_found when thread doesn't exist OR isn't owned by uid
//   - 500 on DB failure
func (a *SyncApi) ListMessages(c *gin.Context) {
	uid := desktopOAuthUID(c)
	if uid == 0 {
		c.JSON(401, gin.H{"error": "unauthorized"})
		return
	}

	threadIDRaw := c.Query("thread_id")
	if threadIDRaw == "" {
		c.JSON(400, gin.H{"error": "invalid_request", "reason": "thread_id is required"})
		return
	}
	threadID, parseErr := strconv.ParseUint(threadIDRaw, 10, 64)
	if parseErr != nil {
		c.JSON(400, gin.H{"error": "invalid_request", "reason": "thread_id must be numeric"})
		return
	}

	cursor, err := svc.DecodeCursor(c.Query("since"))
	if err != nil {
		switch {
		case errors.Is(err, svc.ErrCursorBadEncoding):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "bad_encoding"})
		case errors.Is(err, svc.ErrCursorBadJSON):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "bad_json"})
		case errors.Is(err, svc.ErrCursorMissingField):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "missing_field"})
		case errors.Is(err, svc.ErrCursorFutureTime):
			c.JSON(400, gin.H{"error": "invalid_cursor", "reason": "future_timestamp"})
		default:
			c.JSON(400, gin.H{"error": "invalid_cursor"})
		}
		return
	}

	limit := DefaultLimit
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = ClampLimit(n)
		}
	}

	rows, nextUpsertCursor, hasMoreUpserts, err := svc.ListMessagesDelta(
		c.Request.Context(), a.DB, int(uid), threadID, cursor, limit,
	)
	if err != nil {
		if errors.Is(err, svc.ErrThreadNotOwned) {
			// Don't leak "does it exist but you don't own it" vs
			// "doesn't exist". Collapse to 404.
			c.JSON(404, gin.H{"error": "not_found"})
			return
		}
		c.JSON(500, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}

	// P1.A.5b: merge message tombstones. Note: tombstones aren't
	// scoped by thread_id (the message's owning thread might already
	// be gone by the time the tombstone is read — and the desktop's
	// UpsertMessages delete path identifies the row by uuid alone).
	// We rely on uid-scoping for the IDOR boundary, same as the
	// tombstone insert at delete time.
	tombstoneCursor := svc.Cursor{}
	if cursor.Tombstone != nil {
		tombstoneCursor = *cursor.Tombstone
	}
	tombstones, nextTombstoneCursor, hasMoreTombstones, err := svc.ListTombstonesDelta(
		c.Request.Context(), a.DB, int(uid),
		"message", tombstoneCursor, limit,
	)
	if err != nil {
		c.JSON(500, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}

	items := make([]listMessagesItem, 0, len(rows)+len(tombstones))
	for _, r := range rows {
		items = append(items, listMessagesItem{
			Action:          ActionUpsert,
			MessageDeltaRow: r,
		})
	}
	for _, t := range tombstones {
		items = append(items, listMessagesItem{
			Action: ActionDelete,
			MessageDeltaRow: svc.MessageDeltaRow{
				CloudMessageID: fmt.Sprintf("%d", t.EntityID),
				UUID:           t.EntityUUID,
				UpdatedAt:      t.DeletedAt,
			},
		})
	}

	compositeNext := nextUpsertCursor
	if !nextTombstoneCursor.IsZero() {
		nt := nextTombstoneCursor
		compositeNext.Tombstone = &nt
	} else if cursor.Tombstone != nil {
		compositeNext.Tombstone = cursor.Tombstone
	}

	env := NewEnvelope(nil, "", false)
	c.JSON(200, listMessagesResponse{
		Items:      items,
		NextCursor: EncodeNextCursor(compositeNext),
		HasMore:    hasMoreUpserts || hasMoreTombstones,
		ServerTime: env.ServerTime,
	})
}

func desktopOAuthUID(c *gin.Context) uint {
	claims, ok := middleware.OAuthClaims(c)
	if !ok {
		return 0
	}
	return claims.BaseClaims.Id
}
