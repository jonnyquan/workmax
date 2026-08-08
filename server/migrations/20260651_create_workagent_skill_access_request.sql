CREATE TABLE IF NOT EXISTS `w_workagent_skill_access_request` (
  `id` bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `uid` bigint NOT NULL DEFAULT 0,
  `agent_mode` varchar(80) NOT NULL DEFAULT '',
  `source` varchar(64) NOT NULL DEFAULT '',
  `status` varchar(32) NOT NULL DEFAULT 'pending',
  `reason` varchar(512) NOT NULL DEFAULT '',
  PRIMARY KEY (`id`),
  UNIQUE KEY `idx_skill_access_uid_agent` (`uid`, `agent_mode`),
  KEY `idx_skill_access_status` (`status`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
