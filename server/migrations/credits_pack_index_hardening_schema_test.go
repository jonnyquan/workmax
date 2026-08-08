package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const creditsPackIndexHardeningMigrationFile = "20260809_harden_credits_pack_indexes.sql"

func TestCreditsPackIndexHardeningMigrationFailsClosedBeforeAtomicDDL(t *testing.T) {
	raw, err := os.ReadFile(creditsPackIndexHardeningMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", creditsPackIndexHardeningMigrationFile, err)
	}
	sql := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))

	for _, want := range []string{
		"create temporary table `_w_credits_pack_version_guard`",
		"primary key (`guard_key`)",
		"insert into `_w_credits_pack_version_guard` (`guard_key`) values (0)",
		"regexp_substr(version(), '^[0-9]+[.][0-9]+[.][0-9]+')",
		"locate('mariadb', lower(version())) = 0",
		"@w_credits_pack_version_core is not null",
		"@w_credits_pack_version_major > 8",
		"@w_credits_pack_version_major = 8",
		"@w_credits_pack_version_minor > 0",
		"@w_credits_pack_version_minor = 0",
		"@w_credits_pack_version_patch >= 19",
		"drop temporary table `_w_credits_pack_version_guard`",
		"create temporary table `_w_credits_pack_schema_guard`",
		"constraint `chk_w_credits_pack_schema_guard` check (`incompatible_rows` = 0)",
		"from `information_schema`.`tables`",
		"`table_type` = 'base table'",
		"upper(`engine`) = 'innodb'",
		"from `information_schema`.`partitions`",
		"`partition_name` is not null",
		"from `information_schema`.`columns`",
		"`column_name` = 'id' and `data_type` = 'bigint' and `is_nullable` = 'no' and `column_key` = 'pri'",
		"lower(`extra`) like '%auto_increment%'",
		"`column_name` = 'uid' and `data_type` = 'bigint' and `is_nullable` = 'no'",
		"`column_name` = 'source_type' and `data_type` in ('char', 'varchar') and `is_nullable` = 'no' and `character_maximum_length` = 50 and `character_set_name` = 'utf8mb4'",
		"`column_name` = 'source_id' and `data_type` in ('char', 'varchar') and `is_nullable` = 'no' and `character_maximum_length` = 64 and `character_set_name` = 'utf8mb4'",
		"`column_name` in ('credits_total', 'credits_used') and `data_type` = 'bigint' and `is_nullable` = 'no'",
		"coalesce(sum(`character_octet_length`), 3073)",
		"<= 3064",
		"create temporary table `_w_credits_pack_amount_guard`",
		"constraint `chk_w_credits_pack_amount_guard` check (`incompatible_rows` = 0)",
		"from `w_credits_pack` where `uid` is null or `source_type` is null or `source_id` is null or `credits_total` is null or `credits_used` is null or `credits_total` < 0 or `credits_used` < 0 or `credits_used` > `credits_total` or nullif(trim(`source_type`), '') is null or nullif(trim(`source_id`), '') is null",
		"create temporary table `_w_credits_pack_duplicate_guard`",
		"constraint `chk_w_credits_pack_duplicate_guard` check (`incompatible_rows` = 0)",
		"select `uid`, binary `source_type` as `source_type_binary`, binary `source_id` as `source_id_binary` from `w_credits_pack` group by `uid`, binary `source_type`, binary `source_id` having count(*) > 1",
		"create temporary table `_w_credits_pack_index_fingerprint_guard`",
		"constraint `chk_w_credits_pack_index_fingerprint_guard` check (`incompatible_rows` = 0)",
		"from `information_schema`.`statistics`",
		"'idx_w_credits_pack_uid_id', 'uk_w_credits_pack_source_identity'",
		"group_concat(`column_name` order by `seq_in_index` separator ',') as `ordered_columns`",
		"`ordered_columns` = 'uid,id'",
		"`ordered_columns` = 'uid,source_type,source_id'",
		"alter table `w_credits_pack` modify column `source_type` varchar(50) character set utf8mb4 collate utf8mb4_bin not null",
		"modify column `source_id` varchar(64) character set utf8mb4 collate utf8mb4_bin not null",
		"add key `idx_w_credits_pack_uid_id` (`uid`, `id`), add unique key `uk_w_credits_pack_source_identity` (`uid`, `source_type`, `source_id`), add constraint `chk_w_credits_pack_source_identity_canonical`",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("migration missing %q", want)
		}
	}

	guardInserts := []string{
		"insert into `_w_credits_pack_version_guard` (`guard_key`) select case",
		"insert into `_w_credits_pack_schema_guard`",
		"insert into `_w_credits_pack_amount_guard`",
		"insert into `_w_credits_pack_duplicate_guard`",
		"insert into `_w_credits_pack_index_fingerprint_guard`",
	}
	alter := strings.Index(sql, "alter table `w_credits_pack`")
	if alter < 0 {
		t.Fatal("migration must contain the persistent w_credits_pack ALTER")
	}
	for _, guard := range guardInserts {
		pos := strings.Index(sql, guard)
		if pos < 0 || pos >= alter {
			t.Fatalf("guard %q must execute before the first persistent ALTER", guard)
		}
	}
	guardDrops := []string{
		"drop temporary table `_w_credits_pack_version_guard`",
		"drop temporary table `_w_credits_pack_schema_guard`",
		"drop temporary table `_w_credits_pack_amount_guard`",
		"drop temporary table `_w_credits_pack_duplicate_guard`",
		"drop temporary table `_w_credits_pack_index_fingerprint_guard`",
	}
	for _, drop := range guardDrops {
		pos := strings.Index(sql, drop)
		if pos < 0 || pos >= alter {
			t.Fatalf("guard cleanup %q must execute before the persistent ALTER", drop)
		}
	}
	if strings.Contains(sql[:alter], "create table ") {
		t.Fatal("migration must not create a persistent table before compatibility guards finish")
	}
	versionGuard := strings.Index(sql, "insert into `_w_credits_pack_version_guard` (`guard_key`) select case")
	schemaGuard := strings.Index(sql, "insert into `_w_credits_pack_schema_guard`")
	if versionGuard < 0 || schemaGuard < 0 || versionGuard >= schemaGuard {
		t.Fatal("mechanical MySQL version gate must complete before CHECK-based schema guards")
	}
	versionCreate := strings.Index(sql, "create temporary table `_w_credits_pack_version_guard`")
	versionDrop := strings.Index(sql, "drop temporary table `_w_credits_pack_version_guard`")
	if versionCreate < 0 || versionDrop <= versionCreate || versionDrop >= schemaGuard {
		t.Fatal("version sentinel must be self-contained and dropped before schema guards")
	}
	versionBlock := sql[versionCreate:versionDrop]
	if strings.Contains(versionBlock, " check ") || strings.Contains(versionBlock, "constraint ") {
		t.Fatal("version gate must not rely on CHECK-constraint enforcement")
	}
	if strings.Count(versionBlock, "insert into `_w_credits_pack_version_guard`") != 2 ||
		!strings.Contains(versionBlock, "then 1 else 0 end") {
		t.Fatal("version gate must seed 0 and map compatibility to 1 / rejection to duplicate 0")
	}
	if strings.Count(sql, "alter table `w_credits_pack`") != 1 {
		t.Fatal("both indexes must be published by one atomic MySQL 8 ALTER")
	}
	if strings.Index(sql[alter:], "add unique key `uk_w_credits_pack_source_identity`") <
		strings.Index(sql[alter:], "add key `idx_w_credits_pack_uid_id`") {
		t.Fatal("owner lock index must be declared before source identity UNIQUE in the atomic ALTER")
	}

	for _, safetyNote := range []string{
		"oracle mysql 8.0.19+ only",
		"duplicate-primary-key sentinel",
		"does not depend on check enforcement",
		"rejects mariadb and versions older than 8.0.19 mechanically",
		"mysql 5.7 also fails closed at regexp_substr",
		"stop all creditspack writers",
		"same physical mysql session",
		"stop on the first error",
		"no update, backfill or delete",
		"partial-ddl fingerprint",
		"one alter table that adds both indexes",
		"any one-index",
	} {
		if !strings.Contains(sql, safetyNote) {
			t.Errorf("migration safety notes missing %q", safetyNote)
		}
	}

	if regexp.MustCompile(`(?i)\bupdate\s+`+"`?w_credits_pack").MatchString(string(raw)) ||
		regexp.MustCompile(`(?i)\bdelete\s+from\s+`+"`?w_credits_pack").MatchString(string(raw)) ||
		regexp.MustCompile(`(?i)\binsert\s+into\s+`+"`?w_credits_pack(?:`|\\s)").MatchString(string(raw)) {
		t.Fatal("migration must not rewrite, delete or synthesize CreditsPack rows")
	}

	identifierPattern := regexp.MustCompile("(?i)(?:constraint|(?:unique )?key) `([^`]+)`")
	for _, match := range identifierPattern.FindAllStringSubmatch(string(raw), -1) {
		if len(match[1]) > 64 {
			t.Fatalf("MySQL identifier %q is %d bytes, want <= 64", match[1], len(match[1]))
		}
	}
}
