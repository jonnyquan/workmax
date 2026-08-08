-- Drops the slug-only UNIQUE index on w_drama_template that conflicts
-- with the DB-side i18n pattern (one row per (slug, lang)).
--
-- Filename note: numbered 20260560a (between 20260560 and 20260561) so
-- lexical-sort runners apply it BEFORE 20260561. Authored after the
-- bug it fixes was found live; the filename is its execution order,
-- not its authoring order.
--
-- Why this is needed:
--   The original GORM model declared `Slug` with `uniqueIndex:uk_slug`,
--   creating UNIQUE (slug). Migration 20260561 adds the lang column
--   and inserts per-locale rows for the 5 system slugs — without this
--   prep migration those INSERTs hit duplicate-key errors and silently
--   drop in `mysql < file` batch mode (errors don't abort the script).
--   First dev run produced 5 zh rows but no en rows, breaking the
--   lang-fallback chain for every non-zh user.
--
-- Why the fix is "drop the unique" (not "make it (slug, lang)"):
--   director-preset (the pilot) uses a plain non-unique idx_slug and
--   relies on application-layer dedupe + migration WHERE NOT EXISTS for
--   integrity. Adopting the same posture here keeps the two adopters
--   symmetric and lets users override system slugs with personal ones
--   (the same shadowing semantic the picker already supports).
--
-- Idempotency:
--   Both DROP and ADD guarded by INFORMATION_SCHEMA checks so re-runs
--   are no-ops. Existing rows are untouched.

-- ─── Step 1: drop UNIQUE INDEX uk_slug if present ───────────────────
SET @uk_exists := (
  SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'w_drama_template'
    AND INDEX_NAME = 'uk_slug'
);
SET @ddl := IF(@uk_exists > 0,
  'ALTER TABLE `w_drama_template` DROP INDEX `uk_slug`',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

-- ─── Step 2: add non-unique INDEX idx_slug if absent ────────────────
SET @idx_exists := (
  SELECT COUNT(*) FROM INFORMATION_SCHEMA.STATISTICS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'w_drama_template'
    AND INDEX_NAME = 'idx_slug'
);
SET @ddl := IF(@idx_exists = 0,
  'ALTER TABLE `w_drama_template` ADD INDEX `idx_slug` (`slug`)',
  'SELECT 1');
PREPARE stmt FROM @ddl;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;
