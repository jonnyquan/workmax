-- Minimal project membership table.
--
-- w_global_project.uid remains the compatibility owner field. This table is
-- the explicit collaboration/permission layer used by new code paths.

CREATE TABLE IF NOT EXISTS `w_global_project_member` (
  `id`             bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`     datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`     datetime DEFAULT NULL,

  `project_id`     bigint unsigned NOT NULL,
  `uid`            int NOT NULL,
  `role`           varchar(20) NOT NULL DEFAULT 'viewer',
  `source`         varchar(32) NOT NULL DEFAULT 'invite',
  `created_by`     int NOT NULL DEFAULT 0,
  `last_access_at` datetime DEFAULT NULL,

  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_project_member_user` (`project_id`, `uid`),
  KEY `idx_project_member_uid_access` (`uid`, `last_access_at`),
  KEY `idx_project_member_project_role` (`project_id`, `role`),
  KEY `idx_project_member_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT INTO `w_global_project_member`
  (`project_id`, `uid`, `role`, `source`, `created_by`, `last_access_at`, `created_at`, `updated_at`)
SELECT
  p.`id`,
  p.`uid`,
  'owner',
  'owner',
  p.`uid`,
  p.`updated_at`,
  p.`created_at`,
  p.`updated_at`
FROM `w_global_project` p
WHERE p.`uid` > 0
  AND p.`deleted_at` IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM `w_global_project_member` m
    WHERE m.`project_id` = p.`id`
      AND m.`uid` = p.`uid`
      AND m.`deleted_at` IS NULL
  );
