-- 20260601_rename_drama_character_to_character.sql
--
-- Rename the two surviving "drama_character" tables to drop the now-
-- misleading "drama_" prefix. They were originally created when
-- characters were a short-drama-only feature, but they've long since
-- become the cross-cutting character asset system used by the canvas
-- @-mention pipeline.
--
-- Companion change: the Go model types DramaCharacter / DramaCharacter*
-- constants are renamed to Character / Character* in the same commit.
-- Run this migration before deploying the new server build, otherwise
-- the GORM models will look for tables that no longer exist.
--
-- Idempotency: MySQL's RENAME TABLE has no IF EXISTS form, so this
-- migration must run exactly once. Re-running on an already-renamed
-- DB will surface a 1146 (table doesn't exist) error from the source
-- side — that's the expected sentinel that the rename already
-- happened.
--
-- Rollback: swap the source/destination of each statement.

RENAME TABLE `w_drama_character` TO `w_character`;
RENAME TABLE `w_drama_character_reference` TO `w_character_reference`;
