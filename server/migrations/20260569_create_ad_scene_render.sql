-- Phase 4: per-scene render tracking for video-ad creatives.
--
-- Mirrors short-drama's w_drama_panel_shot in shape: one row per
-- active render attempt per scene. The unique key on
-- (creative_id, scene_id, deleted_at) means at most one *active*
-- render per scene; re-renders soft-delete the prior row, preserving
-- history while keeping the active set well-defined.

CREATE TABLE `w_ad_scene_render` (
  `id` BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  `creative_id` BIGINT UNSIGNED NOT NULL COMMENT 'fk to w_ad_creative',
  `uid` BIGINT NOT NULL COMMENT 'creator',
  `scene_id` VARCHAR(40) NOT NULL COMMENT 'AdScriptScene.id (uuid)',
  `task_id` VARCHAR(128) NOT NULL DEFAULT '' COMMENT 'fk to w_generation_task.task_id',
  `model` VARCHAR(64) NOT NULL DEFAULT '' COMMENT 'video model used',
  `prompt` TEXT COMMENT 'generation prompt snapshot',
  `video_url` VARCHAR(2048) NOT NULL DEFAULT '' COMMENT 'rendered clip url',
  `thumbnail_url` VARCHAR(2048) NOT NULL DEFAULT '' COMMENT 'first-frame thumbnail',
  `duration_sec` INT NOT NULL DEFAULT 0 COMMENT 'actual clip duration',
  `credits_used` INT NOT NULL DEFAULT 0 COMMENT 'credits charged',
  `status` TINYINT NOT NULL DEFAULT 0 COMMENT '0=pending 1=processing 2=completed 3=failed',
  `error_message` VARCHAR(500) NOT NULL DEFAULT '' COMMENT 'failure detail',
  `created_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` DATETIME DEFAULT NULL,
  UNIQUE KEY `uniq_creative_scene_active` (`creative_id`, `scene_id`, `deleted_at`),
  KEY `idx_task_id` (`task_id`),
  KEY `idx_uid` (`uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='per-scene render state for video-ad creatives';
