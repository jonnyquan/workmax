package sync

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ThreadDeltaRow is the per-row shape ListThreadsDelta returns. It
// mirrors the JSON wire shape of GET /api/desktop/sync/threads
// (cloud-sync.md §5.2) MINUS the `action` field — every row from
// this repo is an upsert. Delete actions land in P1.A.5 via the
// tombstone table sweep.
//
// CloudThreadID is the row PK rendered as a string. cloud-sync.md
// §5.2 shows it as a string-shaped opaque id; we keep the wire
// contract stringly-typed so a future switch to a string-format PK
// (e.g. "t_<ts>_<rand>") doesn't change the JSON shape.
//
// Deliberately excluded from this delta view:
//   - prompt / latest_plan / plan_history — large columns; per
//     cloud-sync.md §5.2 fetched only via /api/desktop/sync/threads/:id
//     (P1.A.3) when the user opens the thread.
//   - recipe_id — the Recipe layer is retired (project memory
//     [[project_recipe_layer_retired]] + commit dd8a7ae8). The spec
//     in cloud-sync.md §5.2 still shows the field; deviating here
//     so we don't emit a permanently-empty string clients have to
//     ignore.
//   - workspace_path / max_tokens / temperature / etc — control-plane
//     knobs the desktop renderer doesn't need for the thread-list
//     view. Single-thread fetch can include them when needed.
type ThreadDeltaRow struct {
	CloudThreadID string    `json:"cloud_thread_id"`
	UUID          string    `json:"uuid"`
	Name          string    `json:"name"`
	AgentMode     string    `json:"agent_mode"`
	AgentType     string    `json:"agent_type"`
	Model         string    `json:"model"`
	MessageCount  int       `json:"message_count"`
	MsgPreview    string    `json:"msg_preview"`
	FileCount     int       `json:"file_count"`
	IsPublic      bool      `json:"is_public"`
	UpdatedAt     time.Time `json:"updated_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListThreadsDelta returns the next page of thread upserts for `uid`
// after `cursor`. Returns rows in (updated_at ASC, id ASC) order;
// `next` is the cursor to pass back on the following page;
// `hasMore` reports whether more pages remain.
//
// Empty cursor (Cursor.IsZero()) means "from beginning" — full sync.
//
// Pagination strategy: query limit+1 rows; if we got limit+1 the
// extra row is dropped and we know there's more. The cursor encodes
// the LAST returned row's (updated_at, id), so the next page
// resumes immediately after it.
//
// Uses row-wise comparison `(updated_at, id) > (?, ?)` expressed
// as the equivalent OR-form which all our supported SQL backends
// (SQLite in tests, MySQL in prod) handle without index-defeating
// subqueries:
//
//	WHERE updated_at > ?
//	   OR (updated_at = ? AND id > ?)
//
// Both clauses use the existing index hint on (uid, updated_at, id)
// established by idx_w_workagent_thread_listing in
// server/migrations (and the parallel SQLite migration).
func ListThreadsDelta(ctx context.Context, db *gorm.DB, uid int, cursor Cursor, limit int) (rows []ThreadDeltaRow, next Cursor, hasMore bool, err error) {
	if db == nil {
		return nil, Cursor{}, false, fmt.Errorf("sync threads: db is nil")
	}
	if uid <= 0 {
		return nil, Cursor{}, false, fmt.Errorf("sync threads: uid must be positive (got %d)", uid)
	}
	if limit <= 0 {
		limit = 100
	}

	// scanRow holds updated_at / created_at as STRINGS rather than
	// time.Time. Reason: SQLite stores datetimes as TEXT (per our
	// migration DDL); GORM's default Scan path can't unmarshal that
	// directly into time.Time and errors with "unsupported Scan,
	// storing driver.Value type string". We parse manually in the
	// row loop via parseRowTime. MySQL DATETIME columns return
	// time.Time-compatible drivers — those would unmarshal fine
	// directly, but the string path works for both backends without
	// branching.
	type scanRow struct {
		ID           uint
		UUID         string
		Name         string
		AgentMode    string
		AgentType    string
		Model        string
		MessageCount int
		MsgPreview   string
		FileCount    int
		IsPublic     bool
		UpdatedAt    string
		CreatedAt    string
	}
	var scanRows []scanRow

	tx := db.WithContext(ctx).
		Table("w_workagent_thread").
		Select(`id, uuid, name, agent_mode, agent_type, model,
		        message_count, msg_preview, file_count, is_public,
		        updated_at, created_at`).
		Where("uid = ?", uid)

	if !cursor.IsZero() {
		// Format cursor times as ISO8601 strings so SQLite (TEXT
		// column) compares lexicographically correctly — ISO8601's
		// big-endian shape means string compare == time compare for
		// times in the same timezone. MySQL handles either; SQLite
		// REQUIRES the string form because passing a time.Time would
		// be marshaled as a Go-default RFC3339-ish form that may not
		// match what's stored.
		cursorTime := cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)
		tx = tx.Where(
			"updated_at > ? OR (updated_at = ? AND id > ?)",
			cursorTime, cursorTime, cursor.ID,
		)
	}

	if err := tx.Order("updated_at ASC, id ASC").
		Limit(limit + 1).
		Scan(&scanRows).Error; err != nil {
		return nil, Cursor{}, false, fmt.Errorf("sync threads: query: %w", err)
	}

	hasMore = len(scanRows) > limit
	if hasMore {
		scanRows = scanRows[:limit]
	}

	rows = make([]ThreadDeltaRow, len(scanRows))
	var lastUpdatedAt time.Time
	for i, r := range scanRows {
		updatedAt := parseRowTime(r.UpdatedAt)
		createdAt := parseRowTime(r.CreatedAt)
		rows[i] = ThreadDeltaRow{
			CloudThreadID: fmt.Sprintf("%d", r.ID),
			UUID:          r.UUID,
			Name:          r.Name,
			AgentMode:     r.AgentMode,
			AgentType:     r.AgentType,
			Model:         r.Model,
			MessageCount:  r.MessageCount,
			MsgPreview:    r.MsgPreview,
			FileCount:     r.FileCount,
			IsPublic:      r.IsPublic,
			UpdatedAt:     updatedAt,
			CreatedAt:     createdAt,
		}
		lastUpdatedAt = updatedAt
	}

	// Next cursor points at the LAST returned row (so the next page
	// resumes from just after it). If no rows returned, return the
	// caller's cursor unchanged — a poll-with-no-changes shouldn't
	// reset the resume point.
	if len(rows) > 0 {
		last := scanRows[len(scanRows)-1]
		next = Cursor{UpdatedAt: lastUpdatedAt, ID: int64(last.ID)}
	} else {
		next = cursor
	}

	return rows, next, hasMore, nil
}

// parseRowTime tolerates the formats SQLite + MySQL return for
// datetime columns. SQLite stores TEXT per our migration DDL;
// MySQL DATETIME serializes via the driver. Returns zero time
// on unparseable input — callers see an empty Time in the wire
// row rather than a hard error (a single malformed row shouldn't
// poison the whole sync page).
func parseRowTime(s string) time.Time {
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
