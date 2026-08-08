//go:build desktop

package desktop

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func TestOpenLocalDBSeedsStableHexDeviceID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	first, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB first: %v", err)
	}
	if first.DeviceID == "" {
		t.Fatal("DeviceID is empty")
	}
	if first.IntegrityCheck != "ok" {
		t.Fatalf("IntegrityCheck: got %q, want ok", first.IntegrityCheck)
	}
	if !first.FirstLaunch {
		t.Fatal("first launch should report generated device_id")
	}
	if !isValidDeviceID(first.DeviceID) {
		t.Fatalf("DeviceID should be a 32-character hex string, got %q", first.DeviceID)
	}
	if _, err := hex.DecodeString(first.DeviceID); err != nil {
		t.Fatalf("DeviceID should decode as hex: %v", err)
	}

	second, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB second: %v", err)
	}
	if second.FirstLaunch {
		t.Fatal("second launch should not report first launch")
	}
	if second.DeviceID != first.DeviceID {
		t.Fatalf("DeviceID drifted: first %q second %q", first.DeviceID, second.DeviceID)
	}
}

func TestOpenLocalDBRejectsInvalidStoredDeviceID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	first, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB first: %v", err)
	}
	if err := first.DB.Exec(`UPDATE _local_meta SET value = ? WHERE key = ?`, "device-uuid-test", metaKeyDeviceID).Error; err != nil {
		t.Fatalf("corrupt device_id: %v", err)
	}

	res, err := OpenLocalDB()
	if err == nil {
		t.Fatal("OpenLocalDB should reject invalid stored device_id")
	}
	if res != nil {
		t.Fatalf("OpenLocalDB returned result on failure: %+v", res)
	}
	if !strings.Contains(err.Error(), "stored device_id is invalid") {
		t.Fatalf("error should explain invalid device_id, got: %v", err)
	}
}

func TestOpenLocalDBRejectsFutureMigrationBeforeDeviceIDSeed(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	db, err := gorm.Open(sqlite.Open(filepath.Join(dataDir, "workagent.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE _schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`).Error; err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	if err := db.Exec(`
		INSERT INTO _schema_migrations (version, applied_at)
		VALUES ('9999', '2026-05-20T00:00:00Z')
	`).Error; err != nil {
		t.Fatalf("seed future migration: %v", err)
	}

	res, err := OpenLocalDB()
	if err == nil {
		t.Fatal("OpenLocalDB should reject a future schema version")
	}
	if res != nil {
		t.Fatalf("OpenLocalDB returned result on failure: %+v", res)
	}
	if !strings.Contains(err.Error(), "newer than this sidecar supports") {
		t.Fatalf("error should explain future schema, got: %v", err)
	}

	var localMetaCount int
	if err := db.Raw(`
		SELECT COUNT(*)
		  FROM sqlite_master
		 WHERE type = 'table'
		   AND name = '_local_meta'
	`).Row().Scan(&localMetaCount); err != nil {
		t.Fatalf("scan _local_meta presence: %v", err)
	}
	if localMetaCount != 0 {
		t.Fatal("future schema rejection should happen before _local_meta/device_id setup")
	}
}

func TestOpenLocalDBChecksIntegrityBeforeBootstrapMutation(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	calls := 0
	res, err := openLocalDBWithIntegrityChecker(func(*gorm.DB) (string, error) {
		calls++
		return "", fmt.Errorf("synthetic integrity failure")
	})
	if err == nil {
		t.Fatal("OpenLocalDB should fail when the pre-bootstrap integrity check fails")
	}
	if res != nil {
		t.Fatalf("OpenLocalDB returned result on failure: %+v", res)
	}
	if calls != 1 {
		t.Fatalf("integrity check calls: got %d, want 1", calls)
	}
	if !strings.Contains(err.Error(), "pre-bootstrap sqlite integrity check") {
		t.Fatalf("error should identify pre-bootstrap integrity phase, got: %v", err)
	}

	db, err := gorm.Open(sqlite.Open(filepath.Join(dataDir, "workagent.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open db after failed preflight: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	defer sqlDB.Close()

	var tableCount int
	if err := db.Raw(`
		SELECT COUNT(*)
		  FROM sqlite_master
		 WHERE type = 'table'
		   AND name IN ('_local_meta', 'w_workagent_message')
	`).Row().Scan(&tableCount); err != nil {
		t.Fatalf("scan bootstrap table presence: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("pre-bootstrap integrity failure should not apply migrations or seed local meta, table count=%d", tableCount)
	}
}

func TestOpenLocalDBReportsPostBootstrapIntegrityResult(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	calls := 0
	res, err := openLocalDBWithIntegrityChecker(func(*gorm.DB) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("OpenLocalDB: %v", err)
	}
	if calls != 2 {
		t.Fatalf("integrity check calls: got %d, want 2", calls)
	}
	if res.IntegrityCheck != "ok" {
		t.Fatalf("IntegrityCheck: got %q, want ok", res.IntegrityCheck)
	}
}

func TestOpenLocalDBMarksAbandonedStreamingMessagesPartial(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	first, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB first: %v", err)
	}
	if first.AbandonedStreamingMessages != 0 {
		t.Fatalf("first cleanup count: got %d, want 0", first.AbandonedStreamingMessages)
	}

	if err := first.DB.Exec(`
		INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, updated_at)
		VALUES (7, 'thread-crash-cleanup', 'Crash Cleanup', 'ppt', '2026-05-21T00:00:00Z')
	`).Error; err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	var threadID int64
	if err := first.DB.Raw(`
		SELECT id FROM w_workagent_thread WHERE uuid = 'thread-crash-cleanup'
	`).Row().Scan(&threadID); err != nil {
		t.Fatalf("scan thread id: %v", err)
	}
	for _, seed := range []struct {
		uuid  string
		state string
	}{
		{uuid: "message-complete", state: "complete"},
		{uuid: "message-partial", state: "partial"},
		{uuid: "message-streaming-1", state: "streaming"},
		{uuid: "message-streaming-2", state: "streaming"},
	} {
		if err := first.DB.Exec(`
			INSERT INTO w_workagent_message
				(uid, uuid, thread_id, user_text, ai_text, chat_mode, streaming_state, created_at, updated_at)
			VALUES (7, ?, ?, 'user', 'ai', 'ppt', ?, '2026-05-21T00:00:00Z', '2026-05-21T00:00:00Z')
		`, seed.uuid, threadID, seed.state).Error; err != nil {
			t.Fatalf("seed message %s: %v", seed.uuid, err)
		}
	}
	firstSQLDB, err := first.DB.DB()
	if err != nil {
		t.Fatalf("get first sql db: %v", err)
	}
	if err := firstSQLDB.Close(); err != nil {
		t.Fatalf("close first db: %v", err)
	}

	second, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB second: %v", err)
	}
	if second.AbandonedStreamingMessages != 2 {
		t.Fatalf("cleanup count: got %d, want 2", second.AbandonedStreamingMessages)
	}

	rows, err := second.DB.Raw(`
		SELECT uuid, streaming_state
		  FROM w_workagent_message
		 ORDER BY uuid
	`).Rows()
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var uuid, state string
		if err := rows.Scan(&uuid, &state); err != nil {
			t.Fatalf("scan message: %v", err)
		}
		got[uuid] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate messages: %v", err)
	}
	want := map[string]string{
		"message-complete":    "complete",
		"message-partial":     "partial",
		"message-streaming-1": "partial",
		"message-streaming-2": "partial",
	}
	for uuid, wantState := range want {
		if got[uuid] != wantState {
			t.Fatalf("message %s state: got %q, want %q (all: %#v)", uuid, got[uuid], wantState, got)
		}
	}

	backupDB, err := gorm.Open(sqlite.Open(second.BackupPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open refreshed backup db: %v", err)
	}
	backupSQLDB, err := backupDB.DB()
	if err != nil {
		t.Fatalf("get refreshed backup sql db: %v", err)
	}
	defer backupSQLDB.Close()
	var streamingCount int
	if err := backupDB.Raw(`
		SELECT COUNT(*)
		  FROM w_workagent_message
		 WHERE streaming_state = 'streaming'
	`).Row().Scan(&streamingCount); err != nil {
		t.Fatalf("scan backup streaming count: %v", err)
	}
	if streamingCount != 0 {
		t.Fatalf("refreshed backup should not preserve abandoned streaming rows, got %d", streamingCount)
	}
	backupEntries, err := os.ReadDir(filepath.Dir(second.BackupPath))
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	for _, entry := range backupEntries {
		if strings.HasPrefix(entry.Name(), ".workagent-") && strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("forced backup refresh left temporary file %s", entry.Name())
		}
	}
}

func TestOpenLocalDBMarksAbandonedAgentTurnIntentsInterrupted(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)
	first, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB first: %v", err)
	}
	if first.InterruptedAgentTurnIntents != 0 {
		t.Fatalf("first cleanup count=%d", first.InterruptedAgentTurnIntents)
	}
	if err := first.DB.Exec(`
		INSERT INTO w_workagent_thread (uid, uuid, name, agent_mode, updated_at)
		VALUES (7, 'turn-cleanup-thread', 'Turn Cleanup', 'ppt', '2026-08-06T00:00:00Z')
	`).Error; err != nil {
		t.Fatal(err)
	}
	var threadID uint64
	if err := first.DB.Raw(`SELECT id FROM w_workagent_thread WHERE uuid = 'turn-cleanup-thread'`).Row().Scan(&threadID); err != nil {
		t.Fatal(err)
	}
	seeds := []struct {
		uuid, state, errorKind string
	}{
		{"de305d54-75b4-431b-adb2-eb6b9e546020", agentTurnIntentStarting, ""},
		{"de305d54-75b4-431b-adb2-eb6b9e546021", agentTurnIntentStreaming, ""},
		{"de305d54-75b4-431b-adb2-eb6b9e546022", agentTurnIntentInterrupted, "network"},
		{"de305d54-75b4-431b-adb2-eb6b9e546023", agentTurnIntentCompleted, ""},
		{"de305d54-75b4-431b-adb2-eb6b9e546024", agentTurnIntentCanceled, ""},
	}
	for _, seed := range seeds {
		digest, _ := digestAgentTurnIntent("turn-cleanup-thread", "frozen", "ppt")
		if err := first.DB.Exec(`
			INSERT INTO w_desktop_agent_turn_intent
				(uid, turn_uuid, thread_id, thread_uuid, user_text, chat_mode,
				 request_digest, state, last_error_kind, created_at, updated_at)
			VALUES (7, ?, ?, 'turn-cleanup-thread', 'frozen', 'ppt', ?, ?, ?,
			        '2026-08-06T00:00:00Z', '2026-08-06T00:00:00Z')`,
			seed.uuid, threadID, digest, seed.state, seed.errorKind,
		).Error; err != nil {
			t.Fatalf("seed %s: %v", seed.state, err)
		}
	}
	firstSQL, _ := first.DB.DB()
	if err := firstSQL.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB second: %v", err)
	}
	if second.InterruptedAgentTurnIntents != 2 {
		t.Fatalf("cleanup count=%d, want 2", second.InterruptedAgentTurnIntents)
	}
	rows, err := second.DB.Raw(`
		SELECT turn_uuid, state, last_error_kind
		  FROM w_desktop_agent_turn_intent
		 ORDER BY turn_uuid`).Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type outcome struct{ state, errorKind string }
	got := make(map[string]outcome)
	for rows.Next() {
		var uuid, state, errorKind string
		if err := rows.Scan(&uuid, &state, &errorKind); err != nil {
			t.Fatal(err)
		}
		got[uuid] = outcome{state: state, errorKind: errorKind}
	}
	for _, uuid := range []string{seeds[0].uuid, seeds[1].uuid} {
		if got[uuid] != (outcome{state: agentTurnIntentInterrupted, errorKind: "sidecar_restarted"}) {
			t.Fatalf("abandoned %s outcome=%+v", uuid, got[uuid])
		}
	}
	if got[seeds[2].uuid] != (outcome{state: agentTurnIntentInterrupted, errorKind: "network"}) ||
		got[seeds[3].uuid].state != agentTurnIntentCompleted ||
		got[seeds[4].uuid].state != agentTurnIntentCanceled {
		t.Fatalf("terminal/recoverable rows changed: %#v", got)
	}
}

func TestOpenLocalDBCreatesReadableDailyBackup(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(DataDirEnv, dataDir)

	first, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB first: %v", err)
	}
	if first.BackupPath == "" {
		t.Fatal("BackupPath is empty")
	}
	if _, err := os.Stat(first.BackupPath); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}

	backupDB, err := gorm.Open(sqlite.Open(first.BackupPath), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open backup db: %v", err)
	}
	backupSQLDB, err := backupDB.DB()
	if err != nil {
		t.Fatalf("get backup sql db: %v", err)
	}
	defer backupSQLDB.Close()
	var deviceID string
	if err := backupDB.Raw(`SELECT value FROM _local_meta WHERE key = ?`, metaKeyDeviceID).Row().Scan(&deviceID); err != nil {
		t.Fatalf("backup missing device_id: %v", err)
	}
	if deviceID != first.DeviceID {
		t.Fatalf("backup device_id: got %q, want %q", deviceID, first.DeviceID)
	}

	firstInfo, err := os.Stat(first.BackupPath)
	if err != nil {
		t.Fatalf("stat first backup: %v", err)
	}
	second, err := OpenLocalDB()
	if err != nil {
		t.Fatalf("OpenLocalDB second: %v", err)
	}
	if second.BackupPath != first.BackupPath {
		t.Fatalf("same-day backup path drifted: first %q second %q", first.BackupPath, second.BackupPath)
	}
	secondInfo, err := os.Stat(second.BackupPath)
	if err != nil {
		t.Fatalf("stat second backup: %v", err)
	}
	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) || secondInfo.Size() != firstInfo.Size() {
		t.Fatal("same-day backup should be reused instead of rewritten")
	}
}

func TestPruneLocalDBBackupsKeepsNewestSeven(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}
	for day := 1; day <= 9; day++ {
		path := filepath.Join(backupDir, "workagent-2026050"+string(rune('0'+day))+".db")
		if err := os.WriteFile(path, []byte("backup"), 0o644); err != nil {
			t.Fatalf("write backup %d: %v", day, err)
		}
	}
	if err := os.WriteFile(filepath.Join(backupDir, "notes.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write non-backup: %v", err)
	}

	if err := pruneLocalDBBackups(backupDir, 7); err != nil {
		t.Fatalf("prune backups: %v", err)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatalf("read backup dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	got := strings.Join(names, ",")
	for _, removed := range []string{"workagent-20260501.db", "workagent-20260502.db"} {
		if strings.Contains(got, removed) {
			t.Fatalf("old backup %s should have been pruned; entries=%s", removed, got)
		}
	}
	for _, kept := range []string{"workagent-20260503.db", "workagent-20260509.db", "notes.txt"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("expected %s to remain; entries=%s", kept, got)
		}
	}
}

func TestValidateIntegrityCheckResults(t *testing.T) {
	if got, err := validateIntegrityCheckResults([]string{"ok"}); err != nil || got != "ok" {
		t.Fatalf("ok result: got %q err=%v", got, err)
	}

	if _, err := validateIntegrityCheckResults(nil); err == nil || !strings.Contains(err.Error(), "returned no rows") {
		t.Fatalf("empty result should fail clearly, got %v", err)
	}

	_, err := validateIntegrityCheckResults([]string{
		"row 1 missing from index idx_a",
		"row 2 missing from index idx_a",
		"row 3 missing from index idx_a",
		"row 4 missing from index idx_a",
		"row 5 missing from index idx_a",
		"row 6 missing from index idx_a",
	})
	if err == nil {
		t.Fatal("non-ok integrity result should fail")
	}
	if !strings.Contains(err.Error(), "sqlite integrity_check failed: row 1 missing") {
		t.Fatalf("error should include integrity details, got %v", err)
	}
	if !strings.Contains(err.Error(), "6 total") {
		t.Fatalf("error should include total detail count, got %v", err)
	}
}
