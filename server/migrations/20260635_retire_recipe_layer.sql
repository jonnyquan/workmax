-- Retire the Recipe layer (2026-05-18).
--
-- Background: Recipe started as the "vertical orchestration layer"
-- on top of the general work-agent tools. After audit, every
-- surface Recipe was designed for (system-prompt injection,
-- stage / pipeline projection, asset-hints, discovery form) was
-- already covered by skills/<agent_mode>/SKILL.md + skill.yaml —
-- Recipe was effectively a weaker duplicate.
--
-- The Recipe v1 binding (thread.recipe_id) and v2 derivation
-- (message.stage_tag + the w_global_recipe table) are dropped
-- here. Concrete contents that were unique to the single live
-- recipe (social-ad-vertical-5s: 9:16 / 5s / hook / 3-5 cuts /
-- sound-off / CTA-last-second) moved into
-- skills/socialAd/SKILL.md §"视频变体" in the same commit so the
-- model still reads those constraints — they just arrive via the
-- skill body rather than a <recipe-context> XML block.
--
-- The retired implementation and this migration retain the relevant
-- design history.
--
-- ----------------------------------------------------------------
-- 1) Drop the thread→recipe binding column. Was always optional
-- (default ''), no production rows are known to depend on it.
ALTER TABLE `w_workagent_thread`
  DROP COLUMN `recipe_id`;

-- 2) Drop the v2 stage_tag derivation column + its index. Column
-- defaulted to '' on every row; the in-memory v2 registry was
-- never DB-backed so no historical tagging existed beyond what
-- AppendToolOutputMarker wrote at runtime — which always derived
-- empty for v1 recipes (the only kind ever bound in production).
ALTER TABLE `w_workagent_message`
  DROP KEY `idx_message_thread_stage`,
  DROP COLUMN `stage_tag`;

-- 3) Drop the global recipe catalog table. Created in
-- migration 20260624; was never populated in production
-- (registry stayed in-process via init() blocks). The v2
-- config_json column added in 20260631 disappears with the table.
DROP TABLE IF EXISTS `w_global_recipe`;
