-- Minimal model catalog.
--
-- This table describes model capabilities and display metadata only.
-- Provider credentials, priority, default/failover and quotas stay in
-- w_generator_provider to avoid a redundant routing table.

CREATE TABLE IF NOT EXISTS `w_global_model` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`     datetime DEFAULT NULL,

  `model_id`       varchar(128) NOT NULL,
  `media_type`     varchar(20) NOT NULL,
  `provider_type`  varchar(32) NOT NULL DEFAULT '',
  `display_name`   varchar(128) NOT NULL DEFAULT '',
  `status`         tinyint NOT NULL DEFAULT 1,
  `pricing_status` varchar(20) NOT NULL DEFAULT '',
  `sort_order`     int NOT NULL DEFAULT 0,
  `capabilities`   json DEFAULT NULL,
  `metadata`       json DEFAULT NULL,

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_global_model_media` (`model_id`, `media_type`),
  KEY `idx_global_model_media_status` (`media_type`, `status`),
  KEY `idx_global_model_provider` (`provider_type`),
  KEY `idx_global_model_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
