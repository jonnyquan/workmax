package sync

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"server/model/workagent"
)

// TombstoneItem is the projection used when merging tombstones into
// /sync delta responses. EntityUUID is what the sidecar reads to
// locate the local row; DeletedAt drives the cursor ordering.
type TombstoneItem struct {
	EntityType string
	EntityID   uint
	EntityUUID string
	DeletedAt  time.Time
}

// InsertTombstone records a delete-propagation marker. Caller must
// pass the SAME transaction that hard-deleted the underlying row so
// the two land atomically — orphaned tombstones (or row gone without
// tombstone) would cause sync-loop weirdness for desktop clients.
//
// entityType is validated against workagent.IsValidTombstoneEntityType.
// A typo (e.g. "thrad") would silently break delete propagation
// because ListTombstonesDelta filters by entity_type, so we fail
// fast at write time instead.
func InsertTombstone(tx *gorm.DB, uid int, entityType string, entityID uint, entityUUID string) error {
	if tx == nil {
		return fmt.Errorf("tombstone: tx is nil (must pass the transaction that deleted the row)")
	}
	if uid <= 0 {
		return fmt.Errorf("tombstone: uid must be positive (got %d)", uid)
	}
	if !workagent.IsValidTombstoneEntityType(entityType) {
		return fmt.Errorf("tombstone: invalid entity_type %q (must be one of: thread, message)", entityType)
	}
	if entityID == 0 {
		return fmt.Errorf("tombstone: entity_id required")
	}
	if entityUUID == "" {
		return fmt.Errorf("tombstone: entity_uuid required (sidecar can't locate local row without it)")
	}
	// Raw Exec instead of tx.Create(&row): the model's DeletedAt is
	// time.Time, and SQLite (used by tests) can't auto-Scan from the
	// TEXT column back into time.Time on the post-INSERT round-trip
	// GORM does. Raw INSERT skips that scan-back and works on both
	// SQLite (test) and MySQL (prod) without backend-specific code.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := tx.Exec(
		`INSERT INTO w_workagent_tombstone (uid, entity_type, entity_id, entity_uuid, deleted_at)
		 VALUES (?, ?, ?, ?, ?)`,
		uid, entityType, entityID, entityUUID, now,
	)
	if res.Error != nil {
		return fmt.Errorf("tombstone: create: %w", res.Error)
	}
	return nil
}

// PruneTombstones deletes tombstone rows older than `retention`.
// Returns the number of rows actually deleted.
//
// Per migration 20260642 doc comment, retention is 90 days in
// production. Desktop clients that haven't synced in 90 days are
// expected to re-sync everything from scratch on next connect
// anyway (their threads / messages cursor is also 90 days behind),
// so the dropped tombstones don't manifest as user-visible drift.
//
// `batch` caps the per-call delete size so a long-overdue first
// sweep on a tombstone-heavy table doesn't lock for minutes. The
// caller (TombstoneSweeper) re-invokes until 0 rows are affected.
// Pass 0 for "no batch cap" (single DELETE without LIMIT —
// acceptable for sweepers that run frequently enough that the
// per-tick row count stays small).
//
// Returns (rowsAffected, err). nil error + rowsAffected=0 means
// "nothing to prune" — sweeper uses this to know to stop iterating.
//
// Note: MySQL supports DELETE ... LIMIT N natively; SQLite doesn't
// (without SQLITE_ENABLE_UPDATE_DELETE_LIMIT compile flag, which
// most distributions disable). For portability we use a sub-SELECT
// to pick a bounded set of ids, then delete by id. The extra
// SELECT-from-(SELECT … LIMIT …) wrapper is required for MySQL:
// `DELETE … WHERE id IN (SELECT … LIMIT n)` raises Error 1235
// ("This version of MySQL doesn't yet support 'LIMIT &
// IN/ALL/ANY/SOME subquery'"). Wrapping in a derived table forces
// MySQL to materialize the inner result and lifts the restriction.
// SQLite tolerates the derived-table form identically.
func PruneTombstones(db *gorm.DB, retention time.Duration, batch int) (int64, error) {
	if db == nil {
		return 0, fmt.Errorf("tombstone prune: db is nil")
	}
	if retention <= 0 {
		return 0, fmt.Errorf("tombstone prune: retention must be positive (got %v)", retention)
	}
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339Nano)

	var res *gorm.DB
	if batch > 0 {
		res = db.Exec(
			`DELETE FROM w_workagent_tombstone
			 WHERE id IN (
			   SELECT id FROM (
			     SELECT id FROM w_workagent_tombstone
			     WHERE deleted_at < ?
			     ORDER BY deleted_at ASC, id ASC
			     LIMIT ?
			   ) AS to_prune
			 )`,
			cutoff, batch,
		)
	} else {
		res = db.Exec(`DELETE FROM w_workagent_tombstone WHERE deleted_at < ?`, cutoff)
	}
	if res.Error != nil {
		return 0, fmt.Errorf("tombstone prune: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// ListTombstonesDelta returns tombstones for `uid` + `entityType`
// after `cursor`, ordered by (deleted_at ASC, id ASC). Same
// pagination + cursor semantics as ListThreadsDelta / ListMessagesDelta
// (limit+1 fetch to detect has_more; cursor encodes the last row).
//
// Cursor reuses the same encoded form as the upsert-side cursor —
// MergeUpsertsAndTombstones (caller) tracks BOTH a thread-upsert
// cursor and a thread-tombstone cursor and emits the union as one
// response. The two cursors get concatenated into a composite the
// caller decodes on next request.
//
// Returns rows ordered oldest first so the cursor advances
// monotonically. hasMore=true means there are more tombstones
// behind this page.
func ListTombstonesDelta(ctx context.Context, db *gorm.DB, uid int, entityType string, cursor Cursor, limit int) (rows []TombstoneItem, next Cursor, hasMore bool, err error) {
	if db == nil {
		return nil, Cursor{}, false, fmt.Errorf("tombstone list: db is nil")
	}
	if uid <= 0 {
		return nil, Cursor{}, false, fmt.Errorf("tombstone list: uid must be positive (got %d)", uid)
	}
	if !workagent.IsValidTombstoneEntityType(entityType) {
		return nil, Cursor{}, false, fmt.Errorf("tombstone list: invalid entity_type %q", entityType)
	}
	if limit <= 0 {
		limit = 100
	}

	type scanRow struct {
		ID         uint
		EntityID   uint
		EntityUUID string
		DeletedAt  string
	}
	var scanRows []scanRow

	tx := db.WithContext(ctx).
		Table("w_workagent_tombstone").
		Select(`id, entity_id, entity_uuid, deleted_at`).
		Where("uid = ? AND entity_type = ?", uid, entityType)

	if !cursor.IsZero() {
		cursorTime := cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)
		tx = tx.Where(
			"deleted_at > ? OR (deleted_at = ? AND id > ?)",
			cursorTime, cursorTime, cursor.ID,
		)
	}

	if err := tx.Order("deleted_at ASC, id ASC").
		Limit(limit + 1).
		Scan(&scanRows).Error; err != nil {
		return nil, Cursor{}, false, fmt.Errorf("tombstone list: query: %w", err)
	}

	hasMore = len(scanRows) > limit
	if hasMore {
		scanRows = scanRows[:limit]
	}

	rows = make([]TombstoneItem, len(scanRows))
	var lastDeletedAt time.Time
	for i, r := range scanRows {
		deletedAt := parseRowTime(r.DeletedAt)
		rows[i] = TombstoneItem{
			EntityType: entityType,
			EntityID:   r.EntityID,
			EntityUUID: r.EntityUUID,
			DeletedAt:  deletedAt,
		}
		lastDeletedAt = deletedAt
	}
	if len(rows) > 0 {
		last := scanRows[len(scanRows)-1]
		next = Cursor{UpdatedAt: lastDeletedAt, ID: int64(last.ID)}
	} else {
		next = cursor
	}
	return rows, next, hasMore, nil
}
