-- In-app notifications delivered to users.
-- Each row is addressed to a specific user (uid). System broadcasts are
-- materialized as one row per recipient so list/count queries stay simple.

CREATE TABLE IF NOT EXISTS `w_notification` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`     datetime DEFAULT NULL,
  `uid`            int unsigned NOT NULL DEFAULT 0,
  `title`          varchar(200) NOT NULL DEFAULT '',
  `content`        text,
  `image`          varchar(1024) NOT NULL DEFAULT '',
  `type`           tinyint NOT NULL DEFAULT 1,
  `location`       varchar(255) NOT NULL DEFAULT '',
  `location_label` varchar(100) NOT NULL DEFAULT '',
  `readed`         tinyint(1) NOT NULL DEFAULT 0,
  `read_at`        datetime DEFAULT NULL,
  PRIMARY KEY (`id`),
  KEY `idx_uid_readed` (`uid`, `readed`),
  KEY `idx_uid_created_at` (`uid`, `created_at`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
