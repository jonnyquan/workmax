package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestWorkAgentThreadUUIDUniqueMigrationPinsIdempotencyInvariant(t *testing.T) {
	const migration = "20260806_enforce_workagent_thread_uuid_unique.sql"
	body, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read %s: %v", migration, err)
	}
	sql := strings.ToLower(string(body))
	for _, required := range []string{
		"information_schema.statistics",
		"`non_unique` = 0",
		"group by `index_name`",
		"having count(*) = 1",
		"`column_name` = 'uuid'",
		"alter table `w_workagent_thread` add unique key `uk_workagent_thread_uuid` (`uuid`)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	if strings.Contains(sql, "delete from `w_workagent_thread`") {
		t.Fatal("migration must not delete duplicate user rows")
	}
}
