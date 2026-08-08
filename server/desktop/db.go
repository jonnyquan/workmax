//go:build desktop

// Package desktop bootstraps state for the workagent-desktop sidecar.
//
// This file owns the local SQLite database lifecycle:
//   - resolve the data directory (~/.workmax/ or $WORKMAX_DESKTOP_DATA_DIR)
//   - open SQLite at <data-dir>/workagent.db
//   - apply pending migrations from server/desktop/migrations_desktop/
//   - seed _local_meta with device_id on first launch (Q4 decision)
//
// Desktop keeps this self-contained, separate from server/initialize/
// (the cloud server's heavy init flow), so the sidecar can boot quickly
// without cloud-server globals.
package desktop

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	migrationsdesktop "server/desktop/migrations_desktop"
)

// DataDirEnv overrides the default ~/.workmax data directory. Used in tests
// and CI to keep runs hermetic.
const DataDirEnv = "WORKMAX_DESKTOP_DATA_DIR"

// ResolveDataDir returns the path the sidecar should treat as its
// per-user data root. Creates the directory if missing.
func ResolveDataDir() (string, error) {
	if override := os.Getenv(DataDirEnv); override != "" {
		if err := os.MkdirAll(override, 0o755); err != nil {
			return "", fmt.Errorf("create data dir %s: %w", override, err)
		}
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	dir := filepath.Join(home, ".workmax")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create data dir %s: %w", dir, err)
	}
	return dir, nil
}

// OpenResult bundles what the sidecar typically needs after DB bootstrap.
type OpenResult struct {
	DB                          *gorm.DB
	DBPath                      string
	DataDir                     string
	DeviceID                    string
	NewMigrations               []string // versions applied this boot ("0001", "0002", ...)
	FirstLaunch                 bool     // true on first run for this data dir
	AbandonedStreamingMessages  int64    // rows changed from streaming -> partial during boot
	InterruptedAgentTurnIntents int64    // legacy starting/streaming intents made explicitly recoverable
	BackupPath                  string   // daily backup path created or reused for this boot
	IntegrityCheck              string   // SQLite PRAGMA integrity_check result ("ok" on success)
}

// OpenLocalDB opens (or creates) the local SQLite database, applies all
// pending migrations, and seeds device_id if missing. Returns enough
// context for the caller to log a useful boot summary.
func OpenLocalDB() (*OpenResult, error) {
	return openLocalDBWithIntegrityChecker(checkLocalDBIntegrity)
}

func openLocalDBWithIntegrityChecker(integrityChecker func(*gorm.DB) (string, error)) (*OpenResult, error) {
	dataDir, err := ResolveDataDir()
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dataDir, "workagent.db")

	// WAL + PRAGMAs go through the connection URL so glebarez/sqlite
	// applies them before any user query touches the file.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// PrepareStmt off: SQLite is local + low concurrency; prepared
		// statement caching adds complexity without measurable gain.
		//
		// Custom logger pinned to stderr (NOT stdout): the sidecar
		// reserves stdout for the single-line handshake JSON that
		// Electron parses on startup. Any GORM noise on stdout would
		// break that contract.
		Logger: newSilentishLogger(),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite at %s: %w", dbPath, err)
	}

	if _, err := integrityChecker(db); err != nil {
		return nil, fmt.Errorf("pre-bootstrap sqlite integrity check: %w", err)
	}

	newlyApplied, err := migrationsdesktop.Apply(db)
	if err != nil {
		return nil, fmt.Errorf("apply migrations: %w", err)
	}

	abandonedStreamingMessages, err := cleanupAbandonedStreamingMessages(db)
	if err != nil {
		return nil, fmt.Errorf("cleanup abandoned streaming messages: %w", err)
	}
	interruptedAgentTurnIntents, err := cleanupAbandonedAgentTurnIntents(db)
	if err != nil {
		return nil, fmt.Errorf("cleanup abandoned agent turn intents: %w", err)
	}

	deviceID, firstLaunch, err := ensureDeviceID(db)
	if err != nil {
		return nil, fmt.Errorf("ensure device_id: %w", err)
	}

	integrityCheck, err := integrityChecker(db)
	if err != nil {
		return nil, err
	}

	forceBackupRefresh := len(newlyApplied) > 0 || abandonedStreamingMessages > 0 || interruptedAgentTurnIntents > 0 || firstLaunch
	backupPath, err := backupLocalDB(db, dataDir, forceBackupRefresh)
	if err != nil {
		return nil, fmt.Errorf("backup local db: %w", err)
	}

	return &OpenResult{
		DB:                          db,
		DBPath:                      dbPath,
		DataDir:                     dataDir,
		DeviceID:                    deviceID,
		NewMigrations:               newlyApplied,
		FirstLaunch:                 firstLaunch,
		AbandonedStreamingMessages:  abandonedStreamingMessages,
		InterruptedAgentTurnIntents: interruptedAgentTurnIntents,
		BackupPath:                  backupPath,
		IntegrityCheck:              integrityCheck,
	}, nil
}

// metaRow is the row shape of _local_meta.
type metaRow struct {
	Key       string `gorm:"column:key;primaryKey"`
	Value     string `gorm:"column:value"`
	UpdatedAt string `gorm:"column:updated_at"`
}

func (metaRow) TableName() string { return "_local_meta" }

const (
	metaKeyDeviceID = "device_id"
)

// ensureDeviceID returns the device_id from _local_meta, generating and
// persisting one on first launch. The value is a 32-char hex string
// generated from 16 crypto-random bytes, stable across login/logout
// and shared with OAuth + future marketplace install attribution.
func ensureDeviceID(db *gorm.DB) (id string, firstLaunch bool, err error) {
	var row metaRow
	err = db.Where("key = ?", metaKeyDeviceID).First(&row).Error
	if err == nil {
		if !isValidDeviceID(row.Value) {
			return "", false, fmt.Errorf("stored device_id is invalid: must be a 32-character hex string")
		}
		return row.Value, false, nil
	}
	if err != gorm.ErrRecordNotFound {
		return "", false, err
	}
	// First launch — generate an opaque 128-bit random identifier.
	newID, err := newRandomDeviceID()
	if err != nil {
		return "", false, err
	}
	rec := metaRow{Key: metaKeyDeviceID, Value: newID}
	if err := db.Create(&rec).Error; err != nil {
		return "", false, err
	}
	return newID, true, nil
}

func cleanupAbandonedStreamingMessages(db *gorm.DB) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res := db.Exec(`
		UPDATE w_workagent_message
		   SET streaming_state = 'partial',
		       updated_at = ?
		 WHERE streaming_state = 'streaming'
	`, now)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func backupLocalDB(db *gorm.DB, dataDir string, forceRefresh bool) (string, error) {
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	backupPath := filepath.Join(backupDir, "workagent-"+time.Now().UTC().Format("20060102")+".db")
	if _, err := os.Stat(backupPath); err == nil {
		if !forceRefresh {
			if err := pruneLocalDBBackups(backupDir, 7); err != nil {
				return "", err
			}
			return backupPath, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat backup: %w", err)
	}

	tmpBackupPath := filepath.Join(
		backupDir,
		fmt.Sprintf(".workagent-%s-%d.tmp", time.Now().UTC().Format("20060102"), time.Now().UnixNano()),
	)
	defer func() { _ = os.Remove(tmpBackupPath) }()

	if err := db.Exec(`VACUUM INTO ?`, tmpBackupPath).Error; err != nil {
		return "", fmt.Errorf("vacuum into %s: %w", tmpBackupPath, err)
	}
	if err := os.Rename(tmpBackupPath, backupPath); err != nil {
		return "", fmt.Errorf("promote backup %s to %s: %w", tmpBackupPath, backupPath, err)
	}
	if err := pruneLocalDBBackups(backupDir, 7); err != nil {
		return "", err
	}
	return backupPath, nil
}

func pruneLocalDBBackups(backupDir string, keep int) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return fmt.Errorf("read backup dir: %w", err)
	}
	backups := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "workagent-") && strings.HasSuffix(name, ".db") {
			backups = append(backups, filepath.Join(backupDir, name))
		}
	}
	sort.Strings(backups)
	for len(backups) > keep {
		if err := os.Remove(backups[0]); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old backup %s: %w", backups[0], err)
		}
		backups = backups[1:]
	}
	return nil
}

func checkLocalDBIntegrity(db *gorm.DB) (string, error) {
	rows, err := db.Raw(`PRAGMA integrity_check`).Rows()
	if err != nil {
		return "", fmt.Errorf("run sqlite integrity_check: %w", err)
	}
	defer rows.Close()

	results := make([]string, 0, 1)
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return "", fmt.Errorf("scan sqlite integrity_check: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterate sqlite integrity_check: %w", err)
	}
	return validateIntegrityCheckResults(results)
}

func validateIntegrityCheckResults(results []string) (string, error) {
	if len(results) == 1 && results[0] == "ok" {
		return "ok", nil
	}
	if len(results) == 0 {
		return "", fmt.Errorf("sqlite integrity_check returned no rows")
	}

	const maxDetails = 5
	details := results
	if len(details) > maxDetails {
		details = details[:maxDetails]
	}
	msg := strings.Join(details, "; ")
	if len(results) > maxDetails {
		msg = fmt.Sprintf("%s; ... (%d total)", msg, len(results))
	}
	return "", fmt.Errorf("sqlite integrity_check failed: %s", msg)
}

// newSilentishLogger returns a GORM logger that writes to stderr (never
// stdout — see OpenLocalDB comment), only at Error level. ErrRecordNotFound
// is deliberately silenced because we use First() with explicit
// ErrRecordNotFound handling in normal control flow.
func newSilentishLogger() gormlogger.Interface {
	return gormlogger.New(
		log.New(os.Stderr, "gorm ", log.LstdFlags|log.Lmicroseconds|log.LUTC),
		gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Error,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)
}

// newRandomDeviceID returns a 32-char hex string from 16 random bytes.
// Not strictly RFC 4122 UUIDv4 (no version/variant bits) but sufficient
// as an opaque device identifier.
func newRandomDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func isValidDeviceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
