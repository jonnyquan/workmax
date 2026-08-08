-- Speeds up render_shot queue wake/fallback fetches:
--   WHERE tool_id = 'render_shot' AND status = 0 ORDER BY id ASC LIMIT n
-- The worker is now signal-driven, but this index keeps the low-frequency
-- safety poll cheap and protects manual ProcessOnce/admin recovery paths.

SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_generation_task'
        AND INDEX_NAME = 'idx_generation_task_tool_status_id'
    ),
    'SELECT 1',
    'ALTER TABLE `w_generation_task` ADD INDEX `idx_generation_task_tool_status_id` (`tool_id`, `status`, `id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
