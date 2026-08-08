package sync

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// MessageDeltaRow is the wire shape of one item in GET
// /api/desktop/sync/messages. Mirrors cloud-sync.md §5.4 — the
// sync endpoint emits the same "full message" shape the cache
// can rebuild from, plus uid for IDOR boundary check.
//
// IDs: cloud_message_id is the local PK rendered as a string
// (same convention as cloud_thread_id in P1.A.2). uuid is the
// stable cross-cloud-and-local identifier; the desktop's local
// SQLite indexes on uuid for upserts.
//
// Excluded from this delta (vs the full MySQL ChatMessage):
//   - total_prompt (huge; only useful for admin diagnostics)
//   - ip / task_id (server-side metering fields)
//   - deduct_integral / refund_integral / use_tokens / etc.
//     (billing-side fields; renderer doesn't surface them)
//   - use_audio / ai_audio (audio file refs; deferred until
//     audio chat lands on the desktop)
//
// Included because the renderer DOES surface them:
//   - chat_mode (for skill-specific render paths)
//   - content_type + structured_content (agent message system)
//   - actions (interactive buttons in agent responses)
//   - metadata (plan / progress / etc.)
//   - user_rating + user_feedback (thumbs-down + critique injection)
//   - use_images + use_files (file refs in the turn)
type MessageDeltaRow struct {
	CloudMessageID    string    `json:"cloud_message_id"`
	UUID              string    `json:"uuid"`
	ThreadUUID        string    `json:"thread_uuid"`
	UserText          string    `json:"user_text"`
	AIText            string    `json:"ai_text"`
	ChatMode          string    `json:"chat_mode"`
	ContentType       string    `json:"content_type,omitempty"`
	StructuredContent string    `json:"structured_content,omitempty"`
	Actions           string    `json:"actions,omitempty"`
	Metadata          string    `json:"metadata,omitempty"`
	UseImages         string    `json:"use_images,omitempty"`
	UseFiles          string    `json:"use_files,omitempty"`
	UserRating        int       `json:"user_rating"`
	UserFeedback      string    `json:"user_feedback,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
	CreatedAt         time.Time `json:"created_at"`
}

// ErrThreadNotOwned is returned by ListMessagesDelta when the
// requesting uid doesn't own the cloud_thread_id. Distinct from
// "thread doesn't exist" so the handler can collapse both to a
// 404 per RFC-style "don't leak existence"; tests can distinguish.
var ErrThreadNotOwned = errors.New("sync messages: thread not owned by uid")

// ListMessagesDelta returns the next page of message upserts for
// the given (uid, cloud_thread_id) after `cursor`. Returns rows
// in (updated_at ASC, id ASC) order; `next` is the cursor for
// the following page; `hasMore` reports remaining pages.
//
// IDOR posture: looks up the thread by id AND uid in one query.
// If the row doesn't exist OR belongs to another uid, returns
// ErrThreadNotOwned — caller collapses to 404. Same posture as
// production work-agent's LoadByIDForOwner.
//
// Pagination + time-handling: identical to ListThreadsDelta
// (limit+1 fetch to detect has_more, OR-form WHERE clause,
// parseRowTime for SQLite-vs-MySQL portability).
func ListMessagesDelta(ctx context.Context, db *gorm.DB, uid int, cloudThreadID uint64, cursor Cursor, limit int) (rows []MessageDeltaRow, next Cursor, hasMore bool, err error) {
	if db == nil {
		return nil, Cursor{}, false, fmt.Errorf("sync messages: db is nil")
	}
	if uid <= 0 {
		return nil, Cursor{}, false, fmt.Errorf("sync messages: uid must be positive (got %d)", uid)
	}
	if cloudThreadID == 0 {
		return nil, Cursor{}, false, fmt.Errorf("sync messages: cloud_thread_id required")
	}
	if limit <= 0 {
		limit = 100
	}

	// IDOR check + thread_uuid lookup in one query. We need uuid
	// to populate ThreadUUID on every row (the messages table only
	// stores the integer thread_id; renderer needs the uuid for
	// routing).
	var threadUUID string
	err = db.WithContext(ctx).
		Raw(`SELECT uuid FROM w_workagent_thread WHERE id = ? AND uid = ? LIMIT 1`,
			cloudThreadID, uid).
		Row().Scan(&threadUUID)
	if err != nil || threadUUID == "" {
		return nil, Cursor{}, false, ErrThreadNotOwned
	}

	type scanRow struct {
		ID                uint
		UUID              string
		UserText          string
		AIText            string
		ChatMode          string
		ContentType       string
		StructuredContent string
		Actions           string
		Metadata          string
		UseImages         string
		UseFiles          string
		UserRating        int
		UserFeedback      string
		UpdatedAt         string
		CreatedAt         string
	}
	var scanRows []scanRow

	// GORM maps SELECT columns to struct fields by name. COALESCE
	// expressions need explicit aliases (otherwise the result column
	// name is the full expression text + nothing matches).
	tx := db.WithContext(ctx).
		Table("w_workagent_message").
		Select(`id, uuid,
		        COALESCE(user_text,'')          AS user_text,
		        COALESCE(ai_text,'')            AS ai_text,
		        chat_mode,
		        COALESCE(content_type,'')       AS content_type,
		        COALESCE(structured_content,'') AS structured_content,
		        COALESCE(actions,'')            AS actions,
		        COALESCE(metadata,'')           AS metadata,
		        COALESCE(use_images,'')         AS use_images,
		        COALESCE(use_files,'')          AS use_files,
		        user_rating,
		        COALESCE(user_feedback,'')      AS user_feedback,
		        updated_at, created_at`).
		Where("thread_id = ?", cloudThreadID)

	if !cursor.IsZero() {
		cursorTime := cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)
		tx = tx.Where(
			"updated_at > ? OR (updated_at = ? AND id > ?)",
			cursorTime, cursorTime, cursor.ID,
		)
	}

	if err := tx.Order("updated_at ASC, id ASC").
		Limit(limit + 1).
		Scan(&scanRows).Error; err != nil {
		return nil, Cursor{}, false, fmt.Errorf("sync messages: query: %w", err)
	}

	hasMore = len(scanRows) > limit
	if hasMore {
		scanRows = scanRows[:limit]
	}

	rows = make([]MessageDeltaRow, len(scanRows))
	var lastUpdatedAt time.Time
	for i, r := range scanRows {
		updatedAt := parseRowTime(r.UpdatedAt)
		createdAt := parseRowTime(r.CreatedAt)
		rows[i] = MessageDeltaRow{
			CloudMessageID:    fmt.Sprintf("%d", r.ID),
			UUID:              r.UUID,
			ThreadUUID:        threadUUID,
			UserText:          r.UserText,
			AIText:            r.AIText,
			ChatMode:          r.ChatMode,
			ContentType:       r.ContentType,
			StructuredContent: r.StructuredContent,
			Actions:           r.Actions,
			Metadata:          r.Metadata,
			UseImages:         r.UseImages,
			UseFiles:          r.UseFiles,
			UserRating:        r.UserRating,
			UserFeedback:      r.UserFeedback,
			UpdatedAt:         updatedAt,
			CreatedAt:         createdAt,
		}
		lastUpdatedAt = updatedAt
	}

	if len(rows) > 0 {
		last := scanRows[len(scanRows)-1]
		next = Cursor{UpdatedAt: lastUpdatedAt, ID: int64(last.ID)}
	} else {
		next = cursor
	}
	return rows, next, hasMore, nil
}
