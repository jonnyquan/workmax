-- P6 HTML-native export job queue contract.
--
-- ZIP exports remain available through the synchronous export endpoint.
-- Browser-rendered targets (PNG/PDF/MP4/GIF) are represented here so a
-- renderer worker can claim queued jobs without changing the artifact API
-- contract again.

CREATE TABLE IF NOT EXISTS `w_workagent_artifact_export_job` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  `uid`                int NOT NULL,
  `thread_id`          bigint unsigned NOT NULL,
  `artifact_id`        bigint unsigned NOT NULL,
  `thread_file_id`     bigint unsigned NOT NULL,

  `target`             varchar(16) NOT NULL,
  `kind`               varchar(32) NOT NULL DEFAULT '',
  `worker`             varchar(64) NOT NULL DEFAULT '',
  `status`             varchar(32) NOT NULL DEFAULT 'queued',
  `reason`             varchar(255) NOT NULL DEFAULT '',
  `output_extension`   varchar(16) NOT NULL DEFAULT '',
  `prerequisites_json` text,
  `plan_json`          text,

  `output_file_id`     bigint unsigned NOT NULL DEFAULT 0,
  `output_path`        varchar(1024) NOT NULL DEFAULT '',
  `error_message`      text,

  PRIMARY KEY (`id`),
  KEY `idx_workagent_export_job_owner_artifact` (`uid`, `thread_id`, `artifact_id`, `created_at`),
  KEY `idx_workagent_export_job_status` (`status`, `worker`, `created_at`),
  KEY `idx_workagent_export_job_target` (`uid`, `target`, `created_at`),
  KEY `idx_workagent_export_job_output_file` (`output_file_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
