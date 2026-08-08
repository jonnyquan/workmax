-- R3-b · Split w_storyboard into per-solution dedicated tables.
--
-- 原 w_storyboard 用 solution_type discriminator 给 drama + comic 双
-- 写。R3-b 改为各自一张表（comic_to_storyboard upsert 走 comic 表，
-- short-drama 全套 storyboard 路径走 drama 表）。solution_type 列移除。
--
-- 同步改动（详见 PR）：
--   server/model/storyboard.go 减肥到只剩 StoryboardStatus* 常量
--   server/model/drama_storyboard.go / comic_storyboard.go 新建
--   server/api/pro/tools/short_drama_*.go 改 model.DramaStoryboard
--   server/api/pro/tools/comic_to_storyboard_api.go 改 model.ComicStoryboard
--
-- dev 当前 w_storyboard 0 行，INSERT FROM 是 no-op；prod 同条 migration
-- 时按 solution_type 把行分到对应表，id 保留。

CREATE TABLE IF NOT EXISTS `w_drama_storyboard` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`         datetime DEFAULT NULL,
  `uid`                int NOT NULL DEFAULT 0,
  `project_id`         bigint unsigned NOT NULL          COMMENT 'FK → w_drama_project.id',
  `episode_id`         bigint unsigned NOT NULL          COMMENT 'FK → w_drama_episode.id',
  `script_id`          bigint unsigned NOT NULL          COMMENT 'FK → w_drama_script.id',
  `script_version`     int NOT NULL DEFAULT 0            COMMENT '生成时脚本版本号',
  `panels_json`        mediumtext                        COMMENT '分镜面板JSON',
  `panel_count`        int NOT NULL DEFAULT 0,
  `status`             tinyint NOT NULL DEFAULT 1        COMMENT '1=draft 2=reviewed 3=locked',
  `locked_at`          datetime DEFAULT NULL,
  `generation_version` int NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_episode` (`episode_id`, `deleted_at`),
  KEY `idx_project_uid` (`project_id`, `uid`),
  KEY `idx_script` (`script_id`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS `w_comic_storyboard` (
  `id`                 bigint unsigned NOT NULL AUTO_INCREMENT,
  `created_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP,
  `updated_at`         datetime NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
  `deleted_at`         datetime DEFAULT NULL,
  `uid`                int NOT NULL DEFAULT 0,
  `project_id`         bigint unsigned NOT NULL          COMMENT 'FK → w_comic_project.id',
  `episode_id`         bigint unsigned NOT NULL          COMMENT '在漫剧场景下作为 chapter_id 复用,FK → w_comic_chapter.id',
  `script_id`          bigint unsigned NOT NULL DEFAULT 0 COMMENT '漫剧从 comic-import 走的话 script_id=0',
  `script_version`     int NOT NULL DEFAULT 0,
  `panels_json`        mediumtext,
  `panel_count`        int NOT NULL DEFAULT 0,
  `status`             tinyint NOT NULL DEFAULT 1,
  `locked_at`          datetime DEFAULT NULL,
  `generation_version` int NOT NULL DEFAULT 0,
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_episode` (`episode_id`, `deleted_at`),
  KEY `idx_project_uid` (`project_id`, `uid`),
  KEY `idx_deleted_at` (`deleted_at`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

INSERT IGNORE INTO `w_drama_storyboard`
  (id, created_at, updated_at, deleted_at, uid, project_id, episode_id,
   script_id, script_version, panels_json, panel_count, status, locked_at,
   generation_version)
SELECT id, created_at, updated_at, deleted_at, uid, project_id, episode_id,
       script_id, script_version, panels_json, panel_count, status, locked_at,
       generation_version
FROM `w_storyboard` WHERE `solution_type` = 'short_drama';

INSERT IGNORE INTO `w_comic_storyboard`
  (id, created_at, updated_at, deleted_at, uid, project_id, episode_id,
   script_id, script_version, panels_json, panel_count, status, locked_at,
   generation_version)
SELECT id, created_at, updated_at, deleted_at, uid, project_id, episode_id,
       script_id, script_version, panels_json, panel_count, status, locked_at,
       generation_version
FROM `w_storyboard` WHERE `solution_type` = 'comic_video';

DROP TABLE `w_storyboard`;
