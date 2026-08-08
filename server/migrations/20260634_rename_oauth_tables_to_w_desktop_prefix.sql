-- Compatibility bridge for installations that applied an early revision of
-- 20260633, where the three OAuth Authorization Server tables used bare names.
-- The repository's current 20260633 already creates `w_desktop_oauth_*`, so a
-- fresh sequential migration MUST treat this file as an idempotent no-op.
--
-- Why `w_desktop_*` (vs `w_oauth_*`)
--   These tables today serve only the workmax-desktop client; the rest
--   of the desktop subtree (HTTP routes /api/desktop/*, Go packages
--   under server/{api,service,model,router}/desktop/) already uses
--   the desktop_ infix. Tables follow suit. If a future use case
--   extends OAuth to third-party clients we'd rename / generalize
--   in a follow-up migration.
--
-- Supported entry states:
--   A. all three legacy bare tables exist and no target table exists: rename;
--   B. all three target tables exist and no legacy table exists: no-op.
-- Any partial/mixed state deliberately executes an impossible table rename so
-- the operator gets a hard migration failure instead of a split OAuth schema.
--
-- Rollback (within deploy window — drop the new code, revert this
-- migration, restart on the previous build):
--
--   ALTER TABLE `w_desktop_oauth_refresh_token`
--       RENAME INDEX `uk_w_desktop_oauth_refresh_token_token` TO `uk_oauth_refresh_token_token`,
--       RENAME INDEX `idx_w_desktop_oauth_refresh_token_chain` TO `idx_oauth_refresh_token_chain`,
--       RENAME INDEX `idx_w_desktop_oauth_refresh_token_uid_device` TO `idx_oauth_refresh_token_uid_device`,
--       RENAME INDEX `idx_w_desktop_oauth_refresh_token_uid_chain` TO `idx_oauth_refresh_token_uid_chain`,
--       RENAME INDEX `idx_w_desktop_oauth_refresh_token_revoked_expires` TO `idx_oauth_refresh_token_revoked_expires`;
--   ALTER TABLE `w_desktop_oauth_authorization_code`
--       RENAME INDEX `uk_w_desktop_oauth_auth_code_code` TO `uk_oauth_auth_code_code`,
--       RENAME INDEX `idx_w_desktop_oauth_auth_code_expires_at` TO `idx_oauth_auth_code_expires_at`;
--   ALTER TABLE `w_desktop_oauth_client`
--       RENAME INDEX `uk_w_desktop_oauth_client_client_id` TO `uk_oauth_client_client_id`;
--   RENAME TABLE
--       `w_desktop_oauth_refresh_token`      TO `oauth_refresh_token`,
--       `w_desktop_oauth_authorization_code` TO `oauth_authorization_code`,
--       `w_desktop_oauth_client`             TO `oauth_client`;

SET @workmax_schema_name = DATABASE();

SELECT COUNT(*) INTO @workmax_oauth_legacy_table_count
FROM information_schema.tables
WHERE table_schema = @workmax_schema_name
  AND table_name IN ('oauth_client', 'oauth_authorization_code', 'oauth_refresh_token');

SELECT COUNT(*) INTO @workmax_oauth_target_table_count
FROM information_schema.tables
WHERE table_schema = @workmax_schema_name
  AND table_name IN (
      'w_desktop_oauth_client',
      'w_desktop_oauth_authorization_code',
      'w_desktop_oauth_refresh_token'
  );

SET @workmax_oauth_bridge_sql = CASE
    WHEN @workmax_oauth_legacy_table_count = 3 AND @workmax_oauth_target_table_count = 0 THEN
        'RENAME TABLE `oauth_client` TO `w_desktop_oauth_client`, `oauth_authorization_code` TO `w_desktop_oauth_authorization_code`, `oauth_refresh_token` TO `w_desktop_oauth_refresh_token`'
    WHEN @workmax_oauth_legacy_table_count = 0 AND @workmax_oauth_target_table_count = 3 THEN
        'SELECT ''desktop OAuth table names already current'' AS migration_status'
    ELSE
        -- This source table is intentionally impossible. PREPARE succeeds but
        -- EXECUTE fails, making partial/mixed schemas fail closed.
        'RENAME TABLE `__workmax_invalid_desktop_oauth_schema_state__` TO `__workmax_desktop_oauth_migration_aborted__`'
END;

PREPARE workmax_oauth_bridge_stmt FROM @workmax_oauth_bridge_sql;
EXECUTE workmax_oauth_bridge_stmt;
DEALLOCATE PREPARE workmax_oauth_bridge_stmt;

-- Index names do not participate in runtime lookups and are scoped per table.
-- A bridged legacy installation may retain its historical index names; fresh
-- installations already have the canonical names from current 20260633.
