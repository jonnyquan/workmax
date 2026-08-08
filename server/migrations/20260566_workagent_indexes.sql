-- WorkAgent: hot-path covering indexes.
--
-- Two query patterns dominate the WorkAgent module and both fall back to
-- filesort + temporary tables under the current schema:
--
-- 1. conversation list (server/api/pro/tools/workagent/conversation_api.go:144)
--    SELECT ... FROM w_workagent_thread
--      WHERE uid = ? AND agent_type = ? AND deleted_at IS NULL
--      ORDER BY updated_at DESC LIMIT N
--    Existing indexes: idx_uid, idx_agent_type. Neither covers the (uid,
--    agent_type, updated_at) prefix, so MySQL chooses idx_uid then sorts
--    in memory. Composite (uid, agent_type, updated_at) lets the query
--    walk the index in order and stop after LIMIT.
--
-- 2. AddFile dedup lookup (server/service/tools/workagent/file_service.go:82)
--    SELECT ... FROM w_workagent_thread_file
--      WHERE uid = ? AND thread_id = ? AND file_name = ? AND file_path = ?
--    Existing indexes: idx_uid, idx_thread_id. Neither includes file_name,
--    so the lookup scans every file row owned by that thread on each
--    upload. Composite (uid, thread_id, file_name) narrows the scan to
--    near-O(1) before the file_path equality filter runs.
--
-- The unique constraint on (uid, thread_id, file_name, file_path) called
-- out in file_service.go:65 still TODO is deferred to a follow-up: file_path
-- is varchar(1000) which exceeds InnoDB's 3072-byte index key limit on
-- utf8mb4, so adding a unique index requires either a generated path-hash
-- column or shrinking file_path. The non-unique covering index here
-- already removes the perf hot spot; race-condition narrowing remains the
-- AddFile transaction's job until that follow-up.

ALTER TABLE `w_workagent_thread`
  ADD INDEX `idx_uid_agent_type_updated_at` (`uid`, `agent_type`, `updated_at`);

ALTER TABLE `w_workagent_thread_file`
  ADD INDEX `idx_uid_thread_file_name` (`uid`, `thread_id`, `file_name`);
