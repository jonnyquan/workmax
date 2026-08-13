//go:build desktop

package cloud_proxy

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// cacheStreamingState mirrors the values w_workagent_message.streaming_state
// accepts. cloud-sync.md §6 lists "complete" / "partial"; we add "streaming"
// as a transient state so a row that's in-flight can be distinguished from
// a row a previous run abandoned.
const (
	streamingStateActive   = "streaming"
	streamingStateComplete = "complete"
	streamingStatePartial  = "partial"
)

// CacheWriter buffers SSE event text into a single
// w_workagent_message row and persists incrementally.
//
// Flush triggers (cloud-proxy.md §5.4):
//  1. Semantic boundary event (block_start / block_stop / tool_use /
//     done / error) — always flush so a crash doesn't lose the
//     logical chunk.
//  2. >= flushThreshold (32) events since last flush — backstop
//     for token-storm streams where time threshold alone would
//     keep buffering forever within 200ms windows.
//  3. >= flushThresholdInterval (200ms) since last flush AND at
//     least one event pending — covers long-quiet streams where
//     events trickle slower than the count threshold but the
//     renderer (or next read) still wants near-realtime UI.
//
// On first Enqueue we INSERT the row with streaming_state='streaming'.
// Finalize sets streaming_state to 'complete' (no error) or 'partial'
// (caller passed a non-nil error / context was cancelled).
//
// Why count threshold + time threshold (vs just one): workmax SSE turns
// can emit hundreds of token-level events per second AND have
// minute-long quiet phases (tool use, model thinking). Pure count
// hammers fsync on bursts; pure time wastes flushes on quiet phases.
// Combined: each trigger covers the other's blind spot.
//
// Pre-P1 (P0.7b vintage) this used count-only at 8 events; replaced
// after the SPIKE_REPORT flagged "long-quiet streams get persisted
// only on the next event" as a noticeable lag in the renderer.
type CacheWriter struct {
	db                    *gorm.DB
	uid                   uint64
	threadID              uint64 // numeric foreign key into w_workagent_thread
	threadUUID            string // retained for logs/debug; schema stores numeric thread_id
	messageUUID           string // server-side uuid we mint
	messageIdempotencyKey string
	userText              string
	chatMode              string
	agentEngine           string
	agentModel            string
	agentMind             string

	mu           sync.Mutex
	row          *cacheRow // nil until first Enqueue triggers INSERT
	pendingText  strings.Builder
	pendingHits  int       // events since last flush
	lastFlushAt  time.Time // wall-clock of last flush; drives time-threshold trigger
	finalized    bool      // Finalize() is idempotent
	forcePartial bool      // terminal busy/error after this attempt emitted content

	// nowFn lets tests substitute deterministic time. Production
	// uses time.Now via the default; tests pass a monotonic counter
	// so the time-threshold trigger fires predictably.
	nowFn func() time.Time
}

// cacheRow mirrors the subset of w_workagent_message columns we touch.
// We deliberately do NOT depend on server/model/workagent.Message —
// that lives MySQL-side with GORM tags we don't need here, and pulling
// it in would drag the cloud handler's transitive deps into the
// desktop build. Raw SQL via *gorm.DB is fine.
type cacheRow struct {
	ID int64 // SQLite-assigned PK; needed for the UPDATE statement
}

// CacheWriterParams is the seed for a new CacheWriter. All fields
// required except UID (uid=0 OK in single-user desktop). Returns an
// error only when params are obviously bad — DB errors surface from
// Enqueue / Finalize so the caller can decide whether to abort the
// whole turn or limp on with proxy-only (no cache).
type CacheWriterParams struct {
	UID                   uint64
	ThreadID              uint64
	ThreadUUID            string
	MessageIdempotencyKey string
	UserText              string
	ChatMode              string // e.g. "ppt"
}

// NewCacheWriter constructs a writer for one SSE turn. The
// w_workagent_message row is created on first Enqueue, not here, so
// that an immediately-failed Chat call doesn't litter the cache with
// empty rows.
func NewCacheWriter(db *gorm.DB, p CacheWriterParams) (*CacheWriter, error) {
	if db == nil {
		return nil, fmt.Errorf("cache writer: db is required")
	}
	if p.ThreadID == 0 {
		return nil, fmt.Errorf("cache writer: thread_id is required")
	}
	uuid, err := generateMessageUUID()
	if err != nil {
		return nil, fmt.Errorf("cache writer: mint uuid: %w", err)
	}
	return &CacheWriter{
		db:                    db,
		uid:                   p.UID,
		threadID:              p.ThreadID,
		threadUUID:            p.ThreadUUID,
		messageUUID:           uuid,
		messageIdempotencyKey: p.MessageIdempotencyKey,
		userText:              p.UserText,
		chatMode:              p.ChatMode,
		nowFn:                 time.Now,
	}, nil
}

// SetProvenance records which engine ran this turn and which model it was
// told to use, for the row this writer will create.
//
// Called before the row exists — turn_meta leads the stream and the row is
// created on first Enqueue — so it only stores, and insertRowLocked writes.
// A turn that never announces itself leaves both empty, which the renderer
// reads as "no claim" rather than as a default worth printing.
func (w *CacheWriter) SetProvenance(engine, model, mind string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.agentEngine = engine
	w.agentModel = model
	w.agentMind = mind
}

// MessageUUID returns the message row's uuid. Useful for the
// downstream SSE response so the renderer can join cache rows to
// in-flight turns.
func (w *CacheWriter) MessageUUID() string { return w.messageUUID }

// Enqueue records one SSE event's text fragment. eventType is used to
// trigger flushes at semantic boundaries; text is appended to the
// row's ai_text column. Caller is expected to pass the *content* of
// the event, not the raw "event: foo\ndata: ...\n\n" framing.
//
// Returns an error if the underlying SQLite write fails. Callers
// should log + carry on — a stale row will be cleaned up by Finalize
// or by the next turn that overwrites it.
func (w *CacheWriter) Enqueue(eventType, textFragment string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.finalized {
		// Late event after Finalize — likely a bug in the caller's
		// pipe loop. Drop silently rather than blow up.
		return nil
	}

	if textFragment != "" {
		w.pendingText.WriteString(textFragment)
	}
	w.pendingHits++

	// Metadata-only frames wait for either real answer text or a successful
	// done. Busy/error terminals set forcePartial and must not manufacture an
	// empty local message; repeated Resume clicks therefore leave the prior
	// stable-turn cache row untouched.
	if w.row == nil && w.pendingText.Len() == 0 && (w.forcePartial || eventType != "done") {
		return nil
	}

	// Lazy-INSERT/reuse on the first event carrying answer text, or on an empty
	// successful done so the user prompt remains visible in local history.
	if w.row == nil {
		if err := w.insertRowLocked(); err != nil {
			return err
		}
	}

	elapsed := w.nowFn().Sub(w.lastFlushAt)
	if shouldFlush(eventType, w.pendingHits, elapsed) {
		return w.flushLocked()
	}
	return nil
}

// Finalize stamps the row as complete (err == nil) or partial (err != nil),
// flushes anything pending, and refuses further writes. Idempotent.
//
// If Enqueue was never called (turn failed before any event reached us),
// Finalize is a no-op — no empty row is left behind.
func (w *CacheWriter) Finalize(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.finalized {
		return nil
	}
	w.finalized = true

	// Nothing to persist: turn died before first event.
	if w.row == nil {
		return nil
	}

	state := streamingStateComplete
	if err != nil || w.forcePartial {
		state = streamingStatePartial
	}

	// Final flush + set streaming_state in one UPDATE.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := w.db.Exec(
		`UPDATE w_workagent_message
		    SET ai_text = ai_text || ?,
		        streaming_state = ?,
		        updated_at = ?
		  WHERE id = ?`,
		w.pendingText.String(), state, now, w.row.ID,
	)
	if res.Error != nil {
		return fmt.Errorf("cache writer: finalize update: %w", res.Error)
	}
	w.pendingText.Reset()
	w.pendingHits = 0
	return nil
}

// MarkPartial records that the current upstream terminal is an error-like
// done frame. It intentionally does not create/reuse a row: a busy-only
// attempt has no local message content. If this attempt already wrote text,
// Finalize will retain that single row as partial.
func (w *CacheWriter) MarkPartial() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.forcePartial = true
}

// insertRowLocked creates the initial row. Caller must hold w.mu.
func (w *CacheWriter) insertRowLocked() error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(w.messageIdempotencyKey) != "" {
		var existing cacheRow
		var existingUUID string
		err := w.db.Raw(`
			SELECT id, uuid
			  FROM w_workagent_message
			 WHERE uid = ? AND thread_id = ? AND message_idempotency_key = ?
			 ORDER BY id DESC
			 LIMIT 1`,
			w.uid, w.threadID, w.messageIdempotencyKey,
		).Row().Scan(&existing.ID, &existingUUID)
		if err == nil {
			if existing.ID <= 0 || strings.TrimSpace(existingUUID) == "" {
				return fmt.Errorf("cache writer: reusable row identity is invalid")
			}
			res := w.db.Exec(`
				UPDATE w_workagent_message
				   SET user_text = ?, ai_text = '', chat_mode = ?,
				       agent_engine = ?, agent_model = ?, agent_mind = ?,
				       streaming_state = ?, updated_at = ?
				 WHERE id = ? AND uid = ? AND thread_id = ? AND message_idempotency_key = ?`,
				w.userText, w.chatMode, w.agentEngine, w.agentModel, w.agentMind, streamingStateActive, now,
				existing.ID, w.uid, w.threadID, w.messageIdempotencyKey,
			)
			if res.Error != nil {
				return fmt.Errorf("cache writer: reset reusable row: %w", res.Error)
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("cache writer: reusable row ownership changed")
			}
			w.row = &existing
			w.messageUUID = existingUUID
			w.lastFlushAt = w.nowFn()
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("cache writer: find reusable row: %w", err)
		}
	}
	var id int64
	err := w.db.Raw(
		`INSERT INTO w_workagent_message
			(uid, uuid, thread_id, user_text, ai_text, chat_mode,
			 agent_engine, agent_model, agent_mind,
			 message_idempotency_key, streaming_state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id`,
		w.uid, w.messageUUID, w.threadID, w.userText, w.chatMode,
		w.agentEngine, w.agentModel, w.agentMind,
		w.messageIdempotencyKey, streamingStateActive, now, now,
	).Row().Scan(&id)
	if err != nil {
		return fmt.Errorf("cache writer: insert returning id: %w", err)
	}
	w.row = &cacheRow{ID: id}
	// Stamp lastFlushAt at INSERT time so the time-threshold trigger
	// doesn't immediately fire on the very first Enqueue call.
	w.lastFlushAt = w.nowFn()
	return nil
}

// flushLocked persists pendingText into the row's ai_text. Caller
// must hold w.mu and the row must already exist.
func (w *CacheWriter) flushLocked() error {
	if w.pendingText.Len() == 0 {
		w.pendingHits = 0
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := w.db.Exec(
		`UPDATE w_workagent_message
		    SET ai_text = ai_text || ?,
		        updated_at = ?
		  WHERE id = ?`,
		w.pendingText.String(), now, w.row.ID,
	)
	if res.Error != nil {
		return fmt.Errorf("cache writer: flush: %w", res.Error)
	}
	w.pendingText.Reset()
	w.pendingHits = 0
	w.lastFlushAt = w.nowFn()
	return nil
}

// Flush policy thresholds. cloud-proxy.md §5.4 specifies 32 events
// OR 200ms; we keep the boundary list as the always-immediate
// override on top.
const (
	flushThreshold         = 32
	flushThresholdInterval = 200 * time.Millisecond
)

// shouldFlush decides whether the pending buffer should be persisted
// now, given the latest event's type, the count of events since last
// flush, and the elapsed wall-clock since last flush.
//
// Three triggers (in order of evaluation):
//  1. Semantic boundary event → always flush
//  2. Hit count exceeded backstop → flush (protects against token-
//     storm streams where pure-time threshold buffers too much)
//  3. Time elapsed exceeded threshold AND at least one hit pending
//     → flush (covers long-quiet streams where count threshold
//     would never fire)
//
// hitsSinceLastFlush=0 + boundary event is the one corner: a `done`
// event with no preceding text fragment still flushes (covers an
// empty-result turn finalizing).
func shouldFlush(eventType string, hitsSinceLastFlush int, elapsed time.Duration) bool {
	switch eventType {
	case "block_start", "block_stop", "tool_use", "done", "error":
		return true
	}
	if hitsSinceLastFlush >= flushThreshold {
		return true
	}
	if hitsSinceLastFlush > 0 && elapsed >= flushThresholdInterval {
		return true
	}
	return false
}

// generateMessageUUID returns a "msg_" + 16-hex-byte string. Not strict
// RFC 4122; we just need uniqueness within the local SQLite. Sync to
// cloud will re-mint server-authoritative uuids when the row replicates.
func generateMessageUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "msg_" + hex.EncodeToString(b[:]), nil
}
