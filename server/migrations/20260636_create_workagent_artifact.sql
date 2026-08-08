-- P1 Artifact-first registry for Work Agent.
--
-- w_workagent_thread_file remains the source file fact table. This
-- registry adds lifecycle/version/review metadata that can be attached
-- to files without overloading the file table itself.

CREATE TABLE IF NOT EXISTS `w_workagent_artifact` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  `uid`                int NOT NULL,
  `thread_id`          bigint unsigned NOT NULL,
  `thread_file_id`     bigint unsigned NOT NULL,
  `artifact_key`       varchar(320) NOT NULL,

  `name`               varchar(500) NOT NULL,
  `display_name`       varchar(500) NOT NULL DEFAULT '',
  `artifact_type`      varchar(64) NOT NULL,
  `output_type`        varchar(64) NOT NULL,
  `preview_type`       varchar(64) NOT NULL,
  `export_targets`     text,

  `version`            int NOT NULL DEFAULT 1,
  `status`             varchar(32) NOT NULL DEFAULT 'draft',
  `review_state`       varchar(32) NOT NULL DEFAULT 'none',
  `source`             varchar(20) NOT NULL DEFAULT 'upload',
  `parent_artifact_id` bigint unsigned NOT NULL DEFAULT 0,
  `file_hash`          varchar(64) NOT NULL DEFAULT '',

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_workagent_artifact_thread_file` (`thread_file_id`),
  KEY `idx_workagent_artifact_thread_updated` (`uid`, `thread_id`, `updated_at`),
  KEY `idx_workagent_artifact_key_version` (`uid`, `thread_id`, `artifact_key`, `version`),
  KEY `idx_workagent_artifact_status` (`uid`, `status`, `updated_at`),
  KEY `idx_workagent_artifact_parent` (`parent_artifact_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
