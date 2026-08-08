-- Per-episode storyboard. One storyboard per episode (uk_episode).
-- Generated from a LOCKED script; script_version is captured at generation
-- time so downstream consumers can detect when the source script has been
-- unlocked + regenerated underneath them.

CREATE TABLE IF NOT EXISTS `w_short_drama_storyboard` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`         datetime DEFAULT NULL,
  `uid`                int NOT NULL DEFAULT 0,
  `project_id`         bigint unsigned NOT NULL,
  `episode_id`         bigint unsigned NOT NULL,
  `script_id`          bigint unsigned NOT NULL COMMENT '源脚本ID(必须是locked状态)',
  `script_version`     int NOT NULL DEFAULT 0 COMMENT '生成时的脚本generation_version,用于检测脚本变动',
  `panels_json`        mediumtext COMMENT '分镜面板JSON',
  `panel_count`        int NOT NULL DEFAULT 0 COMMENT '缓存面板数',
  `status`             tinyint NOT NULL DEFAULT 1 COMMENT '1=draft 2=reviewed 3=locked',
  `locked_at`          datetime DEFAULT NULL,
  `generation_version` int NOT NULL DEFAULT 0 COMMENT 'AI生成版本号',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_episode` (`episode_id`, `deleted_at`),
  KEY `idx_project_uid` (`project_id`, `uid`),
  KEY `idx_script` (`script_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
