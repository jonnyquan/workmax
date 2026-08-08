-- M5-B-06 Retire: drop w_canvas_version + project.latest_version_id
-- column. After Stage 2 dual-write soaked + Retire-T1..T4 removed all
-- Go readers/writers + frontend consumers of these structures, the
-- table and column are dead. This migration completes the retirement
-- announced in canvas-version-restructure-design.md.
--
-- No production data migration: per the project owner, historical
-- canvas data is dev-only and acceptably lost. Stage 3 (backfill) was
-- explicitly skipped on this basis.
--
-- Reverting Stage 5: the new tables (w_canvas_snapshot,
-- project.document/schema_version/element_count) carry all live state
-- and snapshot history. A revert would need to reconstruct the legacy
-- shape from these — non-trivial. Don't revert; if a corruption is
-- found, restore from backup instead.

ALTER TABLE `w_canvas_project` DROP COLUMN `latest_version_id`;

DROP TABLE `w_canvas_version`;
