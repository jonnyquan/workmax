-- U2 — record per-take script generation_version on panel-shot rows.
--
-- Problem before this migration: the storyboard-level drift signal
-- (storyboard.script_version vs script.generation_version) tells you
-- "the script changed since the storyboard was generated", but every
-- panel shot under that storyboard inherits the SAME drift state. A
-- user reviewing one specific take (e.g. an 👍'd hero shot) couldn't
-- tell whether THAT take was rendered against the current script or
-- against an older revision the storyboard was already resynced past.
--
-- Fix: stamp `script_generation_version` on the shot at INSERT time.
-- The drift detector and the per-take audit can now answer:
--   - "shot.script_generation_version < script.generation_version"
--     → THIS take is stale, even if the storyboard appears in-sync.
--   - "shot.script_generation_version == 0" → legacy row, fall back
--     to the storyboard-level signal (existing behaviour).
--
-- Backward-compatible: existing rows default to 0 (= unknown). The UI
-- treats 0 as "trust the storyboard-level signal" so legacy data
-- doesn't suddenly look stale.

ALTER TABLE `w_drama_panel_shot`
  ADD COLUMN `script_generation_version` INT NOT NULL DEFAULT 0
    COMMENT 'U2 渲染时脚本generation_version快照; 0=未知/legacy';
