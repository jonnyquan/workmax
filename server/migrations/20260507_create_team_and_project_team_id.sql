-- Team collaboration — Phase 2 minimum.
-- Two tables + one nullable column on w_project.
--
-- Scope:
--   - Owner creates a team.
--   - Owner adds members by uid.
--   - Any project with team_id set is visible/editable by every team
--     member; projects without team_id stay strictly owner-scoped.
--
-- Skipped for this iteration (add later as needed):
--   - Email-based invitations / invite links.
--   - Role hierarchy beyond owner/member (admin, contributor, viewer …).
--   - Per-resource ACL (character, template …).
--   - Team-scoped characters (characters stay per-uid for now).
--
-- Note on `active_key`:
--   GORM soft-delete writes `deleted_at = NULL` for active rows. MySQL
--   treats NULLs as not-equal in UNIQUE indexes, so a naive
--   `UNIQUE KEY (slug, deleted_at)` would allow two active rows to share
--   a slug. We add a generated column `active_key` that is 0 for active
--   rows and NULL for soft-deleted rows, then participate it in the
--   unique index. Result:
--     - two active rows with same slug    → (foo, 0) vs (foo, 0)    → VIOLATION ✓
--     - active + deleted with same slug   → (foo, 0) vs (foo, NULL) → OK      ✓
--     - two deleted rows with same slug   → (foo, NULL) vs (foo, NULL) → OK   ✓ (NULL != NULL)
--   We use 0-or-NULL (not id-or-0) because MySQL 8.x forbids generated
--   columns that reference AUTO_INCREMENT columns.

CREATE TABLE IF NOT EXISTS `w_team` (
  `id`          bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`  datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`  datetime DEFAULT NULL,
  `owner_uid`   int NOT NULL COMMENT '创建者UID',
  `name`        varchar(120) NOT NULL DEFAULT '',
  `slug`        varchar(64) NOT NULL DEFAULT '' COMMENT '短链/URL标识',
  `description` varchar(500) NOT NULL DEFAULT '',
  `status`      tinyint NOT NULL DEFAULT 1 COMMENT '1=active 2=archived',
  `active_key`  tinyint unsigned GENERATED ALWAYS AS
    (CASE WHEN `deleted_at` IS NULL THEN 0 ELSE NULL END) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_slug` (`slug`, `active_key`),
  KEY `idx_owner` (`owner_uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_team_member` (
  `id`         bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at` datetime DEFAULT NULL,
  `team_id`    bigint unsigned NOT NULL,
  `uid`        int NOT NULL,
  `role`       varchar(32) NOT NULL DEFAULT 'member' COMMENT 'owner|member (more later)',
  `status`     tinyint NOT NULL DEFAULT 1 COMMENT '1=active 2=invited 3=removed',
  `active_key` tinyint unsigned GENERATED ALWAYS AS
    (CASE WHEN `deleted_at` IS NULL THEN 0 ELSE NULL END) STORED,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_team_uid` (`team_id`, `uid`, `active_key`),
  KEY `idx_uid` (`uid`),
  KEY `idx_team` (`team_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Nullable team_id on w_project. NULL = strictly owner-scoped (the
-- existing behaviour). Non-null = shared with that team's members.
ALTER TABLE `w_project`
  ADD COLUMN `team_id` bigint unsigned DEFAULT NULL COMMENT '可选团队归属,NULL=仅所有者'
    AFTER `uid`,
  ADD INDEX `idx_team` (`team_id`);
