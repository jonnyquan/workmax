SET @schema_name := DATABASE();

-- Drop low-cardinality index (visibility_status 只有 2 个取值, (uid, source, visibility_status) 已由 uk_uid_source_source_item 前缀覆盖).
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_visibility'
    ),
    'ALTER TABLE `w_user_asset_ledger` DROP INDEX `idx_uid_visibility`',
    'SELECT 1'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 面向 canvas 项目级清理的索引 (DeleteCanvasByProjectIDWithDB / DeleteCanvasThumbnailsByProjectIDWithDB).
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_project'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD INDEX `idx_uid_project` (`uid`, `project_id`, `source`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 面向 thread 级清理的索引 (DeleteThreadByThreadIDWithDB).
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_thread'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD INDEX `idx_uid_thread` (`uid`, `thread_id`, `source`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 面向生成记录批量清理的索引 (DeleteGeneratedByRecordIDsWithDB, SyncGenerationInputsWithDB).
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_record'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD INDEX `idx_uid_record` (`uid`, `record_id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 面向 task 级回填 / 清理的索引 (UpdateGeneratedRecordIDByTaskWithDB, DeleteGeneratedByTaskIDsWithDB).
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.STATISTICS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND INDEX_NAME = 'idx_uid_task'
    ),
    'SELECT 1',
    'ALTER TABLE `w_user_asset_ledger` ADD INDEX `idx_uid_task` (`uid`, `task_id`)'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

-- 派生字段 can_delete / delete_kind 由 source+project_id 决定, 迁移到读时计算, 去除冗余存储.
SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND COLUMN_NAME = 'can_delete'
    ),
    'ALTER TABLE `w_user_asset_ledger` DROP COLUMN `can_delete`',
    'SELECT 1'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;

SET @ddl := (
  SELECT IF(
    EXISTS (
      SELECT 1 FROM information_schema.COLUMNS
      WHERE TABLE_SCHEMA = @schema_name
        AND TABLE_NAME = 'w_user_asset_ledger'
        AND COLUMN_NAME = 'delete_kind'
    ),
    'ALTER TABLE `w_user_asset_ledger` DROP COLUMN `delete_kind`',
    'SELECT 1'
  )
);
PREPARE stmt FROM @ddl; EXECUTE stmt; DEALLOCATE PREPARE stmt;
