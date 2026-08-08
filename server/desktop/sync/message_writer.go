//go:build desktop

package sync

import (
	"database/sql"
	"fmt"
	"time"

	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// UpsertMessages writes a batch of cloud-sourced message deltas
// into local SQLite w_workagent_message. Each item is upserted by
// uuid (the cross-cloud-and-local stable identifier). The local
// thread_id is resolved per call from the thread_uuid — the
// messages table only stores thread_id (integer), but the wire
// item carries thread_uuid (cross-cloud-stable).
//
// Streaming state: cloud messages always land at 'complete' on
// the sidecar side (the cache_writer's 'streaming' / 'partial'
// states are local-write artifacts; cloud's authoritative view
// of any persisted message is complete by definition). If a local
// row exists in 'partial' state (interrupted write that the cloud
// later replicated), the upsert flips it to 'complete' because
// the cloud row is authoritative.
//
// Returns the number of rows actually changed (insert / update /
// delete counted equally). Per-row failure aborts the batch +
// returns the partial count + the error; idempotent retry next
// tick resumes from the cursor that was saved before this batch.
//
// Action handling:
//   - "upsert": insert or update by uuid (most rows)
//   - "delete": remove the row by uuid. Idempotent — a delete for
//     a uuid not present locally is a no-op, not an error. Doesn't
//     require thread_uuid (we already know which row by uuid alone).
//   - anything else → ErrUnknownAction (worker logs + backs off)
//
// If an upsert references a thread_uuid that is not present locally,
// the writer returns ErrMissingParentThread after any earlier rows
// in the batch have been applied. This is intentionally a retryable
// batch failure: returning success would let MessagesJob advance the
// cursor past rows it could not write.
func UpsertMessages(db *gorm.DB, items []cloudproxy.MessageDeltaItem, uid int) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("message writer: db is nil")
	}
	if uid <= 0 {
		return 0, fmt.Errorf("message writer: uid must be positive (got %d)", uid)
	}
	if len(items) == 0 {
		return 0, nil
	}

	// Cache the thread_uuid → local thread_id lookups across the
	// batch. A typical sync page has many messages from the SAME
	// thread (the cloud endpoint scopes by thread_id); without
	// this we'd run N redundant SELECTs.
	threadIDByUUID := make(map[string]int64, 4)

	count := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range items {
		if item.UUID == "" {
			return count, fmt.Errorf("message writer: item missing uuid: %+v", item)
		}

		// Delete path: doesn't need thread_uuid (uuid alone identifies
		// the row); doesn't need to resolve thread_id.
		if item.Action == "delete" {
			res := db.Exec(`DELETE FROM w_workagent_message WHERE uuid = ? AND uid = ?`,
				item.UUID, uid)
			if res.Error != nil {
				return count, fmt.Errorf("message writer: delete uuid=%q: %w",
					item.UUID, res.Error)
			}
			count += int(res.RowsAffected)
			continue
		}

		if item.Action != "upsert" {
			return count, fmt.Errorf("%w: action=%q (item uuid=%q)",
				ErrUnknownAction, item.Action, item.UUID)
		}
		if item.ThreadUUID == "" {
			return count, fmt.Errorf("message writer: item missing thread_uuid (uuid=%q)", item.UUID)
		}
		if err := ensureMessageUUIDOwnedByUID(db, item.UUID, uid); err != nil {
			return count, err
		}

		// Resolve thread_uuid → local thread_id (cache across batch).
		threadID, ok := threadIDByUUID[item.ThreadUUID]
		if !ok {
			err := db.Raw(
				`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ? LIMIT 1`,
				item.ThreadUUID, uid,
			).Row().Scan(&threadID)
			if err != nil || threadID == 0 {
				// Thread not present locally — race between
				// threads sync and messages sync. Return an error
				// so MessagesJob does NOT advance the cursor past
				// rows it could not write; the next tick (after
				// threads sync catches up) retries idempotently.
				return count, fmt.Errorf("%w: thread_uuid=%q (message uuid=%q)",
					ErrMissingParentThread, item.ThreadUUID, item.UUID)
			}
			threadIDByUUID[item.ThreadUUID] = threadID
		}

		updatedAt := item.UpdatedAt
		if updatedAt == "" {
			updatedAt = now
		}
		createdAt := item.CreatedAt
		if createdAt == "" {
			createdAt = updatedAt
		}

		res := db.Exec(`
			INSERT INTO w_workagent_message
				(uid, uuid, thread_id, user_text, ai_text, chat_mode,
				 content_type, structured_content, actions, metadata,
				 use_images, use_files, user_rating, user_feedback,
				 streaming_state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'complete', ?, ?)
			ON CONFLICT(uuid) DO UPDATE SET
				user_text          = excluded.user_text,
				ai_text            = excluded.ai_text,
				chat_mode          = excluded.chat_mode,
				content_type       = excluded.content_type,
				structured_content = excluded.structured_content,
				actions            = excluded.actions,
				metadata           = excluded.metadata,
				use_images         = excluded.use_images,
				use_files          = excluded.use_files,
				user_rating        = excluded.user_rating,
				user_feedback      = excluded.user_feedback,
				streaming_state    = 'complete',
				updated_at         = excluded.updated_at`,
			uid, item.UUID, threadID,
			item.UserText, item.AIText, item.ChatMode,
			item.ContentType, item.StructuredContent, item.Actions, item.Metadata,
			item.UseImages, item.UseFiles, item.UserRating, item.UserFeedback,
			createdAt, updatedAt,
		)
		if res.Error != nil {
			return count, fmt.Errorf("message writer: upsert uuid=%q: %w", item.UUID, res.Error)
		}
		count++
	}
	return count, nil
}

func ensureMessageUUIDOwnedByUID(db *gorm.DB, uuid string, uid int) error {
	var existingUID int
	err := db.Raw(
		`SELECT uid FROM w_workagent_message WHERE uuid = ? LIMIT 1`,
		uuid,
	).Row().Scan(&existingUID)
	if err == nil {
		if existingUID != uid {
			return fmt.Errorf("%w: message uuid=%q existing_uid=%d active_uid=%d",
				ErrUIDConflict, uuid, existingUID, uid)
		}
		return nil
	}
	if err == sql.ErrNoRows {
		return nil
	}
	return fmt.Errorf("message writer: check uuid ownership uuid=%q: %w", uuid, err)
}
