-- WorkAgent thread_file: composite index supporting the dock-panel
-- listing.
--
-- GetFiles in server/service/tools/workagent/file_service.go runs:
--   SELECT … FROM w_workagent_thread_file
--   WHERE uid = ? AND thread_id = ? [AND file_source = ?]
--   ORDER BY created_at DESC LIMIT 500
--
-- Existing indexes cover (uid) and (thread_id) individually, but the
-- ORDER BY filesorts whatever set survives the WHERE — costly once
-- ppt-mode threads accumulate hundreds of `output` rows. The
-- composite below lets InnoDB walk the index in the requested
-- order and stop after LIMIT, no filesort.
--
-- file_source is in the middle position because the dock-panel split
-- view (uploads tab vs outputs tab) is the dominant filter; threads
-- that don't filter still benefit from index-order scanning.

ALTER TABLE `w_workagent_thread_file`
  ADD INDEX `idx_thread_source_created` (`thread_id`, `file_source`, `created_at`);
