//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/glebarez/sqlite"
	gormlogger "gorm.io/gorm/logger"

	cloudproxy "server/desktop/cloud_proxy"
	localrender "server/desktop/local_render"
	migrationsdesktop "server/desktop/migrations_desktop"
)

// recordingKnowledgeIndex records removals so the tests can assert the
// knowledge index is told about every source the deleted thread owned.
type recordingKnowledgeIndex struct {
	mu           sync.Mutex
	removedFiles []int64
	removedTurns []string
}

func (r *recordingKnowledgeIndex) IndexFile(context.Context, uint64, int64) error { return nil }

func (r *recordingKnowledgeIndex) RemoveFile(_ context.Context, fileID int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removedFiles = append(r.removedFiles, fileID)
	return 1, nil
}

func (r *recordingKnowledgeIndex) RemoveTurn(_ context.Context, turnUUID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removedTurns = append(r.removedTurns, turnUUID)
	return 1, nil
}

const deleteTestThreadUUID = "44444444-4444-4444-8444-444444444444"

// openMigratedTestDB applies the real desktop migrations. The shared
// openServerTestDB builds a trimmed schema without cloud_sync_state, and this
// handler's whole contract turns on that column.
func openMigratedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "delete_test.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// newDeleteFixture builds a signed-out local-route sidecar with a real file
// store rooted in a temp dir — the deletion under test spans SQLite and disk,
// so both have to be real.
func newDeleteFixture(t *testing.T) (base string, db *gorm.DB, filesDir string, index *recordingKnowledgeIndex) {
	t.Helper()
	db = openMigratedTestDB(t)
	filesDir = t.TempDir()
	index = &recordingKnowledgeIndex{}
	modelSettings := ensureLocalModelSettingsDB(t, db)

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		DeviceID:       "dev",
		TokenStore:     cloudproxy.NewTokenStore(newMemKeychain()),
		ModelSettings:  modelSettings,
		LocalInference: &fakeLocalRunner{},
		LocalFiles:     localrender.NewStore(db, filesDir),
		KnowledgeIndex: index,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), db, filesDir, index
}

func seedLocalThreadForDelete(t *testing.T, db *gorm.DB, uid uint64, uuid string) uint64 {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Doomed', 'local')`,
		uid, uuid,
	).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	var id uint64
	if err := db.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = ?`, uuid).Row().Scan(&id); err != nil {
		t.Fatalf("seed thread id: %v", err)
	}
	return id
}

func doDelete(t *testing.T, base, uuid string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, base+"/agent/threads/"+uuid, nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func countRows(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Raw(query, args...).Row().Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// The core promise: deleting a local thread removes everything that thread
// put on this machine — rows, bytes, and the knowledge derived from them.
func TestDeleteThread_RemovesEverythingTheThreadOwned(t *testing.T) {
	base, db, filesDir, index := newDeleteFixture(t)
	uid := localSingleUserUID
	threadID := seedLocalThreadForDelete(t, db, uid, deleteTestThreadUUID)

	// A conversation, an upload, and the turn that produced the conversation.
	if err := db.Exec(
		`INSERT INTO w_workagent_message (uid, uuid, thread_id, user_text, ai_text) VALUES (?, 'm1', ?, 'q', 'a'), (?, 'm2', ?, 'q2', 'a2')`,
		uid, threadID, uid, threadID,
	).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_desktop_agent_turn_intent
		 (uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode, request_digest, state, created_at, updated_at)
		 VALUES (?, 'turn-del-1', ?, ?, 'q', 'ppt', 'd', 'completed', ?, ?)`,
		uid, threadID, deleteTestThreadUUID, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}
	files := localrender.NewStore(db, filesDir)
	saved, err := files.SaveThreadFile(uid, threadID, deleteTestThreadUUID, "notes.txt", strings.NewReader("some text"))
	if err != nil {
		t.Fatalf("save file: %v", err)
	}
	onDisk := filepath.Join(filesDir, deleteTestThreadUUID, "notes.txt")
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("precondition: file must exist on disk: %v", err)
	}

	resp := doDelete(t, base, deleteTestThreadUUID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body deleteAgentThreadResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Deleted || body.Messages != 2 || body.Files != 1 || body.TurnIntents != 1 {
		t.Errorf("response undercounts what was removed: %+v", body)
	}

	for name, q := range map[string]string{
		"thread":  `SELECT COUNT(*) FROM w_workagent_thread WHERE uuid = ?`,
		"intents": `SELECT COUNT(*) FROM w_desktop_agent_turn_intent WHERE thread_uuid = ?`,
	} {
		if n := countRows(t, db, q, deleteTestThreadUUID); n != 0 {
			t.Errorf("%s rows remaining: %d", name, n)
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM w_workagent_message WHERE thread_id = ?`, threadID); n != 0 {
		t.Errorf("message rows remaining: %d", n)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM w_workagent_thread_file WHERE thread_id = ?`, threadID); n != 0 {
		t.Errorf("file rows remaining: %d", n)
	}
	if _, err := os.Stat(onDisk); !os.IsNotExist(err) {
		t.Errorf("file bytes remaining on disk: %v", err)
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	if len(index.removedFiles) != 1 || index.removedFiles[0] != saved.FileID {
		t.Errorf("knowledge index not told about file %d: %v", saved.FileID, index.removedFiles)
	}
	if len(index.removedTurns) != 1 || index.removedTurns[0] != "turn-del-1" {
		t.Errorf("knowledge index not told about the turn: %v", index.removedTurns)
	}
}

// A synced thread has a cloud copy and a sync worker that would pull it back;
// deleting it here would be a delete that undoes itself.
func TestDeleteThread_RefusesSyncedThreads(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Synced', 'synced')`,
		localSingleUserUID, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp := doDelete(t, base, deleteTestThreadUUID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM w_workagent_thread WHERE uuid = ?`, deleteTestThreadUUID); n != 1 {
		t.Errorf("the refused thread must survive, rows = %d", n)
	}
}

// Deleting under a running turn would let the turn's cache writer resurrect
// the thread as a half-deleted ghost.
func TestDeleteThread_RefusesWhileATurnIsRunning(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	uid := localSingleUserUID
	threadID := seedLocalThreadForDelete(t, db, uid, deleteTestThreadUUID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := db.Exec(
		`INSERT INTO w_desktop_agent_turn_intent
		 (uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode, request_digest, state, created_at, updated_at)
		 VALUES (?, 'turn-busy', ?, ?, 'q', 'ppt', 'd', 'streaming', ?, ?)`,
		uid, threadID, deleteTestThreadUUID, now, now,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp := doDelete(t, base, deleteTestThreadUUID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var payload map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&payload)
	if payload["error"] != "thread_busy" {
		t.Errorf("error = %q, want thread_busy", payload["error"])
	}
}

// The uid filter is the ownership check: another identity's thread is not
// found, not forbidden — this route does not confirm what exists for others.
func TestDeleteThread_ScopedToTheCaller(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	otherUID := localSingleUserUID + 1
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		otherUID, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp := doDelete(t, base, deleteTestThreadUUID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM w_workagent_thread WHERE uuid = ?`, deleteTestThreadUUID); n != 1 {
		t.Errorf("the other identity's thread must survive, rows = %d", n)
	}
}
