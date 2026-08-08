//go:build desktop

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
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
	// The full DataDir layout, so routes that derive paths from it (the
	// workspace listing) see the same shape production does.
	dataDir := t.TempDir()
	filesDir = filepath.Join(dataDir, "thread_files")
	index = &recordingKnowledgeIndex{}
	modelSettings := ensureLocalModelSettingsDB(t, db)

	srv, err := NewServer(ServerConfig{
		SidecarVersion: "test",
		LocalToken:     "tok",
		DB:             db,
		DeviceID:       "dev",
		DataDir:        dataDir,
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

// --- Rename ----------------------------------------------------------------
// Shares the delete fixture: same identity resolution, same local-only scope,
// same migrated schema.

func doRename(t *testing.T, base, uuid, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPatch, base+"/agent/threads/"+uuid, strings.NewReader(body))
	req.Header.Set("X-Local-Token", "tok")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRenameThread_UpdatesNameAndBumpsUpdatedAt(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)
	// Age the row so the updated_at bump is observable.
	if err := db.Exec(
		`UPDATE w_workagent_thread SET updated_at = '2026-01-01T00:00:00Z' WHERE uuid = ?`,
		deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp := doRename(t, base, deleteTestThreadUUID, `{"name":"Q3 板书"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body renameAgentThreadResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Renamed || body.Thread.Name != "Q3 板书" {
		t.Errorf("response = %+v", body)
	}

	var name, updated string
	if err := db.Raw(`SELECT name, updated_at FROM w_workagent_thread WHERE uuid = ?`, deleteTestThreadUUID).
		Row().Scan(&name, &updated); err != nil {
		t.Fatal(err)
	}
	if name != "Q3 板书" {
		t.Errorf("stored name = %q", name)
	}
	if strings.HasPrefix(updated, "2026-01-01") {
		t.Error("updated_at did not move; the list orders by it and the renamed thread would not surface")
	}
}

// The same honesty rule as deletion: a synced thread's name belongs to the
// cloud copy, and the sync worker would overwrite a local rename.
func TestRenameThread_RefusesSyncedThreads(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Cloud name', 'synced')`,
		localSingleUserUID, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	resp := doRename(t, base, deleteTestThreadUUID, `{"name":"local edit"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var name string
	_ = db.Raw(`SELECT name FROM w_workagent_thread WHERE uuid = ?`, deleteTestThreadUUID).Row().Scan(&name)
	if name != "Cloud name" {
		t.Errorf("name changed to %q despite the refusal", name)
	}
}

func TestRenameThread_RejectsMalformedNames(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)

	for label, body := range map[string]string{
		"empty":         `{"name":"   "}`,
		"too long":      `{"name":"` + strings.Repeat("x", 201) + `"}`,
		"unknown field": `{"name":"ok","uid":1}`,
		"not json":      `name=ok`,
	} {
		resp := doRename(t, base, deleteTestThreadUUID, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", label, resp.StatusCode)
		}
	}
	var name string
	_ = db.Raw(`SELECT name FROM w_workagent_thread WHERE uuid = ?`, deleteTestThreadUUID).Row().Scan(&name)
	if name != "Doomed" {
		t.Errorf("a refused rename must leave the name alone, got %q", name)
	}
}

func TestRenameThread_ScopedToTheCaller(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		localSingleUserUID+1, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	resp := doRename(t, base, deleteTestThreadUUID, `{"name":"mine now"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// --- Workspace listing -----------------------------------------------------

func TestListWorkspaceFiles_ListsOwnedThreadNewestFirst(t *testing.T) {
	base, db, fixtureFilesDir, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)

	ws := filepath.Join(filepath.Dir(fixtureFilesDir), "agent_workspace", "thread_"+deleteTestThreadUUID)
	if err := os.MkdirAll(filepath.Join(ws, "deck"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "notes.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "deck", "outline.md"), []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body workspaceListResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 2 || body.Truncated {
		t.Fatalf("body = %+v", body)
	}
	paths := []string{body.Items[0].Path, body.Items[1].Path}
	found := strings.Join(paths, ",")
	if !strings.Contains(found, "notes.txt") || !strings.Contains(found, "deck/outline.md") {
		t.Errorf("paths = %v; want relative slash paths for both files", paths)
	}
}

// The workspace directory is keyed by uuid alone; the thread row is the
// ownership authority. Another identity's uuid must 404 without revealing
// whether a workspace exists.
func TestListWorkspaceFiles_ScopedToTheCaller(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		localSingleUserUID+1, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// A thread that never ran a tool turn has no workspace directory; that is an
// empty listing, not an error.
func TestListWorkspaceFiles_MissingDirIsEmpty(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)

	req, _ := http.NewRequest(http.MethodGet, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var body workspaceListResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Count != 0 || len(body.Items) != 0 {
		t.Errorf("body = %+v, want empty", body)
	}
}

// listWorkspaceFiles is bounded and does not follow symlinks out.
func TestListWorkspaceFiles_BoundsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxWorkspaceListEntries+20; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	items, truncated := listWorkspaceFiles(root)
	if !truncated {
		t.Error("the cap must report truncation")
	}
	if len(items) > maxWorkspaceListEntries {
		t.Errorf("listed %d entries, cap is %d", len(items), maxWorkspaceListEntries)
	}
	for _, it := range items {
		if strings.Contains(it.Path, "secret") {
			t.Errorf("the walk followed a symlink out of the workspace: %s", it.Path)
		}
	}
}

// --- Workspace reveal ------------------------------------------------------

func TestRevealWorkspace_OpensExactlyTheOwnedDir(t *testing.T) {
	base, db, fixtureFilesDir, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)
	ws := filepath.Join(filepath.Dir(fixtureFilesDir), "agent_workspace", "thread_"+deleteTestThreadUUID)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	var opened []string
	restore := revealWorkspaceDir
	revealWorkspaceDir = func(dir string) error { opened = append(opened, dir); return nil }
	t.Cleanup(func() { revealWorkspaceDir = restore })

	req, _ := http.NewRequest(http.MethodPost, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace/reveal", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if len(opened) != 1 || opened[0] != ws {
		t.Fatalf("opened = %v, want exactly %s", opened, ws)
	}
}

// No workspace directory → 404, and crucially: the OS opener must not run.
// Opening a nonexistent path is harmless; the rule matters so a future edit
// cannot loosen the path derivation without a test noticing.
func TestRevealWorkspace_RefusesWhenNothingExists(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	seedLocalThreadForDelete(t, db, localSingleUserUID, deleteTestThreadUUID)

	var opened []string
	restore := revealWorkspaceDir
	revealWorkspaceDir = func(dir string) error { opened = append(opened, dir); return nil }
	t.Cleanup(func() { revealWorkspaceDir = restore })

	req, _ := http.NewRequest(http.MethodPost, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace/reveal", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if len(opened) != 0 {
		t.Fatalf("the opener ran for a missing workspace: %v", opened)
	}
}

func TestRevealWorkspace_ScopedToTheCaller(t *testing.T) {
	base, db, fixtureFilesDir, _ := newDeleteFixture(t)
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		localSingleUserUID+1, deleteTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	// The directory exists — ownership alone must refuse.
	ws := filepath.Join(filepath.Dir(fixtureFilesDir), "agent_workspace", "thread_"+deleteTestThreadUUID)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	var opened []string
	restore := revealWorkspaceDir
	revealWorkspaceDir = func(dir string) error { opened = append(opened, dir); return nil }
	t.Cleanup(func() { revealWorkspaceDir = restore })

	req, _ := http.NewRequest(http.MethodPost, base+"/agent/threads/"+deleteTestThreadUUID+"/workspace/reveal", nil)
	req.Header.Set("X-Local-Token", "tok")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || len(opened) != 0 {
		t.Fatalf("status = %d opened = %v; another identity's workspace must be invisible", resp.StatusCode, opened)
	}
}
