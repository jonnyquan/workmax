-- Short-drama vertical solution: project-level record.
-- Episodes / scripts / storyboards / shots are deferred to later phases.

CREATE TABLE IF NOT EXISTS `w_short_drama_project` (
  `id`                bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`        datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`        datetime DEFAULT NULL,
  `uid`               int NOT NULL DEFAULT 0,
  `uuid`              varchar(36) NOT NULL DEFAULT '',
  `title`             varchar(200) NOT NULL DEFAULT '',
  `genre`             varchar(64) NOT NULL DEFAULT '',
  `synopsis`          text,
  `target_platform`   varchar(64) NOT NULL DEFAULT '',
  `cover_image_url`   varchar(2048) NOT NULL DEFAULT '',
  `episode_count`     int NOT NULL DEFAULT 0,
  `episode_duration`  int NOT NULL DEFAULT 0,
  `aspect_ratio`      varchar(16) NOT NULL DEFAULT '9:16',
  `style_preset`      varchar(64) NOT NULL DEFAULT '',
  `settings_json`     json DEFAULT NULL,
  `status`            tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_short_drama_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
