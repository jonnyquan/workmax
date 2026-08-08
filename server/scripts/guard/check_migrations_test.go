package main

import (
	"os"
	"testing"
)

// Pure-helper tests — no filesystem, no real migrations. Exercises
// the parsing + drift-detection logic directly so schema-churn
// scenarios can be described as test inputs.

func TestParseTestdbTables_ExtractsNameAndBody(t *testing.T) {
	src := `var schema = []string{
		` + "`CREATE TABLE w_foo (id INTEGER, name TEXT)`" + `,
		` + "`CREATE TABLE w_bar (id INTEGER, solution_type TEXT)`" + `,
	}`
	got := parseTestdbTables(src)
	if _, ok := got["w_foo"]; !ok {
		t.Errorf("w_foo not parsed")
	}
	if _, ok := got["w_bar"]; !ok {
		t.Errorf("w_bar not parsed")
	}
	// Body substring check so per-column drift detection works.
	if body := got["w_bar"]; !contains(body, "solution_type") {
		t.Errorf("w_bar body should include solution_type, got %q", body)
	}
}

func TestFindMigrationDrift_MissingTable(t *testing.T) {
	testdbTables := map[string]string{
		"w_existing": "id INTEGER, name TEXT",
	}
	body := "CREATE TABLE `w_new_table` (id INTEGER);"
	got := findMigrationDrift(body, "20260601_add_w_new_table.sql", testdbTables)
	if len(got) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got))
	}
	if got[0].table != "w_new_table" {
		t.Errorf("table: got %q", got[0].table)
	}
	if got[0].column != "" {
		t.Errorf("finding should be whole-table (no column), got %q", got[0].column)
	}
}

func TestFindMigrationDrift_NoDriftWhenTablePresent(t *testing.T) {
	testdbTables := map[string]string{
		"w_already_there": "id INTEGER",
	}
	body := "CREATE TABLE `w_already_there` (id INTEGER);"
	got := findMigrationDrift(body, "x.sql", testdbTables)
	if len(got) != 0 {
		t.Errorf("expected no drift, got %+v", got)
	}
}

func TestFindMigrationDrift_AddColumnMissing(t *testing.T) {
	testdbTables := map[string]string{
		"w_thing": "id INTEGER, name TEXT",
	}
	body := "ALTER TABLE `w_thing` ADD COLUMN `new_col` VARCHAR(64);"
	got := findMigrationDrift(body, "add_col.sql", testdbTables)
	if len(got) != 1 || got[0].column != "new_col" {
		t.Errorf("expected new_col finding, got %+v", got)
	}
}

func TestFindMigrationDrift_AddColumnPresent(t *testing.T) {
	// Column already in the CREATE body — no drift.
	testdbTables := map[string]string{
		"w_thing": "id INTEGER, new_col VARCHAR(64)",
	}
	body := "ALTER TABLE `w_thing` ADD COLUMN `new_col` VARCHAR(64);"
	got := findMigrationDrift(body, "x.sql", testdbTables)
	if len(got) != 0 {
		t.Errorf("column present; no drift expected, got %+v", got)
	}
}

func TestFindMigrationDrift_AddColumnSubstringFalsePositiveGuard(t *testing.T) {
	// The column name `negative_anchors` must NOT be matched as
	// already-present just because `anchors` appears in the CREATE
	// body. Word-boundary regex should catch this.
	testdbTables := map[string]string{
		"w_thing": "id INTEGER, anchors TEXT",
	}
	body := "ALTER TABLE `w_thing` ADD COLUMN `negative_anchors` TEXT;"
	got := findMigrationDrift(body, "x.sql", testdbTables)
	if len(got) != 1 {
		t.Errorf("substring in existing column should still be drift, got %+v", got)
	}
}

func TestFilterIncrementalMigrations_KeepsDateDashPrefixedFiles(t *testing.T) {
	// Only YYYYMMDD_… files are incremental. Dumps like workmaxdev.sql
	// and misc backups should be dropped.
	inputs := []string{
		"migrations/20260417_create_foo.sql",
		"migrations/20260504_refactor_bar.sql",
		"migrations/workmaxdev.sql",
		"migrations/backup.sql",
		"migrations/2024_legacy.sql", // wrong length (4 digits)
	}
	got := filterIncrementalMigrations(inputs)
	if len(got) != 2 {
		t.Errorf("expected 2 incremental files, got %v", got)
	}
}

func TestLoadAllowList_ParsesCommentsAndBlanks(t *testing.T) {
	tmp := t.TempDir() + "/allow.txt"
	if err := writeFile(tmp, `# comment
w_foo
  # indented comment

w_bar/new_col
`); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := loadAllowList(tmp)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := got["w_foo"]; !ok {
		t.Errorf("w_foo missing")
	}
	if _, ok := got["w_bar/new_col"]; !ok {
		t.Errorf("w_bar/new_col missing")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries, got %v", got)
	}
}

func TestLoadAllowList_MissingFileIsEmpty(t *testing.T) {
	got, err := loadAllowList("/does/not/exist/allow.txt")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestIsAllowed_TableMatch(t *testing.T) {
	allow := allowEntries{"w_foo": {}}
	if !isAllowed(allow, migrationFinding{table: "w_foo"}) {
		t.Error("table match should be allowed")
	}
	if isAllowed(allow, migrationFinding{table: "w_bar"}) {
		t.Error("non-match should not be allowed")
	}
}

func TestIsAllowed_ColumnMatch(t *testing.T) {
	allow := allowEntries{"w_foo/new_col": {}}
	if !isAllowed(allow, migrationFinding{table: "w_foo", column: "new_col"}) {
		t.Error("table/column match should be allowed")
	}
	// Table-only finding with column on allow-list should NOT match —
	// the allow-list entry specifies the column.
	if isAllowed(allow, migrationFinding{table: "w_foo"}) {
		t.Error("table/column entry should not match a bare-table finding")
	}
}

// --- local test helpers ---

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
