-- Character asset tables shared by vertical solutions (short-drama, comic-video, ...).

CREATE TABLE IF NOT EXISTS `w_character` (
  `id`               bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`       datetime DEFAULT NULL,
  `uid`              int NOT NULL DEFAULT 0,
  `project_id`       bigint unsigned DEFAULT NULL,
  `name`             varchar(120) NOT NULL DEFAULT '',
  `slug`             varchar(160) NOT NULL DEFAULT '',
  `avatar_image_url` varchar(2048) NOT NULL DEFAULT '',
  `role_type`        varchar(32) NOT NULL DEFAULT 'supporting',
  `gender`           varchar(32) NOT NULL DEFAULT '',
  `age_range`        varchar(32) NOT NULL DEFAULT '',
  `appearance`       text,
  `personality`      text,
  `visual_dna_json`  json DEFAULT NULL,
  `prompt_suffix`    text,
  `negative_prompt`  text,
  `lora_model_id`    bigint unsigned DEFAULT NULL,
  `source_kind`      varchar(32) NOT NULL DEFAULT 'manual',
  `status`           tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_project` (`project_id`),
  KEY `idx_lora_model` (`lora_model_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_character_reference` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`     datetime DEFAULT NULL,
  `character_id`   bigint unsigned NOT NULL,
  `uid`            int NOT NULL DEFAULT 0,
  `image_url`      varchar(2048) NOT NULL DEFAULT '',
  `reference_type` varchar(32) NOT NULL DEFAULT 'face',
  `label`          varchar(80) NOT NULL DEFAULT '',
  `sort_order`     int NOT NULL DEFAULT 0,
  `metadata`       json DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_character` (`character_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_character_relationship` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,
  `project_id`            bigint unsigned NOT NULL,
  `character_id`          bigint unsigned NOT NULL,
  `related_character_id`  bigint unsigned NOT NULL,
  `relation_type`         varchar(32) NOT NULL DEFAULT '',
  `description`           varchar(255) NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_pair` (`project_id`, `character_id`, `related_character_id`),
  KEY `idx_character` (`character_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
