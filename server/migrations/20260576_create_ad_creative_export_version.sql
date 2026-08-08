-- 广告创意历史导出版本表 — Part of SD-P2-1 (video-ad export version history).
-- Each completed assemble writes one row here; the creative.output_url stays
-- as the "current" pointer and this table is the version log.
-- Restore copies a row's output_url back to creative.output_url without
-- deleting newer rows so users can re-restore.
CREATE TABLE `w_ad_creative_export_version` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `creative_id` BIGINT UNSIGNED NOT NULL,
  `uid` INT NOT NULL DEFAULT 0,
  `version` INT NOT NULL DEFAULT 1 COMMENT '1-indexed within a creative',
  `output_url` VARCHAR(2048) NOT NULL DEFAULT '',
  `output_thumbnail_url` VARCHAR(2048) NOT NULL DEFAULT '',
  `task_id` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'fk to w_generation_task.task_id',
  `duration_sec` INT NOT NULL DEFAULT 0,
  `scene_count` INT NOT NULL DEFAULT 0 COMMENT 'snapshot at assemble time',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY `uk_creative_version` (`creative_id`, `version`),
  KEY `idx_uid` (`uid`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='广告创意历史导出版本';
