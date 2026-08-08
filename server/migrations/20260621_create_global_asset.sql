-- Minimal global asset index.
--
-- w_generation_object and w_workagent_thread_file remain source fact tables;
-- Canvas project files write native source_table='canvas_project_file' rows.
-- w_global_asset is the cross-tool reuse, permission, and assetId surface.

CREATE TABLE IF NOT EXISTS `w_global_asset` (
  `id`               bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`       datetime DEFAULT NULL,

  `uid`              int NOT NULL DEFAULT 0,
  `team_id`          bigint unsigned DEFAULT NULL,
  `project_id`       bigint unsigned DEFAULT NULL,
  `uuid`             varchar(36) NOT NULL,

  `kind`             varchar(32) NOT NULL DEFAULT 'image',
  `source`           varchar(32) NOT NULL DEFAULT 'upload',
  `source_table`     varchar(64) NOT NULL DEFAULT '',
  `source_id`        bigint unsigned NOT NULL DEFAULT 0,
  `source_item_key`  varchar(128) NOT NULL DEFAULT '',

  `url`              varchar(2048) NOT NULL DEFAULT '',
  `thumb_url`        varchar(2048) NOT NULL DEFAULT '',
  `mime_type`        varchar(128) NOT NULL DEFAULT '',
  `size_bytes`       bigint NOT NULL DEFAULT 0,
  `width`            int NOT NULL DEFAULT 0,
  `height`           int NOT NULL DEFAULT 0,
  `duration_ms`      int NOT NULL DEFAULT 0,
  `content_hash`     char(64) NOT NULL DEFAULT '',

  `status`           tinyint NOT NULL DEFAULT 1,
  `visibility`       tinyint NOT NULL DEFAULT 1,
  `parent_asset_id`  bigint unsigned NOT NULL DEFAULT 0,
  `variant_type`     varchar(32) NOT NULL DEFAULT 'original',
  `metadata`         json DEFAULT NULL,

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_global_asset_uuid` (`uuid`),
  UNIQUE KEY `uk_global_asset_source` (`source_table`, `source_id`, `source_item_key`),
  KEY `idx_global_asset_uid_updated` (`uid`, `updated_at`),
  KEY `idx_global_asset_project_updated` (`project_id`, `updated_at`),
  KEY `idx_global_asset_uid_hash` (`uid`, `content_hash`),
  KEY `idx_global_asset_kind_status_updated` (`kind`, `status`, `updated_at`),
  KEY `idx_global_asset_parent_variant` (`parent_asset_id`, `variant_type`),
  KEY `idx_global_asset_team` (`team_id`),
  KEY `idx_global_asset_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE `w_generation_object`
  ADD COLUMN `global_asset_id` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'optional w_global_asset.id bridge',
  ADD KEY `idx_generation_object_global_asset` (`global_asset_id`);

ALTER TABLE `w_workagent_thread_file`
  ADD COLUMN `global_asset_id` bigint unsigned NOT NULL DEFAULT 0
    COMMENT 'optional w_global_asset.id bridge',
  ADD KEY `idx_workagent_thread_file_global_asset` (`global_asset_id`);
