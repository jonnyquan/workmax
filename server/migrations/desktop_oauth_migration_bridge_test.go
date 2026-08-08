package migrations

import (
	"os"
	"strings"
	"testing"
)

const desktopOAuthBridgeMigrationFile = "20260634_rename_oauth_tables_to_w_desktop_prefix.sql"

func TestDesktopOAuthRenameMigrationSupportsFreshAndLegacySchemas(t *testing.T) {
	raw, err := os.ReadFile(desktopOAuthBridgeMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", desktopOAuthBridgeMigrationFile, err)
	}
	sql := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))

	for _, want := range []string{
		"from information_schema.tables",
		"@workmax_oauth_legacy_table_count = 3 and @workmax_oauth_target_table_count = 0",
		"@workmax_oauth_legacy_table_count = 0 and @workmax_oauth_target_table_count = 3",
		"desktop oauth table names already current",
		"__workmax_invalid_desktop_oauth_schema_state__",
		"prepare workmax_oauth_bridge_stmt",
		"execute workmax_oauth_bridge_stmt",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration is missing %q", want)
		}
	}

	// The executable section must not contain an unconditional legacy rename;
	// otherwise a fresh install (where 20260633 already created target tables)
	// fails before Server startup.
	executable := sql[strings.Index(sql, "set @workmax_schema_name"):]
	if strings.HasPrefix(executable, "rename table `oauth_client`") {
		t.Fatal("legacy table rename must be guarded by schema-state discovery")
	}
}
