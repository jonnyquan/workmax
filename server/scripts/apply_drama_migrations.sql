-- SD-S0/S1/S2 + T1.A + T3 + T4 — combined apply for the 10 new drama migrations
-- (20260582 → 20260591). Idempotent: safe to re-run; uses dynamic
-- SQL guarded by information_schema checks so already-applied
-- columns / tables / indexes are skipped.
--
-- USE WITH CARE on production. Recommended order:
--   1. Run check_drama_migrations.sql first to see what's missing.
--   2. Take a quick `mysqldump` snapshot of the affected tables
--      (w_drama_character, w_drama_location, w_drama_panel_shot,
--       w_drama_project, w_drama_episode, w_drama_template,
--       w_generation_task) before running.
--   3. Run this file in a single session.
--   4. Re-run check_drama_migrations.sql — every row should be "OK".
--
-- Each migration body is wrapped in a procedure so we can do
-- conditional ALTER without relying on MySQL 8.0+ `IF NOT EXISTS`
-- on column-add (which 8.0.29+ supports but we don't want to bind to).

DELIMITER $$

-- 20260582: anchors versioning + per-shot anchor snapshots

DROP PROCEDURE IF EXISTS sd_apply_20260582 $$
CREATE PROCEDURE sd_apply_20260582()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'anchors_version'
  ) THEN
    ALTER TABLE `w_drama_character`
      ADD COLUMN `anchors_version` INT NOT NULL DEFAULT 1
        COMMENT 'monotonic counter; +1 on identity_anchors / negative_anchors / appearance change';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_location'
      AND COLUMN_NAME = 'anchors_version'
  ) THEN
    ALTER TABLE `w_drama_location`
      ADD COLUMN `anchors_version` INT NOT NULL DEFAULT 1
        COMMENT 'monotonic counter; +1 on identity_anchors_json / negative_anchors_json / description change';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'anchors_snapshot_json'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `anchors_snapshot_json` JSON DEFAULT NULL
        COMMENT 'snapshot of character + location anchors used at render time';
  END IF;
END $$

-- 20260583: drama_episode_activity append-only log

DROP PROCEDURE IF EXISTS sd_apply_20260583 $$
CREATE PROCEDURE sd_apply_20260583()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_episode_activity'
  ) THEN
    CREATE TABLE `w_drama_episode_activity` (
      `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
      `episode_id`   BIGINT UNSIGNED NOT NULL,
      `project_id`   BIGINT UNSIGNED NOT NULL,
      `actor_uid`    INT             NOT NULL DEFAULT 0,
      `action`       VARCHAR(64)     NOT NULL,
      `target_type`  VARCHAR(32)     NOT NULL DEFAULT '',
      `target_id`    VARCHAR(64)     NOT NULL DEFAULT '',
      `diff_json`    JSON            DEFAULT NULL,
      `desc_text`    VARCHAR(500)    NOT NULL DEFAULT '',
      `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
      PRIMARY KEY (`id`),
      KEY `idx_episode_created` (`episode_id`, `created_at`),
      KEY `idx_project_created` (`project_id`, `created_at`),
      KEY `idx_actor`           (`actor_uid`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='短剧 episode 级 append-only 变更流水';
  END IF;
END $$

-- 20260584: credits_reserved_pending on project

DROP PROCEDURE IF EXISTS sd_apply_20260584 $$
CREATE PROCEDURE sd_apply_20260584()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_project'
      AND COLUMN_NAME = 'credits_reserved_pending'
  ) THEN
    ALTER TABLE `w_drama_project`
      ADD COLUMN `credits_reserved_pending` INT NOT NULL DEFAULT 0
        COMMENT 'in-flight credit reservations from concurrent budget gates';
  END IF;
END $$

-- 20260585: heartbeat_at on generation_task + composite index

DROP PROCEDURE IF EXISTS sd_apply_20260585 $$
CREATE PROCEDURE sd_apply_20260585()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_generation_task'
      AND COLUMN_NAME = 'heartbeat_at'
  ) THEN
    ALTER TABLE `w_generation_task`
      ADD COLUMN `heartbeat_at` DATETIME DEFAULT NULL
        COMMENT 'last heartbeat from worker';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_generation_task'
      AND INDEX_NAME = 'idx_status_heartbeat'
  ) THEN
    ALTER TABLE `w_generation_task`
      ADD INDEX `idx_status_heartbeat` (`status`, `heartbeat_at`);
  END IF;
END $$

-- 20260586: editing claim columns on episode

DROP PROCEDURE IF EXISTS sd_apply_20260586 $$
CREATE PROCEDURE sd_apply_20260586()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'editing_by_uid'
  ) THEN
    ALTER TABLE `w_drama_episode`
      ADD COLUMN `editing_by_uid` INT NOT NULL DEFAULT 0
        COMMENT 'uid currently editing; 0 = no claim';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'editing_expires_at'
  ) THEN
    ALTER TABLE `w_drama_episode`
      ADD COLUMN `editing_expires_at` DATETIME DEFAULT NULL
        COMMENT 'when the editing claim expires';
  END IF;
END $$

-- 20260587: team_id on user templates

DROP PROCEDURE IF EXISTS sd_apply_20260587 $$
CREATE PROCEDURE sd_apply_20260587()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_template'
      AND COLUMN_NAME = 'team_id'
  ) THEN
    ALTER TABLE `w_drama_template`
      ADD COLUMN `team_id` BIGINT UNSIGNED DEFAULT NULL
        COMMENT 'team scope for user templates; NULL = solo';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_template'
      AND INDEX_NAME = 'idx_team'
  ) THEN
    ALTER TABLE `w_drama_template`
      ADD INDEX `idx_team` (`team_id`);
  END IF;
END $$

-- 20260588: character family table + family_id link

DROP PROCEDURE IF EXISTS sd_apply_20260588 $$
CREATE PROCEDURE sd_apply_20260588()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character_family'
  ) THEN
    CREATE TABLE `w_drama_character_family` (
      `id`                  BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
      `uid`                 INT             NOT NULL DEFAULT 0,
      `slug`                VARCHAR(120)    NOT NULL DEFAULT '',
      `name`                VARCHAR(120)    NOT NULL DEFAULT '',
      `description`         VARCHAR(500)    NOT NULL DEFAULT '',
      `default_anchors_json` JSON           DEFAULT NULL,
      `default_negative_json` JSON          DEFAULT NULL,
      `default_voice_preset` VARCHAR(64)    NOT NULL DEFAULT '',
      `default_avatar_url`  VARCHAR(2048)   NOT NULL DEFAULT '',
      `gender`              VARCHAR(32)     NOT NULL DEFAULT '',
      `age_range`           VARCHAR(32)     NOT NULL DEFAULT '',
      `version`             INT             NOT NULL DEFAULT 1,
      `status`              TINYINT         NOT NULL DEFAULT 1,
      `created_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
      `updated_at`          DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
      `deleted_at`          DATETIME        DEFAULT NULL,
      PRIMARY KEY (`id`),
      UNIQUE KEY `uk_uid_slug` (`uid`, `slug`),
      KEY `idx_status` (`status`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COMMENT='短剧跨项目角色族';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'family_id'
  ) THEN
    ALTER TABLE `w_drama_character`
      ADD COLUMN `family_id` BIGINT UNSIGNED DEFAULT NULL
        COMMENT 'optional link to w_drama_character_family.id';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character'
      AND INDEX_NAME = 'idx_family'
  ) THEN
    ALTER TABLE `w_drama_character`
      ADD INDEX `idx_family` (`family_id`);
  END IF;
END $$

-- 20260589: panel-shot kind discriminator + image_url for previews

DROP PROCEDURE IF EXISTS sd_apply_20260589 $$
CREATE PROCEDURE sd_apply_20260589()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'kind'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `kind` VARCHAR(16) NOT NULL DEFAULT 'video'
        COMMENT 'video|preview - 区分视频候选与图片预览';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'image_url'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `image_url` VARCHAR(2048) NOT NULL DEFAULT ''
        COMMENT 'kind=preview 时的图片资源URL';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND INDEX_NAME = 'idx_panel_kind'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD INDEX `idx_panel_kind` (`storyboard_id`, `panel_uuid`, `kind`);
  END IF;
END $$

-- 20260590: T3 panel-shot approval + episode published_urls

DROP PROCEDURE IF EXISTS sd_apply_20260590 $$
CREATE PROCEDURE sd_apply_20260590()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'approval'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `approval` TINYINT NOT NULL DEFAULT 0
        COMMENT 'T3 反馈: -1=差 / 0=未评 / 1=好';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'approval_by_uid'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `approval_by_uid` INT NOT NULL DEFAULT 0
        COMMENT '最后一次评分的用户 uid';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'approval_at'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `approval_at` DATETIME DEFAULT NULL
        COMMENT '最后一次评分时间';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND INDEX_NAME = 'idx_panel_approval'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD INDEX `idx_panel_approval` (`storyboard_id`, `panel_uuid`, `approval`);
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_episode'
      AND COLUMN_NAME = 'published_urls_json'
  ) THEN
    ALTER TABLE `w_drama_episode`
      ADD COLUMN `published_urls_json` JSON DEFAULT NULL
        COMMENT 'T3 发布去向: [{platform, url, postedAt, note}]';
  END IF;
END $$

-- 20260591: T4 appearance_hash on character + location

DROP PROCEDURE IF EXISTS sd_apply_20260591 $$
CREATE PROCEDURE sd_apply_20260591()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character'
      AND COLUMN_NAME = 'appearance_hash'
  ) THEN
    ALTER TABLE `w_drama_character`
      ADD COLUMN `appearance_hash` CHAR(16) NOT NULL DEFAULT ''
        COMMENT 'T4 视觉特征摘要; 影响 stale-shot 判定';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_location'
      AND COLUMN_NAME = 'appearance_hash'
  ) THEN
    ALTER TABLE `w_drama_location`
      ADD COLUMN `appearance_hash` CHAR(16) NOT NULL DEFAULT ''
        COMMENT 'T4 视觉特征摘要';
  END IF;
END $$

-- 20260592: U2 per-take script_generation_version on panel-shot rows

DROP PROCEDURE IF EXISTS sd_apply_20260592 $$
CREATE PROCEDURE sd_apply_20260592()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_panel_shot'
      AND COLUMN_NAME = 'script_generation_version'
  ) THEN
    ALTER TABLE `w_drama_panel_shot`
      ADD COLUMN `script_generation_version` INT NOT NULL DEFAULT 0
        COMMENT 'U2 渲染时脚本generation_version快照; 0=未知/legacy';
  END IF;
END $$

-- 20260593: U3 script revision history table

DROP PROCEDURE IF EXISTS sd_apply_20260593 $$
CREATE PROCEDURE sd_apply_20260593()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.TABLES
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_script_revision'
  ) THEN
    CREATE TABLE `w_drama_script_revision` (
      `id`           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
      `episode_id`   BIGINT UNSIGNED NOT NULL,
      `uid`          INT             NOT NULL DEFAULT 0,
      `version`      INT             NOT NULL DEFAULT 1,
      `scenes_json`  MEDIUMTEXT,
      `scene_count`  INT             NOT NULL DEFAULT 0,
      `source`       VARCHAR(32)     NOT NULL DEFAULT ''
                      COMMENT 'ai_generate | manual_save | restore',
      `created_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
      `updated_at`   DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP
                      ON UPDATE CURRENT_TIMESTAMP,
      PRIMARY KEY (`id`),
      UNIQUE KEY `uk_episode_revision` (`episode_id`, `version`),
      KEY `idx_uid` (`uid`)
    ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
      COMMENT='短剧脚本版本历史; 每次写入插入一行';
  END IF;
END $$

-- 20260594: P2 character-family team scoping

DROP PROCEDURE IF EXISTS sd_apply_20260594 $$
CREATE PROCEDURE sd_apply_20260594()
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.COLUMNS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character_family'
      AND COLUMN_NAME = 'team_id'
  ) THEN
    ALTER TABLE `w_drama_character_family`
      ADD COLUMN `team_id` BIGINT UNSIGNED DEFAULT NULL
        COMMENT 'P2 可选团队共享id; NULL=仅 owner 可见';
  END IF;

  IF NOT EXISTS (
    SELECT 1 FROM information_schema.STATISTICS
    WHERE TABLE_SCHEMA = DATABASE()
      AND TABLE_NAME = 'w_drama_character_family'
      AND INDEX_NAME = 'idx_family_team'
  ) THEN
    ALTER TABLE `w_drama_character_family`
      ADD INDEX `idx_family_team` (`team_id`);
  END IF;
END $$

DELIMITER ;

CALL sd_apply_20260582();
CALL sd_apply_20260583();
CALL sd_apply_20260584();
CALL sd_apply_20260585();
CALL sd_apply_20260586();
CALL sd_apply_20260587();
CALL sd_apply_20260588();
CALL sd_apply_20260589();
CALL sd_apply_20260590();
CALL sd_apply_20260591();
CALL sd_apply_20260592();
CALL sd_apply_20260593();
CALL sd_apply_20260594();

DROP PROCEDURE IF EXISTS sd_apply_20260582;
DROP PROCEDURE IF EXISTS sd_apply_20260583;
DROP PROCEDURE IF EXISTS sd_apply_20260584;
DROP PROCEDURE IF EXISTS sd_apply_20260585;
DROP PROCEDURE IF EXISTS sd_apply_20260586;
DROP PROCEDURE IF EXISTS sd_apply_20260587;
DROP PROCEDURE IF EXISTS sd_apply_20260588;
DROP PROCEDURE IF EXISTS sd_apply_20260589;
DROP PROCEDURE IF EXISTS sd_apply_20260590;
DROP PROCEDURE IF EXISTS sd_apply_20260591;
DROP PROCEDURE IF EXISTS sd_apply_20260592;
DROP PROCEDURE IF EXISTS sd_apply_20260593;
DROP PROCEDURE IF EXISTS sd_apply_20260594;
