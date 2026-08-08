-- P2 — character-family team scoping.
--
-- Problem: w_drama_character_family was uid-only. Two team members
-- working on the same IP would each maintain their own copy of the
-- canonical character anchors — the family table was supposed to be
-- the cross-project identity keeper, but cross-USER (within one
-- team) was missing. Result: drift between team members, since each
-- import could pick up slightly different anchor calibrations.
--
-- Fix: add team_id; mirrors the SD-S2.2 template-share pattern.
-- nil team_id = solo-owner family (legacy + default). Non-nil =
-- visible + importable by every active member of that team. Edits
-- still owner-only — sharing is read-side, not collaborative-edit.
--
-- Index supports the list query "give me families I own OR families
-- shared with my teams" without a full scan.

ALTER TABLE `w_drama_character_family`
  ADD COLUMN `team_id` BIGINT UNSIGNED DEFAULT NULL
    COMMENT 'P2 可选团队共享id; NULL=仅 owner 可见';

ALTER TABLE `w_drama_character_family`
  ADD INDEX `idx_family_team` (`team_id`);
