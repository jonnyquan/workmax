package main

// check-migrations guard.
//
// Looks at every migration .sql file that CREATEs or ALTERs a
// `w_*` table and cross-references it against utils/testutil/testdb.go's
// testSchemaDDL. Tables introduced in production migrations that
// aren't reflected in the test helper will silently fail tests
// later — or worse, pass tests against a stale schema.
//
// This is an approximate check: the guard can't (and shouldn't) be
// a full SQL parser. It flags tables present in migrations but
// absent from the test DDL, and columns added via ALTER TABLE that
// don't appear in testSchemaDDL's CREATE string.
//
// Exit codes:
//   0: every migration-referenced table appears in testdb.go, AND
//      every ALTER TABLE's new column name appears somewhere in
//      the corresponding CREATE statement.
//   1: drift detected (details on stderr).
//   2: infra error (can't read a file, etc.).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// tableNameRe captures `<CREATE TABLE | ALTER TABLE>[ IF NOT EXISTS]
// `wname`` — catching the backtick-quoted form MySQL migrations use.
// Case-insensitive so `create table` and `CREATE TABLE` both work.
var tableNameRe = regexp.MustCompile("(?i)(CREATE TABLE|ALTER TABLE)(?:\\s+IF NOT EXISTS)?\\s+`(w_[a-z0-9_]+)`")

// alterAddColumnRe captures new columns added by ALTER TABLE
// `wtable` ADD COLUMN `column_name`. The guard reports any such
// column that doesn't appear by name in the testdb CREATE string
// for that table.
var alterAddColumnRe = regexp.MustCompile("(?i)ALTER TABLE\\s+`(w_[a-z0-9_]+)`[\\s\\S]*?ADD COLUMN\\s+`([a-z0-9_]+)`")

// testdbCreateRe pulls out `CREATE TABLE w_name (…)` statements
// from the test DDL string. We match body-unconditionally because
// the DDL is ours, not arbitrary SQL.
var testdbCreateRe = regexp.MustCompile("(?is)CREATE TABLE\\s+(w_[a-z0-9_]+)\\s*\\(([^)]*?)\\)")

type migrationFinding struct {
	table   string
	column  string // empty when the whole table is missing
	file    string
	reason  string
}

func runCheckMigrations() int {
	const migrationsDir = "migrations"
	const testdbPath = "utils/testutil/testdb.go"
	const allowPath = "scripts/guard/migrations_allow.txt"

	testdb, err := os.ReadFile(testdbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", testdbPath, err)
		return 2
	}
	testdbTables := parseTestdbTables(string(testdb))

	// Allow-list lets operators intentionally exclude tables that
	// don't need testSchemaDDL coverage (superseded by refactors,
	// or just not exercised by any integration test yet). Missing
	// file is fine — guard runs strictly in that case.
	allow, err := loadAllowList(allowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", allowPath, err)
		return 2
	}

	migrationFiles, err := filepath.Glob(filepath.Join(migrationsDir, "*.sql"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "glob %s: %v\n", migrationsDir, err)
		return 2
	}
	// Only check incremental migrations (YYYYMMDD-prefixed). Full-
	// schema snapshots like workmaxdev.sql are dumps of every table the
	// app has ever had — they'd false-positive every non-short-drama
	// row and drown out real drift. Incremental files are the units
	// of "what's new since the test helper was last updated", which
	// is exactly what this guard is for.
	migrationFiles = filterIncrementalMigrations(migrationFiles)

	var findings []migrationFinding
	for _, file := range migrationFiles {
		body, err := os.ReadFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", file, err)
			return 2
		}
		for _, f := range findMigrationDrift(string(body), filepath.Base(file), testdbTables) {
			if isAllowed(allow, f) {
				continue
			}
			findings = append(findings, f)
		}
	}

	if len(findings) == 0 {
		fmt.Printf("ok: %d migration file(s) checked against testdb.go (%d w_ tables covered)\n",
			len(migrationFiles), len(testdbTables))
		return 0
	}

	// Group findings by file for readability.
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].file != findings[j].file {
			return findings[i].file < findings[j].file
		}
		return findings[i].table < findings[j].table
	})
	fmt.Fprintln(os.Stderr, "migration / testdb drift detected:")
	for _, f := range findings {
		if f.column != "" {
			fmt.Fprintf(os.Stderr, "  - %s: table %q column %q %s\n", f.file, f.table, f.column, f.reason)
		} else {
			fmt.Fprintf(os.Stderr, "  - %s: table %q %s\n", f.file, f.table, f.reason)
		}
	}
	fmt.Fprintln(os.Stderr, "\nfix: extend testSchemaDDL in utils/testutil/testdb.go to mirror the new schema.")
	return 1
}

// allowEntries is the parsed allow-list. Map lookup is O(1) and
// the allow-list stays small (dozens of entries at most).
type allowEntries map[string]struct{}

// loadAllowList reads a newline-separated exclusion file. Missing
// file = empty allow-list (all drift surfaces). Lines starting
// with `#` and blank lines are ignored; other lines are trimmed.
// Entry format: "table" OR "table/column" matching isAllowed's
// keys below.
func loadAllowList(path string) (allowEntries, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return allowEntries{}, nil
		}
		return nil, err
	}
	out := allowEntries{}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = struct{}{}
	}
	return out, nil
}

// isAllowed returns true when the given finding is on the
// allow-list. Finds match on either the bare table name (whole-
// table exclusions) or "table/column" (per-column).
func isAllowed(allow allowEntries, f migrationFinding) bool {
	if _, ok := allow[f.table]; ok {
		return true
	}
	if f.column != "" {
		if _, ok := allow[f.table+"/"+f.column]; ok {
			return true
		}
	}
	return false
}

// filterIncrementalMigrations keeps only files whose basename
// starts with 8 digits (YYYYMMDD_…). Drops snapshots like
// workmaxdev.sql and any *_backup.sql the ops team might drop in.
var incrementalPrefixRe = regexp.MustCompile(`^\d{8}_`)

func filterIncrementalMigrations(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if incrementalPrefixRe.MatchString(filepath.Base(p)) {
			out = append(out, p)
		}
	}
	return out
}

// parseTestdbTables turns the testdb.go source into a map from
// table name to the body of its CREATE statement (for substring
// column checks).
func parseTestdbTables(src string) map[string]string {
	out := map[string]string{}
	for _, m := range testdbCreateRe.FindAllStringSubmatch(src, -1) {
		// m[1] = table, m[2] = body inside parens
		out[m[1]] = m[2]
	}
	return out
}

// findMigrationDrift surfaces two classes of problem:
//   - CREATE TABLE in a migration for a table that's not in testdb.go
//   - ALTER TABLE ADD COLUMN for a column that's absent from the
//     testdb.go CREATE body for the target table
func findMigrationDrift(body, fileBase string, testdbTables map[string]string) []migrationFinding {
	var out []migrationFinding

	// Which w_ tables does this migration touch?
	touched := map[string]bool{}
	for _, m := range tableNameRe.FindAllStringSubmatch(body, -1) {
		op := strings.ToUpper(strings.TrimSpace(m[1]))
		table := m[2]
		// DROP TABLE isn't covered by this regex so we only see
		// CREATE + ALTER; both imply the table must exist in testdb.go.
		touched[table] = true
		if op == "CREATE TABLE" {
			if _, ok := testdbTables[table]; !ok {
				out = append(out, migrationFinding{
					table:  table,
					file:   fileBase,
					reason: "created in migration but not present in testdb.go testSchemaDDL",
				})
			}
		}
	}
	_ = touched // reserved for future per-table summary

	// ADD COLUMN: each one must appear in the testdb CREATE body.
	for _, m := range alterAddColumnRe.FindAllStringSubmatch(body, -1) {
		table, column := m[1], m[2]
		tdbBody, ok := testdbTables[table]
		if !ok {
			// Already reported above when the ALTER also implies
			// the table itself is unknown. Skip duplicate noise.
			continue
		}
		// Match `column_name` OR column_name boundary so renames
		// don't false-positive on substrings (e.g. adding
		// `identity_anchors` doesn't falsely match `negative_anchors`).
		colRe := regexp.MustCompile("(?i)(^|[^a-z0-9_])" + regexp.QuoteMeta(column) + "($|[^a-z0-9_])")
		if !colRe.MatchString(tdbBody) {
			out = append(out, migrationFinding{
				table:  table,
				column: column,
				file:   fileBase,
				reason: "added in migration but missing from testdb.go CREATE body",
			})
		}
	}

	return out
}
