package migrations

import (
	"fmt"
	"os"
	"regexp"
	"server/utils/testutil"
	"strings"
	"testing"
)

const orderWebhookIdempotencyMigrationFile = "20260808_harden_order_webhook_idempotency.sql"

func TestOrderWebhookIdempotencyMigrationGuardsBeforePersistentDDL(t *testing.T) {
	raw, err := os.ReadFile(orderWebhookIdempotencyMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", orderWebhookIdempotencyMigrationFile, err)
	}
	sql := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))

	for _, want := range []string{
		"create temporary table `_w_order_invoice_duplicate_guard`",
		"constraint `chk_w_order_invoice_duplicate_guard` check (`incompatible_rows` = 0)",
		"convert(nullif(trim(`invoice`), '') using utf8mb4) collate utf8mb4_bin as `invoice_idempotency_key`",
		"where `invoice_idempotency_key` is not null group by `invoice_idempotency_key` having count(*) > 1",
		"create temporary table `_w_order_no_capacity_guard`",
		"constraint `chk_w_order_no_capacity_guard` check (`incompatible_rows` = 0)",
		"from `information_schema`.`columns`",
		"`table_schema` = database()",
		"`table_name` = 'w_order'",
		"`column_name` = 'no'",
		"`data_type` in ('char', 'varchar')",
		"`character_maximum_length` >= 32",
		"`character_octet_length` >= 32",
		"create temporary table `_w_order_schema_compatibility_guard`",
		"constraint `chk_w_order_schema_compatibility_guard` check (`incompatible_rows` = 0)",
		"from `information_schema`.`tables`",
		"upper(`engine`) = 'innodb'",
		"lower(coalesce(`create_options`, '')) not like '%partitioned%'",
		"`column_name` = 'invoice'",
		"`data_type` in ('char', 'varchar')",
		"`character_maximum_length` between 1 and 64",
		"`character_set_name` is not null",
		"alter table `w_order` add column `invoice_idempotency_key` varchar(64) character set utf8mb4 collate utf8mb4_bin generated always as (nullif(trim(`invoice`), '')) stored",
		"add unique key `uk_w_order_invoice_idempotency` (`invoice_idempotency_key`)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("migration missing %q", want)
		}
	}

	duplicateGuard := strings.Index(sql, "insert into `_w_order_invoice_duplicate_guard`")
	capacityGuard := strings.Index(sql, "insert into `_w_order_no_capacity_guard`")
	schemaGuard := strings.Index(sql, "insert into `_w_order_schema_compatibility_guard`")
	alter := strings.Index(sql, "alter table `w_order`")
	if duplicateGuard < 0 || capacityGuard < 0 || schemaGuard < 0 || alter < 0 {
		t.Fatal("migration must contain all fail-closed guards and the persistent ALTER")
	}
	if duplicateGuard >= alter || capacityGuard >= alter || schemaGuard >= alter {
		t.Fatal("all compatibility guards must run before the first persistent ALTER")
	}
	if strings.Contains(sql[:alter], "create table ") {
		t.Fatal("migration must not create a persistent table before compatibility guards complete")
	}

	generatedStart := strings.Index(sql, "add column `invoice_idempotency_key`")
	uniqueStart := strings.Index(sql, "add unique key `uk_w_order_invoice_idempotency`")
	if generatedStart < 0 || uniqueStart <= generatedStart {
		t.Fatal("nullable generated invoice key must be declared before its unique index")
	}
	generatedDefinition := sql[generatedStart:uniqueStart]
	if strings.Contains(generatedDefinition, "not null") {
		t.Fatal("generated invoice key must remain nullable so legacy blank invoices may coexist")
	}

	for _, safetyNote := range []string{
		"pause webhook/order writers",
		"multiple null",
		"no update, backfill or deletion",
		"migration fail closed",
		"dropping them would reopen duplicate webhook admission",
	} {
		if !strings.Contains(sql, safetyNote) {
			t.Errorf("migration safety notes missing %q", safetyNote)
		}
	}

	if regexp.MustCompile(`(?i)\bupdate\s+`+"`?w_order").MatchString(string(raw)) ||
		regexp.MustCompile(`(?i)\bdelete\s+from\s+`+"`?w_order").MatchString(string(raw)) {
		t.Fatal("migration must not silently rewrite or delete legacy orders")
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestOrderWebhookIdempotencySQLiteMirrorNormalizesOnlyNonemptyInvoices(t *testing.T) {
	db := testutil.NewTestDB(t)
	for i, invoice := range []any{nil, nil, "", "", "   ", "\t"} {
		if err := db.Exec(
			"INSERT INTO w_order (no, invoice) VALUES (?, ?)",
			fmt.Sprintf("blank-%d", i), invoice,
		).Error; err != nil {
			t.Fatalf("insert compatible blank invoice %d: %v", i, err)
		}
	}
	if err := db.Exec("INSERT INTO w_order (no, invoice) VALUES (?, ?)", "first", " in_exact ").Error; err != nil {
		t.Fatalf("insert first normalized invoice: %v", err)
	}
	if err := db.Exec("INSERT INTO w_order (no, invoice) VALUES (?, ?)", "duplicate", "in_exact").Error; err == nil {
		t.Fatal("trim-equivalent nonempty invoice bypassed unique generated key")
	}
	if err := db.Exec("INSERT INTO w_order (no, invoice) VALUES (?, ?)", "case-distinct", "IN_EXACT").Error; err != nil {
		t.Fatalf("binary-distinct invoice should remain distinct: %v", err)
	}
}
