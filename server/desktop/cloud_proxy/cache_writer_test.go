//go:build desktop

package cloud_proxy

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openCacheTestDB stands up an isolated SQLite file with the
// w_workagent_message table. We only create the message table since
// the cache writer doesn't touch any others; copying the full
// migration would couple this test to migration internals.
func openCacheTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cache_writer_test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sqlite pool: %v", err)
	}
	// A single pooled connection deliberately maximizes the old unsafe
	// INSERT/last_insert_rowid interleaving while avoiding SQLITE_BUSY noise.
	sqlDB.SetMaxOpenConns(1)
	// Minimal subset of the w_workagent_message schema. Matches
	// migrations_desktop/0001_init_workagent_tables.sql but trimmed
	// to the columns the writer touches.
	exec := func(stmt string) {
		t.Helper()
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	exec(`CREATE TABLE w_workagent_message (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		uid INTEGER NOT NULL DEFAULT 0,
		uuid TEXT NOT NULL,
		thread_id INTEGER NOT NULL DEFAULT 0,
		user_text TEXT,
		ai_text TEXT,
		chat_mode TEXT NOT NULL DEFAULT '',
		message_idempotency_key TEXT,
		agent_engine TEXT NOT NULL DEFAULT '',
		agent_model TEXT NOT NULL DEFAULT '',
		agent_mind TEXT NOT NULL DEFAULT '',
		streaming_state TEXT NOT NULL DEFAULT 'complete',
		created_at TEXT,
		updated_at TEXT
	)`)
	return db
}

func readRow(t *testing.T, db *gorm.DB, uuid string) (state, aiText, userText, mode string) {
	t.Helper()
	row := db.Raw(`SELECT streaming_state, ai_text, user_text, chat_mode
	                 FROM w_workagent_message WHERE uuid = ?`, uuid).Row()
	if err := row.Scan(&state, &aiText, &userText, &mode); err != nil {
		t.Fatalf("scan row uuid=%s: %v", uuid, err)
	}
	return
}

func TestCacheWriter_HappyPath(t *testing.T) {
	db := openCacheTestDB(t)
	w, err := NewCacheWriter(db, CacheWriterParams{
		UID:                   42,
		ThreadID:              7,
		ThreadUUID:            "thr_abc",
		MessageIdempotencyKey: "desktop-turn:" + proxyTestTurnUUID,
		UserText:              "做个 ppt",
		ChatMode:              "ppt",
	})
	if err != nil {
		t.Fatalf("NewCacheWriter: %v", err)
	}
	// First event triggers INSERT.
	if err := w.Enqueue("text", "Hello "); err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	if err := w.Enqueue("text", "world"); err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}
	// done event forces flush + sets state via Finalize.
	if err := w.Enqueue("done", ""); err != nil {
		t.Fatalf("Enqueue done: %v", err)
	}
	if err := w.Finalize(nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	state, ai, user, mode := readRow(t, db, w.MessageUUID())
	if state != streamingStateComplete {
		t.Errorf("state: got %q, want %q", state, streamingStateComplete)
	}
	if ai != "Hello world" {
		t.Errorf("ai_text: got %q, want %q", ai, "Hello world")
	}
	if user != "做个 ppt" {
		t.Errorf("user_text: got %q", user)
	}
	if mode != "ppt" {
		t.Errorf("chat_mode: got %q", mode)
	}
	var requestID string
	if err := db.Raw(`SELECT message_idempotency_key FROM w_workagent_message WHERE uuid = ?`, w.MessageUUID()).Row().Scan(&requestID); err != nil {
		t.Fatalf("read idempotency key: %v", err)
	}
	if requestID != "desktop-turn:"+proxyTestTurnUUID {
		t.Fatalf("message idempotency key=%q", requestID)
	}
}

func TestCacheWriter_ConcurrentRowsRetainWriterOwnership(t *testing.T) {
	db := openCacheTestDB(t)
	const writers = 32
	type expectedRow struct {
		uuid, userText, aiText, requestID string
	}
	expected := make([]expectedRow, writers)
	instances := make([]*CacheWriter, writers)
	for index := 0; index < writers; index++ {
		userText := fmt.Sprintf("user-%02d", index)
		requestID := fmt.Sprintf("desktop-turn:00000000-0000-4000-8000-%012d", index)
		writer, err := NewCacheWriter(db, CacheWriterParams{
			UID:                   uint64(index + 1),
			ThreadID:              uint64(index + 1),
			ThreadUUID:            fmt.Sprintf("thread-%02d", index),
			MessageIdempotencyKey: requestID,
			UserText:              userText,
			ChatMode:              "ppt",
		})
		if err != nil {
			t.Fatalf("new writer %d: %v", index, err)
		}
		instances[index] = writer
		expected[index] = expectedRow{
			uuid: writer.MessageUUID(), userText: userText,
			aiText: fmt.Sprintf("answer-%02d", index), requestID: requestID,
		}
	}

	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var group sync.WaitGroup
	for index, writer := range instances {
		index, writer := index, writer
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if err := writer.Enqueue("text", expected[index].aiText); err != nil {
				errorsByWriter <- fmt.Errorf("writer %d enqueue: %w", index, err)
				return
			}
			if err := writer.Finalize(nil); err != nil {
				errorsByWriter <- fmt.Errorf("writer %d finalize: %w", index, err)
			}
		}()
	}
	close(start)
	group.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		t.Error(err)
	}

	for _, want := range expected {
		var userText, aiText, requestID string
		if err := db.Raw(`SELECT user_text, ai_text, message_idempotency_key FROM w_workagent_message WHERE uuid = ?`, want.uuid).
			Row().Scan(&userText, &aiText, &requestID); err != nil {
			t.Fatalf("load %s: %v", want.uuid, err)
		}
		if userText != want.userText || aiText != want.aiText || requestID != want.requestID {
			t.Errorf("row %s ownership drift: got (%q,%q,%q), want (%q,%q,%q)",
				want.uuid, userText, aiText, requestID, want.userText, want.aiText, want.requestID)
		}
	}
}

func TestCacheWriter_StableKeyReusesOneLocalRow(t *testing.T) {
	db := openCacheTestDB(t)
	params := CacheWriterParams{
		UID: 42, ThreadID: 7, ThreadUUID: "thr-stable",
		MessageIdempotencyKey: "desktop-turn:" + proxyTestTurnUUID,
		UserText:              "frozen prompt", ChatMode: "ppt",
	}
	first, err := NewCacheWriter(db, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Enqueue("text", "first answer"); err != nil {
		t.Fatal(err)
	}
	if err := first.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	firstUUID := first.MessageUUID()

	second, err := NewCacheWriter(db, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Enqueue("done", "replayed answer"); err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	if second.MessageUUID() != firstUUID {
		t.Fatalf("stable-key retry changed message uuid: first=%s second=%s", firstUUID, second.MessageUUID())
	}
	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message WHERE message_idempotency_key = ?`, params.MessageIdempotencyKey).Row().Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stable-key row count=%d, want 1", count)
	}
	state, aiText, userText, mode := readRow(t, db, firstUUID)
	if state != streamingStateComplete || aiText != "replayed answer" || userText != params.UserText || mode != params.ChatMode {
		t.Fatalf("reused row=(%q,%q,%q,%q)", state, aiText, userText, mode)
	}
}

func TestCacheWriter_EmptyPartialTerminalCreatesNoRow(t *testing.T) {
	db := openCacheTestDB(t)
	writer, err := NewCacheWriter(db, CacheWriterParams{
		UID: 42, ThreadID: 7, MessageIdempotencyKey: "desktop-turn:" + proxyTestTurnUUID,
		UserText: "busy", ChatMode: "ppt",
	})
	if err != nil {
		t.Fatal(err)
	}
	writer.MarkPartial()
	if err := writer.Enqueue("done", ""); err != nil {
		t.Fatal(err)
	}
	if err := writer.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.Raw(`SELECT COUNT(*) FROM w_workagent_message`).Row().Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("empty partial terminal created %d row(s)", count)
	}
}

func TestCacheWriter_NoEventsNoRow(t *testing.T) {
	db := openCacheTestDB(t)
	w, err := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := w.Finalize(nil); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Verify no row was inserted.
	var count int64
	db.Raw(`SELECT count(*) FROM w_workagent_message`).Row().Scan(&count)
	if count != 0 {
		t.Errorf("want 0 rows, got %d", count)
	}
}

func TestCacheWriter_PartialOnError(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	_ = w.Enqueue("text", "abc")
	_ = w.Enqueue("text", "def")
	if err := w.Finalize(errors.New("upstream EOF")); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	state, ai, _, _ := readRow(t, db, w.MessageUUID())
	if state != streamingStatePartial {
		t.Errorf("state: got %q, want %q", state, streamingStatePartial)
	}
	if ai != "abcdef" {
		t.Errorf("ai_text: got %q, want %q", ai, "abcdef")
	}
}

func TestCacheWriter_FinalizeIdempotent(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	_ = w.Enqueue("text", "x")
	if err := w.Finalize(nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(nil); err != nil {
		t.Errorf("second finalize: %v", err)
	}
	if err := w.Finalize(errors.New("late")); err != nil {
		t.Errorf("third finalize: %v", err)
	}
	state, _, _, _ := readRow(t, db, w.MessageUUID())
	if state != streamingStateComplete {
		t.Errorf("state should reflect first Finalize: got %q", state)
	}
}

func TestCacheWriter_FlushOnBoundary(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	// One text event (no flush) then a block_start (forces flush).
	if err := w.Enqueue("text", "first "); err != nil {
		t.Fatal(err)
	}
	if err := w.Enqueue("block_start", "second"); err != nil {
		t.Fatal(err)
	}
	// Before Finalize, ai_text should already contain both pieces.
	state, ai, _, _ := readRow(t, db, w.MessageUUID())
	if state != streamingStateActive {
		t.Errorf("state pre-finalize: got %q, want %q", state, streamingStateActive)
	}
	if ai != "first second" {
		t.Errorf("ai_text after boundary flush: got %q", ai)
	}
	_ = w.Finalize(nil)
}

func TestCacheWriter_FlushEveryNEvents(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	// Pin a frozen clock so the time-threshold trigger doesn't fire
	// underneath us; we want the count threshold to be the only path.
	w.nowFn = stubClockAt(time.Now())

	// Push (flushThreshold - 1) text events — under threshold, no flush.
	for i := 0; i < flushThreshold-1; i++ {
		if err := w.Enqueue("text", "x"); err != nil {
			t.Fatal(err)
		}
	}
	_, aiBefore, _, _ := readRow(t, db, w.MessageUUID())
	if aiBefore != "" {
		t.Errorf("expected no flush before threshold, ai=%q", aiBefore)
	}
	// Threshold-th event triggers flush.
	if err := w.Enqueue("text", "x"); err != nil {
		t.Fatal(err)
	}
	_, aiAfter, _, _ := readRow(t, db, w.MessageUUID())
	if len(aiAfter) != flushThreshold {
		t.Errorf("expected %d chars after threshold flush, got %d (%q)", flushThreshold, len(aiAfter), aiAfter)
	}
	_ = w.Finalize(nil)
}

// stubClockAt returns a nowFn that always reports the given fixed
// time. Useful when a test wants ONLY the count threshold to drive
// flushes (no time-threshold races).
func stubClockAt(fixed time.Time) func() time.Time {
	return func() time.Time { return fixed }
}

// stubClockAdvancing returns a nowFn that starts at `start` and
// advances by `step` on every call. Lets tests drive the time-
// threshold trigger deterministically.
func stubClockAdvancing(start time.Time, step time.Duration) func() time.Time {
	cur := start
	return func() time.Time {
		out := cur
		cur = cur.Add(step)
		return out
	}
}

// TestCacheWriter_TimeThresholdFlushesBeforeCount: a stream with
// events arriving slowly (one per 250ms simulated) flushes within
// 200ms of the previous flush, not after 32 events as the count
// threshold would demand.
func TestCacheWriter_TimeThresholdFlushesBeforeCount(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	// Each Enqueue call advances the clock by 250ms — above the
	// 200ms threshold so each event after the first should flush.
	// Clock advances on every nowFn call:
	//   - insertRowLocked: t=0
	//   - 1st Enqueue elapsed-check: t=250ms → flush
	//   - flushLocked stamp: t=500ms (new baseline)
	//   - 2nd Enqueue elapsed-check: t=750ms (vs 500ms) → 250ms ≥ 200ms → flush
	//   - and so on
	w.nowFn = stubClockAdvancing(time.Now(), 250*time.Millisecond)

	for i := 0; i < 5; i++ {
		if err := w.Enqueue("text", "x"); err != nil {
			t.Fatal(err)
		}
	}
	// After 5 enqueues, all 5 chars should be flushed (each event
	// triggered the time threshold).
	_, aiAfter, _, _ := readRow(t, db, w.MessageUUID())
	if len(aiAfter) != 5 {
		t.Errorf("time-threshold flush: expected 5 chars persisted, got %d (%q)", len(aiAfter), aiAfter)
	}
	_ = w.Finalize(nil)
}

// TestCacheWriter_TimeThresholdDoesNotFireWithoutHits: an event-less
// elapsed window does NOT cause spurious flushes. (Important because
// flushLocked itself calls nowFn, so a poorly-implemented stub could
// look like time advanced + 0 events and try to flush an empty buffer.)
func TestCacheWriter_TimeThresholdDoesNotFireWithoutHits(t *testing.T) {
	// shouldFlush(..., hits=0, elapsed=1s) must return false.
	if shouldFlush("text", 0, time.Second) {
		t.Error("shouldFlush with 0 hits + elapsed > threshold should not fire")
	}
}

// TestCacheWriter_BoundaryEventStillFlushesAlone: a `done` event
// with zero pending hits AND zero elapsed time should still flush —
// the boundary trigger is independent.
func TestCacheWriter_BoundaryEventStillFlushesAlone(t *testing.T) {
	if !shouldFlush("done", 0, 0) {
		t.Error("boundary event should always flush regardless of count/time")
	}
	if !shouldFlush("block_start", 0, 0) {
		t.Error("block_start should always flush regardless of count/time")
	}
}

func TestCacheWriter_EnqueueAfterFinalizeIsNoop(t *testing.T) {
	db := openCacheTestDB(t)
	w, _ := NewCacheWriter(db, CacheWriterParams{ThreadID: 1, UserText: "hi"})
	_ = w.Enqueue("text", "a")
	_ = w.Finalize(nil)
	if err := w.Enqueue("text", "b"); err != nil {
		t.Errorf("late enqueue should be silent no-op, got %v", err)
	}
	_, ai, _, _ := readRow(t, db, w.MessageUUID())
	if ai != "a" {
		t.Errorf("late event leaked into row: %q", ai)
	}
}

func TestCacheWriter_RejectsBadParams(t *testing.T) {
	if _, err := NewCacheWriter(nil, CacheWriterParams{ThreadID: 1}); err == nil {
		t.Error("nil db should error")
	}
	db := openCacheTestDB(t)
	if _, err := NewCacheWriter(db, CacheWriterParams{}); err == nil {
		t.Error("zero thread_id should error")
	}
}

func TestShouldFlush(t *testing.T) {
	cases := []struct {
		name    string
		event   string
		hits    int
		elapsed time.Duration
		want    bool
	}{
		// Boundary events: always flush, regardless of count/time.
		{"boundary block_start", "block_start", 1, 0, true},
		{"boundary block_stop", "block_stop", 1, 0, true},
		{"boundary tool_use", "tool_use", 1, 0, true},
		{"boundary done", "done", 1, 0, true},
		{"boundary error", "error", 1, 0, true},
		// Empty buffer + boundary still flushes (covers empty-result turn).
		{"boundary done with zero hits", "done", 0, 0, true},

		// Count threshold: at flushThreshold (32), flush; under, don't.
		{"under count threshold", "text", flushThreshold - 1, 0, false},
		{"at count threshold", "text", flushThreshold, 0, true},
		{"over count threshold", "text", flushThreshold + 5, 0, true},

		// Time threshold: at flushThresholdInterval (200ms) with hits, flush.
		{"text under time threshold", "text", 1, flushThresholdInterval / 2, false},
		{"text at time threshold", "text", 1, flushThresholdInterval, true},
		{"text over time threshold", "text", 1, flushThresholdInterval * 2, true},

		// Time elapsed but no hits → no flush (don't flush empty buffer).
		{"no hits, long elapsed", "text", 0, time.Second, false},

		// Plain text events with low count + short elapsed → no flush.
		{"plain text small hits short elapsed", "text", 1, 10 * time.Millisecond, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFlush(tc.event, tc.hits, tc.elapsed); got != tc.want {
				t.Errorf("shouldFlush(%q, %d, %s) = %v, want %v",
					tc.event, tc.hits, tc.elapsed, got, tc.want)
			}
		})
	}
}

// SetProvenance must persist even when the row already exists — the guard
// that turns the "turn_meta arrives before text" convention into something
// that holds if the convention ever breaks. Without the UPDATE path, a
// runtime that emitted text first would leave provenance in memory and the
// row's columns empty forever.
func TestCacheWriter_SetProvenanceAfterRowExists(t *testing.T) {
	db := openCacheTestDB(t)
	w, err := NewCacheWriter(db, CacheWriterParams{
		UID: 1, ThreadID: 1, ThreadUUID: "thr", MessageIdempotencyKey: "desktop-turn:late-prov",
		UserText: "hi", ChatMode: "general",
	})
	if err != nil {
		t.Fatalf("NewCacheWriter: %v", err)
	}
	// Text first — row is created with empty provenance.
	if err := w.Enqueue("text", "answer"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	// Provenance arrives late. The UPDATE guard must persist it.
	w.SetProvenance("local", "specialist-model", "Payroll mind")
	if err := w.Enqueue("done", ""); err != nil {
		t.Fatalf("Enqueue done: %v", err)
	}
	if err := w.Finalize(nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	var engine, model, mind string
	if err := db.Raw(
		`SELECT agent_engine, agent_model, agent_mind FROM w_workagent_message WHERE uuid = ?`,
		w.MessageUUID(),
	).Row().Scan(&engine, &model, &mind); err != nil {
		t.Fatalf("read provenance: %v", err)
	}
	if engine != "local" || model != "specialist-model" || mind != "Payroll mind" {
		t.Errorf("late provenance = engine=%q model=%q mind=%q; the UPDATE guard did not fire", engine, model, mind)
	}
}
