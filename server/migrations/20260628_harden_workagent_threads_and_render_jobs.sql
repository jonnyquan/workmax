-- WorkAgent hardening:
-- 1) New conversations are private until the user explicitly shares them.
-- 2) render_shot jobs can be listed by indexed thread_id instead of scanning request_data JSON.

SET @db_name := DATABASE();

SET @sql := (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE `w_generation_task` ADD COLUMN `thread_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''source thread id for WorkAgent render_shot'' AFTER `duration_ms`',
    'SELECT 1'
  )
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'w_generation_task'
    AND COLUMN_NAME = 'thread_id'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @sql := (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE `w_generation_task` ADD COLUMN `message_id` bigint unsigned NOT NULL DEFAULT 0 COMMENT ''source message id for WorkAgent render_shot'' AFTER `thread_id`',
    'SELECT 1'
  )
  FROM information_schema.COLUMNS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'w_generation_task'
    AND COLUMN_NAME = 'message_id'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

UPDATE `w_generation_task`
SET
  `thread_id` = COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(`request_data`, '$.thread_id')) AS UNSIGNED), 0),
  `message_id` = COALESCE(CAST(JSON_UNQUOTE(JSON_EXTRACT(`request_data`, '$.message_id')) AS UNSIGNED), 0)
WHERE `tool_id` = 'render_shot'
  AND (`thread_id` = 0 OR `message_id` = 0)
  AND JSON_VALID(`request_data`);

SET @sql := (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE `w_generation_task` ADD INDEX `idx_generation_task_uid_tool_thread_id` (`uid`, `tool_id`, `thread_id`, `id`)',
    'SELECT 1'
  )
  FROM information_schema.STATISTICS
  WHERE TABLE_SCHEMA = @db_name
    AND TABLE_NAME = 'w_generation_task'
    AND INDEX_NAME = 'idx_generation_task_uid_tool_thread_id'
);
PREPARE stmt FROM @sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

ALTER TABLE `w_workagent_thread`
  ALTER COLUMN `is_public` SET DEFAULT 0;

-- v1 defaulted every WorkAgent thread to public. There was no separate
-- "explicitly shared" marker, so the safe migration is to revoke legacy
-- implicit shares and require the owner to publish again.
UPDATE `w_workagent_thread`
SET `is_public` = 0
WHERE `is_public` = 1;
