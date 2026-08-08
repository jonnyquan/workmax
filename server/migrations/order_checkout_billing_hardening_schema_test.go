package migrations

import (
	"fmt"
	"os"
	"regexp"
	"server/utils/testutil"
	"strings"
	"testing"
)

const orderCheckoutBillingHardeningMigrationFile = "20260810_harden_order_checkout_and_billing_period.sql"

func TestOrderCheckoutBillingHardeningMigrationFailsClosedBeforeAtomicDDL(t *testing.T) {
	raw, err := os.ReadFile(orderCheckoutBillingHardeningMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", orderCheckoutBillingHardeningMigrationFile, err)
	}
	sql := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))

	for _, want := range []string{
		"create temporary table `_w_order_checkout_version_guard`",
		"regexp_substr(version(), '^[0-9]+[.][0-9]+[.][0-9]+')",
		"locate('mariadb', lower(version())) = 0",
		"@w_order_checkout_version_major = 8",
		"@w_order_checkout_version_minor = 0",
		"@w_order_checkout_version_patch >= 19",
		"create temporary table `_w_order_checkout_table_guard`",
		"from `information_schema`.`tables`",
		"`table_type` = 'base table'",
		"upper(`engine`) = 'innodb'",
		"upper(`row_format`) in ('dynamic', 'compressed')",
		"@@innodb_page_size >= 8192",
		"from `information_schema`.`partitions`",
		"`partition_name` is not null",
		"create temporary table `_w_order_checkout_no_guard`",
		"`column_name` = 'no'",
		"`data_type` in ('char', 'varchar')",
		"`character_maximum_length` >= 32",
		"`character_octet_length` >= 32",
		"from `w_order` where `no` is null or nullif(trim(`no`), '') is null",
		"`ordered_columns` = 'no'",
		"create temporary table `_w_order_checkout_invoice_baseline_guard`",
		"`column_name` = 'invoice_idempotency_key'",
		"`character_maximum_length` = 64",
		"`collation_name` = 'utf8mb4_bin'",
		"upper(`extra`) like '%stored generated%'",
		"replace(`generation_expression`, '`', '')",
		") = 'nullif(trim(invoice),'''')') = 1",
		"`index_name` = 'uk_w_order_invoice_idempotency'",
		"`ordered_columns` = 'invoice_idempotency_key'",
		"create temporary table `_w_order_checkout_legacy_unpaid_guard`",
		"from `w_order` where upper(trim(`status`)) = 'unpaid' and lower(trim(`pay_method`)) = 'stripe'",
		"create temporary table `_w_order_checkout_duplicate_guard`",
		"convert(nullif(trim(`checkout_session_id`), '''') using utf8mb4) collate utf8mb4_bin as `checkout_session_idempotency_key`",
		"where `checkout_session_idempotency_key` is not null",
		"group by `checkout_session_idempotency_key` having count(*) > 1",
		"create temporary table `_w_order_checkout_billing_pair_guard`",
		"where (`billing_period_start` is null) <> (`billing_period_end` is null)",
		"`billing_period_start` >= `billing_period_end`",
		"create temporary table `_w_order_checkout_provider_guard`",
		"where nullif(trim(`checkout_session_id`), '''') is not null and nullif(trim(`provider_price_id`), '''') is null",
		"create temporary table `_w_order_checkout_fingerprint_guard`",
		"'provider_price_id', 'billing_period_start', 'billing_period_end', 'checkout_session_id', 'checkout_session_idempotency_key'",
		"`index_name` = 'uk_w_order_checkout_session_identity'",
		"from `information_schema`.`table_constraints`",
		"'chk_w_order_billing_period_pair', 'chk_w_order_checkout_provider_price'",
		"`ordered_columns` = 'checkout_session_idempotency_key'",
		"alter table `w_order` add column `provider_price_id` varchar(64) character set utf8mb4 collate utf8mb4_bin not null default ''",
		"add column `billing_period_start` datetime(6) null default null",
		"add column `billing_period_end` datetime(6) null default null",
		"add column `checkout_session_id` varchar(255) character set utf8mb4 collate utf8mb4_bin not null default ''",
		"add column `checkout_session_idempotency_key` varchar(255) character set utf8mb4 collate utf8mb4_bin generated always as (nullif(trim(`checkout_session_id`), '')) stored",
		"add unique key `uk_w_order_checkout_session_identity` (`checkout_session_idempotency_key`)",
		"add constraint `chk_w_order_billing_period_pair` check (",
		"(`billing_period_start` is null and `billing_period_end` is null)",
		"(`billing_period_start` is not null and `billing_period_end` is not null and `billing_period_start` < `billing_period_end`)",
		"add constraint `chk_w_order_checkout_provider_price` check (",
		"nullif(trim(`checkout_session_id`), '') is null or nullif(trim(`provider_price_id`), '') is not null",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("migration missing %q", want)
		}
	}

	guardInserts := []string{
		"insert into `_w_order_checkout_version_guard` (`guard_key`) select case",
		"insert into `_w_order_checkout_table_guard` (`guard_key`) select case",
		"insert into `_w_order_checkout_no_guard` (`guard_key`) select case",
		"insert into `_w_order_checkout_invoice_baseline_guard` (`guard_key`) select case",
		"insert into `_w_order_checkout_legacy_unpaid_guard` (`guard_key`) select case",
		"execute `w_order_checkout_duplicate_stmt`",
		"execute `w_order_checkout_billing_pair_stmt`",
		"execute `w_order_checkout_provider_stmt`",
		"insert into `_w_order_checkout_fingerprint_guard` (`guard_key`) select case",
	}
	alter := strings.Index(sql, "alter table `w_order`")
	if alter < 0 {
		t.Fatal("migration must contain its persistent w_order ALTER")
	}
	for _, guard := range guardInserts {
		pos := strings.Index(sql, guard)
		if pos < 0 || pos >= alter {
			t.Fatalf("guard %q must execute before the first persistent ALTER", guard)
		}
	}
	if strings.Count(sql, "alter table `w_order`") != 1 {
		t.Fatal("all checkout and billing-period definitions must use one atomic ALTER")
	}
	if strings.Contains(sql[:alter], "create table ") {
		t.Fatal("migration must not create a persistent table before guards finish")
	}

	generatedStart := strings.Index(sql[alter:], "add column `checkout_session_idempotency_key`")
	uniqueStart := strings.Index(sql[alter:], "add unique key `uk_w_order_checkout_session_identity`")
	if generatedStart < 0 || uniqueStart <= generatedStart {
		t.Fatal("nullable generated checkout key must precede its UNIQUE key")
	}
	generatedDefinition := sql[alter+generatedStart : alter+uniqueStart]
	if strings.Contains(generatedDefinition, "not null") {
		t.Fatal("generated checkout identity must remain nullable for blank legacy values")
	}

	for _, guardName := range []string{
		"_w_order_checkout_version_guard",
		"_w_order_checkout_table_guard",
		"_w_order_checkout_no_guard",
		"_w_order_checkout_invoice_baseline_guard",
		"_w_order_checkout_legacy_unpaid_guard",
		"_w_order_checkout_duplicate_guard",
		"_w_order_checkout_billing_pair_guard",
		"_w_order_checkout_provider_guard",
		"_w_order_checkout_fingerprint_guard",
	} {
		if !strings.Contains(sql, "primary key (`guard_key`)") {
			t.Fatalf("guard %s must use a duplicate-primary-key fail-closed sentinel", guardName)
		}
		if !strings.Contains(sql, "drop temporary table `"+guardName+"`") {
			t.Fatalf("guard %s must be cleaned up before persistent DDL", guardName)
		}
	}

	for _, safetyNote := range []string{
		"mysql 8.0.19 or newer",
		"migration ordering: 20260808_harden_order_webhook_idempotency.sql must have",
		"completed first. its exact invoice generated-key/unique baseline",
		"full utf8mb4 varchar(255) index needs 1020 bytes",
		"stop every w_order writer",
		"same physical mysql session",
		"stop on the first error",
		"no update, delete or business-data backfill",
		"legacy unpaid stripe orders are also rejected",
		"sole persistent statement is one mysql 8 atomic alter table",
		"zero target definitions",
		"any partial, renamed-equivalent, prefix-index",
		"never retry the alter blindly",
	} {
		if !strings.Contains(sql, safetyNote) {
			t.Errorf("migration safety notes missing %q", safetyNote)
		}
	}

	if regexp.MustCompile(`(?i)\bupdate\s+`+"`?w_order").MatchString(string(raw)) ||
		regexp.MustCompile(`(?i)\bdelete\s+from\s+`+"`?w_order").MatchString(string(raw)) ||
		regexp.MustCompile(`(?i)\binsert\s+into\s+`+"`?w_order(?:`|\\s)").MatchString(string(raw)) {
		t.Fatal("migration must not rewrite, delete or synthesize order rows")
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}

func TestOrderCheckoutBillingSQLiteMirrorUsesBinaryTrimmedSessionIdentity(t *testing.T) {
	db := testutil.NewTestDB(t)
	for i, sessionID := range []string{"", "", "   "} {
		if err := db.Exec(
			"INSERT INTO w_order (no, checkout_session_id) VALUES (?, ?)",
			fmt.Sprintf("checkout-blank-%d", i), sessionID,
		).Error; err != nil {
			t.Fatalf("insert compatible blank checkout session %d: %v", i, err)
		}
	}
	if err := db.Exec(
		"INSERT INTO w_order (no, checkout_session_id) VALUES (?, ?)",
		"checkout-tab-is-identity", "\t",
	).Error; err == nil {
		t.Fatal("tab-only checkout identity without provider price bypassed invariant")
	}
	if err := db.Exec(
		"INSERT INTO w_order (no, checkout_session_id, provider_price_id) VALUES (?, ?, ?)",
		"checkout-first", " cs_exact ", "price_one",
	).Error; err != nil {
		t.Fatalf("insert first normalized checkout session: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO w_order (no, checkout_session_id, provider_price_id) VALUES (?, ?, ?)",
		"checkout-duplicate", "cs_exact", "price_one",
	).Error; err == nil {
		t.Fatal("trim-equivalent nonempty checkout session bypassed generated UNIQUE key")
	}
	if err := db.Exec(
		"INSERT INTO w_order (no, checkout_session_id, provider_price_id) VALUES (?, ?, ?)",
		"checkout-case-distinct", "CS_EXACT", "price_one",
	).Error; err != nil {
		t.Fatalf("binary-distinct checkout session should remain distinct: %v", err)
	}
}

func TestOrderCheckoutBillingDataGuardsClassifyInvalidLegacyFacts(t *testing.T) {
	db := testutil.NewTestDB(t)
	// Use an intentionally unconstrained legacy fixture table. The shared
	// w_order mirror carries the post-migration CHECK constraints, which should
	// reject these rows at write time; the preflight still needs proof that it
	// classifies the same bad history before production DDL.
	if err := db.Exec(`
		CREATE TABLE w_order_checkout_legacy_guard_fixture (
			no TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '0',
			pay_method TEXT NOT NULL DEFAULT '0',
			checkout_session_id TEXT NOT NULL DEFAULT '',
			provider_price_id TEXT NOT NULL DEFAULT '',
			billing_period_start DATETIME,
			billing_period_end DATETIME
		)`).Error; err != nil {
		t.Fatalf("create unconstrained legacy guard fixture: %v", err)
	}
	fixtures := []struct {
		no, sessionID, priceID string
		periodStart, periodEnd any
	}{
		{no: "valid-empty"},
		{no: "valid-provider", sessionID: "cs_valid", priceID: "price_valid", periodStart: "2026-08-01 00:00:00", periodEnd: "2026-09-01 00:00:00"},
		{no: "missing-price", sessionID: "cs_missing_price"},
		{no: "missing-end", periodStart: "2026-08-01 00:00:00"},
		{no: "missing-start", periodEnd: "2026-09-01 00:00:00"},
		{no: "equal-period", periodStart: "2026-08-01 00:00:00", periodEnd: "2026-08-01 00:00:00"},
		{no: "reverse-period", periodStart: "2026-09-01 00:00:00", periodEnd: "2026-08-01 00:00:00"},
	}
	for _, fixture := range fixtures {
		if err := db.Exec(`
			INSERT INTO w_order_checkout_legacy_guard_fixture
				(no, checkout_session_id, provider_price_id, billing_period_start, billing_period_end)
			VALUES (?, ?, ?, ?, ?)`,
			fixture.no, fixture.sessionID, fixture.priceID, fixture.periodStart, fixture.periodEnd,
		).Error; err != nil {
			t.Fatalf("insert %s: %v", fixture.no, err)
		}
	}

	var incompatible int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM w_order_checkout_legacy_guard_fixture
		WHERE (billing_period_start IS NULL) <> (billing_period_end IS NULL)
		   OR (billing_period_start IS NOT NULL
		       AND billing_period_end IS NOT NULL
		       AND billing_period_start >= billing_period_end)`).Scan(&incompatible).Error; err != nil {
		t.Fatalf("evaluate billing-pair guard mirror: %v", err)
	}
	if incompatible != 4 {
		t.Fatalf("billing-pair guard incompatible rows = %d, want 4", incompatible)
	}

	if err := db.Raw(`
		SELECT COUNT(*)
		FROM w_order_checkout_legacy_guard_fixture
		WHERE NULLIF(TRIM(checkout_session_id), '') IS NOT NULL
		  AND NULLIF(TRIM(provider_price_id), '') IS NULL`).Scan(&incompatible).Error; err != nil {
		t.Fatalf("evaluate provider-price guard mirror: %v", err)
	}
	if incompatible != 1 {
		t.Fatalf("provider-price guard incompatible rows = %d, want 1", incompatible)
	}

	if err := db.Exec(`
		INSERT INTO w_order_checkout_legacy_guard_fixture (no, status, pay_method)
		VALUES ('legacy-unpaid-stripe', 'UNPAID', 'stripe')`).Error; err != nil {
		t.Fatalf("insert legacy unpaid Stripe fixture: %v", err)
	}
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM w_order_checkout_legacy_guard_fixture
		WHERE UPPER(TRIM(status)) = 'UNPAID'
		  AND LOWER(TRIM(pay_method)) = 'stripe'`).Scan(&incompatible).Error; err != nil {
		t.Fatalf("evaluate legacy unpaid Stripe guard mirror: %v", err)
	}
	if incompatible != 1 {
		t.Fatalf("legacy unpaid Stripe guard incompatible rows = %d, want 1", incompatible)
	}
}

func TestOrderCheckoutBillingSQLiteContractRejectsFutureInvariantViolations(t *testing.T) {
	db := testutil.NewTestDB(t)
	if err := db.Exec(`
		CREATE TABLE w_order_checkout_billing_contract (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_price_id TEXT COLLATE BINARY NOT NULL DEFAULT '',
			billing_period_start DATETIME,
			billing_period_end DATETIME,
			checkout_session_id TEXT COLLATE BINARY NOT NULL DEFAULT '',
			checkout_session_idempotency_key TEXT COLLATE BINARY
				GENERATED ALWAYS AS (NULLIF(TRIM(checkout_session_id), '')) STORED,
			UNIQUE (checkout_session_idempotency_key),
			CHECK (
				(billing_period_start IS NULL AND billing_period_end IS NULL)
				OR
				(billing_period_start IS NOT NULL
				 AND billing_period_end IS NOT NULL
				 AND billing_period_start < billing_period_end)
			),
			CHECK (
				NULLIF(TRIM(checkout_session_id), '') IS NULL
				OR NULLIF(TRIM(provider_price_id), '') IS NOT NULL
			)
		)`).Error; err != nil {
		t.Fatalf("create SQLite checkout/billing contract mirror: %v", err)
	}

	for _, statement := range []struct {
		name      string
		sessionID string
		priceID   string
		start     any
		end       any
		wantError bool
	}{
		{name: "blank checkout and period"},
		{name: "complete provider facts", sessionID: "cs_complete", priceID: "price_complete", start: "2026-08-01 00:00:00", end: "2026-09-01 00:00:00"},
		{name: "session without price", sessionID: "cs_no_price", wantError: true},
		{name: "space-only price", sessionID: "cs_blank_price", priceID: "   ", wantError: true},
		{name: "start without end", start: "2026-08-01 00:00:00", wantError: true},
		{name: "end without start", end: "2026-09-01 00:00:00", wantError: true},
		{name: "equal period", start: "2026-08-01 00:00:00", end: "2026-08-01 00:00:00", wantError: true},
		{name: "reverse period", start: "2026-09-01 00:00:00", end: "2026-08-01 00:00:00", wantError: true},
	} {
		err := db.Exec(`
			INSERT INTO w_order_checkout_billing_contract
				(provider_price_id, billing_period_start, billing_period_end, checkout_session_id)
			VALUES (?, ?, ?, ?)`,
			statement.priceID, statement.start, statement.end, statement.sessionID,
		).Error
		if statement.wantError && err == nil {
			t.Errorf("%s: invariant-violating insert succeeded", statement.name)
		}
		if !statement.wantError && err != nil {
			t.Errorf("%s: valid insert failed: %v", statement.name, err)
		}
	}
}
