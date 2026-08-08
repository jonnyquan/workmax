-- §1 Project/Workspace Stage 1.5 — system_kind column on w_global_project.
--
-- Replaces the Stage 1 title-based sentinel ("Personal Workspace" literal)
-- with an unambiguous column marker. Closes four real risk paths the
-- title-based detection had:
--
--   1. A user manually creating a project literally named "Personal
--      Workspace" before Stage 1.5 would shadow the system sentinel.
--   2. A future rename UI on the Project would let the user accidentally
--      break their own protection.
--   3. The canvas DELETE endpoint (DELETE /api/tools/canvas/projects/:id)
--      shares w_global_project; without a column marker, the canvas-side
--      delete path could not check "is this row a system workspace" and
--      would happily soft-delete the workagent default workspace.
--   4. Multi-instance race could land two title-PW rows; the column lets
--      a follow-up cleanup script disambiguate the canonical row.
--
-- Values:
--   0 = user-created (default for every existing row + every new
--       canvas/workagent project going forward unless explicitly set)
--   1 = workagent Personal Workspace
--   2,3,… reserved for future system kinds (team-default, template
--         root, etc.) — no Stage 1.5 producer for these yet.
--
-- Backfill keeps Stage 1's lazy-create model: only projects that
-- ALREADY exist with title 'Personal Workspace' get marked. New users
-- still trigger creation on first /api/work-agent/projects GET — we
-- explicitly do NOT pre-create a workspace for every active user, since
-- the majority may never touch /work-agent.
--
-- Idempotent — the column-existence guard short-circuits a re-run.

SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_global_project'
        AND COLUMN_NAME = 'system_kind'
    ),
    'SELECT 1',
    'ALTER TABLE `w_global_project`
       ADD COLUMN `system_kind` TINYINT NOT NULL DEFAULT 0
         COMMENT ''Stage 1.5: 0=user-created, 1=workagent Personal Workspace; 2-127 reserved for future system kinds''
       AFTER `uid`,
       ADD INDEX `idx_global_project_uid_system_kind` (`uid`, `system_kind`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- Backfill: tag existing Personal Workspace rows. WHERE system_kind = 0
-- on the predicate makes the UPDATE idempotent on re-run (rows already
-- bumped to 1 are skipped). Bounded by title literal so user-created
-- "Personal Workspace"-named projects DO get tagged here — that's the
-- Stage 1 sentinel's contract and we honour it during the cutover. A
-- future cleanup script can dedupe + reassign if a real collision
-- exists; flagging the wrong row as system is a user-visible quirk
-- they can rename around, not a data integrity loss.

UPDATE `w_global_project`
SET `system_kind` = 1
WHERE `title` = 'Personal Workspace'
  AND `deleted_at` IS NULL
  AND `system_kind` = 0;
