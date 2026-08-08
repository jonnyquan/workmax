package migrations

import (
	"os"
	"strings"
	"testing"
)

const desktopLoginTransactionMigrationFile = "20260672_create_desktop_login_transaction.sql"

func TestDesktopLoginTransactionMigrationPinsSecurityBoundary(t *testing.T) {
	raw, err := os.ReadFile(desktopLoginTransactionMigrationFile)
	if err != nil {
		t.Fatalf("read %s: %v", desktopLoginTransactionMigrationFile, err)
	}
	sql := strings.ToLower(strings.Join(strings.Fields(string(raw)), " "))
	for _, want := range []string{
		"create table if not exists `w_desktop_login_transaction`",
		"`transaction_id` varchar(64) character set ascii collate ascii_bin not null",
		"`secret_hash` binary(32) not null",
		"`oauth_state_digest` binary(32) not null",
		"`oauth_state_ciphertext` varchar(1536) character set ascii collate ascii_bin not null",
		"`provider_state_digest` binary(32) default null",
		"`provider_pkce_ciphertext` varchar(1024) character set ascii collate ascii_bin default null",
		"`failed_attempts` smallint unsigned not null default 0",
		"`last_failed_at` datetime(6) default null",
		"unique key `uk_w_desktop_login_tx_transaction` (`transaction_id`)",
		"unique key `uk_w_desktop_login_tx_secret` (`secret_hash`)",
		"unique key `uk_w_desktop_login_tx_provider_state` (`provider_state_digest`)",
		"key `idx_w_desktop_login_tx_status_expiry` (`status`, `expires_at`)",
		"check (`code_challenge_method` = 's256')",
		"check (`failed_attempts` <= 5)",
		"alter table `w_desktop_oauth_authorization_code` add column `device_id`",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration is missing %q", want)
		}
	}
	for _, forbidden := range []string{"`transaction_secret`", "`password`", "`provider_code`", "`access_token`", "`refresh_token`"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration must not persist %s", forbidden)
		}
	}
}
