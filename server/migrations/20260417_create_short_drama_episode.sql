-- Episodes belonging to a short-drama project. Phase 0 stores metadata only;
-- script / storyboard / shot payloads live in separate tables added in later phases.

CREATE TABLE IF NOT EXISTS `w_short_drama_episode` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`     datetime DEFAULT NULL,
  `project_id`     bigint unsigned NOT NULL,
  `uid`            int NOT NULL DEFAULT 0,
  `episode_number` int NOT NULL DEFAULT 0,
  `title`          varchar(200) NOT NULL DEFAULT '',
  `synopsis`       text,
  `key_events`     text,
  `emotional_arc`  varchar(255) NOT NULL DEFAULT '',
  `duration`       int NOT NULL DEFAULT 0,
  `output_url`     varchar(2048) NOT NULL DEFAULT '',
  `status`         tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_episode` (`project_id`, `episode_number`, `deleted_at`),
  KEY `idx_project_status` (`project_id`, `status`),
  KEY `idx_uid` (`uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
