-- 历史导出版本表 — Part B of SD-FU2 (export version history).
-- Each completed export writes one row here; the episode.output_url stays
-- as the "current" pointer and this table is the version log.
-- Restore copies a row's output_url back to episode.output_url without
-- deleting newer rows so users can re-restore.
CREATE TABLE `w_drama_episode_export_version` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `episode_id` BIGINT UNSIGNED NOT NULL,
  `uid` INT NOT NULL DEFAULT 0,
  `version` INT NOT NULL DEFAULT 1 COMMENT '1-indexed within an episode',
  `output_url` VARCHAR(2048) NOT NULL DEFAULT '',
  `task_id` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'fk to w_generation_task.task_id',
  `duration_sec` INT NOT NULL DEFAULT 0,
  `panel_count` INT NOT NULL DEFAULT 0 COMMENT 'snapshot at export time',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY `uk_episode_version` (`episode_id`, `version`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='历史导出版本';
