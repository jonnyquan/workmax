-- Speeds up GET /api/character for Canvas character registries.
-- The hot queries filter by uid/deleted_at and optionally project_id,
-- then return the newest rows with ORDER BY id DESC LIMIT n.

SET @schema_name := DATABASE();

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_character'
        AND INDEX_NAME = 'idx_character_uid_deleted_id'
    ),
    'SELECT 1',
    'ALTER TABLE `w_character` ADD INDEX `idx_character_uid_deleted_id` (`uid`, `deleted_at`, `id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_character'
        AND INDEX_NAME = 'idx_character_uid_project_deleted_id'
    ),
    'SELECT 1',
    'ALTER TABLE `w_character` ADD INDEX `idx_character_uid_project_deleted_id` (`uid`, `project_id`, `deleted_at`, `id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
