//go:build desktop

// Package migrationsdesktop applies SQL migration files to the local
// SQLite database used by the workagent-desktop sidecar.
//
// Migrations are embedded into the binary via embed.FS. Each file is
// named NNNN_*.sql and executed in lexicographic order. Already-applied
// files are tracked in the _schema_migrations table.
//
// The runner creates _schema_migrations itself before scanning, so
// migration files only have to deal with business tables.
//
// Single-writer SQLite means we don't need a distributed lock; each
// migration runs inside one transaction.
package migrationsdesktop

import (
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

//go:embed *.sql
var migrationFS embed.FS

// MigrationRecord tracks which migration files have been applied.
type MigrationRecord struct {
	Version   string `gorm:"column:version;primaryKey"`
	AppliedAt string `gorm:"column:applied_at"`
}

// TableName matches the table name used by the bootstrap CREATE TABLE
// below; we sidestep gorm's pluralization rules with an explicit name.
func (MigrationRecord) TableName() string { return "_schema_migrations" }

// Apply runs any migration files that have not yet been recorded
// against the given database. Idempotent: re-running is a no-op when
// nothing has changed.
//
// Returns the list of versions newly applied (empty if up-to-date).
func Apply(db *gorm.DB) ([]string, error) {
	if err := ensureMigrationsTable(db); err != nil {
		return nil, fmt.Errorf("create _schema_migrations: %w", err)
	}

	applied, err := loadAppliedVersions(db)
	if err != nil {
		return nil, fmt.Errorf("load applied versions: %w", err)
	}

	available, err := scanMigrationFiles()
	if err != nil {
		return nil, fmt.Errorf("scan migration files: %w", err)
	}
	if err := rejectUnsupportedAppliedVersions(applied, available); err != nil {
		return nil, err
	}

	var newlyApplied []string
	for _, m := range available {
		if applied[m.version] {
			continue
		}
		if err := applyOne(db, m); err != nil {
			return newlyApplied, fmt.Errorf("apply %s: %w", m.filename, err)
		}
		newlyApplied = append(newlyApplied, m.version)
	}
	return newlyApplied, nil
}

func ensureMigrationsTable(db *gorm.DB) error {
	return db.Exec(`
		CREATE TABLE IF NOT EXISTS _schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)
	`).Error
}

func loadAppliedVersions(db *gorm.DB) (map[string]bool, error) {
	var rows []MigrationRecord
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Version] = true
	}
	return out, nil
}

type migrationFile struct {
	version  string
	filename string
	body     []byte
}

func scanMigrationFiles() ([]migrationFile, error) {
	entries, err := migrationFS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var migs []migrationFile
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		migs = append(migs, migrationFile{
			version:  versionFromFilename(name),
			filename: name,
			body:     body,
		})
	}
	sort.Slice(migs, func(i, j int) bool {
		return migs[i].version < migs[j].version
	})
	return migs, nil
}

func rejectUnsupportedAppliedVersions(applied map[string]bool, available []migrationFile) error {
	known := make(map[string]bool, len(available))
	maxSupported := ""
	for _, m := range available {
		known[m.version] = true
		if m.version > maxSupported {
			maxSupported = m.version
		}
	}
	for version := range applied {
		if known[version] {
			continue
		}
		if maxSupported == "" || version > maxSupported {
			return fmt.Errorf("database schema version %s is newer than this sidecar supports (max %s)", version, maxSupported)
		}
		return fmt.Errorf("database has unknown applied migration %s", version)
	}
	return nil
}

// versionFromFilename returns the leading "NNNN" portion of a migration
// filename. "0001_init.sql" -> "0001". If a name lacks an underscore we
// fall back to the whole name without .sql.
func versionFromFilename(name string) string {
	trimmed := strings.TrimSuffix(name, ".sql")
	if idx := strings.Index(trimmed, "_"); idx > 0 {
		return trimmed[:idx]
	}
	return trimmed
}

// applyOne runs a single migration file inside a transaction and
// records it in _schema_migrations on success.
func applyOne(db *gorm.DB, m migrationFile) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// SQLite can execute multi-statement SQL in one Exec; split is
		// unnecessary here. If a migration spans many statements they
		// run as one transaction.
		if err := tx.Exec(string(m.body)).Error; err != nil {
			return err
		}
		record := MigrationRecord{
			Version:   m.version,
			AppliedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		return tx.Create(&record).Error
	})
}
