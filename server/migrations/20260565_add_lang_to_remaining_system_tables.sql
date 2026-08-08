-- Adds the lang column to the remaining 5 system-content-eligible
-- tables, getting the platform onto a uniform DB-side i18n footprint.
--
-- These 5 tables share three properties: (a) the model declares a
-- system-row sentinel (is_system=1 on the templates, uid=0+project_id
-- IS NULL on character/location), (b) they have user-facing text
-- columns (name + description), (c) Phase 2 hasn't seeded system
-- content yet but the architecture is ready for it.
--
-- Rather than wait for content to land table-by-table, this migration
-- gets the schema in place now so a future seed PR is purely data —
-- no schema dance, no handler refactor, no testdb mirror update.
--
-- Tables touched:
--   - w_ad_template       (Phase 2, no system seeds yet)
--   - w_comic_template    (Phase 2, no system seeds yet)
--   - w_ecom_template     (Phase 2, no system seeds yet)
--   - w_drama_character   (production rows exist but only user/project,
--                          no system slugs; existing rows get lang='en'
--                          as informational metadata)
--   - w_drama_location    (same as drama_character)
--
-- Two prep steps for the 3 template tables only:
--   They inherited a slug-only `uk_slug` UNIQUE index from the GORM
--   model (`uniqueIndex:uk_slug`), which conflicts with the lang
--   fan-out pattern (one row per (slug, lang) means slugs must NOT be
--   uniquely constrained on their own). Same bug shape as the one
--   20260560a fixed for w_drama_template — handled here in one shot
--   for the remaining adopters so a future seed migration can just
--   INSERT freely. drama_character / drama_location never had this
--   constraint, so no prep needed for them.
--
-- All steps use INFORMATION_SCHEMA guards so re-runs are no-ops.

-- ─── Helpers via local variables (one block per step) ──────────────

-- ── Step 1a: w_ad_template — drop uk_slug if present ───────────────
SET @uk_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ad_template' AND INDEX_NAME = 'uk_slug');
SET @ddl := IF(@uk_exists > 0,
  'ALTER TABLE `w_ad_template` DROP INDEX `uk_slug`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 1b: w_ad_template — add idx_slug if absent ────────────────
SET @idx_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ad_template' AND INDEX_NAME = 'idx_slug');
SET @ddl := IF(@idx_exists = 0,
  'ALTER TABLE `w_ad_template` ADD INDEX `idx_slug` (`slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 2a: w_comic_template — drop uk_slug if present ────────────
SET @uk_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_comic_template' AND INDEX_NAME = 'uk_slug');
SET @ddl := IF(@uk_exists > 0,
  'ALTER TABLE `w_comic_template` DROP INDEX `uk_slug`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 2b: w_comic_template — add idx_slug if absent ─────────────
SET @idx_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_comic_template' AND INDEX_NAME = 'idx_slug');
SET @ddl := IF(@idx_exists = 0,
  'ALTER TABLE `w_comic_template` ADD INDEX `idx_slug` (`slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 3a: w_ecom_template — drop uk_slug if present ─────────────
SET @uk_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ecom_template' AND INDEX_NAME = 'uk_slug');
SET @ddl := IF(@uk_exists > 0,
  'ALTER TABLE `w_ecom_template` DROP INDEX `uk_slug`',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 3b: w_ecom_template — add idx_slug if absent ──────────────
SET @idx_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ecom_template' AND INDEX_NAME = 'idx_slug');
SET @ddl := IF(@idx_exists = 0,
  'ALTER TABLE `w_ecom_template` ADD INDEX `idx_slug` (`slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ─── Now the lang column for all 5 tables ──────────────────────────

-- ── Step 4: w_ad_template — add lang ───────────────────────────────
SET @col_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ad_template' AND COLUMN_NAME = 'lang');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_ad_template`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 5: w_comic_template — add lang ────────────────────────────
SET @col_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_comic_template' AND COLUMN_NAME = 'lang');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_comic_template`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 6: w_ecom_template — add lang ─────────────────────────────
SET @col_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_ecom_template' AND COLUMN_NAME = 'lang');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_ecom_template`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 7: w_drama_character — add lang ───────────────────────────
SET @col_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_drama_character' AND COLUMN_NAME = 'lang');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_drama_character`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- ── Step 8: w_drama_location — add lang ────────────────────────────
SET @col_exists := (SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'w_drama_location' AND COLUMN_NAME = 'lang');
SET @ddl := IF(@col_exists = 0,
  'ALTER TABLE `w_drama_location`
     ADD COLUMN `lang` VARCHAR(16) NOT NULL DEFAULT ''en'' AFTER `slug`,
     ADD INDEX `idx_lang_slug` (`lang`, `slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
