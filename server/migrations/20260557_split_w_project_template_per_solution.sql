-- R3-c · Split w_project_template into per-solution template tables.
--
-- 原 w_project_template 用 solution_type 给所有方案共享种子模板。dev
-- 当前有 5 行全是 short_drama seed。R3-c 改为：把 w_project_template
-- rename 成 w_drama_template（数据天然带过去），drop solution_type
-- 列 + 重建索引；同时建 ad / comic / ecom 三张空模板表，等各方案
-- 真正需要 template seed 时再分别填。
--
-- 各方案模板结构差异：drama 保留原有的 default_episode_count /
-- default_aspect_ratio 等字段；ad / comic / ecom 暂时只放共享字段
-- （slug / name / description / settings_json），solution 专属默认值
-- 用 settings_json 装载 —— 等各自有真实模板需求时再 ALTER。
--
-- dev migration: w_project_template 现有 5 行 (drama seed) → 全部
-- 进入 w_drama_template；ad / comic / ecom 表建出来空着。

RENAME TABLE `w_project_template` TO `w_drama_template`;

ALTER TABLE `w_drama_template`
  DROP INDEX `uk_slug_solution`,
  DROP INDEX `idx_solution_system`,
  DROP COLUMN `solution_type`,
  ADD UNIQUE INDEX `uk_slug` (`slug`),
  ADD INDEX `idx_system_status` (`is_system`, `status`);

CREATE TABLE IF NOT EXISTS `w_ad_template` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`    datetime DEFAULT NULL,
  `uid`           int NOT NULL DEFAULT 0          COMMENT '系统模板为0',
  `slug`          varchar(64) NOT NULL DEFAULT '' COMMENT '短链',
  `name`          varchar(120) NOT NULL DEFAULT '',
  `description`   varchar(500) NOT NULL DEFAULT '',
  `settings_json` json DEFAULT NULL              COMMENT '方案专属默认值容器',
  `is_system`     tinyint(1) NOT NULL DEFAULT 0,
  `sort_order`    int NOT NULL DEFAULT 0,
  `status`        tinyint NOT NULL DEFAULT 1     COMMENT '1=active 2=archived',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_slug` (`slug`),
  KEY `idx_system_status` (`is_system`, `status`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_comic_template` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`    datetime DEFAULT NULL,
  `uid`           int NOT NULL DEFAULT 0,
  `slug`          varchar(64) NOT NULL DEFAULT '',
  `name`          varchar(120) NOT NULL DEFAULT '',
  `description`   varchar(500) NOT NULL DEFAULT '',
  `settings_json` json DEFAULT NULL,
  `is_system`     tinyint(1) NOT NULL DEFAULT 0,
  `sort_order`    int NOT NULL DEFAULT 0,
  `status`        tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_slug` (`slug`),
  KEY `idx_system_status` (`is_system`, `status`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_ecom_template` (
  `id`            bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`    datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`    datetime DEFAULT NULL,
  `uid`           int NOT NULL DEFAULT 0,
  `slug`          varchar(64) NOT NULL DEFAULT '',
  `name`          varchar(120) NOT NULL DEFAULT '',
  `description`   varchar(500) NOT NULL DEFAULT '',
  `settings_json` json DEFAULT NULL,
  `is_system`     tinyint(1) NOT NULL DEFAULT 0,
  `sort_order`    int NOT NULL DEFAULT 0,
  `status`        tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_slug` (`slug`),
  KEY `idx_system_status` (`is_system`, `status`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
