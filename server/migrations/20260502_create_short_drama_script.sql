-- Per-episode script. One script per episode (enforced by uk_episode).
-- Scenes/dialogues live in a JSON blob because the structure evolves faster
-- than the rest of the schema and downstream consumers (storyboard engine,
-- shot generator) treat the script as an opaque nested object.

CREATE TABLE IF NOT EXISTS `w_short_drama_script` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`         datetime DEFAULT NULL,
  `uid`                int NOT NULL DEFAULT 0,
  `project_id`         bigint unsigned NOT NULL,
  `episode_id`         bigint unsigned NOT NULL,
  `scenes_json`        mediumtext COMMENT '场景+对白结构化JSON',
  `scene_count`        int NOT NULL DEFAULT 0 COMMENT '缓存场景数,便于列表展示',
  `status`             tinyint NOT NULL DEFAULT 1 COMMENT '1=draft 2=reviewed 3=locked',
  `locked_at`          datetime DEFAULT NULL,
  `generation_version` int NOT NULL DEFAULT 0 COMMENT 'AI每次生成+1,审计用',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_episode` (`episode_id`, `deleted_at`),
  KEY `idx_project_uid` (`project_id`, `uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
