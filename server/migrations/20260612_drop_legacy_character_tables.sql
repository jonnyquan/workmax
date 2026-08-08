-- Drop the legacy w_character family — Sprint-E sunset migration.
--
-- These tables predate the Sprint-E platform-level asset library:
--
--   w_character             — drama-era character asset, created
--                             by 20260417_create_character_assets.sql
--                             (originally w_drama_character, renamed
--                             by 20260601). Superseded by
--                             w_global_character (created by
--                             20260611_create_platform_character.sql).
--   w_character_reference   — sibling reference table, same
--                             lineage. Superseded by
--                             w_global_character_reference.
--   w_character_relationship — drama-era character relationship
--                             graph. Zero Go-code callers post-
--                             vertical-retirement (2026-05-07);
--                             dropped as dead schema.
--
-- Why a sunset migration: fresh environments running the full
-- migration trail would otherwise end up with both w_character
-- (created by 20260417, renamed by 20260601) AND w_global_character
-- (created by 20260611), with the legacy table empty and
-- orphaned. This DROP cleans the duplicate.
--
-- DROP TABLE IF EXISTS makes the migration safely re-runnable. In
-- environments that already cleaned the tables manually (the
-- user's local DB), this is a no-op.

DROP TABLE IF EXISTS `w_character_relationship`;
DROP TABLE IF EXISTS `w_character_reference`;
DROP TABLE IF EXISTS `w_character`;
