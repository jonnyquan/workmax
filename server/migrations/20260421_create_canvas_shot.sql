-- Canvas Shot backing table. One row per promoted card; LocalCardID is
-- the idempotency key the sync endpoint uses to upsert from the canvas
-- document's shots[] array.

CREATE TABLE IF NOT EXISTS `w_shot` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,
  `uid`                   int NOT NULL DEFAULT 0,
  `canvas_project_id`     bigint unsigned NOT NULL,
  `local_card_id`         varchar(64) NOT NULL DEFAULT '',
  `order_index`           int NOT NULL DEFAULT 0,
  `title`                 varchar(200) NOT NULL DEFAULT '',
  `timeline_start_ms`     bigint DEFAULT NULL,
  `timeline_duration_ms`  bigint DEFAULT NULL,
  `status`                tinyint NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_card` (`canvas_project_id`, `local_card_id`),
  KEY `idx_uid_project` (`uid`, `canvas_project_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
