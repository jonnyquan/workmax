//go:build desktop

package desktop

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// LocalThreadRow is the per-thread row the renderer sees from
// GET /agent/threads. Mirrors the columns w_workagent_thread holds
// locally, omitting fields that only matter cloud-side (msg_preview
// recomputation, pricing knobs, etc.).
type LocalThreadRow struct {
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	AgentMode    string    `json:"agent_mode"`
	MessageCount int       `json:"message_count"`
	UpdatedAt    time.Time `json:"updated_at"`
	CloudSync    string    `json:"cloud_sync_state"`
	Pinned       bool      `json:"pinned"`
}

// LocalMessageRow is the per-message row the renderer sees from
// GET /agent/threads/:uuid/messages. Carries enough to render the
// turn in offline browse mode but skips MySQL-only metering fields.
type LocalMessageRow struct {
	UUID           string    `json:"uuid"`
	UserText       string    `json:"user_text"`
	AIText         string    `json:"ai_text"`
	ChatMode       string    `json:"chat_mode"`
	// Which engine produced this answer and which model it was told to use.
	// Empty on every row written before migration 0013, and on any turn the
	// sidecar did not announce — the renderer says nothing rather than
	// labelling an answer with a setting that may have changed since.
	AgentEngine    string    `json:"agent_engine"`
	AgentModel     string    `json:"agent_model"`
	AgentMind      string    `json:"agent_mind"`
	StreamingState string    `json:"streaming_state"` // "streaming" | "complete" | "partial"
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ListLocalThreads returns up to `limit` most-recent threads in the
// local cache, newest first by updated_at. uid filter is applied so
// the eventual multi-account-on-one-machine story (Q5 still open)
// has a hook ready; pass 0 to return all rows.
//
// includePaused controls the local-only sync preference. Paused rows
// are still present in SQLite (and can be resumed later), but callers
// that want the normal "active conversation list" can hide them with
// includePaused=false.
//
// Raw SQL (rather than a GORM model) keeps this self-contained: the
// MySQL-side model.Thread has 30+ columns we don't write on desktop
// and pulling it in would drag the cloud server's transitive deps
// into the desktop build.
func ListLocalThreads(db *gorm.DB, uid uint64, limit int, includePaused bool) ([]LocalThreadRow, error) {
	if db == nil {
		return nil, fmt.Errorf("list threads: db is nil")
	}
	if limit <= 0 {
		limit = 50
	}
	// message_count is written by cloud sync, so a thread that only ever
	// existed on this machine reported 0 no matter how much was in it — the
	// sidebar said "0 messages" next to a conversation the user had just had.
	// The larger of the two is the honest answer: the stored count can exceed
	// what is cached locally (the cloud knows about messages this machine has
	// not pulled), and the local count can exceed the stored one (local-only
	// turns the cloud has never seen).
	// Pins live in their own uid-scoped table (see migration 0006), so a
	// sync upsert of the thread row can never clobber the preference.
	baseQuery := `
		SELECT uuid, name, agent_mode,
		       MAX(
		         message_count,
		         (SELECT COUNT(*) FROM w_workagent_message m
		           WHERE m.thread_id = w_workagent_thread.id
		             AND m.uid = w_workagent_thread.uid)
		       ),
		       updated_at,
		       COALESCE(cloud_sync_state, 'synced'),
		       EXISTS(
		         SELECT 1 FROM w_desktop_thread_pin p
		          WHERE p.uid = w_workagent_thread.uid
		            AND p.thread_uuid = w_workagent_thread.uuid
		       )
		  FROM w_workagent_thread
		 WHERE agent_type = 'general_agent'`
	args := []any{}
	if uid != 0 {
		baseQuery += `
		   AND uid = ?`
		args = append(args, uid)
	}
	if !includePaused {
		baseQuery += `
		   AND COALESCE(cloud_sync_state, 'synced') <> 'paused'`
	}
	// Pinned first, then recency: the pin IS a sort preference, and a pin
	// that does not move the row is a star sticker, not a feature.
	baseQuery += `
		 ORDER BY 7 DESC, updated_at DESC, id DESC
		 LIMIT ?`
	args = append(args, limit)

	rows, err := db.Raw(baseQuery, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("list threads: query: %w", err)
	}
	defer rows.Close()

	out := make([]LocalThreadRow, 0, limit)
	for rows.Next() {
		var (
			r         LocalThreadRow
			updatedAt string
			pinned    int
		)
		if err := rows.Scan(&r.UUID, &r.Name, &r.AgentMode, &r.MessageCount, &updatedAt, &r.CloudSync, &pinned); err != nil {
			return nil, fmt.Errorf("list threads: scan: %w", err)
		}
		r.Pinned = pinned == 1
		r.UpdatedAt = parseSQLiteTime(updatedAt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list threads: rows.Err: %w", err)
	}
	return out, nil
}

// ListLocalMessages returns up to `limit` messages for the given
// thread uuid, oldest first by the message's authored timestamp
// (the renderer scrolls newest-at-bottom in chat mode). uid filter
// is applied to the parent thread so a stale cross-account cache row
// cannot be read after account switch; pass 0 to return all rows in
// tests/diagnostic boots that do not have an active token.
// Returns an empty slice when the thread doesn't exist locally —
// never errors on missing data.
func ListLocalMessages(db *gorm.DB, uid uint64, threadUUID string, limit int) ([]LocalMessageRow, error) {
	if db == nil {
		return nil, fmt.Errorf("list messages: db is nil")
	}
	if threadUUID == "" {
		return nil, fmt.Errorf("list messages: thread_uuid required")
	}
	if limit <= 0 {
		limit = 200
	}

	// Look up the integer PK first; messages join by thread_id, not
	// thread_uuid (the messages table doesn't carry the uuid column).
	var threadID int64
	err := db.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND (? = 0 OR uid = ?)`,
		threadUUID, uid, uid,
	).Row().Scan(&threadID)
	if err != nil {
		// Missing thread = empty messages, not an error. Renderer can
		// render an "empty thread" state cleanly.
		return []LocalMessageRow{}, nil
	}

	baseQuery := `
		SELECT uuid,
		       COALESCE(user_text, ''),
		       COALESCE(ai_text, ''),
		       chat_mode,
		       COALESCE(agent_engine, ''),
		       COALESCE(agent_model, ''),
		       COALESCE(agent_mind, ''),
		       streaming_state,
		       created_at,
		       updated_at
		  FROM w_workagent_message
		 WHERE thread_id = ?`
	args := []any{threadID}
	if uid != 0 {
		baseQuery += `
		   AND uid = ?`
		args = append(args, uid)
	}
	baseQuery += `
		 ORDER BY created_at ASC, id ASC
		 LIMIT ?`
	args = append(args, limit)

	rows, err := db.Raw(baseQuery, args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("list messages: query: %w", err)
	}
	defer rows.Close()

	out := make([]LocalMessageRow, 0, limit)
	for rows.Next() {
		var (
			r                    LocalMessageRow
			createdAt, updatedAt string
		)
		if err := rows.Scan(&r.UUID, &r.UserText, &r.AIText, &r.ChatMode,
			&r.AgentEngine, &r.AgentModel, &r.AgentMind, &r.StreamingState, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("list messages: scan: %w", err)
		}
		r.CreatedAt = parseSQLiteTime(createdAt)
		r.UpdatedAt = parseSQLiteTime(updatedAt)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list messages: rows.Err: %w", err)
	}
	return out, nil
}

// parseSQLiteTime tolerates the multiple timestamp formats SQLite
// returns:
//   - RFC3339 with nanoseconds (what cache_writer + this codebase writes)
//   - SQLite's default "YYYY-MM-DD HH:MM:SS" form (DEFAULT CURRENT_TIMESTAMP)
//   - Empty string (NULL columns) — returns zero time
//
// Returns a zero time.Time on parse failure rather than erroring; the
// renderer can decide whether to show "—" or fall back to a placeholder.
func parseSQLiteTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
