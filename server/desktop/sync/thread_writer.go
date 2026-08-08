//go:build desktop

package sync

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cloudproxy "server/desktop/cloud_proxy"
)

// UpsertThreads writes a batch of cloud-sourced thread deltas into
// the local SQLite w_workagent_thread table. Each item is either
// an upsert (keyed by uuid; new rows at cloud_sync_state='synced',
// existing rows update in place while preserving a local 'paused'
// preference) OR a delete (uuid resolved to local PK; row removed
// + messages cascaded + per-thread cursor cleared).
//
// Returns the number of rows actually changed. Failure on any row
// aborts the batch + returns the partial count + the error — the
// SyncWorker treats the whole tick as failed in that case and
// retries via backoff with the same cursor (idempotent retry,
// since upsert + delete are both idempotent).
//
// Per cloud-sync.md §5.2, items carry `action: "upsert" | "delete"`.
// P1.A.5a (this code) handles both. cloud-side tombstone emission
// (P1.A.5b) is the bookend that actually produces "delete" items.
// Unknown actions still error via ErrUnknownAction so a future
// shape change is loudly rejected.
//
// cascade-on-delete: deleting a thread also deletes its local
// messages + clears the per-thread message cursor. Two reasons:
//
//  1. Cloud may purge thread + messages together without emitting
//     per-message tombstones. Local cascade keeps SQLite clean.
//  2. Even if cloud DOES send message tombstones separately, they
//     arrive on a different sync tick. Leaving orphan messages
//     between the two ticks would show in ThreadView for any race
//     where the user opens the (now-deleted) thread before the
//     message tombstones land.
//
// cursorStore is optional — pass nil to skip the per-thread
// message-cursor cleanup (production caller always passes; tests
// without a cursor store can omit).
func UpsertThreads(db *gorm.DB, items []cloudproxy.ThreadDeltaItem, uid int, cursorStore *CursorStore) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("thread writer: db is nil")
	}
	if uid <= 0 {
		return 0, fmt.Errorf("thread writer: uid must be positive (got %d)", uid)
	}

	count := 0
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range items {
		if item.UUID == "" {
			return count, fmt.Errorf("thread writer: item missing uuid: %+v", item)
		}

		// Delete path.
		if item.Action == "delete" {
			n, err := deleteLocalThread(db, item.UUID, uid, cursorStore)
			if err != nil {
				return count, err
			}
			count += n
			continue
		}

		if item.Action != "upsert" {
			return count, fmt.Errorf("%w: action=%q (item uuid=%q)",
				ErrUnknownAction, item.Action, item.UUID)
		}
		if err := ensureThreadUUIDOwnedByUID(db, item.UUID, uid); err != nil {
			return count, err
		}

		// SQLite INSERT...ON CONFLICT(uuid) is the idiomatic upsert
		// (matches the SQLite version we use; MySQL would use
		// INSERT...ON DUPLICATE KEY UPDATE, but we're SQLite-only
		// on desktop). uuid has a UNIQUE index per the migration.
		//
		// updated_at takes the cloud's value if non-empty, else now;
		// cache_writer's local writes always set RFC3339Nano, so
		// the cloud round-trips fine.
		updatedAt := item.UpdatedAt
		if updatedAt == "" {
			updatedAt = now
		}
		createdAt := item.CreatedAt
		if createdAt == "" {
			createdAt = updatedAt
		}
		agentType := item.AgentType
		if agentType == "" {
			agentType = "general_agent"
		}

		res := db.Exec(`
			INSERT INTO w_workagent_thread
				(uid, uuid, name, agent_mode, agent_type, model,
				 message_count, msg_preview, file_count, is_public,
				 cloud_sync_state, cloud_thread_id, last_synced_at,
				 created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'synced', ?, ?, ?, ?)
			ON CONFLICT(uuid) DO UPDATE SET
				name             = excluded.name,
				agent_mode       = excluded.agent_mode,
				agent_type       = excluded.agent_type,
				model            = excluded.model,
				message_count    = excluded.message_count,
				msg_preview      = excluded.msg_preview,
				file_count       = excluded.file_count,
				is_public        = excluded.is_public,
				cloud_sync_state = CASE
					WHEN w_workagent_thread.cloud_sync_state = 'paused' THEN 'paused'
					ELSE 'synced'
				END,
				cloud_thread_id  = excluded.cloud_thread_id,
				last_synced_at   = excluded.last_synced_at,
				updated_at       = excluded.updated_at`,
			uid, item.UUID, item.Name, item.AgentMode, agentType, item.Model,
			item.MessageCount, item.MsgPreview, item.FileCount, item.IsPublic,
			item.CloudThreadID, now,
			createdAt, updatedAt,
		)
		if res.Error != nil {
			return count, fmt.Errorf("thread writer: upsert uuid=%q: %w", item.UUID, res.Error)
		}
		count++
	}
	return count, nil
}

// ErrCreatedThreadWriteCount guards the single-resource create contract.
var ErrCreatedThreadWriteCount = fmt.Errorf("thread writer: unexpected created-thread write count")

// ErrInvalidCreatedThread rejects a cloud create response that is not safe to
// use as the identity-bearing local cache row. The cloud client validates the
// same contract first; this second check keeps the exported commit boundary
// safe for future callers too.
var ErrInvalidCreatedThread = fmt.Errorf("thread writer: invalid created thread")

// CommittedThreadRow is the renderer-safe projection read back from SQLite in
// the same transaction that creates or recovers a cloud thread. Reading after
// the upsert matters for replay: local-only preferences such as "paused" are
// intentionally preserved and must not be overwritten in the Sidecar reply.
type CommittedThreadRow struct {
	UUID           string
	Name           string
	AgentMode      string
	MessageCount   int
	UpdatedAt      time.Time
	CloudSyncState string
}

// CommitCreatedThread commits one cloud thread resource under the exact
// authenticated session that fetched it. The entire SQLite
// Begin/write/Commit section runs inside SessionLease.WithCurrent, preserving
// the global TokenStore -> SQLite lock order and closing the account-switch
// TOCTOU before a newly created cloud thread becomes visible locally.
// It deliberately receives no CursorStore: creating one resource must never
// advance the incremental sync cursor past concurrent thread changes.
func CommitCreatedThread(
	ctx context.Context,
	db *gorm.DB,
	lease cloudproxy.SessionLease,
	uid int,
	item cloudproxy.ThreadDeltaItem,
) (CommittedThreadRow, error) {
	var committed CommittedThreadRow
	if db == nil {
		return committed, fmt.Errorf("%w: db is nil", ErrInvalidCreatedThread)
	}
	if uid <= 0 {
		return committed, fmt.Errorf("%w: uid must be positive", ErrInvalidCreatedThread)
	}
	if err := validateCreatedThreadItem(item); err != nil {
		return committed, err
	}
	item.Action = "upsert"
	err := runSessionTransaction(ctx, db, lease, func(tx *gorm.DB) error {
		written, err := UpsertThreads(tx, []cloudproxy.ThreadDeltaItem{item}, uid, nil)
		if err != nil {
			return err
		}
		if written != 1 {
			return fmt.Errorf("%w: got %d", ErrCreatedThreadWriteCount, written)
		}
		var updatedAt string
		if err := tx.Raw(`
			SELECT uuid, name, agent_mode, message_count, updated_at,
			       COALESCE(cloud_sync_state, 'synced')
			  FROM w_workagent_thread
			 WHERE uuid = ? AND uid = ?
			 LIMIT 1`, item.UUID, uid).Row().Scan(
			&committed.UUID,
			&committed.Name,
			&committed.AgentMode,
			&committed.MessageCount,
			&updatedAt,
			&committed.CloudSyncState,
		); err != nil {
			return fmt.Errorf("thread writer: read committed uuid=%q: %w", item.UUID, err)
		}
		committed.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil || committed.UpdatedAt.IsZero() || committed.CloudSyncState == "" {
			return fmt.Errorf("thread writer: invalid committed row uuid=%q", item.UUID)
		}
		return nil
	})
	if err != nil {
		return CommittedThreadRow{}, err
	}
	return committed, nil
}

func validateCreatedThreadItem(item cloudproxy.ThreadDeltaItem) error {
	if item.Action != "" && item.Action != "upsert" {
		return fmt.Errorf("%w: action=%q", ErrInvalidCreatedThread, item.Action)
	}
	parsedUUID, err := uuid.Parse(item.UUID)
	if err != nil || parsedUUID.Version() != 4 || parsedUUID.Variant() != uuid.RFC4122 || parsedUUID.String() != item.UUID {
		return fmt.Errorf("%w: uuid must be canonical v4", ErrInvalidCreatedThread)
	}
	cloudID, err := strconv.ParseUint(item.CloudThreadID, 10, 64)
	if err != nil || cloudID == 0 || strconv.FormatUint(cloudID, 10) != item.CloudThreadID {
		return fmt.Errorf("%w: cloud_thread_id must be a canonical positive integer", ErrInvalidCreatedThread)
	}
	if !validCreatedThreadText(item.Name, 200) ||
		!validCreatedThreadText(item.AgentMode, 50) ||
		item.AgentType != "general_agent" ||
		!validCreatedThreadText(item.Model, 255) ||
		item.MessageCount < 0 || item.FileCount < 0 || !utf8.ValidString(item.MsgPreview) {
		return fmt.Errorf("%w: malformed canonical fields", ErrInvalidCreatedThread)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, item.CreatedAt)
	if err != nil || createdAt.IsZero() {
		return fmt.Errorf("%w: created_at must be RFC3339", ErrInvalidCreatedThread)
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, item.UpdatedAt)
	if err != nil || updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return fmt.Errorf("%w: updated_at must be RFC3339 and not precede created_at", ErrInvalidCreatedThread)
	}
	return nil
}

func validCreatedThreadText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// ErrUnknownAction is returned by UpsertThreads / UpsertMessages
// when an item carries an action value other than the supported
// "upsert" or "delete". Pinned as a sentinel so the worker can
// errors.Is-check + log distinctly from generic DB errors.
var ErrUnknownAction = fmt.Errorf("sync writer: unknown action")

// ErrMissingParentThread is returned by UpsertMessages when a cloud
// message delta references a thread_uuid that has not landed in the
// local thread cache yet. Workers must treat this as retryable and
// must not advance the message cursor for the page.
var ErrMissingParentThread = fmt.Errorf("sync writer: missing parent thread")

// ErrUIDConflict is returned when a cloud delta's uuid already exists
// locally for a different uid. The local cache schema currently has a
// UNIQUE(uuid) constraint, so the writer cannot represent both rows. Failing
// loudly prevents overwriting another account's cache row and prevents the
// sync cursor from advancing past an item that was not written.
var ErrUIDConflict = fmt.Errorf("sync writer: uuid belongs to different uid")

func ensureThreadUUIDOwnedByUID(db *gorm.DB, uuid string, uid int) error {
	var existingUID int
	err := db.Raw(
		`SELECT uid FROM w_workagent_thread WHERE uuid = ? LIMIT 1`,
		uuid,
	).Row().Scan(&existingUID)
	if err == nil {
		if existingUID != uid {
			return fmt.Errorf("%w: thread uuid=%q existing_uid=%d active_uid=%d",
				ErrUIDConflict, uuid, existingUID, uid)
		}
		return nil
	}
	if err == sql.ErrNoRows {
		return nil
	}
	return fmt.Errorf("thread writer: check uuid ownership uuid=%q: %w", uuid, err)
}

// deleteLocalThread removes a thread row + cascades to its messages
// + clears the per-thread message cursor. Returns the count of rows
// affected (thread row + cascaded message rows), or 0 if the thread
// wasn't present locally (no-op; not an error — the cloud may emit
// a delete for a thread the sidecar never synced).
//
// uid scope: we DO filter by uid on the thread delete so a future
// multi-account local cache (Q5 still open) can't accidentally
// delete another user's thread that happens to share a uuid (uuids
// are unique on our table today; the uid filter is defense-in-depth
// for the day uuids stop being globally unique).
//
// cursorStore is optional — nil skips the cursor cleanup, which is
// fine for tests but production should always pass it. A leftover
// stale cursor wouldn't cause incorrect data (next message sync
// would fetch from cursor → presumably zero rows because the cloud
// purged them too), just unnecessary work.
func deleteLocalThread(db *gorm.DB, uuid string, uid int, cursorStore *CursorStore) (int, error) {
	// Resolve uuid → local PK (we need it to cascade to messages).
	var localID int64
	err := db.Raw(
		`SELECT id FROM w_workagent_thread WHERE uuid = ? AND uid = ? LIMIT 1`,
		uuid, uid,
	).Row().Scan(&localID)
	if err != nil || localID == 0 {
		// No matching local row — cloud emitted a tombstone for a
		// thread we never had. Not an error; idempotent no-op.
		return 0, nil
	}

	// Cascade: delete messages first (FK direction).
	res := db.Exec(`DELETE FROM w_workagent_message WHERE thread_id = ?`, localID)
	if res.Error != nil {
		return 0, fmt.Errorf("thread writer: cascade delete messages for thread %d: %w",
			localID, res.Error)
	}
	cascaded := int(res.RowsAffected)

	res = db.Exec(`DELETE FROM w_workagent_thread WHERE id = ?`, localID)
	if res.Error != nil {
		return cascaded, fmt.Errorf("thread writer: delete thread %d: %w",
			localID, res.Error)
	}

	if cursorStore != nil {
		// Clear the per-thread message cursor. Doesn't error if
		// the key doesn't exist (Delete is idempotent).
		_ = cursorStore.Delete(CursorKeyMessagesPrefix + uuid)
	}

	return cascaded + int(res.RowsAffected), nil
}
