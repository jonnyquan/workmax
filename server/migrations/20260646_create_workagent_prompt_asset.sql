-- First-class prompt assets materialized from confirmed Work Agent
-- prompt_asset candidates. This replaces candidate-backed pseudo-targets
-- with a durable, queryable prompt asset table.

CREATE TABLE IF NOT EXISTS `w_workagent_prompt_asset` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  `uid`             int NOT NULL,
  `project_id`      bigint unsigned NOT NULL DEFAULT 0,
  `thread_id`       bigint unsigned NOT NULL DEFAULT 0,
  `artifact_id`     bigint unsigned NOT NULL DEFAULT 0,
  `candidate_id`    bigint unsigned NOT NULL DEFAULT 0,

  `name`            varchar(255) NOT NULL DEFAULT '',
  `slug`            varchar(255) NOT NULL DEFAULT '',
  `prompt`          mediumtext,
  `negative_prompt` text,
  `profile_json`    text,
  `status`          varchar(32) NOT NULL DEFAULT 'confirmed',

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_workagent_prompt_asset_candidate` (`candidate_id`),
  KEY `idx_workagent_prompt_asset_project` (`uid`, `project_id`, `status`),
  KEY `idx_workagent_prompt_asset_thread` (`uid`, `thread_id`, `status`),
  KEY `idx_workagent_prompt_asset_slug` (`uid`, `slug`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
