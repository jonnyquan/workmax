-- Staging table for turning final/exported Work Agent artifacts into
-- reusable asset-library candidates. This does not write to the typed
-- brand / character / product / director-style tables yet; it stores
-- the confirmed classification and reusable profile first.

CREATE TABLE IF NOT EXISTS `w_workagent_artifact_asset_candidate` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  `uid`            int NOT NULL,
  `thread_id`      bigint unsigned NOT NULL,
  `artifact_id`    bigint unsigned NOT NULL,
  `thread_file_id` bigint unsigned NOT NULL,
  `asset_kind`     varchar(32) NOT NULL,
  `name`           varchar(255) NOT NULL DEFAULT '',
  `slug`           varchar(255) NOT NULL DEFAULT '',
  `profile_json`   text,
  `status`         varchar(32) NOT NULL DEFAULT 'draft',

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_workagent_artifact_asset_candidate_kind` (`artifact_id`, `asset_kind`),
  KEY `idx_workagent_artifact_asset_candidate_thread` (`uid`, `thread_id`, `updated_at`),
  KEY `idx_workagent_artifact_asset_candidate_kind` (`uid`, `asset_kind`, `status`, `updated_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
