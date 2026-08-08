-- Split shared w_project into per-solution dedicated tables.
--
-- 原 w_project 用 solution_type discriminator 给 4 个方案共享。R3-a
-- 改为每个方案各自一张表，solution_type 列彻底移除（表名即
-- discriminator）。每张表只保留该方案实际用到的字段：
--
--   w_drama_project   = 共享列 + genre + aspect_ratio + style_preset +
--                       episode_count + episode_duration + outline_*
--   w_ad_project      = 共享列 + brand_profile_id + objective +
--                       target_platforms_json + target_audience
--                       （用 target_platforms_json 数组，drop 单一
--                        target_platform）
--   w_comic_project   = 共享列 + genre
--   w_ecom_project    = 共享列（无 genre — 商品按 category 不按 genre）
--
-- 数据迁移：dev 当前 w_project 0 行，INSERT FROM 是 no-op；prod 同条
-- migration 会按 solution_type 把每行送到对应新表，id 保留以保证
-- 子表 project_id 外键引用语义不变（drama_episode.project_id=42 在
-- migration 后仍指向 w_drama_project.id=42）。
--
-- 同步改动（详见 PR）：
--   server/model/project.go 拆成 drama_project.go / ad_project.go /
--     comic_project.go / ecom_project.go 4 个文件 + 4 个 struct
--   server/api/pro/tools/* 28 个文件 model.Project → 对应方案 struct
--   server/utils/testutil/testdb.go SQLite schema 同步
--   相关模型、表名和迁移说明同步
--
-- 不动：
--   - w_storyboard / w_project_template 拆分留给 R3-b / R3-c
--   - 子表 (drama_episode / ad_creative / comic_chapter / ecom_*)
--     的 project_id 列内容不变，FK 语义改为指向新的 per-solution
--     project 表（应用层维护一致性，无 schema 级 FK 约束）

CREATE TABLE IF NOT EXISTS `w_drama_project` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,
  `uid`                   int NOT NULL DEFAULT 0          COMMENT '所属用户',
  `team_id`               bigint unsigned DEFAULT NULL    COMMENT '可选团队归属',
  `uuid`                  varchar(36) NOT NULL DEFAULT '' COMMENT '对外UUID',
  `title`                 varchar(200) NOT NULL DEFAULT '' COMMENT '剧名',
  `genre`                 varchar(64) NOT NULL DEFAULT '' COMMENT '题材',
  `synopsis`              text                            COMMENT '核心冲突/简介',
  `target_platform`       varchar(64) NOT NULL DEFAULT '' COMMENT '投放平台',
  `cover_image_url`       varchar(2048) NOT NULL DEFAULT '' COMMENT '封面图URL',
  `settings_json`         json DEFAULT NULL                 COMMENT '扩展设置',
  `status`                tinyint NOT NULL DEFAULT 1        COMMENT '1=draft 2=in_progress 3=completed 4=archived',
  `aspect_ratio`          varchar(16) NOT NULL DEFAULT '9:16' COMMENT '画幅比例',
  `style_preset`          varchar(64) NOT NULL DEFAULT ''    COMMENT '风格预设',
  `episode_count`         int NOT NULL DEFAULT 0             COMMENT '规划集数',
  `episode_duration`      int NOT NULL DEFAULT 0             COMMENT '单集时长秒',
  `outline_generated_at`  datetime DEFAULT NULL              COMMENT '最近一次AI大纲生成时间',
  `outline_version`       int NOT NULL DEFAULT 0             COMMENT '大纲版本号,每次AI生成+1',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_team` (`team_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_ad_project` (
  `id`                    bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`            datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`            datetime DEFAULT NULL,
  `uid`                   int NOT NULL DEFAULT 0,
  `team_id`               bigint unsigned DEFAULT NULL,
  `uuid`                  varchar(36) NOT NULL DEFAULT '',
  `title`                 varchar(200) NOT NULL DEFAULT '' COMMENT '广告项目名',
  `synopsis`              text                              COMMENT '简介',
  `cover_image_url`       varchar(2048) NOT NULL DEFAULT '',
  `settings_json`         json DEFAULT NULL,
  `status`                tinyint NOT NULL DEFAULT 1,
  `brand_profile_id`      bigint unsigned DEFAULT NULL      COMMENT 'FK → w_ad_brand_profile.id',
  `objective`             varchar(32) NOT NULL DEFAULT ''   COMMENT '广告目标 awareness/consideration/conversion',
  `target_platforms_json` json DEFAULT NULL                 COMMENT '投放平台数组',
  `target_audience`       varchar(500) NOT NULL DEFAULT ''  COMMENT '目标人群描述',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_team` (`team_id`),
  KEY `idx_brand_profile` (`brand_profile_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_comic_project` (
  `id`              bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`      datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`      datetime DEFAULT NULL,
  `uid`             int NOT NULL DEFAULT 0,
  `team_id`         bigint unsigned DEFAULT NULL,
  `uuid`            varchar(36) NOT NULL DEFAULT '',
  `title`           varchar(200) NOT NULL DEFAULT '',
  `genre`           varchar(64) NOT NULL DEFAULT ''   COMMENT '漫画题材',
  `synopsis`        text,
  `target_platform` varchar(64) NOT NULL DEFAULT '',
  `cover_image_url` varchar(2048) NOT NULL DEFAULT '',
  `settings_json`   json DEFAULT NULL,
  `status`          tinyint NOT NULL DEFAULT 1,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_team` (`team_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_ecom_project` (
  `id`               bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`       datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`       datetime DEFAULT NULL,
  `uid`              int NOT NULL DEFAULT 0,
  `team_id`          bigint unsigned DEFAULT NULL,
  `uuid`             varchar(36) NOT NULL DEFAULT '',
  `title`            varchar(200) NOT NULL DEFAULT '',
  `synopsis`         text,
  `target_platform`  varchar(64) NOT NULL DEFAULT '',
  `cover_image_url`  varchar(2048) NOT NULL DEFAULT '',
  `settings_json`    json DEFAULT NULL,
  `status`           tinyint NOT NULL DEFAULT 1,
  `brand_profile_id` bigint unsigned DEFAULT NULL COMMENT 'FK → w_ad_brand_profile.id (电商活动可绑定品牌资产)',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_uuid` (`uuid`),
  KEY `idx_uid_status` (`uid`, `status`),
  KEY `idx_team` (`team_id`),
  KEY `idx_brand_profile` (`brand_profile_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 数据搬迁：按 solution_type 把 w_project 的行送到对应新表。
-- 保留 id 以保证子表 project_id 外键引用语义不变。
-- dev 当前 w_project 0 行，下面 4 条 INSERT 都是 no-op；prod 同条
-- migration 时会移动数据。

INSERT IGNORE INTO `w_drama_project`
  (id, created_at, updated_at, deleted_at, uid, team_id, uuid, title, genre,
   synopsis, target_platform, cover_image_url, settings_json, status,
   aspect_ratio, style_preset, episode_count, episode_duration,
   outline_generated_at, outline_version)
SELECT id, created_at, updated_at, deleted_at, uid, team_id, uuid, title, genre,
       synopsis, target_platform, cover_image_url, settings_json, status,
       aspect_ratio, style_preset, episode_count, episode_duration,
       outline_generated_at, outline_version
FROM `w_project` WHERE `solution_type` = 'short_drama';

INSERT IGNORE INTO `w_ad_project`
  (id, created_at, updated_at, deleted_at, uid, team_id, uuid, title,
   synopsis, cover_image_url, settings_json, status,
   brand_profile_id, objective, target_platforms_json, target_audience)
SELECT id, created_at, updated_at, deleted_at, uid, team_id, uuid, title,
       synopsis, cover_image_url, settings_json, status,
       brand_profile_id, objective, target_platforms_json, target_audience
FROM `w_project` WHERE `solution_type` = 'video_ad';

INSERT IGNORE INTO `w_comic_project`
  (id, created_at, updated_at, deleted_at, uid, team_id, uuid, title, genre,
   synopsis, target_platform, cover_image_url, settings_json, status)
SELECT id, created_at, updated_at, deleted_at, uid, team_id, uuid, title, genre,
       synopsis, target_platform, cover_image_url, settings_json, status
FROM `w_project` WHERE `solution_type` = 'comic_video';

INSERT IGNORE INTO `w_ecom_project`
  (id, created_at, updated_at, deleted_at, uid, team_id, uuid, title,
   synopsis, target_platform, cover_image_url, settings_json, status,
   brand_profile_id)
SELECT id, created_at, updated_at, deleted_at, uid, team_id, uuid, title,
       synopsis, target_platform, cover_image_url, settings_json, status,
       brand_profile_id
FROM `w_project` WHERE `solution_type` = 'ecommerce';

-- 老表退役。子表的 project_id 列内容不变（id 已被新表保留），
-- FK 语义改为指向对应方案的新表。
DROP TABLE `w_project`;
