-- T4 — anchor facet split via appearance_hash.
--
-- Problem before this migration: stale-shot detection compared
-- character/location anchors_version (a monolithic monotonic counter
-- bumped on EVERY calibration apply, including identity-only edits
-- like personality or role-type tweaks). A user who fixed a single
-- character's backstory would see ALL panels referencing that
-- character flagged as "stale", even though the visual rendering
-- would be identical. Result: the amber stale-banner became a "wolf
-- cried" signal that users learned to ignore.
--
-- Fix: alongside anchors_version, store appearance_hash — a 16-hex-
-- char digest of the visual-only subset (appearance text +
-- identity_anchors + negative_anchors). The stale detector prefers
-- hash equality; on a match it explicitly suppresses the stale
-- flag even when versions diverged.
--
-- Backwards-compatible: legacy rows have empty appearance_hash and
-- the detector falls back to version comparison for them (current
-- behaviour). Backfill is intentionally lazy — calibration apply
-- and any future write path computes the hash, so over time the
-- table becomes hash-eligible without a one-shot UPDATE.

ALTER TABLE `w_drama_character`
  ADD COLUMN `appearance_hash` CHAR(16) NOT NULL DEFAULT ''
    COMMENT 'T4 视觉特征摘要; 影响 stale-shot 判定; identity-only 修改不会变';

ALTER TABLE `w_drama_location`
  ADD COLUMN `appearance_hash` CHAR(16) NOT NULL DEFAULT ''
    COMMENT 'T4 视觉特征摘要';
